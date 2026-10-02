// Package canlog reads and writes CAN log files in Vector ASC and BLF format.
package canlog

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Frame is one CAN or CAN-FD frame from a log file.
type Frame struct {
	Time     time.Duration // timestamp relative to the start of the log
	Channel  int           // 1-based channel number as stored in the file
	ID       uint32        // identifier without flags
	Extended bool          // 29-bit identifier
	Remote   bool          // remote transmission request
	FD       bool          // CAN-FD frame
	BRS      bool          // CAN-FD bit rate switch
	ESI      bool          // CAN-FD error state indicator
	Tx       bool          // transmitted by the logging node
	Data     []byte
}

// Reader returns frames in file order. Next returns io.EOF at the end.
type Reader interface {
	Next() (*Frame, error)
	Close() error
}

// Format is a supported log file format.
type Format string

const (
	FormatASC Format = "asc"
	FormatBLF Format = "blf"
)

// FormatOf returns the format implied by the file extension, or "".
func FormatOf(path string) Format {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".asc":
		return FormatASC
	case ".blf":
		return FormatBLF
	}
	return ""
}

// Open opens a log file, choosing the parser from the file extension.
func Open(path string) (Reader, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	switch FormatOf(path) {
	case FormatASC:
		return newASCReader(f), nil
	case FormatBLF:
		r, err := newBLFReader(f)
		if err != nil {
			f.Close()
			return nil, err
		}
		return r, nil
	}
	f.Close()
	return nil, fmt.Errorf("unsupported log format: %s (use .asc or .blf)", filepath.Ext(path))
}

// dlcToLen maps a CAN-FD DLC (0..15) to its payload length.
func dlcToLen(dlc int) int {
	table := [16]int{0, 1, 2, 3, 4, 5, 6, 7, 8, 12, 16, 20, 24, 32, 48, 64}
	if dlc < 0 {
		return 0
	}
	if dlc > 15 {
		return 64
	}
	return table[dlc]
}

// lenToDLC maps a payload length to the smallest DLC that can carry it.
func lenToDLC(n int) int {
	for dlc := 0; dlc < 16; dlc++ {
		if dlcToLen(dlc) >= n {
			return dlc
		}
	}
	return 15
}
