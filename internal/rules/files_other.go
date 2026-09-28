//go:build !linux && !darwin

// SPDX-License-Identifier: AGPL-3.0-only
package rules

import "errors"

func ReadFile(string, int) ([]byte, error) {
	return nil, errors.New("safe rule files require Linux or Darwin")
}
func WriteFile(string, []byte, bool) error {
	return errors.New("safe rule files require Linux or Darwin")
}
