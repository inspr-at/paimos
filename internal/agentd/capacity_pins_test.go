// SPDX-License-Identifier: AGPL-3.0-only
package agentd

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
)

// The copied native test executable receives the real capacity argv, without a
// shell, interpreter, test flags or extra environment variables. Intercept only
// this private fixture name/argv before the testing package parses CLI flags.
func init() {
	if len(os.Args) != 4 || filepath.Base(os.Args[0]) != "aeon-capacity-native-fixture" ||
		os.Args[1] != "app-server" || os.Args[2] != "--listen" || os.Args[3] != "stdio://" {
		return
	}
	raw, err := json.Marshal(struct {
		Args []string
		Env  []string
	}{os.Args, os.Environ()})
	if err != nil || os.WriteFile(filepath.Join(os.Getenv("CODEX_HOME"), "launch.json"), raw, 0600) != nil {
		os.Exit(20)
	}
	runFakeCodexIdleProcess()
	os.Exit(0)
}

func fixtureCapacityPin(harness, path string, raw []byte, version string, helpers ...capacityHelperPin) *capacityPin {
	return &capacityPin{Harness: harness, Path: path, SHA256: sha256Hex(raw), Version: version, Helpers: helpers}
}

func nativeCapacityFixture(t *testing.T) capacityPin {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	src, err := os.Open(exe)
	if err != nil {
		t.Fatal(err)
	}
	defer src.Close()
	path := filepath.Join(privateCapacityHome(t), "aeon-capacity-native-fixture")
	dst, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0700)
	if err != nil {
		t.Fatal(err)
	}
	h := sha256.New()
	_, err = io.Copy(io.MultiWriter(dst, h), src)
	closeErr := dst.Close()
	if err != nil || closeErr != nil {
		t.Fatal("copy native fixture", err, closeErr)
	}
	return capacityPin{Harness: Codex, Version: "synthetic-native-1514", Path: path, SHA256: hex.EncodeToString(h.Sum(nil))}
}

func assertNativeCapacityLaunch(t *testing.T, home, path string) {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(home, "launch.json"))
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Args []string
		Env  []string
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	wantArgs := []string{path, "app-server", "--listen", "stdio://"}
	wantEnv := []string{"HOME=" + home, "CODEX_HOME=" + home,
		"PATH=" + filepath.Dir(path) + string(filepath.ListSeparator) + "/usr/bin:/bin", "LANG=C", "LC_ALL=C"}
	sort.Strings(got.Env)
	sort.Strings(wantEnv)
	if !reflect.DeepEqual(got.Args, wantArgs) || !reflect.DeepEqual(got.Env, wantEnv) {
		t.Fatal("capacity did not directly execute its pinned path/argv with exactly the minimal environment")
	}
	if _, err := os.Stat(filepath.Join(home, "unexpected-spawn")); !os.IsNotExist(err) {
		t.Fatal("enrolled wrapper or Node was started", err)
	}
}

func refusingCapacityExecutable(t *testing.T, home, name string) string {
	t.Helper()
	path := filepath.Join(privateCapacityHome(t), name)
	raw := []byte("#!/bin/sh\nprintf invoked > '" + filepath.Join(home, "unexpected-spawn") + "'\nexit 89\n")
	if err := os.WriteFile(path, raw, 0700); err != nil {
		t.Fatal(err)
	}
	return path
}

func assertNoCapacitySpawn(t *testing.T, home string) {
	t.Helper()
	for _, name := range []string{"unexpected-spawn", "launch.json"} {
		if _, err := os.Stat(filepath.Join(home, name)); !os.IsNotExist(err) {
			t.Fatal("unqualified executable or enrolled wrapper was started", name, err)
		}
	}
}

