//go:build linux

package candiag

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/penghongxia/rkcan/can"
)

type DiagResult struct {
	Interface  string       `json:"interface"`
	Timestamp  string       `json:"timestamp"`
	Overall    string       `json:"overall"` // "OK", "WARNING", "ERROR"
	Checks     []CheckItem  `json:"checks"`
	BitTiming  *BitTiming   `json:"bitTiming,omitempty"`
	Statistics *CANStatInfo `json:"statistics,omitempty"`
}

type CheckItem struct {
	Name   string `json:"name"`
	Status string `json:"status"` // "PASS", "WARN", "FAIL"
	Detail string `json:"detail"`
}

type BitTiming struct {
	Bitrate   uint32 `json:"bitrate"`
	SamplePt  uint32 `json:"samplePt"`
	TQ        uint32 `json:"tq"`
	PropSeg   uint32 `json:"propSeg"`
	PhaseSeg1 uint32 `json:"phaseSeg1"`
	PhaseSeg2 uint32 `json:"phaseSeg2"`
	SJW       uint32 `json:"sjw"`
	BRP       uint32 `json:"brp"`
	Clock     uint32 `json:"clock"`
}

type CANStatInfo struct {
	TxFrames  uint64 `json:"txFrames"`
	RxFrames  uint64 `json:"rxFrames"`
	TxErrors  uint64 `json:"txErrors"`
	RxErrors  uint64 `json:"rxErrors"`
	BusErrors uint64 `json:"busErrors"`
	Restarts  uint64 `json:"restarts"`
	State     string `json:"state"`
}

func RunDiagnostic(ifaceName string) *DiagResult {
	result := &DiagResult{
		Interface: ifaceName,
		Timestamp: time.Now().Format("2006-01-02 15:04:05"),
		Overall:   "OK",
		Checks:    make([]CheckItem, 0),
	}

	checkInterfaceExists(result, ifaceName)
	checkDriverLoaded(result, ifaceName)
	checkInterfaceUp(result, ifaceName)
	checkBitrate(result, ifaceName)
	checkCANFDSupport(result, ifaceName)
	checkBusState(result, ifaceName)
	checkErrorCounters(result, ifaceName)
	readBitTiming(result, ifaceName)
	readStatistics(result, ifaceName)

	for _, c := range result.Checks {
		if c.Status == "FAIL" {
			result.Overall = "ERROR"
			break
		}
		if c.Status == "WARN" && result.Overall != "ERROR" {
			result.Overall = "WARNING"
		}
	}

	return result
}

func checkInterfaceExists(r *DiagResult, iface string) {
	path := fmt.Sprintf("/sys/class/net/%s", iface)
	if _, err := os.Stat(path); os.IsNotExist(err) {
		r.Checks = append(r.Checks, CheckItem{
			Name:   "Interface Exists",
			Status: "FAIL",
			Detail: fmt.Sprintf("Interface %s not found. Check hardware connection and kernel driver.", iface),
		})
		return
	}
	r.Checks = append(r.Checks, CheckItem{
		Name:   "Interface Exists",
		Status: "PASS",
		Detail: fmt.Sprintf("Interface %s found in /sys/class/net/", iface),
	})
}

func checkDriverLoaded(r *DiagResult, iface string) {
	driverPath := fmt.Sprintf("/sys/class/net/%s/device/driver", iface)
	target, err := os.Readlink(driverPath)
	if err != nil {
		r.Checks = append(r.Checks, CheckItem{
			Name:   "CAN Driver",
			Status: "WARN",
			Detail: "Cannot determine CAN driver. Transceiver chip may not be detected.",
		})
		return
	}
	driver := filepath.Base(target)
	r.Checks = append(r.Checks, CheckItem{
		Name:   "CAN Driver",
		Status: "PASS",
		Detail: fmt.Sprintf("Driver loaded: %s", driver),
	})
}

