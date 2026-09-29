// SPDX-License-Identifier: AGPL-3.0-only

package piprobe

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestProviderRPC(t *testing.T) {
	for _, tc := range []struct{ name, state, models, expected, want string }{
		{"configured", `{"model":{"provider":"anthropic","id":"test"}}`, `{"models":[{"provider":"anthropic","id":"test"}]}`, "", "anthropic"},
		{"explicit provider", `{"model":{"provider":"openai","id":"other"}}`, `{"models":[{"provider":"anthropic","id":"test"}]}`, "anthropic", "anthropic"},
		{"not signed in", `{"model":{"provider":"anthropic","id":"test"}}`, `{"models":[]}`, "", ""},
		{"unconfigured model", `{"model":{"provider":"anthropic","id":"missing"}}`, `{"models":[{"provider":"anthropic","id":"test"}]}`, "", ""},
		{"missing state", `{}`, `{"models":[]}`, "", ""},
		{"wrong provider", `{"model":{"provider":"openai","id":"test"}}`, `{"models":[{"provider":"openai","id":"test"}]}`, "anthropic", ""},
		{"unsafe provider", `{"model":{"provider":"private diagnostic","id":"test"}}`, `{"models":[{"provider":"private diagnostic","id":"test"}]}`, "", ""},
		{"malformed", `null`, `invalid`, "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home, err := filepath.EvalSymlinks(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(home, 0700); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(home, "pi")
			// The fake asserts isolation and permits exactly the two read RPCs.
			script := `#!/bin/sh
[ "$PWD" = / ] || exit 1
[ -z "$ANTHROPIC_API_KEY" ] && [ -z "$NODE_OPTIONS" ] || exit 2
[ "$*" = '--mode rpc --no-session --no-tools --no-extensions --no-skills --no-prompt-templates --no-themes' ] || exit 3
IFS= read -r line
[ "$line" = '{"id":"get_state","type":"get_state"}' ] || exit 4
printf '%s\n' '{"id":"get_state","type":"response","command":"get_state","success":true,"data":` + tc.state + `}'
IFS= read -r line
[ "$line" = '{"id":"get_available_models","type":"get_available_models"}' ] || exit 5
printf '%s\n' '{"id":"get_available_models","type":"response","command":"get_available_models","success":true,"data":` + tc.models + `}'
`
			if err := os.WriteFile(path, []byte(script), 0700); err != nil {
				t.Fatal(err)
			}
			t.Setenv("ANTHROPIC_API_KEY", "synthetic-not-a-credential")
			t.Setenv("NODE_OPTIONS", "synthetic-not-an-option")
			got, err := Provider(t.Context(), path, home, tc.expected, "")
			if got != tc.want || (err != nil) != (tc.want == "") {
				t.Fatalf("provider=%q error=%v", got, err)
			}
			if tc.want == "" {
				wantErr := ErrProviderUnavailable
				if tc.name == "malformed" {
					wantErr = ErrStart
				}
				if !errors.Is(err, wantErr) {
					t.Fatal("probe failure classification changed", err)
				}
			}
			if err != nil && strings.Contains(err.Error(), "private diagnostic") {
				t.Fatal("RPC data leaked")
			}
		})
	}
}

func TestProviderRefusesUnpinnedAndBoundsCancellation(t *testing.T) {
	home, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(home, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(home, "pi")
	if err := os.WriteFile(path, []byte("#!/bin/sh\nwhile IFS= read -r line; do :; done\n"), 0700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(home, "alias")
	if err := os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	if _, err := Provider(t.Context(), link, home, "", ""); !errors.Is(err, ErrStart) {
		t.Fatal("accepted unpinned executable")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	if _, err := Provider(ctx, path, home, "", ""); !errors.Is(err, ErrStart) {
		t.Fatal("accepted silent RPC")
	}
	if time.Since(start) > 2*time.Second {
		t.Fatal("cancellation did not stop owned probe")
	}
	if err := os.Chmod(home, 0755); err != nil {
		t.Fatal(err)
	}
	if _, err := Provider(t.Context(), path, home, "", ""); !errors.Is(err, ErrPrivateProfile) {
		t.Fatal("public profile accepted")
	}
}
