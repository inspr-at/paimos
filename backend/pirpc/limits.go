// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package pirpc

import "time"

const (
	// MaxFrameBytes bounds one LF-delimited JSONL record from the child.
	MaxFrameBytes = 8 << 20

	DefaultCommandTimeout = 20 * time.Second
	DefaultExitGrace      = 250 * time.Millisecond
)
