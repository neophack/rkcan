//go:build linux

package main

import (
	"context"
	"encoding/binary"
	"flag"
	"hash/crc32"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"runtime"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/penghongxia/rkcan/can"
	"github.com/penghongxia/rkcan/filemanager"
	"github.com/penghongxia/rkcan/serial"
	"github.com/penghongxia/rkcan/system"
	"github.com/penghongxia/rkcan/timesync"
	"github.com/penghongxia/rkcan/web"
	"github.com/penghongxia/rkcan/wifi"
)

const (
	udpPacketMaxSize  = 1400
	payloadHeaderSize = 30
	flushInterval     = 500 * time.Microsecond
	statsInterval     = 5 * time.Second
)

var (
	targetAddr = flag.String("addr", "10.0.0.22:6000", "UDP target address (host:port)")
	can0Iface  = flag.String("can0", "can0", "First CAN interface")
	can1Iface  = flag.String("can1", "can1", "Second CAN interface")
	noSend     = flag.Bool("nosend", true, "Disable built-in demo CAN sender")
	webPort    = flag.Int("port", 80, "Web dashboard HTTP port")
	fileRoot   = flag.String("fileroot", "/userdata", "File manager root directory")
	txBRS      = flag.Bool("brs", false, "Send CAN-FD frames with Bit Rate Switch (data phase at dbitrate); default for demo sender and time sync")
	udpFlags   = flag.Bool("udpflags", false, "Encode CAN-FD flags in the high nibble of the UDP DLC byte (bit4=FD, bit5=BRS, bit6=ESI)")
)

// UDP DLC byte flag bits (only used when -udpflags is set). The low nibble
// always carries the CAN/CAN-FD DLC (0..15).
const (
	udpDLCFlagFD  = 0x10
	udpDLCFlagBRS = 0x20
	udpDLCFlagESI = 0x40
)

var globalSeq uint32
var startTime = time.Now()

type canFrameInfo struct {
	seq     uint32
	channel uint8
	canID   uint32
	dlc     uint8
	fd      bool
	brs     bool
	esi     bool
	dataLen int
	data    [64]byte
	ts      time.Time
}

// Note: 64-bit atomic fields MUST be placed at the top for 32-bit ARM alignment.
type udpBatchSender struct {
	sentFrames  uint64
	sentPkts    uint64
	writeErrs   uint64
	recvFrames0 uint64
	recvFrames1 uint64
	recvBRS0    uint64
	recvBRS1    uint64

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
	if err := conn.SetWriteBuffer(8 * 1024 * 1024); err != nil {
		log.Printf("Warning: failed to set UDP write buffer: %v", err)
	}
	return &udpBatchSender{conn: conn, dst: dst}, nil
}

func (s *udpBatchSender) Close() error {
	s.Flush()
	return s.conn.Close()
}

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

func (s *udpBatchSender) Send(f *canFrameInfo) {
	payloadLen := uint16(payloadHeaderSize + f.dataLen)
	frameTotal := 2 + 2 + int(payloadLen) + 4

	s.mu.Lock()

	if s.frames > 0 && s.pos+frameTotal > udpPacketMaxSize {
		s.flushLocked()
	}

	s.buf[s.pos] = 0xAA
	s.buf[s.pos+1] = 0x55
	s.pos += 2

	s.buf[s.pos] = byte(payloadLen)
	s.buf[s.pos+1] = byte(payloadLen >> 8)
	s.pos += 2

	payloadStart := s.pos

	binary.LittleEndian.PutUint32(s.buf[s.pos:s.pos+4], f.seq)
	s.pos += 4

	s.buf[s.pos] = f.channel
	s.pos++

	utcUs := uint64(f.ts.UnixMicro())
	mcuRelUs := uint32(time.Since(startTime).Microseconds())

	binary.LittleEndian.PutUint64(s.buf[s.pos:s.pos+8], utcUs)
	s.pos += 8

	binary.LittleEndian.PutUint32(s.buf[s.pos:s.pos+4], mcuRelUs)
	s.pos += 4

	binary.LittleEndian.PutUint64(s.buf[s.pos:s.pos+8], utcUs)
	s.pos += 8

	binary.BigEndian.PutUint32(s.buf[s.pos:s.pos+4], f.canID)
	s.pos += 4

	dlcByte := f.dlc & 0x0F
	if *udpFlags {
		if f.fd {
			dlcByte |= udpDLCFlagFD
		}
		if f.brs {
			dlcByte |= udpDLCFlagBRS
		}
		if f.esi {
			dlcByte |= udpDLCFlagESI
		}
	}
	s.buf[s.pos] = dlcByte
	s.pos++

	copy(s.buf[s.pos:s.pos+f.dataLen], f.data[:f.dataLen])
	s.pos += f.dataLen

	crc := crc32.ChecksumIEEE(s.buf[payloadStart:s.pos])
	s.buf[s.pos] = byte(crc)
	s.buf[s.pos+1] = byte(crc >> 8)
	s.buf[s.pos+2] = byte(crc >> 16)
	s.buf[s.pos+3] = byte(crc >> 24)
	s.pos += 4

	s.frames++
	atomic.AddUint64(&s.sentFrames, 1)

	if s.pos >= udpPacketMaxSize-100 {
		s.flushLocked()
	}

	s.mu.Unlock()
}

