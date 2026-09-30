// SPDX-License-Identifier: AGPL-3.0-only
//go:build linux

package agentd

import (
	"errors"
	"os"
	"os/exec"
	"strings"
	"testing"
)

func TestAttachLinuxStatSessionIdentity(t *testing.T) {
	fields := []string{"S", "20", "30", "20", "34816", "30", "0", "0", "0", "0", "0", "0", "0", "0", "0", "0", "0", "1", "0", "12345"}
	raw := func() []byte { return []byte("30 (helper (fixture)) " + strings.Join(fields, " ")) }
	start, parent, session, tty, err := linuxAttachStat(raw())
	if err != nil || start != "12345" || parent != 20 || session != 20 || !tty {
		t.Fatal("kernel stat lost parent, session or start identity")
	}
	for _, bad := range []string{"0", "-1", "unknown"} {
		fields[3] = bad
		if _, _, _, _, err := linuxAttachStat(raw()); err == nil {
			t.Fatal("invalid session accepted")
		}
	}
	fields[3], fields[0] = "20", "Z"
	if _, _, _, _, err := linuxAttachStat(raw()); err == nil {
		t.Fatal("dead session leader accepted")
	}
}

func TestAttachLinuxIdentitySkipsAncestorPaths(t *testing.T) {
	self, err := observeAttachProcessIdentity(os.Getpid())
	if err != nil || self.PID != os.Getpid() || self.UID != os.Getuid() || self.Started == "" || self.Session < 1 || self.Executable != "" || self.CWD != "" {
		t.Fatal("kernel identity unavailable or includes paths", err)
	}
	full, err := observeAttachProcess(os.Getpid())
	if err != nil || full.Executable == "" || full.CWD == "" || full.Started != self.Started || full.UID != self.UID {
		t.Fatal("target observation lost executable or cwd", err)
	}
	root, err := observeAttachProcessIdentity(1)
	if err != nil || root.PID != 1 || root.Started == "" || root.Executable != "" || root.CWD != "" {
		t.Fatal("root ancestor metadata unavailable or includes paths", err)
	}
	if root.UID != 0 {
		t.Skip("PID 1 is not uid 0; rootless or user-namespace sandbox")
	}
}

func TestAttachLinuxExitedProcessIsKernelConfirmed(t *testing.T) {
	cmd := exec.Command("/bin/true")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	pid := cmd.Process.Pid
	if err := cmd.Wait(); err != nil {
		t.Fatal(err)
	}
	if _, err := observeAttachProcess(pid); !errors.Is(err, errAttachExited) {
		t.Fatalf("exited process not distinguished: %v", err)
	}
}
