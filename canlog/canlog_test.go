package canlog

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"io"
	"math"
	"os"
	"path/filepath"
	"testing"
	"time"
)

type expectedFrame struct {
	T    float64 `json:"t"`
	Ch   int     `json:"ch"`
	ID   uint32  `json:"id"`
	Ext  bool    `json:"ext"`
	RTR  bool    `json:"rtr"`
	FD   bool    `json:"fd"`
	BRS  bool    `json:"brs"`
	ESI  bool    `json:"esi"`
	Tx   bool    `json:"tx"`
	Data string  `json:"data"`
}

func readAll(t *testing.T, path string) []*Frame {
	t.Helper()
	r, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	var out []*Frame
	for {
		f, err := r.Next()
		if err == io.EOF {
			return out
		}
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, f)
	}
}

// The fixtures were written by python-can (testdata/gen.py), an independent
// implementation, so this checks interoperability rather than round-tripping.
func checkAgainstFixture(t *testing.T, logFile string) {
	frames := readAll(t, filepath.Join("testdata", logFile))
	raw, err := os.ReadFile(filepath.Join("testdata", logFile+".json"))
	if err != nil {
		t.Fatal(err)
	}
	var want []expectedFrame
	if err := json.Unmarshal(raw, &want); err != nil {
		t.Fatal(err)
	}
	if len(frames) != len(want) {
		t.Fatalf("%s: got %d frames, want %d", logFile, len(frames), len(want))
	}
	t0 := frames[0].Time
	for i, w := range want {
		f := frames[i]
		gotT := (f.Time - t0).Seconds()
		if math.Abs(gotT-w.T) > 2e-6 {
			t.Fatalf("%s frame %d: time %f, want %f", logFile, i, gotT, w.T)
		}
		data, _ := hex.DecodeString(w.Data)
		if f.Channel != w.Ch+1 || f.ID != w.ID || f.Extended != w.Ext || f.Remote != w.RTR ||
			f.FD != w.FD || f.BRS != w.BRS || f.ESI != w.ESI || f.Tx != w.Tx || !bytes.Equal(f.Data, data) {
			t.Fatalf("%s frame %d:\n got  %+v data=%X\n want %+v", logFile, i, *f, f.Data, w)
		}
	}
}

func TestReadBLF(t *testing.T) { checkAgainstFixture(t, "sample.blf") }
func TestReadASC(t *testing.T) { checkAgainstFixture(t, "sample.asc") }

func TestASCWriterRoundTrip(t *testing.T) {
	frames := readAll(t, filepath.Join("testdata", "sample.blf"))[:500]
	start := time.Date(2026, 10, 2, 8, 0, 0, 0, time.UTC)

	path := filepath.Join(t.TempDir(), "out.asc")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	w, err := NewASCWriter(f, start)
	if err != nil {
		t.Fatal(err)
	}
	t0 := frames[0].Time
	for _, fr := range frames {
		if err := w.Write(start.Add(fr.Time-t0), fr); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	f.Close()

	got := readAll(t, path)
	if len(got) != len(frames) {
		t.Fatalf("got %d frames, want %d", len(got), len(frames))
	}
	for i := range frames {
		a, b := frames[i], got[i]
		if a.Channel != b.Channel || a.ID != b.ID || a.Extended != b.Extended || a.Remote != b.Remote ||
			a.FD != b.FD || a.BRS != b.BRS || a.ESI != b.ESI || a.Tx != b.Tx || !bytes.Equal(a.Data, b.Data) {
			t.Fatalf("frame %d differs:\n %+v\n %+v", i, *a, *b)
		}
		if d := (a.Time - t0) - b.Time; d > time.Microsecond || d < -time.Microsecond {
			t.Fatalf("frame %d time differs by %v", i, d)
		}
	}
}

func TestASCRelativeDec(t *testing.T) {
	src := "base dec  timestamps relative\n" +
		"   0.100000 1  291             Rx   d 2 1 255\n" +
		"   0.050000 1  Statistic: D 0 R 0\n" +
		"   0.050000 2  100x            Tx   d 1 16\n" +
		"   0.010000 1  291             TxRq d 0\n"
	path := filepath.Join(t.TempDir(), "rel.asc")
	os.WriteFile(path, []byte(src), 0644)
	got := readAll(t, path)
	if len(got) != 2 {
		t.Fatalf("got %d frames", len(got))
	}
	if got[0].ID != 291 || !bytes.Equal(got[0].Data, []byte{1, 255}) || got[0].Time != 100*time.Millisecond {
		t.Fatalf("frame 0: %+v", *got[0])
	}
	if got[1].ID != 100 || !got[1].Extended || !got[1].Tx || got[1].Time != 200*time.Millisecond {
		t.Fatalf("frame 1: %+v", *got[1])
	}
}

func TestBLFTruncated(t *testing.T) {
	raw, _ := os.ReadFile(filepath.Join("testdata", "sample.blf"))
	path := filepath.Join(t.TempDir(), "cut.blf")
	os.WriteFile(path, raw[:len(raw)*2/3], 0644)
	frames := readAll(t, path) // must not error or hang
	if len(frames) == 0 {
		t.Fatal("expected frames before the truncation point")
	}
}
