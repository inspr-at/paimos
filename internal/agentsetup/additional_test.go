// SPDX-License-Identifier: AGPL-3.0-only
package agentsetup

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

type profileProbe struct{ calls []Command }

func (p *profileProbe) Run(_ context.Context, c Command) ([]byte, error) {
	p.calls = append(p.calls, c)
	if len(c.Args) == 1 && c.Args[0] == "--version" {
		return []byte("1.14.48\n"), nil
	}
	return nil, errors.New("no authentication or model calls permitted")
}

func TestAdditionalHarnessEnrollment(t *testing.T) {
	for _, name := range []string{"gemini", "opencode"} {
		for _, operation := range []string{"Begin", "AddHarness"} {
			t.Run(name+"/"+operation, func(t *testing.T) {
				e, api, local, opts, _ := engineFixture(t)
				home := opts.Candidates[0].Home
				if err := os.Chmod(home, 0700); err != nil {
					t.Fatal(err)
				}
				probe := &profileProbe{}
				discovery := Discovery{Home: home, Workspace: opts.Workspace, Executor: probe, LookPath: func(string) (string, error) { return opts.Candidates[0].Path, nil }}
				candidate, err := discovery.Detect(t.Context(), name, "local-profile")
				if err != nil || candidate.Login != "unverified" || candidate.Identity != "local-profile" || candidateReady(candidate) {
					t.Fatal("explicit profile discovery failed")
				}
				accountID, requests, accounts := testAccount, 1, 1
				var pending Progress
				if operation == "AddHarness" {
					approveFixture(t, e, api, opts)
					api.approved = false
					accountID, requests, accounts = otherAccount, 2, 2
					pending, err = e.AddHarness(t.Context(), []Candidate{candidate})
				} else {
					opts.Candidates = []Candidate{candidate}
					pending, err = e.Begin(t.Context(), opts)
				}
				if err != nil || pending.Stage != "awaiting_approval" || api.createCount != requests || len(api.request.Accounts) != 1 || api.request.Accounts[0].Harness != name || api.request.Accounts[0].Label != candidate.Label {
					t.Fatalf("%s did not request approval: stage=%s requests=%d err=%v", operation, pending.Stage, api.createCount, err)
				}
				raw, err := e.Store.Read(RuntimeName, 128<<10)
				var runtime RuntimeConfig
				if operation == "Begin" {
					if !errors.Is(err, os.ErrNotExist) {
						t.Fatal("runtime provisioned before person approval")
					}
				} else if err != nil || json.Unmarshal(raw, &runtime) != nil || len(runtime.Accounts) != 1 || runtime.Accounts[0].Harness != "codex" {
					t.Fatal("pending AddHarness changed existing runtime")
				}
				blocked := HarnessDetail{State: "blocked", Reason: "sign_in_unverified"}
				local.states[""] = LocalStatus{
					DaemonID: "paired-daemon", State: "drained",
					AccountStatuses: map[string]HarnessDetail{accountID: blocked},
					HarnessDetails:  map[string]HarnessDetail{name: blocked},
					HarnessStatuses: map[string]string{name: "blocked"},
				}
				if operation == "AddHarness" {
					status := local.states[""]
					status.Ready = true // The existing Codex sibling remains ready.
					status.AccountStatuses[testAccount] = HarnessDetail{State: "ready"}
					status.HarnessDetails["codex"] = HarnessDetail{State: "ready"}
					status.HarnessStatuses["codex"] = "ready"
					local.states[""] = status
				}
				api.approved = true
				e.Now = func() time.Time { return time.Now().Add(2 * time.Minute) }
				progress, err := e.Step(t.Context())
				if err != nil {
					t.Fatal(err)
				}
				if operation == "Begin" && progress.Stage != "blocked" {
					t.Fatalf("unverified profile reported ready: %s", progress.Stage)
				}
				if operation == "AddHarness" && progress.Stage != "connected" {
					t.Fatalf("ready sibling lost connectivity: %s", progress.Stage)
				}
				detail := progress.HarnessDetails[name]
				if detail.State != blocked.State || detail.Reason != blocked.Reason || progress.HarnessStatuses[name] != "blocked" {
					t.Fatal("unverified profile lost its blocked sign-in reason")
				}
				raw, err = e.Store.Read(RuntimeName, 128<<10)
				if err != nil || json.Unmarshal(raw, &runtime) != nil || len(runtime.Accounts) != accounts {
					t.Fatal("approved profile missing from runtime")
				}
				bound := runtime.Accounts[accounts-1]
				if bound.Harness != name || bound.Identity != "local-profile" || bound.Home != candidate.Home || bound.Path != candidate.Path {
					t.Fatal("explicit profile binding changed")
				}
				saved, err := e.SavedOptions()
				if err != nil || len(saved.Candidates) != accounts {
					t.Fatal("saved profile missing")
				}
				choice := saved.Candidates[accounts-1]
				if choice.Login != "unverified" || choice.Identity != "local-profile" || candidateReady(choice) || len(probe.calls) != 1 {
					t.Fatal("enrollment invented sign-in or called a vendor model")
				}
			})
		}
	}
}

