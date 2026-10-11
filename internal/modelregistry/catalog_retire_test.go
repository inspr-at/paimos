// SPDX-License-Identifier: AGPL-3.0-only
package modelregistry

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/modelactivation"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Risk: stale/aggregated facts override native successors, or lack of lifecycle
// evidence is reported as active. Inputs are recorded source fixtures plus
// explicit lifecycle test vectors, never network or harness logins.
func TestCatalogOutdatedLifecycleEvidence(t *testing.T) {
	at := time.Date(2026, 10, 11, 0, 0, 0, 0, time.UTC)
	native, err := ParseProviderCatalog("anthropic", catalogFixture(t, "anthropic"), at)
	if err != nil {
		t.Fatal(err)
	}
	fact := func(name, value, kind, source string) CatalogFact {
		return CatalogFact{Provider: "anthropic", ModelID: "claude-opus-5", Fact: name, Value: json.RawMessage(value), Source: source, SourceKind: kind, FieldPath: name, FetchedAt: at}
	}
	facts := append(native.Facts, fact("successor", `"claude-opus-5.5"`, "native", "provider-native"), fact("successor", `"claude-opus-6"`, "aggregator", "aggregator"))
	got := resolveLifecycle(facts, "anthropic:claude-opus-5", at)
	if got.State == nil || *got.State != "superseded" || got.Successor == nil || *got.Successor != "anthropic:claude-opus-5.5" || got.Source != "provider-native" {
		t.Fatalf("native successor lost: %+v", got)
	}
	for _, tc := range []struct{ name, value, want string }{{"lifecycle", `"deprecated"`, "deprecated"}, {"retirement_at", `"2026-11-01"`, "retiring"}, {"present", `false`, "vanished"}} {
		state := resolveLifecycle([]CatalogFact{fact(tc.name, tc.value, "native", "recorded-source")}, "anthropic:claude-opus-5", at)
		if state.State == nil || *state.State != tc.want || state.Source != "recorded-source" || state.Reason == "" {
			t.Fatal(tc, state)
		}
	}
	if got := resolveLifecycle(nil, "anthropic:unreported", at); got.State != nil || got.Successor != nil || got.Source != "" {
		t.Fatal("invented unknown lifecycle", got)
	}
}

// Risk: a sourced floating alias replaces concrete successors and puts the
// newest numbered version in the outdated tray, moving immutable pins to it.
func TestCatalogOutdatedSourcedAliasIsNotSuccessor(t *testing.T) {
	admin, _, _ := retireFixture(t)
	inRegistry(t, admin, func(tx pgx.Tx) error {
		return RecordCatalogFacts(t.Context(), tx, admin.TenantID, []CatalogFact{{
			Provider: "anthropic", ModelID: "opus", Route: "claude/opus",
			Fact: "context_window", Value: json.RawMessage(`200000`),
			Source: "recorded-harness", SourceKind: "harness", FieldPath: "context_window",
			FetchedAt: time.Date(2026, 10, 11, 0, 0, 0, 0, time.UTC),
		}})
	})
	preview := outdatedPreview(t, admin)
	old := requireOutdated(t, preview, "anthropic:claude-opus-5")
	if old.Successor == nil || *old.Successor != "anthropic:claude-opus-5.5" {
		t.Fatalf("numbered successor replaced by alias: %+v", old)
	}
	for _, version := range preview.Versions {
		if version.Version == "anthropic:claude-opus-5.5" {
			t.Fatalf("newest numbered model became outdated: %+v", version)
		}
	}
}

type lifecycleTestProfile struct {
	Profile
	Lifecycle *CatalogLifecycle `json:"lifecycle"`
}

