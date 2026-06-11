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

	"golang.org/x/sys/unix"
)

const (
	// TimeSyncCANID is the standard CAN ID for CCU time sync frames (0x5A4).
	TimeSyncCANID = uint32(0x5A4)

	// SyncInterval is the period between time sync frame pairs.
	SyncInterval = 500 * time.Millisecond

	msgType1 = uint8(0x20) // seconds frame
	msgType2 = uint8(0x28) // nanoseconds frame
)

// Sender manages periodic CAN time sync frame transmission.
//
// Frame encoding (8 bytes each, matching the CCU ccu_synctime_500ms_t format):
//
//	Seconds frame (type=0x20):
//	  [0:4] UTC seconds, little-endian uint32
//	  [4]   0x00  (reserved / sgw / ovs)
//	  [5]   (timedomain<<4) | seq
//	  [6]   0x00  (crc, not used by receiver)
//	  [7]   0x20  (type)
//
//	Nanoseconds frame (type=0x28):
//	  [0:4] UTC nanoseconds, little-endian uint32
//	  [4]   ovs   (1 if the second boundary was crossed, else 0)
//	  [5]   (timedomain<<4) | seq  (same seq as the seconds frame)
//	  [6]   0x00
//	  [7]   0x28  (type)
//
// The receiver reverses byte order before parsing; this encoding matches that
// expectation so that reversed[0]=type, reversed[2]&0x0F=seq,
// reversed[4:8] big-endian = value.
type Sender struct {
	mu      sync.Mutex
	enabled bool
	iface   string
	cancel  context.CancelFunc
}

// NewSender returns a new Sender that is initially disabled.
func NewSender() *Sender {
	return &Sender{}
}

// Configure atomically sets the enabled state and CAN interface, restarting the
// background goroutine as needed.
func (s *Sender) Configure(enabled bool, iface string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Cancel any running goroutine.
	if s.cancel != nil {
		s.cancel()
		s.cancel = nil
	}

	s.enabled = enabled
	s.iface = iface

	if enabled && iface != "" {
		ctx, cancel := context.WithCancel(context.Background())
		s.cancel = cancel
		go s.run(ctx, iface)
	}
}

// Status returns the current enabled flag and selected CAN interface.
func (s *Sender) Status() (enabled bool, iface string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.enabled, s.iface
}

// canFDSocket is a minimal blocking CAN-FD socket used exclusively for TX.
// It is deliberately kept in blocking mode so writes block when the kernel TX
// queue is full instead of returning ENOBUFS.
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

	// NOTE: we intentionally do NOT call SetNonblock here. Keeping the socket
	// in blocking mode means unix.Write will block until the kernel TX queue
	// has room, rather than returning EAGAIN/ENOBUFS.

	return &canFDSocket{fd: fd}, nil
}

func (cs *canFDSocket) Close() {
	unix.Close(cs.fd)
}

// canfdMTU is the full size of a Linux canfd_frame (must equal can.CANFD_MTU).
const canfdMTU = 72

// buildCANFDFrame encodes data into a 72-byte canfd_frame.
//
//	Bytes 0-3 : CAN ID (little-endian uint32)
//	Byte  4   : len  (actual payload length, 0-64)
//	Byte  5   : flags (BRS=0x01, ESI=0x02; 0 for plain data rate)
//	Bytes 6-7 : reserved (zero)
//	Bytes 8-71: payload
func buildCANFDFrame(canID uint32, data []byte) [canfdMTU]byte {
	var frame [canfdMTU]byte
	binary.LittleEndian.PutUint32(frame[0:4], canID)
	frame[4] = uint8(len(data)) // actual length (not DLC)
	frame[5] = 0                // no BRS, no ESI
	copy(frame[8:], data)
	return frame
}

// writeFrame sends one CAN-FD frame via the blocking socket.
func (cs *canFDSocket) writeFrame(canID uint32, data []byte) error {
	frame := buildCANFDFrame(canID, data)
	_, err := unix.Write(cs.fd, frame[:])
	return err
}

// run is the background goroutine that sends time sync pairs on the given iface.
func (s *Sender) run(ctx context.Context, iface string) {
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

	log.Printf("timesync: sender started on %s (CAN ID 0x%03X, interval %s)",
		iface, TimeSyncCANID, SyncInterval)
	defer log.Printf("timesync: sender stopped on %s", iface)

	// Close the socket when context is cancelled so the blocking write unblocks.
	go func() {
		<-ctx.Done()
		sock.Close()
	}()

	ticker := time.NewTicker(SyncInterval)
	defer ticker.Stop()

	var seq uint8
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			now := time.Now().UTC()
			seconds := uint32(now.Unix())
			nanoseconds := uint32(now.Nanosecond())

			if err := sendPair(sock, seq, seconds, nanoseconds); err != nil {
				if ctx.Err() != nil {
					return // cancelled, not a real error
				}
				log.Printf("timesync: send error on %s: %v", iface, err)
			}
			seq = (seq + 1) & 0x0F
		}
	}
}

// buildPayload1 encodes the UTC-seconds payload (type=0x20, 8 bytes).
func buildPayload1(seq uint8, seconds uint32) []byte {
	data := make([]byte, 8)
	binary.LittleEndian.PutUint32(data[0:4], seconds)
	data[4] = 0x00       // reserved / sgw=0 / ovs=0
	data[5] = seq & 0x0F // timedomain(4b)=0 | seq(4b)
	data[6] = 0x00       // crc (unused by receiver)
	data[7] = msgType1
	return data
}

// buildPayload2 encodes the UTC-nanoseconds payload (type=0x28, 8 bytes).
func buildPayload2(seq uint8, nanoseconds uint32) []byte {
	data := make([]byte, 8)
	binary.LittleEndian.PutUint32(data[0:4], nanoseconds)
	data[4] = 0x00       // ovs=0
	data[5] = seq & 0x0F // same seq as frame1
	data[6] = 0x00
	data[7] = msgType2
	return data
}

func sendPair(sock *canFDSocket, seq uint8, seconds uint32, nanoseconds uint32) error {
	if err := sock.writeFrame(TimeSyncCANID, buildPayload1(seq, seconds)); err != nil {
		return err
	}
	// 5 ms gap between the two frames in a pair (matches typical CCU behaviour).
	time.Sleep(5 * time.Millisecond)
	return sock.writeFrame(TimeSyncCANID, buildPayload2(seq, nanoseconds))
}
