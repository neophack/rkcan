package canlog

import (
	"bufio"
	"fmt"
	"io"
	"strings"
	"time"
)

// ASCWriter writes frames in Vector ASC format (base hex, absolute
// timestamps), readable by CANoe/CANalyzer, python-can and this package.
type ASCWriter struct {
	w     *bufio.Writer
	start time.Time
	sb    strings.Builder
}

// NewASCWriter writes the ASC header; start is the measurement start time.
func NewASCWriter(w io.Writer, start time.Time) (*ASCWriter, error) {
	aw := &ASCWriter{w: bufio.NewWriterSize(w, 64*1024), start: start}
	date := ascDate(start)
	_, err := fmt.Fprintf(aw.w, "date %s\nbase hex  timestamps absolute\ninternal events logged\n"+
		"// version 13.0.0\nBegin Triggerblock %s\n   0.000000 Start of measurement\n", date, date)
	return aw, err
}

// ascDate formats like "Thu Oct 2 08:25:55.000 am 2026".
func ascDate(t time.Time) string {
	ampm := "am"
	if t.Hour() >= 12 {
		ampm = "pm"
	}
	h := t.Hour() % 12
	if h == 0 {
		h = 12
	}
	return fmt.Sprintf("%s %s %d %02d:%02d:%02d.%03d %s %d",
		t.Format("Mon"), t.Format("Jan"), t.Day(), h, t.Minute(), t.Second(),
		t.Nanosecond()/1e6, ampm, t.Year())
}

// Write appends one frame received at ts.
func (aw *ASCWriter) Write(ts time.Time, f *Frame) error {
	rel := ts.Sub(aw.start).Seconds()
	if rel < 0 {
		rel = 0
	}
	id := fmt.Sprintf("%X", f.ID)
	if f.Extended {
		id += "x"
	}
	dir := "Rx"
	if f.Tx {
		dir = "Tx"
	}

	sb := &aw.sb
	sb.Reset()
	for i, b := range f.Data {
		if i > 0 {
			sb.WriteByte(' ')
		}
		fmt.Fprintf(sb, "%02X", b)
	}
	data := sb.String()

	var err error
	if f.FD {
		brs, esi := 0, 0
		flags := 0x1000
		if f.BRS {
			brs = 1
			flags |= 0x2000
		}
		if f.ESI {
			esi = 1
			flags |= 0x4000
		}
		_, err = fmt.Fprintf(aw.w, "%11.6f CANFD %3d %-4s %8s %32s %d %d %x %2d %s %8d %4d %8X %8d %8d %8d %8d %8d\n",
			rel, f.Channel, dir, id, "", brs, esi, lenToDLC(len(f.Data)), len(f.Data), data,
			0, 0, flags, 0, 0, 0, 0, 0)
	} else if f.Remote {
		_, err = fmt.Fprintf(aw.w, "%11.6f %d  %-15s %-4s r\n", rel, f.Channel, id, dir)
	} else {
		_, err = fmt.Fprintf(aw.w, "%11.6f %d  %-15s %-4s d %d %s\n", rel, f.Channel, id, dir, len(f.Data), data)
	}
	return err
}

// Flush writes buffered lines to the underlying writer.
func (aw *ASCWriter) Flush() error { return aw.w.Flush() }

// Close writes the trailer and flushes. It does not close the underlying writer.
func (aw *ASCWriter) Close() error {
	if _, err := aw.w.WriteString("End TriggerBlock\n"); err != nil {
		return err
	}
	return aw.w.Flush()
}
