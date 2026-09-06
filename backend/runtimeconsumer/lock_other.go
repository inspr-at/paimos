//go:build paimos_test_unsupported || (!darwin && !dragonfly && !freebsd && !linux && !netbsd && !openbsd)

// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package runtimeconsumer

func acquireLock(string) (func(), error) { return nil, ErrUnsupported }
