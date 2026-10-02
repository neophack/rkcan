//go:build linux

package main

import (
	"flag"
	"fmt"
	"os"
	"strings"
)

// envPrefix is prepended to the upper-cased flag name (with '-' replaced by
// '_') to form the environment variable that sets a flag's default, e.g.
// -timesync-proto  ->  RKCAN_TIMESYNC_PROTO. This lets one KEY=VALUE file be
// used both as a systemd EnvironmentFile and by the SysV init script.
const envPrefix = "RKCAN_"

func envName(flagName string) string {
	return envPrefix + strings.ToUpper(strings.ReplaceAll(flagName, "-", "_"))
}

// applyEnvDefaults sets flags from RKCAN_* environment variables. It must be
// called before flag.Parse so that command line arguments still win.
func applyEnvDefaults(fs *flag.FlagSet) error {
	var firstErr error
	fs.VisitAll(func(f *flag.Flag) {
		if firstErr != nil {
			return
		}
		val, ok := os.LookupEnv(envName(f.Name))
		if !ok {
			return
		}
		if err := fs.Set(f.Name, strings.TrimSpace(val)); err != nil {
			firstErr = fmt.Errorf("%s=%q: %w", envName(f.Name), val, err)
		}
	})
	return firstErr
}
