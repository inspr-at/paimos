// SPDX-License-Identifier: AGPL-3.0-only
//go:build !linux

package ciproof

import (
	"context"
	"fmt"
)

func runVM(context.Context, VMProfile, VMTask, []byte) ([]byte, error) {
	return nil, fmt.Errorf("external Linux/KVM executor unavailable; retain full CI in shadow mode")
}

func OpenAuthorityRepository(context.Context, string, string, FilePin) (*Repository, error) {
	return nil, fmt.Errorf("installed authority requires an independent immutable Linux host")
}
