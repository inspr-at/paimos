// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
	"unsafe"

	"github.com/inspr-at/paimos/internal/config"
	"github.com/inspr-at/paimos/internal/dbtest"
	"golang.org/x/sys/unix"
)

// The helper inherits its inbound listener and reaches the fixture Postgres
// through a Unix socket proxy. Any IPv4/IPv6 socket creation (including DNS),
// through any transport or goroutine, traps before a packet can leave.
// TSYNC applies the filter to every Go runtime thread, not just this goroutine.
func denyInternetSockets(t *testing.T) {
	t.Helper()
	if err := installDenyFilter(denyFilterArch(t), true, nil); err != nil {
		t.Fatal(err)
	}
}

func denyFilterArch(t *testing.T) uint32 {
	t.Helper()
	switch runtime.GOARCH {
	case "amd64":
		return unix.AUDIT_ARCH_X86_64
	case "arm64":
		return unix.AUDIT_ARCH_AARCH64
	}
	t.Skip("network-denial fixture supports Linux amd64 and arm64")
	return 0
}

// installDenyFilter sets no_new_privs and installs the seccomp filter on every
// thread. no_new_privs belongs to one OS thread and seccomp(2) refuses a caller
// thread without it (EACCES), so both calls must run on the same thread: a
// goroutine the scheduler moved in between failed the whole fixture at random.
// pin=false exists only for the migration test's negative control; afterNoNewPrivs
// is its seam between the two calls and receives the thread that set the flag.
func installDenyFilter(arch uint32, pin bool, afterNoNewPrivs func(setBy int)) error {
	if pin {
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()
	}
	filter := []unix.SockFilter{
		{Code: unix.BPF_LD | unix.BPF_W | unix.BPF_ABS, K: 4},
		{Code: unix.BPF_JMP | unix.BPF_JEQ | unix.BPF_K, K: arch, Jt: 1},
		{Code: unix.BPF_RET | unix.BPF_K, K: unix.SECCOMP_RET_KILL_PROCESS},
		{Code: unix.BPF_LD | unix.BPF_W | unix.BPF_ABS, K: 0},
		{Code: unix.BPF_JMP | unix.BPF_JEQ | unix.BPF_K, K: unix.SYS_SOCKET, Jf: 4},
		{Code: unix.BPF_LD | unix.BPF_W | unix.BPF_ABS, K: 16},
		{Code: unix.BPF_JMP | unix.BPF_JEQ | unix.BPF_K, K: unix.AF_INET, Jt: 1},
		{Code: unix.BPF_JMP | unix.BPF_JEQ | unix.BPF_K, K: unix.AF_INET6, Jf: 1},
		{Code: unix.BPF_RET | unix.BPF_K, K: unix.SECCOMP_RET_TRAP},
		{Code: unix.BPF_RET | unix.BPF_K, K: unix.SECCOMP_RET_ALLOW},
	}
	setBy := unix.Gettid()
	if err := unix.Prctl(unix.PR_SET_NO_NEW_PRIVS, 1, 0, 0, 0); err != nil {
		return err
	}
	if afterNoNewPrivs != nil {
		afterNoNewPrivs(setBy)
	}
	program := unix.SockFprog{Len: uint16(len(filter)), Filter: &filter[0]}
	result, _, errno := unix.Syscall(unix.SYS_SECCOMP, unix.SECCOMP_SET_MODE_FILTER, unix.SECCOMP_FILTER_FLAG_TSYNC, uintptr(unsafe.Pointer(&program)))
	runtime.KeepAlive(filter)
	if errno != 0 || result != 0 {
		return fmt.Errorf("seccomp TSYNC failed: result=%d errno=%d", result, errno)
	}
	return nil
}

// noNewPrivsOf reads one thread's own no_new_privs flag from procfs.
func noNewPrivsOf(t *testing.T, tid int) string {
	t.Helper()
	raw, err := os.ReadFile(fmt.Sprintf("/proc/self/task/%d/status", tid))
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(string(raw), "\n") {
		if value, ok := strings.CutPrefix(line, "NoNewPrivs:"); ok {
			return strings.TrimSpace(value)
		}
	}
	t.Fatalf("thread %d status has no NoNewPrivs line", tid)
	return ""
}

