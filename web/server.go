//go:build linux

package web

import (
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/penghongxia/rkcan/candiag"
	"github.com/penghongxia/rkcan/filemanager"
	"github.com/penghongxia/rkcan/serial"
	"github.com/penghongxia/rkcan/system"
	"github.com/penghongxia/rkcan/timesync"
	"github.com/penghongxia/rkcan/wifi"
)

//go:embed static
var staticFiles embed.FS

type CANStats struct {
	Can0RxFPS     uint64  `json:"can0RxFps"`
	Can0TxFPS     uint64  `json:"can0TxFps"`
	Can0Total     uint64  `json:"can0Total"`
	Can0Bitrate   uint32  `json:"can0Bitrate"`
	Can0State     string  `json:"can0State"`
	Can0SamplePt  float64 `json:"can0SamplePt"`
	Can0DBitrate  uint32  `json:"can0DBitrate"`
	Can0DSamplePt float64 `json:"can0DSamplePt"`
	Can0BusState  string  `json:"can0BusState"`
	Can0RxBRS     uint64  `json:"can0RxBrs"`
	Can1RxFPS     uint64  `json:"can1RxFps"`
	Can1TxFPS     uint64  `json:"can1TxFps"`
	Can1Total     uint64  `json:"can1Total"`
	Can1Bitrate   uint32  `json:"can1Bitrate"`
	Can1State     string  `json:"can1State"`
	Can1SamplePt  float64 `json:"can1SamplePt"`
	Can1DBitrate  uint32  `json:"can1DBitrate"`
	Can1DSamplePt float64 `json:"can1DSamplePt"`
	Can1BusState  string  `json:"can1BusState"`
	Can1RxBRS     uint64  `json:"can1RxBrs"`
	UDPSentPkts   uint64  `json:"udpSentPkts"`
	UDPErrors     uint64  `json:"udpErrors"`
	SentFrames    uint64  `json:"sentFrames"`
}

type CANStatsProvider struct {
	RecvFrames0 *uint64
	RecvFrames1 *uint64
	RecvBRS0    *uint64 // received CAN-FD frames with BRS set (optional)
	RecvBRS1    *uint64
	SentFrames  *uint64
	SentPkts    *uint64
	WriteErrs   *uint64
	Ifaces      []string

	mu        sync.RWMutex
	stats     CANStats
	prevRecv0 uint64
	prevRecv1 uint64
	prevTx0   uint64
	prevTx1   uint64
}

// Start runs a background ticker that updates FPS and bitrate once per second.
func (p *CANStatsProvider) Start(ctx context.Context) {
	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			p.tick()
		}
	}
}

