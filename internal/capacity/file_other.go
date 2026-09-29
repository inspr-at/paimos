// SPDX-License-Identifier: AGPL-3.0-only
//go:build !linux && !darwin

package capacity

import "os"

func openReadingFile(string) (*os.File, error) { return nil, os.ErrPermission }
