//go:build paimos_test_unsupported || (!darwin && !dragonfly && !freebsd && !linux && !netbsd && !openbsd)

// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package agentd

import "errors"

type InstanceLock struct{}

func AcquireInstanceLock(string, string) (*InstanceLock, error) {
	return nil, errors.New("agentd instance locking is unsupported")
}
func (*InstanceLock) Close() error { return nil }