// Risk: display metadata aborts dispatch reads due to the board's profile cap,
// unrelated source identities, or an evidence budget instead of returning null.
func TestCatalogProfileLifecycleBestEffort(t *testing.T) {
	t.Run("retired profiles do not impose board cap", func(t *testing.T) {
		admin, old, _ := retireFixture(t)
		before := boardDecode[[]lifecycleTestProfile](t, boardCall(t, admin, "GET", "/api/models", nil, ""), 200)
		inRegistry(t, admin, func(tx pgx.Tx) error {
			_, err := tx.Exec(t.Context(), `WITH added AS (
				INSERT INTO model_profiles(tenant_id,slug,version,harness,family,model,effort,tier,enabled)
				SELECT $1,'retired-fixture-'||n,'1','claude','anthropic','claude-opus-4','high','strong',false
				FROM generate_series(1,513) n RETURNING id
			) INSERT INTO model_profile_retirements(tenant_id,profile_id,retired_by,reason)
			SELECT $1,id,$2,'recorded retired fixture' FROM added`, admin.TenantID, admin.ID)
			return err
		})
		profiles := boardDecode[[]lifecycleTestProfile](t, boardCall(t, admin, "GET", "/api/models", nil, ""), 200)
		if len(profiles) != len(before)+513 {
			t.Fatalf("profile list truncated: got %d, want %d", len(profiles), len(before)+513)
		}
		found := false
		for _, profile := range profiles {
			if profile.ID == old.ID {
				found = true
				if profile.Lifecycle == nil || profile.Lifecycle.Successor == nil || *profile.Lifecycle.Successor != "anthropic:claude-opus-5.5" {
					t.Fatalf("board cap suppressed available lifecycle: %+v", profile)
				}
			}
		}
		if !found {
			t.Fatal("listed profile disappeared")
		}
	})
	t.Run("unrelated identities do not consume lifecycle budget", func(t *testing.T) {
		admin, old, _ := retireFixture(t)
		inRegistry(t, admin, func(tx pgx.Tx) error {
			_, err := tx.Exec(t.Context(), `INSERT INTO model_catalog_facts(tenant_id,provider,model_id,fact,value,source,source_kind,field_path,fetched_at)
				SELECT $1,'anthropic','unrelated-'||n,'context_window','200000','recorded-harness','harness','context_window',now()
				FROM generate_series(1,20001) n`, admin.TenantID)
			return err
		})
		profiles := boardDecode[[]lifecycleTestProfile](t, boardCall(t, admin, "GET", "/api/models", nil, ""), 200)
		found := false
		for _, profile := range profiles {
			if profile.ID == old.ID {
				found = true
				if profile.Lifecycle == nil || profile.Lifecycle.Successor == nil || *profile.Lifecycle.Successor != "anthropic:claude-opus-5.5" {
					t.Fatalf("unrelated models suppressed available lifecycle: %+v", profile)
				}
			}
		}
		if !found {
			t.Fatal("listed profile disappeared")
		}
	})
	t.Run("evidence overflow returns profiles with null lifecycle", func(t *testing.T) {
		admin, _, _ := retireFixture(t)
		before := boardDecode[[]lifecycleTestProfile](t, boardCall(t, admin, "GET", "/api/models", nil, ""), 200)
		inRegistry(t, admin, func(tx pgx.Tx) error {
			_, err := tx.Exec(t.Context(), `INSERT INTO model_catalog_facts(tenant_id,provider,model_id,route,fact,value,source,source_kind,field_path,fetched_at)
				SELECT $1,'anthropic','claude-opus-5','claude/fixture-'||n,'present','true','recorded-harness','harness','present',now()
				FROM generate_series(1,32001) n`, admin.TenantID)
			return err
		})
		profiles := boardDecode[[]lifecycleTestProfile](t, boardCall(t, admin, "GET", "/api/models", nil, ""), 200)
		if len(profiles) != len(before) {
			t.Fatal("evidence overflow changed profile list")
		}
		for i, profile := range profiles {
			if profile.ID != before[i].ID || profile.Lifecycle != nil {
				t.Fatalf("overflow must preserve profiles and report null lifecycle: %+v", profile)
			}
		}
	})
	t.Run("failed lifecycle query returns profiles with null lifecycle", func(t *testing.T) {
		admin, _, _ := retireFixture(t)
		before := boardDecode[[]lifecycleTestProfile](t, boardCall(t, admin, "GET", "/api/models", nil, ""), 200)
		probe := &lifecycleQueryBarrier{fail: true}
		config := appPool.Config()
		config.ConnConfig.Tracer = probe
		pool, err := pgxpool.NewWithConfig(t.Context(), config)
		if err != nil {
			t.Fatal(err)
		}
		defer pool.Close()
		r := httptest.NewRequest("GET", "/api/models", nil).WithContext(tenant.WithPrincipal(t.Context(), admin))
		w := httptest.NewRecorder()
		(&Module{pool: pool}).list(w, r)
		profiles := boardDecode[[]lifecycleTestProfile](t, w, 200)
		if probe.calls.Load() != 1 || len(profiles) != len(before) {
			t.Fatal("fixture did not fail exactly one lifecycle query or preserve profiles")
		}
		for i, profile := range profiles {
			if profile.ID != before[i].ID || profile.Lifecycle != nil {
				t.Fatalf("failed enrichment must preserve profiles and report null: %+v", profile)
			}
		}
	})
}

