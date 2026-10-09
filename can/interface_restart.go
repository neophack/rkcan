//go:build linux

package can

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// Controller states as reported by the kernel (see "ip -details link show").
const (
	StateErrorActive  = "ERROR-ACTIVE"
	StateErrorWarning = "ERROR-WARNING"
	StateErrorPassive = "ERROR-PASSIVE"
	StateBusOff       = "BUS-OFF"
	StateStopped      = "STOPPED"
	StateSleeping     = "SLEEPING"
	StateUnknown      = "UNKNOWN"
)

// minRestartInterval limits how often a single interface is restarted so that
// several senders noticing the same fault do not bounce the link repeatedly.
const minRestartInterval = 5 * time.Second

var (
	restartMu   sync.Mutex
	lastRestart = map[string]time.Time{}
)

// BusState returns the CAN controller state of ifaceName.
//
// Some vendor kernels expose /sys/class/net/<iface>/can_state; mainline
// kernels only report it via netlink, so "ip -details link show" is used as
// the fallback. StateUnknown is returned when the interface exists but does
// not report a CAN state (e.g. vcan).
func BusState(ifaceName string) (string, error) {
	if ifaceName == "" {
		return "", fmt.Errorf("interface name is empty")
	}

	if data, err := os.ReadFile(fmt.Sprintf("/sys/class/net/%s/can_state", ifaceName)); err == nil {
		if state := strings.ToUpper(strings.TrimSpace(string(data))); state != "" {
			return state, nil
		}
	}

	out, err := exec.Command("ip", "-details", "link", "show", ifaceName).Output()
	if err != nil {
		return "", fmt.Errorf("ip -details link show %s: %w", ifaceName, err)
	}
	return parseIPLinkCANState(string(out)), nil
}

// parseIPLinkCANState extracts the controller state from the output of
// "ip -details link show", e.g.
//
//	can <FD> state ERROR-ACTIVE (berr-counter tx 0 rx 0) restart-ms 0
func parseIPLinkCANState(out string) string {
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 || fields[0] != "can" {
			continue
		}
		for i := 0; i < len(fields)-1; i++ {
			if fields[i] == "state" {
				return strings.ToUpper(strings.TrimSuffix(fields[i+1], ")"))
			}
		}
	}
	return StateUnknown
}

// InterfaceHealthy reports whether the CAN controller is currently able to
// transmit. ERROR-WARNING and ERROR-PASSIVE controllers still transmit, so
// only BUS-OFF, STOPPED and SLEEPING are considered unhealthy. An interface
// that does not report a CAN state is assumed healthy.
func InterfaceHealthy(ifaceName string) (bool, string, error) {
	state, err := BusState(ifaceName)
	if err != nil {
		return false, "", err
	}
	switch state {
	case StateBusOff, StateStopped, StateSleeping:
		return false, state, nil
	default:
		return true, state, nil
	}
}

// RestartInterface performs a down/up cycle for the given CAN interface,
// which resets the controller and clears a BUS-OFF condition. Restarts of the
// same interface are rate limited to one per minRestartInterval.
func RestartInterface(ifaceName string) error {
	if ifaceName == "" {
		return fmt.Errorf("interface name is empty")
	}

	restartMu.Lock()
	defer restartMu.Unlock()

	if last, ok := lastRestart[ifaceName]; ok && time.Since(last) < minRestartInterval {
		return nil
	}
	lastRestart[ifaceName] = time.Now()

	if out, err := exec.Command("ip", "link", "set", ifaceName, "down").CombinedOutput(); err != nil {
		return fmt.Errorf("bring %s down: %w (%s)", ifaceName, err, strings.TrimSpace(string(out)))
	}

	if out, err := exec.Command("ip", "link", "set", ifaceName, "up").CombinedOutput(); err != nil {
		return fmt.Errorf("bring %s up: %w (%s)", ifaceName, err, strings.TrimSpace(string(out)))
	}

	return nil
}
