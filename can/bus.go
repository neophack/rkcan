//go:build linux

package can

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"os"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

const (
	defaultSendQueueSize = 10000
	defaultRecvQueueSize = 200000
	defaultSendInterval  = 500 * time.Microsecond
	healthCheckInterval  = 1 * time.Second
	maxSendFailures      = 3
)

// Options configures a Bus.
type Options struct {
	// TxOnly installs an empty receive filter so the socket never queues
	// received frames. Use it for sockets that only transmit.
	TxOnly bool
	// SendQueueSize and RecvQueueSize override the default channel sizes.
	SendQueueSize int
	RecvQueueSize int
}

// Bus represents a SocketCAN raw socket bound to one interface. It supports
// both CAN-FD (MTU 72) and classic CAN (MTU 16) interfaces; CAN-FD frames,
// including frames with BRS, can only be sent on a CAN-FD interface.
type Bus struct {
	file      *os.File
	fd        int
	ifaceName string
	fdMode    bool // interface MTU is CANFD_MTU

	sendQueue chan *Message
	recvQueue chan *Message

	running      atomic.Bool
	ctx          context.Context
	cancel       context.CancelFunc
	shutdownOnce sync.Once

	minSendInterval atomic.Int64 // nanoseconds

	// Only touched by sendLoop.
	lastSendTime            time.Time
	lastHealthCheck         time.Time
	consecutiveSendFailures int
}

// NewBus creates a new bus instance for the specified interface.
func NewBus(ifaceName string) (*Bus, error) {
	return NewBusWithOptions(ifaceName, Options{})
}

// NewBusWithOptions creates a new bus instance with the given options.
func NewBusWithOptions(ifaceName string, opts Options) (bus *Bus, err error) {
	iface, err := net.InterfaceByName(ifaceName)
	if err != nil {
		return nil, fmt.Errorf("interface %s: %w", ifaceName, err)
	}

	if (iface.Flags & net.FlagUp) == 0 {
		return nil, fmt.Errorf("interface %s is down", ifaceName)
	}

	var fdMode bool
	switch iface.MTU {
	case CANFD_MTU:
		fdMode = true
	case CAN_MTU:
		fdMode = false
	default:
		return nil, fmt.Errorf("interface %s: unexpected MTU %d (want %d for CAN-FD or %d for CAN)",
			ifaceName, iface.MTU, CANFD_MTU, CAN_MTU)
	}

	fd, err := unix.Socket(unix.AF_CAN, unix.SOCK_RAW, unix.CAN_RAW)
	if err != nil {
		return nil, fmt.Errorf("socket: %w", err)
	}
	closeOnErr := func(e error) (*Bus, error) {
		unix.Close(fd)
		return nil, e
	}

	// Put fd in non-blocking mode, so the created file will be
	// registered by the runtime poller
	if err := unix.SetNonblock(fd, true); err != nil {
		return closeOnErr(fmt.Errorf("set nonblock: %w", err))
	}

	// Enable CAN-FD frames. Harmless on classic interfaces, where the kernel
	// simply never delivers CAN-FD frames.
	if err := unix.SetsockoptInt(fd, unix.SOL_CAN_RAW, unix.CAN_RAW_FD_FRAMES, 1); err != nil {
		return closeOnErr(fmt.Errorf("setsockopt CAN_RAW_FD_FRAMES: %w", err))
	}

	// Disable error frames
	if err := unix.SetsockoptInt(fd, unix.SOL_CAN_RAW, unix.CAN_RAW_ERR_FILTER, 0); err != nil {
		return closeOnErr(fmt.Errorf("setsockopt CAN_RAW_ERR_FILTER: %w", err))
	}

	if opts.TxOnly {
		// An empty filter list means "receive nothing"
		if err := unix.SetsockoptCanRawFilter(fd, unix.SOL_CAN_RAW, unix.CAN_RAW_FILTER, []unix.CanFilter{}); err != nil {
			return closeOnErr(fmt.Errorf("setsockopt CAN_RAW_FILTER: %w", err))
		}
	} else {
		// Increase socket receive buffer to prevent kernel-level frame loss
		// under high bus load. Default (~208KB) is too small for burst traffic.
		rcvBufSize := 4 * 1024 * 1024
		if err := unix.SetsockoptInt(fd, unix.SOL_SOCKET, unix.SO_RCVBUF, rcvBufSize); err != nil {
			log.Printf("Warning: failed to set CAN socket SO_RCVBUF to %d: %v", rcvBufSize, err)
		}
	}

	if err := unix.Bind(fd, &unix.SockaddrCAN{Ifindex: iface.Index}); err != nil {
		return closeOnErr(fmt.Errorf("bind: %w", err))
	}

	sendQueueSize := opts.SendQueueSize
	if sendQueueSize <= 0 {
		sendQueueSize = defaultSendQueueSize
	}
	recvQueueSize := opts.RecvQueueSize
	if recvQueueSize <= 0 {
		recvQueueSize = defaultRecvQueueSize
		if opts.TxOnly {
			recvQueueSize = 1
		}
	}

	ctx, cancel := context.WithCancel(context.Background())
	bus = &Bus{
		file:      os.NewFile(uintptr(fd), ifaceName),
		fd:        fd,
		ifaceName: ifaceName,
		fdMode:    fdMode,
		sendQueue: make(chan *Message, sendQueueSize),
		recvQueue: make(chan *Message, recvQueueSize),
		ctx:       ctx,
		cancel:    cancel,
	}
	bus.running.Store(true)
	bus.minSendInterval.Store(int64(defaultSendInterval))

	go bus.recvLoop(ctx)
	go bus.sendLoop(ctx)

	return bus, nil
}

