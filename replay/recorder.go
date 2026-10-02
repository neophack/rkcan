//go:build linux

package replay

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/penghongxia/rkcan/can"
	"github.com/penghongxia/rkcan/canlog"
)

// RecordConfig selects what to record and where.
type RecordConfig struct {
	Dir       string   `json:"dir"`       // directory for the .asc file
	Ifaces    []string `json:"ifaces"`    // interfaces to record (empty = all)
	MaxSizeMB int      `json:"maxSizeMB"` // start a new file after this size (0 = no limit)
}

// RecordStatus is a snapshot of the recorder.
type RecordStatus struct {
	Active  bool     `json:"active"`
	File    string   `json:"file"`
	Files   []string `json:"files"` // files written in this session
	Ifaces  []string `json:"ifaces"`
	Frames  uint64   `json:"frames"`
	Dropped uint64   `json:"dropped"`
	Bytes   int64    `json:"bytes"`
	Started string   `json:"started,omitempty"`
	Error   string   `json:"error,omitempty"`
}

type recEvent struct {
	ts      time.Time
	channel int
	msg     can.Message
}

// Recorder writes received frames to ASC files. Observe never blocks the
// receive path: when the writer falls behind, frames are dropped and counted.
type Recorder struct {
	mu      sync.Mutex
	st      RecordStatus
	ifaces  map[string]int // iface -> ASC channel
	events  chan recEvent
	done    chan struct{}
	maxSize int64
	dir     string
}

// NewRecorder returns an idle recorder.
func NewRecorder() *Recorder { return &Recorder{} }

// Start begins recording. channelOf maps each interface to its 1-based ASC
// channel number (e.g. can0 -> 1, can1 -> 2).
func (r *Recorder) Start(cfg RecordConfig, channelOf map[string]int) error {
	r.mu.Lock()
	if r.st.Active {
		r.mu.Unlock()
		return fmt.Errorf("recording already active")
	}
	r.mu.Unlock()
	r.reap() // writer of a session that failed on its own

	r.mu.Lock()
	defer r.mu.Unlock()
	if r.events != nil {
		return fmt.Errorf("recording already active")
	}
	if cfg.MaxSizeMB < 0 {
		return fmt.Errorf("maxSizeMB must be >= 0")
	}
	if err := os.MkdirAll(cfg.Dir, 0755); err != nil {
		return fmt.Errorf("create %s: %w", cfg.Dir, err)
	}

	sel := map[string]int{}
	if len(cfg.Ifaces) == 0 {
		for k, v := range channelOf {
			sel[k] = v
		}
	} else {
		for _, name := range cfg.Ifaces {
			ch, ok := channelOf[name]
			if !ok {
				return fmt.Errorf("unknown interface %q", name)
			}
			sel[name] = ch
		}
	}
	if len(sel) == 0 {
		return fmt.Errorf("no interfaces to record")
	}

	w, f, path, err := createASC(cfg.Dir, time.Now())
	if err != nil {
		return err
	}

	names := make([]string, 0, len(sel))
	for k := range sel {
		names = append(names, k)
	}

	r.ifaces = sel
	r.dir = cfg.Dir
	r.maxSize = int64(cfg.MaxSizeMB) << 20
	r.events = make(chan recEvent, 65536)
	r.done = make(chan struct{})
	r.st = RecordStatus{
		Active:  true,
		File:    path,
		Files:   []string{path},
		Ifaces:  names,
		Started: time.Now().Format("2006-01-02 15:04:05"),
	}
	go r.writeLoop(w, f, r.events, r.done)
	return nil
}

func createASC(dir string, now time.Time) (*canlog.ASCWriter, *os.File, string, error) {
	path := filepath.Join(dir, "rkcan_"+now.Format("20060102_150405")+".asc")
	for i := 1; ; i++ {
		if _, err := os.Stat(path); os.IsNotExist(err) {
			break
		}
		path = filepath.Join(dir, fmt.Sprintf("rkcan_%s_%d.asc", now.Format("20060102_150405"), i))
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0644)
	if err != nil {
		return nil, nil, "", fmt.Errorf("create log file: %w", err)
	}
	w, err := canlog.NewASCWriter(f, now)
	if err != nil {
		f.Close()
		return nil, nil, "", err
	}
	return w, f, path, nil
}

