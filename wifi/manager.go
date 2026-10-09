//go:build linux

package wifi

import (
	"encoding/hex"
	"fmt"
	"log"
	"net"
	"os"
	"os/exec"
	"path/filepath"
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
	mu      sync.Mutex // guards backend
	opMu    sync.Mutex // serialises state-changing operations (connect, forget...)
	backend string     // "nmcli" or "wpa_cli"
	iface   string
}

// NewManager returns a WiFi manager for iface (default "wlan0").
func NewManager(iface string) *Manager {
	if iface == "" {
		iface = "wlan0"
	}
	m := &Manager{iface: iface}
	m.detectBackend()
	return m
}

// detectBackend selects nmcli or wpa_cli. It is re-run while no backend is
// found, so a supplicant started after rkcan is picked up. Caller must hold
// m.mu (or be the constructor).
func (m *Manager) detectBackend() {
	if m.backend != "" {
		return
	}
	if _, err := exec.LookPath("nmcli"); err == nil {
		if exec.Command("nmcli", "-t", "general", "status").Run() == nil {
			m.backend = "nmcli"
			return
		}
	}
	if _, err := exec.LookPath("wpa_cli"); err == nil {
		// Verify wpa_cli can actually connect to wpa_supplicant
		if exec.Command("wpa_cli", "-i", m.iface, "ping").Run() == nil {
			m.backend = "wpa_cli"
		}
	}
}

func (m *Manager) Available() bool {
	return m.currentBackend() != ""
}

// currentBackend returns the detected backend, retrying detection if none.
func (m *Manager) currentBackend() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.detectBackend()
	return m.backend
}

var errNoBackend = fmt.Errorf("no WiFi backend available (need NetworkManager or wpa_supplicant)")

func (m *Manager) Scan() ([]Network, error) {
	switch m.currentBackend() {
	case "nmcli":
		return m.scanNmcli()
	case "wpa_cli":
		return m.scanWpaCli()
	}
	// No supplicant control available; a passive iw scan may still work
	return m.scanIw()
}

// Connect joins a network and waits until it has an IP address. hidden
// enables directed probing for networks that do not broadcast their SSID.
func (m *Manager) Connect(ssid, password string, hidden bool) error {
	m.opMu.Lock()
	defer m.opMu.Unlock()
	switch m.currentBackend() {
	case "nmcli":
		return m.connectNmcli(ssid, password, hidden)
	case "wpa_cli":
		return m.connectWpaCli(ssid, password, hidden)
	}
	return errNoBackend
}

func (m *Manager) Disconnect() error {
	m.opMu.Lock()
	defer m.opMu.Unlock()
	var out []byte
	var err error
	switch m.currentBackend() {
	case "nmcli":
		out, err = exec.Command("nmcli", "device", "disconnect", m.iface).CombinedOutput()
	case "wpa_cli":
		out, err = exec.Command("wpa_cli", "-i", m.iface, "disconnect").CombinedOutput()
	default:
		return errNoBackend
	}
	if err != nil {
		return fmt.Errorf("disconnect failed: %s", strings.TrimSpace(string(out)))
	}
	return nil
}

func (m *Manager) GetStatus() (*Status, error) {
	switch m.currentBackend() {
	case "nmcli":
		return m.statusNmcli()
	case "wpa_cli":
		return m.statusWpaCli()
	}
	s := &Status{Connected: false}
	if ifc, err := net.InterfaceByName(m.iface); err == nil {
		s.MAC = ifc.HardwareAddr.String()
		s.IP = ifaceIPv4(m.iface)
	}
	return s, nil
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
		fields := splitNmcliTerse(line)
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

func (m *Manager) connectNmcli(ssid, password string, hidden bool) error {
	args := []string{"--wait", "30", "device", "wifi", "connect", ssid}
	if password != "" {
		args = append(args, "password", password)
	}
	args = append(args, "ifname", m.iface)
	if hidden {
		args = append(args, "hidden", "yes")
	}

	out, err := exec.Command("nmcli", args...).CombinedOutput()
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
		"GENERAL.HWADDR,GENERAL.STATE,GENERAL.CONNECTION,IP4.ADDRESS,IP4.GATEWAY,IP4.DNS",
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
		val := strings.ReplaceAll(strings.TrimSpace(parts[1]), `\:`, ":")
		switch key {
		case "GENERAL.HWADDR":
			s.MAC = val
		case "GENERAL.STATE":
			// e.g. "100 (connected)"
			s.Connected = strings.HasPrefix(val, "100")
		case "GENERAL.CONNECTION":
			if val != "" && val != "--" {
				s.SSID = val
			}
		case "IP4.ADDRESS[1]":
			s.IP = val
		case "IP4.GATEWAY":
			if val != "--" {
				s.Gateway = val
			}
		case "IP4.DNS[1]":
			s.DNS = val
		}
	}

	if s.Connected {
		// Signal/frequency/SSID of the active AP from the cached scan list
		out, err := exec.Command("nmcli", "-t", "-f", "IN-USE,SSID,SIGNAL,FREQ,RATE",
			"device", "wifi", "list", "ifname", m.iface, "--rescan", "no").Output()
		if err == nil {
			for _, line := range strings.Split(string(out), "\n") {
				f := splitNmcliTerse(line)
				if len(f) >= 5 && f[0] == "*" {
					s.SSID = f[1]
					s.Signal, _ = strconv.Atoi(f[2])
					s.Frequency = f[3]
					s.Speed = f[4]
					break
				}
			}
		}
		if s.Gateway == "" {
			s.Gateway = getDefaultGateway(m.iface)
		}
	}

	return s, nil
}

