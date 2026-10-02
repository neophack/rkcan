//go:build linux

package timesync

import (
	"testing"

	"github.com/penghongxia/rkcan/can"
)

func TestBuildFrame(t *testing.T) {
	data := buildPayload(msgType1, 3, 0x01020304, Protocol594)
	want := []byte{0x20, 0x00, 0x30, 0x00, 0x01, 0x02, 0x03, 0x04}
	for i := range want {
		if data[i] != want[i] {
			t.Fatalf("payload = % X, want % X", data, want)
		}
	}

	frame, n := buildFrame(0x594, data, true, true)
	if n != can.CANFD_MTU || frame[4] != 8 || frame[5] != can.CANFD_BRS || frame[0] != 0x94 || frame[1] != 0x05 {
		t.Fatalf("unexpected CAN-FD frame: n=%d % X", n, frame[:16])
	}

	frame, n = buildFrame(0x5A4, data, false, true)
	if n != can.CAN_MTU || frame[5] != 0 || frame[8] != 0x20 {
		t.Fatalf("unexpected classic frame: n=%d % X", n, frame[:16])
	}

	if b := byte2(5, Protocol5A4); b != 0x05 {
		t.Fatalf("5A4 byte2 = %#x", b)
	}
}