func checkInterfaceUp(r *DiagResult, iface string) {
	flagsData, err := os.ReadFile(fmt.Sprintf("/sys/class/net/%s/flags", iface))
	if err != nil {
		r.Checks = append(r.Checks, CheckItem{
			Name:   "Interface UP",
			Status: "WARN",
			Detail: "Cannot read interface flags",
		})
		return
	}
	flagStr := strings.TrimSpace(string(flagsData))
	flags, err := strconv.ParseUint(strings.TrimPrefix(flagStr, "0x"), 16, 32)
	if err != nil {
		return
	}
	if flags&0x1 != 0 {
		r.Checks = append(r.Checks, CheckItem{
			Name:   "Interface UP",
			Status: "PASS",
			Detail: fmt.Sprintf("Interface %s is UP", iface),
		})
	} else {
		r.Checks = append(r.Checks, CheckItem{
			Name:   "Interface UP",
			Status: "FAIL",
			Detail: fmt.Sprintf("Interface %s is DOWN. Run: ip link set %s up", iface, iface),
		})
	}
}

func checkBitrate(r *DiagResult, iface string) {
	data, err := os.ReadFile(fmt.Sprintf("/sys/class/net/%s/can_bittiming/bitrate", iface))
	if err != nil {
		r.Checks = append(r.Checks, CheckItem{
			Name:   "Bitrate Configuration",
			Status: "WARN",
			Detail: "Cannot read bitrate. Interface may not be a CAN device.",
		})
		return
	}
	bitrate := strings.TrimSpace(string(data))
	br, _ := strconv.ParseUint(bitrate, 10, 64)
	if br == 0 {
		r.Checks = append(r.Checks, CheckItem{
			Name:   "Bitrate Configuration",
			Status: "FAIL",
			Detail: fmt.Sprintf("Bitrate is 0. Configure with: ip link set %s type can bitrate 500000", iface),
		})
		return
	}
	r.Checks = append(r.Checks, CheckItem{
		Name:   "Bitrate Configuration",
		Status: "PASS",
		Detail: fmt.Sprintf("Bitrate: %d bps (%d kbps)", br, br/1000),
	})
}

func checkCANFDSupport(r *DiagResult, iface string) {
	mtuData, err := os.ReadFile(fmt.Sprintf("/sys/class/net/%s/mtu", iface))
	if err != nil {
		return
	}
	mtu, _ := strconv.Atoi(strings.TrimSpace(string(mtuData)))
	if mtu == 72 {
		r.Checks = append(r.Checks, CheckItem{
			Name:   "CAN-FD Support",
			Status: "PASS",
			Detail: "MTU=72, CAN-FD mode enabled",
		})
	} else if mtu == 16 {
		r.Checks = append(r.Checks, CheckItem{
			Name:   "CAN-FD Support",
			Status: "WARN",
			Detail: fmt.Sprintf("MTU=16, Classic CAN mode. Enable FD with: ip link set %s type can bitrate 500000 dbitrate 2000000 fd on", iface),
		})
	} else {
		r.Checks = append(r.Checks, CheckItem{
			Name:   "CAN-FD Support",
			Status: "WARN",
			Detail: fmt.Sprintf("Unexpected MTU=%d", mtu),
		})
	}
}

func checkBusState(r *DiagResult, iface string) {
	state, err := can.BusState(iface)
	if err != nil || state == can.StateUnknown {
		return
	}
	status := "PASS"
	detail := fmt.Sprintf("Bus state: %s", state)

	switch state {
	case "ERROR-ACTIVE":
		detail = "Bus state: ERROR-ACTIVE (normal operation)"
	case "ERROR-WARNING":
		status = "WARN"
		detail = "Bus state: ERROR-WARNING (elevated error count, check cable and termination)"
	case "ERROR-PASSIVE":
		status = "WARN"
		detail = "Bus state: ERROR-PASSIVE (high error count, check cable/bitrate/termination)"
	case "BUS-OFF":
		status = "FAIL"
		detail = "Bus state: BUS-OFF (too many errors, check: 1) cable connection 2) bitrate match 3) termination 120Ω)"
	case "STOPPED":
		status = "WARN"
		detail = "Bus state: STOPPED (interface down)"
	}

	r.Checks = append(r.Checks, CheckItem{
		Name:   "Bus State",
		Status: status,
		Detail: detail,
	})
}

