//go:build darwin

// SPDX-License-Identifier: AGPL-3.0-only

package cli

import (
	"encoding/binary"
	"fmt"
	"unsafe"

	"golang.org/x/sys/unix"
)

const (
	// procInfoPID is PROC_INFO_CALL_PIDINFO, the libproc call behind proc_pidinfo.
	procInfoPID = 2
	// procPIDTbsdinfo is PROC_PIDTBSDINFO. The buffer matches struct proc_bsdinfo,
	// which is packed to 4-byte alignment: start time begins at offset 120.
	procPIDTbsdinfo  = 3
	procBsdInfoSize  = 136
	procStatusZombie = 5
)

func readOwnerStamp(pid int) (ownerStamp, error) {
	if pid <= 0 {
		return ownerStamp{}, errOwnerGone
	}
	var buf [procBsdInfoSize]byte
	r1, _, errno := unix.Syscall6(
		unix.SYS_PROC_INFO,
		procInfoPID,
		uintptr(pid),
		procPIDTbsdinfo,
		0,
		uintptr(unsafe.Pointer(&buf[0])),
		uintptr(len(buf)),
	)
	if errno != 0 || r1 != uintptr(len(buf)) {
		return ownerStamp{}, errOwnerGone
	}
	got := binary.LittleEndian.Uint32(buf[12:16])
	status := binary.LittleEndian.Uint32(buf[4:8])
	if int(got) != pid || status == 0 || status == procStatusZombie {
		return ownerStamp{}, errOwnerGone
	}
	sec := binary.LittleEndian.Uint64(buf[120:128])
	usec := binary.LittleEndian.Uint64(buf[128:136])
	if sec == 0 && usec == 0 {
		return ownerStamp{}, errOwnerGone
	}
	return ownerStamp{PID: pid, Start: fmt.Sprintf("%d.%d", sec, usec)}, nil
}
