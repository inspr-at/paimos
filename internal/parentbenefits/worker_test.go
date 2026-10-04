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
	"time"

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
	case err := <-result:
		t.Fatalf("worker finished before provider barrier: %v", err)
	case <-t.Context().Done():
		t.Fatal("provider not entered")
	}
	// A second server must not claim an unexpired attempt.
	if claimed, err := New(f.d.App, f.m.chat).Once(t.Context(), f.p.TenantID); err != nil || claimed {
		t.Fatal("overlapping worker claimed active lease", claimed, err)
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

func TestPublishedSnapshotAndUnrelatedFieldsSurvive(t *testing.T) {
	f := setup(t)
	var project string
	var original string
	// Historical published parents may have incomplete benefits; this parent
	// must still need generation when its leaf completes.
	parentTexts := Texts{}
	err := db.InTenant(dbtest.Seed(t.Context()), f.d.App, f.p.TenantID, func(tx pgx.Tx) error {
		if err := tx.QueryRow(t.Context(), `INSERT INTO nodes(tenant_id,kind_id,title,key) SELECT $1,id,'Published project','PRJ-1' FROM node_kinds WHERE slug='project' RETURNING id::text`, f.p.TenantID).Scan(&project); err != nil {
			return err
		}

		if _, err := tx.Exec(t.Context(), `UPDATE nodes SET parent_id=$2 WHERE id=$1`, f.parent, project); err != nil {
			return err
		}
		var linked int
		if err := tx.QueryRow(t.Context(), `SELECT count(*) FROM nodes WHERE id=ANY($1::uuid[]) AND project_id=$2`, []string{f.parent, f.leaf}, project).Scan(&linked); err != nil {
			return err
		}
		if linked != 2 {
			t.Fatal("snapshot fixtures are not in their published project", linked)
		}
		return tx.QueryRow(t.Context(), `INSERT INTO release_manifest_note_snapshots(tenant_id,project_node_id,version,snapshot)
        SELECT $1::uuid,$2::uuid,'frozen-fixture',jsonb_build_object(
          'schema','aeon.release-note-snapshot.v1','membership_source','release-manifest-tickets',
          'label','backfilled','backfilled',true,'frozen',true,
          'tenant_id',$1::uuid::text,'project_node_id',$2::uuid::text,'version','frozen-fixture',
          'items',(SELECT jsonb_agg(jsonb_build_object('id',id,'key',key,'fields',aeon_benefit_texts(fields)) ORDER BY key)
                   FROM nodes WHERE id=ANY($3::uuid[]) AND project_id=$2))
        RETURNING snapshot::text`, f.p.TenantID, project, []string{f.parent, f.leaf}).Scan(&original)
	})
	if err != nil {
		t.Fatal(err)
	}

	// Assert exact source identities and frozen text before exercising writes.
	var snapshot struct {
		Items []struct {
			ID     string `json:"id"`
			Fields Texts  `json:"fields"`
		} `json:"items"`
	}
	if err := json.Unmarshal([]byte(original), &snapshot); err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Items) != 2 || snapshot.Items[0].ID != f.parent || snapshot.Items[1].ID != f.leaf || snapshot.Items[0].Fields != parentTexts || snapshot.Items[1].Fields != sample {
		t.Fatal("snapshot does not contain the edited records", original)
	}
	assertFrozen := func(stage string) {
		t.Helper()
		var current string
		if err := db.InTenant(dbtest.Seed(t.Context()), f.d.App, f.p.TenantID, func(tx pgx.Tx) error {
			return tx.QueryRow(t.Context(), `SELECT snapshot::text FROM release_manifest_note_snapshots WHERE project_node_id=$1 AND version='frozen-fixture'`, project).Scan(&current)
		}); err != nil {
			t.Fatal(err)
		}
		if current != original {
			t.Fatal(stage + ": published note snapshot rewritten")
		}
	}
	f.done(t)
	j, err := f.m.claim(t.Context(), f.p.TenantID)
	if err != nil || j == nil {
		t.Fatal(err)
	}
	// A concurrent unrelated edit is preserved by the four-field merge.
	f.call(t, f.p, "PATCH", "/api/nodes/"+f.parent, map[string]any{"fields": map[string]any{"priority": "high", "hide_from_release_notes": true}}, 200)
	if err := f.m.finish(t.Context(), *j, summary); err != nil {
		t.Fatal(err)
	}
	fields := f.readFields(t)
	if fields["priority"] != "high" || fields["hide_from_release_notes"] != true || fields["benefit_en"] != summary.BenefitEN {
		t.Fatal("unrelated fields lost", fields)
	}

	assertFrozen("generation")
	// A subsequent completion fails visibly; retry must preserve the same notes.
	f.call(t, f.p, "PATCH", "/api/nodes/"+f.leaf, map[string]any{"state": "open"}, 200)
	f.done(t)
	chat := f.m.chat
	f.m.chat = func(context.Context, string, string, int64, []modelprovider.Message) (modelprovider.Completion, error) {
		return modelprovider.Completion{}, errors.New("provider unavailable")
	}
	if ran, err := f.m.Once(t.Context(), f.p.TenantID); err != nil || !ran {
		t.Fatal("failure attempt did not run", ran, err)
	}
	failed := f.status(t)
	if failed.State != "failed" || failed.Error != failureProvider {
		t.Fatal("wrong failure before retry", failed)
	}
	assertFrozen("failed generation")
	f.m.chat = chat
	f.call(t, f.p, "POST", "/api/nodes/"+f.parent+"/benefit-generation/retry", map[string]any{"expected_generation": failed.Generation, "expected_revision": failed.Revision}, 202)
	assertFrozen("retry queued")
	if ran, err := f.m.Once(t.Context(), f.p.TenantID); err != nil || !ran {
		t.Fatal("retry did not run", ran, err)
	}
	if status := f.status(t); status.State != "generated" || !status.Generated {
		t.Fatal("retry did not regenerate", status)
	}
	assertFrozen("retry generated")
	f.call(t, f.p, "PATCH", "/api/nodes/"+f.parent, map[string]any{"fields": sample}, 200)
	if f.status(t).Generated {
		t.Fatal("edited text still marked generated")
	}
	assertFrozen("manual parent edit")
	editedLeaf := sample
	editedLeaf.BenefitDE = "Der manuell bearbeitete Nutzen verändert keine veröffentlichten Notizen."
	f.call(t, f.p, "PATCH", "/api/nodes/"+f.leaf, map[string]any{"fields": editedLeaf}, 200)
	assertFrozen("manual leaf edit")
}
func TestNestedLeafSourcesAndExplicitLimits(t *testing.T) {
	f := setup(t)
	err := db.InTenant(dbtest.Seed(t.Context()), f.d.App, f.p.TenantID, func(tx pgx.Tx) error {
		raw, _ := json.Marshal(sample)
		if _, err := tx.Exec(t.Context(), `INSERT INTO nodes(tenant_id,kind_id,title,key,state,fields,parent_id) SELECT $1,id,'Nested leaf','BEN-3','done',$3,$2 FROM node_kinds WHERE slug='work'`, f.p.TenantID, f.leaf, raw); err != nil {
			return err
		}
		if _, err := tx.Exec(t.Context(), `INSERT INTO nodes(tenant_id,kind_id,title,key,fields,parent_id) SELECT $1,id,'Non-work knowledge','MEM-1','{}',$2 FROM node_kinds WHERE slug='memory'`, f.p.TenantID, f.parent); err != nil {
			return err
		}
		leaves, _, err := sources(t.Context(), tx, f.parent)
		if err != nil {
			return err
		}
		if len(leaves) != 1 || leaves[0].ID == f.leaf || leaves[0].BenefitEN != sample.BenefitEN {
			t.Fatalf("not leaf-only: %+v", leaves)
		}
		// One additional leaf beyond the source budget must fail, not truncate.
		if _, err := tx.Exec(t.Context(), `INSERT INTO nodes(tenant_id,kind_id,title,key,state,fields,parent_id) SELECT $1,k.id,'Limit leaf','LIM-'||g,'done',$3,$2 FROM node_kinds k CROSS JOIN generate_series(1,$4)g WHERE slug='work'`, f.p.TenantID, f.parent, raw, maxLeaves); err != nil {
			return err
		}
		_, _, err = sources(t.Context(), tx, f.parent)
		if !errors.Is(err, errSources) {
			t.Fatalf("wrong scope-limit result: %v", err)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
func TestSourceTextBudgetAndMissingBenefitsFailVisibly(t *testing.T) {
	for _, missing := range []bool{true, false} {
		t.Run(map[bool]string{true: "missing", false: "oversized"}[missing], func(t *testing.T) {
			f := setup(t)
			f.done(t)
			text := sample
			if missing {
				text.BenefitDE = ""
			} else {
				text.BenefitDE = strings.Repeat("x", maxTextBytes+1)
			}
			f.call(t, f.p, "PATCH", "/api/nodes/"+f.leaf, map[string]any{"fields": text}, 200)
			calls := 0
			f.m.chat = func(context.Context, string, string, int64, []modelprovider.Message) (modelprovider.Completion, error) {
				calls++
				return modelprovider.Completion{}, nil
			}
			if _, err := f.m.Once(t.Context(), f.p.TenantID); err != nil {
				t.Fatal(err)
			}
			s := f.status(t)
			if calls != 0 || s.State != "failed" || s.Error != errSources.Error() {
				t.Fatal("bad source sent or wrong failure", calls, s)
			}
			f.readFields(t)
		})
	}
}
func TestFlagAndProviderOffNeverPreventDone(t *testing.T) {
	for _, off := range []string{"flag", "provider"} {
		t.Run(off, func(t *testing.T) {
			f := setup(t)
			if off == "flag" {
				if err := db.InTenant(dbtest.Seed(t.Context()), f.d.App, f.p.TenantID, func(tx pgx.Tx) error {
					_, err := tx.Exec(t.Context(), `UPDATE features SET enabled=false WHERE key='work-parent-status'`)
					return err
				}); err != nil {
					t.Fatal(err)
				}
				f.call(t, f.p, "PATCH", "/api/nodes/"+f.leaf, map[string]any{"state": "done"}, 200)
				if f.status(t).State != "none" {
					t.Fatal("feature off queued generation")
				}
			} else {
				if _, err := f.provider.Save(tenant.WithPrincipal(t.Context(), f.p), f.p, modelprovider.Write{ExpectedRevision: 1, Settings: modelprovider.Settings{}}); err != nil {
					t.Fatal(err)
				}
				f.done(t)
				if _, err := f.m.Once(t.Context(), f.p.TenantID); err != nil {
					t.Fatal(err)
				}
				if f.status(t).State != "failed" {
					t.Fatal("provider off reported success")
				}
				f.readFields(t)
			}
		})
	}
}

func TestRetryIsPersonOnlyAndRechecksPermission(t *testing.T) {
	f := setup(t)
	f.done(t)
	f.m.chat = func(context.Context, string, string, int64, []modelprovider.Message) (modelprovider.Completion, error) {
		return modelprovider.Completion{}, errors.New("fixture outage")
	}
	if _, err := f.m.Once(t.Context(), f.p.TenantID); err != nil {
		t.Fatal(err)
	}
	s := f.status(t)
	body := map[string]any{"expected_generation": s.Generation, "expected_revision": s.Revision}
	agent := f.p
	agent.Kind = tenant.Agent
	f.call(t, agent, "POST", "/api/nodes/"+f.parent+"/benefit-generation/retry", body, 403)
	dbtest.BindRole(t, f.d, f.p.TenantID, f.p.ID, "viewer")
	f.call(t, f.p, "POST", "/api/nodes/"+f.parent+"/benefit-generation/retry", body, 403)
	if now := f.status(t); now.Generation != s.Generation || now.State != "failed" {
		t.Fatal("rejected retry changed attempt", now)
	}
	dbtest.BindRole(t, f.d, f.p.TenantID, f.p.ID, "admin")
	body["expected_revision"] = s.Revision.Add(-time.Second)
	f.call(t, f.p, "POST", "/api/nodes/"+f.parent+"/benefit-generation/retry", body, 409)
}

func TestCombinedHumanEditAndStatusChangeKeepsAuthoredText(t *testing.T) {
	f := setup(t)
	f.done(t)
	j, err := f.m.claim(t.Context(), f.p.TenantID)
	if err != nil || j == nil {
		t.Fatal(err)
	}
	// Exercise the combined-write boundary in the engine's write context; a
	// person-facing status request remains forbidden by the parent status guard.
	err = db.InTenant(dbtest.Seed(t.Context()), f.d.App, f.p.TenantID, func(tx pgx.Tx) error {
		if _, err := tx.Exec(t.Context(), `SELECT set_config('aeon.work_status_deriving','on',true)`); err != nil {
			return err
		}
		raw, _ := json.Marshal(sample)
		_, err := tx.Exec(t.Context(), `UPDATE nodes SET state='open',fields=fields||$2::jsonb WHERE id=$1`, f.parent, raw)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := f.m.finish(t.Context(), *j, summary); err != nil {
		t.Fatal(err)
	}
	if s := f.status(t); s.State != "edited" || s.Generated || s.Generation == j.Generation {
		t.Fatal("combined edit lost its fence", s)
	}
	f.call(t, f.p, "PATCH", "/api/nodes/"+f.leaf, map[string]any{"state": "open"}, 200)
	f.call(t, f.p, "PATCH", "/api/nodes/"+f.leaf, map[string]any{"state": "done"}, 200)
	if s := f.status(t); s.State != "edited" || f.readFields(t)["benefit_en"] != sample.BenefitEN {
		t.Fatal("recompletion replaced authored text", s)
	}
}

func TestGenerationRechecksCurrentFieldSchema(t *testing.T) {
	f := setup(t)
	f.done(t)
	j, err := f.m.claim(t.Context(), f.p.TenantID)
	if err != nil || j == nil {
		t.Fatal(err)
	}
	if err := db.InTenant(dbtest.Seed(t.Context()), f.d.App, f.p.TenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE node_kinds SET field_schema=jsonb_set(field_schema,'{properties,benefit_en,maxLength}','1'::jsonb) WHERE slug='work'`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := f.m.finish(t.Context(), *j, summary); err != nil {
		t.Fatal(err)
	}
	if s := f.status(t); s.State != "failed" || !strings.Contains(s.Error, "field rules") {
		t.Fatal("current field schema was ignored", s)
	}
	if _, exists := f.readFields(t)["benefit_en"]; exists {
		t.Fatal("invalid generated fields applied")
	}
}
