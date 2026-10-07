// SPDX-License-Identifier: AGPL-3.0-only

package agentd

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
)

func TestVerificationPreRoutePolicyAndStrictStartBinding(t *testing.T) {
	adapter := NewClaudeAdapter("/unused-node", "/unused-sdk", "/unused-claude", nil)
	r := verificationRequest(t)
	r.Run.RequestedAccountID = r.Run.AccountID
	r.Run.AccountID = ""
	if err := validQueuedExecutionMode(r.Run, adapter); err != nil {
		t.Fatal("valid unbound queue rejected before Route", err)
	}
	if p, err := adapter.Start(t.Context(), r, func(AdapterEvent) {}); p != nil || err == nil || !strings.Contains(err.Error(), "verification account binding") {
		t.Fatal("strict adapter.Start accepted an unbound account", err)
	}
	r.Run.AccountID = "other-account"
	if err := validExecutionMode(r.Run, adapter); err == nil {
		t.Fatal("strict adapter guard accepted another enrollment")
	}
	r.Run.AccountID = r.Run.RequestedAccountID
	if err := validExecutionMode(r.Run, adapter); err != nil {
		t.Fatal("complete route binding rejected", err)
	}
	for _, tc := range []struct {
		name string
		edit func(*Run)
	}{
		{"task", func(r *Run) { r.VerificationTask = "change files" }},
		{"duration missing", func(r *Run) { r.MaxDurationSeconds = nil }},
		{"duration zero", func(r *Run) { n := int64(0); r.MaxDurationSeconds = &n }},
		{"duration extended", func(r *Run) { n := int64(61); r.MaxDurationSeconds = &n }},
		{"policy", func(r *Run) { r.VerificationPolicy = "managed" }},
		{"mutation missing", func(r *Run) { r.RepositoryMutationAllowed = nil }},
		{"mutation enabled", func(r *Run) { yes := true; r.RepositoryMutationAllowed = &yes }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			changed := r.Run
			tc.edit(&changed)
			if validQueuedExecutionMode(changed, adapter) == nil || validExecutionMode(changed, adapter) == nil {
				t.Fatal("routing split weakened immutable verification policy")
			}
		})
	}
}

// These wrappers count attempted vendor boundaries without ever invoking a
// real probe or process. Capability comes from the production adapters; Grok
// additionally exercises the supported matrix's nonqualified Linux branch.
type verificationBoundaryCounter struct {
	Adapter
	qualified      bool
	probes, starts int
}

func (a *verificationBoundaryCounter) VerificationSupported() bool        { return a.qualified }
func (a *verificationBoundaryCounter) Probe(context.Context, string) bool { a.probes++; return true }
func (a *verificationBoundaryCounter) Start(context.Context, StartRequest, func(AdapterEvent)) (Process, error) {
	a.starts++
	return nil, errors.New("unexpected vendor boundary")
}

func TestUnsupportedVerificationPollProbesWithoutLaunching(t *testing.T) {
	for _, vendor := range []Adapter{NewCodexAdapter("/unused", nil), NewCursorAdapter("/unused", nil), NewPiAdapter("/unused", nil), NewGrokAdapter()} {
		t.Run(vendor.Name(), func(t *testing.T) {
			s, api, _ := claimFixture(t)
			verificationClaim(api)
			api.run.AccountID, api.run.RequestedAccountID = "", "account"
			api.server = api.run
			qualified := false
			if capability, ok := vendor.(VerificationAdapter); ok {
				qualified = capability.VerificationSupported()
			}
			if vendor.Name() == Grok {
				qualified = grokVerificationSupported("linux", "amd64")
			}
			adapter := &verificationBoundaryCounter{Adapter: vendor, qualified: qualified}
			s.adapters = map[string]Adapter{vendor.Name(): adapter}
			s.accounts[0].Harness = vendor.Name()
			api.profile.Harness = vendor.Name()
			if err := s.PollOnce(t.Context()); err != nil {
				t.Fatal("missing fail-closed capability refusal", err)
			}
			if err := s.PollOnce(t.Context()); err != nil {
				t.Fatal("same refused run polled again", err)
			}
			if adapter.probes != 2 || adapter.starts != 0 || len(api.probes) != 2 || api.routes != 0 || len(api.claimIDs) != 0 || api.profiles != 1 {
				t.Fatal("unsupported verification prevented probing or reached a route, launch claim or vendor launch")
			}
			if got := s.Lifecycle("").VerificationUnavailable; !slices.Equal(got, []string{"account"}) {
				t.Fatalf("unbound refusal lost the exact enrollment: %v", got)
			}
		})
	}
}

type wrongVerificationRoute struct{ *fakeAPI }

func (a *wrongVerificationRoute) Route(ctx context.Context, id, daemon string, accounts []string, units map[string]int64) (Route, error) {
	r, err := a.fakeAPI.Route(ctx, id, daemon, accounts, units)
	r.AccountID, r.AccountKey = "another-account", "another-key"
	return r, err
}

func TestVerificationRouteCannotSubstituteEnrollment(t *testing.T) {
	s, api, adapter := claimFixture(t)
	verificationClaim(api)
	api.run.AccountID, api.run.RequestedAccountID = "", "account"
	s.accounts = append(s.accounts, EnrolledAccount{ID: "another-account", Key: "another-key", Harness: Codex})
	s.api = &wrongVerificationRoute{fakeAPI: api.fakeAPI}
	if err := s.StartRun(t.Context(), api.run); err == nil {
		t.Fatal("server route substituted another local enrollment")
	}
	if !slices.Equal(api.routeAccounts, []string{"account"}) || api.claims != 0 || adapter.starts != 0 {
		t.Fatal("verification widened requested account or claimed a mismatched route")
	}
}

func TestVerificationFailedProbeSettlesWithoutVendorLaunch(t *testing.T) {
	s, api, adapter := claimFixture(t)
	verificationClaim(api)
	api.run.AccountID, api.run.RequestedAccountID = "", "account"
	api.server = api.run
	s.blockedAccounts["account"] = true
	delete(s.probePendingSince, "account")
	var stage, reason string
	s.verificationDiagnostic = func(_, _ string, st, why string) { stage, reason = st, why }
	if err := s.StartRun(t.Context(), api.run); err != nil {
		t.Fatal(err)
	}
	if adapter.starts != 0 || api.routes != 0 || len(api.claimIDs) != 0 || api.server.Status != "failed" || stage != "refused" || reason != "account_not_ready" {
		t.Fatal("failed probe did not produce the exact no-launch result", stage, reason)
	}
	if err := s.StartRun(t.Context(), api.run); err != nil {
		t.Fatal("refusal replay", err)
	}
	if !s.blockedAccounts["account"] || s.accountAvailable("account") || adapter.starts != 0 {
		t.Fatal("verification refusal changed account authority")
	}
}
