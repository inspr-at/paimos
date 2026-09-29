//go:build aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

// SPDX-License-Identifier: AGPL-3.0-only

package cli

import (
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestOwnerStampMatchesStartAndRejectsReuse(t *testing.T) {
	stamp, err := readOwnerStamp(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	if stamp.Start == "" || stamp.PID != os.Getpid() || !ownerAlive(os.Getpid(), stamp.Start) {
		t.Fatalf("live stamp %#v", stamp)
	}
	if ownerAlive(os.Getpid(), stamp.Start+"9") || ownerAlive(os.Getpid(), "") {
		t.Fatal("a different start identity was treated as the same process")
	}
}

func TestOwnerStampRejectsZombie(t *testing.T) {
	cmd := exec.Command("/bin/sh", "-c", "exit 0")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	pid := cmd.Process.Pid
	deadline := time.Now().Add(2 * time.Second)
	zombie := false
	for time.Now().Before(deadline) {
		killErr := syscall.Kill(pid, 0)
		_, readErr := readOwnerStamp(pid)
		if killErr == nil && readErr != nil {
			zombie = true
			break
		}
		if killErr != nil && readErr != nil {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	_ = cmd.Wait()
	if !zombie {
		t.Fatal("an exited unreaped process was still treated as alive")
	}
}

func TestOpenNoFollowReadsRegularFileAndRejectsSymlinkParent(t *testing.T) {
	dir := t.TempDir()
	real := filepath.Join(dir, "real")
	if err := os.Mkdir(real, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(real, "note.json")
	if err := os.WriteFile(path, []byte("ok"), 0o600); err != nil {
		t.Fatal(err)
	}
	f, err := openNoFollow(path)
	if err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(f)
	_ = f.Close()
	if err != nil || string(got) != "ok" {
		t.Fatalf("read %q %v", got, err)
	}
	if err := os.Symlink(real, filepath.Join(dir, "link")); err != nil {
		t.Fatal(err)
	}
	f, err = openNoFollow(filepath.Join(dir, "link", "note.json"))
	if f != nil {
		_ = f.Close()
	}
	if err == nil {
		t.Fatal("opened through a symlinked parent")
	}
	alias := filepath.Join(real, "alias.json")
	if err := os.Symlink(path, alias); err != nil {
		t.Fatal(err)
	}
	f, err = openNoFollow(alias)
	if f != nil {
		_ = f.Close()
	}
	if err == nil {
		t.Fatal("opened a symlinked file")
	}
}

func TestOpenNoFollowRejectsFIFO(t *testing.T) {
	path := t.TempDir() + "/pipe"
	if err := unix.Mkfifo(path, 0o600); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		f, err := openNoFollow(path)
		if f != nil {
			_ = f.Close()
		}
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("opened a fifo")
		}
	case <-time.After(time.Second):
		t.Fatal("opening a fifo blocked")
	}
}
