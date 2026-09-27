// SPDX-License-Identifier: AGPL-3.0-only
//go:build (!linux && !darwin) || aeon_test_unsupported

package ownedprocess

import "errors"

const waitObservationSupported = false

func observeExit(int) error { return errors.New("non-reaping child observation unsupported") }
