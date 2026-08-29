//go:build (aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris) && !paimos_test_unsupported

// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package ownedprocess

import (
	"os/exec"
	"testing"
	"time"
)

func TestConfigureVerifyAndSignalExactSpawnedGroup(t *testing.T) {
	cmd := exec.Command("sh", "-c", "trap 'exit 0' TERM; while :; do sleep 1; done")
	configured := Configure(cmd)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	if err := Verify(cmd, configured); err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		t.Fatal(err)
	}
	if err := Signal(cmd, false); err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		_ = Signal(cmd, true)
		<-done
		t.Fatal("owned process group ignored termination")
	}
}
