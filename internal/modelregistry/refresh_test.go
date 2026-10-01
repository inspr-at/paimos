// SPDX-License-Identifier: AGPL-3.0-only
package modelregistry

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

func inRegistry(t *testing.T, p tenant.Principal, fn func(pgx.Tx) error) {
	t.Helper()
	if err := db.InTenant(t.Context(), appPool, p.TenantID, fn); err != nil {
		t.Fatal(err)
	}
}

func TestV2UpgradePreservesPinsCustomRoutesAndOverrides(t *testing.T) {
	reset(t)
	p := makePrincipal(t, "upgrade", "person", "Owner", []string{"admin"})
	inRegistry(t, p, func(tx pgx.Tx) error {
		seeds := catalogProfiles()
		ids := map[string]string{}
		// Reproduce exact immutable v2 profiles and its old terra profile.
		for _, s := range seeds {
			if strings.HasPrefix(s.Slug, "codex-6-1-") || s.Harness == "grok" {
				continue
			}
			s.Version = "2"
			row, err := insertProfile(t.Context(), tx, p.TenantID, profileWrite{s.Slug, s.Version, s.Harness, s.Family, s.Model, s.Effort, s.Tier})
			if err != nil {
				return err
			}
			ids[s.Slug] = row.ID
		}
		terra, err := insertProfile(t.Context(), tx, p.TenantID, profileWrite{"codex-terra-high", "2", "codex", "openai", "gpt-6-terra", "high", "standard"})
		if err != nil {
			return err
		}
		ids[terra.Slug] = terra.ID
		until := time.Now().Add(time.Hour)
		for role, slugs := range v2Ladders {
			for i, slug := range slugs {
				r := Route{Role: role, Priority: i + 1, ProfileID: ids[slug], State: "available"}
				if role == "review-gate" && i == 1 {
					r.State = "conserved"
					r.Reason = "owner preference"
					r.ValidUntil = &until
				}
				if err := insertRoute(t.Context(), tx, p.TenantID, r); err != nil {
					return err
				}
			}
		}
		// Custom build-hard order must survive. Both versions match seed values,
		// but it is structurally different from the v2 default.
		_, err = tx.Exec(t.Context(), `UPDATE model_role_routes SET priority=priority+10 WHERE role='build-hard'`)
		return err
	})
	var before []Route
	inRegistry(t, p, func(tx pgx.Tx) error { var err error; before, err = listRoutes(t.Context(), tx); return err })
	profiles := decode[[]Profile](t, &p, "GET", "/api/models", "", 200)
	if profileBySlug(profiles, "codex-6-1-sol-high").ID == "" || profileBySlug(profiles, "grok-4-7-xhigh").ID == "" {
		t.Fatal("v3 profiles missing")
	}
	inRegistry(t, p, func(tx pgx.Tx) error {
		steps, err := loadLadder(t.Context(), tx, "build")
		if err != nil {
			return err
		}
		if steps[0].Profile.Model != "gpt-6.1-sol" {
			t.Fatalf("build still selects %s", steps[0].Profile.Model)
		}
		after, err := listRoutes(t.Context(), tx)
		if err != nil {
			return err
		}
		filter := func(in []Route, role string) []Route {
			out := []Route{}
			for _, r := range in {
				if r.Role == role {
					out = append(out, r)
				}
			}
			return out
		}
		if !reflect.DeepEqual(filter(before, "build-hard"), filter(after, "build-hard")) {
			t.Fatal("custom routes changed")
		}
		oldGate, newGate := filter(before, "review-gate"), filter(after, "review-gate")
		if !reflect.DeepEqual(oldGate[1], newGate[1]) {
			t.Fatal("active override changed")
		}
		return nil
	})
	decode[[]Profile](t, &p, "GET", "/api/models", "", 200)
	if eventCount(t, p, "model.catalog_upgraded") != 1 {
		t.Fatal("upgrade not idempotent")
	}
	gate := decode[Resolution](t, &p, "GET", "/api/models/resolve?role=review-gate-security&author_family=xai", "", 200)
	if gate.Profile == nil || gate.Profile.Family != "openai" || !strings.Contains(gate.CommandTemplate, "--sandbox read-only") {
		t.Fatalf("security route: %+v", gate)
	}
}

