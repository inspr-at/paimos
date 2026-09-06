//go:build (!darwin && !linux) || paimos_test_unsupported

// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>
package lifecycleclient

func ReadPrivate(string, int64) ([]byte, error) { return nil, ErrOwnership }
func FileCredential(string) func() (string, error) {
	return func() (string, error) { return "", ErrOwnership }
}
