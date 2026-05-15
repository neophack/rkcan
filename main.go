package main

import (
	"context"
	"encoding/binary"
	"flag"
	"hash/crc32"
	"log"
	"net"
	"os"
	"os/signal"
	"runtime"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/penghongxia/rkcan/can"
)

const (
	udpPacketMaxSize   = 1400 // avoid IP fragmentation
	payloadHeaderSize  = 30   // fixed header before CAN data
	flushInterval      = 500 * time.Microsecond
	statsInterval      = 5 * time.Second
)

var (
	targetAddr = flag.String("addr", "10.0.0.22:6000", "UDP target address (host:port)")
	can0Iface  = flag.String("can0", "can0", "First CAN interface")
	can1Iface  = flag.String("can1", "can1", "Second CAN interface")
	noSend     = flag.Bool("nosend", true, "Disable built-in demo CAN sender")
)

// global sequence number, monotonically increasing across both channels
var globalSeq uint32

// program start time for mcuRelUs
var startTime = time.Now()

// canFrameInfo holds the necessary fields for UDP serialization without
// referencing the underlying *can.Message after Send() returns.
type canFrameInfo struct {
	seq     uint32
	channel uint8
	canID   uint32
	dlc     uint8
	dataLen int
	data    [64]byte
	ts      time.Time
}

// udpBatchSender aggregates multiple CAN frames into UDP packets and flushes
// periodically to keep latency low.  All methods are goroutine-safe.
//
// Note: 64-bit atomic fields MUST be placed at the top of the struct to
// guarantee alignment on 32-bit architectures (e.g. ARM32).
type udpBatchSender struct {
	// 64-bit fields used with sync/atomic — keep first!
	sentFrames  uint64
	sentPkts    uint64
	writeErrs   uint64
	recvFrames0 uint64
	recvFrames1 uint64

	conn   *net.UDPConn
	dst    *net.UDPAddr
	mu     sync.Mutex
	buf    [1500]byte
	pos    int
	frames int
}

func newUDPSender(addr string) (*udpBatchSender, error) {
	dst, err := net.ResolveUDPAddr("udp", addr)
	if err != nil {
		return nil, err
	}
	conn, err := net.ListenUDP("udp", nil)
	if err != nil {
		return nil, err
	}
	// 8 MB send buffer to absorb transient network back-pressure
	if err := conn.SetWriteBuffer(8 * 1024 * 1024); err != nil {
		log.Printf("Warning: failed to set UDP write buffer: %v", err)
	}
	return &udpBatchSender{conn: conn, dst: dst}, nil
}

func (s *udpBatchSender) Close() error {
	s.Flush()
	return s.conn.Close()
}

// Flush sends the current packet immediately.  Safe to call from any goroutine.
func (s *udpBatchSender) Flush() {
	s.mu.Lock()
	s.flushLocked()
	s.mu.Unlock()
}

func (s *udpBatchSender) flushLocked() {
	if s.frames == 0 {
		return
	}
	if _, err := s.conn.WriteToUDP(s.buf[:s.pos], s.dst); err != nil {
		atomic.AddUint64(&s.writeErrs, 1)
		log.Printf("[UDP ERROR] write failed: %v (dst=%s, pos=%d, frames=%d)", err, s.dst, s.pos, s.frames)
	} else {
		atomic.AddUint64(&s.sentPkts, 1)
	}
	s.pos = 0
	s.frames = 0
}

