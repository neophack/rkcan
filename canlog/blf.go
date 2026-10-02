package canlog

import (
	"bufio"
	"bytes"
	"compress/zlib"
	"encoding/binary"
	"fmt"
	"io"
	"os"
	"time"
)

// BLF object types (Vector binlog API)
const (
	blfCANMessage   = 1
	blfLogContainer = 10
	blfCANMessage2  = 86
	blfCANFDMessage = 100
	blfCANFD64      = 101
)

const (
	blfObjHeaderBaseSize = 16
	blfMaxObjectSize     = 64 << 20

	blfFlagTenMicros = 1 // timestamps in 10 µs units, otherwise ns

	blfCANDirTx  = 0x01
	blfCANRemote = 0x80

	blfFDEDL = 0x01
	blfFDBRS = 0x02
	blfFDESI = 0x04

	blfFD64Remote = 0x0010
	blfFD64EDL    = 0x1000
	blfFD64BRS    = 0x2000
	blfFD64ESI    = 0x4000

	canEFFFlag = 0x80000000
)

var lobj = []byte("LOBJ")

// blfReader streams frames from a BLF file. Objects are usually packed into
// zlib-compressed LOG_CONTAINER objects; an object may span containers, so
// decompressed data is buffered until a whole object is available.
type blfReader struct {
	f   *os.File
	br  *bufio.Reader
	buf []byte // decompressed object stream not yet consumed
	pos int
	eof bool
}

func newBLFReader(f *os.File) (*blfReader, error) {
	br := bufio.NewReaderSize(f, 256*1024)
	hdr := make([]byte, 8)
	if _, err := io.ReadFull(br, hdr); err != nil {
		return nil, fmt.Errorf("blf: read header: %w", err)
	}
	if string(hdr[:4]) != "LOGG" {
		return nil, fmt.Errorf("blf: not a BLF file (bad signature)")
	}
	hdrSize := binary.LittleEndian.Uint32(hdr[4:8])
	if hdrSize < 8 || hdrSize > 4096 {
		return nil, fmt.Errorf("blf: invalid header size %d", hdrSize)
	}
	if _, err := br.Discard(int(hdrSize) - 8); err != nil {
		return nil, fmt.Errorf("blf: read header: %w", err)
	}
	return &blfReader{f: f, br: br}, nil
}

func (r *blfReader) Close() error { return r.f.Close() }

func (r *blfReader) Next() (*Frame, error) {
	for {
		fr, need, err := r.parseNext()
		if err != nil {
			return nil, err
		}
		if fr != nil {
			return fr, nil
		}
		if !need {
			continue
		}
		if r.eof {
			return nil, io.EOF
		}
		if err := r.fill(); err != nil {
			return nil, err
		}
	}
}

// parseNext decodes one object from the buffered stream. It returns
// need=true when more data must be read first.
func (r *blfReader) parseNext() (*Frame, bool, error) {
	r.compact()
	data := r.buf[r.pos:]

	// Objects are padded to 4 bytes; find the next signature
	idx := bytes.Index(data[:min(len(data), 8+len(lobj))], lobj)
	if idx < 0 {
		if len(data) >= 8+len(lobj) {
			// Lost sync: search further ahead
			idx = bytes.Index(data, lobj)
			if idx < 0 {
				r.pos = len(r.buf) - (len(lobj) - 1)
				return nil, true, nil
			}
		} else {
			return nil, true, nil
		}
	}
	data = data[idx:]
	if len(data) < blfObjHeaderBaseSize {
		r.pos += idx
		return nil, true, nil
	}

	hdrSize := int(binary.LittleEndian.Uint16(data[4:6]))
	objSize := int(binary.LittleEndian.Uint32(data[8:12]))
	objType := binary.LittleEndian.Uint32(data[12:16])
	if objSize < blfObjHeaderBaseSize || objSize > blfMaxObjectSize || hdrSize < blfObjHeaderBaseSize || hdrSize > objSize {
		// Corrupt object: skip the signature and resync
		r.pos += idx + len(lobj)
		return nil, false, nil
	}
	if len(data) < objSize {
		r.pos += idx
		return nil, true, nil
	}

	obj := data[:objSize]
	r.pos += idx + objSize

	if objType == blfLogContainer {
		// A container inside the stream (rare); expand it in place
		if err := r.appendContainer(obj[hdrSize:]); err != nil {
			return nil, false, err
		}
		return nil, false, nil
	}

	fr := decodeBLFObject(obj, hdrSize, objType)
	return fr, false, nil
}

