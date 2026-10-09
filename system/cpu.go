//go:build linux

package system

import (
	"bufio"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

const historySize = 300

type CPUStats struct {
	UsagePercent float64   `json:"usagePercent"`
	History      []float64 `json:"history"`
	CoreCount    int       `json:"coreCount"`
	PerCore      []float64 `json:"perCore"`
}

type cpuTick struct {
	user, nice, system, idle, iowait, irq, softirq, steal uint64
}

func (t cpuTick) total() uint64 {
	return t.user + t.nice + t.system + t.idle + t.iowait + t.irq + t.softirq + t.steal
}

func (t cpuTick) busy() uint64 {
	return t.total() - t.idle - t.iowait
}

type CPUCollector struct {
	mu      sync.RWMutex
	prev    cpuTick
	prevPer []cpuTick
	stats   CPUStats
}

func NewCPUCollector() *CPUCollector {
	c := &CPUCollector{}
	c.stats.History = make([]float64, 0, historySize)
	ticks := readCPUTicks()
	if len(ticks) == 0 {
		return c
	}
	c.prev = ticks[0]
	if len(ticks) > 1 {
		c.prevPer = ticks[1:]
		c.stats.CoreCount = len(c.prevPer)
		c.stats.PerCore = make([]float64, c.stats.CoreCount)
	}
	return c
}

func (c *CPUCollector) Collect() {
	ticks := readCPUTicks()
	if len(ticks) == 0 {
		return
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	cur := ticks[0]
	c.stats.UsagePercent = usagePercent(c.prev, cur)
	c.prev = cur

	if len(ticks) > 1 {
		cores := ticks[1:]
		c.stats.CoreCount = len(cores)
		if len(c.stats.PerCore) != len(cores) {
			c.stats.PerCore = make([]float64, len(cores))
			c.prevPer = make([]cpuTick, len(cores))
		}
		for i, ct := range cores {
			if i < len(c.prevPer) {
				c.stats.PerCore[i] = usagePercent(c.prevPer[i], ct)
			}
		}
		c.prevPer = cores
	}

	c.stats.History = append(c.stats.History, c.stats.UsagePercent)
	if len(c.stats.History) > historySize {
		c.stats.History = c.stats.History[len(c.stats.History)-historySize:]
	}
}

// usagePercent returns the busy share between two samples, guarding against
// counters that went backwards (CPU hotplug).
func usagePercent(prev, cur cpuTick) float64 {
	if cur.total() <= prev.total() || cur.busy() < prev.busy() {
		return 0
	}
	return float64(cur.busy()-prev.busy()) / float64(cur.total()-prev.total()) * 100
}

func (c *CPUCollector) Stats() CPUStats {
	c.mu.RLock()
	defer c.mu.RUnlock()
	s := c.stats
	s.History = make([]float64, len(c.stats.History))
	copy(s.History, c.stats.History)
	s.PerCore = make([]float64, len(c.stats.PerCore))
	copy(s.PerCore, c.stats.PerCore)
	return s
}

func readCPUTicks() []cpuTick {
	f, err := os.Open("/proc/stat")
	if err != nil {
		return nil
	}
	defer f.Close()

	var result []cpuTick
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "cpu") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 8 {
			continue
		}
		t := cpuTick{}
		vals := []*uint64{&t.user, &t.nice, &t.system, &t.idle, &t.iowait, &t.irq, &t.softirq, &t.steal}
		for i, p := range vals {
			if i+1 < len(fields) {
				v, _ := strconv.ParseUint(fields[i+1], 10, 64)
				*p = v
			}
		}
		result = append(result, t)
	}
	return result
}

func readUptime() float64 {
	data, err := os.ReadFile("/proc/uptime")
	if err != nil {
		return 0
	}
	fields := strings.Fields(string(data))
	if len(fields) < 1 {
		return 0
	}
	v, _ := strconv.ParseFloat(fields[0], 64)
	return v
}

func formatUptime(seconds float64) string {
	d := time.Duration(seconds) * time.Second
	days := int(d.Hours()) / 24
	hours := int(d.Hours()) % 24
	mins := int(d.Minutes()) % 60
	if days > 0 {
		return fmt.Sprintf("%dd %dh %dm", days, hours, mins)
	}
	return fmt.Sprintf("%dh %dm", hours, mins)
}