// Shutdown gracefully shuts down the bus. It is safe to call more than once
// and from any goroutine. RecvQueue is closed once the receive loop exits.
func (b *Bus) Shutdown() {
	b.shutdownOnce.Do(func() {
		b.running.Store(false)
		b.cancel()
		// Shut down the socket first to unblock any pending Read/Write
		unix.Shutdown(b.fd, unix.SHUT_RDWR)
		b.file.Close()
	})
}

// Done returns a channel that is closed when the bus shuts down.
func (b *Bus) Done() <-chan struct{} {
	return b.ctx.Done()
}

// ResetFilters resets CAN filters to allow all messages
func (b *Bus) ResetFilters() error {
	return b.SetFilters(&[]unix.CanFilter{{Id: 0, Mask: 0}})
}

// SetFilters sets CAN ID filters for message reception
func (b *Bus) SetFilters(filters *[]unix.CanFilter) error {
	if err := unix.SetsockoptCanRawFilter(b.fd, unix.SOL_CAN_RAW, unix.CAN_RAW_FILTER, *filters); err != nil {
		return fmt.Errorf("setsockopt CAN_RAW_FILTER: %w", err)
	}
	return nil
}

// SendQueue returns the channel for sending messages
func (b *Bus) SendQueue() chan<- *Message {
	return b.sendQueue
}

// RecvQueue returns the channel for receiving messages. It is closed when
// the bus shuts down.
func (b *Bus) RecvQueue() <-chan *Message {
	return b.recvQueue
}

// IsFD reports whether the interface is configured for CAN-FD (MTU 72).
func (b *Bus) IsFD() bool {
	return b.fdMode
}

func (b *Bus) checkSendable(msg *Message) error {
	if msg == nil {
		return fmt.Errorf("nil message")
	}
	if !b.running.Load() {
		return fmt.Errorf("bus is not running")
	}
	if msg.FD && !b.fdMode {
		return fmt.Errorf("interface %s is not in CAN-FD mode (fd on), cannot send CAN-FD frame", b.ifaceName)
	}
	return nil
}

// Send queues a message, blocking while the send queue is full.
func (b *Bus) Send(msg *Message) error {
	if err := b.checkSendable(msg); err != nil {
		return err
	}

	select {
	case b.sendQueue <- msg:
		return nil
	case <-b.ctx.Done():
		return fmt.Errorf("bus is shutting down")
	}
}

// SendNonBlocking queues a message, failing if the send queue is full.
func (b *Bus) SendNonBlocking(msg *Message) error {
	if err := b.checkSendable(msg); err != nil {
		return err
	}

	select {
	case b.sendQueue <- msg:
		return nil
	default:
		return fmt.Errorf("send queue is full")
	}
}

