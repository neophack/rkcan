//go:build linux

// Package replay plays CAN log files (ASC/BLF) onto CAN interfaces, records
// live traffic to ASC files, and manages removable storage (TF/SD cards).
package replay

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"math"
	"net"
	"sync"
	"time"

	"github.com/penghongxia/rkcan/can"
	"github.com/penghongxia/rkcan/canlog"
)

// Player states
const (
	StateIdle     = "idle"
	StatePlaying  = "playing"
	StatePaused   = "paused"
	StateFinished = "finished"
	StateStopped  = "stopped"
	StateError    = "error"
)

// BRS handling for CAN-FD frames
const (
	BRSKeep = "keep" // as recorded
	BRSOn   = "on"   // force BRS on all CAN-FD frames
	BRSOff  = "off"  // force BRS off
)

// Direction filter
const (
	DirAll = "all"
	DirRx  = "rx"
	DirTx  = "tx"
)

// Sender transmits frames. Implemented by busSet; replaceable in tests.
type Sender interface {
	Send(ctx context.Context, iface string, msg *can.Message) error
	IsFD(iface string) (bool, error)
	Close()
}

// Config describes one replay session.
type Config struct {
	Path       string         `json:"path"`
	Speed      float64        `json:"speed"`      // 1 = real time, 2 = twice as fast; 0 = as fast as possible
	Loop       bool           `json:"loop"`       // restart at the end
	ChannelMap map[int]string `json:"channelMap"` // log channel -> interface ("" = skip)
	BRS        string         `json:"brs"`        // keep | on | off
	Direction  string         `json:"direction"`  // all | rx | tx
}

// Status is a snapshot of the player.
type Status struct {
	State     string         `json:"state"`
	Path      string         `json:"path"`
	Speed     float64        `json:"speed"`
	Loop      bool           `json:"loop"`
	BRS       string         `json:"brs"`
	Direction string         `json:"direction"`
	Channels  map[int]string `json:"channelMap"`
	Position  float64        `json:"position"` // seconds into the log
	Duration  float64        `json:"duration"` // seconds, 0 until known
	Total     int64          `json:"total"`    // frames in file, 0 until known
	Sent      uint64         `json:"sent"`
	Skipped   uint64         `json:"skipped"`
	Errors    uint64         `json:"errors"`
	Loops     int            `json:"loops"`
	LagMs     float64        `json:"lagMs"` // how far sending is behind schedule
	Error     string         `json:"error,omitempty"`
}

// Player replays one log file at a time.
type Player struct {
	newSender  func() Sender
	checkIface func(string) error

	mu     sync.Mutex
	st     Status
	cancel context.CancelFunc
	done   chan struct{}

	// Logical clock: position(now) = offset + (now - anchor) * speed
	paused  bool
	offset  time.Duration
	anchor  time.Time
	wake    chan struct{}
	lastErr time.Time
}

// NewPlayer returns a player that transmits on real CAN interfaces.
func NewPlayer() *Player {
	p := newPlayer(func() Sender { return newBusSet() })
	p.checkIface = ifaceUp
	return p
}

func newPlayer(newSender func() Sender) *Player {
	return &Player{newSender: newSender, st: Status{State: StateIdle}, wake: make(chan struct{}, 1)}
}

// ifaceUp checks that a CAN interface exists and is up.
func ifaceUp(name string) error {
	ifc, err := net.InterfaceByName(name)
	if err != nil {
		return fmt.Errorf("interface %s not found", name)
	}
	if ifc.Flags&net.FlagUp == 0 {
		return fmt.Errorf("interface %s is down", name)
	}
	return nil
}

