//go:build linux

package system

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
)

type TempInfo struct {
	Zone string  `json:"zone"`
	Type string  `json:"type"`
	Temp float64 `json:"temp"`
}

type TempCollector struct {
	mu      sync.RWMutex
	temps   []TempInfo
	history []float64
}

func NewTempCollector() *TempCollector {
	return &TempCollector{
		history: make([]float64, 0, historySize),
	}
}

func (c *TempCollector) Collect() {
	zones, _ := filepath.Glob("/sys/class/thermal/thermal_zone*")
	temps := make([]TempInfo, 0, len(zones))

	for _, zone := range zones {
		tempData, err := os.ReadFile(filepath.Join(zone, "temp"))
		if err != nil {
			continue
		}
		milliC, err := strconv.ParseInt(strings.TrimSpace(string(tempData)), 10, 64)
		if err != nil {
			continue
		}

		typeData, _ := os.ReadFile(filepath.Join(zone, "type"))
		typeName := strings.TrimSpace(string(typeData))
		if typeName == "" {
			typeName = filepath.Base(zone)
		}

		temps = append(temps, TempInfo{
			Zone: filepath.Base(zone),
			Type: typeName,
			Temp: float64(milliC) / 1000.0,
		})
	}

	// Also try hwmon
	hwmons, _ := filepath.Glob("/sys/class/hwmon/hwmon*")
	for _, hwmon := range hwmons {
		for i := 1; i <= 10; i++ {
			tempFile := filepath.Join(hwmon, fmt.Sprintf("temp%d_input", i))
			data, err := os.ReadFile(tempFile)
			if err != nil {
				break
			}
			milliC, err := strconv.ParseInt(strings.TrimSpace(string(data)), 10, 64)
			if err != nil {
				continue
			}
			labelFile := filepath.Join(hwmon, fmt.Sprintf("temp%d_label", i))
			label, _ := os.ReadFile(labelFile)
			name := strings.TrimSpace(string(label))
			if name == "" {
				nameData, _ := os.ReadFile(filepath.Join(hwmon, "name"))
				name = strings.TrimSpace(string(nameData))
			}
			if name == "" {
				name = fmt.Sprintf("hwmon%d_temp%d", i, i)
			}
			temps = append(temps, TempInfo{
				Zone: filepath.Base(hwmon),
				Type: name,
				Temp: float64(milliC) / 1000.0,
			})
		}
	}

	var maxTemp float64
	for _, t := range temps {
		if t.Temp > maxTemp {
			maxTemp = t.Temp
		}
	}

	c.mu.Lock()
	c.temps = temps
	c.history = append(c.history, maxTemp)
	if len(c.history) > historySize {
		c.history = c.history[len(c.history)-historySize:]
	}
	c.mu.Unlock()
}

func (c *TempCollector) Stats() []TempInfo {
	c.mu.RLock()
	defer c.mu.RUnlock()
	result := make([]TempInfo, len(c.temps))
	copy(result, c.temps)
	return result
}

func (c *TempCollector) History() []float64 {
	c.mu.RLock()
	defer c.mu.RUnlock()
	result := make([]float64, len(c.history))
	copy(result, c.history)
	return result
}