// Risk: metadata changes, a wrapper substitution, or changed bytes enable a
// process before qualification. Every refusal must retain today's reason and
// leave both the selected executable's and wrapper's spawn marker absent.
func TestCapacityPinsRejectUnqualifiedExecutables(t *testing.T) {
	for _, boundary := range []string{"no-pin", "wrapper", "digest", "version", "native-node", "helper", "changed-file"} {
		t.Run(boundary, func(t *testing.T) {
			home := privateCapacityHome(t)
			pin := nativeCapacityFixture(t)
			path := pin.Path
			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			a := NewCodexAdapter(refusingCapacityExecutable(t, home, "wrapper"), map[string]string{"one": home})
			a.SetExpectedEmails(map[string]string{"one": "fixture@example.test"})
			a.idleUsage = loadCodexCapacityPin([]capacityPin{pin})
			if a.idleUsage == nil || !a.CanCaptureCapacity("one") {
				t.Fatal("matching synthetic pin did not qualify")
			}
			c := a.idleUsage
			switch boundary {
			case "no-pin":
				c.pin = nil
			case "wrapper":
				c.binaryPath = a.Path
			case "digest":
				c.binarySHA256 = strings.Repeat("0", 64)
			case "version":
				c.version = "different-version"
			case "native-node":
				c.nodePath, c.nodeSHA256 = path, pin.SHA256
			case "helper":
				c.pin.Helpers = []capacityHelperPin{{Name: "helper", Path: path, SHA256: strings.Repeat("0", 64)}}
			case "changed-file":
				if err := os.WriteFile(path, append(raw, '#'), 0700); err != nil {
					t.Fatal(err)
				}
			}
			want := CapacityCapture{Result: "unsupported"}
			if got := a.CaptureCapacityResult(t.Context(), "one"); !reflect.DeepEqual(got, want) || a.CanCaptureCapacity("one") ||
				a.CapacitySupport("one") != "not available idle: reading starts with a run" {
				t.Fatalf("qualification refusal changed: %+v", got)
			}
			assertNoCapacitySpawn(t, home)
		})
	}
	t.Run("wrapper-entry", func(t *testing.T) {
		home := privateCapacityHome(t)
		path := refusingCapacityExecutable(t, home, "wrapper")
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if c := loadCodexCapacityPin([]capacityPin{*fixtureCapacityPin(Codex, path, raw, "synthetic-wrapper")}); c != nil {
			t.Fatal("a script wrapper was accepted as the native capacity executable")
		}
		assertNoCapacitySpawn(t, home)
	})
}

// Risk: loading the production table enables a harness accidentally or changes
// its published unsupported cause. Exercise the supervisor's real result path,
// including harnesses without a CaptureCapacityResult method.
func TestProductionCapacityPinsKeepUnsupportedResults(t *testing.T) {
	if len(productionCapacityPins()) != 0 {
		t.Fatal("production idle entries require separate OPS evidence")
	}
	home := privateCapacityHome(t)
	path := refusingCapacityExecutable(t, home, "wrapper")
	homes := map[string]string{"local": home}
	codex := NewCodexAdapter(path, homes)
	codex.SetExpectedEmails(map[string]string{"local": "fixture@example.test"})
	grok := NewGrokAdapter(map[string]GrokBinding{"local": {BinaryPath: path, PrincipalSHA256: "fixture"}})
	grok.Homes = homes
	for _, tc := range []struct {
		adapter Adapter
		reason  string
	}{
		{codex, "not available idle: reading starts with a run"},
		{NewClaudeAdapter(path, path, path, homes), "not available idle: reading starts with a run"},
		{grok, "not available: Grok billing capability unverified"},
		{NewCursorAdapter(path, nil), "not available headless"},
		{NewPiAdapter(path, homes), "not available: provider measurement unsupported"},
		{NewGeminiAdapter(path, homes), "not available: vendor quota-neutral capacity API unqualified"},
		{NewOpenCodeAdapter(path, homes), "not available: vendor quota-neutral capacity API unqualified"},
	} {
		t.Run(tc.adapter.Name(), func(t *testing.T) {
			s, api, _, clock := checkFixture(t)
			local := s.accounts[0]
			local.Harness = tc.adapter.Name()
			api.account.Harness = local.Harness
			s.captureCapacityCheck(t.Context(), clock.Now(), local, api.account, tc.adapter, api)
			want := []CapacityCheckReport{{Result: "unsupported"}}
			if !reflect.DeepEqual(api.reports, want) {
				t.Fatalf("unsupported result changed: %+v", api.reports)
			}
			if got := tc.adapter.(interface{ CapacitySupport(string) string }).CapacitySupport("local"); got != tc.reason {
				t.Fatalf("unsupported reason changed: %q", got)
			}
			if c, ok := tc.adapter.(interface {
				CaptureCapacityResult(context.Context, string) CapacityCapture
			}); ok {
				if got := c.CaptureCapacityResult(t.Context(), "local"); !reflect.DeepEqual(got, CapacityCapture{Result: "unsupported"}) {
					t.Fatalf("unsupported capture changed: %+v", got)
				}
			}
			assertNoCapacitySpawn(t, home)
		})
	}
}