func checkErrorCounters(r *DiagResult, iface string) {
	output, err := exec.Command("ip", "-s", "-d", "link", "show", iface).Output()
	if err != nil {
		return
	}
	lines := strings.Split(string(output), "\n")
	for _, line := range lines {
		if strings.Contains(line, "bus-error") || strings.Contains(line, "restarts") {
			r.Checks = append(r.Checks, CheckItem{
				Name:   "Error Counters",
				Status: "PASS",
				Detail: strings.TrimSpace(line),
			})
		}
	}
}

func readBitTiming(r *DiagResult, iface string) {
	base := fmt.Sprintf("/sys/class/net/%s/can_bittiming", iface)
	bt := &BitTiming{}

	readUint32 := func(name string) uint32 {
		data, err := os.ReadFile(filepath.Join(base, name))
		if err != nil {
			return 0
		}
		v, _ := strconv.ParseUint(strings.TrimSpace(string(data)), 10, 32)
		return uint32(v)
	}

	bt.Bitrate = readUint32("bitrate")
	bt.SamplePt = readUint32("sample_point")
	bt.TQ = readUint32("tq")
	bt.PropSeg = readUint32("prop_seg")
	bt.PhaseSeg1 = readUint32("phase_seg1")
	bt.PhaseSeg2 = readUint32("phase_seg2")
	bt.SJW = readUint32("sjw")
	bt.BRP = readUint32("brp")

	clockData, err := os.ReadFile(fmt.Sprintf("/sys/class/net/%s/can_clock/freq", iface))
	if err == nil {
		v, _ := strconv.ParseUint(strings.TrimSpace(string(clockData)), 10, 32)
		bt.Clock = uint32(v)
	}

	if bt.Bitrate > 0 {
		r.BitTiming = bt
	}
}

func readStatistics(r *DiagResult, iface string) {
	stat := &CANStatInfo{}

	readStat := func(name string) uint64 {
		data, err := os.ReadFile(fmt.Sprintf("/sys/class/net/%s/statistics/%s", iface, name))
		if err != nil {
			return 0
		}
		v, _ := strconv.ParseUint(strings.TrimSpace(string(data)), 10, 64)
		return v
	}

	stat.TxFrames = readStat("tx_packets")
	stat.RxFrames = readStat("rx_packets")
	stat.TxErrors = readStat("tx_errors")
	stat.RxErrors = readStat("rx_errors")

	stat.State, _ = can.BusState(iface)
	stat.BusErrors, stat.Restarts = readCANXstats(iface)

	r.Statistics = stat
}

// readCANXstats parses the CAN extended statistics from
// "ip -s -d link show", e.g.
//
//	re-started bus-errors arbit-lost error-warn error-pass bus-off
//	0          12         0          1          0          0
func readCANXstats(iface string) (busErrors, restarts uint64) {
	out, err := exec.Command("ip", "-s", "-d", "link", "show", iface).Output()
	if err != nil {
		return 0, 0
	}
	lines := strings.Split(string(out), "\n")
	for i, line := range lines {
		hdr := strings.Fields(line)
		if len(hdr) == 0 || hdr[0] != "re-started" || i+1 >= len(lines) {
			continue
		}
		vals := strings.Fields(lines[i+1])
		for j, name := range hdr {
			if j >= len(vals) {
				break
			}
			v, _ := strconv.ParseUint(vals[j], 10, 64)
			switch name {
			case "re-started":
				restarts = v
			case "bus-errors":
				busErrors = v
			}
		}
		break
	}
	return busErrors, restarts
}