func (p *CANStatsProvider) tick() {
	recv0 := atomic.LoadUint64(p.RecvFrames0)
	recv1 := atomic.LoadUint64(p.RecvFrames1)

	// Read kernel TX packet counters from sysfs.
	readSysStat := func(iface, name string) uint64 {
		data, err := os.ReadFile(fmt.Sprintf("/sys/class/net/%s/statistics/%s", iface, name))
		if err != nil {
			return 0
		}
		v, _ := strconv.ParseUint(strings.TrimSpace(string(data)), 10, 64)
		return v
	}

	var tx0, tx1 uint64
	if len(p.Ifaces) > 0 {
		tx0 = readSysStat(p.Ifaces[0], "tx_packets")
	}
	if len(p.Ifaces) > 1 {
		tx1 = readSysStat(p.Ifaces[1], "tx_packets")
	}

	p.mu.Lock()
	p.stats.Can0RxFPS = recv0 - p.prevRecv0
	p.stats.Can1RxFPS = recv1 - p.prevRecv1
	p.stats.Can0TxFPS = tx0 - p.prevTx0
	p.stats.Can1TxFPS = tx1 - p.prevTx1
	p.stats.Can0Total = recv0
	p.stats.Can1Total = recv1
	if p.RecvBRS0 != nil {
		p.stats.Can0RxBRS = atomic.LoadUint64(p.RecvBRS0)
	}
	if p.RecvBRS1 != nil {
		p.stats.Can1RxBRS = atomic.LoadUint64(p.RecvBRS1)
	}
	p.stats.UDPSentPkts = atomic.LoadUint64(p.SentPkts)
	p.stats.UDPErrors = atomic.LoadUint64(p.WriteErrs)
	p.stats.SentFrames = atomic.LoadUint64(p.SentFrames)

	for i, iface := range p.Ifaces {
		if info, err := candiag.GetCANInterfaceInfo(iface); err == nil {
			if i == 0 {
				p.stats.Can0Bitrate = info.Bitrate
				p.stats.Can0State = info.State
				p.stats.Can0SamplePt = info.SamplePoint
				p.stats.Can0DBitrate = info.DBitrate
				p.stats.Can0DSamplePt = info.DSamplePoint
				p.stats.Can0BusState = info.BusState
			} else if i == 1 {
				p.stats.Can1Bitrate = info.Bitrate
				p.stats.Can1State = info.State
				p.stats.Can1SamplePt = info.SamplePoint
				p.stats.Can1DBitrate = info.DBitrate
				p.stats.Can1DSamplePt = info.DSamplePoint
				p.stats.Can1BusState = info.BusState
			}
		}
	}

	p.prevRecv0 = recv0
	p.prevRecv1 = recv1
	p.prevTx0 = tx0
	p.prevTx1 = tx1
	p.mu.Unlock()
}

func (p *CANStatsProvider) Get() CANStats {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.stats
}

type Server struct {
	httpServer     *http.Server
	sysCollector   *system.Collector
	canStats       *CANStatsProvider
	wifiMgr        *wifi.Manager
	serialReader   *serial.Reader
	fileMgr        *filemanager.Manager
	canIfaces      []string
	timeSyncSender *timesync.Sender
}

type ServerConfig struct {
	Port           int
	SysCollector   *system.Collector
	CANStats       *CANStatsProvider
	WifiMgr        *wifi.Manager
	SerialReader   *serial.Reader
	FileMgr        *filemanager.Manager
	CANIfaces      []string
	TimeSyncSender *timesync.Sender
}

func NewServer(cfg ServerConfig) *Server {
	s := &Server{
		sysCollector:   cfg.SysCollector,
		canStats:       cfg.CANStats,
		wifiMgr:        cfg.WifiMgr,
		serialReader:   cfg.SerialReader,
		fileMgr:        cfg.FileMgr,
		canIfaces:      cfg.CANIfaces,
		timeSyncSender: cfg.TimeSyncSender,
	}

	mux := http.NewServeMux()

	// Static files
	staticSub, _ := fs.Sub(staticFiles, "static")
	mux.Handle("/", http.FileServer(http.FS(staticSub)))

	// SSE endpoint for real-time data
	mux.HandleFunc("/api/sse", s.handleSSE)

	// System APIs
	mux.HandleFunc("/api/system/overview", s.handleSystemOverview)
	mux.HandleFunc("/api/system/processes", s.handleProcesses)
	mux.HandleFunc("/api/system/time", s.handleTime)
	mux.HandleFunc("/api/system/settime", s.handleSetTime)
	mux.HandleFunc("/api/system/dmesg", s.handleDmesg)
	mux.HandleFunc("/api/system/poweroff", s.handlePoweroff)

	// CAN APIs
	mux.HandleFunc("/api/can/stats", s.handleCANStats)
	mux.HandleFunc("/api/can/diagnostics", s.handleCANDiagnostics)
	mux.HandleFunc("/api/can/details", s.handleCANDetails)
	mux.HandleFunc("/api/can/configure", s.handleCANConfigure)
	mux.HandleFunc("/api/can/timesync", s.handleCANTimeSync)

	// WiFi APIs
	mux.HandleFunc("/api/wifi/status", s.handleWiFiStatus)
	mux.HandleFunc("/api/wifi/scan", s.handleWiFiScan)
	mux.HandleFunc("/api/wifi/connect", s.handleWiFiConnect)
	mux.HandleFunc("/api/wifi/disconnect", s.handleWiFiDisconnect)

	// Serial APIs
	mux.HandleFunc("/api/serial/ports", s.handleSerialPorts)
	mux.HandleFunc("/api/serial/open", s.handleSerialOpen)
	mux.HandleFunc("/api/serial/close", s.handleSerialClose)
	mux.HandleFunc("/api/serial/logs", s.handleSerialLogs)
	mux.HandleFunc("/api/serial/sse", s.handleSerialSSE)

	// Chrony APIs
	mux.HandleFunc("/api/chrony", s.handleChrony)
	mux.HandleFunc("/api/chrony/config", s.handleChronyConfig)

	// File APIs
	mux.HandleFunc("/api/files/list", s.handleFileList)
	mux.HandleFunc("/api/files/upload", s.handleFileUpload)
	mux.HandleFunc("/api/files/download", s.handleFileDownload)
	mux.HandleFunc("/api/files/delete", s.handleFileDelete)
	mux.HandleFunc("/api/files/mkdir", s.handleFileMkdir)
	mux.HandleFunc("/api/files/rename", s.handleFileRename)

	s.httpServer = &http.Server{
		Addr:         fmt.Sprintf(":%d", cfg.Port),
		Handler:      corsMiddleware(mux),
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 0, // SSE needs no write timeout
		IdleTimeout:  120 * time.Second,
	}

	return s
}

