//go:build linux

package wifi

import (
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

type Network struct {
	SSID      string `json:"ssid"`
	BSSID     string `json:"bssid"`
	Signal    int    `json:"signal"`
	Frequency string `json:"frequency"`
	Security  string `json:"security"`
	InUse     bool   `json:"inUse"`
}

type Status struct {
	Connected bool   `json:"connected"`
	SSID      string `json:"ssid"`
	IP        string `json:"ip"`
	Signal    int    `json:"signal"`
	Frequency string `json:"frequency"`
	Speed     string `json:"speed"`
	MAC       string `json:"mac"`
	Gateway   string `json:"gateway"`
	DNS       string `json:"dns"`
}

type Manager struct {
	mu      sync.Mutex
	backend string // "nmcli" or "wpa_cli"
	iface   string
}

func NewManager() *Manager {
	m := &Manager{iface: "wlan0"}

	if _, err := exec.LookPath("nmcli"); err == nil {
		m.backend = "nmcli"
	} else if _, err := exec.LookPath("wpa_cli"); err == nil {
		// Verify wpa_cli can actually connect to wpa_supplicant
		if err := exec.Command("wpa_cli", "-i", m.iface, "ping").Run(); err == nil {
			m.backend = "wpa_cli"
		}
	}

	return m
}

func (m *Manager) Available() bool {
	return m.backend != ""
}

func (m *Manager) Scan() ([]Network, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.backend == "nmcli" {
		return m.scanNmcli()
	}
	return m.scanWpaCli()
}

func (m *Manager) Connect(ssid, password string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.backend == "nmcli" {
		return m.connectNmcli(ssid, password)
	}
	return m.connectWpaCli(ssid, password)
}

func (m *Manager) Disconnect() error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.backend == "nmcli" {
		out, err := exec.Command("nmcli", "device", "disconnect", m.iface).CombinedOutput()
		if err != nil {
			return fmt.Errorf("disconnect failed: %s", string(out))
		}
		return nil
	}
	out, err := exec.Command("wpa_cli", "-i", m.iface, "disconnect").CombinedOutput()
	if err != nil {
		return fmt.Errorf("disconnect failed: %s", string(out))
	}
	return nil
}

func (m *Manager) GetStatus() (*Status, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.backend == "nmcli" {
		return m.statusNmcli()
	}
	return m.statusWpaCli()
}

func (m *Manager) scanNmcli() ([]Network, error) {
	exec.Command("nmcli", "device", "wifi", "rescan").Run()

	out, err := exec.Command("nmcli", "-t", "-f",
		"IN-USE,SSID,BSSID,SIGNAL,FREQ,SECURITY",
		"device", "wifi", "list").Output()
	if err != nil {
		return nil, fmt.Errorf("nmcli scan failed: %w", err)
	}

	var networks []Network
	seen := make(map[string]bool)

	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if line == "" {
			continue
		}
		fields := strings.SplitN(line, ":", 6)
		if len(fields) < 6 {
			continue
		}
		ssid := strings.TrimSpace(fields[1])
		if ssid == "" || ssid == "--" || seen[ssid] {
			continue
		}
		seen[ssid] = true

		signal, _ := strconv.Atoi(strings.TrimSpace(fields[3]))
		networks = append(networks, Network{
			SSID:      ssid,
			BSSID:     strings.TrimSpace(fields[2]),
			Signal:    signal,
			Frequency: strings.TrimSpace(fields[4]),
			Security:  strings.TrimSpace(fields[5]),
			InUse:     strings.TrimSpace(fields[0]) == "*",
		})
	}

	sort.Slice(networks, func(i, j int) bool {
		return networks[i].Signal > networks[j].Signal
	})

	return networks, nil
}

func (m *Manager) connectNmcli(ssid, password string) error {
	var cmd *exec.Cmd
	if password != "" {
		cmd = exec.Command("nmcli", "device", "wifi", "connect", ssid, "password", password, "ifname", m.iface)
	} else {
		cmd = exec.Command("nmcli", "device", "wifi", "connect", ssid, "ifname", m.iface)
	}

	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("connect failed: %s", strings.TrimSpace(string(out)))
	}
	return nil
}

func rssiToPercent(rssi int) int {
	if rssi >= -50 {
		return 100
	} else if rssi <= -100 {
		return 0
	}
	return 2 * (rssi + 100)
}

