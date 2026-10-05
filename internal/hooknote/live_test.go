// SPDX-License-Identifier: AGPL-3.0-only
//go:build darwin || linux

package hooknote

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestMain(m *testing.M) {
	if os.Getenv("AEON_HOOK_PEER_HELPER") != "" {
		runHookHelper()
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func runHookHelper() {
	switch os.Getenv("AEON_HOOK_PEER_HELPER") {
	case "sleep":
		fmt.Println(os.Getpid())
		_, _ = io.Copy(io.Discard, os.Stdin)
	case "spawn":
		env := make([]string, 0, len(os.Environ()))
		for _, e := range os.Environ() {
			if !strings.HasPrefix(e, "AEON_HOOK_PEER_HELPER=") {
				env = append(env, e)
			}
		}
		cmd := exec.Command(os.Args[0])
		cmd.Env = append(env, "AEON_HOOK_PEER_HELPER=dial")
		cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
		if err := cmd.Run(); err != nil {
			os.Exit(1)
		}
	case "dial":
		EnableForTest(func(func()) {})
		pinRaw := os.Getenv("AEON_HOOK_PEER_PIN")
		var pin DaemonPin
		if json.Unmarshal([]byte(pinRaw), &pin) != nil {
			fmt.Fprintln(os.Stderr, "pin")
			os.Exit(1)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		session, err := dialWithTrust(ctx, os.Getenv("AEON_HOOK_PEER_SOCKET"), pin, fixtureDaemonTrust)
		if err != nil {
			fmt.Fprintln(os.Stderr, "dial")
			os.Exit(1)
		}
		defer session.Close()
		claim := Claim{Event: "PostToolUse", VendorRef: os.Getenv("AEON_HOOK_PEER_VENDOR"), EnvSession: os.Getenv("AEON_HOOK_PEER_ENV_SESSION"), Subagent: os.Getenv("AEON_HOOK_PEER_SUBAGENT") == "1"}
		note, nonce, err := session.Offer(ctx, claim)
		if err == ErrExpired {
			_ = session.Settle(ctx, nonce, OutcomeUncertain)
			fmt.Fprintln(os.Stderr, "expired")
			os.Exit(1)
		}
		if err != nil {
			fmt.Fprintln(os.Stderr, "offer")
			os.Exit(1)
		}
		if os.Getenv("AEON_HOOK_PEER_BAD_NONCE") == "1" {
			if session.Settle(ctx, strings.Repeat("ab", 32), OutcomeShown) == nil {
				fmt.Fprintln(os.Stdout, "BAD-NONCE-ACCEPTED")
				os.Exit(1)
			}
		}
		if note.Origin == OriginAgent {
			if note.Body != "" || note.Owner != "" {
				fmt.Fprintln(os.Stdout, "AGENT-LEAK")
				os.Exit(1)
			}
			if session.Settle(ctx, nonce, OutcomeDropped) != nil {
				os.Exit(1)
			}
			fmt.Fprintln(os.Stdout, "AGENT-SILENT")
			return
		}
		fmt.Fprintln(os.Stdout, "BODY:"+note.Body)
		if session.Settle(ctx, nonce, OutcomeShown) != nil {
			os.Exit(1)
		}
	case "inherit":
		f := os.NewFile(3, "sock")
		body := `{"op":"offer","event":"PostToolUse"}`
		fmt.Fprintf(f, "POST /v1/inbox-hook HTTP/1.1\r\nHost: agentd\r\nContent-Type: application/json\r\nContent-Length: %d\r\nConnection: close\r\n\r\n%s", len(body), body)
		buf := make([]byte, 2048)
		_ = f.SetReadDeadline(time.Now().Add(2 * time.Second))
		n, _ := f.Read(buf)
		os.Stdout.Write(buf[:n])
	case "exec":
		c, err := net.Dial("unix", os.Getenv("AEON_HOOK_PEER_SOCKET"))
		if err != nil {
			os.Exit(1)
		}
		f, err := c.(*net.UnixConn).File()
		if err != nil {
			os.Exit(1)
		}
		if unix.Dup2(int(f.Fd()), 3) != nil {
			os.Exit(1)
		}
		if _, err = unix.FcntlInt(3, unix.F_SETFD, 0); err != nil {
			os.Exit(1)
		}
		script := "req='{\"op\":\"offer\",\"event\":\"PostToolUse\"}'\n" +
			"printf 'POST /v1/inbox-hook HTTP/1.1\r\nHost: agentd\r\nContent-Type: application/json\r\nContent-Length: %s\r\nConnection: close\r\n\r\n%s' ${#req} \"$req\" >&3\n" +
			"dd bs=2048 count=1 <&3 2>/dev/null\n"
		if err = syscall.Exec("/bin/sh", []string{"/bin/sh", "-c", script}, os.Environ()); err != nil {
			os.Exit(1)
		}
	case "delegate":
		c, err := net.Dial("unix", os.Getenv("AEON_HOOK_PEER_SOCKET"))
		if err != nil {
			os.Exit(1)
		}
		f, err := c.(*net.UnixConn).File()
		if err != nil {
			os.Exit(1)
		}
		cmd := exec.Command(os.Args[0])
		cmd.Env = append(os.Environ(), "AEON_HOOK_PEER_HELPER=inherit")
		cmd.ExtraFiles = []*os.File{f}
		cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
		if cmd.Run() != nil {
			os.Exit(1)
		}
	case "other":
		env := make([]string, 0, len(os.Environ()))
		for _, e := range os.Environ() {
			if !strings.HasPrefix(e, "AEON_HOOK_PEER_HELPER=") {
				env = append(env, e)
			}
		}
		cmd := exec.Command(os.Args[0])
		cmd.Env = append(env, "AEON_HOOK_PEER_HELPER=dial")
		cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
		if cmd.Run() != nil {
			os.Exit(1)
		}
	case "hold-exec":
		c, err := net.Dial("unix", os.Getenv("AEON_HOOK_PEER_SOCKET"))
		if err != nil {
			os.Exit(1)
		}
		f, err := c.(*net.UnixConn).File()
		if err != nil {
			os.Exit(1)
		}
		dup, err := unix.Dup(int(f.Fd()))
		if err != nil {
			os.Exit(1)
		}
		if _, err = unix.FcntlInt(uintptr(dup), unix.F_SETFD, 0); err != nil {
			os.Exit(1)
		}
		fmt.Println("ready")
		_ = os.Stdout.Sync()
		buf := make([]byte, 1)
		_, _ = os.Stdin.Read(buf)
		if err = unix.Exec("/bin/sleep", []string{"sleep", "30"}, []string{}); err != nil {
			os.Exit(1)
		}
	case "raw":
		EnableForTest(func(func()) {})
		var pin DaemonPin
		if json.Unmarshal([]byte(os.Getenv("AEON_HOOK_PEER_PIN")), &pin) != nil {
			os.Exit(1)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		session, err := dialWithTrust(ctx, os.Getenv("AEON_HOOK_PEER_SOCKET"), pin, fixtureDaemonTrust)
		if err != nil {
			os.Exit(1)
		}
		defer session.Close()
		note, _, err := session.Offer(ctx, Claim{Event: "PostToolUse"})
		if err != nil {
			os.Exit(1)
		}
		_ = json.NewEncoder(os.Stdout).Encode(note)
	default:
		os.Exit(2)
	}
}

func TestLiveHookPeerExchange(t *testing.T) {
	EnableForTest(t.Cleanup)
	self, err := Observe(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	pin := selfPin(t)
	rawPin, _ := json.Marshal(pin)
	noteBody := "inspect the gate"
	src := &fakeSource{note: ownerNote(), nonce: serverNonce}
	src.note.Body = noteBody
	reg := NewRegistry()
	reg.Add(Grant{Binding: Binding{SessionID: sessionA, MessageGeneration: genA, DaemonGeneration: "daemon-1", Harness: self, HookExecutable: self.Executable, HookDev: self.Dev, HookIno: self.Ino}, VendorRef: "real-ref"})
	socket := serveHook(t, src, reg)

	out := runHelper(t, "dial", socket, string(rawPin), nil)
	if !strings.Contains(out, "BODY:"+noteBody) || strings.Contains(out, "BAD-NONCE") || len(src.offers) != 1 || src.offers[0].SessionID != sessionA || len(src.settles) != 1 || !strings.HasPrefix(src.settles[0], OutcomeShown) {
		t.Fatalf("direct child offers %d settles %v out %q", len(src.offers), src.settles, out)
	}

	src.offers, src.settles = nil, nil
	out = runHelper(t, "spawn", socket, string(rawPin), nil)
	if strings.Contains(out, noteBody) || len(src.offers) != 0 {
		t.Fatal("tool descendant received a note")
	}

	src.offers = nil
	out = runHelper(t, "dial", socket, string(rawPin), []string{"AEON_HOOK_PEER_SUBAGENT=1"})
	if strings.Contains(out, noteBody) || len(src.offers) != 0 {
		t.Fatal("subagent received a note")
	}

	src.offers = nil
	out = runHelper(t, "dial", socket, string(rawPin), []string{"AEON_HOOK_PEER_VENDOR=forged-ref"})
	if strings.Contains(out, noteBody) || len(src.offers) != 0 {
		t.Fatal("forged vendor reference received a note")
	}

	src.offers = nil
	out = runHelper(t, "dial", socket, string(rawPin), []string{"AEON_HOOK_PEER_ENV_SESSION=" + sessionB})
	if strings.Contains(out, noteBody) || len(src.offers) != 0 {
		t.Fatal("forged environment binding received a note")
	}

	reg.RevokeGeneration(genA)
	src.offers = nil
	out = runHelper(t, "dial", socket, string(rawPin), nil)
	if strings.Contains(out, noteBody) || len(src.offers) != 0 {
		t.Fatal("revoked generation received a note")
	}
}

func TestLiveInheritedSocketAndExec(t *testing.T) {
	EnableForTest(t.Cleanup)
	self, err := Observe(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	noteBody := "inspect the gate"
	src := &fakeSource{note: ownerNote(), nonce: serverNonce}
	src.note.Body = noteBody
	reg := NewRegistry()
	reg.Add(Grant{Binding: Binding{SessionID: sessionA, MessageGeneration: genA, DaemonGeneration: "daemon-1", Harness: self, HookExecutable: self.Executable, HookDev: self.Dev, HookIno: self.Ino}})
	socket := serveHook(t, src, reg)

	conn, err := net.Dial("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	file, err := conn.(*net.UnixConn).File()
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	out := runHelper(t, "inherit", socket, "", []*os.File{file})
	if strings.Contains(out, noteBody) || len(src.offers) != 0 {
		t.Fatal("inherited socket received a note")
	}

	src.offers = nil
	out = runHelper(t, "exec", socket, "", nil)
	if !strings.Contains(out, "403") || strings.Contains(out, noteBody) || len(src.offers) != 0 {
		t.Fatalf("exec-in-place out %q offers %d", out, len(src.offers))
	}
}

func TestLiveSiblingAndAmbiguousAndAgentOrigin(t *testing.T) {
	EnableForTest(t.Cleanup)
	pin := selfPin(t)
	rawPin, _ := json.Marshal(pin)
	sleeper := exec.Command(os.Args[0])
	sleeper.Env = append(os.Environ(), "AEON_HOOK_PEER_HELPER=sleep")
	stdout, err := sleeper.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdin, err := sleeper.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err = sleeper.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { stdin.Close(); _ = sleeper.Wait() }()
	var pidLine bytes.Buffer
	buf := make([]byte, 64)
	for pidLine.Len() == 0 || !bytes.Contains(pidLine.Bytes(), []byte("\n")) {
		n, err := stdout.Read(buf)
		pidLine.Write(buf[:n])
		if err != nil {
			t.Fatal(err)
		}
	}
	pid, err := strconv.Atoi(strings.TrimSpace(pidLine.String()))
	if err != nil {
		t.Fatal(err)
	}
	sibling, err := Observe(pid)
	if err != nil {
		t.Fatal(err)
	}
	self, err := Observe(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	noteBody := "inspect the gate"
	src := &fakeSource{note: ownerNote(), nonce: serverNonce}
	src.note.Body = noteBody
	reg := NewRegistry()
	reg.Add(Grant{Binding: Binding{SessionID: sessionA, MessageGeneration: genA, DaemonGeneration: "daemon-1", Harness: sibling, HookExecutable: self.Executable, HookDev: self.Dev, HookIno: self.Ino}})
	socket := serveHook(t, src, reg)
	out := runHelper(t, "dial", socket, string(rawPin), nil)
	if strings.Contains(out, noteBody) || len(src.offers) != 0 {
		t.Fatal("sibling received a note")
	}

	ambiguous := NewRegistry()
	ambiguous.Add(Grant{Binding: Binding{SessionID: sessionA, MessageGeneration: genA, DaemonGeneration: "daemon-1", Harness: self, HookExecutable: self.Executable, HookDev: self.Dev, HookIno: self.Ino}})
	ambiguous.Add(Grant{Binding: Binding{SessionID: sessionB, MessageGeneration: genB, DaemonGeneration: "daemon-1", Harness: self, HookExecutable: self.Executable, HookDev: self.Dev, HookIno: self.Ino}})
	socket = serveHook(t, src, ambiguous)
	src.offers = nil
	out = runHelper(t, "dial", socket, string(rawPin), nil)
	if strings.Contains(out, noteBody) || len(src.offers) != 0 {
		t.Fatal("ambiguous same-directory sessions received a note")
	}

	agent := &fakeSource{note: ownerNote(), nonce: serverNonce}
	agent.note.Origin = OriginAgent
	agent.note.Body = "agent-origin-secret-body"
	agent.note.Owner = "agent-label"
	one := NewRegistry()
	one.Add(Grant{Binding: Binding{SessionID: sessionA, MessageGeneration: genA, DaemonGeneration: "daemon-1", Harness: self, HookExecutable: self.Executable, HookDev: self.Dev, HookIno: self.Ino}})
	socket = serveHook(t, agent, one)
	out = runHelper(t, "raw", socket, string(rawPin), nil)
	if strings.Contains(out, "agent-origin-secret-body") || strings.Contains(out, "agent-label") || !strings.Contains(out, `"origin":"agent"`) {
		t.Fatal("agent origin body was released to the hook")
	}
	out = runHelper(t, "dial", socket, string(rawPin), nil)
	if !strings.Contains(out, "AGENT-SILENT") || strings.Contains(out, "agent-origin-secret-body") || len(agent.settles) != 1 || !strings.HasPrefix(agent.settles[0], OutcomeDropped) {
		t.Fatal("agent origin produced model-visible output")
	}
}

func TestLiveBadNonceAfterOffer(t *testing.T) {
	EnableForTest(t.Cleanup)
	self, err := Observe(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	pin := selfPin(t)
	rawPin, _ := json.Marshal(pin)
	src := &fakeSource{note: ownerNote(), nonce: serverNonce}
	reg := NewRegistry()
	reg.Add(Grant{Binding: Binding{SessionID: sessionA, MessageGeneration: genA, DaemonGeneration: "daemon-1", Harness: self, HookExecutable: self.Executable, HookDev: self.Dev, HookIno: self.Ino}})
	socket := serveHook(t, src, reg)
	out := runHelper(t, "dial", socket, string(rawPin), []string{"AEON_HOOK_PEER_BAD_NONCE=1"})
	if strings.Contains(out, "BAD-NONCE-ACCEPTED") || len(src.settles) != 1 || !strings.HasPrefix(src.settles[0], OutcomeShown+":"+serverNonce) {
		t.Fatalf("nonce settles %v", src.settles)
	}
}

func runHelper(t *testing.T, mode, socket, pin string, extra any) string {
	t.Helper()
	cmd := exec.Command(os.Args[0])
	env := []string{"AEON_HOOK_PEER_HELPER=" + mode, "AEON_HOOK_PEER_SOCKET=" + socket, "AEON_HOOK_PEER_PIN=" + pin}
	var files []*os.File
	switch v := extra.(type) {
	case []string:
		env = append(env, v...)
	case []*os.File:
		files = v
	case nil:
	default:
		t.Fatalf("bad extra")
	}
	cmd.Env = append(os.Environ(), env...)
	cmd.ExtraFiles = files
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	if mode == "dial" && err != nil && stdout.Len() == 0 && stderr.Len() == 0 {
		t.Fatal("helper failed")
	}
	return stdout.String() + stderr.String()
}

func liveFixture(t *testing.T, src NoteSource) (string, string) {
	t.Helper()
	EnableForTest(t.Cleanup)
	self, err := Observe(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	pin, err := json.Marshal(selfPin(t))
	if err != nil {
		t.Fatal(err)
	}
	reg := NewRegistry()
	reg.Add(Grant{Binding: Binding{SessionID: sessionA, MessageGeneration: genA, DaemonGeneration: "daemon-1", Harness: self, HookExecutable: self.Executable, HookDev: self.Dev, HookIno: self.Ino}})
	if revoking, ok := src.(*revokingSource); ok {
		revoking.reg = reg
	}
	return serveHook(t, src, reg), string(pin)
}

type revokingSource struct {
	*fakeSource
	reg *Registry
}

func (s *revokingSource) Offer(ctx context.Context, b Binding) (Note, string, error) {
	note, nonce, err := s.fakeSource.Offer(ctx, b)
	s.reg.RevokeGeneration(genA)
	return note, nonce, err
}

func TestLargeNotesDeliverThroughChunkedResponses(t *testing.T) {
	for _, n := range []int{3000, MaxBodyBytes} {
		t.Run(strconv.Itoa(n), func(t *testing.T) {
			src := &fakeSource{note: ownerNote(), nonce: serverNonce}
			src.note.Body = strings.Repeat("a", n)
			socket, pin := liveFixture(t, src)
			out := runHelper(t, "dial", socket, pin, nil)
			if !strings.Contains(out, "BODY:"+src.note.Body) || len(src.settles) != 1 || !strings.HasPrefix(src.settles[0], OutcomeShown) {
				t.Fatalf("note lost offers=%d settles=%v helper=%q", len(src.offers), src.settles, out)
			}
		})
	}
}

func TestRevokedGenerationBetweenOfferAndReceipt(t *testing.T) {
	src := &revokingSource{fakeSource: &fakeSource{note: ownerNote(), nonce: serverNonce}}
	socket, pin := liveFixture(t, src)
	out := runHelper(t, "dial", socket, pin, nil)
	if len(src.settles) != 1 || strings.HasPrefix(src.settles[0], OutcomeShown) || !strings.HasPrefix(src.settles[0], OutcomeUncertain) {
		t.Fatalf("shown settled after revocation settles=%v helper=%q", src.settles, out)
	}
}

func TestExpiredOfferProducesNoOutput(t *testing.T) {
	src := &fakeSource{note: ownerNote(), nonce: serverNonce}
	src.note.Deadline = time.Now().Add(-time.Minute).UTC().Format(time.RFC3339)
	socket, pin := liveFixture(t, src)
	out := runHelper(t, "dial", socket, pin, nil)
	if strings.Contains(out, "BODY:") || len(src.settles) != 1 || !strings.HasPrefix(src.settles[0], OutcomeUncertain) {
		t.Fatalf("expired note was delivered settles=%v helper=%q", src.settles, out)
	}
}

func TestOtherHarnessCannotDialTheApprovedHelper(t *testing.T) {
	src := &fakeSource{note: ownerNote(), nonce: serverNonce}
	socket, pin := liveFixture(t, src)
	out := runHelper(t, "other", socket, pin, nil)
	if strings.Contains(out, src.note.Body) || len(src.offers) != 0 {
		t.Fatalf("other harness received a note offers=%d helper=%q", len(src.offers), out)
	}
}

func TestKnownLimitSameRecipientInsideApprovedTree(t *testing.T) {
	src := &fakeSource{note: ownerNote(), nonce: serverNonce}
	socket, pin := liveFixture(t, src)
	// A direct-child shell that execs the approved helper keeps the harness as
	// its parent. It is the same recipient; the receipt stays "hook reported".
	cmd := exec.Command("/bin/sh", "-c", `exec "$1"`, "tool-shell", os.Args[0])
	cmd.Env = append(os.Environ(), "AEON_HOOK_PEER_HELPER=dial", "AEON_HOOK_PEER_SOCKET="+socket, "AEON_HOOK_PEER_PIN="+pin)
	out, _ := cmd.CombinedOutput()
	if !strings.Contains(string(out), "BODY:"+src.note.Body) || len(src.offers) != 1 {
		t.Fatalf("in-tree shell did not behave as the same recipient offers=%d helper=%q", len(src.offers), out)
	}

	src.offers, src.settles = nil, nil
	// The kernel names the connector, not the process that writes. On Linux
	// that connector is still the approved child, so the grandchild receives
	// the note. A rejection is the same boundary: the socket never selects
	// another session.
	inherited := runHelper(t, "delegate", socket, pin, nil)
	if len(src.offers) > 1 || (len(src.offers) == 1 && src.offers[0].SessionID != sessionA) || (len(src.offers) == 0 && strings.Contains(inherited, src.note.Body)) {
		t.Fatalf("inherited socket left this session offers=%d helper=%q", len(src.offers), inherited)
	}
}

func TestLoadedImageIgnoresPathnameReplacement(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "hook")
	replacement := filepath.Join(root, "replacement")
	raw, err := os.ReadFile(os.Args[0])
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(path, raw, 0500); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(replacement, raw, 0500); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(path)
	cmd.Env = append(os.Environ(), "AEON_HOOK_PEER_HELPER=sleep")
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err = cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { stdin.Close(); _ = cmd.Wait() }()
	var pid int
	if _, err = fmt.Fscanln(stdout, &pid); err != nil {
		t.Fatal(err)
	}
	before, err := Observe(pid)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.Rename(replacement, path); err != nil {
		t.Fatal(err)
	}
	after, err := Observe(pid)
	if err == nil && after.Ino != before.Ino {
		t.Fatalf("running image misidentified as replacement before=%d observed=%d", before.Ino, after.Ino)
	}
}

func TestRecheckRejectsExecImageChange(t *testing.T) {
	socket := filepath.Join(shortDir(t), "exec")
	ln, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	cmd := exec.Command(os.Args[0])
	cmd.Env = append(os.Environ(), "AEON_HOOK_PEER_HELPER=hold-exec", "AEON_HOOK_PEER_SOCKET="+socket)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err = cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		stdin.Close()
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	}()
	server, err := ln.Accept()
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	var ready string
	if _, err = fmt.Fscanln(stdout, &ready); err != nil || ready != "ready" {
		t.Fatal(err, ready)
	}
	before, err := Snapshot(server)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = stdin.Write([]byte("x")); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	changed := false
	for time.Now().Before(deadline) {
		now, obsErr := Observe(before.PID)
		if obsErr == nil && (now.Ino != before.Ino || now.Executable != before.Executable) {
			changed = true
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if !changed {
		t.Fatal("exec did not replace the peer image")
	}
	if err = Recheck(server, before); err == nil {
		t.Fatal("peer recheck ignored the image change")
	}
}

type reviewChangingSource struct {
	*fakeSource
	change func()
}

func (s *reviewChangingSource) Offer(ctx context.Context, b Binding) (Note, string, error) {
	n, nonce, err := s.fakeSource.Offer(ctx, b)
	s.change()
	return n, nonce, err
}

func TestSettlementOfAChangedBindingIsUncertain(t *testing.T) {
	for _, change := range []string{"replace-message-generation", "replace-daemon-generation", "replace-harness", "append-message-generation", "append-other-session"} {
		t.Run(change, func(t *testing.T) {
			EnableForTest(t.Cleanup)
			self, err := Observe(os.Getpid())
			if err != nil {
				t.Fatal(err)
			}
			pin, _ := json.Marshal(selfPin(t))
			g := Grant{Binding: Binding{SessionID: sessionA, MessageGeneration: genA, DaemonGeneration: "daemon-1", Harness: self, HookExecutable: self.Executable, HookDev: self.Dev, HookIno: self.Ino}}
			reg := NewRegistry()
			reg.Add(g)
			src := &reviewChangingSource{fakeSource: &fakeSource{note: ownerNote(), nonce: serverNonce}}
			src.change = func() {
				if change == "append-message-generation" {
					next := g
					next.Binding.MessageGeneration = genB
					reg.Add(next)
					return
				}
				if change == "append-other-session" {
					next := g
					next.Binding.SessionID = sessionB
					reg.Add(next)
					return
				}
				reg.mu.Lock()
				defer reg.mu.Unlock()
				switch change {
				case "replace-message-generation":
					reg.grants[0].Binding.MessageGeneration = genB
				case "replace-daemon-generation":
					reg.grants[0].Binding.DaemonGeneration = "daemon-2"
				case "replace-harness":
					reg.grants[0].Binding.Harness.Ino++
				}
			}
			socket := serveHook(t, src, reg)
			helper := runHelper(t, "dial", socket, string(pin), nil)
			if len(src.settles) != 1 || !strings.HasPrefix(src.settles[0], OutcomeUncertain) || reg.Receipt(serverNonce) == OutcomeShown {
				t.Fatalf("changed binding settled as shown: settles=%v receipt=%s helper=%q", src.settles, reg.Receipt(serverNonce), helper)
			}
		})
	}
}

// comparingSource is a NoteSource with the server's compare-and-settle.
// The first receipt for a nonce is terminal. The same outcome settles
// again without change. A different outcome conflicts: the stored receipt
// is kept and returned as SettledError. shown is stored on that first
// receipt only when the offered epoch is still the server's epoch. A stale
// epoch stores uncertain. It does not read the local registry.
// reportRecorded returns the stored outcome instead of nil. loseAck stores
// the outcome and then returns a plain error, which is a lost response:
// the caller cannot read it back.
type comparingSource struct {
	*fakeSource
	offered        Binding
	serverEpoch    Epoch
	entered        chan struct{}
	release        chan struct{}
	transport      error
	reportRecorded bool
	loseAck        bool
	calls          int
	recorded       map[string]string
}

func (s *comparingSource) Offer(ctx context.Context, b Binding) (Note, string, error) {
	s.offered = b
	s.serverEpoch = b.Epoch
	return s.fakeSource.Offer(ctx, b)
}

func (s *comparingSource) Settle(ctx context.Context, nonce, outcome string, epoch Epoch) error {
	s.calls++
	if s.entered != nil {
		close(s.entered)
		<-s.release
	}
	if s.transport != nil {
		return s.transport
	}
	if !ValidNonce(nonce) || !ValidOutcome(outcome) {
		return ErrMalformedNonce
	}
	if prev, ok := s.recorded[nonce]; ok {
		if prev != outcome {
			return &SettledError{Outcome: prev}
		}
		return nil
	}
	final := outcome
	if outcome == OutcomeShown && epoch != s.serverEpoch {
		final = OutcomeUncertain
	}
	s.note(nonce, final)
	if s.loseAck {
		return errors.New("receipt response lost")
	}
	if s.reportRecorded || final != outcome {
		return &SettledError{Outcome: final}
	}
	return nil
}

func (s *comparingSource) note(nonce, outcome string) {
	if s.recorded == nil {
		s.recorded = map[string]string{}
	}
	s.recorded[nonce] = outcome
	s.settles = append(s.settles, outcome+":"+nonce)
}

func TestSettlementAgreesWhenTheSourceSettles(t *testing.T) {
	self, err := Observe(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	grant := Grant{Binding: Binding{SessionID: sessionA, MessageGeneration: genA, DaemonGeneration: "daemon-1", Harness: self, HookExecutable: self.Executable, HookDev: self.Dev, HookIno: self.Ino}}
	cases := []struct {
		name    string
		mutate  func(reg *Registry, src *comparingSource)
		want    string
		stillUp bool
	}{
		{name: "shown", want: OutcomeShown, stillUp: true},
		{name: "revoked-during-settle", want: OutcomeShown, mutate: func(reg *Registry, _ *comparingSource) { reg.RevokeGeneration(genA) }},
		{name: "second-grant-during-settle", want: OutcomeShown, mutate: func(reg *Registry, _ *comparingSource) {
			next := grant
			next.Binding.SessionID = sessionB
			reg.Add(next)
		}},
		{name: "server-rejects-stale-epoch", want: OutcomeUncertain, stillUp: true, mutate: func(_ *Registry, src *comparingSource) {
			src.serverEpoch.Counter += 1
		}},
		{name: "settle-transport-error", want: OutcomeUncertain, stillUp: true, mutate: func(_ *Registry, src *comparingSource) {
			src.transport = errors.New("attached exchange unavailable")
		}},
		{name: "lost-response", want: OutcomeUncertain, stillUp: true, mutate: func(_ *Registry, src *comparingSource) {
			src.loseAck = true
		}},
		{name: "returns-recorded-outcome", want: OutcomeShown, stillUp: true, mutate: func(_ *Registry, src *comparingSource) {
			src.reportRecorded = true
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			EnableForTest(t.Cleanup)
			pin, _ := json.Marshal(selfPin(t))
			reg := NewRegistry()
			reg.Add(grant)
			src := &comparingSource{
				fakeSource: &fakeSource{note: ownerNote(), nonce: serverNonce},
				entered:    make(chan struct{}),
				release:    make(chan struct{}),
			}
			var once sync.Once
			release := func() { once.Do(func() { close(src.release) }) }
			t.Cleanup(release)
			socket := serveHook(t, src, reg)
			done := make(chan string, 1)
			go func() { done <- runHelper(t, "dial", socket, string(pin), nil) }()
			select {
			case <-src.entered:
			case <-time.After(4 * time.Second):
				t.Fatal("settlement not entered")
			}
			if src.offered.Epoch.Counter == 0 || !reg.Live(src.offered.Epoch) {
				t.Fatal("epoch was not current when settlement began")
			}
			if tc.mutate != nil {
				tc.mutate(reg, src)
			}
			release()
			var helper string
			select {
			case helper = <-done:
			case <-time.After(4 * time.Second):
				t.Fatal("helper did not finish")
			}
			local := reg.Receipt(serverNonce)
			remote := src.recorded[serverNonce]
			if src.calls != 1 {
				t.Fatalf("settle calls %d registry %s source %s", src.calls, local, remote)
			}
			if tc.name == "lost-response" {
				if local != OutcomeUncertain || remote != OutcomeShown {
					t.Fatalf("lost response registry %s source %s settles %v helper %q", local, remote, src.settles, helper)
				}
			} else if (local == OutcomeShown) != (remote == OutcomeShown) || local != tc.want {
				t.Fatalf("registry %s source %s calls %d settles %v helper %q", local, remote, src.calls, src.settles, helper)
			}
			if tc.name == "settle-transport-error" && remote != "" {
				t.Fatalf("transport error still recorded %s", remote)
			}
			if tc.name == "server-rejects-stale-epoch" && remote != OutcomeUncertain {
				t.Fatalf("stale epoch stored %s", remote)
			}
			if (tc.name == "revoked-during-settle" || tc.name == "second-grant-during-settle") && (local != OutcomeShown || remote != OutcomeShown) {
				t.Fatalf("completed write recalled registry %s source %s", local, remote)
			}
			if tc.stillUp && !reg.Live(src.offered.Epoch) {
				t.Fatal("local epoch was retired")
			}
			if tc.name == "second-grant-during-settle" && reg.Live(src.offered.Epoch) {
				t.Fatal("conflicting grant left the offered epoch live")
			}
		})
	}
}

type armingSource struct {
	*fakeSource
	arm func()
}

func (s *armingSource) Offer(ctx context.Context, b Binding) (Note, string, error) {
	n, nonce, err := s.fakeSource.Offer(ctx, b)
	if s.arm != nil {
		s.arm()
	}
	return n, nonce, err
}

// Revocation that Commit observes before the receipt is sent still fails
// closed. The write has not been settled, so the result is never shown.
func TestRevocationDuringSettlementObservationIsUncertain(t *testing.T) {
	EnableForTest(t.Cleanup)
	self, err := Observe(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	pin, _ := json.Marshal(selfPin(t))
	reg := NewRegistry()
	reg.Add(Grant{Binding: Binding{SessionID: sessionA, MessageGeneration: genA, DaemonGeneration: "daemon-1", Harness: self, HookExecutable: self.Executable, HookDev: self.Dev, HookIno: self.Ino}})
	if !reg.Live(Epoch{Generation: genA, Counter: 1}) {
		t.Fatal("grant epoch was not live")
	}
	var armed atomic.Bool
	entered := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	observe := func(pid int) (Process, error) {
		if armed.Load() && pid == self.PID {
			once.Do(func() {
				close(entered)
				<-release
			})
		}
		return Observe(pid)
	}
	src := &armingSource{fakeSource: &fakeSource{note: ownerNote(), nonce: serverNonce}, arm: func() { armed.Store(true) }}
	socket := serveHookObserve(t, src, reg, observe)
	done := make(chan string, 1)
	go func() { done <- runHelper(t, "dial", socket, string(pin), nil) }()
	select {
	case <-entered:
	case <-time.After(4 * time.Second):
		t.Fatal("settlement observation did not block")
	}
	reg.RevokeGeneration(genA)
	close(release)
	select {
	case <-done:
	case <-time.After(4 * time.Second):
		t.Fatal("helper did not finish")
	}
	if len(src.settles) != 1 || !strings.HasPrefix(src.settles[0], OutcomeUncertain) || reg.Receipt(serverNonce) == OutcomeShown {
		t.Fatalf("revocation during re-observation settled %v receipt=%s", src.settles, reg.Receipt(serverNonce))
	}
}

// A confirmed server answer replaces the local proposal. The hook asks for
// shown after a full write; dropped and uncertain must still become the receipt.
func TestConfirmedServerAnswerReplacesTheLocalReceipt(t *testing.T) {
	for _, terminal := range []string{OutcomeShown, OutcomeUncertain, OutcomeDropped} {
		t.Run(terminal, func(t *testing.T) {
			EnableForTest(t.Cleanup)
			self, err := Observe(os.Getpid())
			if err != nil {
				t.Fatal(err)
			}
			pin, _ := json.Marshal(selfPin(t))
			reg := NewRegistry()
			reg.Add(Grant{Binding: Binding{SessionID: sessionA, MessageGeneration: genA, DaemonGeneration: "daemon-1", Harness: self, HookExecutable: self.Executable, HookDev: self.Dev, HookIno: self.Ino}})
			src := &recordedAnswerSource{fakeSource: &fakeSource{note: ownerNote(), nonce: serverNonce}, recorded: terminal}
			socket := serveHook(t, src, reg)
			helper := runHelper(t, "dial", socket, string(pin), nil)
			if got := reg.Receipt(serverNonce); got != terminal || src.requested != OutcomeShown {
				t.Fatalf("server answered %s, registry %s, requested %s, settles %v, helper %q", terminal, got, src.requested, src.settles, helper)
			}
		})
	}
}

type recordedAnswerSource struct {
	*fakeSource
	recorded  string
	requested string
}

func (s *recordedAnswerSource) Settle(_ context.Context, nonce, outcome string, _ Epoch) error {
	s.requested = outcome
	s.settles = append(s.settles, s.recorded+":"+nonce)
	return &SettledError{Outcome: s.recorded}
}

func TestComparingSourcePreservesTerminalReceipts(t *testing.T) {
	epoch := Epoch{Generation: genA, Counter: 1}
	for _, tc := range []struct {
		name  string
		prior string
		next  string
	}{
		{name: "uncertain-then-shown", prior: OutcomeUncertain, next: OutcomeShown},
		{name: "shown-then-dropped", prior: OutcomeShown, next: OutcomeDropped},
		{name: "dropped-then-shown", prior: OutcomeDropped, next: OutcomeShown},
	} {
		t.Run(tc.name, func(t *testing.T) {
			src := &comparingSource{fakeSource: &fakeSource{}, serverEpoch: epoch}
			if err := src.Settle(t.Context(), serverNonce, tc.prior, epoch); err != nil {
				t.Fatal(err)
			}
			err := src.Settle(t.Context(), serverNonce, tc.next, epoch)
			var settled *SettledError
			if !errors.As(err, &settled) || settled.Outcome != tc.prior || src.recorded[serverNonce] != tc.prior || len(src.settles) != 1 {
				t.Fatalf("first receipt %s, conflicting request %s, stored %s, settles %v, returned %v", tc.prior, tc.next, src.recorded[serverNonce], src.settles, err)
			}
		})
	}
	for _, outcome := range []string{OutcomeShown, OutcomeUncertain, OutcomeDropped} {
		t.Run("duplicate-"+outcome, func(t *testing.T) {
			src := &comparingSource{fakeSource: &fakeSource{}, serverEpoch: epoch}
			if err := src.Settle(t.Context(), serverNonce, outcome, epoch); err != nil {
				t.Fatal(err)
			}
			if err := src.Settle(t.Context(), serverNonce, outcome, epoch); err != nil || src.recorded[serverNonce] != outcome || len(src.settles) != 1 {
				t.Fatalf("duplicate %s stored %s settles %v err %v", outcome, src.recorded[serverNonce], src.settles, err)
			}
		})
	}
}
