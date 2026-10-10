// SPDX-License-Identifier: AGPL-3.0-only
//go:build darwin

package hooknote

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"path/filepath"
	"strings"
	"syscall"
	"unsafe"

	"golang.org/x/sys/unix"
)

// snapshot reads LOCAL_PEERPID, LOCAL_PEERCRED and the audit token, then the
// loaded image. The audit token's pid version changes on exec and on pid reuse.
func snapshot(c net.Conn, requireProjectCWD bool) (Process, error) {
	var pid, uid int
	var pidVersion uint32
	err := withFD(c, func(fd int) error {
		if err := setCloexec(fd); err != nil {
			return err
		}
		peer, err := unix.GetsockoptInt(fd, unix.SOL_LOCAL, unix.LOCAL_PEERPID)
		if err != nil || peer < 1 {
			return ErrPeer
		}
		cred, err := unix.GetsockoptXucred(fd, unix.SOL_LOCAL, unix.LOCAL_PEERCRED)
		if err != nil {
			return ErrPeer
		}
		auditPID, auditUID, version, err := peerAudit(fd)
		if err != nil || auditPID != peer || int(cred.Uid) != auditUID {
			return ErrPeer
		}
		pid, uid, pidVersion = peer, auditUID, version
		return nil
	})
	if err != nil {
		return Process{}, ErrPeer
	}
	proc, err := observe(pid, requireProjectCWD)
	if err != nil || proc.PID != pid || proc.UID != uid || proc.UID != kernelSelfUID() {
		return Process{}, ErrPeer
	}
	proc.PIDVersion = pidVersion
	if pidVersion == 0 {
		return Process{}, ErrPeer
	}
	return proc, nil
}

func peerAudit(fd int) (pid, uid int, pidVersion uint32, err error) {
	var raw [8]uint32
	n := uint32(32)
	_, _, errno := unix.Syscall6(unix.SYS_GETSOCKOPT, uintptr(fd), uintptr(unix.SOL_LOCAL), uintptr(unix.LOCAL_PEERTOKEN), uintptr(unsafe.Pointer(&raw[0])), uintptr(unsafe.Pointer(&n)), 0)
	if errno != 0 || n < 32 {
		return 0, 0, 0, ErrPeer
	}
	// audit_token_t.val: euid at [1], pid at [5], pid version at [7].
	return int(raw[5]), int(raw[1]), raw[7], nil
}

func observe(pid int, requireProjectCWD bool) (Process, error) {
	fail := ErrPeer
	if pid < 1 {
		return Process{}, fail
	}
	first, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if err != nil || first.Proc.P_pid != int32(pid) || first.Proc.P_stat == 5 {
		return Process{}, fail
	}
	exe := make([]byte, 4096)
	n, err := procInfo(pid, 11, 0, exe)
	if err != nil || n < 0 || n > len(exe) {
		return Process{}, fail
	}
	paths := make([]byte, 2*(152+1024))
	n, err = procInfo(pid, 9, 0, paths)
	if err != nil || n != len(paths) {
		return Process{}, fail
	}
	end := bytes.IndexByte(exe, 0)
	if end < 1 {
		return Process{}, fail
	}
	rawExec := string(exe[:end])
	executable, err := filepath.EvalSymlinks(rawExec)
	if err != nil || len(executable) < 2 || executable[0] != '/' {
		return Process{}, fail
	}
	cwdBytes := paths[152 : 152+1024]
	end = bytes.IndexByte(cwdBytes, 0)
	if end < 1 {
		return Process{}, fail
	}
	cwd, err := filepath.EvalSymlinks(string(cwdBytes[:end]))
	if err != nil || !filepath.IsAbs(cwd) || (requireProjectCWD && cwd == "/") {
		return Process{}, fail
	}
	// The mapped vnode, not a stat of the pathname. Replacing the directory
	// entry while this image keeps running must not change dev/ino.
	dev, ino, err := mappedImage(pid, rawExec, executable)
	if err != nil {
		return Process{}, fail
	}
	version, err := darwinPIDVersion(pid)
	if err != nil {
		return Process{}, fail
	}
	last, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if err != nil || last.Proc.P_pid != int32(pid) || last.Proc.P_starttime != first.Proc.P_starttime || last.Eproc.Ucred.Uid != first.Eproc.Ucred.Uid || last.Eproc.Ppid != first.Eproc.Ppid {
		return Process{}, fail
	}
	dev2, ino2, err := mappedImage(pid, rawExec, executable)
	if err != nil || dev2 != dev || ino2 != ino {
		return Process{}, fail
	}
	version2, err := darwinPIDVersion(pid)
	if err != nil || version2 != version {
		return Process{}, fail
	}
	started := fmt.Sprintf("%d:%d", first.Proc.P_starttime.Sec, first.Proc.P_starttime.Usec)
	return Process{PID: pid, UID: int(first.Eproc.Ucred.Uid), Parent: int(first.Eproc.Ppid), Started: started, Executable: executable, Dev: dev, Ino: ino, CWD: cwd, PIDVersion: version}, nil
}

