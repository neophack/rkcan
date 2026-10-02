//go:build linux

package can

import "testing"

func TestEncodeFDWithBRS(t *testing.T) {
	msg := NewFDMessage(0x123, []byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12}, true)

	var frame [CANFD_MTU]byte
	n, err := msg.Encode(&frame)
	if err != nil {
		t.Fatal(err)
	}
	if n != CANFD_MTU {
		t.Fatalf("expected %d bytes, got %d", CANFD_MTU, n)
	}
	if frame[5] != CANFD_BRS {
		t.Fatalf("expected flags 0x%02X, got 0x%02X", CANFD_BRS, frame[5])
	}

	// Kernel sets FDF on received CAN-FD frames
	frame[5] |= CANFD_FDF
	var got Message
	if err := got.Unmarshal(frame[:n]); err != nil {
		t.Fatal(err)
	}
	if !got.FD || !got.HasBRS() || got.Length != 12 || got.DLC != 9 || got.Data[11] != 12 {
		t.Fatalf("unexpected decode: %s", got.String())
	}
}

func TestEncodeClassic(t *testing.T) {
	msg := NewClassicMessage(0x7FF, []byte{0xAA, 0xBB})

	var frame [CANFD_MTU]byte
	n, err := msg.Encode(&frame)
	if err != nil {
		t.Fatal(err)
	}
	if n != CAN_MTU {
		t.Fatalf("expected %d bytes, got %d", CAN_MTU, n)
	}

	// Byte 5 of a classic frame is padding and must not be read as flags
	frame[5] = 0xFF
	var got Message
	if err := got.Unmarshal(frame[:n]); err != nil {
		t.Fatal(err)
	}
	if got.FD || got.HasBRS() || got.Length != 2 || got.Data[1] != 0xBB {
		t.Fatalf("unexpected decode: %s", got.String())
	}

	msg.SetBRS(true)
	if _, err := msg.Encode(&frame); err == nil {
		t.Fatal("expected error for BRS on classic frame")
	}
}
