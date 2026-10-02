//go:build linux

// Package cantool provides the interactive CAN tools behind the dashboard:
// a live per-ID traffic monitor and periodic frame transmission.
package cantool

import (
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/penghongxia/rkcan/can"
)

// maxEntries bounds memory when a bus carries very many distinct IDs.
const maxEntries = 4096

type monKey struct {
	iface string
	id    uint32 // includes EFF flag to separate 11/29-bit IDs
}

type monEntry struct {
	count    uint64
	last     time.Time
	periodMs float64
	msg      can.Message
}

// Entry is one row of the monitor table.
type Entry struct {
	Iface    string  `json:"iface"`
	ID       string  `json:"id"` // hex, 3 or 8 digits
	Ext      bool    `json:"ext"`
	FD       bool    `json:"fd"`
	BRS      bool    `json:"brs"`
	RTR      bool    `json:"rtr"`
	Len      int     `json:"len"`
	Data     string  `json:"data"` // hex bytes separated by spaces
	Count    uint64  `json:"count"`
	PeriodMs float64 `json:"periodMs"`
	AgeMs    int64   `json:"ageMs"`
}

// Monitor aggregates received frames per interface and CAN ID.
type Monitor struct {
	mu      sync.Mutex
	entries map[monKey]*monEntry
	total   map[string]uint64
	dropped uint64
	paused  bool
}

// NewMonitor returns an empty monitor.
func NewMonitor() *Monitor {
	return &Monitor{entries: make(map[monKey]*monEntry), total: make(map[string]uint64)}
}

// Observe records a received frame. Safe for concurrent use.
func (m *Monitor) Observe(iface string, msg *can.Message, ts time.Time) {
	key := monKey{iface, msg.ID & (can.CAN_EFF_FLAG | can.CAN_EFF_MASK)}
	if !msg.IsExtended() {
		key.id = msg.ID & can.CAN_SFF_MASK
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	m.total[iface]++
	if m.paused {
		return
	}
	e, ok := m.entries[key]
	if !ok {
		if len(m.entries) >= maxEntries {
			m.dropped++
			return
		}
		e = &monEntry{}
		m.entries[key] = e
	} else if !e.last.IsZero() {
		dt := float64(ts.Sub(e.last)) / float64(time.Millisecond)
		if dt >= 0 {
			if e.periodMs == 0 {
				e.periodMs = dt
			} else {
				e.periodMs = e.periodMs*0.8 + dt*0.2 // smoothed cycle time
			}
		}
	}
	e.count++
	e.last = ts
	e.msg = *msg
}

// SetPaused freezes the table (counters keep running).
func (m *Monitor) SetPaused(p bool) {
	m.mu.Lock()
	m.paused = p
	m.mu.Unlock()
}

// Reset clears the table.
func (m *Monitor) Reset() {
	m.mu.Lock()
	m.entries = make(map[monKey]*monEntry)
	m.total = make(map[string]uint64)
	m.dropped = 0
	m.mu.Unlock()
}

// Snapshot is the monitor state sent to the UI.
type Snapshot struct {
	Entries []Entry           `json:"entries"`
	Totals  map[string]uint64 `json:"totals"`
	Dropped uint64            `json:"dropped"`
	Paused  bool              `json:"paused"`
}

// Snapshot returns the table sorted by interface and ID.
func (m *Monitor) Snapshot() Snapshot {
	now := time.Now()
	m.mu.Lock()
	snap := Snapshot{
		Entries: make([]Entry, 0, len(m.entries)),
		Totals:  make(map[string]uint64, len(m.total)),
		Dropped: m.dropped,
		Paused:  m.paused,
	}
	for k, v := range m.total {
		snap.Totals[k] = v
	}
	for k, e := range m.entries {
		msg := &e.msg
		id := msg.GetActualID()
		idStr := fmt.Sprintf("%03X", id)
		if msg.IsExtended() {
			idStr = fmt.Sprintf("%08X", id)
		}
		data := ""
		if !msg.IsRTR() {
			data = formatHex(msg.GetData())
		}
		snap.Entries = append(snap.Entries, Entry{
			Iface:    k.iface,
			ID:       idStr,
			Ext:      msg.IsExtended(),
			FD:       msg.FD,
			BRS:      msg.FD && msg.HasBRS(),
			RTR:      msg.IsRTR(),
			Len:      int(msg.Length),
			Data:     data,
			Count:    e.count,
			PeriodMs: e.periodMs,
			AgeMs:    now.Sub(e.last).Milliseconds(),
		})
	}
	m.mu.Unlock()

	sort.Slice(snap.Entries, func(i, j int) bool {
		a, b := snap.Entries[i], snap.Entries[j]
		if a.Iface != b.Iface {
			return a.Iface < b.Iface
		}
		if a.Ext != b.Ext {
			return !a.Ext
		}
		return a.ID < b.ID
	})
	return snap
}

func formatHex(b []byte) string {
	const digits = "0123456789ABCDEF"
	out := make([]byte, 0, len(b)*3)
	for i, v := range b {
		if i > 0 {
			out = append(out, ' ')
		}
		out = append(out, digits[v>>4], digits[v&0x0F])
	}
	return string(out)
}
