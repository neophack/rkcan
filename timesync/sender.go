//go:build linux

package timesync

import (
	"context"
	"encoding/binary"
	"fmt"
	"log"
	"net"
	"sync"
	"syscall"
	"time"

	"github.com/penghongxia/rkcan/can"
	"golang.org/x/sys/unix"
)

const (
	// SyncInterval is the period between time sync frame pairs.
	SyncInterval = 500 * time.Millisecond

	msgType1 = uint8(0x20) // seconds frame
	msgType2 = uint8(0x28) // nanoseconds frame
)

// Protocol selects the time sync message format.
type Protocol string

const (
	// Protocol5A4 is the EEA2.1 format: ADAS_SYNCTime_500ms on CAN ID 0x5A4.
	// Byte 2 (after reversal): high-nibble = TimeDomain, low-nibble = SequenceCnt.
	Protocol5A4 Protocol = "5A4"

	// Protocol594 is the EEA3.0 format: CCU_SYNCTime_500ms on CAN ID 0x594.
	// Byte 2 (after reversal): high-nibble = SequenceCnt, low-nibble = TimeDomain.
	Protocol594 Protocol = "594"
)

// canIDForProtocol returns the CAN ID for the given protocol.
func canIDForProtocol(p Protocol) uint32 {
	if p == Protocol594 {
		return 0x594
	}
	return 0x5A4
}

// Sender manages periodic CAN time sync frame transmission.
//
// Frame encoding (8 bytes each, Big-Endian / Motorola, matching DBC @0+):
//
//	Byte 0 : Type  (0x20 = seconds frame, 0x28 = nanoseconds frame)
//	Byte 1 : CRC   (0x00, unused)
//	Byte 2 : Protocol5A4 (EEA2.1): [TimeDomain(7:4) | SequenceCnt(3:0)]
//	         Protocol594 (EEA3.0): [SequenceCnt(7:4) | TimeDomain(3:0)]
//	Byte 3 : Protocol5A4: [Reserved(7:3) | SGW(2) | OVS(1:0)] = 0x00
//	         Protocol594: [OVS(7:6) | SGW(5) | Reserved(4:0)] = 0x00
//	Bytes 4-7: SyncTime (UTC seconds or nanoseconds), Big-Endian uint32
type Sender struct {
	mu       sync.Mutex
	enabled  bool
	iface    string
	protocol Protocol
	cancel   context.CancelFunc
}

// NewSender returns a new Sender that is initially disabled.
func NewSender() *Sender {
	return &Sender{protocol: Protocol5A4}
}

// Configure atomically sets the enabled state, CAN interface, and protocol,
// restarting the background goroutine as needed.
func (s *Sender) Configure(enabled bool, iface string, protocol Protocol) {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Cancel any running goroutine.
	if s.cancel != nil {
		s.cancel()
		s.cancel = nil
	}

	if protocol == "" {
		protocol = Protocol5A4
	}

	s.enabled = enabled
	s.iface = iface
	s.protocol = protocol

	if enabled && iface != "" {
		ctx, cancel := context.WithCancel(context.Background())
		s.cancel = cancel
		go s.run(ctx, iface, protocol)
	}
}

// Status returns the current enabled flag, selected CAN interface, and protocol.
func (s *Sender) Status() (enabled bool, iface string, protocol Protocol) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.enabled, s.iface, s.protocol
}

// canFDSocket is a minimal blocking CAN-FD raw socket used exclusively for TX.
type canFDSocket struct {
	fd int
}

// openCANFDSocket opens a blocking CAN-FD raw socket bound to ifaceName.
func openCANFDSocket(ifaceName string) (*canFDSocket, error) {
	iface, err := net.InterfaceByName(ifaceName)
	if err != nil {
		return nil, fmt.Errorf("interface %s: %w", ifaceName, err)
	}
	if (iface.Flags & net.FlagUp) == 0 {
		return nil, fmt.Errorf("interface %s is down", ifaceName)
	}

	fd, err := unix.Socket(unix.AF_CAN, unix.SOCK_RAW, unix.CAN_RAW)
	if err != nil {
		return nil, fmt.Errorf("socket: %w", err)
	}

	// Enable CAN-FD frames (required to write 72-byte canfd_frame).
	if err := syscall.SetsockoptInt(fd, unix.SOL_CAN_RAW, unix.CAN_RAW_FD_FRAMES, 1); err != nil {
		unix.Close(fd)
		return nil, fmt.Errorf("setsockopt CAN_RAW_FD_FRAMES: %w", err)
	}

	// Bind to the specific CAN interface.
	if err := unix.Bind(fd, &unix.SockaddrCAN{Ifindex: iface.Index}); err != nil {
		unix.Close(fd)
		return nil, fmt.Errorf("bind: %w", err)
	}

	// Enlarge send buffer to absorb short bursts without blocking.
	if err := syscall.SetsockoptInt(fd, unix.SOL_SOCKET, unix.SO_SNDBUF, 512*1024); err != nil {
		log.Printf("timesync: warning: failed to set SO_SNDBUF: %v", err)
	}

	// Receive nothing – this socket is TX-only.
	if err := syscall.SetsockoptInt(fd, unix.SOL_CAN_RAW, unix.CAN_RAW_FILTER, 0); err != nil {
		log.Printf("timesync: warning: failed to disable RX filter: %v", err)
	}

	return &canFDSocket{fd: fd}, nil
}

func (cs *canFDSocket) Close() {
	unix.Close(cs.fd)
}

