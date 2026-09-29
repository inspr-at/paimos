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

// readOwnerStamp identifies the process by its start time. proc_pidinfo and
// kern.proc.pid must agree when both succeed. A larger proc_bsdinfo still
// carries this 136-byte prefix. A zombie or a disagreement is not a live owner.
func readOwnerStamp(pid int) (ownerStamp, error) {
	if pid <= 0 {
		return ownerStamp{}, errOwnerGone
	}
	procStart, procDead, procOK := readProcBSDStart(pid)
	kinfoStart, kinfoDead, kinfoOK := readKinfoStart(pid)
	if procDead || kinfoDead || (procOK && kinfoOK && procStart != kinfoStart) {
		return ownerStamp{}, errOwnerGone
	}
	start := procStart
	if !procOK {
		start = kinfoStart
	}
	if start == "" {
		return ownerStamp{}, errOwnerGone
	}
	return ownerStamp{PID: pid, Start: start}, nil
}

func readProcBSDStart(pid int) (start string, zombie, ok bool) {
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
	if errno != 0 || r1 < uintptr(procBsdInfoSize) {
		return "", false, false
	}
	got := binary.LittleEndian.Uint32(buf[12:16])
	status := binary.LittleEndian.Uint32(buf[4:8])
	if status == procStatusZombie {
		return "", true, false
	}
	if int(got) != pid || status == 0 {
		return "", false, false
	}
	sec := binary.LittleEndian.Uint64(buf[120:128])
	usec := binary.LittleEndian.Uint64(buf[128:136])
	if sec == 0 && usec == 0 {
		return "", false, false
	}
	return fmt.Sprintf("%d.%d", sec, usec), false, true
}

func readKinfoStart(pid int) (start string, zombie, ok bool) {
	kp, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if err != nil || kp == nil || int(kp.Proc.P_pid) != pid {
		return "", false, false
	}
	if kp.Proc.P_stat == procStatusZombie {
		return "", true, false
	}
	if kp.Proc.P_stat <= 0 {
		return "", false, false
	}
	sec := kp.Proc.P_starttime.Sec
	usec := kp.Proc.P_starttime.Usec
	if sec == 0 && usec == 0 {
		return "", false, false
	}
	return fmt.Sprintf("%d.%d", sec, usec), false, true
}
