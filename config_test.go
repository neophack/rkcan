//go:build linux

package main

import (
	"flag"
	"testing"
)

func TestApplyEnvDefaults(t *testing.T) {
	fs := flag.NewFlagSet("t", flag.ContinueOnError)
	proto := fs.String("timesync-proto", "5A4", "")
	brs := fs.Bool("brs", false, "")
	can1 := fs.String("can1", "can1", "")

	t.Setenv("RKCAN_TIMESYNC_PROTO", "594")
	t.Setenv("RKCAN_BRS", "true")
	t.Setenv("RKCAN_CAN1", "")
	if err := applyEnvDefaults(fs); err != nil {
		t.Fatal(err)
	}
	if err := fs.Parse([]string{"-brs=false"}); err != nil {
		t.Fatal(err)
	}
	if *proto != "594" || *brs != false || *can1 != "" {
		t.Fatalf("got proto=%q brs=%v can1=%q", *proto, *brs, *can1)
	}

	t.Setenv("RKCAN_BRS", "maybe")
	if err := applyEnvDefaults(fs); err == nil {
		t.Fatal("expected error for invalid bool")
	}
}
