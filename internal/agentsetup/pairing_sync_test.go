// SPDX-License-Identifier: AGPL-3.0-only

package agentsetup

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

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