func (s *Server) Start() error {
	log.Printf("Web server starting on %s", s.httpServer.Addr)
	return s.httpServer.ListenAndServe()
}

func (s *Server) Shutdown(ctx context.Context) error {
	return s.httpServer.Shutdown(ctx)
}

func corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, DELETE, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
		if r.Method == "OPTIONS" {
			w.WriteHeader(http.StatusOK)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func writeJSON(w http.ResponseWriter, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

// handleSSE pushes system stats + CAN stats every second
func (s *Server) handleSSE(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "SSE not supported", http.StatusInternalServerError)
		return
	}

	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case <-ticker.C:
			overview := s.sysCollector.Overview()
			canStats := s.canStats.Get()

			data := map[string]interface{}{
				"system": overview,
				"can":    canStats,
			}

			jsonData, err := json.Marshal(data)
			if err != nil {
				continue
			}

			fmt.Fprintf(w, "data: %s\n\n", jsonData)
			flusher.Flush()
		}
	}
}

// System API handlers
func (s *Server) handleSystemOverview(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, s.sysCollector.Overview())
}

func (s *Server) handleProcesses(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, s.sysCollector.Proc.Stats())
}

func (s *Server) handleTime(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, s.sysCollector.Time.Stats())
}

// handleSetTime sets the system clock to the timestamp provided by the browser.
// POST /api/system/settime  body: {"unixMs": 1718000000000}
func (s *Server) handleSetTime(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		writeError(w, 405, "Method not allowed")
		return
	}

	var req struct {
		UnixMs int64 `json:"unixMs"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.UnixMs == 0 {
		writeError(w, 400, "Invalid request: unixMs required")
		return
	}

	// Format as MMDDhhmm[[CC]YY][.ss] for the `date` command.
	t := time.UnixMilli(req.UnixMs).UTC()
	dateStr := t.Format("01021504") + strconv.Itoa(t.Year()) + "." + fmt.Sprintf("%02d", t.Second())

	out, err := exec.Command("date", "-u", "-s", t.Format("2006-01-02 15:04:05")).CombinedOutput()
	if err != nil {
		_ = dateStr // fallback format not needed
		writeError(w, 500, fmt.Sprintf("Failed to set time: %v (%s)", err, strings.TrimSpace(string(out))))
		return
	}

	// Best-effort: sync hardware clock if hwclock is available.
	exec.Command("hwclock", "-w").Run()

	writeJSON(w, map[string]string{
		"status": "ok",
		"time":   t.Format("2006-01-02 15:04:05 UTC"),
	})
}

func (s *Server) handleDmesg(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" {
		writeError(w, 405, "Method not allowed")
		return
	}

	out, err := exec.Command("dmesg").CombinedOutput()
	if err != nil {
		writeError(w, 500, fmt.Sprintf("Failed to run dmesg: %v", err))
		return
	}

	writeJSON(w, map[string]string{"dmesg": string(out)})
}

func (s *Server) handlePoweroff(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		writeError(w, 405, "Method not allowed")
		return
	}

	var req struct {
		Action string `json:"action"` // "poweroff" or "reboot"
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, 400, "Invalid request")
		return
	}

	go func() {
		// Give the HTTP response a chance to be sent before killing the system
		time.Sleep(1 * time.Second)

		// Flush filesystem buffers
		exec.Command("sync").Run()

		// Safely unmount userdata to avoid UBIFS recovery on next boot
		exec.Command("umount", "/userdata").Run()

		// Flush again
		exec.Command("sync").Run()

		var cmd *exec.Cmd
		if req.Action == "reboot" {
			cmd = exec.Command("reboot")
		} else {
			cmd = exec.Command("poweroff")
		}
		if err := cmd.Run(); err != nil {
			log.Printf("Poweroff command failed: %v", err)
		}
	}()

	writeJSON(w, map[string]string{"status": "ok", "message": "System is shutting down..."})
}

// CAN API handlers
func (s *Server) handleCANStats(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, s.canStats.Get())
}

func (s *Server) handleCANDiagnostics(w http.ResponseWriter, r *http.Request) {
	results := make([]*candiag.DiagResult, 0, len(s.canIfaces))
	for _, iface := range s.canIfaces {
		results = append(results, candiag.RunDiagnostic(iface))
	}
	writeJSON(w, results)
}

func (s *Server) handleCANDetails(w http.ResponseWriter, r *http.Request) {
	iface := r.URL.Query().Get("iface")
	if iface == "" {
		iface = s.canIfaces[0]
	}

	valid := false
	for _, i := range s.canIfaces {
		if i == iface {
			valid = true
			break
		}
	}
	if !valid {
		writeError(w, 400, "Invalid interface")
		return
	}

	info, err := candiag.GetCANInterfaceInfo(iface)
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	writeJSON(w, info)
}

func (s *Server) handleCANConfigure(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		writeError(w, 405, "Method not allowed")
		return
	}

	var req struct {
		Iface        string  `json:"interface"`
		Bitrate      uint32  `json:"bitrate"`
		SamplePoint  float64 `json:"samplePoint"`
		DBitrate     uint32  `json:"dbitrate"`
		DSamplePoint float64 `json:"dsamplePoint"`
		FD           bool    `json:"fd"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, 400, "Invalid request")
		return
	}

	valid := false
	for _, i := range s.canIfaces {
		if i == req.Iface {
			valid = true
			break
		}
	}
	if !valid {
		writeError(w, 400, "Invalid interface")
		return
	}

	if req.Bitrate == 0 {
		writeError(w, 400, "Bitrate required")
		return
	}

	// Build command: ip link set <iface> type can ...
	cmdArgs := []string{"link", "set", req.Iface, "type", "can",
		"bitrate", strconv.FormatUint(uint64(req.Bitrate), 10)}

	if req.SamplePoint > 0 {
		cmdArgs = append(cmdArgs, "sample-point", fmt.Sprintf("%.2f", req.SamplePoint))
	}

	if req.DBitrate > 0 {
		cmdArgs = append(cmdArgs, "dbitrate", strconv.FormatUint(uint64(req.DBitrate), 10))
		if req.DSamplePoint > 0 {
			cmdArgs = append(cmdArgs, "dsample-point", fmt.Sprintf("%.2f", req.DSamplePoint))
		}
	}

	if req.FD {
		cmdArgs = append(cmdArgs, "fd", "on")
	}

	// Bring interface down
	if out, err := exec.Command("ip", "link", "set", req.Iface, "down").CombinedOutput(); err != nil {
		writeError(w, 500, fmt.Sprintf("Failed to bring interface down: %v (%s)", err, string(out)))
		return
	}

	// Apply configuration
	if out, err := exec.Command("ip", cmdArgs...).CombinedOutput(); err != nil {
		// Attempt to bring it back up so we don't leave it down
		exec.Command("ip", "link", "set", req.Iface, "up").Run()
		writeError(w, 500, fmt.Sprintf("Failed to configure interface: %v (%s)", err, string(out)))
		return
	}

	// Bring interface up
	if out, err := exec.Command("ip", "link", "set", req.Iface, "up").CombinedOutput(); err != nil {
		writeError(w, 500, fmt.Sprintf("Failed to bring interface up: %v (%s)", err, string(out)))
		return
	}

	writeJSON(w, map[string]string{"status": "ok"})
}