type lifecycleQueryBarrier struct {
	entered chan struct{}
	release chan struct{}
	once    sync.Once
	calls   atomic.Int32
	fail    bool
}

func (b *lifecycleQueryBarrier) TraceQueryStart(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	if strings.Contains(data.SQL, "aeon_model_version_newer") {
		b.calls.Add(1)
		if b.fail {
			ctx, cancel := context.WithCancel(ctx)
			cancel()
			return ctx
		}
		b.once.Do(func() {
			close(b.entered)
			select {
			case <-b.release:
			case <-ctx.Done():
			}
		})
	}
	return ctx
}
func (*lifecycleQueryBarrier) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}

// Risk: optional successor calculation holds the global tenant writer fence.
// A query barrier proves overlap; NOWAIT rejects the specific lock conflict.
func TestCatalogProfileLifecycleReleasesTenantFence(t *testing.T) {
	admin, old, _ := retireFixture(t)
	var older Profile
	inRegistry(t, admin, func(tx pgx.Tx) error {
		var err error
		older, err = insertActivatedProfile(t.Context(), tx, admin, profileWrite{Slug: "fence-older-opus", Version: "1", Harness: "claude", Family: "anthropic", Model: "claude-opus-4", Effort: "high", Tier: "strong"}, true, modelactivation.Person)
		return err
	})
	barrier := &lifecycleQueryBarrier{entered: make(chan struct{}), release: make(chan struct{})}
	config := appPool.Config()
	config.ConnConfig.Tracer = barrier
	pool, err := pgxpool.NewWithConfig(t.Context(), config)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(barrier.release) }) }
	defer release()
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		r := httptest.NewRequest("GET", "/api/models", nil).WithContext(tenant.WithPrincipal(t.Context(), admin))
		w := httptest.NewRecorder()
		(&Module{pool: pool}).list(w, r)
		done <- w
	}()
	select {
	case <-barrier.entered:
	case w := <-done:
		t.Fatalf("list finished without successor query: %d %s", w.Code, w.Body.String())
	case <-time.After(10 * time.Second):
		t.Fatal("successor query barrier was not reached")
	}
	err = db.InTenant(t.Context(), appPool, admin.TenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `SELECT id FROM tenants WHERE id=$1 FOR NO KEY UPDATE NOWAIT`, admin.TenantID)
		return err
	})
	if err != nil {
		t.Fatalf("lifecycle held tenant writer fence: %v", err)
	}
	release()
	profiles := boardDecode[[]lifecycleTestProfile](t, finishEditorRequest(t, done), 200)
	if barrier.calls.Load() != 1 {
		t.Fatalf("successor comparisons used %d queries, want one", barrier.calls.Load())
	}
	found := 0
	for _, profile := range profiles {
		if profile.ID == old.ID || profile.ID == older.ID {
			if profile.Lifecycle == nil || profile.Lifecycle.Successor == nil || *profile.Lifecycle.Successor != "anthropic:claude-opus-5.5" {
				t.Fatalf("batched successor query failed: %+v", profile)
			}
			found++
		}
	}
	if found != 2 {
		t.Fatal("batched successor query lost a listed profile")
	}
}

