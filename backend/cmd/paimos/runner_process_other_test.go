//go:build paimos_test_unsupported || (!aix && !darwin && !dragonfly && !freebsd && !linux && !netbsd && !openbsd && !solaris)

package main

import (
	"context"
	"os/exec"
	"strings"
	"testing"
)

func TestUnsupportedPlatformProcessOwnershipFailsClosed(t *testing.T) {
	command := exec.Command("not-started")
	if configureProcessGroup(command) {
		t.Fatal("unsupported platform advertised owned process-group support")
	}
	if err := verifyOwnedProcess(command, false); err == nil ||
		!strings.Contains(err.Error(), "owned process groups are unsupported") {
		t.Fatalf("unsupported ownership verification error=%v", err)
	}

	control := newRunControlArbiterWithAdapter(nil, 7, "device-unsupported", nil, oneShotRunnerControlAdapter{})
	request := supervisorFixture("printf ok")
	request.OwnedProcessStarted = control.start
	result := superviseAgentProcess(context.Background(), request)
	if result.Outcome != outcomeNormalExit {
		t.Fatalf("unsupported-platform supervisor result=%+v", result)
	}
	control.mu.Lock()
	defer control.mu.Unlock()
	if control.done != nil || control.cancel != nil || control.lease.LeaseID != "" {
		t.Fatalf("unsupported platform started a control lease: lease=%q done=%t cancel=%t",
			control.lease.LeaseID, control.done != nil, control.cancel != nil)
	}
}