// migrationProbe installs the filter after forcing the scheduler to take the
// goroutine off its thread between the two kernel calls. With GOMAXPROCS=1 the
// hop is deterministic: a goroutine locked to the thread parks it, and the
// only runnable goroutine, this one, is handed to another thread unless it is
// itself locked. The unpinned probe must therefore fail on the thread that
// never got no_new_privs; the pinned probe must stay on its thread and succeed.
func migrationProbe(t *testing.T, pin bool) {
	arch := denyFilterArch(t)
	// Start the runtime's template thread and a few idle threads while no
	// thread has no_new_privs yet, so a hop can land on one that lacks it.
	runtime.LockOSThread()
	runtime.UnlockOSThread()
	var parked, gate = new(sync.WaitGroup), make(chan struct{})
	for range 4 {
		parked.Add(1)
		go func() {
			runtime.LockOSThread()
			parked.Done()
			<-gate
			runtime.UnlockOSThread()
		}()
	}
	parked.Wait()
	close(gate)

	var releases []chan struct{}
	defer func() {
		for _, release := range releases {
			close(release)
		}
	}()
	hop := func() {
		ready, release := make(chan struct{}), make(chan struct{})
		releases = append(releases, release)
		go func() {
			runtime.LockOSThread()
			close(ready)
			<-release
		}()
		<-ready
	}
	between := func(setBy int) {
		if got := noNewPrivsOf(t, setBy); got != "1" {
			t.Fatalf("thread %d did not get no_new_privs: %s", setBy, got)
		}
		if pin {
			hop()
			if got := unix.Gettid(); got != setBy {
				t.Fatalf("pinned goroutine moved from thread %d to %d", setBy, got)
			}
			return
		}
		for range 16 {
			hop()
			// Hold the new thread so the filter call runs where we measured.
			runtime.LockOSThread()
			if got := unix.Gettid(); got != setBy && noNewPrivsOf(t, got) == "0" {
				return
			}
			runtime.UnlockOSThread()
		}
		t.Fatal("could not move the goroutine to a thread without no_new_privs")
	}
	if err := installDenyFilter(arch, pin, between); err != nil {
		t.Fatal(err)
	}
	fmt.Println("probe: filter installed after a forced scheduling round")
}

func TestNoOutboundServerHelper(t *testing.T) {
	mode := os.Getenv("AEON_NO_OUTBOUND_TEST")
	if mode == "" {
		return
	}
	if strings.HasPrefix(mode, "migrate-") {
		migrationProbe(t, mode == "migrate-pinned")
		return
	}
	denyInternetSockets(t)
	switch mode {
	case "connect":
		_, _ = net.DialTimeout("tcp", "192.0.2.1:443", time.Second)
		t.Fatal("outbound connect escaped the denial filter")
	case "dns":
		// PreferGo LookupHost returns "no such host" without a socket when
		// nsswitch has no DNS source (hosts: files). The pure Go resolver
		// dials a numeric nameserver only after that order includes DNS
		// (Go 1.26 dnsclient_unix.go, hostLookupFiles), so dial one if the
		// lookup returned. 192.0.2.1 is TEST-NET-1 and is not a real resolver.
		_, _ = (&net.Resolver{PreferGo: true}).LookupHost(t.Context(), "aeon-outbound-guard.invalid")
		_, _ = net.DialTimeout("udp", "192.0.2.1:53", time.Second)
		t.Fatal("DNS escaped the denial filter")
	case "server":
		cfg, err := config.FromEnv()
		if err != nil {
			t.Fatal(err)
		}
		file := os.NewFile(3, "inbound-listener")
		ln, err := net.FileListener(file)
		_ = file.Close()
		if err != nil {
			t.Fatal(err)
		}
		ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM)
		defer stop()
		if err := serveListener(ctx, cfg, ln); err != nil {
			t.Fatal(err)
		}
	default:
		t.Fatal("unknown helper mode")
	}
}

