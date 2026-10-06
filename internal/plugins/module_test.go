// SPDX-License-Identifier: AGPL-3.0-only

package plugins

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/events"
	"github.com/inspr-at/paimos/internal/plugins/fence"
	"github.com/inspr-at/paimos/internal/tenant"
)

func TestInstallationIsAuditedAndTenantScoped(t *testing.T) {
	database := dbtest.Open(t)
	ctx := t.Context()
	tenantA := insertTenant(t, database, "plug-a")
	tenantB := insertTenant(t, database, "plug-b")
	admin := insertPrincipal(t, database, tenantA, tenant.Person, "Ada", []string{"admin"})
	member := insertPrincipal(t, database, tenantA, tenant.Person, "Mae", nil)
	agent := insertPrincipal(t, database, tenantA, tenant.Agent, "Agent", []string{"admin"})
	other := insertPrincipal(t, database, tenantB, tenant.Person, "Bea", []string{"admin"})

	reg, err := Builtin(testStagePlugin("janus"), testStagePlugin("pharos"))
	if err != nil {
		t.Fatal(err)
	}
	mod := NewWithRegistry(database.App, reg)
	mux := http.NewServeMux()
	mod.Mount(mux)
	call := func(p *tenant.Principal, method, path, body string) (int, []byte) {
		t.Helper()
		var reader io.Reader
		if body != "" {
			reader = strings.NewReader(body)
		}
		req := httptest.NewRequest(method, path, reader)
		if p != nil {
			req = req.WithContext(tenant.WithPrincipal(req.Context(), *p))
		}
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		return rec.Code, rec.Body.Bytes()
	}

	if status, _ := call(nil, http.MethodGet, "/api/plugins", ""); status != http.StatusUnauthorized {
		t.Fatalf("anonymous status = %d", status)
	}
	status, body := call(&member, http.MethodGet, "/api/plugins", "")
	if status != http.StatusOK {
		t.Fatalf("list status = %d %s", status, body)
	}
	var catalog []CatalogItem
	if err := json.Unmarshal(body, &catalog); err != nil {
		t.Fatal(err)
	}
	if len(catalog) != 2 || catalog[0].ID != "janus" || catalog[1].ID != "pharos" {
		t.Fatalf("catalog = %#v", catalog)
	}
	pharosItem := catalog[1]
	if pharosItem.Installation.Enabled || !pharosItem.Installation.UpdatedAt.IsZero() || len(pharosItem.Installation.Permissions) != 0 {
		t.Fatalf("unconfigured = %#v", pharosItem.Installation)
	}
	if pharosItem.DigestSHA256 != pharosItem.Installation.ManifestDigestSHA256 {
		t.Fatal("unconfigured pin is not the compiled digest")
	}

	put := func(p tenant.Principal, id, digest string, enabled bool, perms []string) (int, []byte) {
		t.Helper()
		raw, err := json.Marshal(map[string]any{
			"manifest_digest_sha256": digest,
			"enabled":                enabled,
			"permissions":            perms,
		})
		if err != nil {
			t.Fatal(err)
		}
		return call(&p, http.MethodPut, "/api/plugins/"+id+"/installation", string(raw))
	}
	if status, body := put(member, "pharos", pharosItem.DigestSHA256, true, []string{fence.PermStageDeploy}); status != http.StatusForbidden {
		t.Fatalf("member status = %d %s", status, body)
	}
	if status, body := put(agent, "pharos", pharosItem.DigestSHA256, true, []string{fence.PermStageDeploy}); status != http.StatusForbidden {
		t.Fatalf("agent status = %d %s", status, body)
	}
	if countInstalls(t, database, tenantA) != 0 || countEvents(t, database, tenantA, eventInstallation) != 0 {
		t.Fatal("rejected configure wrote state")
	}
	if status, _ := put(admin, "PHAROS", pharosItem.DigestSHA256, true, []string{}); status != http.StatusBadRequest {
		t.Fatalf("bad id status = %d", status)
	}
	if status, _ := put(admin, "missing", pharosItem.DigestSHA256, true, []string{}); status != http.StatusNotFound {
		t.Fatalf("missing status = %d", status)
	}
	if status, _ := call(&admin, http.MethodPut, "/api/plugins/pharos/installation", `{"manifest_digest_sha256":"`+pharosItem.DigestSHA256+`","enabled":true,"permissions":[],"extra":1}`); status != http.StatusBadRequest {
		t.Fatalf("extra field status = %d", status)
	}
	if status, _ := put(admin, "pharos", "abcd", true, []string{}); status != http.StatusBadRequest {
		t.Fatalf("short digest status = %d", status)
	}
	if status, body := put(admin, "pharos", strings.Repeat("ab", 32), true, []string{fence.PermStageDeploy}); status != http.StatusConflict {
		t.Fatalf("digest mismatch = %d %s", status, body)
	}
	if status, body := put(admin, "pharos", pharosItem.DigestSHA256, true, []string{fence.PermStageAccessApply}); status != http.StatusConflict {
		t.Fatalf("outside ceiling = %d %s", status, body)
	}
	if status, _ := put(admin, "pharos", pharosItem.DigestSHA256, true, []string{"shell.exec"}); status != http.StatusBadRequest {
		t.Fatalf("unknown permission status = %d", status)
	}
	if countInstalls(t, database, tenantA) != 0 {
		t.Fatal("failed configure persisted a pin")
	}

	granted := []string{fence.PermStepsEvaluate, fence.PermStepsRequest, fence.PermStepsApply, fence.PermStageDeploy}
	status, body = put(admin, "pharos", pharosItem.DigestSHA256, true, granted)
	if status != http.StatusOK {
		t.Fatalf("enable = %d %s", status, body)
	}
	var installed Installation
	if err := json.Unmarshal(body, &installed); err != nil {
		t.Fatal(err)
	}
	if !installed.Enabled || installed.PluginID != "pharos" || installed.Version != "1" || installed.UpdatedAt.IsZero() {
		t.Fatalf("installation = %#v", installed)
	}
	if countEvents(t, database, tenantA, eventInstallation) != 1 || countLinks(t, database, tenantA) != 1 {
		t.Fatal("enable did not write one event")
	}
	status, body = put(admin, "pharos", pharosItem.DigestSHA256, true, []string{fence.PermStageDeploy, fence.PermStepsApply, fence.PermStepsRequest, fence.PermStepsEvaluate})
	if status != http.StatusOK {
		t.Fatalf("replay = %d %s", status, body)
	}
	if countEvents(t, database, tenantA, eventInstallation) != 1 {
		t.Fatal("identical pin wrote another event")
	}

	decision, err := mod.Request(ctx, admin, "pharos", StepRequest{Stage: "deploy", Operation: "deploy", Payload: struct{}{}})
	if err != nil || !decision.Proceed || decision.AdvancesRelease || decision.AdvancesAccess {
		t.Fatalf("request = %#v %v", decision, err)
	}
	if _, err := mod.Request(ctx, other, "pharos", StepRequest{Stage: "deploy", Operation: "deploy", Payload: struct{}{}}); !errors.Is(err, ErrClosed) {
		t.Fatalf("other tenant request = %v", err)
	}
	status, body = call(&other, http.MethodGet, "/api/plugins", "")
	if status != http.StatusOK {
		t.Fatal(status)
	}
	if err := json.Unmarshal(body, &catalog); err != nil {
		t.Fatal(err)
	}
	if catalog[1].Installation.Enabled {
		t.Fatal("tenant B saw tenant A's pin")
	}
	if installsVisible(t, database.App, tenantB) != 0 || installsVisible(t, database.App, "") != 0 || installsVisible(t, database.Admin, "") != 1 {
		t.Fatal("row level security did not isolate installations")
	}

	if status, body = put(admin, "pharos", pharosItem.DigestSHA256, true, []string{fence.PermStepsEvaluate}); status != http.StatusOK {
		t.Fatalf("narrow = %d %s", status, body)
	}
	if _, err := mod.Request(ctx, admin, "pharos", StepRequest{Stage: "deploy", Operation: "deploy", Payload: struct{}{}}); !errors.Is(err, ErrDenied) {
		t.Fatalf("narrowed request = %v", err)
	}
	var firstPerms []string
	if err := database.Admin.QueryRow(ctx, `SELECT permissions FROM plugin_installation_events
		WHERE tenant_id = $1::uuid AND plugin_id = 'pharos' ORDER BY event_id LIMIT 1`, tenantA).Scan(&firstPerms); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(firstPerms, []string{fence.PermStageDeploy, fence.PermStepsApply, fence.PermStepsEvaluate, fence.PermStepsRequest}) {
		t.Fatalf("historical permissions = %#v", firstPerms)
	}
	if status, body = put(admin, "pharos", pharosItem.DigestSHA256, false, []string{fence.PermStepsEvaluate}); status != http.StatusOK {
		t.Fatalf("disable = %d %s", status, body)
	}
	if _, err := mod.Evaluate(ctx, admin, "pharos", StepRequest{Stage: "deploy", Operation: "deploy", Payload: struct{}{}}); !errors.Is(err, ErrClosed) {
		t.Fatalf("disabled evaluate = %v", err)
	}
	if countEvents(t, database, tenantA, eventInstallation) != 3 {
		t.Fatal("enable, narrow, and disable did not each write an event")
	}

	if _, err := database.Admin.Exec(ctx, `UPDATE plugin_installations SET manifest_digest_sha256 = $2
		WHERE tenant_id = $1::uuid AND plugin_id = 'pharos'`, tenantA, strings.Repeat("cd", 32)); err != nil {
		t.Fatal(err)
	}
	status, body = call(&admin, http.MethodGet, "/api/plugins", "")
	if err := json.Unmarshal(body, &catalog); err != nil {
		t.Fatal(err)
	}
	if catalog[1].DigestSHA256 == catalog[1].Installation.ManifestDigestSHA256 {
		t.Fatal("tampered pin matched the compiled digest")
	}
	if _, err := mod.Evaluate(ctx, admin, "pharos", StepRequest{Operation: "deploy", Stage: "deploy", Payload: struct{}{}}); !errors.Is(err, ErrClosed) {
		t.Fatalf("mismatched digest = %v", err)
	}

	mod.appendEvent = func(context.Context, pgx.Tx, tenant.Principal, events.Change) (events.Event, error) {
		return events.Event{}, errors.New("event writer failed")
	}
	if status, _ = put(admin, "janus", catalog[0].DigestSHA256, true, []string{fence.PermStepsEvaluate}); status != http.StatusInternalServerError {
		t.Fatalf("failed event status = %d", status)
	}
	var janusRows int
	if err := database.Admin.QueryRow(ctx, `SELECT count(*) FROM plugin_installations WHERE plugin_id = 'janus'`).Scan(&janusRows); err != nil {
		t.Fatal(err)
	}
	if janusRows != 0 {
		t.Fatal("installation committed without its event")
	}
	if _, err := database.Admin.Exec(ctx, `UPDATE plugin_installation_events SET enabled = NOT enabled`); err == nil {
		t.Fatal("installation history was updated")
	}
}

