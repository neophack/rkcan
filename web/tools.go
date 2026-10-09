//go:build linux

package web

import (
	"encoding/json"
	"net/http"
	"path/filepath"
	"strings"

	"github.com/penghongxia/rkcan/cantool"
	"github.com/penghongxia/rkcan/replay"
)

// decodeJSON decodes a size-limited JSON body.
func decodeJSON(w http.ResponseWriter, r *http.Request, v interface{}) bool {
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(v); err != nil {
		writeError(w, 400, "Invalid request")
		return false
	}
	return true
}

func requirePost(w http.ResponseWriter, r *http.Request) bool {
	if r.Method != "POST" {
		writeError(w, 405, "Method not allowed")
		return false
	}
	return true
}

func ok(w http.ResponseWriter) { writeJSON(w, map[string]string{"status": "ok"}) }

func (s *Server) registerToolRoutes(mux *http.ServeMux) {
	// CAN transmit and monitor
	mux.HandleFunc("/api/can/send", s.handleCANSend)
	mux.HandleFunc("/api/can/periodic", s.handleCANPeriodic)
	mux.HandleFunc("/api/can/periodic/stop", s.handleCANPeriodicStop)
	mux.HandleFunc("/api/can/monitor", s.handleCANMonitor)
	mux.HandleFunc("/api/can/monitor/reset", s.handleCANMonitorReset)
	mux.HandleFunc("/api/can/monitor/pause", s.handleCANMonitorPause)

	// Storage (TF/SD card)
	mux.HandleFunc("/api/storage", s.handleStorage)
	mux.HandleFunc("/api/storage/mount", s.handleStorageMount)
	mux.HandleFunc("/api/storage/unmount", s.handleStorageUnmount)

	// Replay
	mux.HandleFunc("/api/replay/files", s.handleReplayFiles)
	mux.HandleFunc("/api/replay/info", s.handleReplayInfo)
	mux.HandleFunc("/api/replay/start", s.handleReplayStart)
	mux.HandleFunc("/api/replay/stop", s.handleReplayStop)
	mux.HandleFunc("/api/replay/pause", s.handleReplayPause)
	mux.HandleFunc("/api/replay/resume", s.handleReplayResume)
	mux.HandleFunc("/api/replay/speed", s.handleReplaySpeed)
	mux.HandleFunc("/api/replay/status", s.handleReplayStatus)

	// Recording
	mux.HandleFunc("/api/record/start", s.handleRecordStart)
	mux.HandleFunc("/api/record/stop", s.handleRecordStop)
	mux.HandleFunc("/api/record/status", s.handleRecordStatus)

	// WiFi saved networks
	mux.HandleFunc("/api/wifi/saved", s.handleWiFiSaved)
	mux.HandleFunc("/api/wifi/saved/connect", s.handleWiFiSavedConnect)
	mux.HandleFunc("/api/wifi/saved/forget", s.handleWiFiSavedForget)
}

// ---- CAN transmit / monitor ----

func (s *Server) handleCANSend(w http.ResponseWriter, r *http.Request) {
	if !requirePost(w, r) {
		return
	}
	var spec cantool.FrameSpec
	if !decodeJSON(w, r, &spec) {
		return
	}
	if !s.validIface(spec.Iface) {
		writeError(w, 400, "Invalid interface")
		return
	}
	if err := s.canSender.SendOnce(spec); err != nil {
		writeError(w, 400, err.Error())
		return
	}
	ok(w)
}

