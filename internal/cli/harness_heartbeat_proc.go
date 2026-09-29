// SPDX-License-Identifier: AGPL-3.0-only

package cli

import (
	"bytes"
	"strconv"
	"strings"
)

// ownerStamp is the operating system's start identity for the watched process.
// A PID alone is not identity: the kernel reuses it, and an unreaped zombie
// still answers a zero signal.
type ownerStamp struct {
	PID   int
	Start string
}

func ownerAlive(pid int, start string) bool {
	if pid <= 0 || start == "" {
		return false
	}
	stamp, err := readOwnerStamp(pid)
	if err != nil {
		return false
	}
	return stamp.PID == pid && stamp.Start == start
}

// parseProcStat reads Linux /proc/<pid>/stat. field 3 is the state and field 22
// is the start time in clock ticks since boot. The command name is inside the
// last pair of parentheses, so the rest is parsed after that.
func parseProcStat(raw []byte) (state string, start uint64, err error) {
	end := bytes.LastIndexByte(raw, ')')
	if end < 0 || end+2 >= len(raw) {
		return "", 0, errOwnerGone
	}
	fields := strings.Fields(string(raw[end+2:]))
	if len(fields) < 20 || len(fields[0]) != 1 {
		return "", 0, errOwnerGone
	}
	start, convErr := strconv.ParseUint(fields[19], 10, 64)
	if convErr != nil {
		return "", 0, errOwnerGone
	}
	return fields[0], start, nil
}