// PROC_PIDUNIQIDENTIFIERINFO is flavor 17. p_idversion sits at byte 32 of the
// 56-byte proc_uniqidentifierinfo and matches audit_token_t.val[7]. A short
// read or a zero version fails closed. The public SDK omits this flavor.
func darwinPIDVersion(pid int) (uint32, error) {
	buf := make([]byte, 56)
	n, err := procInfo(pid, 17, 0, buf)
	if err != nil || n < 56 {
		return 0, ErrPeer
	}
	version := binary.LittleEndian.Uint32(buf[32:36])
	if version == 0 {
		return 0, ErrPeer
	}
	return version, nil
}

// proc_regionwithpathinfo is 96 bytes of proc_regioninfo plus a 152-byte
// vnode_info and a MAXPATHLEN path. PROC_PIDREGIONPATHINFO is flavor 8.
// VM_PROT_EXECUTE is 0x4. Sizes match the macOS 26/27 SDK; a short result
// fails closed instead of falling back to a pathname stat.
const (
	regionPathInfoSize = 96 + 152 + 1024
	vmProtExecute      = 0x4
)

func mappedImage(pid int, rawPath, evaluated string) (uint64, uint64, error) {
	buf := make([]byte, regionPathInfoSize)
	var addr uint64
	for range 8192 {
		n, err := procInfo(pid, 8, addr, buf)
		if err != nil || n < regionPathInfoSize {
			break
		}
		prot := binary.LittleEndian.Uint32(buf[0:4])
		regionAddr := binary.LittleEndian.Uint64(buf[80:88])
		regionSize := binary.LittleEndian.Uint64(buf[88:96])
		if regionSize == 0 || regionAddr+regionSize <= regionAddr {
			break
		}
		dev := uint64(binary.LittleEndian.Uint32(buf[96:100]))
		ino := binary.LittleEndian.Uint64(buf[104:112])
		path := cString(buf[248:1272])
		if prot&vmProtExecute != 0 && (dev != 0 || ino != 0) && imagePath(path, rawPath, evaluated) {
			return dev, ino, nil
		}
		next := regionAddr + regionSize
		if next <= addr {
			break
		}
		addr = next
	}
	return 0, 0, errors.New("mapped image unavailable")
}

func imagePath(got, raw, evaluated string) bool {
	got = strings.TrimSuffix(got, " (deleted)")
	if sameImagePath(got, raw) || sameImagePath(got, evaluated) {
		return true
	}
	// Directory symlinks (/tmp vs /private/tmp) keep the final component.
	// The inode returned to the caller is still the mapped vnode, not this stat.
	if filepath.Base(got) != filepath.Base(raw) && filepath.Base(got) != filepath.Base(evaluated) {
		return false
	}
	resolved, err := filepath.EvalSymlinks(got)
	if err != nil {
		return false
	}
	return sameImagePath(resolved, raw) || sameImagePath(resolved, evaluated)
}

func sameImagePath(got, want string) bool {
	got = strings.TrimSuffix(got, " (deleted)")
	want = strings.TrimSuffix(want, " (deleted)")
	return got != "" && got == want
}

func cString(b []byte) string {
	if i := bytes.IndexByte(b, 0); i >= 0 {
		b = b[:i]
	}
	return string(b)
}

func procInfo(pid, flavor int, arg uint64, buf []byte) (int, error) {
	if len(buf) == 0 {
		return 0, errors.New("empty proc info")
	}
	n, _, errno := syscall.Syscall6(unix.SYS_PROC_INFO, 2, uintptr(pid), uintptr(flavor), uintptr(arg), uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)))
	if errno != 0 {
		return 0, errno
	}
	return int(n), nil
}
