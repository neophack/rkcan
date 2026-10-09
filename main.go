//go:build linux

package main

import (
	"context"
	"encoding/binary"
	"flag"
	"fmt"
	"hash/crc32"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/penghongxia/rkcan/can"
	"github.com/penghongxia/rkcan/cantool"
	"github.com/penghongxia/rkcan/filemanager"
	"github.com/penghongxia/rkcan/replay"
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

// version is set at build time with -ldflags "-X main.version=..."
var version = "dev"

var (
	targetAddr    = flag.String("addr", "10.0.0.22:6000", "UDP target address (host:port)")
	can0Iface     = flag.String("can0", "can0", "First CAN interface (empty to disable)")
	can1Iface     = flag.String("can1", "can1", "Second CAN interface (empty to disable)")
	noSend        = flag.Bool("nosend", true, "Disable built-in demo CAN sender (ID 0x123 on -can0 every 100 ms)")
	webListen     = flag.String("listen", "", "Web dashboard listen address (empty = all interfaces)")
	webPort       = flag.Int("port", 80, "Web dashboard HTTP port (0 to disable)")
	webAuth       = flag.String("auth", "", "Protect the web dashboard with HTTP basic auth, format user:password")
	fileRoot      = flag.String("fileroot", "/userdata", "File manager root directory")
	wifiIface     = flag.String("wifi", "wlan0", "WiFi interface managed by the dashboard")
	chronyc       = flag.String("chronyc", "", "Path to chronyc (default: /userdata/chronyc, then $PATH)")
	chronyConf    = flag.String("chrony-conf", "/etc/chrony.conf", "Path to chrony.conf shown/edited in the dashboard")
	txBRS         = flag.Bool("brs", false, "Send CAN-FD frames with Bit Rate Switch (data phase at dbitrate); default for demo sender and time sync")
	udpFlags      = flag.Bool("udpflags", false, "Encode CAN-FD flags in the high nibble of the UDP DLC byte (bit4=FD, bit5=BRS, bit6=ESI)")
	timeSyncIface = flag.String("timesync", "", "Start CAN time sync on this interface at startup (e.g. can0)")
	timeSyncProto = flag.String("timesync-proto", "5A4", "Time sync protocol at startup: 5A4 (EEA2.1) or 594 (EEA3.0)")
	logFile       = flag.String("logfile", "", "Write logs to this file instead of stderr (rotated at -logmax MB)")
	logMaxMB      = flag.Int("logmax", 10, "Rotate -logfile when it exceeds this many MB (one backup is kept)")
	showVersion   = flag.Bool("version", false, "Print version and exit")
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

	conn       *net.UDPConn
	dst        *net.UDPAddr
	mu         sync.Mutex
	buf        [1500]byte
	pos        int
	frames     int
	closeOnce  sync.Once
	lastErrLog time.Time
	suppressed uint64
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
	var err error
	s.closeOnce.Do(func() {
		s.Flush()
		err = s.conn.Close()
	})
	return err
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
		// Rate limit: an unreachable target fails thousands of times per second
		if time.Since(s.lastErrLog) >= 5*time.Second {
			log.Printf("[UDP ERROR] write failed: %v (dst=%s, frames=%d, %d similar errors suppressed)",
				err, s.dst, s.frames, s.suppressed)
			s.lastErrLog = time.Now()
			s.suppressed = 0
		} else {
			s.suppressed++
		}
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

// frameObserver receives every frame read from a CAN interface.
type frameObserver interface {
	Observe(iface string, msg *can.Message, ts time.Time)
}

func runReceiver(ctx context.Context, iface string, channel uint8, sender *udpBatchSender, observers []frameObserver) {
	backoff := time.Second
	var lastErr string
	for {
		started := time.Now()
		err := runReceiverOnce(ctx, iface, channel, sender, observers)
		if ctx.Err() != nil {
			return
		}

		// Reset the backoff after a receiver that ran for a while
		if time.Since(started) > time.Minute {
			backoff = time.Second
		}
		msg := "bus closed"
		if err != nil {
			msg = err.Error()
		}
		if msg != lastErr {
			log.Printf("CAN receiver %s stopped: %s — retrying", iface, msg)
			lastErr = msg
		}

		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		if backoff < 10*time.Second {
			backoff *= 2
		}
	}
}

func runReceiverOnce(ctx context.Context, iface string, channel uint8, sender *udpBatchSender, observers []frameObserver) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("panic: %v", r)
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

	mode := "CAN"
	if bus.IsFD() {
		mode = "CAN-FD"
	}
	log.Printf("CAN receiver started: %s (channel %d, %s)", iface, channel, mode)

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

			for _, o := range observers {
				o.Observe(iface, msg, info.ts)
			}
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

// canSender sends a demo frame every 100 ms, reopening the bus whenever it
// goes away (interface restart, reconfiguration).
func canSender(ctx context.Context, iface string, brs bool) {
	log.Printf("CAN demo sender started on %s (BRS=%v)", iface, brs)
	defer log.Printf("CAN demo sender stopped")

	counter := uint8(0)
	for ctx.Err() == nil {
		bus, err := can.NewBusWithOptions(iface, can.Options{TxOnly: true})
		if err != nil {
			select {
			case <-ctx.Done():
				return
			case <-time.After(2 * time.Second):
			}
			continue
		}

		frameBRS := brs && bus.IsFD()
		ticker := time.NewTicker(100 * time.Millisecond)
	loop:
		for {
			select {
			case <-ctx.Done():
				break loop
			case <-bus.Done():
				break loop
			case <-ticker.C:
				data := []byte{0x01, 0x02, counter, 0x04}
				var msg *can.Message
				if bus.IsFD() {
					msg = can.NewFDMessage(0x123, data, frameBRS)
				} else {
					msg = can.NewClassicMessage(0x123, data)
				}
				if err := bus.SendNonBlocking(msg); err != nil {
					log.Printf("CAN demo sender: %v", err)
				}
				counter++
			}
		}
		ticker.Stop()
		bus.Shutdown()
	}
}

func main() {
	flag.Usage = func() {
		fmt.Fprintf(flag.CommandLine.Output(), "rkcan %s - dual CAN-FD to UDP bridge with web dashboard\n\n", version)
		fmt.Fprintf(flag.CommandLine.Output(), "Usage: rkcan [flags]\n\nEvery flag can also be set with an environment variable,\n")
		fmt.Fprintf(flag.CommandLine.Output(), "e.g. -timesync-proto -> %s. Command line flags take precedence.\n\n", envName("timesync-proto"))
		flag.PrintDefaults()
	}
	if err := applyEnvDefaults(flag.CommandLine); err != nil {
		fmt.Fprintf(os.Stderr, "rkcan: invalid environment: %v\n", err)
		os.Exit(2)
	}
	flag.Parse()

	if *showVersion {
		fmt.Println("rkcan", version)
		return
	}

	log.SetFlags(log.LstdFlags | log.Lmicroseconds)
	if *logFile != "" {
		lf, err := openRotatingFile(*logFile, int64(*logMaxMB)*1024*1024)
		if err != nil {
			log.Fatalf("FATAL: open log file: %v", err)
		}
		defer lf.Close()
		log.SetOutput(lf)
	}
	runtime.GOMAXPROCS(runtime.NumCPU())

	var authUser, authPass string
	if *webAuth != "" {
		var ok bool
		authUser, authPass, ok = strings.Cut(*webAuth, ":")
		if !ok || authUser == "" || authPass == "" {
			log.Fatalf("FATAL: -auth must be in the form user:password")
		}
	}

	proto := timesync.Protocol(strings.ToUpper(*timeSyncProto))
	if proto != timesync.Protocol5A4 && proto != timesync.Protocol594 {
		log.Fatalf("FATAL: -timesync-proto must be 5A4 or 594, got %q", *timeSyncProto)
	}

	system.ChronycPath = *chronyc
	system.ChronyConfPath = *chronyConf

	var canIfaces []string
	for _, iface := range []string{*can0Iface, *can1Iface} {
		if iface != "" {
			canIfaces = append(canIfaces, iface)
		}
	}

	log.Printf("========================================")
	log.Printf(" RKCAN %s - Dual CAN-FD to UDP Bridge", version)
	log.Printf(" Target : %s", *targetAddr)
	log.Printf(" CAN0   : %s", *can0Iface)
	log.Printf(" CAN1   : %s", *can1Iface)
	if *webPort > 0 {
		log.Printf(" Web    : http://%s", net.JoinHostPort(*webListen, strconv.Itoa(*webPort)))
	} else {
		log.Printf(" Web    : disabled")
	}
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

	// CAN time sync sender
	timeSyncSender := timesync.NewSender()
	timeSyncSender.SetDefaultBRS(*txBRS)
	if *timeSyncIface != "" {
		timeSyncSender.Configure(true, *timeSyncIface, proto, *txBRS)
	}
	defer timeSyncSender.Configure(false, "", proto, *txBRS)

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

	serialReader := serial.NewReader()
	defer serialReader.Close()

	// Interactive CAN tools, replay and recording
	txPool := can.NewTxPool()
	defer txPool.Close()
	canTool := cantool.NewSender(txPool)
	defer canTool.Stop(0)
	monitor := cantool.NewMonitor()
	recorder := replay.NewRecorder()
	defer recorder.Stop()
	player := replay.NewPlayer()
	defer player.Stop()
	replay.ExtraDirs = []string{*fileRoot}
	observers := []frameObserver{monitor, recorder}

	// Log channel numbers follow the flag order: -can0 = 1, -can1 = 2
	canChannels := map[string]int{}
	for i, iface := range []string{*can0Iface, *can1Iface} {
		if iface != "" {
			canChannels[iface] = i + 1
		}
	}

	var webServer *web.Server
	if *webPort > 0 {
		webServer = web.NewServer(web.ServerConfig{
			Port:           *webPort,
			Listen:         *webListen,
			AuthUser:       authUser,
			AuthPass:       authPass,
			Version:        version,
			SysCollector:   sysCollector,
			CANStats:       canStats,
			WifiMgr:        wifi.NewManager(*wifiIface),
			SerialReader:   serialReader,
			FileMgr:        filemanager.NewManager(*fileRoot),
			CANIfaces:      canIfaces,
			CANChannels:    canChannels,
			TimeSyncSender: timeSyncSender,
			Monitor:        monitor,
			CANSender:      canTool,
			Player:         player,
			Recorder:       recorder,
		})

		go func() {
			if err := webServer.Start(); err != nil && err != http.ErrServerClosed {
				log.Printf("Web server error: %v", err)
			}
		}()
	}

	// Start CAN infrastructure
	go periodicFlusher(ctx, sender)
	go statsReporter(ctx, sender)

	var wg sync.WaitGroup

	// CAN receivers with auto-restart
	for ch, iface := range []string{*can0Iface, *can1Iface} {
		if iface == "" {
			continue
		}
		wg.Add(1)
		go func(iface string, ch uint8) {
			defer wg.Done()
			runReceiver(ctx, iface, ch, sender, observers)
		}(iface, uint8(ch))
	}

	// Optional demo sender
	if !*noSend && *can0Iface != "" {
		wg.Add(1)
		go func() {
			defer wg.Done()
			canSender(ctx, *can0Iface, *txBRS)
		}()
	}

	// Wait for shutdown signal
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	sig := <-sigCh
	log.Printf("Received signal %v, initiating graceful shutdown...", sig)
	cancel()

	if webServer != nil {
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
		if err := webServer.Shutdown(shutdownCtx); err != nil {
			log.Printf("Web server shutdown: %v", err)
		}
		shutdownCancel()
	}

	// Wait for CAN goroutines, but never hang the service stop
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		log.Printf("Timed out waiting for CAN goroutines")
	}

	sender.Close()

	log.Printf("Shutdown complete. Total frames forwarded: %d",
		atomic.LoadUint64(&sender.sentFrames))
}
