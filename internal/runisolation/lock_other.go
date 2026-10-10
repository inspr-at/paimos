// SPDX-License-Identifier: AGPL-3.0-only
//go:build !darwin && !linux

package runisolation

import (
	"context"
	"errors"
	"os"
)

func lockFile(*os.Root, string, bool) (*os.File, error) {
	return nil, errors.New("run isolation requires Darwin or Linux ownership support")
}
func openRecord(*os.Root) (*os.File, error) {
	return nil, errors.New("run isolation ownership unsupported")
}
func waitLock(context.Context, *os.Root, string, bool) (*os.File, error) {
	return lockFile(nil, "", false)
}