func (m *Manager) statusNmcli() (*Status, error) {
	out, err := exec.Command("nmcli", "-t", "-f",
		"GENERAL.STATE,GENERAL.CONNECTION,WIRED-PROPERTIES.CARRIER,IP4.ADDRESS,IP4.GATEWAY,IP4.DNS,WIFI.SSID,WIFI.SIGNAL,WIFI.FREQ",
		"device", "show", m.iface).Output()
	if err != nil {
		return &Status{Connected: false}, nil
	}

	s := &Status{}
	for _, line := range strings.Split(string(out), "\n") {
		parts := strings.SplitN(line, ":", 2)
		if len(parts) != 2 {
			continue
		}
		key := strings.TrimSpace(parts[0])
		val := strings.TrimSpace(parts[1])
		switch key {
		case "GENERAL.CONNECTION":
			s.Connected = val != "" && val != "--"
			s.SSID = val
		case "IP4.ADDRESS[1]":
			s.IP = val
		case "IP4.GATEWAY":
			s.Gateway = val
		case "IP4.DNS[1]":
			s.DNS = val
		case "WIFI.SIGNAL":
			s.Signal, _ = strconv.Atoi(val)
		case "WIFI.FREQ":
			s.Frequency = val
		}
	}
	if s.Connected && s.Gateway == "" {
		s.Gateway = getDefaultGateway(m.iface)
	}

	return s, nil
}

func (m *Manager) scanWpaCli() ([]Network, error) {
	// Check if interface exists
	if _, err := os.Stat("/sys/class/net/" + m.iface); os.IsNotExist(err) {
		return nil, fmt.Errorf("wifi interface %s not found", m.iface)
	}

	// Trigger scan
	if err := exec.Command("wpa_cli", "-i", m.iface, "scan").Run(); err != nil {
		// wpa_cli may not be connected to wpa_supplicant, fallback to iw
		return m.scanIw()
	}

	// Wait for scan to complete
	time.Sleep(1500 * time.Millisecond)

	out, err := exec.Command("wpa_cli", "-i", m.iface, "scan_results").Output()
	if err != nil {
		// Fallback to iw scan
		return m.scanIw()
	}

	var networks []Network
	seen := make(map[string]bool)
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")

	for i, line := range lines {
		if i == 0 { // header
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 5 {
			continue
		}
		ssid := strings.Join(fields[4:], " ")
		if ssid == "" || seen[ssid] {
			continue
		}
		seen[ssid] = true

		signal, _ := strconv.Atoi(fields[2])
		// Convert dBm to percentage (rough approximation)
		signalPct := 0
		if signal >= -50 {
			signalPct = 100
		} else if signal <= -100 {
			signalPct = 0
		} else {
			signalPct = 2 * (signal + 100)
		}

		networks = append(networks, Network{
			SSID:      ssid,
			BSSID:     fields[0],
			Signal:    signalPct,
			Frequency: fields[1],
			Security:  fields[3],
		})
	}

	sort.Slice(networks, func(i, j int) bool {
		return networks[i].Signal > networks[j].Signal
	})

	return networks, nil
}

func (m *Manager) scanIw() ([]Network, error) {
	out, err := exec.Command("iw", "dev", m.iface, "scan").Output()
	if err != nil {
		return nil, fmt.Errorf("wifi scan failed: %w", err)
	}

	var networks []Network
	seen := make(map[string]bool)

	var current *Network
	lines := strings.Split(string(out), "\n")

	for _, line := range lines {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "BSS ") {
			if current != nil && current.SSID != "" && !seen[current.SSID] {
				seen[current.SSID] = true
				networks = append(networks, *current)
			}
			current = &Network{}
			parts := strings.Fields(line)
			if len(parts) >= 2 {
				current.BSSID = strings.TrimSuffix(parts[1], "(on")
			}
		} else if current != nil {
			if strings.HasPrefix(line, "freq: ") {
				current.Frequency = strings.TrimPrefix(line, "freq: ")
			} else if strings.HasPrefix(line, "signal: ") {
				sigStr := strings.TrimPrefix(line, "signal: ")
				sigStr = strings.Fields(sigStr)[0]
				signal, _ := strconv.ParseFloat(sigStr, 64)
				signalPct := 0
				if signal >= -50 {
					signalPct = 100
				} else if signal <= -100 {
					signalPct = 0
				} else {
					signalPct = int(2 * (signal + 100))
				}
				current.Signal = signalPct
			} else if strings.HasPrefix(line, "SSID: ") {
				current.SSID = strings.TrimPrefix(line, "SSID: ")
			} else if line == "RSN:" {
				if current.Security == "" || current.Security == "Open" {
					current.Security = "WPA2"
				}
			} else if line == "WPA:" {
				if current.Security == "" {
					current.Security = "WPA"
				}
			}
		}
	}

	if current != nil && current.SSID != "" && !seen[current.SSID] {
		seen[current.SSID] = true
		networks = append(networks, *current)
	}

	for i := range networks {
		if networks[i].Security == "" {
			networks[i].Security = "Open"
		}
	}

	sort.Slice(networks, func(i, j int) bool {
		return networks[i].Signal > networks[j].Signal
	})

	return networks, nil
}

