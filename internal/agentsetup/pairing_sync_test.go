// SPDX-License-Identifier: AGPL-3.0-only

package agentsetup

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/inspr-at/paimos/internal/agentsecurity"
)

func TestReadyAccountExpiredVerificationIsInformationalAndReadOnly(t *testing.T) {
	for _, tc := range []struct{ verification, account, stage string }{
		{"expired", "ready", "connected"},
		{"expired", "blocked", "verification_failed"},
		{"failed", "ready", "verification_failed"},
		{"cancelled", "ready", "verification_failed"},
		{"ownership_lost", "ready", "verification_failed"},
		{"queued", "ready", "verification_pending"},
	} {
		t.Run(tc.verification+"/"+tc.account, func(t *testing.T) {
			e, api, local, opts, executor := engineFixture(t)
			approveFixture(t, e, api, opts)
			s, err := e.load()
			if err != nil {
				t.Fatal(err)
			}
			s.View.Enrollments[0].VerificationRunID = otherAccount
			s.View.Enrollments[0].VerificationState = tc.verification
			s.View.Enrollments[0].Cleanup = "pending"
			s.View.Verification.Allowance = 1
			if err := e.save(s, false); err != nil {
				t.Fatal(err)
			}
			// A ready sibling must not turn this account's expiry informational.
			local.states[""] = LocalStatus{DaemonID: s.View.DaemonID, State: "unconfirmed", Ready: true,
				AccountStatuses: map[string]HarnessDetail{testAccount: {State: tc.account}, "sibling": {State: "ready"}},
				HarnessDetails:  map[string]HarnessDetail{"codex": {State: "ready"}},
				Unconfirmed:     []string{otherAccount},
			}
			before, err := e.Store.Read(snapshotName, 1<<20)
			if err != nil {
				t.Fatal(err)
			}
			calls, creates := len(executor.calls), api.createCount
			api.progress = nil
			for range 2 {
				p, err := e.Status(t.Context())
				if err != nil || p.Stage != tc.stage {
					t.Fatalf("expected %s, got %s: %v", tc.stage, p.Stage, err)
				}
				if tc.verification == "expired" && tc.account == "ready" && (!strings.Contains(p.Action, "expired") || !strings.Contains(p.Action, "run verification again")) {
					t.Fatal("expiry lacks informational next action", p.Action)
				}
				if p.Accounts[0].VerificationState != tc.verification || p.Accounts[0].VerificationRunID != otherAccount || p.Accounts[0].Cleanup != "pending" || p.LocalProcesses != "unconfirmed" || p.AccountStatuses[testAccount].State != tc.account {
					t.Fatal("status changed verification, cleanup or account health")
				}
			}
			after, err := e.Store.Read(snapshotName, 1<<20)
			if err != nil || !bytes.Equal(before, after) || len(executor.calls) != calls || api.createCount != creates || api.progress != nil || len(local.fenced) != 0 {
				t.Fatal("status retried, reconciled cleanup, or changed approval/allowance")
			}
		})
	}
}

func TestPairingRetryPolicyAndSafeFirstCause(t *testing.T) {
	for _, tc := range []struct {
		err   error
		cause string
		retry bool
	}{
		{&APIError{Code: "unavailable", StatusCode: 503}, "http_503", true},
		{&APIError{Code: "rate_limited", StatusCode: 429}, "http_429", true},
		{&APIError{StatusCode: 408}, "http_408", true},
		{&APIError{Code: "unreachable"}, "unreachable", true},
		{context.DeadlineExceeded, "timeout", true},
		{&APIError{Code: "forbidden", StatusCode: 403}, "http_403", false},
		{&APIError{Code: "pairing_revoked", StatusCode: 410}, "http_410", false},
		{&APIError{Code: "unavailable", StatusCode: 422}, "http_422", false},
		{&APIError{Code: "conflict", StatusCode: 409}, "http_409", false},
		{ErrBusy, "store_busy", true},
		{agentsecurity.ErrDenied, "keychain_denied", false},
		{ErrCollision, "state_collision", false},
		{errors.New("fixture private response"), "unclassified", false},
	} {
		err := &pairingSyncError{err: tc.err, detail: PairingServerUnavailable}
		if PairingFailureCause(err) != tc.cause || PairingRetryable(err) != tc.retry {
			t.Errorf("wrong safe cause or retry policy for %s", tc.cause)
		}
	}
}