func TestAdditionalHarnessDiscoveryIsExplicitLocalProfile(t *testing.T) {
	for _, name := range []string{"gemini", "opencode"} {
		t.Run(name, func(t *testing.T) {
			home := physicalTemp(t)
			if err := os.Chmod(home, 0700); err != nil {
				t.Fatal(err)
			}
			workspace := physicalTemp(t)
			path := filepath.Join(home, name)
			if err := os.WriteFile(path, []byte("#!/bin/sh\nexit 0\n"), 0700); err != nil {
				t.Fatal(err)
			}
			probe := &profileProbe{}
			d := Discovery{Home: home, Workspace: workspace, Executor: probe, LookPath: func(string) (string, error) { return path, nil }}
			c, err := d.Detect(t.Context(), name, "")
			if err != nil || c.Home != home || c.Login != "unverified" || c.Identity != "local-profile" || candidateReady(c) || len(probe.calls) != 1 {
				t.Fatal("profile discovery", c, err, len(probe.calls))
			}
			if _, err := d.Detect(t.Context(), name, "person@example.test"); err == nil {
				t.Fatal("person identity invented")
			}
			c.Login = "signed_out"
			if candidateReady(c) {
				t.Fatal("signed-out profile enrolled")
			}
			c.Login = "local_profile"
			if candidateReady(c) {
				t.Fatal("legacy version-only profile enrolled as ready")
			}
		})
	}
}

func TestAdditionalHarnessEnrollmentRejectsUnverifiedAccountClaims(t *testing.T) {
	for _, name := range []string{"gemini", "opencode"} {
		for _, operation := range []string{"Begin", "AddHarness"} {
			t.Run(name+"/"+operation, func(t *testing.T) {
				e, api, _, opts, _ := engineFixture(t)
				requests := 0
				if operation == "AddHarness" {
					approveFixture(t, e, api, opts)
					requests = 1
				}
				for _, claim := range []struct{ harness, login, identity string }{
					{name, "unverified", ""},
					{name, "unverified", "person@example.test"},
					{name, "signed_out", "local-profile"},
					{name, "local_profile", "local-profile"},
					{"codex", "unverified", "local-profile"},
				} {
					candidate := opts.Candidates[0]
					candidate.Harness, candidate.Login, candidate.Identity = claim.harness, claim.login, claim.identity
					var err error
					if operation == "Begin" {
						invalid := opts
						invalid.Candidates = []Candidate{candidate}
						_, err = e.Begin(t.Context(), invalid)
					} else {
						_, err = e.AddHarness(t.Context(), []Candidate{candidate})
					}
					if err == nil || api.createCount != requests {
						t.Fatalf("unverified claim enrolled: harness=%s login=%s identity=%s", claim.harness, claim.login, claim.identity)
					}
				}
			})
		}
	}
}
