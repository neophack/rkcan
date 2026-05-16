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
	"os/exec"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/penghongxia/rkcan/candiag"
	"github.com/penghongxia/rkcan/filemanager"
	"github.com/penghongxia/rkcan/serial"
	"github.com/penghongxia/rkcan/system"
	"github.com/penghongxia/rkcan/wifi"
)

//go:embed static
var staticFiles embed.FS

type CANStats struct {
	Can0FPS        uint64  `json:"can0Fps"`
	Can0Total      uint64  `json:"can0Total"`
	Can0Bitrate    uint32  `json:"can0Bitrate"`
	Can0State      string  `json:"can0State"`
	Can0SamplePt   float64 `json:"can0SamplePt"`
	Can0DBitrate   uint32  `json:"can0DBitrate"`
	Can0DSamplePt  float64 `json:"can0DSamplePt"`
	Can0BusState   string  `json:"can0BusState"`
	Can1FPS        uint64  `json:"can1Fps"`
	Can1Total      uint64  `json:"can1Total"`
	Can1Bitrate    uint32  `json:"can1Bitrate"`
	Can1State      string  `json:"can1State"`
	Can1SamplePt   float64 `json:"can1SamplePt"`
	Can1DBitrate   uint32  `json:"can1DBitrate"`
	Can1DSamplePt  float64 `json:"can1DSamplePt"`
	Can1BusState   string  `json:"can1BusState"`
	UDPSentPkts    uint64  `json:"udpSentPkts"`
	UDPErrors      uint64  `json:"udpErrors"`
	SentFrames     uint64  `json:"sentFrames"`
}

type CANStatsProvider struct {
	RecvFrames0 *uint64
	RecvFrames1 *uint64
	SentFrames  *uint64
	SentPkts    *uint64
	WriteErrs   *uint64
	Ifaces      []string

	mu        sync.RWMutex
	stats     CANStats
	prevRecv0 uint64
	prevRecv1 uint64
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

	p.mu.Lock()
	p.stats.Can0FPS = recv0 - p.prevRecv0
	p.stats.Can1FPS = recv1 - p.prevRecv1
	p.stats.Can0Total = recv0
	p.stats.Can1Total = recv1
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
	p.mu.Unlock()
}

func (p *CANStatsProvider) Get() CANStats {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.stats
}

type Server struct {
	httpServer   *http.Server
	sysCollector *system.Collector
	canStats     *CANStatsProvider
	wifiMgr      *wifi.Manager
	serialReader *serial.Reader
	fileMgr      *filemanager.Manager
	canIfaces    []string
}

type ServerConfig struct {
	Port         int
	SysCollector *system.Collector
	CANStats     *CANStatsProvider
	WifiMgr      *wifi.Manager
	SerialReader *serial.Reader
	FileMgr      *filemanager.Manager
	CANIfaces    []string
}

func NewServer(cfg ServerConfig) *Server {
	s := &Server{
		sysCollector: cfg.SysCollector,
		canStats:     cfg.CANStats,
		wifiMgr:      cfg.WifiMgr,
		serialReader: cfg.SerialReader,
		fileMgr:      cfg.FileMgr,
		canIfaces:    cfg.CANIfaces,
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
	mux.HandleFunc("/api/system/dmesg", s.handleDmesg)
	mux.HandleFunc("/api/system/poweroff", s.handlePoweroff)

	// CAN APIs
	mux.HandleFunc("/api/can/stats", s.handleCANStats)
	mux.HandleFunc("/api/can/diagnostics", s.handleCANDiagnostics)
	mux.HandleFunc("/api/can/details", s.handleCANDetails)
	mux.HandleFunc("/api/can/configure", s.handleCANConfigure)

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