func TestStatusKeepsAccountHealthSeparateFromExpiredVerification(t *testing.T) {
	e, api, local, opts, _ := engineFixture(t)
	approveFixture(t, e, api, opts)
	s, err := e.load()
	if err != nil {
		t.Fatal(err)
	}
	s.View.Enrollments[0].VerificationState = "failed"
	s.View.Enrollments[0].VerificationError = "verification expired"
	if err := e.save(s, false); err != nil {
		t.Fatal(err)
	}
	local.states[""] = LocalStatus{DaemonID: s.View.DaemonID, State: "drained", Ready: true,
		AccountStatuses: map[string]HarnessDetail{testAccount: {State: "ready"}},
		HarnessDetails:  map[string]HarnessDetail{"claude": {State: "ready"}},
		HarnessStatuses: map[string]string{"claude": "ready"},
	}
	p, err := e.Status(t.Context())
	if err != nil || p.AccountStatuses[testAccount].State != "ready" || p.HarnessDetails["claude"].State != "ready" || p.Stage == "login_required" {
		t.Fatal("verification failure changed account sign-in state", err)
	}
}

func TestSyncFencesAcceptsTombstoneAndChangedLiveLabels(t *testing.T) {
	e, api, local, opts, _ := engineFixture(t)
	approveFixture(t, e, api, opts)
	s, err := e.load()
	if err != nil {
		t.Fatal(err)
	}
	for i, harness := range []string{"codex", "cursor"} {
		id := []string{otherAccount, "66666666-6666-4666-8666-666666666666"}[i]
		candidate := Candidate{Harness: harness, Key: harness + "-approved", Label: harness}
		s.Candidates = append(s.Candidates, LocalCandidate{Candidate: candidate})
		api.view.Enrollments = append(api.view.Enrollments, Enrollment{AccountID: id, AccountKey: candidate.Key, Harness: harness, Label: harness, State: "connected"})
	}
	api.view.Enrollments[0].Label = "changed display label"
	const retired = "77777777-7777-4777-8777-777777777777"
	api.view.Enrollments = append(api.view.Enrollments, Enrollment{AccountID: retired, AccountKey: "retired", Harness: "claude", State: "revoked"})
	if err := e.save(s, false); err != nil {
		t.Fatal(err)
	}
	if err := e.SyncFences(t.Context()); err != nil {
		t.Fatal("historical tombstone or informational label blocked live accounts", err)
	}
	if len(local.fenced) != 1 || local.fenced[0] != retired {
		t.Fatal("revoked enrollment lost its fence", local.fenced)
	}
	saved, err := e.load()
	if err != nil || !saved.Removed[retired] || len(saved.View.Enrollments) != 4 {
		t.Fatal("sync lost the tombstone or a live enrollment", err)
	}
}

