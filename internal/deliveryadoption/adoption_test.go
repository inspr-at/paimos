// SPDX-License-Identifier: AGPL-3.0-only
package deliveryadoption

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/delivery"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

type fixture struct {
	d                *dbtest.DB
	s                *Service
	p                tenant.Principal
	a                Authority
	clock            time.Time
	provider         *fakeProvider
	project, release string
	kinds            map[string]string
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	f := &fixture{d: dbtest.Open(t), clock: time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC), kinds: map[string]string{}}
	if err := f.d.Admin.QueryRow(t.Context(), `INSERT INTO tenants(slug,name) VALUES('adoption','adoption') RETURNING id::text`).Scan(&f.p.TenantID); err != nil {
		t.Fatal(err)
	}
	f.p.Kind = tenant.Person
	f.exec(t, func(ctx context.Context, tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `INSERT INTO principals(tenant_id,kind,name) VALUES($1,'person','Local deployment owner') RETURNING id::text`, f.p.TenantID).Scan(&f.p.ID); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `SELECT slug,id::text FROM node_kinds`)
		if err != nil {
			return err
		}
		for rows.Next() {
			var kind, id string
			if err = rows.Scan(&kind, &id); err != nil {
				rows.Close()
				return err
			}
			f.kinds[kind] = id
		}
		err = rows.Err()
		rows.Close()
		return err
	})
	dbtest.BindRole(t, f.d, f.p.TenantID, f.p.ID, "owner")
	f.a = Authority{Executor: f.p, Authorizer: f.p, Reference: "local-authority/E"}
	cfg := Config{Instance: "test-instance", Artifact: "sha256:" + sum([]byte("E")), PreFirstAdoptionPin: "pre-first-pin", ConsumersReady: true, WritersStopped: true, RecoveryReconciled: true, Authorities: []Authority{f.a}, Quota: Quota{BundleBytes: 1024, RestoreBytes: 4096, OperationSlots: 8, ActiveBytes: 2048, FailedBytes: 2048, ProtectedBytes: 1 << 20}}
	cfg.RollbackFloor = cfg.Artifact
	f.provider = &fakeProvider{instance: cfg.Instance, results: map[string]ProviderResult{}, bundles: map[string]string{}, clock: func() time.Time { return f.clock }}
	var err error
	f.s, err = New(f.d.App, cfg, f.provider, FileReports{Root: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	f.s = f.s.WithClock(func() time.Time { return f.clock })
	f.project = f.node(t, "project", "PR-1", "", "open")
	return f
}
func (f *fixture) exec(t *testing.T, fn func(context.Context, pgx.Tx) error) {
	t.Helper()
	ctx := dbtest.Seed(t.Context())
	if err := db.InTenant(ctx, f.d.App, f.p.TenantID, func(tx pgx.Tx) error { return fn(ctx, tx) }); err != nil {
		t.Fatal(err)
	}
}
func (f *fixture) sql(t *testing.T, q string, args ...any) {
	t.Helper()
	f.exec(t, func(ctx context.Context, tx pgx.Tx) error { _, err := tx.Exec(ctx, q, args...); return err })
}
func (f *fixture) count(t *testing.T, q string, args ...any) int {
	t.Helper()
	var n int
	f.exec(t, func(ctx context.Context, tx pgx.Tx) error { return tx.QueryRow(ctx, q, args...).Scan(&n) })
	return n
}
func (f *fixture) node(t *testing.T, kind, key, project, state string) string {
	t.Helper()
	var id string
	var parent any
	if project != "" {
		parent = project
	}
	f.exec(t, func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, `INSERT INTO nodes(tenant_id,kind_id,key,title,parent_id,state) VALUES($1,$2,$3,$3,$4,$5) RETURNING id::text`, f.p.TenantID, f.kinds[kind], key, parent, state).Scan(&id)
	})
	return id
}
func (f *fixture) journey(t *testing.T, project string) {
	t.Helper()
	f.sql(t, `INSERT INTO journey_projects(tenant_id,project_node_id) VALUES($1,$2) ON CONFLICT DO NOTHING`, f.p.TenantID, project)
}
func (f *fixture) legacyRelease(t *testing.T, project, key, state string, number int) string {
	t.Helper()
	f.journey(t, project)
	id := f.node(t, "release", key, project, "open")
	var at any
	if state == "released" || state == "superseded" {
		at = f.clock.Add(-time.Hour)
	}
	f.sql(t, `INSERT INTO journey_releases(tenant_id,project_node_id,release_node_id,number,state,released_at) VALUES($1,$2,$3,$4,$5,$6)`, f.p.TenantID, project, id, number, state, at)
	return id
}
func (f *fixture) member(t *testing.T, project, release, key string, pos int) string {
	t.Helper()
	id := f.node(t, "ticket", key, project, "open")
	f.sql(t, `INSERT INTO journey_tickets(tenant_id,project_node_id,ticket_node_id,release_node_id,walker_position,source) VALUES($1,$2,$3,$4,$5,'manual')`, f.p.TenantID, project, id, release, pos)
	return id
}
func (f *fixture) discover(t *testing.T) {
	t.Helper()
	_, _, err := f.s.Discover(t.Context(), "")
	if err != nil {
		t.Fatal(err)
	}
}
func (f *fixture) claim(t *testing.T, project string) job {
	t.Helper()
	f.discover(t)
	j, err := f.s.Claim(t.Context(), f.a, project)
	if err != nil {
		t.Fatal(err)
	}
	return j
}
func (f *fixture) prepare(t *testing.T, project string) job {
	t.Helper()
	j := f.claim(t, project)
	var r Report
	err := f.s.snapshot(t.Context(), f.p, func(tx pgx.Tx) error { var err error; r, err = f.s.plan(t.Context(), tx, j.Identity); return err })
	if err != nil {
		t.Fatal(err)
	}
	if !r.Eligible {
		t.Fatalf("fixture refused: %+v", r.Reasons)
	}
	if err = f.s.saveReport(t.Context(), f.a, &j, r); err != nil {
		t.Fatal(err)
	}
	if err = f.s.backup(t.Context(), f.a, &j); err != nil {
		t.Fatal(err)
	}
	return j
}

