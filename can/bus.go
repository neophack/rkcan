//go:build linux

package can

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"net"
	"os"
	"sync"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

// min returns the smaller of two integers
func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// Bus represents a CAN-FD bus interface
type Bus struct {
	file                    *os.File
	fd                      int
	ifaceName               string
	sendQueue               chan *Message
	stopSend                chan interface{}
	recvQueue               chan *Message
	running                 bool
	ctx                     context.Context
	cancel                  context.CancelFunc
	shutdownOnce            sync.Once
	lastSendTime            time.Time
	lastHealthCheck         time.Time
	minSendInterval         time.Duration
	healthCheckInterval     time.Duration
	consecutiveSendFailures int
}

// NewBus creates a new CAN-FD bus instance for the specified interface
func NewBus(ifaceName string) (bus *Bus, err error) {
	// Find interface
	iface, err := net.InterfaceByName(ifaceName)
	if err != nil {
		err = fmt.Errorf("interface %s: %w", ifaceName, err)
		return
	}

	// Check if network is up. Can technically still be a race
	// condition after this, but we're just using it for stability
	// purposes in the k8s environment.
	if (iface.Flags & net.FlagUp) == 0 {
		err = fmt.Errorf("interface %s is down", ifaceName)
		return
	}

	// Check if CAN-FD MTU is set
	if iface.MTU != CANFD_MTU {
		err = fmt.Errorf(
			"Expected CAN-FD MTU (%d), got: %d",
			CANFD_MTU, iface.MTU,
		)
		return
	}

	// Open CAN socket fd
	fd, err := unix.Socket(
		unix.AF_CAN,
		unix.SOCK_RAW,
		unix.CAN_RAW,
	)
	if err != nil {
		err = fmt.Errorf("socket: %w", err)
		return
	}

	// Put fd in non-blocking mode, so the created file will be
	// registered by the runtime poller
	// More info: https://morsmachine.dk/netpoller
	if err = unix.SetNonblock(fd, true); err != nil {
		err = fmt.Errorf("set nonblock: %w", err)
		unix.Close(fd)
		return
	}

	// Enable CAN-FD frames
	err = syscall.SetsockoptInt(
		fd,
		unix.SOL_CAN_RAW,
		unix.CAN_RAW_FD_FRAMES,
		1,
	)
	if err != nil {
		err = fmt.Errorf("setsockopt CAN_RAW_FD_FRAMES: %w", err)
		unix.Close(fd)
		return
	}

	// Disable error frames to avoid conflicts
	err = syscall.SetsockoptInt(
		fd,
		unix.SOL_CAN_RAW,
		unix.CAN_RAW_ERR_FILTER,
		0,
	)
	if err != nil {
		err = fmt.Errorf("setsockopt CAN_RAW_ERR_FILTER: %w", err)
		unix.Close(fd)
		return
	}

	// Increase socket receive buffer to prevent kernel-level frame loss
	// under high bus load. Default (~208KB) is too small for burst traffic.
	rcvBufSize := 4 * 1024 * 1024 // 4 MB
	if err = syscall.SetsockoptInt(fd, unix.SOL_SOCKET, unix.SO_RCVBUF, rcvBufSize); err != nil {
		log.Printf("Warning: failed to set CAN socket SO_RCVBUF to %d: %v", rcvBufSize, err)
	}

	// Bind socket to actual interface
	err = unix.Bind(fd, &unix.SockaddrCAN{Ifindex: iface.Index})
	if err != nil {
		err = fmt.Errorf("bind: %w", err)
		unix.Close(fd)
		return
	}

	ctx, cancel := context.WithCancel(context.Background())
	file := os.NewFile(uintptr(fd), ifaceName)
	bus = &Bus{
		file:                file,
		fd:                  fd,
		ifaceName:           ifaceName,
		sendQueue:           make(chan *Message, 10000),
		stopSend:            make(chan interface{}),
		running:             true,
		recvQueue:           make(chan *Message, 200000),
		ctx:                 ctx,
		cancel:              cancel,
		minSendInterval:     500 * time.Microsecond, // Minimum 500µs between sends
		healthCheckInterval: 250 * time.Millisecond,
	}

	// Start receiving and sending loops
	go bus.recvLoop(ctx)
	go bus.sendLoop(ctx)

	return
}