// Send serializes a CAN frame into the current UDP packet and flushes
// automatically when the packet is full.
func (s *udpBatchSender) Send(f *canFrameInfo) {
	payloadLen := uint16(payloadHeaderSize + f.dataLen)
	frameTotal := 2 + 2 + int(payloadLen) + 4 // sync + len + payload + crc

	s.mu.Lock()

	// If the current packet cannot hold this frame, flush first.
	if s.frames > 0 && s.pos+frameTotal > udpPacketMaxSize {
		s.flushLocked()
	}

	// Sync word
	s.buf[s.pos] = 0xAA
	s.buf[s.pos+1] = 0x55
	s.pos += 2

	// Payload length (little-endian)
	s.buf[s.pos] = byte(payloadLen)
	s.buf[s.pos+1] = byte(payloadLen >> 8)
	s.pos += 2

	// --- Payload ---
	payloadStart := s.pos

	// 0-3: sequence number
	binary.LittleEndian.PutUint32(s.buf[s.pos:s.pos+4], f.seq)
	s.pos += 4

	// 4: channel
	s.buf[s.pos] = f.channel
	s.pos++

	utcUs := uint64(f.ts.UnixMicro())
	mcuRelUs := uint32(time.Since(startTime).Microseconds())

	// 5-12: utcUs
	binary.LittleEndian.PutUint64(s.buf[s.pos:s.pos+8], utcUs)
	s.pos += 8

	// 13-16: mcuRelUs
	binary.LittleEndian.PutUint32(s.buf[s.pos:s.pos+4], mcuRelUs)
	s.pos += 4

	// 17-24: qnxUtcUs (same as utcUs for Linux hosts)
	binary.LittleEndian.PutUint64(s.buf[s.pos:s.pos+8], utcUs)
	s.pos += 8

	// 25-28: CAN ID (big-endian, matching SocketCAN bit layout)
	binary.BigEndian.PutUint32(s.buf[s.pos:s.pos+4], f.canID)
	s.pos += 4

	// 29: DLC
	s.buf[s.pos] = f.dlc
	s.pos++

	// 30+: data bytes
	copy(s.buf[s.pos:s.pos+f.dataLen], f.data[:f.dataLen])
	s.pos += f.dataLen

	// CRC32 over payload (little-endian)
	crc := crc32.ChecksumIEEE(s.buf[payloadStart:s.pos])
	s.buf[s.pos] = byte(crc)
	s.buf[s.pos+1] = byte(crc >> 8)
	s.buf[s.pos+2] = byte(crc >> 16)
	s.buf[s.pos+3] = byte(crc >> 24)
	s.pos += 4

	s.frames++
	atomic.AddUint64(&s.sentFrames, 1)

	// If we have reached a reasonable frame count, flush immediately to
	// bound latency for bursty traffic.
	if s.pos >= udpPacketMaxSize-100 {
		s.flushLocked()
	}

	s.mu.Unlock()
}

// runReceiver opens a CAN interface and forwards every received frame to
// the UDP sender.  It never drops frames: the bus recvQueue blocks instead.
func runReceiver(ctx context.Context, iface string, channel uint8, sender *udpBatchSender) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("PANIC recovered in receiver %s: %v", iface, r)
		}
	}()

	bus, err := can.NewBus(iface)
	if err != nil {
		log.Fatalf("FATAL: failed to open %s: %v", iface, err)
	}
	defer bus.Shutdown()

	if err := bus.ResetFilters(); err != nil {
		log.Printf("Warning: failed to reset filters on %s: %v", iface, err)
	}

	log.Printf("CAN receiver started: %s (channel %d)", iface, channel)

	for {
		select {
		case <-ctx.Done():
			return
		case msg, ok := <-bus.RecvQueue():
			if !ok {
				return
			}

			var info canFrameInfo
			info.seq = atomic.AddUint32(&globalSeq, 1)
			info.channel = channel
			info.canID = msg.ID
			info.dlc = msg.DLC
			info.dataLen = int(msg.Length)
			if info.dataLen > 64 {
				info.dataLen = 64
			}
			copy(info.data[:], msg.Data[:info.dataLen])
			info.ts = time.Now()

			switch channel {
			case 0:
				atomic.AddUint64(&sender.recvFrames0, 1)
			case 1:
				atomic.AddUint64(&sender.recvFrames1, 1)
			}

			sender.Send(&info)
		}
	}
}

