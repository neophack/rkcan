//go:build linux

package can

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// InterfaceHealthy reports whether the CAN controller is currently able to transmit.
func InterfaceHealthy(ifaceName string) (bool, string, error) {
	if ifaceName == "" {
		return false, "", fmt.Errorf("interface name is empty")
	}

	stateData, err := os.ReadFile(fmt.Sprintf("/sys/class/net/%s/can_state", ifaceName))
	if err != nil {
		return false, "", fmt.Errorf("read can_state for %s: %w", ifaceName, err)
	}

	state := strings.TrimSpace(string(stateData))
	switch state {
	case "ERROR-ACTIVE", "error-active":
		return true, state, nil
	case "STOPPED", "stopped":
		return false, state, nil
	case "ERROR-WARNING", "error-warning", "ERROR-PASSIVE", "error-passive", "BUS-OFF", "bus-off":
		return false, state, nil
	default:
		return false, state, nil
	}
}

// RestartInterface performs a simple down/up cycle for the given CAN interface.
func RestartInterface(ifaceName string) error {
	if ifaceName == "" {
		return fmt.Errorf("interface name is empty")
	}

	if out, err := exec.Command("ip", "link", "set", ifaceName, "down").CombinedOutput(); err != nil {
		return fmt.Errorf("bring %s down: %w (%s)", ifaceName, err, string(out))
	}

	if out, err := exec.Command("ip", "link", "set", ifaceName, "up").CombinedOutput(); err != nil {
		return fmt.Errorf("bring %s up: %w (%s)", ifaceName, err, string(out))
	}

	return nil
}
