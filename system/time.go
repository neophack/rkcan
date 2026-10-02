//go:build linux

package system

import (
	"context"
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

// ChronycPath and ChronyConfPath locate chrony. An empty ChronycPath is
// resolved automatically by Chronyc(). Set them before starting collectors.
var (
	ChronycPath    = ""
	ChronyConfPath = "/etc/chrony.conf"
)

// Chronyc returns the chronyc binary to run: ChronycPath if set, otherwise
// /userdata/chronyc (the bundled build), otherwise chronyc from $PATH.
func Chronyc() string {
	if ChronycPath != "" {
		return ChronycPath
	}
	if _, err := os.Stat("/userdata/chronyc"); err == nil {
		return "/userdata/chronyc"
	}
	if p, err := exec.LookPath("chronyc"); err == nil {
		return p
	}
	return "chronyc"
}

// RunChronyc runs chronyc with a timeout so a hung chronyd cannot block callers.
func RunChronyc(args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, Chronyc(), args...).CombinedOutput()
	return string(out), err
}

// chronyPollInterval is how often chronyc is queried for the dashboard.
const chronyPollInterval = 5 * time.Second

type TimeCollector struct {
	mu        sync.RWMutex
	stats     TimeStats
	phcFd     int
	phcAvail  bool
	prevSys   time.Time
	prevPHC   time.Time
	lastChron time.Time
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
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.phcFd >= 0 {
		unix.Close(c.phcFd)
		c.phcFd = -1
		c.phcAvail = false
	}
}

const ptpClockGettime = 0xc0105001

type ptpClockTime struct {
	Sec  int64
	NSec uint32
	_    uint32
}

func getChronyTracking() (leapStatus string, stratum int) {
	out, err := RunChronyc("tracking")
	if err != nil {
		return "N/A", 0
	}
	lines := strings.Split(out, "\n")
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
	// Query chrony outside the lock (it spawns a process)
	var chronyUpdated bool
	var leapStatus string
	var stratum int
	if time.Since(c.lastChron) >= chronyPollInterval {
		leapStatus, stratum = getChronyTracking()
		chronyUpdated = true
	}

	now := time.Now()

	c.mu.Lock()
	defer c.mu.Unlock()

	c.stats.SystemTime = now.Format("2006-01-02 15:04:05.000")
	c.stats.SystemUnixMs = now.UnixMilli()
	c.stats.PHCAvailable = c.phcAvail

	if chronyUpdated {
		c.lastChron = now
		c.stats.ChronyLeapStatus = leapStatus
		c.stats.ChronyStratum = stratum
	}

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
