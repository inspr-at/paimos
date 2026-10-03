// SPDX-License-Identifier: AGPL-3.0-only
//go:build linux

package agentd

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/inspr-at/paimos/internal/attachwatch"
	"golang.org/x/sys/unix"
)

func attachPeerPID(fd int) (int, error) {
	u, err := unix.GetsockoptUcred(fd, unix.SOL_SOCKET, unix.SO_PEERCRED)
	if err != nil {
		return 0, err
	}
	return int(u.Pid), nil
}
func linuxAttachStat(raw []byte) (start string, parent, session int, tty bool, err error) {
	// comm may itself contain spaces and parentheses; fields after its LAST ')' are fixed.
	end := strings.LastIndexByte(string(raw), ')')
	if end < 0 {
		return "", 0, 0, false, errors.New("invalid process stat")
	}
	fields := strings.Fields(string(raw[end+1:]))
	if len(fields) < 20 {
		return "", 0, 0, false, errors.New("process unavailable")
	}
	if fields[0] == "Z" || fields[0] == "X" {
		return "", 0, 0, false, errAttachExited
	}
	parent, err = strconv.Atoi(fields[1])
	if err != nil {
		return
	}
	session, err = strconv.Atoi(fields[3])
	if err != nil || session < 1 {
		return "", 0, 0, false, errors.New("invalid process session")
	}
	_, err = strconv.ParseUint(fields[19], 10, 64)
	return fields[19], parent, session, fields[4] != "0", err
}

// Metadata only: /proc/<pid>/stat and the directory uid. exe and cwd are not
// readable for another user's process, including root-owned sshd, su and sudo.
// The /proc uid is the effective uid. Non-dumpable processes show as 0.
func observeAttachProcessIdentity(pid int) (attachObservation, error) {
	fail := errors.New("kernel process identity unavailable")
	root := fmt.Sprintf("/proc/%d", pid)
	raw, err := os.ReadFile(root + "/stat")
	if os.IsNotExist(err) && errors.Is(unix.Kill(pid, 0), unix.ESRCH) {
		return attachObservation{}, errAttachExited
	}
	if err != nil {
		return attachObservation{}, fail
	}
	start, parent, session, tty, err := linuxAttachStat(raw)
	if err != nil {
		return attachObservation{}, err
	}
	var st unix.Stat_t
	if unix.Stat(root, &st) != nil {
		return attachObservation{}, fail
	}
	after, err := os.ReadFile(root + "/stat")
	if os.IsNotExist(err) && errors.Is(unix.Kill(pid, 0), unix.ESRCH) {
		return attachObservation{}, errAttachExited
	}
	if err != nil {
		return attachObservation{}, fail
	}
	again, lastParent, lastSession, lastTTY, statErr := linuxAttachStat(after)
	if statErr != nil {
		return attachObservation{}, statErr
	}
	var againStat unix.Stat_t
	if again != start || parent != lastParent || session != lastSession || tty != lastTTY || unix.Stat(root, &againStat) != nil || againStat.Uid != st.Uid {
		return attachObservation{}, fail
	}
	return attachObservation{Process: attachwatch.Process{PID: pid, UID: int(st.Uid), Started: start}, Parent: parent, Session: session, TTY: tty}, nil
}

func observeAttachProcess(pid int) (attachObservation, error) {
	fail := errors.New("kernel process identity unavailable")
	first, err := observeAttachProcessIdentity(pid)
	if err != nil {
		return attachObservation{}, err
	}
	root := fmt.Sprintf("/proc/%d", pid)
	exe, err := os.Readlink(root + "/exe")
	if err != nil {
		return attachObservation{}, fail
	}
	cwd, err := os.Readlink(root + "/cwd")
	if err != nil || strings.HasSuffix(exe, " (deleted)") || strings.HasSuffix(cwd, " (deleted)") {
		return attachObservation{}, fail
	}
	last, err := observeAttachProcessIdentity(pid)
	if err != nil {
		return attachObservation{}, err
	}
	if last != first {
		return attachObservation{}, fail
	}
	first.Executable, first.CWD = exe, cwd
	return first, nil
}