// compact drops consumed bytes once enough have accumulated.
func (r *blfReader) compact() {
	if r.pos > 1<<20 && r.pos > len(r.buf)/2 {
		n := copy(r.buf, r.buf[r.pos:])
		r.buf = r.buf[:n]
		r.pos = 0
	}
}

// fill reads the next top-level object from the file into the stream.
func (r *blfReader) fill() error {
	hdr := make([]byte, blfObjHeaderBaseSize)
	for {
		// Skip padding between top-level objects
		b, err := r.br.Peek(4)
		if err != nil {
			r.eof = true
			return nil
		}
		if bytes.Equal(b, lobj) {
			break
		}
		r.br.Discard(1)
	}
	if _, err := io.ReadFull(r.br, hdr); err != nil {
		r.eof = true
		return nil
	}
	hdrSize := int(binary.LittleEndian.Uint16(hdr[4:6]))
	objSize := int(binary.LittleEndian.Uint32(hdr[8:12]))
	objType := binary.LittleEndian.Uint32(hdr[12:16])
	if objSize < blfObjHeaderBaseSize || objSize > blfMaxObjectSize || hdrSize < blfObjHeaderBaseSize || hdrSize > objSize {
		return fmt.Errorf("blf: corrupt object header (size %d)", objSize)
	}

	body := make([]byte, objSize-blfObjHeaderBaseSize)
	if _, err := io.ReadFull(r.br, body); err != nil {
		// Truncated file (e.g. logger lost power): stop cleanly
		r.eof = true
		return nil
	}

	if objType == blfLogContainer {
		return r.appendContainer(body[hdrSize-blfObjHeaderBaseSize:])
	}

	// Uncompressed top-level object: feed it to the stream parser as-is
	r.buf = append(r.buf, hdr...)
	r.buf = append(r.buf, body...)
	return nil
}

// appendContainer decodes a LOG_CONTAINER payload:
//
//	u16 compression method (0 = none, 2 = zlib), 6 bytes reserved,
//	u32 uncompressed size, 4 bytes reserved, data...
func (r *blfReader) appendContainer(p []byte) error {
	if len(p) < 16 {
		return fmt.Errorf("blf: container too short")
	}
	method := binary.LittleEndian.Uint16(p[0:2])
	size := int(binary.LittleEndian.Uint32(p[8:12]))
	payload := p[16:]

	switch method {
	case 0:
		r.buf = append(r.buf, payload...)
	case 2:
		if size > blfMaxObjectSize {
			return fmt.Errorf("blf: container too large (%d bytes)", size)
		}
		zr, err := zlib.NewReader(bytes.NewReader(payload))
		if err != nil {
			return fmt.Errorf("blf: container: %w", err)
		}
		start := len(r.buf)
		r.buf = append(r.buf, make([]byte, size)...)
		n, err := io.ReadFull(zr, r.buf[start:])
		zr.Close()
		r.buf = r.buf[:start+n]
		if err != nil && err != io.ErrUnexpectedEOF {
			return fmt.Errorf("blf: container: %w", err)
		}
	default:
		return fmt.Errorf("blf: unsupported container compression %d", method)
	}
	return nil
}