// WiFi API handlers
func (s *Server) handleWiFiStatus(w http.ResponseWriter, r *http.Request) {
	status, err := s.wifiMgr.GetStatus()
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	writeJSON(w, status)
}

func (s *Server) handleWiFiScan(w http.ResponseWriter, r *http.Request) {
	networks, err := s.wifiMgr.Scan()
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	writeJSON(w, networks)
}

func (s *Server) handleWiFiConnect(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		writeError(w, 405, "Method not allowed")
		return
	}

	var req struct {
		SSID     string `json:"ssid"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, 400, "Invalid request")
		return
	}

	if err := s.wifiMgr.Connect(req.SSID, req.Password); err != nil {
		writeError(w, 500, err.Error())
		return
	}

	writeJSON(w, map[string]string{"status": "ok"})
}

func (s *Server) handleWiFiDisconnect(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		writeError(w, 405, "Method not allowed")
		return
	}
	if err := s.wifiMgr.Disconnect(); err != nil {
		writeError(w, 500, err.Error())
		return
	}
	writeJSON(w, map[string]string{"status": "ok"})
}

// Serial API handlers
func (s *Server) handleSerialPorts(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, serial.ListPorts())
}

func (s *Server) handleSerialOpen(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		writeError(w, 405, "Method not allowed")
		return
	}

	var cfg serial.Config
	if err := json.NewDecoder(r.Body).Decode(&cfg); err != nil {
		writeError(w, 400, "Invalid request")
		return
	}

	if err := s.serialReader.Open(cfg); err != nil {
		writeError(w, 500, err.Error())
		return
	}
	writeJSON(w, map[string]string{"status": "ok"})
}

func (s *Server) handleSerialClose(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		writeError(w, 405, "Method not allowed")
		return
	}
	s.serialReader.Close()
	writeJSON(w, map[string]string{"status": "ok"})
}

func (s *Server) handleSerialLogs(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, s.serialReader.GetLines(500))
}

func (s *Server) handleSerialSSE(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "SSE not supported", http.StatusInternalServerError)
		return
	}

	subID, ch := s.serialReader.Subscribe()
	defer s.serialReader.Unsubscribe(subID)

	for {
		select {
		case <-r.Context().Done():
			return
		case line, ok := <-ch:
			if !ok {
				return
			}
			data, _ := json.Marshal(line)
			fmt.Fprintf(w, "data: %s\n\n", data)
			flusher.Flush()
		}
	}
}

// File API handlers
func (s *Server) handleFileList(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Query().Get("path")
	if path == "" {
		path = "/"
	}
	files, err := s.fileMgr.List(path)
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	writeJSON(w, files)
}

func (s *Server) handleFileUpload(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		writeError(w, 405, "Method not allowed")
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, s.fileMgr.MaxSize+1024)
	if err := r.ParseMultipartForm(32 << 20); err != nil {
		writeError(w, 400, "File too large or invalid form")
		return
	}

	path := r.FormValue("path")
	if path == "" {
		path = "/"
	}

	file, header, err := r.FormFile("file")
	if err != nil {
		writeError(w, 400, "No file provided")
		return
	}
	defer file.Close()

	if err := s.fileMgr.Upload(path, file, header); err != nil {
		writeError(w, 500, err.Error())
		return
	}

	writeJSON(w, map[string]string{"status": "ok", "filename": header.Filename})
}

func (s *Server) handleFileDownload(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Query().Get("path")
	if path == "" {
		writeError(w, 400, "Path required")
		return
	}

	name, f, err := s.fileMgr.Download(path)
	if err != nil {
		writeError(w, 404, err.Error())
		return
	}
	defer f.Close()

	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=\"%s\"", name))
	w.Header().Set("Content-Type", "application/octet-stream")
	http.ServeContent(w, r, name, time.Now(), f)
}

func (s *Server) handleFileDelete(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" && r.Method != "DELETE" {
		writeError(w, 405, "Method not allowed")
		return
	}

	var req struct {
		Path string `json:"path"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, 400, "Invalid request")
		return
	}

	if err := s.fileMgr.Delete(req.Path); err != nil {
		writeError(w, 500, err.Error())
		return
	}
	writeJSON(w, map[string]string{"status": "ok"})
}

