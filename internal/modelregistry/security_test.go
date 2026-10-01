// SPDX-License-Identifier: AGPL-3.0-only
package modelregistry

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/linkvault"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

func enrollEvidenceHarness(t *testing.T, owner, agent tenant.Principal, harness string) string {
	t.Helper()
	var id string
	inRegistry(t, owner, func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), `INSERT INTO agent_accounts(tenant_id,account_key,harness,daemon_id,registered_by_principal_id,label) VALUES($1,$2,$2,'fixture',$3,'Fixture') RETURNING id::text`, owner.TenantID, harness, agent.ID).Scan(&id)
	})
	return id
}

func seedEvidenceSession(t *testing.T, owner, agent tenant.Principal, harness, model, effort string) string {
	t.Helper()
	var session string
	err := db.InTenant(dbtest.Seed(t.Context()), appPool, owner.TenantID, func(tx pgx.Tx) error {
		var project string
		if err := tx.QueryRow(t.Context(), `INSERT INTO nodes(tenant_id,key,kind_id,title) SELECT $1,'EVD-1',id,'Evidence project' FROM node_kinds WHERE slug='project' RETURNING id::text`, owner.TenantID).Scan(&project); err != nil {
			return err
		}
		return tx.QueryRow(t.Context(), `INSERT INTO harness_sessions(tenant_id,project_id,agent_principal_id,harness,host,management,role,ref_digest,lease_digest,model,reasoning_effort) VALUES($1,$2,$3,$4,'fixture','unmanaged','worker',$5,$5,$6,$7) RETURNING id::text`, owner.TenantID, project, agent.ID, harness, []byte(harness), model, effort).Scan(&session)
	})
	if err != nil {
		t.Fatal(err)
	}
	return session
}

func reportEvidence(t *testing.T, actor tenant.Principal, observations []Observation, want int) ReportResult {
	t.Helper()
	raw, err := json.Marshal(observations)
	if err != nil {
		t.Fatal(err)
	}
	return decode[ReportResult](t, &actor, "POST", "/api/models/reports", string(raw), want)
}

func TestAgentEvidenceCannotSuppressUnattemptedOrForeignModels(t *testing.T) {
	reset(t)
	owner := makePrincipal(t, "evidence-binding", "person", "Owner", []string{"admin"})
	agent := addPrincipal(t, owner.TenantID, "agent", "Reporter", nil)
	grantModelReporter(t, owner, agent)
	agent.Scopes = []string{"models.read", "models.report", "models.refresh", "models.manage"}
	decode[[]Profile](t, &owner, "GET", "/api/models", "", 200)
	before := registryRoutes(t, owner)
	o := Observation{ReportID: EvidenceID("unattempted"), Harness: "codex", Model: "gpt-6.1-sol", Effort: "high", Status: "invalid"}
	reportEvidence(t, agent, []Observation{o}, 403)
	enrollEvidenceHarness(t, owner, agent, "codex")
	reportEvidence(t, agent, []Observation{o}, 403) // enrollment is not an attempt
	session := seedEvidenceSession(t, owner, agent, "codex", o.Model, o.Effort)
	if got := reportEvidence(t, agent, []Observation{o}, 200); got.Recorded != 1 {
		t.Fatal(got)
	}
	for _, mutation := range []Observation{
		{Harness: "grok", Model: "grok-4.7", Effort: "xhigh", Status: "invalid"},
		{Harness: "codex", Model: "gpt-6-astra", Effort: "high", Status: "invalid"},
		{Harness: "codex", Model: o.Model, Effort: "xhigh", Status: "invalid"},
		{Harness: "grok", Model: "grok-4.7", Effort: "xhigh", Status: "working"},
		{Harness: "grok", Model: "grok-next", Effort: "xhigh", Status: "advertised"},
	} {
		mutation.ReportID = EvidenceID(mutation.Harness + mutation.Model + mutation.Effort + mutation.Status)
		reportEvidence(t, agent, []Observation{mutation}, 403)
	}
	// Even an enrolled harness cannot report an attempted model from another tenant.
	foreignOwner := makePrincipal(t, "foreign-evidence", "person", "Owner", []string{"admin"})
	foreignAgent := addPrincipal(t, foreignOwner.TenantID, "agent", "Reporter", nil)
	grantModelReporter(t, foreignOwner, foreignAgent)
	enrollEvidenceHarness(t, foreignOwner, foreignAgent, "codex")
	reportEvidence(t, foreignAgent, []Observation{o}, 403)
	inRegistry(t, owner, func(tx pgx.Tx) error {
		var receipts, observations int
		if err := tx.QueryRow(t.Context(), `SELECT count(*) FROM model_report_receipts`).Scan(&receipts); err != nil {
			return err
		}
		if err := tx.QueryRow(t.Context(), `SELECT count(*) FROM model_observations`).Scan(&observations); err != nil {
			return err
		}
		if receipts != 1 || observations != 1 {
			t.Fatalf("forged evidence persisted: %d receipts, %d observations", receipts, observations)
		}
		_, err := tx.Exec(t.Context(), `UPDATE harness_sessions SET stopped_at=clock_timestamp(),phase='stopped' WHERE id=$1`, session)
		return err
	})
	o.ReportID = EvidenceID("stopped-session")
	reportEvidence(t, agent, []Observation{o}, 403)
	if !reflect.DeepEqual(before, registryRoutes(t, owner)) {
		t.Fatal("evidence changed ladders")
	}
}