func retireFixture(t *testing.T) (tenant.Principal, Profile, Profile) {
	t.Helper()
	reset(t)
	admin := makePrincipal(t, "catalog-retire", "person", "Owner", []string{"admin"})
	var old, next Profile
	inRegistry(t, admin, func(tx pgx.Tx) error {
		pending, err := prepareCatalogDeferred(t.Context(), tx, admin)
		if err != nil {
			return err
		}
		old, err = insertActivatedProfile(t.Context(), tx, admin, profileWrite{Slug: "old-opus", Version: "1", Harness: "claude", Family: "anthropic", Model: "claude-opus-5", Effort: "high", Tier: "strong"}, true, modelactivation.Person)
		if err != nil {
			return err
		}
		next, err = insertActivatedProfile(t.Context(), tx, admin, profileWrite{Slug: "next-opus", Version: "1", Harness: "claude", Family: "anthropic", Model: "claude-opus-5.5", Effort: "high", Tier: "strong"}, true, modelactivation.Person)
		if err != nil {
			return err
		}
		at := time.Date(2026, 10, 11, 0, 0, 0, 0, time.UTC)
		recorded, err := ParseProviderCatalog("anthropic", catalogFixture(t, "anthropic"), at)
		if err != nil {
			return err
		}
		// Controlled future-version scenario from the parent's acceptance: retain
		// the recorded source's fields, changing only the model under comparison.
		future := append([]CatalogFact(nil), recorded.Facts...)
		for i := range future {
			future[i].ModelID = "claude-opus-5.5"
		}
		if err := RecordCatalogFacts(t.Context(), tx, admin.TenantID, append(recorded.Facts, future...)); err != nil {
			return err
		}
		if _, err := tx.Exec(t.Context(), `INSERT INTO model_role_routes(tenant_id,role,priority,profile_id) VALUES($1,'build',1,$2)`, admin.TenantID, old.ID); err != nil {
			return err
		}
		return flushCatalogChanges(t.Context(), tx, admin, pending)
	})
	return admin, old, next
}
func outdatedPreview(t *testing.T, p tenant.Principal) outdatedDocument {
	t.Helper()
	return boardDecode[outdatedDocument](t, boardCall(t, p, "GET", "/api/models/outdated", nil, ""), 200)
}
func requireOutdated(t *testing.T, doc outdatedDocument, id string) outdatedVersion {
	t.Helper()
	for _, version := range doc.Versions {
		if version.Version == id {
			return version
		}
	}
	t.Fatalf("missing outdated %s: %+v", id, doc)
	return outdatedVersion{}
}
func retireState(t *testing.T, p tenant.Principal) retireSnapshot {
	t.Helper()
	var out retireSnapshot
	inRegistry(t, p, func(tx pgx.Tx) error { var err error; out, err = loadRetireSnapshot(t.Context(), tx); return err })
	return out
}

