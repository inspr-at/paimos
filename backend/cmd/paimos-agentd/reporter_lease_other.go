//go:build !darwin && !linux

// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package main

import "errors"

func newDiskReporterLeaseStore(_, _ string) (reporterLeaseStore, error) {
	return nil, errors.New("protected reporter lease storage is unsupported on this platform")
}