func DiagnosticJSON(ifaces []string) ([]byte, error) {
	results := make([]*DiagResult, 0, len(ifaces))
	for _, iface := range ifaces {
		results = append(results, RunDiagnostic(iface))
	}
	return json.Marshal(results)
}

type BitratePreset struct {
	Name       string  `json:"name"`
	Bitrate    uint32  `json:"bitrate"`
	Clock      uint32  `json:"clock"`
	Prescaler  uint32  `json:"prescaler"`
	Tseg1      uint32  `json:"tseg1"`
	Tseg2      uint32  `json:"tseg2"`
	SJW        uint32  `json:"sjw"`
	SamplePt   float64 `json:"samplePt"`
	TQ         uint32  `json:"tq"`
	TimeQuanta uint32  `json:"timeQuanta"`
}

// CANInterfaceInfo holds detailed information about a CAN interface
// (equivalent to "ip -details link show <iface>")
type CANInterfaceInfo struct {
	Interface    string  `json:"interface"`
	State        string  `json:"state"` // UP / DOWN
	MTU          int     `json:"mtu"`
	BusState     string  `json:"busState"` // ERROR-ACTIVE, ERROR-WARNING, etc.
	Controller   string  `json:"controller"`
	Bitrate      uint32  `json:"bitrate"`
	SamplePoint  float64 `json:"samplePoint"`
	TQ           uint32  `json:"tq"`
	PropSeg      uint32  `json:"propSeg"`
	PhaseSeg1    uint32  `json:"phaseSeg1"`
	PhaseSeg2    uint32  `json:"phaseSeg2"`
	SJW          uint32  `json:"sjw"`
	BRP          uint32  `json:"brp"`
	DBitrate     uint32  `json:"dbitrate"`
	DSamplePoint float64 `json:"dsamplePoint"`
	DTQ          uint32  `json:"dtq"`
	DPropSeg     uint32  `json:"dpropSeg"`
	DPhaseSeg1   uint32  `json:"dphaseSeg1"`
	DPhaseSeg2   uint32  `json:"dphaseSeg2"`
	DSJW         uint32  `json:"dsjw"`
	DBRP         uint32  `json:"dbrp"`
	Clock        uint32  `json:"clock"`
	RestartMS    int     `json:"restartMs"`
	BerrTx       uint32  `json:"berrTx"`
	BerrRx       uint32  `json:"berrRx"`
}

