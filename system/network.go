//go:build linux

package system

import (
	"bufio"
	"os"
	"strconv"
	"strings"
	"sync"
)

type NetworkStats struct {
	Interfaces map[string]*NetIfaceStats `json:"interfaces"`
}

type NetIfaceStats struct {
	Name          string   `json:"name"`
	RxBytes       uint64   `json:"rxBytes"`
	TxBytes       uint64   `json:"txBytes"`
	RxBytesPerSec uint64   `json:"rxBytesPerSec"`
	TxBytesPerSec uint64   `json:"txBytesPerSec"`
	RxHistory     []uint64 `json:"rxHistory"`
	TxHistory     []uint64 `json:"txHistory"`
}

type NetCollector struct {
	mu   sync.RWMutex
	prev map[string][2]uint64
	stat NetworkStats
}

func NewNetCollector() *NetCollector {
	c := &NetCollector{
		prev: make(map[string][2]uint64),
	}
	c.stat.Interfaces = make(map[string]*NetIfaceStats)
	ifaces := readNetDev()
	for name, vals := range ifaces {
		c.prev[name] = vals
		c.stat.Interfaces[name] = &NetIfaceStats{
			Name:      name,
			RxBytes:   vals[0],
			TxBytes:   vals[1],
			RxHistory: make([]uint64, 0, historySize),
			TxHistory: make([]uint64, 0, historySize),
		}
	}
	return c
}

func (c *NetCollector) Collect() {
	ifaces := readNetDev()

	c.mu.Lock()
	defer c.mu.Unlock()

	for name, vals := range ifaces {
		prev, ok := c.prev[name]
		st, exists := c.stat.Interfaces[name]
		if !exists {
			st = &NetIfaceStats{
				Name:      name,
				RxHistory: make([]uint64, 0, historySize),
				TxHistory: make([]uint64, 0, historySize),
			}
			c.stat.Interfaces[name] = st
		}

		st.RxBytes = vals[0]
		st.TxBytes = vals[1]

		if ok {
			st.RxBytesPerSec = vals[0] - prev[0]
			st.TxBytesPerSec = vals[1] - prev[1]
		}

		st.RxHistory = append(st.RxHistory, st.RxBytesPerSec)
		st.TxHistory = append(st.TxHistory, st.TxBytesPerSec)
		if len(st.RxHistory) > historySize {
			st.RxHistory = st.RxHistory[len(st.RxHistory)-historySize:]
		}
		if len(st.TxHistory) > historySize {
			st.TxHistory = st.TxHistory[len(st.TxHistory)-historySize:]
		}

		c.prev[name] = vals
	}
}

func (c *NetCollector) Stats() NetworkStats {
	c.mu.RLock()
	defer c.mu.RUnlock()

	s := NetworkStats{Interfaces: make(map[string]*NetIfaceStats)}
	for k, v := range c.stat.Interfaces {
		cp := *v
		cp.RxHistory = make([]uint64, len(v.RxHistory))
		copy(cp.RxHistory, v.RxHistory)
		cp.TxHistory = make([]uint64, len(v.TxHistory))
		copy(cp.TxHistory, v.TxHistory)
		s.Interfaces[k] = &cp
	}
	return s
}

func readNetDev() map[string][2]uint64 {
	f, err := os.Open("/proc/net/dev")
	if err != nil {
		return nil
	}
	defer f.Close()

	result := make(map[string][2]uint64)
	scanner := bufio.NewScanner(f)
	lineNo := 0
	for scanner.Scan() {
		lineNo++
		if lineNo <= 2 {
			continue
		}
		line := scanner.Text()
		colonIdx := strings.Index(line, ":")
		if colonIdx < 0 {
			continue
		}
		name := strings.TrimSpace(line[:colonIdx])
		if name == "lo" {
			continue
		}
		fields := strings.Fields(line[colonIdx+1:])
		if len(fields) < 10 {
			continue
		}
		rxBytes, _ := strconv.ParseUint(fields[0], 10, 64)
		txBytes, _ := strconv.ParseUint(fields[8], 10, 64)
		result[name] = [2]uint64{rxBytes, txBytes}
	}
	return result
}