func (s *Server) handleFileMkdir(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		writeError(w, 405, "Method not allowed")
		return
	}

	var req struct {
		Path string `json:"path"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, 400, "Invalid request")
		return
	}

	if err := s.fileMgr.Mkdir(req.Path); err != nil {
		writeError(w, 500, err.Error())
		return
	}
	writeJSON(w, map[string]string{"status": "ok"})
}

func (s *Server) handleFileRename(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		writeError(w, 405, "Method not allowed")
		return
	}

	var req struct {
		OldPath string `json:"oldPath"`
		NewPath string `json:"newPath"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, 400, "Invalid request")
		return
	}

	if err := s.fileMgr.Rename(req.OldPath, req.NewPath); err != nil {
		writeError(w, 500, err.Error())
		return
	}
	writeJSON(w, map[string]string{"status": "ok"})
}

type ChronyTracking struct {
	ReferenceID    string `json:"referenceID"`
	Stratum        int    `json:"stratum"`
	RefTime        string `json:"refTime"`
	SystemTime     string `json:"systemTime"`
	LastOffset     string `json:"lastOffset"`
	RMSOffset      string `json:"rmsOffset"`
	Frequency      string `json:"frequency"`
	ResidualFreq   string `json:"residualFreq"`
	Skew           string `json:"skew"`
	RootDelay      string `json:"rootDelay"`
	RootDispersion string `json:"rootDispersion"`
	UpdateInterval string `json:"updateInterval"`
	LeapStatus     string `json:"leapStatus"`
}

