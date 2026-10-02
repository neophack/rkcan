//go:build linux

package replay

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/penghongxia/rkcan/can"
)

type sent struct {
	at    time.Time
	iface string
	msg   can.Message
}

type fakeSender struct {
	mu   sync.Mutex
	sent []sent
}

func (f *fakeSender) Send(ctx context.Context, iface string, msg *can.Message) error {
	f.mu.Lock()
	f.sent = append(f.sent, sent{time.Now(), iface, *msg})
	f.mu.Unlock()
	return nil
}
func (f *fakeSender) IsFD(string) (bool, error) { return true, nil }
func (f *fakeSender) Close()                    {}

func (f *fakeSender) frames() []sent {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]sent(nil), f.sent...)
}

// writeASC creates a log with frames every step on channels 1 and 2.
func writeASC(t *testing.T, n int, step time.Duration) string {
	var b strings.Builder
	b.WriteString("base hex  timestamps absolute\n")
	for i := 0; i < n; i++ {
		ts := float64(i) * step.Seconds()
		ch := i%2 + 1
		fmt.Fprintf(&b, "%11.6f CANFD %3d Rx %8X %32s 1 0 2  2 %02X 00 0 0 3000 0 0 0 0 0\n", 5+ts, ch, 0x100+i, "", i&0xFF)
	}
	path := filepath.Join(t.TempDir(), "t.asc")
	if err := os.WriteFile(path, []byte(b.String()), 0644); err != nil {
		t.Fatal(err)
	}
	return path
}

func waitState(t *testing.T, p *Player, want string, timeout time.Duration) Status {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if st := p.Status(); st.State == want {
			return st
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("state %q not reached, have %+v", want, p.Status())
	return Status{}
}

func TestPlayerTimingAndMapping(t *testing.T) {
	path := writeASC(t, 21, 20*time.Millisecond) // 400 ms of log
	fs := &fakeSender{}
	p := newPlayer(func() Sender { return fs })

	start := time.Now()
	err := p.Start(Config{Path: path, Speed: 2, ChannelMap: map[int]string{1: "can0", 2: ""}, BRS: BRSOff})
	if err != nil {
		t.Fatal(err)
	}
	st := waitState(t, p, StateFinished, 3*time.Second)
	elapsed := time.Since(start)

	frames := fs.frames()
	if len(frames) != 11 || st.Sent != 11 || st.Skipped != 10 {
		t.Fatalf("sent %d frames, status %+v", len(frames), st)
	}
	// 400 ms at 2x speed = 200 ms
	if elapsed < 180*time.Millisecond || elapsed > 400*time.Millisecond {
		t.Fatalf("2x replay of 400 ms took %v", elapsed)
	}
	for i, f := range frames {
		if f.iface != "can0" || f.msg.HasBRS() || !f.msg.FD {
			t.Fatalf("frame %d: %+v", i, f)
		}
		// frame k is scheduled at k*40ms log time => k*20ms wall time
		want := time.Duration(i) * 20 * time.Millisecond
		got := f.at.Sub(frames[0].at)
		if d := got - want; d < -2*time.Millisecond || d > 15*time.Millisecond {
			t.Fatalf("frame %d at %v, want ~%v", i, got, want)
		}
	}
}

func TestPlayerPauseResumeSpeedStop(t *testing.T) {
	path := writeASC(t, 200, 10*time.Millisecond) // 2 s of log
	fs := &fakeSender{}
	p := newPlayer(func() Sender { return fs })
	if err := p.Start(Config{Path: path, Speed: 1, ChannelMap: map[int]string{1: "can0", 2: "can1"}}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond)
	if err := p.Pause(); err != nil {
		t.Fatal(err)
	}
	n := len(fs.frames())
	time.Sleep(150 * time.Millisecond)
	if got := len(fs.frames()); got != n {
		t.Fatalf("frames sent while paused: %d -> %d", n, got)
	}
	if err := p.Resume(); err != nil {
		t.Fatal(err)
	}
	if err := p.SetSpeed(0); err != nil { // max speed
		t.Fatal(err)
	}
	st := waitState(t, p, StateFinished, 2*time.Second)
	if st.Sent != 200 || st.Errors != 0 {
		t.Fatalf("status %+v", st)
	}

	// Loop + stop: stopping must be prompt
	if err := p.Start(Config{Path: path, Speed: 1, Loop: true, ChannelMap: map[int]string{1: "can0"}}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(50 * time.Millisecond)
	t0 := time.Now()
	p.Stop()
	if d := time.Since(t0); d > 200*time.Millisecond {
		t.Fatalf("stop took %v", d)
	}
	if st := p.Status(); st.State != StateStopped {
		t.Fatalf("state %q", st.State)
	}
}

func TestPlayerRejectsBadConfig(t *testing.T) {
	path := writeASC(t, 2, time.Millisecond)
	p := newPlayer(func() Sender { return &fakeSender{} })
	if err := p.Start(Config{Path: path, Speed: 1}); err == nil {
		t.Fatal("expected error without channel map")
	}
	if err := p.Start(Config{Path: path, Speed: -1, ChannelMap: map[int]string{1: "can0"}}); err == nil {
		t.Fatal("expected error for negative speed")
	}
	if err := p.Start(Config{Path: path + ".missing", Speed: 1, ChannelMap: map[int]string{1: "can0"}}); err == nil {
		t.Fatal("expected error for missing file")
	}
}

func TestRecorderWritesASC(t *testing.T) {
	dir := t.TempDir()
	r := NewRecorder()
	if err := r.Start(RecordConfig{Dir: dir}, map[string]int{"can0": 1, "can1": 2}); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	r.Observe("can0", can.NewFDMessage(0x123, []byte{1, 2, 3}, true), now)
	r.Observe("can1", can.NewClassicMessage(0x1ABCDEF|can.CAN_EFF_FLAG, []byte{9}), now.Add(time.Millisecond))
	r.Observe("can9", can.NewClassicMessage(0x1, nil), now) // not recorded
	st := r.Stop()
	if st.Frames != 2 || st.Active || len(st.Files) != 1 {
		t.Fatalf("status %+v", st)
	}

	info, err := ScanFile(context.Background(), st.File)
	if err != nil {
		t.Fatal(err)
	}
	if info.Frames != 2 || info.BRS != 1 || len(info.Channels) != 2 {
		t.Fatalf("scan %+v", info)
	}
}
