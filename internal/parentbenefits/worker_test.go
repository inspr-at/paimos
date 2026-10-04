// SPDX-License-Identifier: AGPL-3.0-only
package parentbenefits

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/modelprovider"
	"github.com/inspr-at/paimos/internal/nodes"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

var sample = Texts{"Clear release notes", "Verständliche Release Notes", "Completed work explains its benefit.", "Erledigte Arbeit erklärt den Nutzen."}
var summary = Texts{"Clear parent benefits", "Verständlicher Nutzen Überblick", "The leaves explain the completed feature.", "Die Blätter erklären die erledigte Funktion."}

type fixture struct {
	d            *dbtest.DB
	p            tenant.Principal
	parent, leaf string
	m            *Module
	mux          *http.ServeMux
	provider     *modelprovider.Service
}

func setup(t *testing.T) fixture {
	t.Helper()
	f := fixture{d: dbtest.Open(t), mux: http.NewServeMux()}
	f.p = tenant.Principal{ID: "22222222-2222-4222-8222-222222222222", TenantID: "11111111-1111-4111-8111-111111111111", Kind: tenant.Person}
	err := db.InTenant(dbtest.Seed(t.Context()), f.d.App, f.p.TenantID, func(tx pgx.Tx) error {
		if _, err := tx.Exec(t.Context(), `INSERT INTO tenants(id,slug,name) VALUES($1,'benefits','Benefits');`, f.p.TenantID); err != nil {
			return err
		}
		if _, err := tx.Exec(t.Context(), `INSERT INTO principals(tenant_id,id,kind,name) VALUES($1,$2,'person','Benefits editor')`, f.p.TenantID, f.p.ID); err != nil {
			return err
		}
		if _, err := tx.Exec(t.Context(), `SELECT aeon_seed_node_kinds($1)`, f.p.TenantID); err != nil {
			return err
		}
		if err := tx.QueryRow(t.Context(), `INSERT INTO nodes(tenant_id,kind_id,title,key,state,fields) SELECT $1,id,'Parent','BEN-1','open','{}' FROM node_kinds WHERE slug='work' RETURNING id::text`, f.p.TenantID).Scan(&f.parent); err != nil {
			return err
		}
		raw, _ := json.Marshal(sample)
		return tx.QueryRow(t.Context(), `INSERT INTO nodes(tenant_id,kind_id,title,key,state,fields,parent_id) SELECT $1,id,'Leaf','BEN-2','open',$3,$2 FROM node_kinds WHERE slug='work' RETURNING id::text`, f.p.TenantID, f.parent, raw).Scan(&f.leaf)
	})
	if err != nil {
		t.Fatal(err)
	}
	dbtest.BindRole(t, f.d, f.p.TenantID, f.p.ID, "admin")
	dbtest.EnableWorkParentStatus(t, f.d, f.p.TenantID)
	f.provider = modelprovider.New(f.d.App, nil)
	_, err = f.provider.Save(tenant.WithPrincipal(t.Context(), f.p), f.p, modelprovider.Write{Settings: modelprovider.Settings{Enabled: true, BaseURL: "http://127.0.0.1:1/v1", ChatModel: "fixture", Features: modelprovider.Features{ParentBenefits: true}}})
	if err != nil {
		t.Fatal(err)
	}
	f.m = New(f.d.App, func(ctx context.Context, tid, pid string, rev int64, ms []modelprovider.Message) (modelprovider.Completion, error) {
		raw, _ := json.Marshal(summary)
		return modelprovider.Completion{Text: string(raw)}, nil
	})
	f.m.Mount(f.mux)
	nodes.New(f.d.App, nil).Mount(f.mux)
	return f
}
func (f fixture) call(t *testing.T, p tenant.Principal, method, path string, body any, want int) []byte {
	t.Helper()
	raw, _ := json.Marshal(body)
	if body == nil {
		raw = nil
	}
	r := httptest.NewRequest(method, path, bytes.NewReader(raw))
	r = r.WithContext(tenant.WithPrincipal(t.Context(), p))
	w := httptest.NewRecorder()
	f.mux.ServeHTTP(w, r)
	if w.Code != want {
		t.Fatalf("%s %s: %d want %d: %s", method, path, w.Code, want, w.Body.String())
	}
	return w.Body.Bytes()
}
func (f fixture) status(t *testing.T) Status {
	t.Helper()
	raw := f.call(t, f.p, "GET", "/api/nodes/"+f.parent+"/benefit-generation", nil, 200)
	var s Status
	if err := json.Unmarshal(raw, &s); err != nil {
		t.Fatal(err)
	}
	return s
}
func (f fixture) done(t *testing.T) {
	t.Helper()
	f.call(t, f.p, "PATCH", "/api/nodes/"+f.leaf, map[string]any{"state": "done"}, 200)
	if f.status(t).State != "queued" {
		t.Fatal("Done did not queue benefits")
	}
}
func (f fixture) readFields(t *testing.T) map[string]any {
	t.Helper()
	raw := f.call(t, f.p, "GET", "/api/nodes/"+f.parent, nil, 200)
	var n struct {
		Fields map[string]any `json:"fields"`
		State  string         `json:"state"`
	}
	_ = json.Unmarshal(raw, &n)
	if n.State != "done" {
		t.Fatalf("parent Done changed to %s", n.State)
	}
	return n.Fields
}
func TestDoneDoesNotWaitAndSummarisesLeaves(t *testing.T) {
	f := setup(t)
	entered, release := make(chan struct{}), make(chan struct{})
	f.m.chat = func(ctx context.Context, tid, pid string, revision int64, ms []modelprovider.Message) (modelprovider.Completion, error) {
		if !strings.Contains(ms[1].Content, sample.BenefitEN) || strings.Contains(ms[1].Content, f.leaf) {
			return modelprovider.Completion{}, errors.New("wrong source")
		}
		close(entered)
		select {
		case <-release:
		case <-ctx.Done():
			return modelprovider.Completion{}, ctx.Err()
		}
		raw, _ := json.Marshal(summary)
		return modelprovider.Completion{Text: string(raw)}, nil
	}
	f.done(t)
	result := make(chan error, 1)
	go func() { _, err := f.m.Once(t.Context(), f.p.TenantID); result <- err }()
	select {
	case <-entered:
	case <-t.Context().Done():
		t.Fatal("provider not entered")
	}
	// A separate GET must finish while the external request is held at a barrier.
	if s := f.status(t); s.State != "running" {
		t.Fatal(s)
	}
	fields := f.readFields(t)
	if _, exists := fields["benefit_en"]; exists {
		t.Fatal("generated before provider answered")
	}
	close(release)
	if err := <-result; err != nil {
		t.Fatal(err)
	}
	if s := f.status(t); s.State != "generated" || !s.Generated {
		t.Fatal(s)
	}
	if f.readFields(t)["benefit_de"] != summary.BenefitDE {
		t.Fatal("German summary missing")
	}
}
func TestFailureVisibleRetryAndLeafGate(t *testing.T) {
	f := setup(t)
	// Migrated work leaves keep the historical bilingual completion requirement.
	f.call(t, f.p, "PATCH", "/api/nodes/"+f.leaf, map[string]any{"fields": map[string]any{}}, 200)
	raw := f.call(t, f.p, "PATCH", "/api/nodes/"+f.leaf, map[string]any{"state": "done"}, 422)
	if !bytes.Contains(raw, []byte(`"code":"benefit_required"`)) {
		t.Fatalf("wrong rejection: %s", raw)
	}
	f.call(t, f.p, "PATCH", "/api/nodes/"+f.leaf, map[string]any{"fields": sample}, 200)
	f.done(t)
	f.m.chat = func(context.Context, string, string, int64, []modelprovider.Message) (modelprovider.Completion, error) {
		return modelprovider.Completion{}, errors.New("private provider response")
	}
	if _, err := f.m.Once(t.Context(), f.p.TenantID); err != nil {
		t.Fatal(err)
	}
	failed := f.status(t)
	if failed.State != "failed" || failed.Error != failureProvider {
		t.Fatal(failed)
	}
	f.readFields(t)
	body := map[string]any{"expected_generation": failed.Generation, "expected_revision": failed.Revision}
	f.call(t, f.p, "POST", "/api/nodes/"+f.parent+"/benefit-generation/retry", body, 202)
	f.call(t, f.p, "POST", "/api/nodes/"+f.parent+"/benefit-generation/retry", body, 409)
	f.m.chat = func(context.Context, string, string, int64, []modelprovider.Message) (modelprovider.Completion, error) {
		raw, _ := json.Marshal(summary)
		return modelprovider.Completion{Text: string(raw)}, nil
	}
	if _, err := f.m.Once(t.Context(), f.p.TenantID); err != nil {
		t.Fatal(err)
	}
	if f.status(t).State != "generated" {
		t.Fatal("retry did not generate")
	}
}
func TestHumanEditAndSourceChangeFence(t *testing.T) {
	for _, change := range []string{"edit", "source", "reopen", "permission", "provider"} {
		t.Run(change, func(t *testing.T) {
			f := setup(t)
			f.done(t)
			j, err := f.m.claim(t.Context(), f.p.TenantID)
			if err != nil || j == nil {
				t.Fatal(err)
			}
			switch change {
			case "edit":
				f.call(t, f.p, "PATCH", "/api/nodes/"+f.parent, map[string]any{"fields": sample}, 200)
			case "source":
				s := sample
				s.BenefitEN = "A leaf changed after generation started."
				f.call(t, f.p, "PATCH", "/api/nodes/"+f.leaf, map[string]any{"fields": s}, 200)
			case "reopen":
				f.call(t, f.p, "PATCH", "/api/nodes/"+f.leaf, map[string]any{"state": "open"}, 200)
			case "permission":
				dbtest.BindRole(t, f.d, f.p.TenantID, f.p.ID, "viewer")
			case "provider":
				_, err = f.provider.Save(tenant.WithPrincipal(t.Context(), f.p), f.p, modelprovider.Write{ExpectedRevision: 1, Settings: modelprovider.Settings{}})
				if err != nil {
					t.Fatal(err)
				}
			}
			if err := f.m.finish(t.Context(), *j, summary); err != nil {
				t.Fatal(err)
			}
			s := f.status(t)
			if s.State == "generated" || s.Generated {
				t.Fatal("stale result applied", s)
			}
			if change == "edit" {
				if s.State != "edited" || f.readFields(t)["benefit_en"] != sample.BenefitEN {
					t.Fatal("human text overwritten")
				}
			}
		})
	}
}
func TestLeaseRecoveryAndTenantIsolation(t *testing.T) {
	f := setup(t)
	f.done(t)
	old, err := f.m.claim(t.Context(), f.p.TenantID)
	if err != nil || old == nil {
		t.Fatal(err)
	}
	// Set the database lease boundary directly: no elapsed-time assertion or sleep.
	err = db.InTenant(dbtest.Seed(t.Context()), f.d.App, f.p.TenantID, func(tx pgx.Tx) error {
		return writeMeta(t.Context(), tx, f.parent, `benefit_generation || jsonb_build_object('lease_until',clock_timestamp()-interval '1 second')`)
	})
	if err != nil {
		t.Fatal(err)
	}
	next, err := f.m.claim(t.Context(), f.p.TenantID)
	if err != nil || next == nil {
		t.Fatal(err)
	}
	if old.Generation == next.Generation {
		t.Fatal("lease not fenced")
	}
	if err := f.m.finish(t.Context(), *old, summary); err != nil {
		t.Fatal(err)
	}
	if f.status(t).State != "running" {
		t.Fatal("old owner applied")
	}
	if err := f.m.finish(t.Context(), *next, summary); err != nil {
		t.Fatal(err)
	}
	other := f.p
	other.TenantID = "33333333-3333-4333-8333-333333333333"
	f.call(t, other, "GET", "/api/nodes/"+f.parent+"/benefit-generation", nil, 404)
}
func TestSummaryRejectsInvalidOrUnboundedModelOutput(t *testing.T) {
	raw, _ := json.Marshal(summary)
	for _, text := range []string{`{}`, `{"pill_en":"One"}`, string(raw) + ` {}`, string(raw[:len(raw)-1]) + `,"instruction":"surprise"}`, strings.Repeat("x", maxTextBytes*4+1)} {
		if _, err := parse(text); err == nil {
			t.Fatal("invalid output accepted")
		}
	}
	if _, err := parse(string(raw)); err != nil {
		t.Fatal(err)
	}
}
