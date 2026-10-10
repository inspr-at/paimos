// SPDX-License-Identifier: AGPL-3.0-only
//go:build darwin || linux

package hooknote

import (
	"bytes"
	"context"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

const serverNonce = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
const serverCanary = "CANARY-CREDENTIAL-394"

func ownerNote() Note {
	return Note{ID: "00000000-0000-4000-8000-000000000394", Owner: "Markus", Body: "inspect the gate", Origin: OriginOwner, Created: "2026-09-30T02:00:00Z", Deadline: time.Now().Add(time.Hour).UTC().Format(time.RFC3339)}
}

type fakeSource struct {
	note     Note
	nonce    string
	offerErr error
	offers   []Binding
	settles  []string
}

func (f *fakeSource) Offer(_ context.Context, binding Binding) (Note, string, error) {
	f.offers = append(f.offers, binding)
	if f.offerErr != nil {
		return Note{}, "", f.offerErr
	}
	return f.note, f.nonce, nil
}

func (f *fakeSource) Settle(_ context.Context, nonce, outcome string, _ Epoch) error {
	if !ValidNonce(nonce) {
		return ErrMalformedNonce
	}
	f.settles = append(f.settles, outcome+":"+nonce)
	return nil
}

func shortDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "hn-")
	if err != nil {
		t.Fatal(err)
	}
	dir, err = filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

func serveHook(t *testing.T, src NoteSource, reg *Registry) string {
	t.Helper()
	return serveHookObserve(t, src, reg, nil)
}

func serveHookObserve(t *testing.T, src NoteSource, reg *Registry, observe func(int) (Process, error)) string {
	t.Helper()
	socket := filepath.Join(shortDir(t), "s")
	ln, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	srv := NewServer()
	srv.Set(src, reg)
	srv.observe = observe
	httpSrv := &http.Server{Handler: srv, ReadHeaderTimeout: 5 * time.Second, ConnContext: func(ctx context.Context, c net.Conn) context.Context {
		return Annotate(ctx, c)
	}}
	go func() { _ = httpSrv.Serve(ln) }()
	t.Cleanup(func() { _ = httpSrv.Close() })
	return socket
}

