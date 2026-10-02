// SPDX-License-Identifier: AGPL-3.0-only
//go:build (darwin || linux) && !aeon_test_unsupported

// Package processtest provides a barrier-driven inherited-pipe fixture. It
// never launches vendor software or signals a process using a stored PID.
package processtest

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

type Fixture struct {
	Script      string
	root, child *os.File
	ready       chan struct{}
	exited      chan struct{}
	exitErr     error
}

func New(t *testing.T) *Fixture {
	t.Helper()
	dir := t.TempDir()
	paths := make([]string, 3)
	for i, name := range []string{"root", "child", "alive"} {
		paths[i] = filepath.Join(dir, name)
		if err := unix.Mkfifo(paths[i], 0600); err != nil {
			t.Fatal(err)
		}
	}
	open := func(path string) *os.File {
		f, err := os.OpenFile(path, os.O_RDWR, 0600)
		if err != nil {
			t.Fatal(err)
		}
		return f
	}
	f := &Fixture{root: open(paths[0]), child: open(paths[1]), ready: make(chan struct{}), exited: make(chan struct{})}
	// The descendant holds stdout/stderr and a separate witness descriptor.
	// Closing exec's own copy pipes cannot falsely satisfy the witness EOF.
	f.Script = fmt.Sprintf("( exec 3>%q; IFS= read -r finish <%q ) & IFS= read -r finish <%q", paths[2], paths[1], paths[0])
	go func() {
		defer close(f.exited)
		alive, err := os.Open(paths[2])
		close(f.ready)
		if err != nil {
			f.exitErr = err
			return
		}
		defer alive.Close()
		_, err = io.Copy(io.Discard, alive)
		f.exitErr = err
	}()
	t.Cleanup(func() {
		// Release the witness opener even if launching the fixture failed.
		if fd, err := unix.Open(paths[2], unix.O_WRONLY|unix.O_NONBLOCK, 0600); err == nil {
			_ = unix.Close(fd)
		}
		_, _ = io.WriteString(f.child, "release\n")
		_, _ = io.WriteString(f.root, "release\n")
		_ = f.child.Close()
		_ = f.root.Close()
		select {
		case <-f.exited:
		case <-time.After(3 * time.Second):
			t.Error("fixture witness cleanup failed")
		}
	})
	return f
}

func (f *Fixture) Ready(t *testing.T) {
	t.Helper()
	select {
	case <-f.ready:
	case <-time.After(5 * time.Second):
		t.Fatal("fixture descendant never opened witness pipe")
	}
}

func (f *Fixture) ExitRoot() { _, _ = io.WriteString(f.root, "exit\n") }

func (f *Fixture) AssertExited(t *testing.T) {
	t.Helper()
	select {
	case <-f.exited:
		if f.exitErr != nil {
			t.Fatal(f.exitErr)
		}
	case <-time.After(3 * time.Second):
		t.Error("owned descendant still holds witness descriptor")
	}
}
