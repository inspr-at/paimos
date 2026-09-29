//go:build linux

// SPDX-License-Identifier: AGPL-3.0-only

package cli

import (
	"os"
	"strconv"
	"strings"
)

func readOwnerStamp(pid int) (ownerStamp, error) {
	if pid <= 0 {
		return ownerStamp{}, errOwnerGone
	}
	raw, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return ownerStamp{}, errOwnerGone
	}
	state, start, err := parseProcStat(raw)
	if err != nil || state == "Z" || state == "X" {
		return ownerStamp{}, errOwnerGone
	}
	boot, err := os.ReadFile("/proc/sys/kernel/random/boot_id")
	if err != nil {
		return ownerStamp{}, errOwnerGone
	}
	id := strings.TrimSpace(string(boot))
	if id == "" || len(id) > 64 || strings.ContainsAny(id, " \t\r\n") {
		return ownerStamp{}, errOwnerGone
	}
	return ownerStamp{PID: pid, Start: id + ":" + strconv.FormatUint(start, 10)}, nil
}
