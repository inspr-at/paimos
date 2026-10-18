// SPDX-License-Identifier: AGPL-3.0-only
package agentaccounts

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/tenant"
)

// serveOutlook sends a built outlook through the real privacy wrapper, so the
// policy is loaded from current sharing rather than from the handler's snapshot.
func serveOutlook(t *testing.T, principal tenant.Principal, body []byte) (int, []byte) {
	t.Helper()
	mod := New(appPool).(*Module)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/agent-accounts/quota-outlook", nil)
	req = req.WithContext(tenant.WithPrincipal(req.Context(), principal))
	mod.privateResponse(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(body)
	})(rec, req)
	return rec.Code, rec.Body.Bytes()
}

func peerEnrollment(t *testing.T, f limitFixture, now time.Time) (tenant.Principal, Account) {
	t.Helper()
	var account Account
	callStatusAt(t, f.mod, &f.runner, f.token, "POST", "/api/agent-accounts", `{"account_key":"peer","harness":"codex","daemon_id":"daemon-b","label":"Peer"}`, now, 201, &account)
	peer := addPrincipal(t, f.admin.TenantID, "person", "Peer", []string{"admin"})
	ownFixtureAccount(t, peer, &account)
	if _, err := adminPool.Exec(t.Context(), `UPDATE agent_accounts SET share_usage=false WHERE id=$1`, account.ID); err != nil {
		t.Fatal(err)
	}
	return peer, account
}

// Risk: a revoke that lands between the outlook build and the privacy check
// still returns the private account's pool, burn and projection. The generic
// account mask deletes the row's used percent and leaves those aggregates.
func TestQuotaOutlookRevocationBoundary(t *testing.T) {
	now := time.Date(2026, 10, 18, 11, 10, 0, 0, time.UTC)
	anchor := now.Truncate(time.Hour)
	reset := func(int) time.Time { return now.Add(80 * time.Hour) }
	seriesA := func() []weeklySample {
		return series(now, -24, 0, func(k int) float64 { return 10 + float64(k+24) }, reset)
	}
	seriesB := func() []weeklySample {
		return series(now, -24, 0, func(k int) float64 { return 2 * float64(k+24) }, reset)
	}

	t.Run("a private sibling hides the whole shared subscription", func(t *testing.T) {
		f := readinessWorld(t, "outlook-revoke-sibling", now)
		_, peerAccount := peerEnrollment(t, f, now)
		fp := strings.Repeat("ab", 32)
		if _, err := adminPool.Exec(t.Context(), `UPDATE agent_accounts SET quota_pool_fingerprint=$2 WHERE id=$1 OR id=$3`, f.account.ID, fp, peerAccount.ID); err != nil {
			t.Fatal(err)
		}
		g := buildGroup("codex", []outlookInput{
			{ID: f.account.ID, Label: "Main", Harness: "codex", Limit: 100, Samples: seriesA()},
			{ID: peerAccount.ID, Label: "Peer", Harness: "codex", Limit: 100, Samples: seriesB()},
		}, now, anchor)
		code, raw := serveOutlook(t, f.admin, outlookBody(t, now, g))
		if code != 200 {
			t.Fatalf("status %d %s", code, raw)
		}
		var out quotaOutlook
		if err := json.Unmarshal(raw, &out); err != nil {
			t.Fatal(err)
		}
		got := out.Groups[0]
		if got.Pool != nil || got.Current != nil || got.Accounts[0].UsedPercent != nil || got.Accounts[1].UsedPercent != nil {
			t.Fatalf("private sibling still in the outlook: pool %+v current %v used %v %v", got.Pool, got.Current != nil, got.Accounts[0].UsedPercent, got.Accounts[1].UsedPercent)
		}
	})

	t.Run("an unshared neighbor is removed and the owner's pool is rebuilt", func(t *testing.T) {
		f := readinessWorld(t, "outlook-revoke-neighbor", now)
		_, peerAccount := peerEnrollment(t, f, now)
		g := buildGroup("codex", []outlookInput{
			{ID: f.account.ID, Label: "Main", Harness: "codex", Limit: 100, Samples: seriesA()},
			{ID: peerAccount.ID, Label: "Peer", Harness: "codex", Limit: 100, Samples: seriesB()},
		}, now, anchor)
		if g.Pool == nil || g.Pool.Accounts != 2 {
			t.Fatalf("pre-redaction pool: %+v", g.Pool)
		}
		code, raw := serveOutlook(t, f.admin, outlookBody(t, now, g))
		if code != 200 {
			t.Fatalf("status %d %s", code, raw)
		}
		var out quotaOutlook
		if err := json.Unmarshal(raw, &out); err != nil {
			t.Fatal(err)
		}
		got := out.Groups[0]
		owner, neighbor := outlookAccountByID(got, f.account.ID), outlookAccountByID(got, peerAccount.ID)
		if neighbor.State != "private" || neighbor.Forecast != nil || neighbor.UsedPercent != nil {
			t.Fatalf("neighbor: %+v", neighbor)
		}
		if owner.UsedPercent == nil || *owner.UsedPercent != 34 || owner.Forecast != nil {
			t.Fatalf("owner: %+v", owner)
		}
		if got.Pool == nil || got.Pool.Accounts != 1 || got.Pool.UsedPercent != 34 || got.Current != nil || got.Verdict != "unknown" {
			t.Fatalf("pool not rebuilt from the owner alone: %+v verdict %s current %v", got.Pool, got.Verdict, got.Current != nil)
		}
		ownerBurn, neighborBurn := false, false
		for _, h := range got.History {
			if len(h.Burn) > 0 && h.Burn[0] != 0 {
				ownerBurn = true
			}
			if len(h.Burn) > 1 && h.Burn[1] != 0 {
				neighborBurn = true
			}
		}
		if !ownerBurn || neighborBurn {
			t.Fatalf("history burn owner %v neighbor %v", ownerBurn, neighborBurn)
		}
	})
}
