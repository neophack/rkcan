//go:build linux

package replay

import (
	"context"
	"io"
	"os"
	"sort"
	"sync"
	"time"

	"github.com/penghongxia/rkcan/canlog"
)

// FileInfo summarises a log file.
type FileInfo struct {
	Path     string  `json:"path"`
	Frames   int64   `json:"frames"`
	Duration float64 `json:"duration"` // seconds between first and last frame
	Channels []int   `json:"channels"`
	FDFrames int64   `json:"fdFrames"`
	BRS      int64   `json:"brsFrames"`
}

type scanKey struct {
	path  string
	size  int64
	mtime time.Time
}

var (
	scanMu    sync.Mutex
	scanCache = map[scanKey]FileInfo{}
)

// ScanFile reads a whole log file to collect its statistics. Results are
// cached by path, size and modification time.
func ScanFile(ctx context.Context, path string) (FileInfo, error) {
	st, err := os.Stat(path)
	if err != nil {
		return FileInfo{}, err
	}
	key := scanKey{path, st.Size(), st.ModTime()}
	scanMu.Lock()
	if info, ok := scanCache[key]; ok {
		scanMu.Unlock()
		return info, nil
	}
	scanMu.Unlock()

	r, err := canlog.Open(path)
	if err != nil {
		return FileInfo{}, err
	}
	defer r.Close()

	info := FileInfo{Path: path}
	chans := map[int]bool{}
	var first, last time.Duration
	for {
		if info.Frames%4096 == 0 && ctx.Err() != nil {
			return FileInfo{}, ctx.Err()
		}
		f, err := r.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return FileInfo{}, err
		}
		if info.Frames == 0 {
			first = f.Time
		}
		last = f.Time
		info.Frames++
		chans[f.Channel] = true
		if f.FD {
			info.FDFrames++
			if f.BRS {
				info.BRS++
			}
		}
	}
	if info.Frames > 0 {
		info.Duration = (last - first).Seconds()
	}
	for ch := range chans {
		info.Channels = append(info.Channels, ch)
	}
	sort.Ints(info.Channels)

	scanMu.Lock()
	if len(scanCache) > 64 {
		scanCache = map[scanKey]FileInfo{}
	}
	scanCache[key] = info
	scanMu.Unlock()
	return info, nil
}
