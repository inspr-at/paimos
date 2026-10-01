// SPDX-License-Identifier: AGPL-3.0-only

package cli

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestHarnessVersionProbeIsBoundedAndOptional(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("PATH", dir)
	path := filepath.Join(dir, "codex")
	for _, tc := range []struct{ output, want string }{
		{"codex-cli 1.2.3", "1.2.3"}, {"1.2.3-beta.1", "1.2.3-beta.1"},
		{"diagnostic text", ""}, {"codex-cli 1.2.3\nextra line", ""}, {strings.Repeat("x", 5000), ""},
	} {
		body := "#!/bin/sh\n[ \"$1\" = --version ] || exit 1\ncat <<'VERSION'\n" + tc.output + "\nVERSION\n"
		body = strings.Replace(body, "cat <<", "/bin/cat <<", 1)
		if err := os.WriteFile(path, []byte(body), 0700); err != nil {
			t.Fatal(err)
		}
		if got := harnessVersionOrProbe(context.Background(), "codex", ""); got != tc.want {
			t.Fatalf("version length=%d got=%q want=%q", len(tc.output), got, tc.want)
		}
	}
	if got := harnessVersionOrProbe(context.Background(), "cursor", ""); got != "" {
		t.Fatal("missing binary produced a version")
	}
	if got := harnessVersionOrProbe(context.Background(), "codex", "supplied"); got != "supplied" {
		t.Fatal("explicit version was lost")
	}
	if err := os.WriteFile(path, []byte("#!/bin/sh\nexec /bin/sleep 5\n"), 0700); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	started := time.Now()
	if got := harnessVersionOrProbe(ctx, "codex", ""); got != "" || time.Since(started) > time.Second {
		t.Fatal("slow version probe blocked registration")
	}
}

func TestHeartbeatRegistersHarnessVersion(t *testing.T) {
	dir := t.TempDir()
	var calls []hbCall
	srv := hbServer(t, &calls, nil)
	defer srv.Close()
	rt, _, _ := heartbeatRuntime(t, srv)
	path := filepath.Join(dir, "codex")
	if err := os.WriteFile(path, []byte("#!/bin/sh\nprintf 'codex-cli 1.2.3\\n'\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	o := heartbeatTestOptions(dir)
	o.Harness = "codex"
	openUsageSession(t, rt, o)
	var got any
	for _, call := range calls {
		if call.method == "POST" && strings.HasSuffix(call.path, "/harness-sessions") {
			got = call.body["harness_version"]
		}
	}
	if got != "1.2.3" {
		t.Fatalf("registered version = %v", got)
	}
}