// Shutdown gracefully shuts down the CAN bus
func (b *Bus) Shutdown() {
	b.shutdownOnce.Do(func() {
		b.cancel()
		b.running = false
		if b.file != nil {
			// Shut down the socket first to unblock any pending Read/Write
			unix.Shutdown(b.fd, unix.SHUT_RDWR)
			b.file.Close()
		}
		// Close recvQueue so consumers can exit cleanly
		close(b.recvQueue)
	})
}

// ResetFilters resets CAN filters to allow all messages
func (b *Bus) ResetFilters() (err error) {
	allow_all := unix.CanFilter{
		Id:   0,
		Mask: 0,
	}
	err = b.SetFilters(&[]unix.CanFilter{allow_all})
	return
}

// SetFilters sets CAN ID filters for message reception
func (b *Bus) SetFilters(filters *[]unix.CanFilter) (err error) {
	// Set CAN filters
	err = unix.SetsockoptCanRawFilter(
		b.fd,
		unix.SOL_CAN_RAW,
		unix.CAN_RAW_FILTER,
		*filters,
	)
	if err != nil {
		err = fmt.Errorf("setsockopt CAN_RAW_FILTER: %w", err)
		return
	}

	return
}

// SendQueue returns the channel for sending messages
func (b *Bus) SendQueue() chan<- *Message {
	return b.sendQueue
}

// RecvQueue returns the channel for receiving messages
func (b *Bus) RecvQueue() <-chan *Message {
	return b.recvQueue
}

// Send sends a CAN message synchronously
func (b *Bus) Send(msg *Message) error {
	if !b.running {
		return fmt.Errorf("bus is not running")
	}

	select {
	case b.sendQueue <- msg:
		return nil
	case <-b.ctx.Done():
		return fmt.Errorf("bus is shutting down")
	}
}

// SendNonBlocking sends a CAN message without blocking
func (b *Bus) SendNonBlocking(msg *Message) error {
	if !b.running {
		return fmt.Errorf("bus is not running")
	}

	select {
	case b.sendQueue <- msg:
		return nil
	default:
		return fmt.Errorf("send queue is full")
	}
}

// recvLoop handles receiving CAN-FD frames
func (b *Bus) recvLoop(ctx context.Context) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("CAN receive loop panic recovered: %v", r)
		}
	}()

	var frame [CANFD_MTU]byte

	for b.running {
		select {
		case <-ctx.Done():
			return
		default:
		}

		n, err := b.file.Read(frame[:])

		if err != nil {
			if errors.Is(err, os.ErrClosed) {
				return
			}
			// Fatal errors (e.g. interface down) should terminate the recvLoop
			// so that the caller can recreate the bus.
			var errno syscall.Errno
			if errors.As(err, &errno) {
				if errno == syscall.ENETDOWN || errno == syscall.ENETRESET || errno == syscall.ECONNRESET {
					log.Printf("CAN interface error on %s: %v — shutting down bus", b.GetInterfaceName(), err)
					b.Shutdown()
					return
				}
			}
			switch err.(type) {
			case *fs.PathError:
				// File closed, normal shutdown
				return
			default:
				log.Printf("CAN read error: %v", err)
				continue
			}
		}

		// If we stopped while waiting, quit
		if !b.running {
			return
		}

		// If we didn't receive anything, continue
		if n == 0 {
			continue
		}

		msg := new(Message)
		if err := msg.Unmarshal(frame[:n]); err != nil {
			log.Printf("Invalid CAN Message: %v", err)
			continue
		}

		// Block until message is queued to guarantee zero frame loss
		select {
		case b.recvQueue <- msg:
		case <-ctx.Done():
			return
		}
	}
}