func TestFailuresMustBeSpacedAndReceiptsAreBounded(t *testing.T) {
	reset(t)
	owner := makePrincipal(t, "spaced-evidence", "person", "Owner", []string{"admin"})
	agent := addPrincipal(t, owner.TenantID, "agent", "Reporter", nil)
	grantModelReporter(t, owner, agent)
	agent.Scopes = []string{"models.read", "models.report", "models.refresh", "models.manage"}
	seedEvidenceSession(t, owner, agent, "codex", "gpt-6.1-sol", "high")
	o := Observation{ReportID: EvidenceID("first"), Harness: "codex", Model: "gpt-6.1-sol", Effort: "high", Status: "invalid"}
	second := o
	second.ReportID = EvidenceID("same-batch")
	reportEvidence(t, agent, []Observation{o, second}, 200)
	inRegistry(t, owner, func(tx pgx.Tx) error {
		var failures int
		var until *time.Time
		if err := tx.QueryRow(t.Context(), `SELECT failures,suppressed_until FROM model_observations`).Scan(&failures, &until); err != nil {
			return err
		}
		if failures != 1 || until != nil {
			t.Fatal("one batch suppressed a model")
		}
		_, err := tx.Exec(t.Context(), `UPDATE model_observations SET last_failing_at=now()-interval '6 minutes'`)
		return err
	})
	second.ReportID = EvidenceID("later-failure")
	reportEvidence(t, agent, []Observation{second}, 200)
	inRegistry(t, owner, func(tx pgx.Tx) error {
		var failures int
		var until *time.Time
		if err := tx.QueryRow(t.Context(), `SELECT failures,suppressed_until FROM model_observations`).Scan(&failures, &until); err != nil {
			return err
		}
		if failures != 2 || until == nil {
			t.Fatal("spaced failures did not suppress")
		}
		_, err := tx.Exec(t.Context(), `INSERT INTO model_report_receipts(tenant_id,principal_id,report_id,content) SELECT $1,$2,gen_random_uuid(),'{}' FROM generate_series(1,$3)`, owner.TenantID, agent.ID, maxHourlyReceipts-3)
		return err
	})
	if got := reportEvidence(t, agent, []Observation{o}, 200); got.Recorded != 0 {
		t.Fatal("replay rejected at limit")
	}
	o.ReportID = EvidenceID("hourly-limit")
	reportEvidence(t, agent, []Observation{o}, 429)
	inRegistry(t, owner, func(tx pgx.Tx) error {
		if _, err := tx.Exec(t.Context(), `UPDATE model_report_receipts SET created_at=now()-interval '2 hours'`); err != nil {
			return err
		}
		_, err := tx.Exec(t.Context(), `INSERT INTO model_report_receipts(tenant_id,principal_id,report_id,content,created_at) SELECT $1,$2,gen_random_uuid(),'{}',now()-interval '2 hours' FROM generate_series(1,$3)`, owner.TenantID, agent.ID, maxPrincipalReceipts-maxHourlyReceipts)
		return err
	})
	o.ReportID = EvidenceID("storage-limit")
	reportEvidence(t, agent, []Observation{o}, 429)
	inRegistry(t, owner, func(tx pgx.Tx) error {
		var count int
		if err := tx.QueryRow(t.Context(), `SELECT count(*) FROM model_report_receipts`).Scan(&count); err != nil {
			return err
		}
		if count != maxPrincipalReceipts {
			t.Fatal("receipt storage exceeds cap")
		}
		_, err := tx.Exec(t.Context(), `UPDATE model_report_receipts SET created_at=now()-interval '8 days'`)
		return err
	})
	reportEvidence(t, agent, []Observation{o}, 200)
	inRegistry(t, owner, func(tx pgx.Tx) error {
		var count int
		if err := tx.QueryRow(t.Context(), `SELECT count(*) FROM model_report_receipts`).Scan(&count); err != nil {
			return err
		}
		if count != 1 {
			t.Fatal("expired receipts retained")
		}
		return nil
	})
}

