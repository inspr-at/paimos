// SPDX-License-Identifier: AGPL-3.0-only

package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

// Risk: an interrupt leaves a command that waits on the network running
// silently instead of ending it with a clear error.
func TestSignalEndsBlockedCommand(t *testing.T) {
	if os.Getenv("AEON_SIGNAL_CHILD") == "1" {
		runSignalChild()
		return
	}
	code, output := interruptChild(t, "TestSignalEndsBlockedCommand", "whoami")
	if code != 130 || !strings.Contains(output, "interrupted") {
		t.Fatalf("signal did not end the command: exit %d\n%s", code, output)
	}
}

// Risk: an interrupted listen --follow ends with exit status 0 and no message,
// so a wrapper cannot tell the interrupt from a clean finish.
func TestSignalInterruptedListenerReportsInterrupt(t *testing.T) {
	if os.Getenv("AEON_SIGNAL_CHILD") == "1" {
		runSignalChild()
		return
	}
	for _, mode := range []string{"listen-inbox", "listen-project"} {
		t.Run(mode, func(t *testing.T) {
			code, output := interruptChild(t, "TestSignalInterruptedListenerReportsInterrupt", mode)
			if code != 130 || !strings.Contains(output, "aeon: interrupted") {
				t.Fatalf("interrupted listener: exit %d\n%s", code, output)
			}
		})
	}
}

// interruptChild reruns test in a child process that blocks one CLI command on
// a fixture server, sends SIGINT once the server holds the request and returns
// the exit status and combined output. The child reports readiness on fd 3.
func interruptChild(t *testing.T, test, mode string) (int, string) {
	t.Helper()
	isolate(t)
	t.Setenv("HOME", t.TempDir())
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	readyR, readyW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer readyR.Close()
	cmd := exec.Command(exe, "-test.run=^"+test+"$", "-test.count=1", "-test.timeout=30s")
	cmd.Env = append(os.Environ(), "AEON_SIGNAL_CHILD=1", "AEON_SIGNAL_MODE="+mode)
	cmd.ExtraFiles = []*os.File{readyW}
	var output bytes.Buffer
	cmd.Stdout = &output
	cmd.Stderr = &output
	if err := cmd.Start(); err != nil {
		_ = readyW.Close()
		t.Fatal(err)
	}
	_ = readyW.Close()
	waited := make(chan error, 1)
	go func() { waited <- cmd.Wait() }()
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
	})
	ready := make(chan error, 1)
	go func() {
		var marker [5]byte
		_, err := io.ReadFull(readyR, marker[:])
		if err == nil && string(marker[:]) != "ready" {
			err = errors.New("unexpected readiness marker")
		}
		ready <- err
	}()
	select {
	case err := <-ready:
		if err != nil {
			_ = cmd.Process.Kill()
			<-waited
			t.Fatalf("command never reached the fixture request: %v\n%s", err, output.String())
		}
	case <-time.After(10 * time.Second):
		_ = cmd.Process.Kill()
		<-waited
		t.Fatalf("command never reached the fixture request\n%s", output.String())
	}
	if err := cmd.Process.Signal(syscall.SIGINT); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-waited:
		if err == nil {
			return 0, output.String()
		}
		var exit *exec.ExitError
		if !errors.As(err, &exit) {
			t.Fatalf("child wait: %v\n%s", err, output.String())
		}
		return exit.ExitCode(), output.String()
	case <-time.After(10 * time.Second):
		_ = cmd.Process.Kill()
		<-waited
		t.Fatalf("signal left the command running\n%s", output.String())
	}
	return 0, ""
}

func TestSignalContextDoesNotPoisonTheNextCommand(t *testing.T) {
	var out, errBuf bytes.Buffer
	rt := &runtime{program: "aeon", stdin: strings.NewReader(""), stdout: &out, stderr: &errBuf}
	if err := rt.execute([]string{"aeon", "--version"}); err != nil {
		t.Fatal(err)
	}
	if rt.requestContext != nil {
		t.Fatal("finished command left its signal context installed")
	}
	if err := rt.execute([]string{"aeon", "--version"}); err != nil {
		t.Fatalf("next command inherited a dead signal context: %v", err)
	}
}