// GetCANInterfaceInfo reads detailed CAN interface info from sysfs
// and supplements it with "ip -details link show" for berr-counter.
func GetCANInterfaceInfo(iface string) (*CANInterfaceInfo, error) {
	info := &CANInterfaceInfo{Interface: iface}

	// Helper to extract value after an exact key from space-separated fields
	extractField := func(line, key string) string {
		fields := strings.Fields(line)
		for i := 0; i < len(fields)-1; i++ {
			if fields[i] == key {
				return fields[i+1]
			}
		}
		return ""
	}

	// --- Interface state from flags ---
	flagsData, err := os.ReadFile(fmt.Sprintf("/sys/class/net/%s/flags", iface))
	if err == nil {
		flagStr := strings.TrimSpace(string(flagsData))
		flags, _ := strconv.ParseUint(strings.TrimPrefix(flagStr, "0x"), 16, 32)
		if flags&0x1 != 0 {
			info.State = "UP"
		} else {
			info.State = "DOWN"
		}
	}

	// --- MTU ---
	mtuData, err := os.ReadFile(fmt.Sprintf("/sys/class/net/%s/mtu", iface))
	if err == nil {
		info.MTU, _ = strconv.Atoi(strings.TrimSpace(string(mtuData)))
	}

	// --- Bus state (also refined below from "ip -details") ---
	if state, err := can.BusState(iface); err == nil && state != can.StateUnknown {
		info.BusState = state
	}

	// --- Bit timing helpers ---
	readTiming := func(basePath string, nominal bool) {
		readUint32 := func(name string) uint32 {
			data, err := os.ReadFile(filepath.Join(basePath, name))
			if err != nil {
				return 0
			}
			v, _ := strconv.ParseUint(strings.TrimSpace(string(data)), 10, 32)
			return uint32(v)
		}
		readFloat := func(name string) float64 {
			data, err := os.ReadFile(filepath.Join(basePath, name))
			if err != nil {
				return 0
			}
			s := strings.TrimSpace(string(data))
			v, _ := strconv.ParseFloat(s, 64)
			// sysfs may store 870 for 0.870; normalize
			if v > 100 {
				v = v / 1000.0
			} else if v > 1 {
				v = v / 100.0
			}
			return v
		}

		if nominal {
			info.Bitrate = readUint32("bitrate")
			info.SamplePoint = readFloat("sample_point")
			info.TQ = readUint32("tq")
			info.PropSeg = readUint32("prop_seg")
			info.PhaseSeg1 = readUint32("phase_seg1")
			info.PhaseSeg2 = readUint32("phase_seg2")
			info.SJW = readUint32("sjw")
			info.BRP = readUint32("brp")
		} else {
			info.DBitrate = readUint32("bitrate")
			info.DSamplePoint = readFloat("sample_point")
			info.DTQ = readUint32("tq")
			info.DPropSeg = readUint32("prop_seg")
			info.DPhaseSeg1 = readUint32("phase_seg1")
			info.DPhaseSeg2 = readUint32("phase_seg2")
			info.DSJW = readUint32("sjw")
			info.DBRP = readUint32("brp")
		}
	}

	readTiming(fmt.Sprintf("/sys/class/net/%s/can_bittiming", iface), true)
	readTiming(fmt.Sprintf("/sys/class/net/%s/can_data_bittiming", iface), false)

	// --- Clock ---
	clockData, err := os.ReadFile(fmt.Sprintf("/sys/class/net/%s/can_clock/freq", iface))
	if err == nil {
		v, _ := strconv.ParseUint(strings.TrimSpace(string(clockData)), 10, 32)
		info.Clock = uint32(v)
	}

	// --- Restart ms ---
	restartData, err := os.ReadFile(fmt.Sprintf("/sys/class/net/%s/can_restart_ms", iface))
	if err == nil {
		info.RestartMS, _ = strconv.Atoi(strings.TrimSpace(string(restartData)))
	}

	// --- Parse ip -details link show for controller, counters, state and all timing ---
	output, err := exec.Command("ip", "-details", "link", "show", iface).Output()
	if err == nil {
		lines := strings.Split(string(output), "\n")
		for _, line := range lines {
			line = strings.TrimSpace(line)

			// Bus state, berr-counter, restart-ms
			// e.g. "can <FD> state ERROR-ACTIVE (berr-counter tx 0 rx 0) restart-ms 0"
			if strings.HasPrefix(line, "can ") && strings.Contains(line, " state ") {
				parts := strings.Fields(line)
				for i := 0; i < len(parts)-1; i++ {
					if parts[i] == "state" {
						info.BusState = strings.TrimSuffix(parts[i+1], ")")
						break
					}
				}
				if strings.Contains(line, "berr-counter tx") {
					start := strings.Index(line, "berr-counter tx")
					rest := line[start+len("berr-counter tx"):]
					rest = strings.TrimSuffix(rest, ")")
					fields := strings.Fields(rest)
					if len(fields) >= 1 {
						v, _ := strconv.ParseUint(fields[0], 10, 32)
						info.BerrTx = uint32(v)
					}
					if len(fields) >= 3 && fields[1] == "rx" {
						v, _ := strconv.ParseUint(fields[2], 10, 32)
						info.BerrRx = uint32(v)
					}
				}
				if v := extractField(line, "restart-ms"); v != "" {
					if val, err := strconv.Atoi(v); err == nil {
						info.RestartMS = val
					}
				}
			}

			// Controller name, e.g. "rk3576_canfd: tseg1 1..128 ..."
			if strings.Contains(line, "_can") || strings.Contains(line, "_canfd") {
				if idx := strings.Index(line, ":"); idx > 0 {
					ctrl := strings.TrimSpace(line[:idx])
					if strings.Contains(ctrl, "_can") {
						info.Controller = ctrl
					}
				}
			}

			// Nominal timing
			if v := extractField(line, "bitrate"); v != "" {
				if val, err := strconv.ParseUint(v, 10, 32); err == nil && val > 0 {
					info.Bitrate = uint32(val)
				}
			}
			if v := extractField(line, "sample-point"); v != "" {
				if val, err := strconv.ParseFloat(v, 64); err == nil && val > 0 {
					info.SamplePoint = val
				}
			}
			if v := extractField(line, "tq"); v != "" {
				if val, err := strconv.ParseUint(v, 10, 32); err == nil && val > 0 {
					info.TQ = uint32(val)
				}
			}
			if v := extractField(line, "prop-seg"); v != "" {
				if val, err := strconv.ParseUint(v, 10, 32); err == nil && val > 0 {
					info.PropSeg = uint32(val)
				}
			}
			if v := extractField(line, "phase-seg1"); v != "" {
				if val, err := strconv.ParseUint(v, 10, 32); err == nil && val > 0 {
					info.PhaseSeg1 = uint32(val)
				}
			}
			if v := extractField(line, "phase-seg2"); v != "" {
				if val, err := strconv.ParseUint(v, 10, 32); err == nil && val > 0 {
					info.PhaseSeg2 = uint32(val)
				}
			}
			if v := extractField(line, "sjw"); v != "" {
				if val, err := strconv.ParseUint(v, 10, 32); err == nil && val > 0 {
					info.SJW = uint32(val)
				}
			}
			if v := extractField(line, "brp"); v != "" {
				if val, err := strconv.ParseUint(v, 10, 32); err == nil && val > 0 {
					info.BRP = uint32(val)
				}
			}

			// Data timing (CAN-FD)
			if v := extractField(line, "dbitrate"); v != "" {
				if val, err := strconv.ParseUint(v, 10, 32); err == nil && val > 0 {
					info.DBitrate = uint32(val)
				}
			}
			if v := extractField(line, "dsample-point"); v != "" {
				if val, err := strconv.ParseFloat(v, 64); err == nil && val > 0 {
					info.DSamplePoint = val
				}
			}
			if v := extractField(line, "dtq"); v != "" {
				if val, err := strconv.ParseUint(v, 10, 32); err == nil && val > 0 {
					info.DTQ = uint32(val)
				}
			}
			if v := extractField(line, "dprop-seg"); v != "" {
				if val, err := strconv.ParseUint(v, 10, 32); err == nil && val > 0 {
					info.DPropSeg = uint32(val)
				}
			}
			if v := extractField(line, "dphase-seg1"); v != "" {
				if val, err := strconv.ParseUint(v, 10, 32); err == nil && val > 0 {
					info.DPhaseSeg1 = uint32(val)
				}
			}
			if v := extractField(line, "dphase-seg2"); v != "" {
				if val, err := strconv.ParseUint(v, 10, 32); err == nil && val > 0 {
					info.DPhaseSeg2 = uint32(val)
				}
			}
			if v := extractField(line, "dsjw"); v != "" {
				if val, err := strconv.ParseUint(v, 10, 32); err == nil && val > 0 {
					info.DSJW = uint32(val)
				}
			}
			if v := extractField(line, "dbrp"); v != "" {
				if val, err := strconv.ParseUint(v, 10, 32); err == nil && val > 0 {
					info.DBRP = uint32(val)
				}
			}

			// Clock
			if v := extractField(line, "clock"); v != "" {
				if val, err := strconv.ParseUint(v, 10, 32); err == nil && val > 0 {
					info.Clock = uint32(val)
				}
			}
		}
	}

	return info, nil
}
