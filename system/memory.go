//go:build linux

package system

import (
	"bufio"
	"os"
	"strconv"
	"strings"
	"sync"
)

type MemoryStats struct {
	Total     uint64    `json:"total"`
	Used      uint64    `json:"used"`
	Free      uint64    `json:"free"`
	Available uint64    `json:"available"`
	Buffers   uint64    `json:"buffers"`
	Cached    uint64    `json:"cached"`
	SwapTotal uint64    `json:"swapTotal"`
	SwapUsed  uint64    `json:"swapUsed"`
	Usage     float64   `json:"usage"`
	History   []float64 `json:"history"`
}

type MemCollector struct {
	mu    sync.RWMutex
	stats MemoryStats
}

func NewMemCollector() *MemCollector {
	c := &MemCollector{}
	c.stats.History = make([]float64, 0, historySize)
	return c
}

func (c *MemCollector) Collect() {
	info := readMemInfo()

	c.mu.Lock()
	defer c.mu.Unlock()

	c.stats.Total = info["MemTotal"]
	c.stats.Free = info["MemFree"]
	c.stats.Available = info["MemAvailable"]
	c.stats.Buffers = info["Buffers"]
	c.stats.Cached = info["Cached"]
	c.stats.SwapTotal = info["SwapTotal"]
	swapFree := info["SwapFree"]
	c.stats.SwapUsed = c.stats.SwapTotal - swapFree

	if c.stats.Available > 0 {
		c.stats.Used = c.stats.Total - c.stats.Available
	} else {
		c.stats.Used = c.stats.Total - c.stats.Free - c.stats.Buffers - c.stats.Cached
	}

	if c.stats.Total > 0 {
		c.stats.Usage = float64(c.stats.Used) / float64(c.stats.Total) * 100
	}

	c.stats.History = append(c.stats.History, c.stats.Usage)
	if len(c.stats.History) > historySize {
		c.stats.History = c.stats.History[len(c.stats.History)-historySize:]
	}
}

func (c *MemCollector) Stats() MemoryStats {
	c.mu.RLock()
	defer c.mu.RUnlock()
	s := c.stats
	s.History = make([]float64, len(c.stats.History))
	copy(s.History, c.stats.History)
	return s
}

func readMemInfo() map[string]uint64 {
	f, err := os.Open("/proc/meminfo")
	if err != nil {
		return nil
	}
	defer f.Close()

	info := make(map[string]uint64)
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Text()
		parts := strings.SplitN(line, ":", 2)
		if len(parts) != 2 {
			continue
		}
		key := strings.TrimSpace(parts[0])
		valStr := strings.TrimSpace(parts[1])
		valStr = strings.TrimSuffix(valStr, " kB")
		valStr = strings.TrimSpace(valStr)
		v, err := strconv.ParseUint(valStr, 10, 64)
		if err == nil {
			info[key] = v * 1024 // convert kB to bytes
		}
	}
	return info
}
