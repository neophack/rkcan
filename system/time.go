//go:build linux

package system

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
	"unsafe"

	"golang.org/x/sys/unix"
)

type TimeStats struct {
	SystemTime       string  `json:"systemTime"`
	SystemUnixMs     int64   `json:"systemUnixMs"`
	PHCTime          string  `json:"phcTime"`
	PHCUnixMs        int64   `json:"phcUnixMs"`
	OffsetUs         int64   `json:"offsetUs"`
	PHCAvailable     bool    `json:"phcAvailable"`
	FreqPPB          float64 `json:"freqPPB"`
	ChronyLeapStatus string  `json:"chronyLeapStatus"`
	ChronyStratum    int     `json:"chronyStratum"`
}

type TimeCollector struct {
	mu       sync.RWMutex
	stats    TimeStats
	phcFd    int
	phcAvail bool
	prevSys  time.Time
	prevPHC  time.Time
}

func NewTimeCollector() *TimeCollector {
	c := &TimeCollector{phcFd: -1}

	paths := []string{"/dev/ptp0", "/dev/ptp1", "/dev/ptp2"}
	for _, path := range paths {
		fd, err := unix.Open(path, unix.O_RDONLY, 0)
		if err == nil {
			c.phcFd = fd
			c.phcAvail = true
			break
		}
	}
	return c
}

func (c *TimeCollector) Close() {
	if c.phcFd >= 0 {
		unix.Close(c.phcFd)
	}
}

const ptpClockGettime = 0xc0105001

type ptpClockTime struct {
	Sec  int64
	NSec uint32
	_    uint32
}

func getChronyTracking() (leapStatus string, stratum int) {
	out, err := exec.Command("/userdata/chronyc", "tracking").CombinedOutput()
	if err != nil {
		return "N/A", 0
	}
	lines := strings.Split(string(out), "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if idx := strings.Index(line, ":"); idx > 0 {
			key := strings.TrimSpace(line[:idx])
			val := strings.TrimSpace(line[idx+1:])
			switch key {
			case "Stratum":
				fmt.Sscanf(val, "%d", &stratum)
			case "Leap status":
				leapStatus = val
			}
		}
	}
	return
}

func (c *TimeCollector) Collect() {
	now := time.Now()

	c.mu.Lock()
	defer c.mu.Unlock()

	c.stats.SystemTime = now.Format("2006-01-02 15:04:05.000")
	c.stats.SystemUnixMs = now.UnixMilli()
	c.stats.PHCAvailable = c.phcAvail

	// Update chrony status
	leapStatus, stratum := getChronyTracking()
	c.stats.ChronyLeapStatus = leapStatus
	c.stats.ChronyStratum = stratum

	if c.phcAvail && c.phcFd >= 0 {
		var pct ptpClockTime
		_, _, errno := unix.Syscall(unix.SYS_IOCTL,
			uintptr(c.phcFd),
			uintptr(ptpClockGettime),
			uintptr(unsafe.Pointer(&pct)),
		)
		if errno == 0 {
			phcTime := time.Unix(pct.Sec, int64(pct.NSec))
			c.stats.PHCTime = phcTime.Format("2006-01-02 15:04:05.000")
			c.stats.PHCUnixMs = phcTime.UnixMilli()
			c.stats.OffsetUs = (now.UnixNano() - phcTime.UnixNano()) / 1000

			if !c.prevSys.IsZero() && !c.prevPHC.IsZero() {
				sysDelta := now.Sub(c.prevSys).Nanoseconds()
				phcDelta := phcTime.Sub(c.prevPHC).Nanoseconds()
				if phcDelta > 0 {
					c.stats.FreqPPB = float64(sysDelta-phcDelta) / float64(phcDelta) * 1e9
				}
			}
			c.prevSys = now
			c.prevPHC = phcTime
		} else {
			c.phcAvail = false
			c.stats.PHCAvailable = false
			unix.Close(c.phcFd)
			c.phcFd = -1
		}
	}
}

func (c *TimeCollector) Stats() TimeStats {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.stats
}

func ReadLoadAvg() string {
	data, err := os.ReadFile("/proc/loadavg")
	if err != nil {
		return "N/A"
	}
	parts := strings.Fields(string(data))
	if len(parts) >= 3 {
		return fmt.Sprintf("%s %s %s", parts[0], parts[1], parts[2])
	}
	return strings.TrimSpace(string(data))
}