func TestReportsAreScopedIdempotentAndPreserveOverrides(t *testing.T) {
	reset(t)
	p := makePrincipal(t, "reports", "person", "Owner", []string{"admin"})
	agent := addPrincipal(t, p.TenantID, "agent", "Worker", nil)
	other := makePrincipal(t, "other-reports", "person", "Other", []string{"admin"})
	o := Observation{EvidenceID("failure-one"), "codex", "gpt-6.1-sol", "high", "invalid"}
	send := func(p tenant.Principal, o Observation, want int) ReportResult {
		raw, _ := json.Marshal([]Observation{o})
		return decode[ReportResult](t, &p, "POST", "/api/models/reports", string(raw), want)
	}
	raw, _ := json.Marshal([]Observation{o})
	status, _ := call(t, &agent, "POST", "/api/models/reports", string(raw))
	if status != 403 {
		t.Fatalf("ungranted agent: %d", status)
	}
	profiles := decode[[]Profile](t, &p, "GET", "/api/models", "", 200)
	sol := profileBySlug(profiles, "codex-6-1-sol-high")
	until := time.Now().Add(time.Hour)
	inRegistry(t, p, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE model_role_routes SET state='conserved',reason='owner override',valid_until=$2 WHERE profile_id=$1`, sol.ID, until)
		return err
	})
	if got := send(p, o, 200); got.Recorded != 1 {
		t.Fatal(got)
	}
	if got := send(p, o, 200); got.Recorded != 0 {
		t.Fatal("replayed failure counted")
	}
	o.ReportID = EvidenceID("failure-two")
	send(p, o, 200)
	inRegistry(t, p, func(tx pgx.Tx) error {
		now, err := dbNow(t.Context(), tx)
		if err != nil {
			return err
		}
		out, err := resolveRole(t.Context(), tx, resolveQuery{Role: "build"}, now)
		if err != nil {
			return err
		}
		if len(out.Ladder[0].SkipReasons) < 2 {
			t.Fatalf("lost independent override/suppression: %+v", out.Ladder)
		}
		out, err = resolveRole(t.Context(), tx, resolveQuery{Role: "build"}, now.Add(25*time.Hour))
		if err != nil {
			return err
		}
		if out.Profile == nil || out.Profile.ID != sol.ID {
			t.Fatal("suppression/override did not expire")
		}
		return nil
	})
	o.ReportID = EvidenceID("working")
	o.Status = "working"
	send(p, o, 200)
	inRegistry(t, p, func(tx pgx.Tx) error {
		var count int
		var suppressed *time.Time
		if err := tx.QueryRow(t.Context(), `SELECT failures,suppressed_until FROM model_observations WHERE model=$1`, o.Model).Scan(&count, &suppressed); err != nil {
			return err
		}
		if count != 0 || suppressed != nil {
			t.Fatal("working did not reset failures")
		}
		return nil
	})
	changed := o
	changed.Status = "invalid"
	raw, _ = json.Marshal([]Observation{changed})
	status, _ = call(t, &p, "POST", "/api/models/reports", string(raw))
	if status != 409 {
		t.Fatalf("conflicting replay %d", status)
	}
	inRegistry(t, other, func(tx pgx.Tx) error {
		var n int
		if err := tx.QueryRow(t.Context(), `SELECT count(*) FROM model_observations`).Scan(&n); err != nil {
			return err
		}
		if n != 0 {
			t.Fatal("report leaked tenant")
		}
		return nil
	})
	o = Observation{EvidenceID("new-model"), "grok", "grok-next", "xhigh", "advertised"}
	if got := send(p, o, 200); got.Added != 1 {
		t.Fatal(got)
	}
	if got := send(p, o, 200); got.Added != 0 {
		t.Fatal("added duplicate profile")
	}
}

func TestDefaultRefreshNoNetworkAndOneAuditPerRun(t *testing.T) {
	reset(t)
	p := makePrincipal(t, "no-network", "person", "Owner", []string{"admin"})
	called := false
	m := NewWithVault(appPool, []byte(strings.Repeat("x", 32)))
	m.discovery = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		called = true
		t.Error("unexpected outbound call")
		return nil, io.EOF
	})}
	inRegistry(t, p, func(tx pgx.Tx) error { _, err := m.refreshTx(t.Context(), tx, p, false); return err })
	if called {
		t.Fatal("default called vendor")
	}
	if eventCount(t, p, "model.catalog_refreshed") != 1 {
		t.Fatal("refresh audit missing")
	}
	inRegistry(t, p, func(tx pgx.Tx) error { _, err := m.refreshTx(t.Context(), tx, p, true); return err })
	if eventCount(t, p, "model.catalog_refreshed") != 1 {
		t.Fatal("scheduled interval ignored")
	}
	m.sweep(t.Context())
	if eventCount(t, p, "model.catalog_refreshed") != 1 {
		t.Fatal("startup sweep ignored interval")
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestVendorListBoundsPaginationRedirectionAndSanitizedErrors(t *testing.T) {
	calls := 0
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		if r.URL.Host != "api.anthropic.com" || r.Header.Get("x-api-key") != "fixture-key" || r.Header.Get("anthropic-version") != "2023-06-01" {
			t.Fatal("vendor binding")
		}
		body := `{"data":[{"id":"claude-new"}],"has_more":true,"last_id":"claude-new"}`
		if calls == 2 {
			if r.URL.Query().Get("after_id") != "claude-new" {
				t.Fatal("pagination")
			}
			body = `{"data":[{"id":"claude-other"},{"id":"claude-new"}],"has_more":false}`
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body)), Header: http.Header{}}, nil
	})}
	ids, err := listVendorModels(context.Background(), client, "anthropic", "fixture-key")
	if err != nil || !reflect.DeepEqual(ids, []string{"claude-new", "claude-other"}) {
		t.Fatalf("%v %v", ids, err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, "https://example.invalid", 302) }))
	defer server.Close()
	dc := discoveryClient()
	dc.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 302, Body: io.NopCloser(strings.NewReader("fixture-key")), Header: http.Header{"Location": []string{server.URL}}}, nil
	})
	_, err = listVendorModels(t.Context(), dc, "openai", "fixture-key")
	if err == nil || strings.Contains(err.Error(), "fixture-key") {
		t.Fatal("unsafe vendor error")
	}
	bad := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"data":[{"id":"unsafe;command"}]}`))}, nil
	})}
	if _, err := listVendorModels(t.Context(), bad, "xai", "fixture-key"); err == nil {
		t.Fatal("unsafe identifier")
	}
}