// Risk: seccomp(2) needs no_new_privs on the calling thread and that flag is
// per thread, so a goroutine that migrated between the prctl and the filter
// call made the whole fixture fail with EACCES, depending on scheduling.
func TestDenyFilterSurvivesGoroutineMigration(t *testing.T) {
	denyFilterArch(t)
	var header = unix.CapUserHeader{Version: unix.LINUX_CAPABILITY_VERSION_3}
	var caps [2]unix.CapUserData
	if err := unix.Capget(&header, &caps[0]); err != nil {
		t.Fatal(err)
	}
	if caps[0].Effective&(1<<unix.CAP_SYS_ADMIN) != 0 {
		t.Skip("CAP_SYS_ADMIN lets seccomp(2) run without no_new_privs, so the control cannot fail")
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	run := func(mode string) (string, error) {
		ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
		defer cancel()
		probe := exec.CommandContext(ctx, executable, "-test.run=^TestNoOutboundServerHelper$")
		probe.Env = []string{"AEON_NO_OUTBOUND_TEST=" + mode, "GODEBUG=netdns=go", "GOMAXPROCS=1"}
		out, err := probe.CombinedOutput()
		return string(out), err
	}
	// The negative control proves the forced hop reaches a thread without the
	// flag, so a pass below cannot come from a goroutine that never moved.
	out, err := run("migrate-unpinned")
	if err == nil || !strings.Contains(out, "errno=13") || strings.Contains(out, "filter installed") {
		t.Fatalf("unpinned control did not fail with EACCES: %v\n%s", err, out)
	}
	out, err = run("migrate-pinned")
	if err != nil || !strings.Contains(out, "filter installed") {
		t.Fatalf("pinned filter install failed after a forced scheduling round: %v\n%s", err, out)
	}
}

func TestDefaultServerHasNoOutboundNetwork(t *testing.T) {
	if runtime.GOARCH != "amd64" && runtime.GOARCH != "arm64" {
		t.Skip("network-denial fixture supports Linux amd64 and arm64")
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	// Value-free negative controls prove that attempts are observable failures,
	// including DNS, rather than silently refused calls the server can ignore.
	for _, mode := range []string{"connect", "dns"} {
		probe := exec.CommandContext(t.Context(), executable, "-test.run=^TestNoOutboundServerHelper$")
		probe.Env = []string{"AEON_NO_OUTBOUND_TEST=" + mode, "GODEBUG=netdns=go", "GOMAXPROCS=2"}
		out, err := probe.CombinedOutput()
		if err == nil || !bytes.Contains(out, []byte("SIGSYS")) {
			t.Fatalf("%s negative control did not trap: %v\n%s", mode, err, out)
		}
	}
	fresh := dbtest.Open(t)
	u, err := url.Parse(fresh.AppURL)
	if err != nil {
		t.Fatal(err)
	}
	socketDir := t.TempDir()
	proxy, err := net.Listen("unix", filepath.Join(socketDir, ".s.PGSQL.5432"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = proxy.Close() })
	go func() {
		for {
			local, err := proxy.Accept()
			if err != nil {
				return
			}
			go func() {
				defer local.Close()
				upstream, err := net.DialTimeout("tcp", u.Host, 5*time.Second)
				if err != nil {
					return
				}
				defer upstream.Close()
				go func() { _, _ = io.Copy(upstream, local); _ = upstream.Close() }()
				_, _ = io.Copy(local, upstream)
			}()
		}
	}()
	database := *u
	database.Host = ""
	query := database.Query()
	query.Set("host", socketDir)
	query.Set("port", "5432")
	query.Set("sslmode", "disable")
	database.RawQuery = query.Encode()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	file, err := ln.(*net.TCPListener).File()
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	cmd := exec.Command(executable, "-test.run=^TestNoOutboundServerHelper$")
	cmd.ExtraFiles = []*os.File{file}
	cmd.Env = []string{
		"AEON_NO_OUTBOUND_TEST=server", "GODEBUG=netdns=go", "GOMAXPROCS=2",
		"AEON_ENV=dev", "AEON_PUBLIC_URL=http://127.0.0.1",
		"AEON_DATABASE_URL=" + database.String(), "HOME=" + t.TempDir(),
	}
	var logs bytes.Buffer
	cmd.Stdout, cmd.Stderr = &logs, &logs
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	t.Cleanup(func() {
		_ = cmd.Process.Signal(syscall.SIGTERM)
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("server failed under network denial: %v", err)
			}
		case <-time.After(20 * time.Second):
			_ = cmd.Process.Kill()
			<-done
			t.Error("server did not shut down")
		}
	})
	client := &http.Client{Timeout: time.Second}
	base := "http://" + ln.Addr().String()
	deadline := time.Now().Add(20 * time.Second)
	for {
		resp, err := client.Get(base + "/api/ready")
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				break
			}
		}
		if time.Now().After(deadline) {
			t.Fatal("server did not become ready under network denial")
		}
		time.Sleep(20 * time.Millisecond)
	}
	// Keep the full server and its scheduled workers running after startup;
	// exercise both public guide surfaces and health while all egress is denied.
	until := time.Now().Add(2 * time.Second)
	for time.Now().Before(until) {
		for _, path := range []string{"/api/health", "/api/agent-pairing/guide", "/agents/register-agent"} {
			resp, err := client.Get(base + path)
			if err != nil {
				t.Fatal("server stopped serving under network denial")
			}
			body, _ := io.ReadAll(resp.Body)
			_ = resp.Body.Close()
			if resp.StatusCode != http.StatusOK || strings.Contains(string(body), "homebrew_formula_current") {
				t.Fatalf("unexpected response under denial: %s (%d)", path, resp.StatusCode)
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
}
