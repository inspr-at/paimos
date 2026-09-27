// SPDX-License-Identifier: AGPL-3.0-only
//go:build darwin

package agentsetup

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/grokprobe"
)

func TestGuidedGrokBindingStaysPrivateThroughEngine(t *testing.T) {
	if runtime.GOARCH != "arm64" {
		t.Skip("native Grok pins qualify arm64")
	}
	home := physicalTemp(t)
	binary := filepath.Join(home, "node_modules", "@xai-official", "grok", "bin", "grok-native")
	if err := os.MkdirAll(filepath.Dir(binary), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(binary, []byte("synthetic executable"), 0700); err != nil {
		t.Fatal(err)
	}
	const principal = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	probe := func(_ context.Context, b grokprobe.Binding) (grokprobe.Identity, error) {
		if b.PrincipalSHA256 != "" && b.PrincipalSHA256 != principal {
			return grokprobe.Identity{}, errors.New("principal mismatch")
		}
		b.PrincipalSHA256 = principal
		return grokprobe.Identity{Binding: b, Label: "verified@example.test"}, nil
	}
	d := Discovery{Home: home, LookPath: func(name string) (string, error) {
		if name != "grok-native" {
			return "", errors.New("missing")
		}
		return binary, nil
	}, GrokProbe: probe}
	c, err := d.Detect(t.Context(), "grok", "verified@example.test")
	if err != nil || c.Login != "signed_in" || c.Grok.AuthPath != filepath.Join(home, ".grok", "auth.json") || c.Grok.PrincipalSHA256 != principal {
		t.Fatal("qualified Grok discovery lost private binding")
	}
	if _, err := d.Detect(t.Context(), "grok", "other@example.test"); err == nil {
		t.Fatal("selected identity mismatch accepted")
	}
	public, _ := json.Marshal(c)
	for _, private := range []string{binary, c.Grok.AuthPath, c.Grok.ScratchRoot, principal} {
		if strings.Contains(string(public), private) {
			t.Fatal("public candidate exposed private binding")
		}
	}
	e, api, _, o, _ := engineFixture(t)
	e.GrokProbe = probe
	o.Candidates = []Candidate{c}
	if _, err := e.Begin(t.Context(), o); err != nil {
		t.Fatal(err)
	}
	saved, err := e.SavedOptions()
	if err != nil || len(saved.Candidates) != 1 || saved.Candidates[0].Grok != c.Grok {
		t.Fatal("resume lost private Grok binding")
	}
	request, _ := json.Marshal(api.request)
	if strings.Contains(string(request), c.Grok.AuthPath) || strings.Contains(string(request), principal) {
		t.Fatal("device request exposed Grok binding")
	}
	api.approved = true
	e.Now = func() time.Time { return time.Now().Add(time.Minute) }
	if p, err := e.Step(t.Context()); err != nil || p.Stage != "connected" {
		t.Fatalf("Grok provisioning failed: %s %v", p.Stage, err)
	}
	runtime, err := ReadRuntimeConfig(e.Store.Path())
	if err != nil || len(runtime.Accounts) != 1 || runtime.Accounts[0].Grok != c.Grok {
		t.Fatal("runtime lost Grok binding")
	}
	progress, _ := json.Marshal(e.progress(&snapshot{Phase: "connected"}))
	if strings.Contains(string(progress), principal) || strings.Contains(string(progress), c.Grok.AuthPath) {
		t.Fatal("safe status exposed Grok binding")
	}
	// The saved expected principal is rechecked on resume, before runtime use.
	e.GrokProbe = func(context.Context, grokprobe.Binding) (grokprobe.Identity, error) {
		return grokprobe.Identity{}, errors.New("principal mismatch")
	}
	if _, err := e.Begin(t.Context(), o); err == nil {
		t.Fatal("resume accepted changed principal")
	}
}

func TestGuidedGrokRejectsWorkspaceScratchAndNonDarwin(t *testing.T) {
	e, _, _, o, _ := engineFixture(t)
	c := Candidate{Harness: "grok", Label: "Grok subject SHA-256: " + strings.Repeat("a", 64), Identity: strings.Repeat("a", 64), Path: "/qualified/grok-native", Home: "/home/test/.grok", Login: "signed_in", Grok: grokprobe.Binding{BinaryPath: "/qualified/grok-native", AuthPath: "/home/test/.grok/auth.json", ScratchRoot: filepath.Join(o.Workspace, "scratch"), PrincipalSHA256: strings.Repeat("a", 64)}}
	o.Candidates = []Candidate{c}
	if err := validateOptions(o); err == nil {
		t.Fatal("scratch inside approved workspace accepted")
	}
	o.Platform.OS = "linux"
	if err := validateOptions(o); err == nil {
		t.Fatal("non-Darwin Grok accepted")
	}
	_ = e
}

func TestAddHarnessCarriesGrokBindingAcrossApproval(t *testing.T) {
	if runtime.GOARCH != "arm64" {
		t.Skip("native Grok pins qualify arm64")
	}
	e, api, _, o, _ := engineFixture(t)
	approveFixture(t, e, api, o)
	principal := strings.Repeat("b", 64)
	b := grokprobe.Binding{Variant: "npm-grok-1.0.30", BinaryPath: "/synthetic/node_modules/@xai-official/grok/bin/grok-native", AuthPath: "/synthetic/.grok/auth.json", ScratchRoot: "/synthetic/grok-scratch", PrincipalSHA256: principal}
	c := Candidate{Harness: "grok", Label: "verified@example.test", Identity: principal, Path: b.BinaryPath, Home: "/synthetic/.grok", Login: "signed_in", Grok: b}
	e.GrokProbe = func(_ context.Context, observed grokprobe.Binding) (grokprobe.Identity, error) {
		if observed != b {
			return grokprobe.Identity{}, errors.New("binding changed")
		}
		return grokprobe.Identity{Binding: b, Label: c.Label}, nil
	}
	if _, err := e.AddHarness(t.Context(), []Candidate{c}); err != nil {
		t.Fatal(err)
	}
	s, err := e.load()
	if err != nil || len(s.Candidates) != 2 || s.Candidates[1].Candidate.Grok != b {
		t.Fatal("Add harness lost private binding")
	}
	api.approved = true
	e.Now = func() time.Time { return time.Now().Add(2 * time.Minute) }
	if p, err := e.Step(t.Context()); err != nil || p.Stage != "connected" {
		t.Fatalf("Add harness did not provision: %s %v", p.Stage, err)
	}
	runtime, err := ReadRuntimeConfig(e.Store.Path())
	if err != nil || len(runtime.Accounts) != 2 || runtime.Accounts[1].Grok != b {
		t.Fatal("Add harness runtime lost private binding")
	}
}

func TestGuidedGrokFallsThroughToQualifiedSourceVariant(t *testing.T) {
	if runtime.GOARCH != "arm64" {
		t.Skip("native Grok pins qualify arm64")
	}
	home := physicalTemp(t)
	npm := filepath.Join(home, "node_modules", "@xai-official", "grok", "bin", "grok-native")
	source := filepath.Join(home, "target", "release", "xai-grok-pager")
	for _, path := range []string{npm, source} {
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("synthetic"), 0700); err != nil {
			t.Fatal(err)
		}
	}
	d := Discovery{Home: home, LookPath: func(name string) (string, error) {
		if name == "grok-native" {
			return npm, nil
		}
		if name == "xai-grok-pager" {
			return source, nil
		}
		return "", errors.New("unexpected")
	}, GrokProbe: func(_ context.Context, b grokprobe.Binding) (grokprobe.Identity, error) {
		if b.Variant == "npm-grok-1.0.30" {
			return grokprobe.Identity{}, errors.New("native Grok binary hash mismatch")
		}
		b.PrincipalSHA256 = strings.Repeat("c", 64)
		return grokprobe.Identity{Binding: b, Label: "verified@example.test"}, nil
	}}
	c, err := d.Detect(t.Context(), "grok", "")
	if err != nil || c.Grok.Variant != "source-xai-grok-pager-1.0.32" || c.Path != source {
		t.Fatal("qualified source variant was not selected")
	}
}