type ChronySource struct {
	State    string `json:"state"`
	Name     string `json:"name"`
	Stratum  int    `json:"stratum"`
	Poll     int    `json:"poll"`
	Reach    int    `json:"reach"`
	LastRx   int    `json:"lastRx"`
	Offset   string `json:"offset"`
	Selected bool   `json:"selected"`
}

type ChronySourceStat struct {
	Name      string `json:"name"`
	NP        int    `json:"np"`
	NR        int    `json:"nr"`
	Span      string `json:"span"`
	Frequency string `json:"frequency"`
	FreqSkew  string `json:"freqSkew"`
	Offset    string `json:"offset"`
	StdDev    string `json:"stdDev"`
}

type ChronyResult struct {
	Tracking    *ChronyTracking    `json:"tracking"`
	Sources     []ChronySource     `json:"sources"`
	SourceStats []ChronySourceStat `json:"sourceStats"`
	Config      string             `json:"config"`
	Error       string             `json:"error,omitempty"`
}

func parseChronyTracking(out string) *ChronyTracking {
	t := &ChronyTracking{}
	lines := strings.Split(out, "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if idx := strings.Index(line, ":"); idx > 0 {
			key := strings.TrimSpace(line[:idx])
			val := strings.TrimSpace(line[idx+1:])
			switch key {
			case "Reference ID":
				t.ReferenceID = val
			case "Stratum":
				fmt.Sscanf(val, "%d", &t.Stratum)
			case "Ref time (UTC)":
				t.RefTime = val
			case "System time":
				t.SystemTime = val
			case "Last offset":
				t.LastOffset = val
			case "RMS offset":
				t.RMSOffset = val
			case "Frequency":
				t.Frequency = val
			case "Residual freq":
				t.ResidualFreq = val
			case "Skew":
				t.Skew = val
			case "Root delay":
				t.RootDelay = val
			case "Root dispersion":
				t.RootDispersion = val
			case "Update interval":
				t.UpdateInterval = val
			case "Leap status":
				t.LeapStatus = val
			}
		}
	}
	return t
}

