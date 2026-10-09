package canlog

import (
	"bufio"
	"io"
	"os"
	"strconv"
	"strings"
	"time"
)

// ascReader parses Vector ASC text logs:
//
//	base hex  timestamps absolute
//	   0.010000 1  123             Rx   d 8 01 02 03 04 05 06 07 08
//	   0.020000 2  18FEF100x       Tx   d 3 AA BB CC
//	   0.030000 1  7DF             Rx   r
//	   0.040000 CANFD   1 Rx        1A0  Name  1 0 d 32 00 01 ... (more fields)
type ascReader struct {
	f        *os.File
	sc       *bufio.Scanner
	base     int // 16 or 10
	relative bool
	last     float64
}

func newASCReader(f *os.File) *ascReader {
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	return &ascReader{f: f, sc: sc, base: 16}
}

func (r *ascReader) Close() error { return r.f.Close() }

func (r *ascReader) Next() (*Frame, error) {
	for r.sc.Scan() {
		line := r.sc.Text()
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}

		switch strings.ToLower(fields[0]) {
		case "base":
			// "base hex  timestamps absolute"
			if strings.EqualFold(fields[1], "dec") {
				r.base = 10
			} else {
				r.base = 16
			}
			for i := 2; i+1 < len(fields); i++ {
				if strings.EqualFold(fields[i], "timestamps") {
					r.relative = strings.EqualFold(fields[i+1], "relative")
				}
			}
			continue
		}

		ts, err := strconv.ParseFloat(fields[0], 64)
		if err != nil {
			continue
		}
		var abs float64
		if r.relative {
			abs = r.last + ts
		} else {
			abs = ts
		}

		var fr *Frame
		if strings.EqualFold(fields[1], "CANFD") {
			fr = r.parseFD(fields)
		} else {
			fr = r.parseClassic(fields)
		}
		if fr == nil {
			// Other events still advance a relative clock
			if r.relative {
				r.last = abs
			}
			continue
		}
		r.last = abs
		fr.Time = time.Duration(abs * float64(time.Second))
		return fr, nil
	}
	if err := r.sc.Err(); err != nil {
		return nil, err
	}
	return nil, io.EOF
}

func (r *ascReader) parseID(s string) (uint32, bool, bool) {
	ext := false
	if strings.HasSuffix(s, "x") || strings.HasSuffix(s, "X") {
		ext = true
		s = s[:len(s)-1]
	}
	v, err := strconv.ParseUint(s, r.base, 32)
	if err != nil {
		return 0, false, false
	}
	if ext {
		v &= 0x1FFFFFFF
	} else if v > 0x7FF {
		// Some writers omit the 'x' for 29-bit IDs
		ext = true
	}
	return uint32(v), ext, true
}

func (r *ascReader) parseBytes(fields []string) ([]byte, bool) {
	out := make([]byte, len(fields))
	for i, s := range fields {
		v, err := strconv.ParseUint(s, r.base, 8)
		if err != nil {
			return nil, false
		}
		out[i] = byte(v)
	}
	return out, true
}

func parseDir(s string) (tx bool, ok bool) {
	switch strings.ToLower(s) {
	case "rx":
		return false, true
	case "tx":
		return true, true
	}
	return false, false // TxRq and others are not frames on the bus
}

// <time> <ch> <id> <Rx|Tx> d <dlc> <data...> | <time> <ch> <id> <Rx|Tx> r [dlc]
func (r *ascReader) parseClassic(f []string) *Frame {
	if len(f) < 5 {
		return nil
	}
	ch, err := strconv.Atoi(f[1])
	if err != nil {
		return nil
	}
	id, ext, ok := r.parseID(f[2])
	if !ok {
		return nil
	}
	tx, ok := parseDir(f[3])
	if !ok {
		return nil
	}
	fr := &Frame{Channel: ch, ID: id, Extended: ext, Tx: tx}

	switch strings.ToLower(f[4]) {
	case "r":
		fr.Remote = true
		if len(f) > 5 {
			if dlc, err := strconv.ParseUint(f[5], 16, 8); err == nil && dlc <= 8 {
				fr.Data = make([]byte, 0, dlc)
			}
		}
		return fr
	case "d":
		if len(f) < 6 {
			return nil
		}
		dlc, err := strconv.ParseUint(f[5], 16, 8)
		if err != nil {
			return nil
		}
		n := int(dlc)
		if n > 8 {
			n = 8
		}
		if len(f) < 6+n {
			return nil
		}
		data, ok := r.parseBytes(f[6 : 6+n])
		if !ok {
			return nil
		}
		fr.Data = data
		return fr
	}
	return nil
}

// <time> CANFD <ch> <Rx|Tx> <id> [name] <brs> <esi> <dlc> <len> <data...> ...
func (r *ascReader) parseFD(f []string) *Frame {
	if len(f) < 9 {
		return nil
	}
	ch, err := strconv.Atoi(f[2])
	if err != nil {
		return nil
	}
	tx, ok := parseDir(f[3])
	if !ok {
		return nil
	}
	id, ext, ok := r.parseID(f[4])
	if !ok {
		return nil
	}

	// The symbolic name is optional; locate "<brs> <esi> <dlc> <len>"
	for i := 5; i+3 < len(f); i++ {
		if (f[i] != "0" && f[i] != "1") || (f[i+1] != "0" && f[i+1] != "1") {
			continue
		}
		dlc, err := strconv.ParseUint(f[i+2], 16, 8)
		if err != nil || dlc > 15 {
			continue
		}
		n, err := strconv.Atoi(f[i+3])
		if err != nil || n < 0 || n > 64 {
			continue
		}
		if n > dlcToLen(int(dlc)) {
			continue
		}
		if len(f) < i+4+n {
			return nil
		}
		data, ok := r.parseBytes(f[i+4 : i+4+n])
		if !ok {
			return nil
		}
		fd := true
		// Classic frames logged in CANFD syntax carry EDL=0 in the flags
		// field (bit 12), which follows the data, duration and length.
		if j := i + 4 + n + 2; j < len(f) {
			if flags, err := strconv.ParseUint(f[j], 16, 32); err == nil && flags&0x1000 == 0 && n <= 8 && f[i] == "0" {
				fd = false
			}
		}
		return &Frame{
			Channel:  ch,
			ID:       id,
			Extended: ext,
			FD:       fd,
			BRS:      fd && f[i] == "1",
			ESI:      fd && f[i+1] == "1",
			Tx:       tx,
			Data:     data,
		}
	}
	return nil
}