// Risk: Opus 5 is not superseded by the numerically newer sourced 5.5; a batch
// changes only some references, rewrites caps/grants, or Undo clobbers later edits.
func TestCatalogOutdatedBulkRetireAndUndo(t *testing.T) {
	admin, old, next := retireFixture(t)
	account := minimalAccount(t, admin, old)
	inRegistry(t, admin, func(tx pgx.Tx) error {
		older, err := insertActivatedProfile(t.Context(), tx, admin, profileWrite{Slug: "older-opus", Version: "1", Harness: "claude", Family: "anthropic", Model: "claude-opus-4", Effort: "high", Tier: "strong"}, true, modelactivation.Person)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(t.Context(), `INSERT INTO model_role_routes(tenant_id,role,priority,profile_id) VALUES($1,'build',2,$2)`, admin.TenantID, older.ID); err != nil {
			return err
		}
		var scope, kind string
		if err := tx.QueryRow(t.Context(), `INSERT INTO work_kinds(tenant_id,slug,label) VALUES($1,'retire-kind','Retire kind') RETURNING id::text`, admin.TenantID).Scan(&kind); err != nil {
			return err
		}
		if err := tx.QueryRow(t.Context(), `INSERT INTO model_pref_scopes(tenant_id,level) VALUES($1,'default') RETURNING id::text`, admin.TenantID).Scan(&scope); err != nil {
			return err
		}
		if _, err := tx.Exec(t.Context(), `INSERT INTO model_pref_rows(tenant_id,scope_id,kind_id,level,locked) VALUES($1,$2,$3,'default',true)`, admin.TenantID, scope, kind); err != nil {
			return err
		}
		_, err = tx.Exec(t.Context(), `INSERT INTO model_pref_cells(tenant_id,scope_id,kind_id,bucket,mode,profile_id) VALUES($1,$2,$3,'normal','pinned',$4)`, admin.TenantID, scope, kind, old.ID)
		return err
	})
	before := retireState(t, admin)
	preview := outdatedPreview(t, admin)
	version := requireOutdated(t, preview, "anthropic:claude-opus-5")
	if version.State == nil || *version.State != "superseded" || version.Successor == nil || *version.Successor != "anthropic:claude-opus-5.5" || version.Source != "aeon_model_line/aeon_model_version_newer" || len(version.Uses) != 2 {
		t.Fatal(version)
	}
	result := boardDecode[retireResult](t, boardCall(t, admin, "POST", "/api/models/outdated/retire", retireWrite{Versions: []string{version.Version, "anthropic:claude-opus-4"}, Revision: preview.Revision}, ""), 200)
	if len(result.Removed) != 2 || len(result.Moved) != 3 || result.UndoToken == "" || eventCount(t, admin, "model.outdated_retired") != 1 {
		t.Fatal(result)
	}
	inRegistry(t, admin, func(tx pgx.Tx) error {
		var pin string
		var granted, eligible bool
		if err := tx.QueryRow(t.Context(), `SELECT profile_id::text FROM model_role_routes WHERE role='build' AND priority=1`).Scan(&pin); err != nil {
			return err
		}
		if pin != next.ID {
			t.Fatal("role reference did not move", pin)
		}
		var routes int
		if err := tx.QueryRow(t.Context(), `SELECT count(*) FROM model_role_routes WHERE role='build'`).Scan(&routes); err != nil {
			return err
		}
		if routes != 1 {
			t.Fatal("compatible successor routes were not deduplicated", routes)
		}

		if err := tx.QueryRow(t.Context(), `SELECT profile_id::text FROM model_pref_cells WHERE mode='pinned'`).Scan(&pin); err != nil {
			return err
		}
		if pin != next.ID {
			t.Fatal("locked legacy pin did not move", pin)
		}
		if err := tx.QueryRow(t.Context(), `SELECT allowed_model_profile_ids=ARRAY[$2::uuid],aeon_account_allows_profile(harness,allowed_model_profile_ids,$3) FROM agent_accounts WHERE id=$1`, account, old.ID, next.ID).Scan(&granted, &eligible); err != nil {
			return err
		}
		if !granted || !eligible {
			t.Fatal("successor policy or immutable grants changed")
		}
		return nil
	})
	after := retireState(t, admin)
	boardDecode[map[string]any](t, boardCall(t, admin, "POST", "/api/models/outdated/undo", map[string]any{"undo_token": result.UndoToken}, ""), 200)
	restored := retireState(t, admin)
	for _, table := range retireTables {
		if table.revision {
			continue
		}
		if !reflect.DeepEqual(before[table.name], restored[table.name]) {
			t.Fatal("Undo lost references", table.name)
		}
	}
	if reflect.DeepEqual(before["model_pref_scopes"], restored["model_pref_scopes"]) || eventCount(t, admin, "model.outdated_restored") != 1 {
		t.Fatal("Undo did not advance revision or audit")
	}
	inRegistry(t, admin, func(tx pgx.Tx) error {
		var revision int
		if err := tx.QueryRow(t.Context(), `SELECT (after->'model_pref_scopes'->0->>'revision')::int FROM events WHERE type='model.outdated_restored'`).Scan(&revision); err != nil {
			return err
		}
		if revision != 3 {
			t.Fatal("Undo audit did not record actual monotonic revision", revision)
		}
		return nil
	})
	boardDecode[map[string]any](t, boardCall(t, admin, "POST", "/api/models/outdated/undo", map[string]any{"undo_token": result.UndoToken}, ""), 409)
	if reflect.DeepEqual(before["model_profile_retirements"], after["model_profile_retirements"]) {
		t.Fatal("profiles were never retired")
	}
	preview = outdatedPreview(t, admin)
	second := boardDecode[retireResult](t, boardCall(t, admin, "POST", "/api/models/outdated/retire", retireWrite{Versions: []string{version.Version}, Revision: preview.Revision}, ""), 200)
	inRegistry(t, admin, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE model_role_routes SET reason='Later edit' WHERE role='build'`)
		return err
	})
	edited := retireState(t, admin)
	rejected := boardCall(t, admin, "POST", "/api/models/outdated/undo", map[string]any{"undo_token": second.UndoToken}, "")
	if rejected.Code != 409 || !strings.Contains(rejected.Body.String(), "references changed") {
		t.Fatal("wrong stale Undo result", rejected.Body.String())
	}
	if !reflect.DeepEqual(edited, retireState(t, admin)) {
		t.Fatal("stale Undo wrote partial restoration")
	}
}

// Risk: a stale preview, denied authority, second invalid selection or failed
// event leaves a partial retirement; readers learn another person's choices.
func TestCatalogOutdatedRetireSafetyAndPrivacy(t *testing.T) {
	admin, old, next := retireFixture(t)
	member := addPrincipal(t, admin.TenantID, "person", "Member", []string{"member"})
	other := addPrincipal(t, admin.TenantID, "person", "Other", []string{"member"})
	inRegistry(t, admin, func(tx pgx.Tx) error {
		if _, err := tx.Exec(t.Context(), `INSERT INTO model_profile_retirements(tenant_id,profile_id,reason,retired_by) VALUES($1,$2,'Fixture inactive version',$3)`, admin.TenantID, next.ID, admin.ID); err != nil {
			return err
		}
		var id string
		if err := tx.QueryRow(t.Context(), `INSERT INTO model_pref_profiles(tenant_id,scope,person_id) VALUES($1,'person',$2) RETURNING id::text`, admin.TenantID, other.ID).Scan(&id); err != nil {
			return err
		}
		// Pin a source-reported, different-line successor so stable-line uses also move.
		raw, _ := json.Marshal("claude-sonnet-5")
		if err := RecordCatalogFacts(t.Context(), tx, admin.TenantID, []CatalogFact{{Provider: "anthropic", ModelID: old.Model, Fact: "successor", Value: raw, Source: "harness:recorded-lifecycle-vector", SourceKind: "harness", FieldPath: "upgrade", FetchedAt: time.Date(2026, 10, 11, 0, 0, 0, 0, time.UTC)}}); err != nil {
			return err
		}
		if _, err := insertActivatedProfile(t.Context(), tx, admin, profileWrite{Slug: "native-next", Version: "1", Harness: "claude", Family: "anthropic", Model: "claude-sonnet-5", Effort: "high", Tier: "strong"}, true, modelactivation.Person); err != nil {
			return err
		}
		if _, err := tx.Exec(t.Context(), `INSERT INTO model_pref_orders(tenant_id,profile_id,column_key,situation,rank) VALUES($1,$2,'other','first',ARRAY['anthropic:opus'])`, admin.TenantID, id); err != nil {
			return err
		}
		var workspace string
		if err := tx.QueryRow(t.Context(), `INSERT INTO model_pref_profiles(tenant_id,scope) VALUES($1,'workspace') RETURNING id::text`, admin.TenantID).Scan(&workspace); err != nil {
			return err
		}
		if _, err := tx.Exec(t.Context(), `INSERT INTO model_pref_orders(tenant_id,profile_id,column_key,situation,rank,not_allowed) VALUES($1,$2,'other','first',ARRAY['anthropic:opus'],'{}'),($1,$2,'docs','fix',ARRAY['anthropic:sonnet','anthropic:opus'],'{}'),($1,$2,'backend','stuck',ARRAY['anthropic:sonnet'],ARRAY['anthropic:opus'])`, admin.TenantID, workspace); err != nil {
			return err
		}
		if _, err := tx.Exec(t.Context(), `INSERT INTO model_rules(tenant_id,scope,column_key,line,lock,why,set_by) VALUES($1,'workspace','other','anthropic:opus','top','Keep locked',$2)`, admin.TenantID, admin.ID); err != nil {
			return err
		}
		_, err := tx.Exec(t.Context(), `INSERT INTO model_rule_revisions(tenant_id,scope,scope_key) VALUES($1,'workspace','')`, admin.TenantID)
		return err

	})
	preview := outdatedPreview(t, admin)
	id := "anthropic:claude-opus-5"
	body := retireWrite{Versions: []string{id}, Revision: preview.Revision}
	own := requireOutdated(t, outdatedPreview(t, member), id)
	if !own.Incomplete {
		t.Fatal("other person's use not marked hidden")
	}
	for _, use := range own.Uses {
		if use.Who == other.ID {
			t.Fatal("private choice leaked")
		}
	}
	for _, tc := range []struct {
		p      tenant.Principal
		body   retireWrite
		status int
	}{{member, body, 403}, {admin, retireWrite{Versions: []string{id, "anthropic:missing"}, Revision: preview.Revision}, 409}, {admin, retireWrite{Versions: []string{id}, Revision: strings.Repeat("0", 64)}, 409}} {
		before := retireState(t, admin)
		boardDecode[map[string]any](t, boardCall(t, tc.p, "POST", "/api/models/outdated/retire", tc.body, ""), tc.status)
		if !reflect.DeepEqual(before, retireState(t, admin)) {
			t.Fatal("failed retirement changed state")
		}
	}
	if eventCount(t, admin, "model.outdated_retired") != 0 {
		t.Fatal("rejected writes reported success")
	}
	beforeMove := retireState(t, admin)
	moved := boardDecode[retireResult](t, boardCall(t, admin, "POST", "/api/models/outdated/retire", body, ""), 200)
	if len(moved.Moved) != 6 {
		t.Fatal("preview omitted a Default, fallback, never, lock or person reference", moved.Moved)
	}
	inRegistry(t, admin, func(tx pgx.Tx) error {
		var line string
		if err := tx.QueryRow(t.Context(), `SELECT line FROM model_rules WHERE column_key='other'`).Scan(&line); err != nil {
			return err
		}
		if line != "anthropic:sonnet" {
			t.Fatal("lock did not move", line)
		}
		var count int
		if err := tx.QueryRow(t.Context(), `SELECT count(*) FROM model_pref_orders WHERE 'anthropic:opus'=ANY(rank) OR 'anthropic:opus'=ANY(not_allowed)`).Scan(&count); err != nil {
			return err
		}
		if count != 0 {
			t.Fatal("fallback or never reference did not move")
		}
		return nil
	})
	boardDecode[map[string]any](t, boardCall(t, admin, "POST", "/api/models/outdated/undo", map[string]any{"undo_token": moved.UndoToken}, ""), 200)
	afterUndo := retireState(t, admin)
	for _, table := range retireTables {
		if !table.revision && !reflect.DeepEqual(beforeMove[table.name], afterUndo[table.name]) {
			t.Fatal("native successor Undo lost a reference", table.name)
		}
	}
	preview = outdatedPreview(t, admin)
	body.Revision = preview.Revision
	// Inject at the actual event INSERT after references and retirements changed.
	if _, err := adminPool.Exec(t.Context(), `CREATE SEQUENCE retire_event_probe; CREATE FUNCTION retire_event_fail() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.type='model.outdated_retired' THEN PERFORM nextval('retire_event_probe'); RAISE EXCEPTION 'retire injected failure'; END IF; RETURN NEW; END $$; CREATE TRIGGER retire_event_fail BEFORE INSERT ON events FOR EACH ROW EXECUTE FUNCTION retire_event_fail()`); err != nil {
		t.Fatal(err)
	}
	if _, err := adminPool.Exec(t.Context(), `GRANT USAGE ON SEQUENCE retire_event_probe TO `+pgx.Identifier{testDB.Role}.Sanitize()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := adminPool.Exec(context.Background(), `DROP TRIGGER retire_event_fail ON events; DROP FUNCTION retire_event_fail(); DROP SEQUENCE retire_event_probe`); err != nil {
			t.Error(err)
		}
	})
	before := retireState(t, admin)
	failed := boardCall(t, admin, "POST", "/api/models/outdated/retire", body, "")
	var reached bool
	if err := adminPool.QueryRow(t.Context(), `SELECT is_called FROM retire_event_probe`).Scan(&reached); err != nil {
		t.Fatal(err)
	}
	if failed.Code != 500 || !reached {
		t.Fatal("write did not reach injected event failure", failed.Code, failed.Body.String())
	}
	if !reflect.DeepEqual(before, retireState(t, admin)) {
		t.Fatal("event failure left partial retirement")
	}

}

// Risk: an outage, truncated response or stale refresh marks a model vanished;
// only complete fresh responses can remove source presence, and return restores it.
func TestCatalogOutdatedCompleteListingPresence(t *testing.T) {
	admin, _, _ := retireFixture(t)
	at := time.Date(2026, 10, 11, 1, 0, 0, 0, time.UTC)
	inRegistry(t, admin, func(tx pgx.Tx) error {
		complete, err := ParseProviderCatalog("anthropic", catalogFixture(t, "anthropic"), at)
		if err != nil {
			return err
		}
		if err := recordCatalogPresence(t.Context(), tx, admin.TenantID, "anthropic", at, complete); err != nil {
			return err
		}
		if err := recordCatalogPresence(t.Context(), tx, admin.TenantID, "anthropic", at.Add(time.Hour), ProviderCatalog{}); err != nil {
			return err
		}
		if err := recordCatalogPresence(t.Context(), tx, admin.TenantID, "anthropic", at.Add(-time.Hour), complete); err != nil {
			return err
		}
		models, err := loadLifecycleCatalog(t.Context(), tx, at)
		if err != nil {
			return err
		}
		for _, model := range models {
			if model.Identity == "anthropic:claude-opus-5" && (model.Lifecycle.State == nil || *model.Lifecycle.State != "vanished") {
				t.Fatal("stale listing restored presence", model.Lifecycle)
			}
		}
		return recordCatalogPresence(t.Context(), tx, admin.TenantID, "anthropic", at.Add(2*time.Hour), complete)
	})
	found := false
	inRegistry(t, admin, func(tx pgx.Tx) error {
		models, err := loadLifecycleCatalog(t.Context(), tx, at.Add(2*time.Hour))
		if err != nil {
			return err
		}
		for _, model := range models {
			if model.Identity != "anthropic:claude-opus-5" {
				continue
			}
			found = true
			if model.Lifecycle.State == nil || *model.Lifecycle.State != "active" {
				t.Fatal("returned source model stayed vanished", model.Lifecycle)
			}
		}
		return nil
	})
	if !found {
		t.Fatal("returned source model missing from lifecycle projection")
	}
	for _, version := range outdatedPreview(t, admin).Versions {
		if version.Version == "anthropic:claude-opus-5" {
			t.Fatal("returned active model remains in outdated tray", version)
		}
	}
}

// Risk: authorization checked before the tenant fence survives a concurrent
// role revocation. The lock waiter proves the mutation overlaps the revocation.
func TestCatalogOutdatedRetireRevocationFence(t *testing.T) {
	admin, _, _ := retireFixture(t)
	preview := outdatedPreview(t, admin)
	tx, err := appPool.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(context.Background())
	if _, err := tx.Exec(t.Context(), `SELECT set_config('aeon.tenant_id',$1,true)`, admin.TenantID); err != nil {
		t.Fatal(err)
	}
	if err := db.LockTenant(t.Context(), tx, admin.TenantID); err != nil {
		t.Fatal(err)
	}
	var pid int
	if err := tx.QueryRow(t.Context(), `SELECT pg_backend_pid()`).Scan(&pid); err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(retireWrite{Versions: []string{"anthropic:claude-opus-5"}, Revision: preview.Revision})
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		mux := http.NewServeMux()
		(&Module{pool: appPool}).Mount(mux)
		r := httptest.NewRequest("POST", "/api/models/outdated/retire", strings.NewReader(string(body)))
		r = r.WithContext(tenant.WithPrincipal(r.Context(), admin))
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		done <- w
	}()
	waitEditorBlocked(t, pid)
	if _, err := tx.Exec(t.Context(), `UPDATE role_bindings SET role_id=(SELECT id FROM roles WHERE key='member') WHERE principal_id=$1`, admin.ID); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	result := finishEditorRequest(t, done)
	if result.Code != 403 {
		t.Fatal(fmt.Sprintf("revoked mutation: %d %s", result.Code, result.Body.String()))
	}
	if eventCount(t, admin, "model.outdated_retired") != 0 {
		t.Fatal("revocation permitted retirement")
	}
}