func parseChronySources(out string) []ChronySource {
	var sources []ChronySource
	lines := strings.Split(out, "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if len(line) == 0 || strings.HasPrefix(line, "MS") || strings.HasPrefix(line, "==") {
			continue
		}
		parts := strings.Fields(line)
		if len(parts) < 2 {
			continue
		}
		state := parts[0]
		name := parts[1]
		s := ChronySource{State: state, Name: name}
		if len(state) >= 2 && state[len(state)-1] == '*' {
			s.Selected = true
		}
		if len(parts) > 2 {
			fmt.Sscanf(parts[2], "%d", &s.Stratum)
		}
		if len(parts) > 3 {
			fmt.Sscanf(parts[3], "%d", &s.Poll)
		}
		if len(parts) > 4 {
			fmt.Sscanf(parts[4], "%d", &s.Reach)
		}
		if len(parts) > 5 {
			fmt.Sscanf(parts[5], "%d", &s.LastRx)
		}
		if len(parts) > 6 {
			// offset field may contain multiple tokens before +/-
			offsetParts := []string{}
			for i := 6; i < len(parts); i++ {
				if parts[i] == "+/-" {
					break
				}
				offsetParts = append(offsetParts, parts[i])
			}
			s.Offset = strings.Join(offsetParts, " ")
		}
		sources = append(sources, s)
	}
	return sources
}

func parseChronySourceStats(out string) []ChronySourceStat {
	var stats []ChronySourceStat
	lines := strings.Split(out, "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if len(line) == 0 || strings.HasPrefix(line, "Name/IP") || strings.HasPrefix(line, "==") {
			continue
		}
		parts := strings.Fields(line)
		if len(parts) < 8 {
			continue
		}
		s := ChronySourceStat{Name: parts[0]}
		fmt.Sscanf(parts[1], "%d", &s.NP)
		fmt.Sscanf(parts[2], "%d", &s.NR)
		s.Span = parts[3]
		s.Frequency = parts[4]
		s.FreqSkew = parts[5]
		s.Offset = parts[6]
		s.StdDev = parts[7]
		stats = append(stats, s)
	}
	return stats
}

