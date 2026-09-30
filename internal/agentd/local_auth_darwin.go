//go:build darwin && cgo

// SPDX-License-Identifier: AGPL-3.0-only
package agentd

/*
#cgo CFLAGS: -x objective-c -fobjc-arc
#cgo LDFLAGS: -framework Foundation -framework LocalAuthentication -framework Security
#include <stdlib.h>
int aeon_local_auth_capability(void);
void *aeon_local_auth_start(const char *reason, int *failure);
int aeon_local_auth_result(void *handle);
void aeon_local_auth_close(void *handle);
int aeon_installed_daemon_name_allowed(const char *name);
int aeon_running_executable_name_allowed(void);
int aeon_signed_daemon_team(char *team, int size);
*/
import "C"

import (
	"context"
	"errors"
	"time"
	"unsafe"

	"github.com/inspr-at/paimos/internal/agentsecurity"
)

func localAuthInstalledNameAllowed(name string) bool {
	c := C.CString(name)
	defer C.free(unsafe.Pointer(c))
	return C.aeon_installed_daemon_name_allowed(c) == 1
}

func localAuthNilInstalledNameAllowed() bool {
	return C.aeon_installed_daemon_name_allowed(nil) == 1
}

func localAuthRunningExecutableAllowed() bool {
	return C.aeon_running_executable_name_allowed() == 1
}

// localAuthSignedByExpectedTeam reports whether this running executable is the
// installed daemon with a valid hardened Developer ID signature by the team the
// build expects (expectedTeamID).
func localAuthSignedByExpectedTeam() bool {
	var team [32]C.char
	if C.aeon_signed_daemon_team(&team[0], C.int(len(team))) != 1 {
		return false
	}
	return localAuthTeamAllowed(expectedTeamID, C.GoString(&team[0]))
}

func CurrentLocalAuthCapability() string {
	return agentsecurity.LocalAuthCapability()
}

func (systemLocalAuthenticator) Confirm(ctx context.Context, reason string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if !localAuthSignedByExpectedTeam() {
		return errors.New(localAuthUnsignedMessage(expectedTeamID))
	}
	text := C.CString(reason)
	defer C.free(unsafe.Pointer(text))
	var failure C.int
	handle := C.aeon_local_auth_start(text, &failure)
	if handle == nil {
		switch failure {
		case 1:
			return errors.New(localAuthUnsignedMessage(expectedTeamID))
		case 2:
			return errors.New("local confirmation unavailable: no macOS graphical login session")
		default:
			return errors.New("local confirmation unavailable: macOS cannot authenticate the device owner")
		}
	}
	defer C.aeon_local_auth_close(handle)
	tick := time.NewTicker(100 * time.Millisecond)
	defer tick.Stop()
	for {
		switch C.aeon_local_auth_result(handle) {
		case 1:
			return ctx.Err()
		case -1:
			return errors.New("local confirmation cancelled or denied; watch was not activated")
		}
		select {
		case <-ctx.Done():
			return errors.New("local confirmation expired or cancelled; watch was not activated")
		case <-tick.C:
		}
	}
}