// splitNmcliTerse splits a line of "nmcli -t" output on unescaped ':'
// separators and unescapes "\:" and "\\" inside values.
func splitNmcliTerse(line string) []string {
	var fields []string
	var cur strings.Builder
	for i := 0; i < len(line); i++ {
		c := line[i]
		switch {
		case c == '\\' && i+1 < len(line):
			i++
			cur.WriteByte(line[i])
		case c == ':':
			fields = append(fields, cur.String())
			cur.Reset()
		default:
			cur.WriteByte(c)
		}
	}
	return append(fields, cur.String())
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
		signalPct := rssiToPercent(signal)

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
				if f := strings.Fields(strings.TrimPrefix(line, "signal: ")); len(f) > 0 {
					signal, _ := strconv.ParseFloat(f[0], 64)
					current.Signal = rssiToPercent(int(signal))
				}
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

// wpaCli runs a wpa_cli command and fails on "FAIL" replies, which wpa_cli
// reports with exit status 0.
func (m *Manager) wpaCli(args ...string) (string, error) {
	out, err := exec.Command("wpa_cli", append([]string{"-i", m.iface}, args...)...).CombinedOutput()
	res := strings.TrimSpace(string(out))
	if err != nil {
		return res, fmt.Errorf("wpa_cli %s: %w (%s)", args[0], err, res)
	}
	if strings.HasPrefix(res, "FAIL") {
		return res, fmt.Errorf("wpa_cli %s failed", args[0])
	}
	return res, nil
}

func (m *Manager) connectWpaCli(ssid, password string, hidden bool) error {
	if ssid == "" || len(ssid) > 32 {
		return fmt.Errorf("invalid SSID")
	}
	if password != "" && (len(password) < 8 || len(password) > 63) {
		return fmt.Errorf("WPA passphrase must be 8..63 characters")
	}
	if strings.ContainsAny(password, "\"\n\r") {
		return fmt.Errorf("passphrase must not contain quotes or newlines")
	}

	// Replace any saved entry for this SSID so retries do not pile up
	if nets, err := m.savedWpaCli(); err == nil {
		for _, n := range nets {
			if n.SSID == ssid {
				m.wpaCli("remove_network", n.ID)
			}
		}
	}

	netID, err := m.wpaCli("add_network")
	if err != nil {
		return err
	}
	if _, err := strconv.Atoi(netID); err != nil {
		return fmt.Errorf("add_network returned %q", netID)
	}

	cleanup := func(e error) error {
		m.wpaCli("remove_network", netID)
		m.wpaCli("save_config")
		return e
	}

	// Hex-encoded SSID avoids any quoting issues
	if _, err := m.wpaCli("set_network", netID, "ssid", hex.EncodeToString([]byte(ssid))); err != nil {
		return cleanup(err)
	}
	if password != "" {
		if _, err := m.wpaCli("set_network", netID, "psk", `"`+password+`"`); err != nil {
			return cleanup(err)
		}
	} else {
		if _, err := m.wpaCli("set_network", netID, "key_mgmt", "NONE"); err != nil {
			return cleanup(err)
		}
	}
	if hidden {
		if _, err := m.wpaCli("set_network", netID, "scan_ssid", "1"); err != nil {
			return cleanup(err)
		}
	}

	// enable_network keeps other saved networks usable; select_network
	// switches to this one now.
	if _, err := m.wpaCli("enable_network", netID); err != nil {
		return cleanup(err)
	}
	if _, err := m.wpaCli("select_network", netID); err != nil {
		return cleanup(err)
	}

	if err := m.waitAssociated(25 * time.Second); err != nil {
		return cleanup(fmt.Errorf("could not connect to %q: %v", ssid, err))
	}
	m.wpaCli("enable_network", "all") // select_network disabled the others
	if _, err := m.wpaCli("save_config"); err != nil {
		log.Printf("wifi: save_config failed (network will not persist across reboot): %v", err)
	}

	return m.ensureDHCP()
}

// waitAssociated polls wpa_supplicant until the link is up.
func (m *Manager) waitAssociated(timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	lastState := ""
	for time.Now().Before(deadline) {
		out, err := m.wpaCli("status")
		if err == nil {
			for _, line := range strings.Split(out, "\n") {
				if v, ok := strings.CutPrefix(line, "wpa_state="); ok {
					lastState = v
				}
			}
			if lastState == "COMPLETED" {
				return nil
			}
		}
		time.Sleep(500 * time.Millisecond)
	}
	switch lastState {
	case "4WAY_HANDSHAKE", "GROUP_HANDSHAKE":
		return fmt.Errorf("authentication failed (wrong password?)")
	case "SCANNING", "DISCONNECTED", "INACTIVE":
		return fmt.Errorf("network not found or out of range")
	}
	return fmt.Errorf("timeout (state %s)", lastState)
}

// ensureDHCP obtains an IPv4 address on the interface if it has none,
// using whichever DHCP client the system provides.
func (m *Manager) ensureDHCP() error {
	if ifaceIPv4(m.iface) != "" {
		return nil
	}
	if !dhcpClientRunning(m.iface) {
		var cmd *exec.Cmd
		switch {
		case lookPath("dhcpcd"):
			cmd = exec.Command("dhcpcd", "-n", m.iface)
		case lookPath("udhcpc"):
			// Runs in the background (-b) and keeps renewing the lease
			cmd = exec.Command("udhcpc", "-b", "-R", "-i", m.iface, "-p", "/var/run/udhcpc."+m.iface+".pid")
		case lookPath("dhclient"):
			cmd = exec.Command("dhclient", "-nw", m.iface)
		default:
			return fmt.Errorf("connected, but no DHCP client found (install udhcpc, dhcpcd or dhclient)")
		}
		if out, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("connected, but DHCP client failed: %v (%s)", err, strings.TrimSpace(string(out)))
		}
	}
	for i := 0; i < 30; i++ {
		if ifaceIPv4(m.iface) != "" {
			return nil
		}
		time.Sleep(500 * time.Millisecond)
	}
	return fmt.Errorf("connected, but no IP address from DHCP")
}