// GET: list tasks. POST {spec..., intervalMs, count}: start a task.
func (s *Server) handleCANPeriodic(w http.ResponseWriter, r *http.Request) {
	if r.Method == "GET" {
		writeJSON(w, s.canSender.Tasks())
		return
	}
	if !requirePost(w, r) {
		return
	}
	var req struct {
		cantool.FrameSpec
		IntervalMs int `json:"intervalMs"`
		Count      int `json:"count"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	if !s.validIface(req.Iface) {
		writeError(w, 400, "Invalid interface")
		return
	}
	id, err := s.canSender.StartPeriodic(req.FrameSpec, req.IntervalMs, req.Count)
	if err != nil {
		writeError(w, 400, err.Error())
		return
	}
	writeJSON(w, map[string]interface{}{"status": "ok", "id": id})
}

func (s *Server) handleCANPeriodicStop(w http.ResponseWriter, r *http.Request) {
	if !requirePost(w, r) {
		return
	}
	var req struct {
		ID int `json:"id"` // 0 = all
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	s.canSender.Stop(req.ID)
	ok(w)
}

func (s *Server) handleCANMonitor(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, s.monitor.Snapshot())
}

func (s *Server) handleCANMonitorReset(w http.ResponseWriter, r *http.Request) {
	if !requirePost(w, r) {
		return
	}
	s.monitor.Reset()
	ok(w)
}

func (s *Server) handleCANMonitorPause(w http.ResponseWriter, r *http.Request) {
	if !requirePost(w, r) {
		return
	}
	var req struct {
		Paused bool `json:"paused"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	s.monitor.SetPaused(req.Paused)
	ok(w)
}

// ---- Storage ----

func (s *Server) handleStorage(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, map[string]interface{}{
		"volumes": replay.Volumes(),
		"cards":   replay.UnmountedCards(),
	})
}

