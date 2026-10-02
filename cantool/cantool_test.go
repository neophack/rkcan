//go:build linux

package cantool

import (
	"testing"
	"time"

	"github.com/penghongxia/rkcan/can"
)

func TestFrameSpecBuild(t *testing.T) {
	m, err := FrameSpec{ID: "18FEF100", FD: true, BRS: true, Data: "01 02 03 04 05 06 07 08 09 0A 0B 0C"}.Build()
	if err != nil {
		t.Fatal(err)
	}
	if !m.IsExtended() || m.GetActualID() != 0x18FEF100 || !m.FD || !m.HasBRS() || m.Length != 12 {
		t.Fatalf("unexpected %s", m)
	}

	m, err = FrameSpec{ID: "0x7DF", RTR: true, Data: "11"}.Build()
	if err != nil || !m.IsRTR() || m.Length != 0 || m.FD {
		t.Fatalf("remote frame: %v %v", m, err)
	}

	bad := []FrameSpec{
		{ID: "zz"},
		{ID: "123", Data: "010203040506070809"}, // 9 bytes classic
		{ID: "123", FD: true, Data: "0102030405060708090A"}, // 10 bytes FD
		{ID: "123", BRS: true},                              // BRS without FD
		{ID: "123", FD: true, RTR: true},                    // FD remote
		{ID: "3FFFFFFF", Ext: true},                         // > 29 bits
		{ID: "123", Data: "0g"},
	}
	for _, s := range bad {
		if _, err := s.Build(); err == nil {
			t.Fatalf("expected error for %+v", s)
		}
	}
}

func TestMonitor(t *testing.T) {
	m := NewMonitor()
	t0 := time.Now()
	msg := can.NewFDMessage(0x123, []byte{1, 2}, true)
	for i := 0; i < 5; i++ {
		m.Observe("can0", msg, t0.Add(time.Duration(i)*10*time.Millisecond))
	}
	m.Observe("can1", can.NewClassicMessage(0x18DAF110|can.CAN_EFF_FLAG, []byte{9}), t0)
	s := m.Snapshot()
	if len(s.Entries) != 2 || s.Totals["can0"] != 5 {
		t.Fatalf("snapshot: %+v", s)
	}
	e := s.Entries[0]
	if e.ID != "123" || e.Count != 5 || !e.BRS || e.Data != "01 02" || e.PeriodMs < 9 || e.PeriodMs > 11 {
		t.Fatalf("entry: %+v", e)
	}
	if s.Entries[1].ID != "18DAF110" || !s.Entries[1].Ext {
		t.Fatalf("ext entry: %+v", s.Entries[1])
	}
}