// fakeProvider models a durable catalog independent from the application DB.
// It enforces one bundle/project, idempotency and separate protected retention.
// No real backup service, credentials or production host is contacted.
type fakeProvider struct {
	mu                              sync.Mutex
	instance                        string
	results                         map[string]ProviderResult
	bundles                         map[string]string
	clock                           func() time.Time
	cleanupDown, loseBackupResponse bool
	executeCalls                    map[string]int
	onEffect                        func(string, ProviderRequest)
}

func (p *fakeProvider) Capabilities(context.Context) (Capabilities, error) {
	return Capabilities{true, true, true, true, true, true, true, true}, nil
}
func (p *fakeProvider) Lookup(_ context.Context, op Operation) (ProviderResult, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if op.Kind == "pre-first-adoption" {
		return ProviderResult{State: "complete", Pin: "pre-first-pin", Identity: Identity{Instance: p.instance}, IntegrityVerified: true, ChainVerified: true, Restored: true, Protected: true, Unadopted: true}, nil
	}
	v, ok := p.results[op.Key]
	if !ok {
		v = ProviderResult{Key: op.Key, Kind: op.Kind, Identity: op.Identity, State: "missing"}
	}
	return v, nil
}
func (p *fakeProvider) Execute(ctx context.Context, r ProviderRequest) (ProviderResult, error) {
	if p.onEffect != nil {
		p.onEffect(r.Operation.Kind, r)
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	op := r.Operation
	if p.executeCalls == nil {
		p.executeCalls = map[string]int{}
	}
	p.executeCalls[op.Key]++
	if v, ok := p.results[op.Key]; ok {
		return v, nil
	}
	v := ProviderResult{Key: op.Key, Kind: op.Kind, Identity: op.Identity, State: "complete", Handle: op.Kind + "/" + op.Key, Bytes: 512}
	backup := "backup/" + operationKey(op.Identity, "backup")
	restore := "restore/" + operationKey(op.Identity, "restore")
	switch op.Kind {
	case "backup":
		if old := p.bundles[op.Identity.Project]; old != "" && old != op.Identity.Attempt {
			return v, capacityError("backup_capacity")
		}
		if r.Quota.BundleBytes < 512 || len(p.bundles) >= int(r.Quota.FailedBytes/r.Quota.BundleBytes) {
			return v, capacityError("backup_capacity")
		}
		p.bundles[op.Identity.Project] = op.Identity.Attempt
		v.BackupRef = backup
		v.BackupDigest = sum([]byte(backup))
		v.ChainVerified = true
		v.IntegrityVerified = true
	case "restore", "pin":
		v.BackupRef = backup
		v.BackupDigest = sum([]byte(backup))
		v.RestoreRef = restore
		v.RestoreDigest = sum([]byte(restore))
		v.Fingerprint = r.Fingerprint
		v.IntegrityVerified = true
		v.ChainVerified = true
		v.Restored = true
		if op.Kind == "pin" {
			v.Pin = "pin/" + op.Key
			v.Protected = true
		}
	case "cancel":
		v.Bytes = 0
	case "cleanup":
		if p.cleanupDown {
			return v, errors.New("injected cleanup outage")
		}
		v.State = "reclaimed"
		v.Bytes = 0
		delete(p.bundles, op.Identity.Project)
		for key, old := range p.results {
			if old.Identity.Attempt == op.Identity.Attempt && (old.Kind == "restore" || r.Pin != "" && (old.Kind == "pin" || old.Kind == "backup")) {
				old.State = "reclaimed"
				old.Protected = false
				p.results[key] = old
			}
		}
	default:
		return v, errors.New("unexpected fake provider operation")
	}
	p.results[op.Key] = v
	if op.Kind == "backup" && p.loseBackupResponse {
		return v, errors.New("injected success-before-response crash")
	}
	return v, ctx.Err()
}
func (p *fakeProvider) List(_ context.Context, instance, cursor string, limit int) (CatalogPage, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if instance != p.instance || limit > 100 {
		return CatalogPage{}, errors.New("invalid namespace")
	}
	keys := []string{}
	for key := range p.results {
		if key > cursor {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	page := CatalogPage{}
	for _, key := range keys[:min(limit, len(keys))] {
		page.Items = append(page.Items, p.results[key])
	}
	if len(keys) > limit {
		page.Next = keys[limit-1]
	}
	return page, nil
}

func TestTaggedVersionsPreserveHistoricalBytes(t *testing.T) {
	for _, tc := range []struct {
		scheme, version string
		valid           bool
	}{
		{"legacy", "1.2.3-rc.1", true}, {"inspr-calendar-v1", "26.10.03", true}, {"inspr-calendar-v1", "26.10.03.12.01.59", true}, {"inspr-calendar-v2", "261003120159.0.0", true}, {"inspr-calver-3", "261003120159.0.0", true},
		{"", "1.2.3", false}, {"unknown", "261003120159.0.0", false}, {"inspr-calendar-v1", "26.02.30", false}, {"inspr-calendar-v2", "260230120159.0.0", false}, {"inspr-calendar-v2", "261003120159.0.0-rc.1", false}, {"legacy", strings.Repeat("a", 65), false},
	} {
		if got := ValidVersion(tc.scheme, tc.version); got != tc.valid {
			t.Errorf("tagged validation %q/%q=%v", tc.scheme, tc.version, got)
		}
	}
}

func TestAtomicAdoptionTombstonesOriginsAndD3Seed(t *testing.T) {
	f := newFixture(t)
	old := f.legacyRelease(t, f.project, "REL-1", "released", 1)
	next := f.legacyRelease(t, f.project, "REL-2", "planning", 2)
	f.sql(t, `UPDATE journey_releases SET version_scheme='legacy',version='1.2.3-rc.1' WHERE release_node_id=$1`, old)
	first := f.member(t, f.project, next, "TK-1", 9)
	deleted := f.member(t, f.project, old, "TK-2", 2)
	f.sql(t, `UPDATE nodes SET deleted_at=now() WHERE id=$1`, deleted)
	low := f.node(t, "task", "TSK-1", f.project, "open")
	urgent := f.node(t, "epic", "EP-1", f.project, "open")
	f.sql(t, `UPDATE nodes SET fields=jsonb_build_object('priority',$2::text) WHERE id=$1`, low, "low")
	f.sql(t, `UPDATE nodes SET fields=jsonb_build_object('priority',$2::text) WHERE id=$1`, urgent, "urgent")
	done := f.node(t, "ticket", "TK-3", f.project, "done")
	var before string
	f.exec(t, func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT jsonb_agg(to_jsonb(j) ORDER BY release_node_id)::text FROM journey_releases j`).Scan(&before)
	})
	j := f.prepare(t, f.project)
	unchanged, err := f.s.apply(t.Context(), f.a, j)
	if err != nil || unchanged {
		t.Fatalf("apply=%v unchanged=%v", err, unchanged)
	}
	if n := f.count(t, `SELECT count(*) FROM ships_in WHERE item_node_id=ANY($1::uuid[])`, []string{first, deleted, low, urgent}); n != 4 {
		t.Fatal("lossless placement count", n)
	}
	if n := f.count(t, `SELECT count(*) FROM ships_in WHERE item_node_id=$1`, done); n != 0 {
		t.Fatal("completed unassigned work was seeded")
	}
	if n := f.count(t, `SELECT count(*) FROM ships_in a JOIN ships_in b ON a.rank<b.rank WHERE a.item_node_id=$1 AND b.item_node_id=$2`, urgent, low); n != 1 {
		t.Fatal("D3 urgency ordering was not seeded")
	}
	if n := f.count(t, `SELECT count(*) FROM project_releases WHERE (release_node_id=$1 AND origin='adopted_released' AND version='1.2.3-rc.1') OR (release_node_id=$2 AND origin='adopted_planned' AND state='planned')`, old, next); n != 2 {
		t.Fatal("origin/version mapping", n)
	}
	var after string
	f.exec(t, func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT jsonb_agg(to_jsonb(j) ORDER BY release_node_id)::text FROM journey_releases j`).Scan(&after)
	})
	if before != after {
		t.Fatal("journey archive changed")
	}
	if n := f.count(t, `SELECT next_sequence FROM project_delivery WHERE project_node_id=$1`, f.project); n != 3 {
		t.Fatal("project-local high water", n)
	}
	if n := f.count(t, `SELECT count(*) FROM events WHERE type='delivery.adopted'`); n != 1 {
		t.Fatal("adopted event count", n)
	}
	if n := f.count(t, `SELECT count(*) FROM events WHERE type='release.published'`); n != 0 {
		t.Fatal("adoption published work")
	}
	unchanged, err = f.s.apply(t.Context(), f.a, j)
	if err != nil || !unchanged {
		t.Fatalf("replay=%v unchanged=%v", err, unchanged)
	}
	if err = f.s.reconcileJob(t.Context(), f.a, f.project); err != nil {
		t.Fatal(err)
	}
	v, err := f.s.Status(t.Context(), f.p, f.project)
	if err != nil || v.Mode != "releases" || v.Cleanup != "reclaimed" || v.BackupVerifiedAt == nil {
		t.Fatalf("status %+v %v", v, err)
	}
	pinOp := operationKey(j.Identity, "pin")
	pin, _ := f.provider.Lookup(t.Context(), Operation{Key: pinOp})
	if !pin.Protected || pin.State != "complete" {
		t.Fatal("successful recovery pin was reclaimed")
	}
}

func TestProductCounterIsExplicitlyBoundAndOrdinaryEmptyStartsAtOne(t *testing.T) {
	f := newFixture(t)
	product := f.node(t, "project", "PR-2", "", "open")
	f.sql(t, `UPDATE nodes SET fields='{"project_key":"AEON"}' WHERE id=$1`, product)
	legacy := f.legacyRelease(t, product, "REL-2", "planning", 1)
	f.sql(t, `UPDATE nodes SET title='Release 1' WHERE id=$1`, legacy)
	f.s.cfg.Product = Product{TenantID: f.p.TenantID, ProjectID: product, Repository: "inspr-at/paimos", HistoryDigest: sum([]byte("history")), VersionDigest: sum([]byte("version")), VersionSequence: 120, HistorySequences: []int{1, 120}}
	r, err := f.s.DryRun(t.Context(), f.p, f.project)
	if err != nil || !r.Eligible || r.NextSequence != 1 {
		t.Fatalf("ordinary counter=%d reasons=%+v err=%v", r.NextSequence, r.Reasons, err)
	}
	r, err = f.s.DryRun(t.Context(), f.p, product)
	if err != nil || !r.Eligible || r.NextSequence != 122 || len(r.Releases) != 1 || r.Releases[0].Sequence != 121 {
		t.Fatalf("product counter/mapping %+v %v", r, err)
	}
	ordinaryFingerprint := func() string {
		r, err := f.s.DryRun(t.Context(), f.p, f.project)
		if err != nil {
			t.Fatal(err)
		}
		return r.Fingerprint
	}
	before := ordinaryFingerprint()
	f.s.cfg.Product.VersionDigest = sum([]byte("new-version"))
	f.s.cfg.Product.VersionSequence = 125
	if ordinaryFingerprint() != before {
		t.Fatal("unrelated product version polluted ordinary fingerprint")
	}
	f.s.cfg.Product.VersionDigest = sum([]byte("version"))
	f.s.cfg.Product.VersionSequence = 120
	j := f.prepare(t, product)
	if _, err = f.s.apply(t.Context(), f.a, j); err != nil {
		t.Fatal(err)
	}
	if n := f.count(t, `SELECT count(*) FROM project_releases r JOIN nodes n ON n.id=r.release_node_id WHERE r.release_node_id=$1 AND r.sequence=121 AND n.title='Release 121'`, legacy); n != 1 {
		t.Fatal("product renumber/title not applied")
	}
}

func TestSourceChangeOrRevocationAfterBackupCannotCommit(t *testing.T) {
	for _, which := range []string{"source", "authority", "lease", "expired"} {
		t.Run(which, func(t *testing.T) {
			f := newFixture(t)
			f.legacyRelease(t, f.project, "REL-1", "planning", 1)
			j := f.prepare(t, f.project)
			switch which {
			case "source":
				f.sql(t, `UPDATE nodes SET state='closed',updated_at=updated_at+interval '1 second' WHERE id=$1`, f.project)
			case "authority":
				f.sql(t, `DELETE FROM role_bindings WHERE principal_id=$1`, f.p.ID)
			case "lease":
				f.sql(t, `UPDATE delivery_adoption_jobs SET lease_generation=lease_generation+1 WHERE project_node_id=$1`, f.project)
			case "expired":
				f.clock = f.clock.Add(15 * time.Minute)
			}
			_, err := f.s.apply(t.Context(), f.a, j)
			want := ErrLease
			if which == "source" {
				want = ErrStale
			}
			if which == "authority" {
				want = authz.ErrForbidden
			}
			if !errors.Is(err, want) {
				t.Fatalf("%s failed for wrong reason: got %v want %v", which, err, want)
			}
			if n := f.count(t, `SELECT count(*) FROM project_delivery`); n != 0 {
				t.Fatal("failed apply switched mode")
			}
			if n := f.count(t, `SELECT count(*) FROM events WHERE type='delivery.adopted'`); n != 0 {
				t.Fatal("failed apply emitted success")
			}
		})
	}
}

func TestUnknownSuccessfulBackupAndCleanupOutageDoNotConsumeHealthyReserve(t *testing.T) {
	f := newFixture(t)
	healthy := f.node(t, "project", "PR-2", "", "open")
	j := f.claim(t, f.project)
	f.provider.loseBackupResponse = true
	err := f.s.runAttempt(t.Context(), f.a, &j, nil)
	if err == nil {
		t.Fatal("unknown successful effect reported success")
	}
	if err = f.s.fail(t.Context(), f.a, j, "backup_failed", true); err != nil {
		t.Fatal(err)
	}
	backupKey := operationKey(j.Identity, "backup")
	if f.provider.executeCalls[backupKey] != 1 {
		t.Fatal("backup effect not durably keyed")
	}
	f.provider.cleanupDown = true
	if err = f.s.reconcileJob(t.Context(), f.a, f.project); err == nil {
		t.Fatal("cleanup outage reported reclamation")
	}
	v, err := f.s.Status(t.Context(), f.p, f.project)
	if err != nil || v.Cleanup != "blocked" {
		t.Fatalf("cleanup evidence %+v %v", v, err)
	}
	if n := f.count(t, `SELECT reserved_backup_bytes FROM delivery_adoption_jobs WHERE project_node_id=$1`, f.project); n != 1024 {
		t.Fatal("unknown success lost its resource reservation")
	}
	f.provider.loseBackupResponse = false
	healthyJob := f.prepare(t, healthy)
	if _, err = f.s.apply(t.Context(), f.a, healthyJob); err != nil {
		t.Fatal("healthy peer blocked", err)
	}
	if n := f.count(t, `SELECT count(*) FROM project_delivery WHERE project_node_id=$1`, healthy); n != 1 {
		t.Fatal("healthy peer did not adopt")
	}
	f.clock = f.clock.Add(time.Hour)
	f.discover(t)
	next, err := f.s.Claim(t.Context(), f.a, f.project)
	if err != nil {
		t.Fatal(err)
	}
	if err = f.s.runAttempt(t.Context(), f.a, &next, nil); err == nil {
		t.Fatal("failed project allocated over unresolved bundle")
	}
	if f.provider.executeCalls[backupKey] != 1 {
		t.Fatal("unknown success allocated twice")
	}
	if len(f.provider.bundles) > 2 {
		t.Fatal("per-project/failed-pool quota exceeded")
	}
}

func TestDryRunRefusesTargetGuardsAndPopulationBounds(t *testing.T) {
	for _, which := range []string{"deleted_release", "converted_member", "moved_member", "in_flight", "backlog_cap", "duplicate_version"} {
		t.Run(which, func(t *testing.T) {
			f := newFixture(t)
			release := f.legacyRelease(t, f.project, "REL-1", "planning", 1)
			member := f.member(t, f.project, release, "TK-1", 0)
			want := ""
			switch which {
			case "deleted_release":
				f.sql(t, `UPDATE nodes SET deleted_at=now() WHERE id=$1`, release)
				want = "release_guard"
			case "converted_member":
				f.sql(t, `UPDATE nodes SET kind_id=$2 WHERE id=$1`, member, f.kinds["memory"])
				want = "member_guard"
			case "moved_member":
				other := f.node(t, "project", "PR-2", "", "open")
				f.sql(t, `UPDATE nodes SET parent_id=$2 WHERE id=$1`, member, other)
				want = "member_guard"
			case "in_flight":
				f.sql(t, `UPDATE journey_releases SET state='building' WHERE release_node_id=$1`, release)
				want = "journey_in_flight"
			case "backlog_cap":
				f.sql(t, `INSERT INTO nodes(tenant_id,kind_id,key,title,parent_id) SELECT $1,$2,'CAP-'||i,'Bounded',$3 FROM generate_series(1,5001) i`, f.p.TenantID, f.kinds["ticket"], f.project)
				want = "v1_limits"
			case "duplicate_version":
				other := f.legacyRelease(t, f.project, "REL-2", "released", 2)
				f.sql(t, `UPDATE journey_releases SET version_scheme='legacy',version='1.0.0' WHERE release_node_id=ANY($1::uuid[])`, []string{release, other})
				want = "duplicate_version"
			}
			r, err := f.s.DryRun(t.Context(), f.p, f.project)
			if err != nil {
				t.Fatal(err)
			}
			if r.Eligible {
				t.Fatal("ineligible project was green")
			}
			found := false
			for _, reason := range r.Reasons {
				if reason.Code == want {
					found = true
				}
			}
			if !found {
				t.Fatalf("wrong refusal: %+v, want %s", r.Reasons, want)
			}
			if which == "backlog_cap" && (!r.Incomplete || !r.Counts.AtLeast || r.Counts.Members != 5001) {
				t.Fatal("over-cap count claimed completeness")
			}
		})
	}
}

func TestReportStorageBoundsGenerationsAndLatestFailure(t *testing.T) {
	store := FileReports{t.TempDir()}
	id := Identity{Tenant: newUUID(), Project: newUUID(), Attempt: newUUID(), Generation: 1}
	raw := []byte(`{"test":"first"}`)
	one, err := store.Put(t.Context(), id, raw)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.RetainFailed(t.Context(), one); err != nil {
		t.Fatal(err)
	}
	id.Generation = 2
	id.Attempt = newUUID()
	two, err := store.Put(t.Context(), id, []byte(`{"test":"second"}`))
	if err != nil {
		t.Fatal(err)
	}
	if got, err := store.Get(t.Context(), one); err != nil || string(got) != string(raw) {
		t.Fatal("latest failed report was lost")
	}
	stale := id
	stale.Generation = 1
	stale.Attempt = newUUID()
	if _, err = store.Put(t.Context(), stale, raw); !errors.Is(err, ErrLease) {
		t.Fatal("stale writer overwrote report")
	}
	if _, err = store.RetainFailed(t.Context(), two); err != nil {
		t.Fatal(err)
	}
	if _, err = store.Get(t.Context(), one); !errors.Is(err, ErrLease) {
		t.Fatal("old failed report was retained")
	}
	if _, err = store.Put(t.Context(), id, make([]byte, MaxReportBytes+1)); err == nil {
		t.Fatal("oversized report accepted")
	}
	if _, err = store.Get(t.Context(), "../../outside"); err == nil {
		t.Fatal("report path escaped identity")
	}
	files, err := os.ReadDir(filepath.Join(store.Root, id.Tenant, id.Project))
	if err != nil {
		t.Fatal(err)
	}
	payloads := 0
	for _, file := range files {
		if reportName.MatchString(file.Name()) {
			payloads++
		}
	}
	if payloads > 2 {
		t.Fatal("unbounded report retention")
	}
}

func TestEvidenceJSONRejectsDuplicateKeysAndUnknownFields(t *testing.T) {
	for _, raw := range []string{`{"operations":[],"operations":[]}`, `{"operations":[],"token":"never"}`, `{"operations":[]} {}`} {
		if err := decodeJournal([]byte(raw), &Journal{}); err == nil {
			t.Errorf("accepted ambiguous journal: %s", raw)
		}
	}
}

func TestPersistedReportRevisionAndVisibility(t *testing.T) {
	f := newFixture(t)
	j := f.claim(t, f.project)
	var r Report
	err := f.s.snapshot(t.Context(), f.p, func(tx pgx.Tx) error { var err error; r, err = f.s.plan(t.Context(), tx, j.Identity); return err })
	if err != nil {
		t.Fatal(err)
	}
	if err = f.s.saveReport(t.Context(), f.a, &j, r); err != nil {
		t.Fatal(err)
	}
	page, err := f.s.Report(t.Context(), f.p, f.project, j.ReportDigest, 0, 200)
	if err != nil || page.Fingerprint != r.Fingerprint {
		t.Fatal("persisted report read", err)
	}
	if _, err = f.s.Report(t.Context(), f.p, f.project, sum([]byte("stale")), 0, 200); !errors.Is(err, delivery.ErrRevisionChanged) {
		t.Fatal("stale report cursor accepted")
	}
	status, err := f.s.Status(t.Context(), f.p, f.project)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.s.Request(t.Context(), f.p, f.project, "retry", status.Revision-1); !errors.Is(err, delivery.ErrRevisionChanged) {
		t.Fatal("stale retry accepted")
	}
	raw, _ := json.Marshal(status)
	if strings.Contains(string(raw), "lease_token") || strings.Contains(string(raw), "operation_journal") {
		t.Fatal("private worker state exposed")
	}
}

func TestDiscoveryRepairsRestartsAndRefusedPeerDoesNotBlock(t *testing.T) {
	f := newFixture(t)
	bad := f.legacyRelease(t, f.project, "REL-1", "building", 1)
	_ = bad
	healthy := f.node(t, "project", "PR-2", "", "open")
	first, err := f.s.Pass(t.Context(), "")
	if err != nil {
		t.Fatal(err)
	}
	if first.State != "refused" && first.State != "adopted" {
		t.Fatalf("pass made no progress: %+v", first)
	}
	second, err := f.s.Pass(t.Context(), "")
	if err != nil {
		t.Fatal(err)
	}
	if first.Project == second.Project {
		t.Fatal("scheduler did not advance past failed peer")
	}
	if n := f.count(t, `SELECT count(*) FROM project_delivery WHERE project_node_id=$1`, healthy); n != 1 {
		t.Fatal("healthy project not adopted")
	}
	if n := f.count(t, `SELECT count(*) FROM project_delivery WHERE project_node_id=$1`, f.project); n != 0 {
		t.Fatal("in-flight journey was adopted")
	}
	// New service has no cursor history; missing durable checkpoints are repaired.
	third := f.node(t, "project", "PR-3", "", "open")
	copy, err := New(f.d.App, f.s.cfg, f.provider, f.s.reports)
	if err != nil {
		t.Fatal(err)
	}
	copy = copy.WithClock(f.s.now)
	if _, _, err = copy.Discover(t.Context(), ""); err != nil {
		t.Fatal(err)
	}
	if n := f.count(t, `SELECT count(*) FROM delivery_adoption_jobs WHERE project_node_id=$1`, third); n != 1 {
		t.Fatal("restart missed new project")
	}
	if n := f.count(t, `SELECT count(*) FROM delivery_adoption_jobs WHERE project_node_id=$1`, f.project); n != 1 {
		t.Fatal("discovery duplicated checkpoint")
	}
}

func TestOperationKeysBindEveryOwnershipDimension(t *testing.T) {
	id := Identity{Instance: "ppm", Tenant: newUUID(), Project: newUUID(), Migration: Migration, Attempt: newUUID(), Generation: 1}
	seen := map[string]bool{}
	variants := []Identity{id}
	for i := 0; i < 6; i++ {
		v := id
		switch i {
		case 0:
			v.Instance = "pma"
		case 1:
			v.Tenant = newUUID()
		case 2:
			v.Project = newUUID()
		case 3:
			v.Migration = "future"
		case 4:
			v.Attempt = newUUID()
		case 5:
			v.Generation++
		}
		variants = append(variants, v)
	}
	for _, v := range variants {
		key := operationKey(v, "backup")
		if seen[key] {
			t.Fatal("ownership dimension was not fenced")
		}
		seen[key] = true
	}
	if seen[operationKey(id, "restore")] {
		t.Fatal("operation kind was not bound")
	}
}