// recvLoop reads frames from the socket and owns closing recvQueue.
func (b *Bus) recvLoop(ctx context.Context) {
	defer close(b.recvQueue)
	defer func() {
		if r := recover(); r != nil {
			log.Printf("CAN receive loop panic recovered on %s: %v", b.ifaceName, r)
			b.Shutdown()
		}
	}()

	var frame [CANFD_MTU]byte

	for {
		n, err := b.file.Read(frame[:])
		if ctx.Err() != nil {
			return
		}
		if err != nil {
			if errors.Is(err, os.ErrClosed) {
				return
			}
			// Fatal errors (e.g. interface down) terminate the bus so that
			// the owner can recreate it.
			if errors.Is(err, syscall.ENETDOWN) || errors.Is(err, syscall.ENETRESET) ||
				errors.Is(err, syscall.ECONNRESET) || errors.Is(err, syscall.ENODEV) ||
				errors.Is(err, syscall.EBADF) {
				log.Printf("CAN interface error on %s: %v — shutting down bus", b.ifaceName, err)
				b.Shutdown()
				return
			}
			log.Printf("CAN read error on %s: %v", b.ifaceName, err)
			// Avoid a busy loop on persistent errors
			select {
			case <-ctx.Done():
				return
			case <-time.After(10 * time.Millisecond):
			}
			continue
		}

		if n != CAN_MTU && n != CANFD_MTU {
			continue
		}

		msg := new(Message)
		if err := msg.Unmarshal(frame[:n]); err != nil {
			log.Printf("Invalid CAN frame on %s: %v", b.ifaceName, err)
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

// sendLoop writes queued frames to the socket
func (b *Bus) sendLoop(ctx context.Context) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("CAN send loop panic recovered on %s: %v", b.ifaceName, r)
			b.Shutdown()
		}
	}()

	var frame [CANFD_MTU]byte

	for {
		select {
		case <-ctx.Done():
			return
		case msg := <-b.sendQueue:
			if msg == nil {
				continue
			}

			n, err := msg.Encode(&frame)
			if err != nil {
				log.Printf("Couldn't encode CAN frame on %s: %v", b.ifaceName, err)
				continue
			}

			// CAN-FD frames (incl. BRS) are written as the full CANFD_MTU
			// (72 bytes), classic frames as CAN_MTU (16 bytes). SocketCAN
			// selects the frame type from the write size.
			if err := b.writeFrameWithRetry(ctx, frame[:n]); err != nil {
				if ctx.Err() != nil {
					return
				}
				b.noteSendFailure("write error after retries", err)
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

// writeFrameWithRetry writes a frame with rate limiting and exponential
// backoff while the device TX queue is full.
func (b *Bus) writeFrameWithRetry(ctx context.Context, frame []byte) error {
	if !b.lastSendTime.IsZero() {
		interval := time.Duration(b.minSendInterval.Load())
		if elapsed := time.Since(b.lastSendTime); elapsed < interval {
			time.Sleep(interval - elapsed)
		}
	}

	// A full device TX queue is normal at high load (e.g. replay at max
	// speed); keep retrying for up to txQueueFullTimeout before treating it
	// as a failure (no ACK / bus problem).
	const txQueueFullTimeout = 500 * time.Millisecond
	deadline := time.Now().Add(txQueueFullTimeout)
	delay := 100 * time.Microsecond

	for {
		_, err := b.file.Write(frame)
		if err == nil {
			b.lastSendTime = time.Now()
			return nil
		}
		if !isBufferFullError(err) || time.Now().After(deadline) {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(delay):
		}
		if delay < 5*time.Millisecond {
			delay *= 2
		}
	}
}

func (b *Bus) verifyInterfaceHealthy() error {
	if time.Since(b.lastHealthCheck) < healthCheckInterval {
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
	if b.consecutiveSendFailures <= maxSendFailures {
		return
	}

	log.Printf("CAN send failed %d times on %s, restarting interface", b.consecutiveSendFailures, b.ifaceName)
	if restartErr := RestartInterface(b.ifaceName); restartErr != nil {
		log.Printf("CAN interface restart failed on %s: %v", b.ifaceName, restartErr)
		return
	}

	b.consecutiveSendFailures = 0
	b.lastHealthCheck = time.Time{}
}

// isBufferFullError reports whether err means the device TX queue is full
func isBufferFullError(err error) bool {
	return errors.Is(err, syscall.ENOBUFS) || errors.Is(err, syscall.EAGAIN)
}

// SetMinSendInterval sets the minimum time interval between CAN frame transmissions
func (b *Bus) SetMinSendInterval(interval time.Duration) {
	if interval < 0 {
		interval = 0
	}
	b.minSendInterval.Store(int64(interval))
}

// GetMinSendInterval returns the current minimum send interval
func (b *Bus) GetMinSendInterval() time.Duration {
	return time.Duration(b.minSendInterval.Load())
}

// IsRunning returns true if the bus is currently running
func (b *Bus) IsRunning() bool {
	return b.running.Load()
}

// GetInterfaceName returns the name of the CAN interface
func (b *Bus) GetInterfaceName() string {
	return b.ifaceName
}