// Observe queues a received frame. Safe to call from any goroutine.
func (r *Recorder) Observe(iface string, msg *can.Message, ts time.Time) {
	r.mu.Lock()
	if !r.st.Active {
		r.mu.Unlock()
		return
	}
	ch, ok := r.ifaces[iface]
	if !ok || r.events == nil {
		r.mu.Unlock()
		return
	}
	// Send under the lock so reap cannot close the channel concurrently
	select {
	case r.events <- recEvent{ts: ts, channel: ch, msg: *msg}:
	default:
		r.st.Dropped++
	}
	r.mu.Unlock()
}

// Stop ends recording and closes the file.
func (r *Recorder) Stop() RecordStatus {
	r.reap()
	return r.Status()
}

// reap stops the writer goroutine if one exists and waits for it.
func (r *Recorder) reap() {
	r.mu.Lock()
	events, done := r.events, r.done
	r.events, r.done = nil, nil
	r.st.Active = false
	r.mu.Unlock()
	if events == nil {
		return
	}
	close(events)
	<-done
}

// Status returns a snapshot.
func (r *Recorder) Status() RecordStatus {
	r.mu.Lock()
	defer r.mu.Unlock()
	st := r.st
	st.Files = append([]string(nil), r.st.Files...)
	st.Ifaces = append([]string(nil), r.st.Ifaces...)
	return st
}

func (r *Recorder) fail(err error) {
	log.Printf("recorder: %v", err)
	r.mu.Lock()
	r.st.Error = err.Error()
	r.st.Active = false
	r.mu.Unlock()
}

func (r *Recorder) writeLoop(w *canlog.ASCWriter, f *os.File, events chan recEvent, done chan struct{}) {
	defer close(done)

	closeFile := func() {
		if w != nil {
			w.Close()
			f.Sync()
			f.Close()
			w = nil
		}
	}
	defer closeFile()

	flush := time.NewTicker(time.Second)
	defer flush.Stop()

	var frame canlog.Frame
	for {
		select {
		case ev, ok := <-events:
			if !ok {
				return
			}
			if w == nil {
				continue // failed earlier; drain until Stop
			}
			m := &ev.msg
			frame = canlog.Frame{
				Channel:  ev.channel,
				ID:       m.GetActualID(),
				Extended: m.IsExtended(),
				Remote:   m.IsRTR(),
				FD:       m.FD,
				BRS:      m.FD && m.HasBRS(),
				ESI:      m.FD && m.HasESI(),
				Data:     m.GetData(),
			}
			if frame.Remote {
				frame.Data = nil
			}
			if err := w.Write(ev.ts, &frame); err != nil {
				r.fail(fmt.Errorf("write: %w", err))
				closeFile()
				continue
			}
			r.mu.Lock()
			r.st.Frames++
			r.mu.Unlock()

		case <-flush.C:
			if w == nil {
				continue
			}
			if err := w.Flush(); err != nil {
				r.fail(fmt.Errorf("write: %w (card full or removed?)", err))
				closeFile()
				continue
			}
			var size int64
			if st, err := f.Stat(); err == nil {
				size = st.Size()
			}
			r.mu.Lock()
			r.st.Bytes = size
			maxSize, dir := r.maxSize, r.dir
			r.mu.Unlock()

			if maxSize > 0 && size >= maxSize {
				closeFile()
				nw, nf, path, err := createASC(dir, time.Now())
				if err != nil {
					r.fail(err)
					continue
				}
				w, f = nw, nf
				r.mu.Lock()
				r.st.File = path
				r.st.Files = append(r.st.Files, path)
				r.st.Bytes = 0
				r.mu.Unlock()
			}
		}
	}
}