func (s *Server) handleStorageMount(w http.ResponseWriter, r *http.Request) {
	if !requirePost(w, r) {
		return
	}
	var req struct {
		Device string `json:"device"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	dir, err := replay.MountCard(req.Device)
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	writeJSON(w, map[string]string{"status": "ok", "mountPoint": dir})
}

func (s *Server) handleStorageUnmount(w http.ResponseWriter, r *http.Request) {
	if !requirePost(w, r) {
		return
	}
	var req struct {
		MountPoint string `json:"mountPoint"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	// Release files on the card before unmounting
	if st := s.player.Status(); within(st.Path, req.MountPoint) {
		s.player.Stop()
	}
	if st := s.recorder.Status(); st.Active && within(st.File, req.MountPoint) {
		s.recorder.Stop()
	}
	if err := replay.Unmount(req.MountPoint); err != nil {
		writeError(w, 500, err.Error())
		return
	}
	ok(w)
}

// within reports whether path is dir or below it.
func within(path, dir string) bool {
	if path == "" || dir == "" {
		return false
	}
	path, dir = filepath.Clean(path), filepath.Clean(dir)
	return path == dir || strings.HasPrefix(path, dir+string(filepath.Separator))
}

// ---- Replay ----

// GET /api/replay/files?dir=<optional> lists .asc/.blf files.
func (s *Server) handleReplayFiles(w http.ResponseWriter, r *http.Request) {
	dir := r.URL.Query().Get("dir")
	files := []replay.LogFile{}
	if dir != "" {
		if !replay.AllowedPath(dir) {
			writeError(w, 400, "Directory is not on a storage volume")
			return
		}
		files = append(files, replay.FindLogs(dir, 4, 1000)...)
	} else {
		for _, v := range replay.Volumes() {
			files = append(files, replay.FindLogs(v.MountPoint, 4, 1000)...)
		}
	}
	writeJSON(w, files)
}

func (s *Server) handleReplayInfo(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Query().Get("path")
	if !replay.AllowedPath(path) {
		writeError(w, 400, "File is not on a storage volume")
		return
	}
	info, err := replay.ScanFile(r.Context(), path)
	if err != nil {
		writeError(w, 400, err.Error())
		return
	}
	writeJSON(w, info)
}

func (s *Server) handleReplayStart(w http.ResponseWriter, r *http.Request) {
	if !requirePost(w, r) {
		return
	}
	var cfg replay.Config
	if !decodeJSON(w, r, &cfg) {
		return
	}
	if !replay.AllowedPath(cfg.Path) {
		writeError(w, 400, "File is not on a storage volume")
		return
	}
	for _, iface := range cfg.ChannelMap {
		if iface != "" && !s.validIface(iface) {
			writeError(w, 400, "Invalid interface: "+iface)
			return
		}
	}
	if err := s.player.Start(cfg); err != nil {
		writeError(w, 400, err.Error())
		return
	}
	writeJSON(w, s.player.Status())
}

func (s *Server) handleReplayStop(w http.ResponseWriter, r *http.Request) {
	if !requirePost(w, r) {
		return
	}
	s.player.Stop()
	writeJSON(w, s.player.Status())
}

func (s *Server) handleReplayPause(w http.ResponseWriter, r *http.Request) {
	if !requirePost(w, r) {
		return
	}
	if err := s.player.Pause(); err != nil {
		writeError(w, 400, err.Error())
		return
	}
	writeJSON(w, s.player.Status())
}

func (s *Server) handleReplayResume(w http.ResponseWriter, r *http.Request) {
	if !requirePost(w, r) {
		return
	}
	if err := s.player.Resume(); err != nil {
		writeError(w, 400, err.Error())
		return
	}
	writeJSON(w, s.player.Status())
}

func (s *Server) handleReplaySpeed(w http.ResponseWriter, r *http.Request) {
	if !requirePost(w, r) {
		return
	}
	var req struct {
		Speed float64 `json:"speed"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	if err := s.player.SetSpeed(req.Speed); err != nil {
		writeError(w, 400, err.Error())
		return
	}
	writeJSON(w, s.player.Status())
}

func (s *Server) handleReplayStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, s.player.Status())
}

// ---- Recording ----

func (s *Server) handleRecordStart(w http.ResponseWriter, r *http.Request) {
	if !requirePost(w, r) {
		return
	}
	var cfg replay.RecordConfig
	if !decodeJSON(w, r, &cfg) {
		return
	}
	if !replay.AllowedPath(cfg.Dir) {
		writeError(w, 400, "Directory is not on a storage volume")
		return
	}
	channels := map[string]int{}
	for i, iface := range s.canIfaces {
		channels[iface] = s.canChannel(iface, i)
	}
	if err := s.recorder.Start(cfg, channels); err != nil {
		writeError(w, 400, err.Error())
		return
	}
	writeJSON(w, s.recorder.Status())
}

// canChannel returns the 1-based log channel for an interface: the position
// of the interface in the -can0/-can1 flags.
func (s *Server) canChannel(iface string, fallback int) int {
	if ch, ok := s.canChannels[iface]; ok {
		return ch
	}
	return fallback + 1
}

func (s *Server) handleRecordStop(w http.ResponseWriter, r *http.Request) {
	if !requirePost(w, r) {
		return
	}
	writeJSON(w, s.recorder.Stop())
}

func (s *Server) handleRecordStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, s.recorder.Status())
}

// ---- WiFi saved networks ----

func (s *Server) handleWiFiSaved(w http.ResponseWriter, r *http.Request) {
	nets, err := s.wifiMgr.Saved()
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	writeJSON(w, nets)
}

func (s *Server) handleWiFiSavedConnect(w http.ResponseWriter, r *http.Request) {
	if !requirePost(w, r) {
		return
	}
	var req struct {
		ID string `json:"id"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	if err := s.wifiMgr.ConnectSaved(req.ID); err != nil {
		writeError(w, 500, err.Error())
		return
	}
	ok(w)
}

func (s *Server) handleWiFiSavedForget(w http.ResponseWriter, r *http.Request) {
	if !requirePost(w, r) {
		return
	}
	var req struct {
		ID string `json:"id"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	if err := s.wifiMgr.Forget(req.ID); err != nil {
		writeError(w, 500, err.Error())
		return
	}
	ok(w)
}