func runReceiver(ctx context.Context, iface string, channel uint8, sender *udpBatchSender) {
	for {
		err := runReceiverOnce(ctx, iface, channel, sender)
		if ctx.Err() != nil {
			return
		}
		log.Printf("CAN receiver %s crashed: %v — restarting in 1s", iface, err)
		time.Sleep(1 * time.Second)
	}
}

func runReceiverOnce(ctx context.Context, iface string, channel uint8, sender *udpBatchSender) (err error) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("PANIC recovered in receiver %s: %v", iface, r)
			err = nil
		}
	}()

	bus, err := can.NewBus(iface)
	if err != nil {
		return err
	}
	defer bus.Shutdown()

	if resetErr := bus.ResetFilters(); resetErr != nil {
		log.Printf("Warning: failed to reset filters on %s: %v", iface, resetErr)
	}

	log.Printf("CAN receiver started: %s (channel %d)", iface, channel)

	for {
		select {
		case <-ctx.Done():
			return nil
		case msg, ok := <-bus.RecvQueue():
			if !ok {
				return nil
			}

			var info canFrameInfo
			info.seq = atomic.AddUint32(&globalSeq, 1)
			info.channel = channel
			info.canID = msg.ID
			info.dlc = msg.DLC
			info.fd = msg.FD
			info.brs = msg.FD && msg.HasBRS()
			info.esi = msg.FD && msg.HasESI()
			info.dataLen = int(msg.Length)
			if info.dataLen > 64 {
				info.dataLen = 64
			}
			copy(info.data[:], msg.Data[:info.dataLen])
			info.ts = time.Now()

			switch channel {
			case 0:
				atomic.AddUint64(&sender.recvFrames0, 1)
				if info.brs {
					atomic.AddUint64(&sender.recvBRS0, 1)
				}
			case 1:
				atomic.AddUint64(&sender.recvFrames1, 1)
				if info.brs {
					atomic.AddUint64(&sender.recvBRS1, 1)
				}
			}

			sender.Send(&info)
		}
	}
}

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
			brs0 := atomic.LoadUint64(&sender.recvBRS0)
			brs1 := atomic.LoadUint64(&sender.recvBRS1)

			fps := frames - lastFrames
			pps := pkts - lastPkts
			eps := errs - lastErrs

			lastFrames = frames
			lastPkts = pkts
			lastErrs = errs

			log.Printf("[STATS] %d fps | %d pps | total=%d pkts=%d errs=%d | can0=%d (brs=%d) can1=%d (brs=%d)",
				fps, pps, frames, pkts, eps, recv0, brs0, recv1, brs1)
		}
	}
}