func (m *Manager) connectWpaCli(ssid, password string) error {
	// Add network
	out, err := exec.Command("wpa_cli", "-i", m.iface, "add_network").Output()
	if err != nil {
		return fmt.Errorf("add_network failed: %w", err)
	}
	netID := strings.TrimSpace(string(out))

	// Set SSID
	exec.Command("wpa_cli", "-i", m.iface, "set_network", netID, "ssid", fmt.Sprintf(`"%s"`, ssid)).Run()

	if password != "" {
		exec.Command("wpa_cli", "-i", m.iface, "set_network", netID, "psk", fmt.Sprintf(`"%s"`, password)).Run()
	} else {
		exec.Command("wpa_cli", "-i", m.iface, "set_network", netID, "key_mgmt", "NONE").Run()
	}

	// Enable and select
	exec.Command("wpa_cli", "-i", m.iface, "select_network", netID).Run()
	exec.Command("wpa_cli", "-i", m.iface, "enable_network", netID).Run()
	exec.Command("wpa_cli", "-i", m.iface, "save_config").Run()

	return nil
}

func getDefaultGateway(iface string) string {
	out, err := exec.Command("ip", "route", "show", "default", "dev", iface).Output()
	if err != nil {
		out, err = exec.Command("ip", "route", "show", "default").Output()
		if err != nil {
			return ""
		}
	}
	for _, line := range strings.Split(string(out), "\n") {
		fields := strings.Fields(line)
		for i, f := range fields {
			if f == "via" && i+1 < len(fields) {
				return fields[i+1]
			}
		}
	}
	return ""
}

func (m *Manager) statusWpaCli() (*Status, error) {
	out, err := exec.Command("wpa_cli", "-i", m.iface, "status").Output()
	if err != nil {
		return &Status{Connected: false}, nil
	}

	s := &Status{}
	for _, line := range strings.Split(string(out), "\n") {
		parts := strings.SplitN(line, "=", 2)
		if len(parts) != 2 {
			continue
		}
		switch parts[0] {
		case "wpa_state":
			s.Connected = parts[1] == "COMPLETED"
		case "ssid":
			s.SSID = parts[1]
		case "ip_address":
			s.IP = parts[1]
		case "freq":
			s.Frequency = parts[1]
		case "address":
			s.MAC = parts[1]
		}
	}

	// wpa_cli status does not return signal strength; fetch it separately
	if s.Connected {
		out2, err := exec.Command("wpa_cli", "-i", m.iface, "signal_poll").Output()
		if err == nil {
			for _, line := range strings.Split(string(out2), "\n") {
				parts := strings.SplitN(line, "=", 2)
				if len(parts) != 2 {
					continue
				}
				if parts[0] == "RSSI" {
					rssi, _ := strconv.Atoi(parts[1])
					s.Signal = rssiToPercent(rssi)
					break
				}
			}
		}
		// Fallback to iw if signal_poll failed
		if s.Signal == 0 {
			out3, err := exec.Command("iw", "dev", m.iface, "link").Output()
			if err == nil {
				for _, line := range strings.Split(string(out3), "\n") {
					line = strings.TrimSpace(line)
					if strings.HasPrefix(line, "signal: ") {
						sigStr := strings.TrimPrefix(line, "signal: ")
						sigStr = strings.Fields(sigStr)[0]
						signal, _ := strconv.ParseFloat(sigStr, 64)
						s.Signal = rssiToPercent(int(signal))
						break
					}
				}
			}
		}
	}

	if s.Connected && s.Gateway == "" {
		s.Gateway = getDefaultGateway(m.iface)
	}
	return s, nil
}