func lookPath(name string) bool {
	_, err := exec.LookPath(name)
	return err == nil
}

func ifaceIPv4(iface string) string {
	ifc, err := net.InterfaceByName(iface)
	if err != nil {
		return ""
	}
	addrs, err := ifc.Addrs()
	if err != nil {
		return ""
	}
	for _, a := range addrs {
		if ipn, ok := a.(*net.IPNet); ok && ipn.IP.To4() != nil {
			return ipn.IP.String()
		}
	}
	return ""
}

// dhcpClientRunning reports whether a DHCP client process already manages iface.
func dhcpClientRunning(iface string) bool {
	procs, _ := filepath.Glob("/proc/[0-9]*/cmdline")
	for _, p := range procs {
		b, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		args := strings.Split(strings.TrimRight(string(b), "\x00"), "\x00")
		if len(args) == 0 {
			continue
		}
		name := filepath.Base(args[0])
		switch name {
		case "dhcpcd":
			return true // manages all interfaces unless restricted
		case "udhcpc", "dhclient":
			for _, a := range args[1:] {
				if a == iface {
					return true
				}
			}
		}
	}
	return false
}

// SavedNetwork is a configured network.
type SavedNetwork struct {
	ID      string `json:"id"` // wpa_supplicant network id or NetworkManager UUID
	SSID    string `json:"ssid"`
	Current bool   `json:"current"`
}

