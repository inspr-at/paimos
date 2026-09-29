// SPDX-License-Identifier: AGPL-3.0-only

package agentsetup

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPiDiscovery(t *testing.T) {
	for _, scenario := range []string{"found", "missing", "not signed in", "unpinnable", "not executable", "invalid version", "wrong context"} {
		t.Run(scenario, func(t *testing.T) {
			home := physicalTemp(t)
			path := filepath.Join(home, "pi")
			if err := os.WriteFile(path, []byte("fixture"), 0700); err != nil {
				t.Fatal(err)
			}
			if scenario == "not executable" {
				if err := os.Chmod(path, 0600); err != nil {
					t.Fatal(err)
				}
			}
			probes := 0
			d := Discovery{Home: home, LookPath: func(name string) (string, error) {
				if name != "pi" {
					t.Fatal("wrong executable")
				}
				if scenario == "missing" {
					return "", os.ErrNotExist
				}
				if scenario == "unpinnable" {
					return "pi-does-not-exist", nil
				}
				return path, nil
			}, Executor: executorFunc(func(_ context.Context, c Command) ([]byte, error) {
				if c.Path != path || strings.Join(c.Args, " ") != "--version" {
					t.Fatal("unexpected discovery command")
				}
				if scenario == "invalid version" {
					return []byte("private diagnostic"), nil
				}
				return []byte("pi 0.60.0"), nil
			}), PiProvider: func(_ context.Context, executable, profile, expected string) (string, error) {
				probes++
				if executable != path || profile != filepath.Join(home, ".pi", "agent") || expected != "anthropic" {
					t.Fatal("probe lost account binding")
				}
				if scenario == "not signed in" {
					return "", errors.New("private diagnostic")
				}
				if scenario == "wrong context" {
					return "openai", nil
				}
				return "anthropic", nil
			}}
			c, err := d.Detect(t.Context(), "pi", "anthropic")
			if scenario != "found" {
				if err == nil || c.Login == "signed_in" {
					t.Fatal("unsafe discovery succeeded")
				}
				if strings.Contains(err.Error(), "private diagnostic") {
					t.Fatal("vendor diagnostic escaped")
				}
				return
			}
			if err != nil || c.Identity != "anthropic" || c.Provider != "anthropic" || c.Login != "signed_in" || c.Version != "0.60.0" || probes != 1 {
				t.Fatal("provider discovery failed", err)
			}
			raw, _ := json.Marshal(c)
			if strings.Contains(string(raw), home) || strings.Contains(string(raw), "identity") || !strings.Contains(string(raw), "local profile") {
				t.Fatal("public candidate contains private binding or misleading identity")
			}
		})
	}
}

func TestPiSetupAndAddHarnessPersistPrivateBinding(t *testing.T) {
	for _, add := range []bool{false, true} {
		t.Run(map[bool]string{false: "setup", true: "add-harness"}[add], func(t *testing.T) {
			e, api, _, o, _ := engineFixture(t)
			defer e.Store.Close()
			pi := Candidate{Harness: "pi", Provider: "anthropic", Label: "pi / anthropic (local profile)", Identity: "anthropic", Path: o.Candidates[0].Path, Home: o.Candidates[0].Home, Login: "signed_in", Version: "0.60.0"}
			if add {
				approveFixture(t, e, api, o)
				api.approved = false
				if _, err := e.AddHarness(t.Context(), []Candidate{pi}); err != nil {
					t.Fatal(err)
				}
			} else {
				o.Candidates = []Candidate{pi}
				if _, err := e.Begin(t.Context(), o); err != nil {
					t.Fatal(err)
				}
			}
			api.approved = true
			s, err := e.load()
			if err != nil {
				t.Fatal(err)
			}
			s.NextPoll = e.now()
			if err := e.save(s, false); err != nil {
				t.Fatal(err)
			}
			if p, err := e.Step(t.Context()); err != nil || p.Stage != "connected" {
				t.Fatal("pi pairing failed", err)
			}
			config, err := ReadRuntimeConfig(e.Store.Path())
			if err != nil {
				t.Fatal(err)
			}
			found := false
			for _, account := range config.Accounts {
				if account.Harness == "pi" {
					found = account.Identity == "anthropic" && account.Home == pi.Home && account.Path == pi.Path
				}
			}
			if !found {
				t.Fatal("runtime lost pi provider/profile binding")
			}
		})
	}
}

func TestServerSelectedProfileKeepsEveryOtherChoiceBound(t *testing.T) {
	local := []Candidate{{Key: "local", Harness: "pi", Provider: "anthropic", Label: "pi / anthropic (local profile)"}}
	remote := append([]Candidate(nil), local...)
	remote[0].ProfileID = testAccount
	if !sameChoices(remote, local) || local[0].ProfileID != "" {
		t.Fatal("valid server default not accepted without mutating choices")
	}
	for _, change := range []func(*Candidate){
		func(c *Candidate) { c.Provider = "openai" },
		func(c *Candidate) { c.Harness = "codex" },
		func(c *Candidate) { c.Label = "other" },
		func(c *Candidate) { c.Key = "other" },
		func(c *Candidate) { c.ProfileID = "invalid" },
	} {
		changed := append([]Candidate(nil), remote...)
		change(&changed[0])
		if sameChoices(changed, local) {
			t.Fatal("default profile widened another choice")
		}
	}
	local[0].ProfileID = otherAccount
	if sameChoices(remote, local) {
		t.Fatal("pinned profile was replaced")
	}
}