// Start begins a new replay, stopping any replay in progress.
func (p *Player) Start(cfg Config) error {
	if cfg.Speed < 0 || cfg.Speed > 1000 || math.IsNaN(cfg.Speed) {
		return fmt.Errorf("speed must be between 0 (max) and 1000")
	}
	switch cfg.BRS {
	case "":
		cfg.BRS = BRSKeep
	case BRSKeep, BRSOn, BRSOff:
	default:
		return fmt.Errorf("brs must be keep, on or off")
	}
	switch cfg.Direction {
	case "":
		cfg.Direction = DirAll
	case DirAll, DirRx, DirTx:
	default:
		return fmt.Errorf("direction must be all, rx or tx")
	}
	if len(cfg.ChannelMap) == 0 {
		return fmt.Errorf("no channel mapping: map at least one log channel to an interface")
	}
	mapped := false
	for _, iface := range cfg.ChannelMap {
		if iface != "" {
			mapped = true
		}
	}
	if !mapped {
		return fmt.Errorf("all channels are set to skip")
	}
	if p.checkIface != nil {
		for _, iface := range cfg.ChannelMap {
			if iface == "" {
				continue
			}
			if err := p.checkIface(iface); err != nil {
				return err
			}
		}
	}

	// Validate the file before stopping the current replay
	r, err := canlog.Open(cfg.Path)
	if err != nil {
		return err
	}
	if _, err := r.Next(); err != nil {
		r.Close()
		if err == io.EOF {
			return fmt.Errorf("log file contains no CAN frames")
		}
		return err
	}
	r.Close()

	p.Stop()

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})

	p.mu.Lock()
	chMap := make(map[int]string, len(cfg.ChannelMap))
	for k, v := range cfg.ChannelMap {
		chMap[k] = v
	}
	p.st = Status{
		State:     StatePlaying,
		Path:      cfg.Path,
		Speed:     cfg.Speed,
		Loop:      cfg.Loop,
		BRS:       cfg.BRS,
		Direction: cfg.Direction,
		Channels:  chMap,
	}
	p.cancel = cancel
	p.done = done
	p.paused = false
	p.offset = 0
	p.anchor = time.Now()
	p.mu.Unlock()

	// Duration/frame count for the progress bar, computed in the background
	go func() {
		info, err := ScanFile(ctx, cfg.Path)
		if err != nil {
			return
		}
		p.mu.Lock()
		if p.st.Path == cfg.Path && p.done == done {
			p.st.Duration = info.Duration
			p.st.Total = info.Frames
		}
		p.mu.Unlock()
	}()

	go p.run(ctx, cfg, done)
	return nil
}

// Stop ends the replay and waits for it to finish.
func (p *Player) Stop() {
	p.mu.Lock()
	cancel, done := p.cancel, p.done
	p.cancel = nil
	p.mu.Unlock()
	if cancel == nil {
		return
	}
	cancel()
	<-done
}

// Pause suspends a running replay.
func (p *Player) Pause() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.st.State != StatePlaying {
		return fmt.Errorf("not playing")
	}
	p.offset = p.positionLocked(time.Now())
	p.paused = true
	p.st.State = StatePaused
	return nil
}

// Resume continues a paused replay.
func (p *Player) Resume() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.st.State != StatePaused {
		return fmt.Errorf("not paused")
	}
	p.anchor = time.Now()
	p.paused = false
	p.st.State = StatePlaying
	p.signal()
	return nil
}

// SetSpeed changes the playback speed of a running replay.
func (p *Player) SetSpeed(speed float64) error {
	if speed < 0 || speed > 1000 || math.IsNaN(speed) {
		return fmt.Errorf("speed must be between 0 (max) and 1000")
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	now := time.Now()
	if !p.paused {
		p.offset = p.positionLocked(now)
		p.anchor = now
	}
	p.st.Speed = speed
	p.signal()
	return nil
}

// Status returns a snapshot.
func (p *Player) Status() Status {
	p.mu.Lock()
	defer p.mu.Unlock()
	st := p.st
	st.Channels = make(map[int]string, len(p.st.Channels))
	for k, v := range p.st.Channels {
		st.Channels[k] = v
	}
	return st
}

func (p *Player) signal() {
	select {
	case p.wake <- struct{}{}:
	default:
	}
}

// positionLocked returns the logical playback position. Max speed (0) is
// handled by the caller and never waits.
func (p *Player) positionLocked(now time.Time) time.Duration {
	if p.paused {
		return p.offset
	}
	return p.offset + time.Duration(float64(now.Sub(p.anchor))*p.st.Speed)
}

// waitUntil blocks until the logical clock reaches pos. It returns the lag
// (how late we are) or ctx.Err().
func (p *Player) waitUntil(ctx context.Context, pos time.Duration) (time.Duration, error) {
	for {
		p.mu.Lock()
		paused := p.paused
		speed := p.st.Speed
		cur := p.positionLocked(time.Now())
		p.mu.Unlock()

		if !paused && (speed == 0 || cur >= pos) {
			if speed == 0 {
				return 0, nil
			}
			return time.Duration(float64(cur-pos) / speed), nil
		}

		wait := 100 * time.Millisecond
		if !paused {
			if d := time.Duration(float64(pos-cur) / speed); d < wait {
				wait = d
			}
		}
		t := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			t.Stop()
			return 0, ctx.Err()
		case <-p.wake:
			t.Stop()
		case <-t.C:
		}
	}
}