func TestAutomaticallyObservedProfilesRequirePersonGrant(t *testing.T) {
	reset(t)
	owner := makePrincipal(t, "disabled-observation", "person", "Owner", []string{"admin"})
	agent := addPrincipal(t, owner.TenantID, "agent", "Reporter", nil)
	grantModelReporter(t, owner, agent)
	agent.Scopes = []string{"models.read", "models.report", "models.refresh", "models.manage"}
	enrollEvidenceHarness(t, owner, agent, "grok") // null allowlist
	before := decode[[]Route](t, &owner, "PUT", "/api/models/routes", "[]", 200)
	o := Observation{ReportID: EvidenceID("observed-disabled"), Harness: "grok", Model: "grok-next", Effort: "xhigh", Status: "advertised"}
	if result := reportEvidence(t, agent, []Observation{o}, 200); result.Added != 1 {
		t.Fatal(result)
	}
	profiles := decode[[]Profile](t, &owner, "GET", "/api/models", "", 200)
	var observed Profile
	for _, profile := range profiles {
		if profile.Model == o.Model {
			observed = profile
		}
	}
	if observed.ID == "" || observed.Enabled {
		t.Fatal("observation was granted automatically")
	}
	status := decode[struct {
		Observations []struct {
			Pending bool `json:"pending"`
		} `json:"observations"`
	}](t, &owner, "GET", "/api/models/refresh", "", 200)
	if len(status.Observations) != 1 || !status.Observations[0].Pending {
		t.Fatal("disabled profile is not pending acceptance")
	}
	accept := `{"harness":"grok","model":"grok-next","effort":"xhigh"}`
	callStatus, _ := call(t, &agent, "POST", "/api/models/proposals/accept", accept)
	if callStatus != 403 {
		t.Fatal("agent granted a discovered profile")
	}
	accepted := decode[Profile](t, &owner, "POST", "/api/models/proposals/accept", accept, 200)
	if !accepted.Enabled || accepted.ID == observed.ID {
		t.Fatal("person grant did not create a separate enabled pin")
	}
	inRegistry(t, owner, func(tx pgx.Tx) error {
		var enabled bool
		if err := tx.QueryRow(t.Context(), `SELECT enabled FROM model_profiles WHERE id=$1`, observed.ID).Scan(&enabled); err != nil {
			return err
		}
		if enabled {
			t.Fatal("person grant mutated the immutable observed pin")
		}
		return nil
	})
	decode[Profile](t, &owner, "POST", "/api/models/proposals/accept", accept, 200)
	if eventCount(t, owner, "model.proposal_accepted") != 1 || !reflect.DeepEqual(before, registryRoutes(t, owner)) {
		t.Fatal("grant replay or grant changed policy")
	}
}