func TestGrantIsNarrowAndJobWritesAnEvent(t *testing.T) {
	database := dbtest.Open(t)
	ctx := t.Context()
	tenantID := insertTenant(t, database, "plug-lab")
	admin := insertPrincipal(t, database, tenantID, tenant.Person, "Ada", []string{"admin"})
	steps := &captureSteps{}
	jobs := &captureJob{}
	kinds := &kindSwitch{}
	plug := Plugin{
		Manifest: Manifest{
			ID: "lab", Version: "1", Owner: "lab",
			Permissions:    []string{fence.PermNodesContribute, fence.PermStepsEvaluate, fence.PermStageDeploy, fence.PermStageVerify, fence.PermJobsRun},
			NodeKinds:      []NodeKind{{Slug: "widget", FieldSchema: json.RawMessage(`{"type":"object"}`)}},
			WorkflowSteps:  []WorkflowStep{{Key: "run", Gates: []string{fence.GatePersonDecision}}},
			BackgroundJobs: []Capability{{ID: "sweep", Permission: fence.PermJobsRun}},
		},
		StepPermissions: map[string]string{"run": fence.PermStageDeploy},
		Kinds:           kinds,
		Steps:           steps,
		Jobs:            jobs,
	}
	sum, err := Digest(plug)
	if err != nil {
		t.Fatal(err)
	}
	plug.Manifest.DigestSHA256 = sum
	reg := NewRegistry()
	if err := reg.Register(plug); err != nil {
		t.Fatal(err)
	}
	reg.Seal()
	stored, _ := reg.plugin("lab")
	kinds.kinds = stored.Manifest.NodeKinds
	mod := NewWithRegistry(database.App, reg)
	now := time.Date(2026, 9, 23, 12, 0, 0, 0, time.UTC)
	mod.now = func() time.Time { return now }

	if _, err := mod.Configure(ctx, admin, "lab", InstallationWrite{
		ManifestDigestSHA256: sum,
		Enabled:              true,
		Permissions:          plug.Manifest.Permissions,
	}); err != nil {
		t.Fatal(err)
	}
	got, err := mod.NodeKinds(ctx, admin, "lab")
	if err != nil || len(got) != 1 || got[0].Slug != "widget" {
		t.Fatalf("kinds = %#v %v", got, err)
	}
	kinds.drift = true
	if _, err := mod.NodeKinds(ctx, admin, "lab"); !errors.Is(err, ErrClosed) {
		t.Fatalf("drifting kinds = %v", err)
	}
	if _, err := mod.Evaluate(ctx, admin, "lab", StepRequest{Operation: "run", Payload: struct{}{}}); err != nil {
		t.Fatal(err)
	}
	if steps.grant.Allows(fence.PermStageVerify) || !steps.grant.Allows(fence.PermStepsEvaluate) || !steps.grant.Allows(fence.PermStageDeploy) {
		t.Fatalf("step grant = %#v", steps.grant.Names())
	}

	release, err := mod.leases.acquire(leaseKey(tenantID, "lab", "sweep"), admin.ID, now, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if err := mod.RunJob(ctx, admin, "lab", "sweep"); !errors.Is(err, ErrLeaseHeld) {
		t.Fatalf("held lease = %v", err)
	}
	if countEvents(t, database, tenantID, eventJobRan) != 0 {
		t.Fatal("held lease wrote an event")
	}
	release()
	if err := mod.RunJob(ctx, admin, "lab", "sweep"); err != nil {
		t.Fatal(err)
	}
	if jobs.grant.Allows(fence.PermStageDeploy) || !jobs.grant.Allows(fence.PermJobsRun) || len(jobs.grant.Names()) != 1 {
		t.Fatalf("job grant = %#v", jobs.grant.Names())
	}
	if countEvents(t, database, tenantID, eventJobRan) != 1 {
		t.Fatal("job run did not write an event")
	}
	if _, err := mod.Configure(ctx, admin, "lab", InstallationWrite{ManifestDigestSHA256: sum, Permissions: plug.Manifest.Permissions}); err != nil {
		t.Fatal(err)
	}
	if err := mod.RunJob(ctx, admin, "lab", "sweep"); !errors.Is(err, ErrClosed) {
		t.Fatalf("disabled job = %v", err)
	}
	if countEvents(t, database, tenantID, eventJobRan) != 1 {
		t.Fatal("disabled job wrote an event")
	}
}

type captureSteps struct{ grant Grant }

func (c *captureSteps) Evaluate(_ context.Context, call Call, _ StepRequest) (StepDecision, error) {
	c.grant = call.Grant
	return StepDecision{Proceed: true}, nil
}

func (c *captureSteps) Request(context.Context, Call, StepRequest) (StepDecision, error) {
	return StepDecision{}, nil
}

func (c *captureSteps) ApplyResult(context.Context, Call, StepResult) (StepDecision, error) {
	return StepDecision{}, nil
}

type captureJob struct{ grant Grant }

func (c *captureJob) Run(_ context.Context, call Call, _ string) (JobResult, error) {
	c.grant = call.Grant
	return JobResult{Outcome: "succeeded"}, nil
}

type kindSwitch struct {
	drift bool
	kinds []NodeKind
}

func (k *kindSwitch) NodeKinds(context.Context, Call) ([]NodeKind, error) {
	if k.drift {
		return []NodeKind{{Slug: "widget", FieldSchema: json.RawMessage(`{"type":"string"}`)}}, nil
	}
	return k.kinds, nil
}

func insertTenant(t *testing.T, database *dbtest.DB, slug string) string {
	t.Helper()
	var id string
	if err := database.Admin.QueryRow(t.Context(), `INSERT INTO tenants (slug, name) VALUES ($1, $1) RETURNING id::text`, slug).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

func insertPrincipal(t *testing.T, database *dbtest.DB, tenantID string, kind tenant.PrincipalKind, name string, roles []string) tenant.Principal {
	t.Helper()
	if roles == nil {
		roles = []string{}
	}
	var id string
	err := database.Admin.QueryRow(t.Context(), `INSERT INTO principals (tenant_id, kind, name, roles)
		VALUES ($1::uuid, $2, $3, $4) RETURNING id::text`, tenantID, string(kind), name, roles).Scan(&id)
	if err != nil {
		t.Fatal(err)
	}
	dbtest.BindLegacy(t, database, tenantID, id)
	return tenant.Principal{ID: id, TenantID: tenantID, Kind: kind, Name: name, Roles: roles}
}

func countInstalls(t *testing.T, database *dbtest.DB, tenantID string) int {
	t.Helper()
	var n int
	if err := database.Admin.QueryRow(t.Context(), `SELECT count(*) FROM plugin_installations WHERE tenant_id = $1::uuid`, tenantID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func countEvents(t *testing.T, database *dbtest.DB, tenantID, eventType string) int {
	t.Helper()
	var n int
	if err := database.Admin.QueryRow(t.Context(), `SELECT count(*) FROM events WHERE tenant_id = $1::uuid AND type = $2`, tenantID, eventType).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func countLinks(t *testing.T, database *dbtest.DB, tenantID string) int {
	t.Helper()
	var n int
	if err := database.Admin.QueryRow(t.Context(), `SELECT count(*) FROM plugin_installation_events WHERE tenant_id = $1::uuid`, tenantID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func installsVisible(t *testing.T, pool *pgxpool.Pool, tenantID string) int {
	t.Helper()
	var n int
	var err error
	if tenantID == "" {
		err = pool.QueryRow(t.Context(), `SELECT count(*) FROM plugin_installations`).Scan(&n)
	} else {
		err = db.InTenant(dbtest.Seed(t.Context()), pool, tenantID, func(tx pgx.Tx) error {
			return tx.QueryRow(t.Context(), `SELECT count(*) FROM plugin_installations`).Scan(&n)
		})
	}
	if err != nil {
		t.Fatal(err)
	}
	return n
}