func (p *Player) run(ctx context.Context, cfg Config, done chan struct{}) {
	defer close(done)

	sender := p.newSender()
	defer sender.Close()

	finalState := StateFinished
	var finalErr error
	defer func() {
		p.mu.Lock()
		if ctx.Err() != nil {
			p.st.State = StateStopped
		} else if finalErr != nil {
			p.st.State = StateError
			p.st.Error = finalErr.Error()
		} else {
			p.st.State = finalState
		}
		if p.done == done {
			p.cancel = nil
		}
		p.mu.Unlock()
	}()

	for loop := 0; ; loop++ {
		if err := p.playOnce(ctx, cfg, sender); err != nil {
			if !errors.Is(err, context.Canceled) {
				finalErr = err
			}
			return
		}
		if !cfg.Loop || ctx.Err() != nil {
			return
		}
		p.mu.Lock()
		p.st.Loops = loop + 1
		p.offset = 0
		p.anchor = time.Now()
		p.mu.Unlock()
	}
}

func (p *Player) playOnce(ctx context.Context, cfg Config, sender Sender) error {
	r, err := canlog.Open(cfg.Path)
	if err != nil {
		return err
	}
	defer r.Close()

	first := true
	var t0 time.Duration
	for {
		f, err := r.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		if first {
			t0 = f.Time
			first = false
		}
		pos := f.Time - t0
		if pos < 0 {
			pos = 0 // unsorted timestamps: send immediately
		}

		iface := cfg.ChannelMap[f.Channel]
		if iface == "" || (cfg.Direction == DirRx && f.Tx) || (cfg.Direction == DirTx && !f.Tx) {
			p.addCounts(0, 1, 0, pos, -1)
			continue
		}

		lag, err := p.waitUntil(ctx, pos)
		if err != nil {
			return err
		}

		msg := toMessage(f, cfg.BRS)
		if err := sender.Send(ctx, iface, msg); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			p.addCounts(0, 0, 1, pos, lag)
			p.mu.Lock()
			if time.Since(p.lastErr) > 5*time.Second {
				log.Printf("replay: send on %s failed: %v", iface, err)
				p.lastErr = time.Now()
			}
			p.mu.Unlock()
			continue
		}
		p.addCounts(1, 0, 0, pos, lag)
	}
}

func (p *Player) addCounts(sent, skipped, errs uint64, pos, lag time.Duration) {
	p.mu.Lock()
	p.st.Sent += sent
	p.st.Skipped += skipped
	p.st.Errors += errs
	p.st.Position = pos.Seconds()
	if lag >= 0 {
		p.st.LagMs = float64(lag) / float64(time.Millisecond)
	}
	p.mu.Unlock()
}

func toMessage(f *canlog.Frame, brsMode string) *can.Message {
	id := f.ID
	if f.Extended {
		id |= can.CAN_EFF_FLAG
	}
	var msg *can.Message
	if f.FD {
		brs := f.BRS
		switch brsMode {
		case BRSOn:
			brs = true
		case BRSOff:
			brs = false
		}
		msg = can.NewFDMessage(id, f.Data, brs)
		msg.SetESI(f.ESI)
	} else {
		data := f.Data
		if len(data) > 8 {
			data = data[:8]
		}
		msg = can.NewClassicMessage(id, data)
		if f.Remote {
			msg.ID |= can.CAN_RTR_FLAG
		}
	}
	return msg
}

// busSet keeps one TX bus per interface with a short send queue, so that
// stopping a replay takes effect immediately.
type busSet struct {
	buses map[string]*can.Bus
}

func newBusSet() *busSet { return &busSet{buses: make(map[string]*can.Bus)} }

func (s *busSet) bus(iface string) (*can.Bus, error) {
	if b, ok := s.buses[iface]; ok && b.IsRunning() {
		return b, nil
	}
	b, err := can.NewBusWithOptions(iface, can.Options{TxOnly: true, SendQueueSize: 64})
	if err != nil {
		return nil, err
	}
	b.SetMinSendInterval(0)
	s.buses[iface] = b
	return b, nil
}

func (s *busSet) IsFD(iface string) (bool, error) {
	b, err := s.bus(iface)
	if err != nil {
		return false, err
	}
	return b.IsFD(), nil
}

func (s *busSet) Send(ctx context.Context, iface string, msg *can.Message) error {
	b, err := s.bus(iface)
	if err != nil {
		return err
	}
	if msg.FD && !b.IsFD() {
		// Downgrade short FD frames for classic-mode interfaces
		if msg.Length > 8 {
			return fmt.Errorf("%s is not in CAN-FD mode, cannot send %d-byte frame", iface, msg.Length)
		}
		c := *msg
		c.FD = false
		c.Flags = 0
		msg = &c
	}
	for {
		err := b.SendNonBlocking(msg)
		if err == nil {
			return nil
		}
		if !b.IsRunning() {
			return err
		}
		// Queue full: wait for the bus to drain, staying responsive to stop
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(200 * time.Microsecond):
		}
	}
}

func (s *busSet) Close() {
	for _, b := range s.buses {
		b.Shutdown()
	}
}
