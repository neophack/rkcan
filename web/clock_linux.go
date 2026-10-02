//go:build linux

package web

import (
	"time"

	"golang.org/x/sys/unix"
)

func syscallTimeval(t time.Time) unix.Timeval {
	return unix.NsecToTimeval(t.UnixNano())
}

func settimeofday(tv *unix.Timeval) error {
	return unix.Settimeofday(tv)
}