// Chrony API handler
func (s *Server) handleChrony(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" {
		writeError(w, 405, "Method not allowed")
		return
	}

	var res ChronyResult
	var wg sync.WaitGroup
	var mu sync.Mutex

	runCmd := func(args ...string) string {
		out, err := exec.Command("/userdata/chronyc", args...).CombinedOutput()
		if err != nil {
			return fmt.Sprintf("Error: %v\n%s", err, string(out))
		}
		return string(out)
	}

	wg.Add(4)
	go func() {
		defer wg.Done()
		out := runCmd("tracking")
		mu.Lock()
		res.Tracking = parseChronyTracking(out)
		mu.Unlock()
	}()
	go func() {
		defer wg.Done()
		out := runCmd("sources")
		mu.Lock()
		res.Sources = parseChronySources(out)
		mu.Unlock()
	}()
	go func() {
		defer wg.Done()
		out := runCmd("sourcestats")
		mu.Lock()
		res.SourceStats = parseChronySourceStats(out)
		mu.Unlock()
	}()
	go func() {
		defer wg.Done()
		out, err := os.ReadFile("/etc/chrony.conf")
		if err != nil {
			mu.Lock()
			res.Config = fmt.Sprintf("Error reading /etc/chrony.conf: %v", err)
			mu.Unlock()
			return
		}
		mu.Lock()
		res.Config = string(out)
		mu.Unlock()
	}()

	wg.Wait()
	writeJSON(w, res)
}

func (s *Server) handleChronyConfig(w http.ResponseWriter, r *http.Request) {
	if r.Method == "GET" {
		out, err := os.ReadFile("/etc/chrony.conf")
		if err != nil {
			writeError(w, 500, fmt.Sprintf("Failed to read config: %v", err))
			return
		}
		writeJSON(w, map[string]string{"config": string(out)})
		return
	}

	if r.Method == "POST" {
		var req struct {
			Config string `json:"config"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, 400, "Invalid request")
			return
		}
		if err := os.WriteFile("/etc/chrony.conf", []byte(req.Config), 0644); err != nil {
			writeError(w, 500, fmt.Sprintf("Failed to write config: %v", err))
			return
		}
		writeJSON(w, map[string]string{"status": "ok"})
		return
	}

	writeError(w, 405, "Method not allowed")
}

// handleCANTimeSync handles GET (status) and POST (configure) for CAN time sync.
//
// GET  /api/can/timesync  → { "enabled": bool, "iface": string, "protocol": string, "brs": bool }
// POST /api/can/timesync  ← { "enabled": bool, "iface": string, "protocol": string, "brs": bool }
//
//	→ { "status": "ok", "enabled": bool, "iface": string, "protocol": string }
func (s *Server) handleCANTimeSync(w http.ResponseWriter, r *http.Request) {
	if s.timeSyncSender == nil {
		writeError(w, 503, "Time sync not available")
		return
	}

	switch r.Method {
	case "GET":
		enabled, iface, protocol, brs := s.timeSyncSender.Status()
		writeJSON(w, map[string]interface{}{
			"enabled":  enabled,
			"iface":    iface,
			"protocol": string(protocol),
			"brs":      brs,
		})

	case "POST":
		var req struct {
			Enabled  bool   `json:"enabled"`
			Iface    string `json:"iface"`
			Protocol string `json:"protocol"`
			BRS      *bool  `json:"brs"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, 400, "Invalid request")
			return
		}

		// Validate interface name
		valid := false
		for _, i := range s.canIfaces {
			if i == req.Iface {
				valid = true
				break
			}
		}
		if req.Enabled && !valid {
			writeError(w, 400, "Invalid CAN interface")
			return
		}

		// Validate protocol
		proto := timesync.Protocol(req.Protocol)
		if proto != timesync.Protocol5A4 && proto != timesync.Protocol594 {
			proto = timesync.Protocol5A4
		}

		_, _, _, brs := s.timeSyncSender.Status()
		if req.BRS != nil {
			brs = *req.BRS
		}

		s.timeSyncSender.Configure(req.Enabled, req.Iface, proto, brs)
		enabled, iface, protocol, brs := s.timeSyncSender.Status()
		writeJSON(w, map[string]interface{}{
			"status":   "ok",
			"enabled":  enabled,
			"iface":    iface,
			"protocol": string(protocol),
			"brs":      brs,
		})

	default:
		writeError(w, 405, "Method not allowed")
	}
}
