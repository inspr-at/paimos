// SPDX-License-Identifier: AGPL-3.0-only
//go:build linux

package hooknote

import (
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"
)

// snapshot reads SO_PEERCRED and the loaded image. Start time comes from
// /proc/<pid>/stat and changes when the pid is reused; the exe inode changes
// on exec, including exec of a replaced file.
func snapshot(c net.Conn, requireProjectCWD bool) (Process, error) {
	var pid, uid int
	err := withFD(c, func(fd int) error {
		if err := setCloexec(fd); err != nil {
			return err
		}
		cred, err := unix.GetsockoptUcred(fd, unix.SOL_SOCKET, unix.SO_PEERCRED)
		if err != nil || cred == nil || cred.Pid < 1 {
			return ErrPeer
		}
		pid, uid = int(cred.Pid), int(cred.Uid)
		return nil
	})
	if err != nil {
		return Process{}, ErrPeer
	}
	proc, err := observe(pid, requireProjectCWD)
	if err != nil || proc.PID != pid || proc.UID != uid || proc.UID != kernelSelfUID() {
		return Process{}, ErrPeer
	}
	return proc, nil
}

func observe(pid int, requireProjectCWD bool) (Process, error) {
	if pid < 1 {
		return Process{}, ErrPeer
	}
	root := fmt.Sprintf("/proc/%d", pid)
	raw, err := os.ReadFile(root + "/stat")
	if err != nil {
		return Process{}, ErrPeer
	}
	started, parent, err := linuxIdentity(raw)
	if err != nil {
		return Process{}, ErrPeer
	}
	var dir unix.Stat_t
	if unix.Stat(root, &dir) != nil {
		return Process{}, ErrPeer
	}
	exe, err := os.Readlink(root + "/exe")
	if err != nil || strings.HasSuffix(exe, " (deleted)") || len(exe) < 2 || exe[0] != '/' {
		return Process{}, ErrPeer
	}
	cwd, err := os.Readlink(root + "/cwd")
	if err != nil || strings.HasSuffix(cwd, " (deleted)") || !filepath.IsAbs(cwd) || (requireProjectCWD && cwd == "/") {
		return Process{}, ErrPeer
	}
	// /proc/<pid>/exe is the mapped vnode. A pathname replaced on disk keeps
	// this inode until the process execs.
	dev, ino, err := statProcExe(root + "/exe")
	if err != nil {
		return Process{}, ErrPeer
	}
	after, err := os.ReadFile(root + "/stat")
	if err != nil {
		return Process{}, ErrPeer
	}
	started2, parent2, err := linuxIdentity(after)
	dev2, ino2, err2 := statProcExe(root + "/exe")
	if err != nil || err2 != nil || started2 != started || parent2 != parent || dev2 != dev || ino2 != ino {
		return Process{}, ErrPeer
	}
	return Process{PID: pid, UID: int(dir.Uid), Parent: parent, Started: started, Executable: exe, Dev: dev, Ino: ino, CWD: cwd}, nil
}

func statProcExe(path string) (uint64, uint64, error) {
	var st unix.Stat_t
	if err := unix.Stat(path, &st); err != nil {
		return 0, 0, err
	}
	if st.Ino == 0 && st.Dev == 0 {
		return 0, 0, errors.New("empty image identity")
	}
	return uint64(st.Dev), uint64(st.Ino), nil
}

func linuxIdentity(raw []byte) (started string, parent int, err error) {
	end := strings.LastIndexByte(string(raw), ')')
	if end < 0 {
		return "", 0, errors.New("invalid process stat")
	}
	fields := strings.Fields(string(raw[end+1:]))
	if len(fields) < 20 || fields[0] == "Z" {
		return "", 0, errors.New("process unavailable")
	}
	parent, err = strconv.Atoi(fields[1])
	if err != nil || parent < 1 {
		return "", 0, errors.New("invalid parent")
	}
	if _, err = strconv.ParseUint(fields[19], 10, 64); err != nil || fields[19] == "" {
		return "", 0, errors.New("invalid start")
	}
	return fields[19], parent, nil
}
