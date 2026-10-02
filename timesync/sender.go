//go:build linux

package timesync

import (
	"context"
	"encoding/binary"
	"fmt"
	"log"
	"net"
	"sync"
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
	brs      bool // send frames with CAN-FD Bit Rate Switch
	cancel   context.CancelFunc
}

// NewSender returns a new Sender that is initially disabled.
func NewSender() *Sender {
	return &Sender{protocol: Protocol5A4}
}

// SetDefaultBRS sets the BRS state used until the next Configure call.
func (s *Sender) SetDefaultBRS(brs bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.brs = brs
}

// Configure atomically sets the enabled state, CAN interface, protocol and
// CAN-FD Bit Rate Switch, restarting the background goroutine as needed.
func (s *Sender) Configure(enabled bool, iface string, protocol Protocol, brs bool) {
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
	s.brs = brs

	if enabled && iface != "" {
		ctx, cancel := context.WithCancel(context.Background())
		s.cancel = cancel
		go s.run(ctx, iface, protocol, brs)
	}
}

// Status returns the current enabled flag, selected CAN interface, protocol
// and BRS setting.
func (s *Sender) Status() (enabled bool, iface string, protocol Protocol, brs bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.enabled, s.iface, s.protocol, s.brs
}

// canTxSocket is a minimal raw CAN socket used exclusively for TX. It writes
// CAN-FD frames on interfaces configured with "fd on" (MTU 72) and classic
// CAN frames otherwise.
type canTxSocket struct {
	fd     int
	fdMode bool // interface MTU is CAN-FD
	brs    bool // set CANFD_BRS on every transmitted CAN-FD frame
}

// openCANTxSocket opens a TX-only raw CAN socket bound to ifaceName.
func openCANTxSocket(ifaceName string, brs bool) (*canTxSocket, error) {
	iface, err := net.InterfaceByName(ifaceName)
	if err != nil {
		return nil, fmt.Errorf("interface %s: %w", ifaceName, err)
	}
	if (iface.Flags & net.FlagUp) == 0 {
		return nil, fmt.Errorf("interface %s is down", ifaceName)
	}

	var fdMode bool
	switch iface.MTU {
	case can.CANFD_MTU:
		fdMode = true
	case can.CAN_MTU:
		fdMode = false
		if brs {
			log.Printf("timesync: %s is not in CAN-FD mode, sending classic CAN frames without BRS", ifaceName)
			brs = false
		}
	default:
		return nil, fmt.Errorf("interface %s: unexpected MTU %d", ifaceName, iface.MTU)
	}

	fd, err := unix.Socket(unix.AF_CAN, unix.SOCK_RAW, unix.CAN_RAW)
	if err != nil {
		return nil, fmt.Errorf("socket: %w", err)
	}

	if fdMode {
		if err := unix.SetsockoptInt(fd, unix.SOL_CAN_RAW, unix.CAN_RAW_FD_FRAMES, 1); err != nil {
			unix.Close(fd)
			return nil, fmt.Errorf("setsockopt CAN_RAW_FD_FRAMES: %w", err)
		}
	}

	// Receive nothing – this socket is TX-only.
	if err := unix.SetsockoptCanRawFilter(fd, unix.SOL_CAN_RAW, unix.CAN_RAW_FILTER, []unix.CanFilter{}); err != nil {
		log.Printf("timesync: warning: failed to disable RX filter: %v", err)
	}

	// Never block for long on a congested bus; a timed-out write returns
	// EAGAIN and the frame is dropped.
	tv := unix.NsecToTimeval(int64(100 * time.Millisecond))
	if err := unix.SetsockoptTimeval(fd, unix.SOL_SOCKET, unix.SO_SNDTIMEO, &tv); err != nil {
		log.Printf("timesync: warning: failed to set SO_SNDTIMEO: %v", err)
	}

	if err := unix.Bind(fd, &unix.SockaddrCAN{Ifindex: iface.Index}); err != nil {
		unix.Close(fd)
		return nil, fmt.Errorf("bind: %w", err)
	}

	return &canTxSocket{fd: fd, fdMode: fdMode, brs: brs}, nil
}

func (cs *canTxSocket) Close() {
	unix.Close(cs.fd)
}

