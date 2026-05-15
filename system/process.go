//go:build linux

package system

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
)

type ProcessInfo struct {
	PID     int     `json:"pid"`
	Name    string  `json:"name"`
	State   string  `json:"state"`
	CPUPct  float64 `json:"cpuPct"`
	MemKB   uint64  `json:"memKB"`
	MemPct  float64 `json:"memPct"`
	Threads int     `json:"threads"`
}

type procSnap struct {
	utime uint64
	stime uint64
	total uint64
}

type ProcessCollector struct {
	mu       sync.RWMutex
	procs    []ProcessInfo
	prevSnap map[int]procSnap
	prevCPU  cpuTick
}

func NewProcessCollector() *ProcessCollector {
	ticks := readCPUTicks()
	var t cpuTick
	if len(ticks) > 0 {
		t = ticks[0]
	}
	return &ProcessCollector{
		prevSnap: make(map[int]procSnap),
		prevCPU:  t,
	}
}

func (c *ProcessCollector) Collect() {
	dirs, err := filepath.Glob("/proc/[0-9]*")
	if err != nil {
		return
	}

	ticks := readCPUTicks()
	if len(ticks) == 0 {
		return
	}
	curCPU := ticks[0]
	cpuDelta := curCPU.total() - c.prevCPU.total()

	memInfo := readMemInfo()
	totalMem := memInfo["MemTotal"]

	newSnap := make(map[int]procSnap)
	procs := make([]ProcessInfo, 0, len(dirs))

	for _, dir := range dirs {
		pidStr := filepath.Base(dir)
		pid, err := strconv.Atoi(pidStr)
		if err != nil {
			continue
		}

		statData, err := os.ReadFile(filepath.Join(dir, "stat"))
		if err != nil {
			continue
		}

		info := parseProcStat(string(statData), pid)
		if info == nil {
			continue
		}

		snap := procSnap{
			utime: info.utime,
			stime: info.stime,
			total: curCPU.total(),
		}
		newSnap[pid] = snap

		var cpuPct float64
		if prev, ok := c.prevSnap[pid]; ok && cpuDelta > 0 {
			procDelta := (snap.utime + snap.stime) - (prev.utime + prev.stime)
			cpuPct = float64(procDelta) / float64(cpuDelta) * 100
		}

		statusData, _ := os.ReadFile(filepath.Join(dir, "status"))
		memKB := parseVmRSS(string(statusData))
		var memPct float64
		if totalMem > 0 {
			memPct = float64(memKB*1024) / float64(totalMem) * 100
		}

		procs = append(procs, ProcessInfo{
			PID:     pid,
			Name:    info.name,
			State:   info.state,
			CPUPct:  cpuPct,
			MemKB:   memKB,
			MemPct:  memPct,
			Threads: info.threads,
		})
	}

	sort.Slice(procs, func(i, j int) bool {
		return procs[i].CPUPct > procs[j].CPUPct
	})

	if len(procs) > 50 {
		procs = procs[:50]
	}

	c.mu.Lock()
	c.procs = procs
	c.prevSnap = newSnap
	c.prevCPU = curCPU
	c.mu.Unlock()
}

func (c *ProcessCollector) Stats() []ProcessInfo {
	c.mu.RLock()
	defer c.mu.RUnlock()
	result := make([]ProcessInfo, len(c.procs))
	copy(result, c.procs)
	return result
}

type rawProcStat struct {
	name    string
	state   string
	utime   uint64
	stime   uint64
	threads int
}

func parseProcStat(data string, pid int) *rawProcStat {
	// Format: pid (comm) state ppid ...
	// comm can contain spaces/parens, so find last ')'
	start := strings.IndexByte(data, '(')
	end := strings.LastIndexByte(data, ')')
	if start < 0 || end < 0 || end <= start {
		return nil
	}

	name := data[start+1 : end]
	rest := strings.Fields(data[end+2:])
	if len(rest) < 20 {
		return nil
	}

	state := rest[0]
	utime, _ := strconv.ParseUint(rest[11], 10, 64)
	stime, _ := strconv.ParseUint(rest[12], 10, 64)
	threads, _ := strconv.Atoi(rest[17])

	return &rawProcStat{
		name:    name,
		state:   state,
		utime:   utime,
		stime:   stime,
		threads: threads,
	}
}

func parseVmRSS(status string) uint64 {
	for _, line := range strings.Split(status, "\n") {
		if strings.HasPrefix(line, "VmRSS:") {
			fields := strings.Fields(line)
			if len(fields) >= 2 {
				v, _ := strconv.ParseUint(fields[1], 10, 64)
				return v
			}
		}
	}
	return 0
}

func FormatBytes(b uint64) string {
	const (
		KB = 1024
		MB = KB * 1024
		GB = MB * 1024
	)
	switch {
	case b >= GB:
		return fmt.Sprintf("%.1f GB", float64(b)/float64(GB))
	case b >= MB:
		return fmt.Sprintf("%.1f MB", float64(b)/float64(MB))
	case b >= KB:
		return fmt.Sprintf("%.1f KB", float64(b)/float64(KB))
	default:
		return fmt.Sprintf("%d B", b)
	}
}
