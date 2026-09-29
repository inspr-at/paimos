//go:build darwin && cgo

// SPDX-License-Identifier: AGPL-3.0-only
package agentd

/*
#cgo CFLAGS: -x objective-c -fobjc-arc
#cgo LDFLAGS: -framework Foundation -framework LocalAuthentication -framework Security
#include <stdlib.h>
void *aeon_local_auth_start(const char *reason, int *failure);
int aeon_local_auth_result(void *handle);
void aeon_local_auth_close(void *handle);
*/
import "C"

import (
	"context"
	"errors"
	"time"
	"unsafe"
)

func (systemLocalAuthenticator) Confirm(ctx context.Context, reason string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	text := C.CString(reason)
	defer C.free(unsafe.Pointer(text))
	var failure C.int
	handle := C.aeon_local_auth_start(text, &failure)
	if handle == nil {
		switch failure {
		case 1:
			return errors.New("local confirmation unavailable: aeon-agentd must have a valid hardened Developer ID signature without debugging or library-validation exceptions")
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