func canSender(ctx context.Context, bus *can.Bus, brs bool) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("PANIC recovered in canSender: %v", r)
		}
	}()

	log.Printf("CAN sender started (BRS=%v)", brs)
	defer log.Printf("CAN sender stopped")

	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()

	counter := uint8(0)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			msg := can.NewFDMessage(0x123, []byte{0x01, 0x02, counter, 0x04}, brs)
			_ = bus.Send(msg)
			counter++
		}
	}
}

func main() {
	flag.Parse()

	runtime.GOMAXPROCS(runtime.NumCPU())

	log.Printf("========================================")
	log.Printf(" RKCAN - Dual CAN-FD to UDP Bridge")
	log.Printf(" Target : %s", *targetAddr)
	log.Printf(" CAN0   : %s", *can0Iface)
	log.Printf(" CAN1   : %s", *can1Iface)
	log.Printf(" Web    : http://0.0.0.0:%d", *webPort)
	log.Printf(" TX BRS : %v  UDP flags: %v", *txBRS, *udpFlags)
	log.Printf("========================================")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sender, err := newUDPSender(*targetAddr)
	if err != nil {
		log.Fatalf("FATAL: failed to create UDP sender: %v", err)
	}
	defer sender.Close()

	// System monitoring
	sysCollector := system.NewCollector()
	go sysCollector.Start(ctx)

	// WiFi manager
	wifiMgr := wifi.NewManager()

	// Serial reader
	serialReader := serial.NewReader()

	// File manager
	fileMgr := filemanager.NewManager(*fileRoot)

	// CAN time sync sender
	timeSyncSender := timesync.NewSender()
	timeSyncSender.SetDefaultBRS(*txBRS)

	// CAN stats provider for web dashboard
	canStats := &web.CANStatsProvider{
		RecvFrames0: &sender.recvFrames0,
		RecvFrames1: &sender.recvFrames1,
		RecvBRS0:    &sender.recvBRS0,
		RecvBRS1:    &sender.recvBRS1,
		SentFrames:  &sender.sentFrames,
		SentPkts:    &sender.sentPkts,
		WriteErrs:   &sender.writeErrs,
		Ifaces:      []string{*can0Iface, *can1Iface},
	}
	go canStats.Start(ctx)

	// Web server
	webServer := web.NewServer(web.ServerConfig{
		Port:           *webPort,
		SysCollector:   sysCollector,
		CANStats:       canStats,
		WifiMgr:        wifiMgr,
		SerialReader:   serialReader,
		FileMgr:        fileMgr,
		CANIfaces:      []string{*can0Iface, *can1Iface},
		TimeSyncSender: timeSyncSender,
	})

	go func() {
		if err := webServer.Start(); err != nil && err != http.ErrServerClosed {
			log.Printf("Web server error: %v", err)
		}
	}()

	// Start CAN infrastructure
	go periodicFlusher(ctx, sender)
	go statsReporter(ctx, sender)

	var wg sync.WaitGroup

	// CAN0 receiver with auto-restart
	wg.Add(1)
	go func() {
		defer wg.Done()
		runReceiver(ctx, *can0Iface, 0, sender)
	}()

	// CAN1 receiver with auto-restart
	wg.Add(1)
	go func() {
		defer wg.Done()
		runReceiver(ctx, *can1Iface, 1, sender)
	}()

	// Optional demo sender
	if !*noSend {
		bus, err := can.NewBus(*can0Iface)
		if err != nil {
			log.Printf("Warning: demo sender failed to open %s: %v", *can0Iface, err)
		} else {
			defer bus.Shutdown()
			wg.Add(1)
			go func() {
				defer wg.Done()
				canSender(ctx, bus, *txBRS)
			}()
		}
	}

	// Wait for shutdown signal
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	sig := <-sigCh
	log.Printf("Received signal %v, initiating graceful shutdown...", sig)
	cancel()

	// Close UDP sender first to unblock any pending WriteToUDP in CAN receivers
	sender.Close()

	// Shutdown web server with timeout
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer shutdownCancel()
	webServer.Shutdown(shutdownCtx)

	// Close serial reader
	serialReader.Close()

	wg.Wait()

	log.Printf("Shutdown complete. Total frames forwarded: %d",
		atomic.LoadUint64(&sender.sentFrames))
}
