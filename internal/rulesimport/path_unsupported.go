// SPDX-License-Identifier: AGPL-3.0-only
//go:build (!linux && !darwin) || aeon_test_rulesimport_unsupported

package rulesimport

import "os"

func openNoFollow(string) (*os.File, error)            { return nil, ErrUnsupported }
func openInstructionNoFollow(string) (*os.File, error) { return nil, ErrUnsupported }
