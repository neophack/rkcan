//go:build linux

package system

import (
	"context"
	"sync"
	"time"
)

type Collector struct {
	CPU  *CPUCollector
	Mem  *MemCollector
	Net  *NetCollector
	Temp *TempCollector
	Time *TimeCollector
	Proc *ProcessCollector

	mu      sync.RWMutex
	uptime  float64
	loadAvg string
}

func NewCollector() *Collector {
	return &Collector{
		CPU:  NewCPUCollector(),
		Mem:  NewMemCollector(),
		Net:  NewNetCollector(),
		Temp: NewTempCollector(),
		Time: NewTimeCollector(),
		Proc: NewProcessCollector(),
	}
}

func (c *Collector) Start(ctx context.Context) {
	c.collectAll()

	fastTicker := time.NewTicker(1 * time.Second)
	slowTicker := time.NewTicker(5 * time.Second)
	defer fastTicker.Stop()
	defer slowTicker.Stop()

	for {
		select {
		case <-ctx.Done():
			c.Time.Close()
			return
		case <-fastTicker.C:
			c.CPU.Collect()
			c.Mem.Collect()
			c.Net.Collect()
			c.Time.Collect()

			c.mu.Lock()
			c.uptime = readUptime()
			c.loadAvg = ReadLoadAvg()
			c.mu.Unlock()

		case <-slowTicker.C:
			c.Temp.Collect()
			c.Proc.Collect()
		}
	}
}

func (c *Collector) collectAll() {
	c.CPU.Collect()
	c.Mem.Collect()
	c.Net.Collect()
	c.Temp.Collect()
	c.Time.Collect()
	c.Proc.Collect()

	c.mu.Lock()
	c.uptime = readUptime()
	c.loadAvg = ReadLoadAvg()
	c.mu.Unlock()
}

type SystemOverview struct {
	CPU         CPUStats     `json:"cpu"`
	Memory      MemoryStats  `json:"memory"`
	Network     NetworkStats `json:"network"`
	Temps       []TempInfo   `json:"temps"`
	TempHistory []float64    `json:"tempHistory"`
	Time        TimeStats    `json:"time"`
	Uptime      string       `json:"uptime"`
	LoadAvg     string       `json:"loadAvg"`
}

func (c *Collector) Overview() SystemOverview {
	c.mu.RLock()
	uptime := c.uptime
	loadAvg := c.loadAvg
	c.mu.RUnlock()

	return SystemOverview{
		CPU:         c.CPU.Stats(),
		Memory:      c.Mem.Stats(),
		Network:     c.Net.Stats(),
		Temps:       c.Temp.Stats(),
		TempHistory: c.Temp.History(),
		Time:        c.Time.Stats(),
		Uptime:      formatUptime(uptime),
		LoadAvg:     loadAvg,
	}
}
