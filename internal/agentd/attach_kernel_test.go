// SPDX-License-Identifier: AGPL-3.0-only
//go:build darwin || linux

package agentd

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"testing"
)

// Observe only the test process and its socket. No vendor harness or signals.
func TestAttachKernelSelfAndSocketPeer(t *testing.T) {
	p, err := observeAttachProcess(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	cwd, err = filepath.EvalSymlinks(cwd)
	if err != nil {
		t.Fatal(err)
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	exe, err = filepath.EvalSymlinks(exe)
	if err != nil {
		t.Fatal(err)
	}
	if p.PID != os.Getpid() || p.UID != os.Getuid() || p.CWD != cwd || p.Executable != exe || p.Started == "" || p.Session < 1 {
		t.Fatal("kernel observation does not match test process")
	}
	root, err := os.MkdirTemp("/tmp", "attach-peer-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	l, err := net.Listen("unix", filepath.Join(root, "s"))
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	c, err := net.Dial("unix", filepath.Join(root, "s"))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	server, err := l.Accept()
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	ctx := attachConnContext(context.Background(), server)
	observed, ok := ctx.Value(attachPeerKey{}).(attachObservation)
	if !ok || observed.Process != p.Process {
		t.Fatal("socket peer identity unavailable or forged")
	}
}