func TestRefreshReleasesCatalogLockAndSharesIntervalWithAgents(t *testing.T) {
	reset(t)
	owner := makePrincipal(t, "refresh-concurrency", "person", "Owner", []string{"admin"})
	agent := addPrincipal(t, owner.TenantID, "agent", "Reporter", nil)
	grantModelReporter(t, owner, agent)
	agent.Scopes = []string{"models.read", "models.report", "models.refresh", "models.manage"}
	account := enrollEvidenceHarness(t, owner, agent, "codex")
	m := NewWithVault(appPool, []byte(strings.Repeat("x", 32)))
	mux := http.NewServeMux()
	m.Mount(mux)
	send := func(actor tenant.Principal, method, path, body string) int {
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r = r.WithContext(tenant.WithPrincipal(r.Context(), actor))
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		return w.Code
	}
	if got := send(owner, "PUT", "/api/models/refresh/credentials/"+account, `{"vendor":"openai","api_key":"fixture-key"}`); got != 200 {
		t.Fatalf("credential status %d", got)
	}
	if got := send(owner, "PUT", "/api/models/refresh/settings", `{"agent_reports_enabled":true,"auto_add_profiles":true,"api_enabled":true,"interval_minutes":1440}`); got != 200 {
		t.Fatalf("settings status %d", got)
	}
	started, release, finished := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	m.discovery = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		deadline, ok := r.Context().Deadline()
		if !ok || time.Until(deadline) > discoveryDeadline {
			return nil, errors.New("missing round trip deadline")
		}
		close(started)
		select {
		case <-release:
			return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"data":[{"id":"gpt-next"}]}`))}, nil
		case <-r.Context().Done():
			return nil, r.Context().Err()
		}
	})}
	go func() {
		_, err := m.runRefresh(tenant.WithPrincipal(t.Context(), agent), agent, false)
		finished <- err
	}()
	defer func() {
		close(release)
		if err := <-finished; err != nil {
			t.Error(err)
		}
	}()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("refresh did not start")
	}
	ctx, cancel := context.WithTimeout(tenant.WithPrincipal(t.Context(), owner), time.Second)
	defer cancel()
	var reservation time.Time
	if err := db.InTenant(ctx, appPool, owner.TenantID, func(tx pgx.Tx) error {
		if err := ensureCatalog(ctx, tx, owner); err != nil {
			return err
		}
		var at *time.Time
		if err := tx.QueryRow(ctx, `SELECT last_run_at FROM model_refresh_settings`).Scan(&at); err != nil {
			return err
		}
		if at == nil {
			return errors.New("network preceded reservation commit")
		}
		reservation = *at
		_, err := resolveRole(ctx, tx, resolveQuery{Role: "build"}, time.Now())
		return err
	}); err != nil {
		t.Fatalf("vendor stalled catalog routing: %v", err)
	}
	if got := send(agent, "POST", "/api/models/refresh", "{}"); got != 429 {
		t.Fatalf("parallel agent refresh %d", got)
	}
	inRegistry(t, owner, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE model_refresh_settings SET last_run_at=now()-interval '61 minutes'`)
		return err
	})
	if got := send(agent, "POST", "/api/models/refresh", "{}"); got != 429 {
		t.Fatalf("agent bypassed configured 1440-minute interval: %d", got)
	}
	// Restore the reservation so the first request can complete normally.
	inRegistry(t, owner, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE model_refresh_settings SET last_run_at=$1`, reservation)
		return err
	})
}

func TestVendorDiscoveryCapsPagesModelsAndUsesOneDeadline(t *testing.T) {
	calls := 0
	var deadline time.Time
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		current, ok := r.Context().Deadline()
		if !ok || time.Until(current) > discoveryDeadline || (!deadline.IsZero() && !current.Equal(deadline)) {
			t.Error("pagination resets or omits deadline")
		}
		deadline = current
		calls++
		body := fmt.Sprintf(`{"data":[{"id":"claude-%d"}],"has_more":true,"last_id":"claude-%d"}`, calls, calls)
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body))}, nil
	})}
	if _, err := listVendorModels(t.Context(), client, "anthropic", "fixture-key"); err == nil || calls != maxDiscoveryPages {
		t.Fatalf("pagination bound: %d calls, %v", calls, err)
	}
	rows := []map[string]string{}
	for i := 0; i <= maxDiscoveryModels; i++ {
		rows = append(rows, map[string]string{"id": fmt.Sprintf("gpt-%d", i)})
	}
	raw, _ := json.Marshal(map[string]any{"data": rows})
	client.Transport = roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(string(raw)))}, nil
	})
	if _, err := listVendorModels(t.Context(), client, "openai", "fixture-key"); err == nil {
		t.Fatal("unbounded model identifiers")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	client.Transport = roundTripFunc(func(*http.Request) (*http.Response, error) {
		t.Fatal("cancelled discovery made an outbound call")
		return nil, io.EOF
	})
	if _, err := listVendorModels(ctx, client, "openai", "fixture-key"); err == nil {
		t.Fatal("cancelled discovery succeeded")
	}
}

func TestRefreshBoundsProfileAdditionsAndRejectsDisabledDiscoveryResults(t *testing.T) {
	reset(t)
	owner := makePrincipal(t, "bounded-refresh", "person", "Owner", []string{"admin"})
	agent := addPrincipal(t, owner.TenantID, "agent", "Reporter", nil)
	account := enrollEvidenceHarness(t, owner, agent, "codex")
	m := NewWithVault(appPool, []byte(strings.Repeat("x", 32)))
	decode[[]Profile](t, &owner, "GET", "/api/models", "", 200)
	before := registryRoutes(t, owner)
	cipher, err := linkvault.Encrypt(m.vaultKey, owner.TenantID, "models/"+account+"/openai", "fixture-key")
	if err != nil {
		t.Fatal(err)
	}
	inRegistry(t, owner, func(tx pgx.Tx) error {
		if _, err := tx.Exec(t.Context(), `INSERT INTO model_discovery_credentials(tenant_id,account_id,vendor,ciphertext) VALUES($1,$2,'openai',$3)`, owner.TenantID, account, cipher); err != nil {
			return err
		}
		_, err := tx.Exec(t.Context(), `UPDATE model_refresh_settings SET api_enabled=true`)
		return err
	})
	models := []map[string]string{}
	for i := 0; i < maxRefreshObservations+50; i++ {
		models = append(models, map[string]string{"id": fmt.Sprintf("gpt-future-%03d", i)})
	}
	raw, _ := json.Marshal(map[string]any{"data": models})
	calls := 0
	disableDuringFetch := false
	m.discovery = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		calls++
		if disableDuringFetch {
			inRegistry(t, owner, func(tx pgx.Tx) error {
				_, err := tx.Exec(t.Context(), `UPDATE model_refresh_settings SET api_enabled=false`)
				return err
			})
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(string(raw)))}, nil
	})}
	ctx := tenant.WithPrincipal(t.Context(), owner)
	result, err := m.runRefresh(ctx, owner, false)
	if err != nil || result.Added != maxRefreshObservations || result.Sources[0].State != "limited" {
		t.Fatalf("unbounded discovery: %+v, %v", result, err)
	}
	inRegistry(t, owner, func(tx pgx.Tx) error {
		var granted, observed int
		if err := tx.QueryRow(t.Context(), `SELECT count(*),count(*) FILTER(WHERE enabled) FROM model_profiles WHERE version='observed-1'`).Scan(&observed, &granted); err != nil {
			return err
		}
		if observed != maxRefreshObservations || granted != 0 {
			t.Fatalf("discovery created or granted too many profiles: %d/%d", observed, granted)
		}
		_, err := tx.Exec(t.Context(), `UPDATE model_refresh_settings SET last_run_at=NULL`)
		return err
	})
	disableDuringFetch = true
	result, err = m.runRefresh(ctx, owner, false)
	if err != nil || result.Added != 0 || result.Sources[0].State != "stale" {
		t.Fatalf("disabled discovery published results: %+v, %v", result, err)
	}
	inRegistry(t, owner, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE model_refresh_settings SET last_run_at=NULL`)
		return err
	})
	if _, err = m.runRefresh(ctx, owner, false); err != nil {
		t.Fatal(err)
	}
	if calls != 2 || !reflect.DeepEqual(before, registryRoutes(t, owner)) {
		t.Fatal("disabled discovery called vendor or discovery rewrote policy")
	}
}