// canfdMTU is the full size of a Linux canfd_frame (72 bytes):
//
//	bytes 0-3 : CAN ID (LE uint32)
//	byte  4   : len (actual payload length, 0-64)
//	byte  5   : flags (BRS=0x01, ESI=0x02)
//	bytes 6-7 : reserved (zero)
//	bytes 8-71: payload
const canfdMTU = 72

// buildCANFDFrame encodes data into a 72-byte canfd_frame.
func buildCANFDFrame(canID uint32, data []byte) [canfdMTU]byte {
	var frame [canfdMTU]byte
	binary.LittleEndian.PutUint32(frame[0:4], canID)
	frame[4] = uint8(len(data)) // actual length
	frame[5] = 0                // no BRS, no ESI
	copy(frame[8:], data)
	return frame
}

// writeFrame sends one CAN-FD frame.
// ENOBUFS means the kernel CAN device TX queue is full (bus congestion or no
// ACK from any node); the frame is silently dropped so we never block or spam
// the log.
func (cs *canFDSocket) writeFrame(canID uint32, data []byte) error {
	frame := buildCANFDFrame(canID, data)
	_, err := unix.Write(cs.fd, frame[:])
	if err == unix.ENOBUFS {
		return nil // TX queue full – drop this frame silently
	}
	return err
}

// run is the background goroutine that sends time sync pairs on the given iface.
func (s *Sender) run(ctx context.Context, iface string, protocol Protocol) {
	sock, err := openCANFDSocket(iface)
	if err != nil {
		log.Printf("timesync: failed to open socket on %s: %v", iface, err)
		s.mu.Lock()
		s.enabled = false
		s.cancel = nil
		s.mu.Unlock()
		return
	}
	defer sock.Close()

	canID := canIDForProtocol(protocol)
	log.Printf("timesync: sender started on %s (protocol %s, CAN ID 0x%03X, interval %s)",
		iface, protocol, canID, SyncInterval)
	defer log.Printf("timesync: sender stopped on %s", iface)

	// Close the socket when context is cancelled so the blocking write unblocks.
	go func() {
		<-ctx.Done()
		sock.Close()
	}()

	ticker := time.NewTicker(SyncInterval)
	defer ticker.Stop()

	var seq uint8
	consecutiveSendFailures := 0
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			now := time.Now().UTC()
			seconds := uint32(now.Unix())
			nanoseconds := uint32(now.Nanosecond())

			if err := sendPair(sock, canID, protocol, seq, seconds, nanoseconds); err != nil {
				if ctx.Err() != nil {
					return // cancelled, not a real error
				}
				consecutiveSendFailures++
				log.Printf("timesync: send error on %s: %v", iface, err)
				if consecutiveSendFailures > 3 {
					log.Printf("timesync: send failed %d times on %s, restarting interface", consecutiveSendFailures, iface)
					if restartErr := can.RestartInterface(iface); restartErr != nil {
						log.Printf("timesync: interface restart failed on %s: %v", iface, restartErr)
					} else {
						log.Printf("timesync: interface restarted on %s after send failures", iface)
						consecutiveSendFailures = 0
					}
				}
			} else {
				healthy, state, healthErr := can.InterfaceHealthy(iface)
				if healthErr != nil {
					consecutiveSendFailures++
					log.Printf("timesync: health check failed on %s: %v", iface, healthErr)
				} else if !healthy {
					consecutiveSendFailures++
					log.Printf("timesync: interface unhealthy on %s after write: %s", iface, state)
				} else {
					consecutiveSendFailures = 0
				}

				if consecutiveSendFailures > 3 {
					log.Printf("timesync: send health check failed %d times on %s, restarting interface", consecutiveSendFailures, iface)
					if restartErr := can.RestartInterface(iface); restartErr != nil {
						log.Printf("timesync: interface restart failed on %s: %v", iface, restartErr)
					} else {
						log.Printf("timesync: interface restarted on %s after health check failures", iface)
						consecutiveSendFailures = 0
					}
				}
			}
			seq = (seq + 1) & 0x0F
		}
	}
}

// byte2 encodes Byte 2 of the frame (seq / timedomain nibbles).
// Protocol5A4 (EEA2.1): bits[7:4]=TimeDomain(0), bits[3:0]=SequenceCnt
// Protocol594 (EEA3.0): bits[7:4]=SequenceCnt,   bits[3:0]=TimeDomain(0)
func byte2(seq uint8, protocol Protocol) uint8 {
	if protocol == Protocol594 {
		return (seq & 0x0F) << 4 // Seq in high nibble, TD=0 in low nibble
	}
	return seq & 0x0F // TD=0 in high nibble, Seq in low nibble
}

// buildPayload encodes an 8-byte SYNCTime frame in Big-Endian format.
//
//	Byte 0: msgType  Byte 1: CRC(0)  Byte 2: seq/td  Byte 3: ovs/sgw/reserved(0)
//	Bytes 4-7: value as Big-Endian uint32
func buildPayload(msgType, seq uint8, value uint32, protocol Protocol) []byte {
	data := make([]byte, 8)
	data[0] = msgType
	data[1] = 0x00 // CRC (unused)
	data[2] = byte2(seq, protocol)
	data[3] = 0x00 // OVS=0, SGW=0, Reserved=0
	binary.BigEndian.PutUint32(data[4:8], value)
	return data
}

func sendPair(sock *canFDSocket, canID uint32, protocol Protocol, seq uint8, seconds uint32, nanoseconds uint32) error {
	if err := sock.writeFrame(canID, buildPayload(msgType1, seq, seconds, protocol)); err != nil {
		return err
	}
	// 5 ms gap between the two frames in a pair (matches typical CCU behaviour).
	time.Sleep(5 * time.Millisecond)
	return sock.writeFrame(canID, buildPayload(msgType2, seq, nanoseconds, protocol))
}
