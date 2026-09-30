// SPDX-License-Identifier: AGPL-3.0-only
//go:build darwin

package agentd

import (
	"bytes"
	"errors"
	"fmt"
	"path/filepath"
	"syscall"
	"unsafe"

	"github.com/inspr-at/paimos/internal/attachwatch"
	"golang.org/x/sys/unix"
)

func attachPeerPID(fd int) (int, error) {
	return unix.GetsockoptInt(fd, unix.SOL_LOCAL, unix.LOCAL_PEERPID)
}

// proc_info is the kernel interface behind libproc's proc_pidinfo/path APIs.
// These fixed 64-bit ABI sizes and offsets come from sys/proc_info.h:
// vnode_info is 152 bytes, followed by a MAXPATHLEN (1024) path, twice.
func attachProcInfo(pid, flavor int, buf []byte) (int, error) {
	n, _, errno := syscall.Syscall6(unix.SYS_PROC_INFO, 2, uintptr(pid), uintptr(flavor), 0, uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)))
	if errno != 0 {
		return 0, errno
	}
	return int(n), nil
}

// sysctl and getsid are readable across UIDs, unlike libproc's cwd/path APIs.
// Keep this observation separate so a root-owned terminal leader is usable.
func observeAttachProcessIdentity(pid int) (attachObservation, error) {
	fail := errors.New("kernel process identity unavailable")
	first, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if err := checkAttachDarwinProcess(pid, first, err, func() error { return unix.Kill(pid, 0) }); err != nil {
		return attachObservation{}, err
	}
	session, err := unix.Getsid(pid)
	if err != nil || session < 1 {
		return attachObservation{}, fail
	}
	last, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if err := checkAttachDarwinProcess(pid, last, err, func() error { return unix.Kill(pid, 0) }); err != nil {
		return attachObservation{}, err
	}
	lastSession, sessionErr := unix.Getsid(pid)
	if sessionErr != nil || session != lastSession || last.Proc.P_starttime != first.Proc.P_starttime || last.Eproc.Ucred.Uid != first.Eproc.Ucred.Uid || last.Eproc.Ppid != first.Eproc.Ppid || last.Eproc.Tdev != first.Eproc.Tdev {
		return attachObservation{}, fail
	}
	return attachObservation{Process: attachwatch.Process{PID: pid, UID: int(first.Eproc.Ucred.Uid), Started: fmt.Sprintf("%d:%d", first.Proc.P_starttime.Sec, first.Proc.P_starttime.Usec)}, Parent: int(first.Eproc.Ppid), Session: session, TTY: first.Eproc.Tdev != -1}, nil
}

func observeAttachProcess(pid int) (attachObservation, error) {
	fail := errors.New("kernel process identity unavailable")
	first, err := observeAttachProcessIdentity(pid)
	if err != nil {
		return attachObservation{}, err
	}
	exe := make([]byte, 4096)
	n, err := attachProcInfo(pid, 11, exe)
	// libproc proc_pidpath treats any non-error as success, then uses strlen.
	// https://github.com/apple-oss-distributions/xnu/blob/main/libsyscall/wrappers/libproc/libproc.c
	if err != nil || n < 0 || n > len(exe) {
		return attachObservation{}, fmt.Errorf("%w: path %d %v", fail, n, err)
	}
	paths := make([]byte, 2*(152+1024))
	n, err = attachProcInfo(pid, 9, paths)
	if err != nil || n != len(paths) {
		return attachObservation{}, fmt.Errorf("%w: cwd %d %v", fail, n, err)
	}
	end := bytes.IndexByte(exe, 0)
	if end < 1 {
		return attachObservation{}, fail
	}
	executable := string(exe[:end])
	cwdBytes := paths[152 : 152+1024]
	end = bytes.IndexByte(cwdBytes, 0)
	if end < 1 {
		return attachObservation{}, fail
	}
	cwd := string(cwdBytes[:end])
	physical, err := filepath.EvalSymlinks(cwd)
	if err != nil || physical != cwd {
		return attachObservation{}, fail
	}
	last, err := observeAttachProcessIdentity(pid)
	if err != nil || last != first {
		return attachObservation{}, fail
	}
	first.Executable, first.CWD = executable, cwd
	return first, nil
}

func checkAttachDarwinProcess(pid int, first *unix.KinfoProc, err error, exists func() error) error {
	// An empty/mismatched result (including SysctlKinfoProc's EIO) is not
	// proof of exit. Confirm absence with signal 0, an existence check only.
	if err != nil || first == nil || first.Proc.P_pid != int32(pid) {
		if errors.Is(exists(), syscall.ESRCH) {
			return errAttachExited
		}
		return fmt.Errorf("kernel process identity unavailable: sysctl PID mismatch or error: %v", err)
	}
	if first.Proc.P_stat == 5 {
		return errAttachExited
	}
	return nil
}
