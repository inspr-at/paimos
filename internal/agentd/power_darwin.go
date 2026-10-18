//go:build darwin && cgo

// SPDX-License-Identifier: AGPL-3.0-only
package agentd

/*
#cgo LDFLAGS: -framework IOKit -framework CoreFoundation
#include <IOKit/pwr_mgt/IOPMLib.h>

static IOReturn aeon_prevent_idle_sleep(IOPMAssertionID *id) {
	return IOPMAssertionCreateWithName(kIOPMAssertionTypePreventUserIdleSystemSleep,
		kIOPMAssertionLevelOn, CFSTR("PAIMOS active agent run"), id);
}
*/
import "C"

import (
	"errors"
	"log/slog"
)

func (systemPower) PreventIdleSleep() (func(), error) {
	var id C.IOPMAssertionID
	if C.aeon_prevent_idle_sleep(&id) != C.kIOReturnSuccess {
		return nil, errors.New("macOS idle-sleep assertion unavailable")
	}
	// IOKit owns the assertion for this process and releases it on process
	// death. Normal completion releases it explicitly, without an idle timer.
	return func() {
		if C.IOPMAssertionRelease(id) != C.kIOReturnSuccess {
			slog.Warn("macOS idle-sleep assertion release failed")
		}
	}, nil
}