// periodicFlusher ensures the UDP packet is sent at least every flushInterval,
// bounding end-to-end latency even during low-rate periods.
func periodicFlusher(ctx context.Context, sender *udpBatchSender) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("PANIC recovered in periodicFlusher: %v", r)
		}
	}()

	ticker := time.NewTicker(flushInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			sender.Flush()
			return
		case <-ticker.C:
			sender.Flush()
		}
	}
}

// statsReporter prints throughput and health metrics every few seconds.
func statsReporter(ctx context.Context, sender *udpBatchSender) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("PANIC recovered in statsReporter: %v", r)
		}
	}()

	ticker := time.NewTicker(statsInterval)
	defer ticker.Stop()

	var lastFrames, lastPkts, lastErrs uint64
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			frames := atomic.LoadUint64(&sender.sentFrames)
			pkts := atomic.LoadUint64(&sender.sentPkts)
			errs := atomic.LoadUint64(&sender.writeErrs)
			recv0 := atomic.LoadUint64(&sender.recvFrames0)
			recv1 := atomic.LoadUint64(&sender.recvFrames1)

			fps := frames - lastFrames
			pps := pkts - lastPkts
			eps := errs - lastErrs

			lastFrames = frames
			lastPkts = pkts
			lastErrs = errs

			log.Printf("[STATS] %d fps | %d pps | total=%d pkts=%d errs=%d | can0=%d can1=%d",
				fps, pps, frames, pkts, eps, recv0, recv1)
		}
	}
}

// canSender is the original demo sender, disabled by default so it does not
// interfere with real CAN traffic on a live bus.
func canSender(ctx context.Context, bus *can.Bus) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("PANIC recovered in canSender: %v", r)
		}
	}()

	log.Printf("CAN sender started")
	defer log.Printf("CAN sender stopped")

	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()

	counter := uint8(0)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			msg := can.NewMessage(0x123, []byte{0x01, 0x02, counter, 0x04})
			_ = bus.Send(msg) // ignore errors in demo sender
			counter++
		}
	}
}

func main() {
	flag.Parse()

	// Use all available CPU cores; Go 1.20+ defaults to this, but be explicit.
	runtime.GOMAXPROCS(runtime.NumCPU())

	log.Printf("========================================")
	log.Printf(" RKCAN - Dual CAN-FD to UDP Bridge")
	log.Printf(" Target: %s", *targetAddr)
	log.Printf(" CAN0  : %s", *can0Iface)
	log.Printf(" CAN1  : %s", *can1Iface)
	log.Printf("========================================")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sender, err := newUDPSender(*targetAddr)
	if err != nil {
		log.Fatalf("FATAL: failed to create UDP sender: %v", err)
	}
	defer sender.Close()

	// Start helper goroutines.
	go periodicFlusher(ctx, sender)
	go statsReporter(ctx, sender)

	var wg sync.WaitGroup

	// CAN0 receiver
	wg.Add(1)
	go func() {
		defer wg.Done()
		runReceiver(ctx, *can0Iface, 0, sender)
	}()

	// CAN1 receiver
	wg.Add(1)
	go func() {
		defer wg.Done()
		runReceiver(ctx, *can1Iface, 1, sender)
	}()

	// Optional demo sender on can0 (disabled by default on a live bus).
	if !*noSend {
		bus, err := can.NewBus(*can0Iface)
		if err != nil {
			log.Printf("Warning: demo sender failed to open %s: %v", *can0Iface, err)
		} else {
			defer bus.Shutdown()
			wg.Add(1)
			go func() {
				defer wg.Done()
				canSender(ctx, bus)
			}()
		}
	}

	// Wait for shutdown signal.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	sig := <-sigCh
	log.Printf("Received signal %v, initiating graceful shutdown...", sig)
	cancel()
	wg.Wait()

	// Final flush to ensure no frames are left in the buffer.
	sender.Flush()
	log.Printf("Shutdown complete. Total frames forwarded: %d",
		atomic.LoadUint64(&sender.sentFrames))
}