// sendLoop handles sending CAN-FD frames
func (b *Bus) sendLoop(ctx context.Context) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("CAN send loop panic recovered: %v", r)
		}
	}()

	for b.running {
		select {
		case <-ctx.Done():
			// Drain the send queue on shutdown
			for {
				select {
				case <-b.sendQueue:
					// Discard remaining messages
				default:
					return
				}
			}
		case msg := <-b.sendQueue:
			if msg == nil {
				continue
			}

			var frame [CANFD_MTU]byte
			err := msg.Marshal(&frame)
			if err != nil {
				log.Printf("Couldn't marshal CAN frame: %v", err)
				continue
			}

			// For CAN-FD frames, always write the full CANFD_MTU (72 bytes)
			// Linux SocketCAN expects the complete frame structure
			err = b.writeFrameWithRetry(frame[:])
			if err != nil {
				b.noteSendFailure("write error after retries", err)
				// Don't panic, just log and continue
				continue
			}

			if err := b.verifyInterfaceHealthy(); err != nil {
				b.noteSendFailure("post-write health check failed", err)
				continue
			}

			b.consecutiveSendFailures = 0
		}
	}
}

// writeFrameWithRetry attempts to write a frame with rate limiting and exponential backoff retry logic
func (b *Bus) writeFrameWithRetry(frame []byte) error {
	// Rate limiting: ensure minimum interval between sends
	now := time.Now()
	if !b.lastSendTime.IsZero() {
		elapsed := now.Sub(b.lastSendTime)
		if elapsed < b.minSendInterval {
			time.Sleep(b.minSendInterval - elapsed)
		}
	}

	maxRetries := 3
	baseDelay := 1 * time.Millisecond

	for attempt := 0; attempt <= maxRetries; attempt++ {
		_, err := b.file.Write(frame)
		if err == nil {
			b.lastSendTime = time.Now()
			return nil
		}

		// Check for specific buffer full errors
		if attempt < maxRetries {
			if isBufferFullError(err) {
				// Exponential backoff: 1ms, 2ms, 4ms
				delay := baseDelay * time.Duration(1<<attempt)
				time.Sleep(delay)
				continue
			}
		}

		// For other errors or final attempt, return immediately
		return err
	}

	return fmt.Errorf("max retries exceeded")
}

func (b *Bus) verifyInterfaceHealthy() error {
	if time.Since(b.lastHealthCheck) < b.healthCheckInterval {
		return nil
	}
	b.lastHealthCheck = time.Now()

	healthy, state, err := InterfaceHealthy(b.ifaceName)
	if err != nil {
		return err
	}
	if healthy {
		return nil
	}
	return fmt.Errorf("interface state is %s", state)
}

func (b *Bus) noteSendFailure(reason string, err error) {
	b.consecutiveSendFailures++
	log.Printf("CAN send failure on %s: %s: %v", b.ifaceName, reason, err)
	if b.consecutiveSendFailures <= 3 {
		return
	}

	log.Printf("CAN send failed %d times on %s, restarting interface", b.consecutiveSendFailures, b.ifaceName)
	if restartErr := RestartInterface(b.ifaceName); restartErr != nil {
		log.Printf("CAN interface restart failed on %s: %v", b.ifaceName, restartErr)
		return
	}

	log.Printf("CAN interface restarted on %s after send failures", b.ifaceName)
	b.consecutiveSendFailures = 0
	b.lastHealthCheck = time.Time{}
}

// isBufferFullError checks if the error indicates buffer space issues
func isBufferFullError(err error) bool {
	// Check for common buffer full error messages
	errStr := err.Error()
	return errStr == "no buffer space available" ||
		errStr == "resource temporarily unavailable" ||
		errStr == "would block"
}

// SetMinSendInterval sets the minimum time interval between CAN frame transmissions
func (b *Bus) SetMinSendInterval(interval time.Duration) {
	b.minSendInterval = interval
}

// GetMinSendInterval returns the current minimum send interval
func (b *Bus) GetMinSendInterval() time.Duration {
	return b.minSendInterval
}

// IsRunning returns true if the bus is currently running
func (b *Bus) IsRunning() bool {
	return b.running
}

// GetInterfaceName returns the name of the CAN interface
func (b *Bus) GetInterfaceName() string {
	if b.ifaceName != "" {
		return b.ifaceName
	}
	if b.file != nil {
		return b.file.Name()
	}
	return ""
}