func TestCallerCancelIsNotAnInterrupt(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	rt := &runtime{program: "aeon", requestContext: ctx, stdin: strings.NewReader(""), stdout: &bytes.Buffer{}, stderr: &bytes.Buffer{}}
	err := rt.fail(context.Canceled)
	exit, ok := err.(*exitError)
	if !ok || exit.code != 1 || !strings.Contains(err.Error(), "context canceled") || strings.Contains(err.Error(), "interrupted") {
		t.Fatalf("caller cancellation reported as an interrupt: %v", err)
	}
}

func TestWorkRuntimeKeepsTheCommandInterrupt(t *testing.T) {
	rt := &runtime{program: "aeon", stdin: strings.NewReader(""), stdout: &bytes.Buffer{}, stderr: &bytes.Buffer{}}
	work := rt.workRuntime(context.Background())
	rt.commandInterrupted.Store(true)
	err := work.fail(context.Canceled)
	exit, ok := err.(*exitError)
	if !ok || exit.code != 130 || err.Error() != "interrupted" {
		t.Fatalf("derived runtime lost the command interrupt: %v", err)
	}
	err = rt.fail(context.Canceled)
	exit, ok = err.(*exitError)
	if !ok || exit.code != 130 || err.Error() != "interrupted" {
		t.Fatalf("parent runtime lost the command interrupt: %v", err)
	}
	other := &runtime{program: "aeon", stdin: strings.NewReader(""), stdout: &bytes.Buffer{}, stderr: &bytes.Buffer{}}
	quiet := other.workRuntime(context.Background())
	err = quiet.fail(context.Canceled)
	exit, ok = err.(*exitError)
	if !ok || exit.code != 1 || !strings.Contains(err.Error(), "context canceled") || strings.Contains(err.Error(), "interrupted") {
		t.Fatalf("derived runtime invented an interrupt: %v", err)
	}
}

func TestSignalCancelReportsInterrupt(t *testing.T) {
	rt := &runtime{program: "aeon", stdin: strings.NewReader(""), stdout: &bytes.Buffer{}, stderr: &bytes.Buffer{}}
	rt.commandInterrupted.Store(true)
	err := rt.fail(context.Canceled)
	exit, ok := err.(*exitError)
	if !ok || exit.code != 130 || err.Error() != "interrupted" {
		t.Fatalf("signal cancellation: %v", err)
	}
	err = rt.fail(errors.New("api 500: unavailable"))
	if !strings.Contains(err.Error(), "api 500:") || strings.Contains(err.Error(), "interrupted") {
		t.Fatalf("unrelated failure became an interrupt: %v", err)
	}
}

func runSignalChild() {
	ready := os.NewFile(3, "ready")
	var once sync.Once
	hold := func(w http.ResponseWriter, r *http.Request) {
		once.Do(func() {
			_, _ = ready.Write([]byte("ready"))
			_ = ready.Close()
		})
		<-r.Context().Done()
	}
	// whoami blocks on its first request; listeners resolve the project and
	// caller first and block on the inbox long poll.
	mode := os.Getenv("AEON_SIGNAL_MODE")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case mode == "listen-project" && r.URL.Path == "/api/projects/lookup":
			_, _ = w.Write([]byte(`{"id":"` + transcriptProjectID + `"}`))
		case mode == "listen-project" && r.URL.Path == "/api/me":
			_, _ = w.Write([]byte(`{"principal":{"id":"` + transcriptProjectID + `","name":"worker"}}`))
		case mode != "whoami" && r.URL.Path != "/api/inbox/messages":
			http.NotFound(w, r)
		default:
			hold(w, r)
		}
	}))
	defer srv.Close()
	_ = os.Setenv("AEON_URL", srv.URL)
	_ = os.Setenv("AEON_API_KEY", "fixture-signal-key")
	args := []string{"aeon", "whoami"}
	switch mode {
	case "listen-inbox":
		args = []string{"aeon", "listen", "--inbox", "--follow"}
	case "listen-project":
		args = []string{"aeon", "listen", "--project", "AEON", "--follow"}
	}
	var stdout, stderr bytes.Buffer
	code := RunMessaging(args, strings.NewReader(""), &stdout, &stderr)
	fmt.Fprint(os.Stderr, stdout.String()+stderr.String())
	os.Exit(code)
}
