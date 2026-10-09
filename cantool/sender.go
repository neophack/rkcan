//go:build linux

package cantool

import (
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/penghongxia/rkcan/can"
)

// FrameSpec is a frame description from the UI.
type FrameSpec struct {
	Iface string `json:"iface"`
	ID    string `json:"id"`   // hex, e.g. "123" or "18FEF100"
	Ext   bool   `json:"ext"`  // 29-bit identifier
	FD    bool   `json:"fd"`   // CAN-FD frame
	BRS   bool   `json:"brs"`  // bit rate switch (CAN-FD only)
	RTR   bool   `json:"rtr"`  // remote frame (classic only)
	Data  string `json:"data"` // hex bytes, separators allowed
}

var validFDLen = map[int]bool{0: true, 1: true, 2: true, 3: true, 4: true, 5: true, 6: true, 7: true, 8: true,
	12: true, 16: true, 20: true, 24: true, 32: true, 48: true, 64: true}

// Build validates the spec and returns the frame to send.
func (s FrameSpec) Build() (*can.Message, error) {
	idStr := strings.TrimPrefix(strings.TrimPrefix(strings.TrimSpace(s.ID), "0x"), "0X")
	id, err := strconv.ParseUint(idStr, 16, 32)
	if err != nil || idStr == "" {
		return nil, fmt.Errorf("invalid CAN ID %q (hex expected)", s.ID)
	}
	ext := s.Ext || id > can.CAN_SFF_MASK
	if ext && id > can.CAN_EFF_MASK {
		return nil, fmt.Errorf("CAN ID 0x%X exceeds 29 bits", id)
	}

	clean := strings.NewReplacer(" ", "", ",", "", ":", "", "-", "", "0x", "", "0X", "", "\t", "").Replace(s.Data)
	data, err := hex.DecodeString(clean)
	if err != nil {
		return nil, fmt.Errorf("invalid data (hex bytes expected)")
	}

	if s.FD {
		if s.RTR {
			return nil, fmt.Errorf("CAN-FD has no remote frames")
		}
		if !validFDLen[len(data)] {
			return nil, fmt.Errorf("CAN-FD data length %d is not valid (0-8, 12, 16, 20, 24, 32, 48, 64)", len(data))
		}
	} else {
		if s.BRS {
			return nil, fmt.Errorf("BRS requires a CAN-FD frame")
		}
		if len(data) > 8 {
			return nil, fmt.Errorf("classic CAN carries at most 8 bytes (enable CAN-FD for more)")
		}
		if s.RTR {
			data = nil
		}
	}

	fid := uint32(id)
	if ext {
		fid |= can.CAN_EFF_FLAG
	}
	var msg *can.Message
	if s.FD {
		msg = can.NewFDMessage(fid, data, s.BRS)
	} else {
		msg = can.NewClassicMessage(fid, data)
		if s.RTR {
			msg.ID |= can.CAN_RTR_FLAG
		}
	}
	return msg, nil
}

// Task is a periodic transmission.
type Task struct {
	ID         int       `json:"id"`
	Spec       FrameSpec `json:"spec"`
	IntervalMs int       `json:"intervalMs"`
	Count      int       `json:"count"` // 0 = until stopped
	Sent       int       `json:"sent"`
	Errors     int       `json:"errors"`
	LastError  string    `json:"lastError,omitempty"`
	Running    bool      `json:"running"`
	stop       chan struct{}
}

// Sender sends single frames and runs periodic tasks over a TX pool.
type Sender struct {
	pool   *can.TxPool
	mu     sync.Mutex
	tasks  map[int]*Task
	nextID int
}

// NewSender returns a sender using pool.
func NewSender(pool *can.TxPool) *Sender {
	return &Sender{pool: pool, tasks: make(map[int]*Task), nextID: 1}
}

// SendOnce transmits one frame.
func (s *Sender) SendOnce(spec FrameSpec) error {
	msg, err := spec.Build()
	if err != nil {
		return err
	}
	return s.pool.Send(spec.Iface, msg)
}

// maxTasks bounds the number of concurrent periodic tasks.
const maxTasks = 64

// StartPeriodic starts a periodic task and returns its ID.
func (s *Sender) StartPeriodic(spec FrameSpec, intervalMs, count int) (int, error) {
	if intervalMs < 1 || intervalMs > 3600000 {
		return 0, fmt.Errorf("interval must be 1..3600000 ms")
	}
	if count < 0 {
		return 0, fmt.Errorf("count must be >= 0")
	}
	msg, err := spec.Build()
	if err != nil {
		return 0, err
	}
	if _, err := s.pool.Bus(spec.Iface); err != nil {
		return 0, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	running := 0
	for _, t := range s.tasks {
		if t.Running {
			running++
		}
	}
	if running >= maxTasks {
		return 0, fmt.Errorf("too many periodic tasks (max %d)", maxTasks)
	}
	t := &Task{ID: s.nextID, Spec: spec, IntervalMs: intervalMs, Count: count, Running: true, stop: make(chan struct{})}
	s.nextID++
	s.tasks[t.ID] = t
	go s.runTask(t, msg)
	return t.ID, nil
}

func (s *Sender) runTask(t *Task, msg *can.Message) {
	ticker := time.NewTicker(time.Duration(t.IntervalMs) * time.Millisecond)
	defer ticker.Stop()
	for {
		err := s.pool.Send(t.Spec.Iface, msg)
		s.mu.Lock()
		if err != nil {
			t.Errors++
			t.LastError = err.Error()
		} else {
			t.Sent++
		}
		done := t.Count > 0 && t.Sent+t.Errors >= t.Count
		if done {
			t.Running = false
		}
		s.mu.Unlock()
		if done {
			return
		}
		select {
		case <-t.stop:
			return
		case <-ticker.C:
		}
	}
}

// Stop stops a task (id 0 stops all) and removes it from the list.
func (s *Sender) Stop(id int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for tid, t := range s.tasks {
		if id != 0 && tid != id {
			continue
		}
		if t.Running {
			t.Running = false
			close(t.stop)
		}
		delete(s.tasks, tid)
	}
}

// Tasks lists tasks.
func (s *Sender) Tasks() []Task {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Task, 0, len(s.tasks))
	for i := 1; i < s.nextID; i++ {
		if t, ok := s.tasks[i]; ok {
			c := *t
			c.stop = nil
			out = append(out, c)
		}
	}
	return out
}
