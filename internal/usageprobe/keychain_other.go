// SPDX-License-Identifier: AGPL-3.0-only
//go:build !darwin || !cgo

package usageprobe

func claudeKeychain(string) ([]byte, error) { return nil, errLogin }
