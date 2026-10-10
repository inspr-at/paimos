// SPDX-License-Identifier: AGPL-3.0-only
//go:build !linux && !darwin

package doctrine

import "os"

func openMirrorFile(*os.Root, string, ...func(*os.File) bool) (*os.File, error) {
	return nil, gitFail("read-only host mirrors are unsupported on this platform")
}
func openMirrorDirectory(*os.Root, string, ...func(*os.File) bool) (*os.File, error) {
	return nil, gitFail("read-only host mirrors are unsupported on this platform")
}