func selfPin(t *testing.T) DaemonPin {
	t.Helper()
	socket := filepath.Join(shortDir(t), "pin")
	ln, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	client, err := net.Dial("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	server, err := ln.Accept()
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	peer, err := Snapshot(server)
	if err != nil {
		t.Fatal(err)
	}
	pin := PinFrom(peer)
	if !pin.Valid() || pin.PID != os.Getpid() || pin.UID != os.Getuid() {
		t.Fatalf("kernel pin pid %d uid %d", pin.PID, pin.UID)
	}
	return pin
}

func TestKernelPeerMatchesProcess(t *testing.T) {
	pin := selfPin(t)
	self, err := Observe(os.Getpid())
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
	if self.PID != os.Getpid() || self.UID != os.Getuid() || self.Executable != exe || self.CWD != cwd || self.Started == "" || self.Ino == 0 {
		t.Fatalf("observe mismatch pid %d uid %d", self.PID, self.UID)
	}
	again, err := Observe(os.Getpid())
	if err != nil || !SameIdentity(self, again) {
		t.Fatal("start identity was not stable")
	}
	reused := self
	reused.Started = self.Started + "-reused"
	if SameIdentity(self, reused) {
		t.Fatal("pid reuse compared equal")
	}
	replaced := self
	replaced.Ino++
	if SameIdentity(self, replaced) {
		t.Fatal("exec-in-place compared equal")
	}
	zeroVersion := DaemonPin{PID: 1, UID: 1, Started: "1", Executable: "/bin/aeon", Dev: 1, Ino: 1}
	if !zeroVersion.Valid() && pin.PIDVersion == 0 {
		t.Fatal("audit token pid version missing")
	}
	socket := filepath.Join(shortDir(t), "c")
	ln, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	client, err := net.Dial("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	server, err := ln.Accept()
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	if err := withFD(server, func(fd int) error {
		flags, err := unix.FcntlInt(uintptr(fd), unix.F_GETFD, 0)
		if err != nil || flags&unix.FD_CLOEXEC == 0 {
			t.Fatal("accepted socket is inheritable")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	_ = pin
}

func TestObservePinIsUsable(t *testing.T) {
	self, err := Observe(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	pin := PinFrom(self)
	if !pin.Valid() {
		t.Fatalf("observe pin is not usable pidv=%d", pin.PIDVersion)
	}
	if runtime.GOOS != "darwin" {
		return
	}
	snapped := selfPin(t)
	if self.PIDVersion == 0 || snapped.PIDVersion == 0 || self.PIDVersion != snapped.PIDVersion {
		t.Fatalf("darwin pid version observe %d snapshot %d", self.PIDVersion, snapped.PIDVersion)
	}
}

func TestSnapshotRejectsForeignUID(t *testing.T) {
	prev := kernelSelfUID
	kernelSelfUID = func() int { return os.Getuid() + 1000 }
	t.Cleanup(func() { kernelSelfUID = prev })
	socket := filepath.Join(shortDir(t), "uid")
	ln, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	client, err := net.Dial("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	server, err := ln.Accept()
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	if _, err = Snapshot(server); err == nil {
		t.Fatal("snapshot accepted a uid outside the kernel check")
	}
}

func TestHookPeerDisabledAndTokenAndMalformed(t *testing.T) {
	src := &fakeSource{note: ownerNote(), nonce: serverNonce}
	socket := serveHook(t, src, NewRegistry())
	post := func(header, body string) (int, string) {
		t.Helper()
		c, err := net.Dial("unix", socket)
		if err != nil {
			t.Fatal(err)
		}
		defer c.Close()
		raw := "POST /v1/inbox-hook HTTP/1.1\r\nHost: agentd\r\nContent-Type: application/json\r\nContent-Length: " + itoa(len(body)) + "\r\n"
		if header != "" {
			raw += header + "\r\n"
		}
		raw += "Connection: close\r\n\r\n" + body
		if _, err = c.Write([]byte(raw)); err != nil {
			t.Fatal(err)
		}
		buf, err := io.ReadAll(c)
		if err != nil {
			t.Fatal(err)
		}
		line := string(buf)
		if i := strings.Index(line, " "); i >= 0 {
			rest := line[i+1:]
			if j := strings.IndexAny(rest, " \r"); j > 0 {
				return atoi(rest[:j]), line
			}
		}
		t.Fatal("no status")
		return 0, ""
	}
	if status, text := post("Authorization: Bearer socket-token-is-not-authority", `{"op":"offer","event":"PostToolUse"}`); status != 404 || strings.Contains(text, ownerNote().Body) || len(src.offers) != 0 {
		t.Fatalf("disabled status %d offers %d", status, len(src.offers))
	}
	EnableForTest(t.Cleanup)
	if status, text := post("Authorization: Bearer socket-token-is-not-authority", `{"op":"offer","event":"PostToolUse","cwd":"/tmp","pid":1}`); status != 400 || len(src.offers) != 0 || strings.Contains(text, "socket-token") {
		t.Fatalf("forged fields status %d", status)
	}
	if status, _ := post("", `{"op":"settle","nonce":"not-a-nonce","outcome":"shown"}`); status != 400 || len(src.settles) != 0 {
		t.Fatalf("malformed nonce status %d settles %d", status, len(src.settles))
	}
	if status, _ := post("", `{"op":"settle","nonce":"`+serverNonce+`","outcome":"shown"}`); status != 400 || len(src.settles) != 0 {
		t.Fatalf("unissued nonce status %d", status)
	}
}

func TestFakeDaemonSocketIsNotRead(t *testing.T) {
	EnableForTest(t.Cleanup)
	socket := filepath.Join(shortDir(t), "fake")
	ln, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	got := make(chan []byte, 1)
	go func() {
		c, err := ln.Accept()
		if err != nil {
			got <- nil
			return
		}
		defer c.Close()
		_, _ = c.Write([]byte(serverCanary))
		c.SetReadDeadline(time.Now().Add(300 * time.Millisecond))
		buf := make([]byte, 1024)
		n, _ := c.Read(buf)
		got <- buf[:n]
	}()
	pin := selfPin(t)
	pin.Ino ^= 1
	if pin.Ino == 0 {
		pin.Ino = 1
	}
	_, err = Dial(context.Background(), socket, pin)
	if err == nil || strings.Contains(err.Error(), serverCanary) {
		t.Fatal("fake daemon was accepted or its payload was read")
	}
	select {
	case buf := <-got:
		if len(buf) != 0 || bytes.Contains(buf, []byte(serverCanary)) {
			t.Fatal("client wrote to the fake daemon")
		}
	case <-time.After(time.Second):
		t.Fatal("fake daemon was not contacted")
	}
}

func itoa(n int) string {
	return strconvItoa(n)
}

func atoi(s string) int {
	n := 0
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0
		}
		n = n*10 + int(c-'0')
	}
	return n
}

func strconvItoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [16]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}

func (f *fakeSource) Validate(context.Context, string, Binding) error { return nil }