// decodeBLFObject converts a CAN object to a Frame; other objects return nil.
func decodeBLFObject(obj []byte, hdrSize int, objType uint32) *Frame {
	if len(obj) < 32 {
		return nil
	}
	// Header v1 and v2 both store flags at 16 and the timestamp at 24
	flags := binary.LittleEndian.Uint32(obj[16:20])
	ts := binary.LittleEndian.Uint64(obj[24:32])
	var t time.Duration
	if flags == blfFlagTenMicros {
		t = time.Duration(ts) * 10 * time.Microsecond
	} else {
		t = time.Duration(ts)
	}

	body := obj[hdrSize:]
	fr := &Frame{Time: t}

	setID := func(id uint32) {
		fr.Extended = id&canEFFFlag != 0
		fr.ID = id & 0x1FFFFFFF
		if !fr.Extended {
			fr.ID &= 0x7FF
		}
	}

	switch objType {
	case blfCANMessage, blfCANMessage2:
		// u16 channel, u8 flags, u8 dlc, u32 id, u8 data[8]
		if len(body) < 16 {
			return nil
		}
		fr.Channel = int(binary.LittleEndian.Uint16(body[0:2]))
		f := body[2]
		dlc := int(body[3])
		setID(binary.LittleEndian.Uint32(body[4:8]))
		fr.Tx = f&blfCANDirTx != 0
		fr.Remote = f&blfCANRemote != 0
		n := min(dlc, 8)
		if !fr.Remote {
			fr.Data = append([]byte(nil), body[8:8+n]...)
		}
		return fr

	case blfCANFDMessage:
		// u16 channel, u8 flags, u8 dlc, u32 id, u32 frameLength,
		// u8 bitCount, u8 fdFlags, u8 validDataBytes, 5 reserved, data[64]
		if len(body) < 20 {
			return nil
		}
		fr.Channel = int(binary.LittleEndian.Uint16(body[0:2]))
		f := body[2]
		dlc := int(body[3])
		setID(binary.LittleEndian.Uint32(body[4:8]))
		fdFlags := body[13]
		valid := int(body[14])
		fr.Tx = f&blfCANDirTx != 0
		fr.Remote = f&blfCANRemote != 0
		fr.FD = fdFlags&blfFDEDL != 0
		fr.BRS = fr.FD && fdFlags&blfFDBRS != 0
		fr.ESI = fr.FD && fdFlags&blfFDESI != 0
		n := valid
		if n == 0 || n > 64 {
			n = dlcToLen(dlc)
		}
		if !fr.FD {
			n = min(dlc, 8)
		}
		if fr.Remote {
			n = 0
		}
		if 20+n > len(body) {
			n = max(len(body)-20, 0)
		}
		fr.Data = append([]byte(nil), body[20:20+n]...)
		return fr

	case blfCANFD64:
		// u8 channel, u8 dlc, u8 validDataBytes, u8 txCount, u32 id,
		// u32 frameLength, u32 flags, u32 btrArb, u32 btrData,
		// u32 timeOffsetBrsNs, u32 timeOffsetCrcDelNs, u16 bitCount,
		// u8 dir, u8 extDataOffset, u32 crc, data...
		const fixed = 40
		if len(body) < fixed {
			return nil
		}
		fr.Channel = int(body[0])
		dlc := int(body[1])
		valid := int(body[2])
		setID(binary.LittleEndian.Uint32(body[4:8]))
		f := binary.LittleEndian.Uint32(body[12:16])
		fr.Tx = body[34] != 0
		fr.Remote = f&blfFD64Remote != 0
		fr.FD = f&blfFD64EDL != 0
		fr.BRS = fr.FD && f&blfFD64BRS != 0
		fr.ESI = fr.FD && f&blfFD64ESI != 0
		n := valid
		if n > 64 {
			n = 64
		}
		if !fr.FD {
			n = min(n, min(dlc, 8))
		}
		if fr.Remote {
			n = 0
		}
		if fixed+n > len(body) {
			n = max(len(body)-fixed, 0)
		}
		fr.Data = append([]byte(nil), body[fixed:fixed+n]...)
		return fr
	}
	return nil
}