// buildFrame encodes data into a SocketCAN frame and returns the bytes to
// write: a 72-byte canfd_frame (with BRS if requested, so the data phase is
// transmitted at the interface's dbitrate) or a 16-byte classic can_frame.
func buildFrame(canID uint32, data []byte, fdMode, brs bool) ([can.CANFD_MTU]byte, int) {
	var frame [can.CANFD_MTU]byte
	binary.LittleEndian.PutUint32(frame[0:4], canID)
	frame[4] = uint8(len(data)) // actual length
	if fdMode && brs {
		frame[5] = can.CANFD_BRS
	}
	copy(frame[8:], data)
	if fdMode {
		return frame, can.CANFD_MTU
	}
	return frame, can.CAN_MTU
}

// writeFrame sends one frame. ENOBUFS/EAGAIN mean the device TX queue is
// full (bus congestion or no ACK from any node); the frame is dropped
// silently so we never block or spam the log.
func (cs *canTxSocket) writeFrame(canID uint32, data []byte) error {
	frame, n := buildFrame(canID, data, cs.fdMode, cs.brs)
	_, err := unix.Write(cs.fd, frame[:n])
	if err == unix.ENOBUFS || err == unix.EAGAIN {
		return nil
	}
	return err
}

// run is the background goroutine that sends time sync pairs on the given
// iface until ctx is cancelled. The socket is (re)opened as needed, so the
// sender survives the interface being down at startup or reconfigured.
func (s *Sender) run(ctx context.Context, iface string, protocol Protocol, brs bool) {
	canID := canIDForProtocol(protocol)
	log.Printf("timesync: sender started on %s (protocol %s, CAN ID 0x%03X, BRS %v, interval %s)",
		iface, protocol, canID, brs, SyncInterval)
	defer log.Printf("timesync: sender stopped on %s", iface)

	var lastOpenErr string
	for {
		sock, err := openCANTxSocket(iface, brs)
		if err != nil {
			if err.Error() != lastOpenErr {
				log.Printf("timesync: cannot open %s, retrying: %v", iface, err)
				lastOpenErr = err.Error()
			}
		} else {
			lastOpenErr = ""
			s.sendLoop(ctx, sock, iface, canID, protocol)
			sock.Close()
		}

		select {
		case <-ctx.Done():
			return
		case <-time.After(2 * time.Second):
		}
	}
}

// sendLoop sends time sync pairs until ctx is cancelled or the socket fails.
func (s *Sender) sendLoop(ctx context.Context, sock *canTxSocket, iface string, canID uint32, protocol Protocol) {
	ticker := time.NewTicker(SyncInterval)
	defer ticker.Stop()

	var seq uint8
	unhealthyCount := 0
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}

		now := time.Now().UTC()
		seconds := uint32(now.Unix())
		nanoseconds := uint32(now.Nanosecond())

		if err := sendPair(ctx, sock, canID, protocol, seq, seconds, nanoseconds); err != nil {
			if ctx.Err() != nil {
				return
			}
			// Socket-level errors (interface down, MTU changed, ...):
			// reopen the socket, which re-reads the interface mode.
			log.Printf("timesync: send error on %s, reopening socket: %v", iface, err)
			return
		}
		seq = (seq + 1) & 0x0F

		healthy, state, err := can.InterfaceHealthy(iface)
		switch {
		case err != nil:
			log.Printf("timesync: health check failed on %s: %v", iface, err)
			unhealthyCount++
		case !healthy:
			log.Printf("timesync: interface %s is %s", iface, state)
			unhealthyCount++
		default:
			unhealthyCount = 0
		}

		if unhealthyCount > 3 {
			log.Printf("timesync: %s unhealthy %d times in a row, restarting interface", iface, unhealthyCount)
			if err := can.RestartInterface(iface); err != nil {
				log.Printf("timesync: interface restart failed on %s: %v", iface, err)
			}
			unhealthyCount = 0
			return
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

func sendPair(ctx context.Context, sock *canTxSocket, canID uint32, protocol Protocol, seq uint8, seconds uint32, nanoseconds uint32) error {
	if err := sock.writeFrame(canID, buildPayload(msgType1, seq, seconds, protocol)); err != nil {
		return err
	}
	// 5 ms gap between the two frames in a pair (matches typical CCU behaviour).
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(5 * time.Millisecond):
	}
	return sock.writeFrame(canID, buildPayload(msgType2, seq, nanoseconds, protocol))
}