func TestValidateViewPreservesAuthorityChecks(t *testing.T) {
	e, api, _, opts, _ := engineFixture(t)
	approveFixture(t, e, api, opts)
	s, err := e.load()
	if err != nil {
		t.Fatal(err)
	}
	for _, state := range []string{"connected", "draining"} {
		for _, changed := range []string{"label", "key", "harness"} {
			t.Run(state+"/"+changed, func(t *testing.T) {
				v := api.view
				v.Enrollments = append([]Enrollment(nil), v.Enrollments...)
				v.Enrollments[0].State = state
				switch changed {
				case "label":
					v.Enrollments[0].Label = "new label"
				case "key":
					v.Enrollments[0].AccountKey = "unapproved"
				case "harness":
					v.Enrollments[0].Harness = "cursor"
				}
				err := validateView(s, v, false)
				if changed == "label" && err != nil || changed != "label" && !errors.Is(err, ErrUnapprovedAccount) {
					t.Fatalf("wrong binding decision for %s: %v", changed, err)
				}
			})
		}
	}
	for _, kind := range []string{"identity", "duplicate", "state", "scope", "revision", "computer", "choices"} {
		t.Run(kind, func(t *testing.T) {
			v := api.view
			v.Enrollments = append([]Enrollment(nil), v.Enrollments...)
			v.Enrollments[0].State = "revoked"
			switch kind {
			case "identity":
				v.Enrollments[0].AccountID = "invalid"
			case "duplicate":
				v.Enrollments = append(v.Enrollments, v.Enrollments[0])
			case "state":
				v.Enrollments[0].State = "unknown"
			case "scope":
				v.TenantID = otherAccount
			case "revision":
				v.Revision = s.View.Revision - 1
			case "computer":
				v.ComputerID = otherAccount
			case "choices":
				v.Requested = append([]Candidate(nil), v.Requested...)
				v.Requested[0].Label = "different approval"
			}
			want := map[string]string{
				"identity": "invalid enrollment identity", "duplicate": "invalid enrollment identity",
				"state": "unknown enrollment lifecycle state", "scope": "pairing response scope mismatch",
				"revision": "stale computer revision", "computer": "immutable enrollment binding changed",
				"choices": "approved choices do not match local request",
			}[kind]
			if err := validateView(s, v, kind == "choices"); err == nil || err.Error() != want {
				t.Fatalf("%s: expected %q, got %v", kind, want, err)
			}
		})
	}
	api.view.Enrollments[0].AccountKey = "unapproved"
	if err := e.SyncFences(t.Context()); !errors.Is(err, ErrUnapprovedAccount) || PairingFailureDetail(err) != PairingAccountUnapproved {
		t.Fatal("unapproved connected account not refused with its safe cause", err)
	}
}

func TestPairingFailureSurvivesStatusAndReconcileWithoutRawErrors(t *testing.T) {
	e, api, local, opts, _ := engineFixture(t)
	approveFixture(t, e, api, opts)
	api.offline = true
	err := e.SyncFences(t.Context())
	if err == nil || PairingFailureDetail(err) != PairingServerUnavailable {
		t.Fatal("wrong sync failure", err)
	}
	detail := HarnessDetail{State: "blocked", Reason: PairingSyncFailed}.WithProbeDetail("claude", PairingFailureDetail(err))
	local.states[""] = LocalStatus{DaemonID: "paired-daemon", State: "drained", AccountStatuses: map[string]HarnessDetail{testAccount: detail}, HarnessDetails: map[string]HarnessDetail{"claude": detail}, HarnessStatuses: map[string]string{"claude": "blocked"}}
	p, err := e.Status(t.Context())
	if err != nil || p.Stage != "blocked" || !strings.Contains(p.Action, PairingServerUnavailable) || p.HarnessDetails["claude"].Reason != PairingSyncFailed {
		t.Fatal("status hid the sync cause", p, err)
	}
	api.offline = false
	if err := e.SyncFences(t.Context()); err != nil || api.progress.HarnessDetails["claude"].ReasonDetail != PairingServerUnavailable {
		t.Fatal("lifecycle report lost sync cause", err)
	}
	for _, cause := range []string{PairingLocalUnavailable, PairingServerUnavailable, PairingViewInvalid, PairingAccountUnapproved, PairingFenceUnavailable, PairingRuntimeUnavailable, "private error /private/profile"} {
		want := cause
		if cause == "private error /private/profile" {
			want = ""
		}
		raw, _ := json.Marshal(HarnessDetail{State: "blocked", Reason: PairingSyncFailed, ReasonDetail: cause})
		var decoded HarnessDetail
		if err := json.Unmarshal(raw, &decoded); err != nil || decoded.ReasonDetail != want {
			t.Fatal("diagnostic allowlist lost", err)
		}
	}
}
