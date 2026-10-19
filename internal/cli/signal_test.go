// SPDX-License-Identifier: AGPL-3.0-only

package cli

import (
	"bytes"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

func TestSignalEndsBlockedCommand(t *testing.T) {
	if os.Getenv("AEON_SIGNAL_CHILD") == "1" {
		runSignalChild()
		return
	}
	ready := filepath.Join(t.TempDir(), "ready")
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(exe, "-test.run=^TestSignalEndsBlockedCommand$", "-test.count=1", "-test.timeout=30s")
	cmd.Env = append(os.Environ(), "AEON_SIGNAL_CHILD=1", "AEON_SIGNAL_READY="+ready)
	var output bytes.Buffer
	cmd.Stdout = &output
	cmd.Stderr = &output
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
	})
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(ready); err == nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if _, err := os.Stat(ready); err != nil {
		t.Fatalf("blocked command never reached the request: %s", output.String())
	}
	if err := cmd.Process.Signal(syscall.SIGINT); err != nil {
		t.Fatal(err)
	}
	waited := make(chan error, 1)
	go func() { waited <- cmd.Wait() }()
	select {
	case err := <-waited:
		var exit *exec.ExitError
		if !errors.As(err, &exit) || exit.ExitCode() != 130 || !strings.Contains(output.String(), "interrupted") {
			t.Fatalf("signal did not end the command: %v\n%s", err, output.String())
		}
	case <-time.After(5 * time.Second):
		t.Fatal("signal left the command running")
	}
}

func runSignalChild() {
	ready := os.Getenv("AEON_SIGNAL_READY")
	var once sync.Once
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		once.Do(func() { _ = os.WriteFile(ready, []byte("1"), 0o600) })
		<-r.Context().Done()
	}))
	defer srv.Close()
	_ = os.Setenv("AEON_URL", srv.URL)
	_ = os.Setenv("AEON_API_KEY", "fixture-signal-key")
	_ = os.Unsetenv("AEON_API_KEY_FILE")
	var stdout, stderr bytes.Buffer
	code := runMessaging([]string{"aeon", "whoami"}, strings.NewReader(""), &stdout, &stderr, nil)
	fmt.Fprint(os.Stderr, stderr.String())
	os.Exit(code)
}
