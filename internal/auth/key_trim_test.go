// SPDX-License-Identifier: AGPL-3.0-only
package auth

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

func trimFixture(t *testing.T) (*Module, tenant.Principal, agentKeyCreatedJSON, trimInput) {
	t.Helper()
	m, owner := keyFixture(t)
	now := time.Now().UTC()
	m.trimNow = func() time.Time { return now }
	key := decodeKey(t, keyRequest(m, owner, map[string]any{"name": "trim-worker", "scopes": []string{"nodes.read", "nodes.write"}}))
	in := trimInput{RequestID: "10000000-0000-4000-8000-000000000001", Expected: []string{"nodes.write", "nodes.read"}, Candidate: []string{"nodes.read"}, ExpiresAt: now.Add(time.Hour), Evidence: trimEvidence{Summary: "Reviewed ticket workload; read dependencies retained.", ObservedAt: now.Add(-time.Hour), Risks: []trimRisk{{Scope: "nodes.write", Evidence: "No recorded writes in the analysis; earlier use is unknown.", Risk: "Ticket creation and editing will fail."}}}}
	return m, owner, key, in
}
func proposeTrim(t *testing.T, m *Module, p tenant.Principal, key string, in trimInput) trimProposal {
	t.Helper()
	out, err := m.proposeKeyTrim(t.Context(), p, key, in)
	if err != nil {
		t.Fatalf("proposal: %v", err)
	}
	return out
}
func trimMutation(t *testing.T, m *Module, p tenant.Principal, proposal trimProposal, decision string) (trimProposal, error) {
	t.Helper()
	digest := proposal.SnapshotDigest
	if decision == "restore" {
		digest = proposal.CandidateDigest
	}
	in := trimDecision{RequestID: "20000000-0000-4000-8000-000000000001", ExpectedDigest: digest, Decision: decision}
	if decision == "restore" {
		in.Decision = ""
	}
	return m.decideKeyTrim(t.Context(), p, proposal.ID, in, decision == "restore")
}
func trimScopesStored(t *testing.T, m *Module, p tenant.Principal, id string) []string {
	t.Helper()
	var scopes []string
	if err := db.InTenant(dbtest.Seed(t.Context()), m.pool, p.TenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), `SELECT scopes FROM agent_keys WHERE id=$1::uuid`, id).Scan(&scopes)
	}); err != nil {
		t.Fatal(err)
	}
	return scopes
}
func trimHTTP(m *Module, p tenant.Principal, method, path string, body any) *httptest.ResponseRecorder {
	data, _ := json.Marshal(body)
	r := httptest.NewRequest(method, path, strings.NewReader(string(data)))
	if p.ID != "" {
		r = r.WithContext(tenant.WithPrincipal(r.Context(), p))
	}
	mux := http.NewServeMux()
	m.Mount(mux)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	return w
}
func TestKeyTrimApproveRestoreAuditAndExactIdempotency(t *testing.T) {
	m, owner, key, in := trimFixture(t)
	_, startEvents := keyCounts(t, m, owner)
	proposal := proposeTrim(t, m, owner, key.ID, in)
	replay := proposeTrim(t, m, owner, key.ID, in)
	if replay.ID != proposal.ID || proposal.State != "pending" || len(proposal.Usage) != 2 {
		t.Fatal("proposal replay or usage evidence missing")
	}
	if !slices.Equal(trimScopesStored(t, m, owner, key.ID), key.Scopes) {
		t.Fatal("proposal changed scopes")
	}
	in.Candidate = []string{}
	in.Evidence.Risks = append(in.Evidence.Risks, trimRisk{Scope: "nodes.read", Evidence: "Unknown", Risk: "Reads fail"})
	if _, err := m.proposeKeyTrim(t.Context(), owner, key.ID, in); !errors.Is(err, errTrimReplay) {
		t.Fatalf("conflicting proposal replay: %v", err)
	}
	applied, err := trimMutation(t, m, owner, proposal, "approve")
	if err != nil {
		t.Fatal(err)
	}
	if applied.State != "applied" || applied.Revision != 2 || applied.RestoreUntil == nil || applied.RestoreUntil.Sub(*applied.AppliedAt) != 30*24*time.Hour {
		t.Fatal("missing applied restore window")
	}
	if !slices.Equal(trimScopesStored(t, m, owner, key.ID), []string{"nodes.read"}) {
		t.Fatal("trim not applied")
	}
	if replay, err := trimMutation(t, m, owner, proposal, "approve"); err != nil || replay.State != "applied" {
		t.Fatalf("decision replay: %v", err)
	}
	if _, err := trimMutation(t, m, owner, proposal, "decline"); !errors.Is(err, errTrimReplay) {
		t.Fatalf("conflicting decision replay: %v", err)
	}
	restored, err := trimMutation(t, m, owner, applied, "restore")
	if err != nil {
		t.Fatal(err)
	}
	if restored.State != "restored" || restored.Revision != 3 || !slices.Equal(trimScopesStored(t, m, owner, key.ID), proposal.Previous) {
		t.Fatal("restore lost previous scopes")
	}
	if _, err := trimMutation(t, m, owner, applied, "restore"); err != nil {
		t.Fatal(err)
	}
	_, endEvents := keyCounts(t, m, owner)
	if endEvents != startEvents+3 {
		t.Fatal("replay audited twice or missed an event")
	}
	if err := db.InTenant(dbtest.Seed(t.Context()), m.pool, owner.TenantID, func(tx pgx.Tx) error {
		var ok bool
		err := tx.QueryRow(t.Context(), `SELECT count(*)=2 AND bool_and(actor_principal_id=$1::uuid AND before->>'key_id'=$2 AND after->>'proposal_id'=$3 AND before->'scopes'<>after->'scopes' AND NOT(before ?| ARRAY['prefix','token','hash']) AND NOT(after ?| ARRAY['prefix','token','hash'])) FROM events WHERE type='agent_key.scopes_changed'`, owner.ID, key.ID, proposal.ID).Scan(&ok)
		if err == nil && !ok {
			t.Error("scope audit actor, CAS receipt, before/after or redaction failed")
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	bytes, _ := json.Marshal(restored)
	for _, forbidden := range []string{`"prefix"`, `"hash"`, `"token"`, key.Token, key.Prefix} {
		if strings.Contains(string(bytes), forbidden) {
			t.Fatal("trim response exposed key material")
		}
	}
}
func TestKeyTrimRequestUUIDCasingPreservesIdempotency(t *testing.T) {
	m, owner, key, in := trimFixture(t)
	in.RequestID = "abcdef00-0000-4000-8000-000000000001"
	proposal := proposeTrim(t, m, owner, key.ID, in)
	in.RequestID = strings.ToUpper(in.RequestID)
	if replay := proposeTrim(t, m, owner, strings.ToUpper(key.ID), in); replay.ID != proposal.ID {
		t.Fatal("UUID aliases created a different proposal")
	}
	decision := trimDecision{RequestID: "abcdef00-0000-4000-8000-000000000002", ExpectedDigest: proposal.SnapshotDigest, Decision: "approve"}
	if _, err := m.decideKeyTrim(t.Context(), owner, proposal.ID, decision, false); err != nil {
		t.Fatal(err)
	}
	decision.RequestID = strings.ToUpper(decision.RequestID)
	if replay, err := m.decideKeyTrim(t.Context(), owner, proposal.ID, decision, false); err != nil || replay.State != "applied" || replay.Revision != 2 {
		t.Fatalf("UUID decision alias failed to replay: %v", err)
	}
}
func TestKeyTrimRecentUseIsRecheckedAtProposalReadAndApproval(t *testing.T) {
	m, owner, key, in := trimFixture(t)
	proposal := proposeTrim(t, m, owner, key.ID, in)
	prefix, secret, _ := parseBearer("Bearer " + key.Token)
	agent, ok, err := m.authenticateAgent(t.Context(), prefix, secret)
	if err != nil || !ok || agent.KeyID != key.ID {
		t.Fatal("authenticating key identity missing")
	}
	ctx := authz.BindPool(tenant.WithPrincipal(t.Context(), agent), m.pool)
	if err := authz.Require(ctx, "nodes.write", authz.Scope{}); err != nil {
		t.Fatal(err)
	}
	in.RequestID = "10000000-0000-4000-8000-000000000002"
	if _, err := m.proposeKeyTrim(t.Context(), owner, key.ID, in); !errors.Is(err, errTrimRecent) {
		t.Fatalf("recent propose: %v", err)
	}
	w := trimHTTP(m, owner, "GET", "/api/key-trim-proposals", nil)
	var page struct {
		Items []trimProposal `json:"items"`
	}
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &page) != nil || len(page.Items) != 1 || page.Items[0].BlockedReason != errTrimRecent.Error() {
		t.Fatal("read failed to mark newly used scope")
	}
	if _, err := trimMutation(t, m, owner, proposal, "approve"); !errors.Is(err, errTrimRecent) {
		t.Fatalf("recent approval: %v", err)
	}
	if !slices.Equal(trimScopesStored(t, m, owner, key.ID), key.Scopes) {
		t.Fatal("recent use failed closed")
	}
	// Deterministic 24h boundary, including the one-minute debounce margin.
	now := m.trimClock()
	if err := db.InTenant(dbtest.Seed(t.Context()), m.pool, owner.TenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE agent_key_scope_usage SET last_used_at=$2 WHERE key_id=$1`, key.ID, now.Add(-24*time.Hour-30*time.Second))
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := m.proposeKeyTrim(t.Context(), owner, key.ID, in); !errors.Is(err, errTrimRecent) {
		t.Fatal("debounce margin ignored")
	}
	if err := db.InTenant(dbtest.Seed(t.Context()), m.pool, owner.TenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE agent_key_scope_usage SET last_used_at=$2 WHERE key_id=$1`, key.ID, now.Add(-25*time.Hour))
		return err
	}); err != nil {
		t.Fatal(err)
	}
	_ = proposeTrim(t, m, owner, key.ID, in)
}
func TestKeyTrimDeclineExpiryAndRestoreWindow(t *testing.T) {
	for _, scenario := range []string{"decline", "expired", "restore-expired", "inactive"} {
		t.Run(scenario, func(t *testing.T) {
			m, owner, key, in := trimFixture(t)
			proposal := proposeTrim(t, m, owner, key.ID, in)
			switch scenario {
			case "decline":
				out, err := trimMutation(t, m, owner, proposal, "decline")
				if err != nil || out.State != "declined" {
					t.Fatalf("decline: %v", err)
				}
			case "expired":
				m.trimNow = func() time.Time { return in.ExpiresAt }
				if _, err := trimMutation(t, m, owner, proposal, "approve"); !errors.Is(err, errTrimExpired) {
					t.Fatalf("expiry: %v", err)
				}
			case "restore-expired":
				applied, err := trimMutation(t, m, owner, proposal, "approve")
				if err != nil {
					t.Fatal(err)
				}
				m.trimNow = func() time.Time { return *applied.RestoreUntil }
				if _, err := trimMutation(t, m, owner, applied, "restore"); !errors.Is(err, errTrimRestore) {
					t.Fatalf("restore expiry: %v", err)
				}
				return
			case "inactive":
				if err := m.revokeAgentKey(tenant.WithPrincipal(t.Context(), owner), owner, key.ID); err != nil {
					t.Fatal(err)
				}
				if _, err := trimMutation(t, m, owner, proposal, "approve"); !errors.Is(err, errKeyInactive) {
					t.Fatalf("inactive: %v", err)
				}
			}
			if !slices.Equal(trimScopesStored(t, m, owner, key.ID), key.Scopes) {
				t.Fatal("non-approval mutated key")
			}
		})
	}
}
func TestKeyTrimCASNeverOverwritesInterveningEdit(t *testing.T) {
	for _, restore := range []bool{false, true} {
		t.Run(map[bool]string{false: "approve", true: "restore"}[restore], func(t *testing.T) {
			m, owner, key, in := trimFixture(t)
			proposal := proposeTrim(t, m, owner, key.ID, in)
			delta := &keyScopeDelta{Remove: []string{"nodes.read"}}
			if restore {
				var err error
				proposal, err = trimMutation(t, m, owner, proposal, "approve")
				if err != nil {
					t.Fatal(err)
				}
			}
			if _, err := m.agentKeyScopes(tenant.WithPrincipal(t.Context(), owner), owner, key.ID, delta); err != nil {
				t.Fatal(err)
			}
			want := trimScopesStored(t, m, owner, key.ID)
			action := "approve"
			if restore {
				action = "restore"
			}
			if _, err := trimMutation(t, m, owner, proposal, action); !errors.Is(err, errTrimChanged) {
				t.Fatalf("CAS failure: %v", err)
			}
			if !slices.Equal(trimScopesStored(t, m, owner, key.ID), want) {
				t.Fatal("CAS overwrote intervening edit")
			}
		})
	}
}
func TestKeyTrimAgentDeniedAtMiddlewareAndHandlerLayers(t *testing.T) {
	m, owner, key, in := trimFixture(t)
	proposal := proposeTrim(t, m, owner, key.ID, in)
	agent := tenant.Principal{ID: key.PrincipalID, TenantID: owner.TenantID, Kind: tenant.Agent, Scopes: []string{"keys.manage", "approvals.request"}}
	for _, path := range []string{"/api/key-trim-proposals/" + proposal.ID + "/decision", "/api/key-trim-proposals/" + proposal.ID + "/restore"} {
		if w := trimHTTP(m, agent, "POST", path, map[string]any{}); w.Code != 403 {
			t.Fatalf("handler person gate: %d", w.Code)
		}
	}
	// Mint a real proposal-capable key to exercise the outer scope allowlist.
	proposer := decodeKey(t, keyRequest(m, owner, map[string]any{"name": "proposer", "scopes": []string{"approvals.request"}}))
	mux := http.NewServeMux()
	m.Mount(mux)
	secured := m.Middleware(mux)
	call := func(path string, body any) *httptest.ResponseRecorder {
		data, _ := json.Marshal(body)
		r := httptest.NewRequest("POST", path, strings.NewReader(string(data)))
		r.Header.Set("Authorization", "Bearer "+proposer.Token)
		_, r.Pattern = mux.Handler(r)
		w := httptest.NewRecorder()
		secured.ServeHTTP(w, r)
		return w
	}
	in.RequestID = "10000000-0000-4000-8000-000000000003"
	if w := call("/api/agent-keys/"+key.ID+"/trim-proposals", in); w.Code != 201 {
		t.Fatalf("middleware propose scope: %d", w.Code)
	}
	for _, suffix := range []string{"decision", "restore"} {
		if w := call("/api/key-trim-proposals/"+proposal.ID+"/"+suffix, trimDecision{}); w.Code != 403 || strings.Contains(w.Body.String(), "key trim JSON") {
			t.Fatal("middleware allowlist failed to refuse agent before handler")
		}
	}
	if _, err := m.decideKeyTrim(t.Context(), agent, proposal.ID, trimDecision{}, false); !errors.Is(err, authz.ErrForbidden) {
		t.Fatal("direct agent applied trim")
	}
}
func TestKeyTrimTenantIsolationAndBoundedInput(t *testing.T) {
	m, owner, key, in := trimFixture(t)
	proposal := proposeTrim(t, m, owner, key.ID, in)
	foreign := tenant.Principal{Kind: tenant.Person}
	ctx := dbtest.Seed(t.Context())
	if err := db.InTenant(ctx, m.pool, "00000000-0000-0000-0000-000000000000", func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `INSERT INTO tenants(slug,name) VALUES('trim-foreign','Foreign') RETURNING id::text`).Scan(&foreign.TenantID)
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.InTenant(ctx, m.pool, foreign.TenantID, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `INSERT INTO principals(tenant_id,kind,name,roles) VALUES($1,'person','Foreign',ARRAY['super_admin']) RETURNING id::text`, foreign.TenantID).Scan(&foreign.ID); err != nil {
			return err
		}
		return dbtest.BindLegacyTx(ctx, tx, foreign.TenantID, foreign.ID)
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := m.proposeKeyTrim(t.Context(), foreign, key.ID, in); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("foreign proposal: %v", err)
	}
	if _, err := trimMutation(t, m, foreign, proposal, "approve"); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("foreign approval: %v", err)
	}
	w := trimHTTP(m, foreign, "GET", "/api/key-trim-proposals", nil)
	var page struct {
		Items []trimProposal `json:"items"`
	}
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &page) != nil || len(page.Items) != 0 {
		t.Fatal("foreign list disclosed proposal")
	}
	// Preserve a real row, so an empty foreign read actually proves filtering.
	if err := db.InTenant(ctx, m.pool, owner.TenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `INSERT INTO agent_key_scope_usage(tenant_id,key_id,scope,last_used_at) VALUES($1,$2,'nodes.read',now())`, owner.TenantID, key.ID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	// RLS also protects the timestamp table independently of API filters.
	if err := db.InTenant(ctx, m.pool, foreign.TenantID, func(tx pgx.Tx) error {
		var count int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM agent_key_scope_usage WHERE tenant_id=$1`, owner.TenantID).Scan(&count); err != nil {
			return err
		}
		if count != 0 {
			t.Error("usage crossed tenants")
		}
		_, err := tx.Exec(ctx, `INSERT INTO agent_key_scope_usage(tenant_id,key_id,scope,last_used_at) VALUES($1,$2,'nodes.read',now())`, owner.TenantID, key.ID)
		return err
	}); err == nil {
		t.Fatal("foreign usage insert bypassed RLS")
	} else {
		var pe *pgconn.PgError
		if !errors.As(err, &pe) || pe.Code != "42501" {
			t.Fatalf("foreign usage insert failed for the wrong reason: %v", err)
		}
	}
	for _, query := range []string{"?limit=101", "?limit=0", "?cursor=no", "?state=all"} {
		if w := trimHTTP(m, owner, "GET", "/api/key-trim-proposals"+query, nil); w.Code != 400 {
			t.Fatal("unbounded list accepted")
		}
	}
	bad := in
	bad.Candidate = []string{"nodes.read", "nodes.write", "events.read"}
	if _, err := m.proposeKeyTrim(t.Context(), owner, key.ID, bad); !errors.Is(err, errTrimInvalid) {
		t.Fatal("proposal could add scopes")
	}
	bad = in
	bad.Evidence.Summary = strings.Repeat("x", 65<<10)
	if w := trimHTTP(m, owner, "POST", "/api/agent-keys/"+key.ID+"/trim-proposals", bad); w.Code != 400 {
		t.Fatal("oversized body accepted")
	}
}
func TestKeyTrimConcurrentCASAndAuthorizationUseBarriers(t *testing.T) {
	m, owner, key, in := trimFixture(t)
	proposal := proposeTrim(t, m, owner, key.ID, in)
	// Two distinct approvals start together; tenant/key fences choose one winner.
	transactionReady, transactionsGo := make(chan struct{}, 2), make(chan struct{})
	t.Cleanup(func() {
		select {
		case <-transactionsGo:
		default:
			close(transactionsGo)
		}
	})
	m.inTenant = func(ctx context.Context, pool *pgxpool.Pool, tid string, fn func(pgx.Tx) error) error {
		return db.InTenant(ctx, pool, tid, func(tx pgx.Tx) error {
			transactionReady <- struct{}{}
			select {
			case <-transactionsGo:
			case <-ctx.Done():
				return ctx.Err()
			}
			return fn(tx)
		})
	}
	start := make(chan struct{})
	ready := make(chan struct{}, 2)
	errs := make(chan error, 2)
	var workers sync.WaitGroup
	for i := 0; i < 2; i++ {
		workers.Add(1)
		go func(i int) {
			defer workers.Done()
			ready <- struct{}{}
			<-start
			request := trimDecision{RequestID: []string{"30000000-0000-4000-8000-000000000001", "30000000-0000-4000-8000-000000000002"}[i], ExpectedDigest: proposal.SnapshotDigest, Decision: "approve"}
			_, err := m.decideKeyTrim(t.Context(), owner, proposal.ID, request, false)
			errs <- err
		}(i)
	}
	<-ready
	<-ready
	close(start)
	<-transactionReady
	<-transactionReady
	close(transactionsGo)
	workers.Wait()
	m.inTenant = db.InTenant
	close(errs)
	successes, conflicts := 0, 0
	for err := range errs {
		if err == nil {
			successes++
		} else if errors.Is(err, errTrimReplay) {
			conflicts++
		} else {
			t.Fatalf("wrong concurrency failure: %v", err)
		}
	}
	if successes != 1 || conflicts != 1 {
		t.Fatal("CAS did not select exactly one winner")
	}
	// A source-authorized person can lose keys.manage before the final mutation.
	m2, owner2, key2, in2 := trimFixture(t)
	proposal2 := proposeTrim(t, m2, owner2, key2.ID, in2)
	if err := authz.Require(authz.BindPool(tenant.WithPrincipal(t.Context(), owner2), m2.pool), "keys.manage", authz.Scope{}); err != nil {
		t.Fatal(err)
	}
	// Keep a second owner so revoking this editor tests keys.manage rather
	// than the unrelated last-owner invariant.
	if err := db.InTenant(dbtest.Seed(t.Context()), m2.pool, owner2.TenantID, func(tx pgx.Tx) error {
		var other string
		if err := tx.QueryRow(t.Context(), `INSERT INTO principals(tenant_id,kind,name,roles) VALUES($1,'person','Remaining owner',ARRAY['super_admin']) RETURNING id::text`, owner2.TenantID).Scan(&other); err != nil {
			return err
		}
		return dbtest.BindLegacyTx(t.Context(), tx, owner2.TenantID, other)
	}); err != nil {
		t.Fatal(err)
	}
	entered, release := make(chan struct{}), make(chan struct{})
	t.Cleanup(func() {
		select {
		case <-release:
		default:
			close(release)
		}
	})
	m2.inTenant = func(ctx context.Context, pool *pgxpool.Pool, tid string, fn func(pgx.Tx) error) error {
		return db.InTenant(ctx, pool, tid, func(tx pgx.Tx) error {
			close(entered)
			select {
			case <-release:
			case <-ctx.Done():
				return ctx.Err()
			}
			return fn(tx)
		})
	}
	done := make(chan error, 1)
	go func() { _, err := trimMutation(t, m2, owner2, proposal2, "approve"); done <- err }()
	<-entered
	if err := db.InTenant(dbtest.Seed(t.Context()), m2.pool, owner2.TenantID, func(tx pgx.Tx) error {
		var locked string
		if err := tx.QueryRow(t.Context(), `SELECT id::text FROM tenants WHERE id=$1 FOR NO KEY UPDATE`, owner2.TenantID).Scan(&locked); err != nil {
			return err
		}
		_, err := tx.Exec(t.Context(), `DELETE FROM role_bindings WHERE principal_id=$1`, owner2.ID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	close(release)
	if err := <-done; !errors.Is(err, authz.ErrForbidden) {
		t.Fatalf("in-write authz missed revocation: %v", err)
	}
	if !slices.Equal(trimScopesStored(t, m2, owner2, key2.ID), key2.Scopes) {
		t.Fatal("revoked person applied trim")
	}
}
func TestKeyTrimLiveCeilingsGuardRestore(t *testing.T) {
	m, owner, key, in := trimFixture(t)
	proposal := proposeTrim(t, m, owner, key.ID, in)
	applied, err := trimMutation(t, m, owner, proposal, "approve")
	if err != nil {
		t.Fatal(err)
	}
	if err := db.InTenant(dbtest.Seed(t.Context()), m.pool, owner.TenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `DELETE FROM role_permissions WHERE permission='nodes.write' AND role_id IN (SELECT role_id FROM role_bindings WHERE principal_id=$1)`, key.PrincipalID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := trimMutation(t, m, owner, applied, "restore"); !errors.Is(err, authz.ErrForbidden) {
		t.Fatalf("restore broadened agent role: %v", err)
	}
	if !slices.Equal(trimScopesStored(t, m, owner, key.ID), []string{"nodes.read"}) {
		t.Fatal("failed restore changed scopes")
	}
}

func TestKeyTrimGrantFenceRechecksAfterInFlightUseCommits(t *testing.T) {
	m, owner, key, in := trimFixture(t)
	proposal := proposeTrim(t, m, owner, key.ID, in)
	prefix, secret, _ := parseBearer("Bearer " + key.Token)
	agent, ok, err := m.authenticateAgent(t.Context(), prefix, secret)
	if err != nil || !ok {
		t.Fatal("authentication failed")
	}
	pool, barrier, ctx := dbtest.BarrierPool(t, m.pool, func(sql string) bool {
		return strings.Contains(sql, "INSERT INTO agent_key_scope_usage")
	})
	grantDone := make(chan error, 1)
	go func() {
		// Exercise the usage fence directly, independently of InTenant's
		// earlier keyed-agent admission fence.
		grantDone <- db.InTenant(ctx, pool, owner.TenantID, func(tx pgx.Tx) error {
			return authz.RequireTx(ctx, tx, agent, "nodes.write", authz.Scope{})
		})
	}()
	holder := barrier.Wait(t, ctx)
	approvalDone, finished := make(chan error, 1), make(chan struct{})
	go func() { _, err := trimMutation(t, m, owner, proposal, "approve"); approvalDone <- err; close(finished) }()
	// PostgreSQL must show approval waiting for the grant's usage fence.
	// Entering its callback alone does not prove this interleaving.
	if lock := dbtest.BlockedOrDone(t, ctx, m.pool, holder, finished); lock != "advisory" {
		t.Fatalf("approval did not wait on the usage fence: %q", lock)
	}
	barrier.Release()
	if err := dbtest.Await(t, ctx, grantDone); err != nil {
		t.Fatal(err)
	}
	if err := dbtest.Await(t, ctx, approvalDone); !errors.Is(err, errTrimRecent) {
		t.Fatalf("usage/approval race: %v", err)
	}
	if !slices.Equal(trimScopesStored(t, m, owner, key.ID), key.Scopes) {
		t.Fatal("racing approval dropped a used scope")
	}
}

func TestKeyTrimGrantFenceDeniesStaleGrantAfterApprovalCommits(t *testing.T) {
	m, owner, key, in := trimFixture(t)
	proposal := proposeTrim(t, m, owner, key.ID, in)
	prefix, secret, _ := parseBearer("Bearer " + key.Token)
	agent, ok, err := m.authenticateAgent(t.Context(), prefix, secret)
	if err != nil || !ok {
		t.Fatal("authentication failed")
	}
	pool, barrier, ctx := dbtest.BarrierPool(t, m.pool, func(sql string) bool {
		return strings.Contains(sql, "FOR NO KEY UPDATE OF k")
	})
	originalPool := m.pool
	m.pool = pool
	approvalDone := make(chan error, 1)
	go func() { _, err := trimMutation(t, m, owner, proposal, "approve"); approvalDone <- err }()
	holder := barrier.Wait(t, ctx)
	grantDone, finished := make(chan error, 1), make(chan struct{})
	go func() {
		grantDone <- db.InTenant(ctx, originalPool, owner.TenantID, func(tx pgx.Tx) error {
			return authz.RequireTx(ctx, tx, agent, "nodes.write", authz.Scope{})
		})
		close(finished)
	}()
	if lock := dbtest.BlockedOrDone(t, ctx, originalPool, holder, finished); lock != "advisory" {
		t.Fatalf("grant did not wait on the usage fence: %q", lock)
	}
	barrier.Release()
	if err := dbtest.Await(t, ctx, approvalDone); err != nil {
		t.Fatal(err)
	}
	if err := dbtest.Await(t, ctx, grantDone); !errors.Is(err, authz.ErrForbidden) {
		t.Fatalf("stale scope granted after approval: %v", err)
	}
	if !slices.Equal(trimScopesStored(t, m, owner, key.ID), []string{"nodes.read"}) {
		t.Fatal("approval did not commit its reduction")
	}
	if err := db.InTenant(ctx, originalPool, owner.TenantID, func(tx pgx.Tx) error {
		var count int
		err := tx.QueryRow(ctx, `SELECT count(*) FROM agent_key_scope_usage WHERE key_id=$1 AND scope='nodes.write'`, key.ID).Scan(&count)
		if err == nil && count != 0 {
			t.Error("denied grant recorded usage")
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
}

func TestKeyTrimOppositeProposalsFenceCompleteKeyBatch(t *testing.T) {
	m, owner := keyFixture(t)
	scopes := []string{"approvals.request", "nodes.read", "nodes.write"}
	keys := []agentKeyCreatedJSON{
		decodeKey(t, keyRequest(m, owner, map[string]any{"name": "first-proposer", "scopes": scopes})),
		decodeKey(t, keyRequest(m, owner, map[string]any{"name": "second-proposer", "scopes": scopes})),
	}
	// Start with the higher authenticating key: taking self first instead of
	// the complete sorted batch would invert the order of these proposals.
	if keys[0].ID < keys[1].ID {
		keys[0], keys[1] = keys[1], keys[0]
	}
	agents := make([]tenant.Principal, 2)
	for i, key := range keys {
		prefix, secret, _ := parseBearer("Bearer " + key.Token)
		p, ok, err := m.authenticateAgent(t.Context(), prefix, secret)
		if err != nil || !ok {
			t.Fatal("authentication failed")
		}
		agents[i] = p
	}
	now := time.Now().UTC()
	m.trimNow = func() time.Time { return now }
	in := trimInput{RequestID: "10000000-0000-4000-8000-000000000001", Expected: scopes, Candidate: []string{"approvals.request", "nodes.read"}, ExpiresAt: now.Add(time.Hour), Evidence: trimEvidence{Summary: "Retain proposal and read permissions.", ObservedAt: now, Risks: []trimRisk{{Scope: "nodes.write", Evidence: "No recorded use; earlier use unknown.", Risk: "Writes fail."}}}}
	pool, barrier, ctx := dbtest.BarrierPool(t, m.pool, func(sql string) bool {
		return strings.Contains(sql, "pg_advisory_xact_lock(hashtextextended($1,615))")
	})
	first := *m
	first.pool = pool
	firstDone, secondDone := make(chan error, 1), make(chan error, 1)
	go func() { _, err := first.proposeKeyTrim(ctx, agents[0], keys[1].ID, in); firstDone <- err }()
	holder := barrier.Wait(t, ctx)
	finished := make(chan struct{})
	go func() { _, err := m.proposeKeyTrim(ctx, agents[1], keys[0].ID, in); secondDone <- err; close(finished) }()
	if lock := dbtest.BlockedOrDone(t, ctx, m.pool, holder, finished); lock != "advisory" {
		t.Fatalf("opposite proposal did not wait on the first key in the batch: %q", lock)
	}
	barrier.Release()
	if err := dbtest.Await(t, ctx, firstDone); err != nil {
		t.Fatal(err)
	}
	if err := dbtest.Await(t, ctx, secondDone); err != nil {
		t.Fatal(err)
	}
	for _, key := range keys {
		if !slices.Equal(trimScopesStored(t, m, owner, key.ID), scopes) {
			t.Fatal("agent proposal changed key scopes")
		}
	}
}

func TestKeyTrimProposalKeysetPagination(t *testing.T) {
	m, owner, key, in := trimFixture(t)
	for _, request := range []string{"10000000-0000-4000-8000-000000000001", "10000000-0000-4000-8000-000000000002", "10000000-0000-4000-8000-000000000003"} {
		in.RequestID = request
		proposeTrim(t, m, owner, key.ID, in)
	}
	seen := map[string]bool{}
	cursor := ""
	for page := 0; page < 3; page++ {
		w := trimHTTP(m, owner, "GET", "/api/key-trim-proposals?limit=1&cursor="+cursor, nil)
		var result struct {
			Items      []trimProposal `json:"items"`
			HasMore    bool           `json:"has_more"`
			NextCursor string         `json:"next_cursor"`
		}
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &result) != nil || len(result.Items) != 1 || seen[result.Items[0].ID] || result.HasMore != (page < 2) {
			t.Fatal("keyset page repeated, omitted or misreported an item")
		}
		seen[result.Items[0].ID] = true
		cursor = result.NextCursor
	}
}

func TestKeyTrimProposalPageReadFailure(t *testing.T) {
	m, owner, key, in := trimFixture(t)
	for _, request := range []string{"10000000-0000-4000-8000-000000000001", "10000000-0000-4000-8000-000000000002"} {
		in.RequestID = request
		proposeTrim(t, m, owner, key.ID, in)
	}
	const path = "/api/key-trim-proposals?limit=1"
	w := trimHTTP(m, owner, "GET", path, nil)
	var page struct {
		Items      []trimProposal `json:"items"`
		HasMore    bool           `json:"has_more"`
		NextCursor string         `json:"next_cursor"`
	}
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &page) != nil || len(page.Items) != 1 || !page.HasMore || page.NextCursor != page.Items[0].ID {
		t.Fatal("fixture must have a readable first item and another page")
	}
	// Keep both proposal rows so the ID query still reports another page, but
	// make loading the first proposal fail while decoding persisted evidence.
	if err := db.InTenant(dbtest.Seed(t.Context()), m.pool, owner.TenantID, func(tx pgx.Tx) error {
		result, err := tx.Exec(t.Context(), `UPDATE key_trim_proposals SET evidence='[]'::jsonb WHERE tenant_id=$1::uuid AND id=$2::uuid`, owner.TenantID, page.Items[0].ID)
		if err == nil && result.RowsAffected() != 1 {
			t.Fatal("fixture did not corrupt exactly the first proposal")
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	var readErr error
	m.inTenant = func(ctx context.Context, pool *pgxpool.Pool, tid string, fn func(pgx.Tx) error) error {
		readErr = db.InTenant(ctx, pool, tid, fn)
		return readErr
	}
	w = trimHTTP(m, owner, "GET", path, nil)
	var decodeErr *json.UnmarshalTypeError
	if !errors.As(readErr, &decodeErr) || decodeErr.Value != "array" || decodeErr.Type.Name() != "trimEvidence" {
		t.Fatalf("expected first proposal evidence decode failure, got %v", readErr)
	}
	var body map[string]any
	if w.Code != http.StatusInternalServerError || json.Unmarshal(w.Body.Bytes(), &body) != nil || len(body) != 1 || body["error"] != "internal" {
		t.Fatalf("expected only the internal error response, got %d: %s", w.Code, w.Body.String())
	}
}

func TestKeyTrimRestoreRechecksEditorAndOriginalCreatorCeilings(t *testing.T) {
	for _, narrow := range []string{"editor", "original-creator"} {
		t.Run(narrow, func(t *testing.T) {
			m, owner, key, in := trimFixture(t)
			proposal := proposeTrim(t, m, owner, key.ID, in)
			applied, err := trimMutation(t, m, owner, proposal, "approve")
			if err != nil {
				t.Fatal(err)
			}
			limited := tenant.Principal{TenantID: owner.TenantID, Kind: tenant.Person}
			if err := db.InTenant(dbtest.Seed(t.Context()), m.pool, owner.TenantID, func(tx pgx.Tx) error {
				if err := tx.QueryRow(t.Context(), `INSERT INTO principals(tenant_id,kind,name) VALUES($1,'person','Limited key manager') RETURNING id::text`, owner.TenantID).Scan(&limited.ID); err != nil {
					return err
				}
				var role string
				if err := tx.QueryRow(t.Context(), `INSERT INTO roles(tenant_id,key,name) VALUES($1,'limited_trim_manager','Limited key manager') RETURNING id::text`, owner.TenantID).Scan(&role); err != nil {
					return err
				}
				if _, err := tx.Exec(t.Context(), `INSERT INTO role_permissions(tenant_id,role_id,permission) SELECT $1,$2,unnest(ARRAY['keys.manage','nodes.read'])`, owner.TenantID, role); err != nil {
					return err
				}
				if _, err := tx.Exec(t.Context(), `INSERT INTO role_bindings(tenant_id,principal_id,role_id,scope_type) VALUES($1,$2,$3,'workspace')`, owner.TenantID, limited.ID, role); err != nil {
					return err
				}
				if narrow == "original-creator" {
					_, err := tx.Exec(t.Context(), `UPDATE agent_keys SET created_by_principal_id=$2 WHERE id=$1`, key.ID, limited.ID)
					return err
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			editor := owner
			if narrow == "editor" {
				editor = limited
			}
			if err := authz.Require(authz.BindPool(tenant.WithPrincipal(t.Context(), editor), m.pool), "keys.manage", authz.Scope{}); err != nil {
				t.Fatal("test editor lacks key management authority")
			}
			if _, err := trimMutation(t, m, editor, applied, "restore"); !errors.Is(err, authz.ErrForbidden) {
				t.Fatalf("restore bypassed %s ceiling: %v", narrow, err)
			}
			if !slices.Equal(trimScopesStored(t, m, owner, key.ID), []string{"nodes.read"}) {
				t.Fatal("rejected restore broadened key")
			}
		})
	}
}