// Saved lists configured networks.
func (m *Manager) Saved() ([]SavedNetwork, error) {
	switch m.currentBackend() {
	case "nmcli":
		out, err := exec.Command("nmcli", "-t", "-f", "NAME,UUID,TYPE,DEVICE", "connection", "show").Output()
		if err != nil {
			return nil, fmt.Errorf("nmcli: %w", err)
		}
		nets := []SavedNetwork{}
		for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
			f := splitNmcliTerse(line)
			if len(f) >= 4 && f[2] == "802-11-wireless" {
				nets = append(nets, SavedNetwork{ID: f[1], SSID: f[0], Current: f[3] == m.iface})
			}
		}
		return nets, nil
	case "wpa_cli":
		return m.savedWpaCli()
	}
	return []SavedNetwork{}, nil
}

func (m *Manager) savedWpaCli() ([]SavedNetwork, error) {
	out, err := m.wpaCli("list_networks")
	if err != nil {
		return nil, err
	}
	nets := []SavedNetwork{}
	for i, line := range strings.Split(out, "\n") {
		if i == 0 { // header: network id / ssid / bssid / flags
			continue
		}
		f := strings.Split(line, "\t")
		if len(f) < 2 {
			continue
		}
		n := SavedNetwork{ID: f[0], SSID: unescapeWpa(f[1])}
		if len(f) >= 4 {
			n.Current = strings.Contains(f[3], "[CURRENT]")
		}
		nets = append(nets, n)
	}
	return nets, nil
}

// unescapeWpa decodes wpa_cli's printf-style escaping of SSIDs (\xNN).
func unescapeWpa(s string) string {
	if !strings.Contains(s, "\\") {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+3 < len(s) && s[i+1] == 'x' {
			if v, err := strconv.ParseUint(s[i+2:i+4], 16, 8); err == nil {
				b.WriteByte(byte(v))
				i += 3
				continue
			}
		}
		if s[i] == '\\' && i+1 < len(s) {
			i++
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

// ConnectSaved activates a saved network.
func (m *Manager) ConnectSaved(id string) error {
	m.opMu.Lock()
	defer m.opMu.Unlock()
	switch m.currentBackend() {
	case "nmcli":
		out, err := exec.Command("nmcli", "--wait", "30", "connection", "up", "uuid", id, "ifname", m.iface).CombinedOutput()
		if err != nil {
			return fmt.Errorf("connect failed: %s", strings.TrimSpace(string(out)))
		}
		return nil
	case "wpa_cli":
		if _, err := strconv.Atoi(id); err != nil {
			return fmt.Errorf("invalid network id")
		}
		if _, err := m.wpaCli("select_network", id); err != nil {
			return err
		}
		if err := m.waitAssociated(25 * time.Second); err != nil {
			return err
		}
		m.wpaCli("enable_network", "all")
		return m.ensureDHCP()
	}
	return errNoBackend
}

// Forget deletes a saved network.
func (m *Manager) Forget(id string) error {
	m.opMu.Lock()
	defer m.opMu.Unlock()
	switch m.currentBackend() {
	case "nmcli":
		out, err := exec.Command("nmcli", "connection", "delete", "uuid", id).CombinedOutput()
		if err != nil {
			return fmt.Errorf("delete failed: %s", strings.TrimSpace(string(out)))
		}
		return nil
	case "wpa_cli":
		if _, err := strconv.Atoi(id); err != nil {
			return fmt.Errorf("invalid network id")
		}
		if _, err := m.wpaCli("remove_network", id); err != nil {
			return err
		}
		if _, err := m.wpaCli("save_config"); err != nil {
			log.Printf("wifi: save_config failed: %v", err)
		}
		return nil
	}
	return errNoBackend
}

// Backend returns "nmcli", "wpa_cli" or "" when WiFi cannot be managed.
func (m *Manager) Backend() string {
	return m.currentBackend()
}

// Iface returns the managed interface name.
func (m *Manager) Iface() string { return m.iface }

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
						if f := strings.Fields(strings.TrimPrefix(line, "signal: ")); len(f) > 0 {
							signal, _ := strconv.ParseFloat(f[0], 64)
							s.Signal = rssiToPercent(int(signal))
						}
						break
					}
				}
			}
		}
	}

	if s.IP == "" {
		s.IP = ifaceIPv4(m.iface)
	}
	if s.Connected && s.Gateway == "" {
		s.Gateway = getDefaultGateway(m.iface)
	}
	return s, nil
}
