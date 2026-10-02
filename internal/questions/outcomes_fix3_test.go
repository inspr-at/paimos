// SPDX-License-Identifier: AGPL-3.0-only
package questions

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/rules/doctrine"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

func decideOnlyPerson(t *testing.T, f *deliveryFixture) tenant.Principal {
	t.Helper()
	p := tenant.Principal{TenantID: f.person.TenantID, Kind: tenant.Person}
	var role string
	if err := f.d.Admin.QueryRow(t.Context(), `INSERT INTO principals(tenant_id,kind,name) VALUES($1,'person','decide only') RETURNING id::text`, p.TenantID).Scan(&p.ID); err != nil {
		t.Fatal(err)
	}
	if err := f.d.Admin.QueryRow(t.Context(), `INSERT INTO roles(tenant_id,key,name) VALUES($1,'decide_only','Decide only') RETURNING id::text`, p.TenantID).Scan(&role); err != nil {
		t.Fatal(err)
	}
	if _, err := f.d.Admin.Exec(t.Context(), `INSERT INTO role_permissions(tenant_id,role_id,permission) SELECT $1,$2,unnest(ARRAY['nodes.read','questions.read','questions.decide'])`, p.TenantID, role); err != nil {
		t.Fatal(err)
	}
	if _, err := f.d.Admin.Exec(t.Context(), `INSERT INTO role_bindings(tenant_id,principal_id,role_id,scope_type,scope_id) VALUES($1,$2,$3,'project',$4)`, p.TenantID, p.ID, role, f.project); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestFix3UnavailablePreviousTicketDoesNotBlockCorrection(t *testing.T) {
	for _, state := range []string{"deleted", "moved", "converted", "permission_lost"} {
		t.Run(state, func(t *testing.T) {
			f := deliveryFixtureFor(t)
			in := input()
			in.TicketID = f.ticket
			q := f.outcome(t, f.ask(t, in), "requirement", "Keep this exact criterion")
			f.advance(10 * time.Second)
			f.dispatch(t)
			previous := outcomeRow(t, f.status(t, q.ID))
			if previous.State != "delivered" || previous.EffectData.Criterion == "" {
				t.Fatalf("missing initial criterion: %+v", previous)
			}
			original := f.criteria(t)
			actor := f.person
			switch state {
			case "deleted":
				if _, err := f.d.Admin.Exec(t.Context(), `UPDATE nodes SET deleted_at=clock_timestamp() WHERE id=$1`, f.ticket); err != nil {
					t.Fatal(err)
				}
			case "moved":
				// The membership FK requires routing to be detached before a move.
				// Keep the immutable answer/effect's earlier ticket reference intact.
				if _, err := f.d.Admin.Exec(t.Context(), `UPDATE desk_askers SET ticket_id=NULL,comment_node_id=question_id WHERE question_id=$1`, q.ID); err != nil {
					t.Fatal(err)
				}
				if _, err := f.d.Admin.Exec(t.Context(), `UPDATE nodes SET parent_id=$2 WHERE id=$1`, f.ticket, f.hidden); err != nil {
					t.Fatal(err)
				}
				if f.count(t, `SELECT count(*) FROM nodes WHERE id=$1 AND project_id=$2`, f.ticket, f.hidden) != 1 {
					t.Fatal("fixture did not move ticket")
				}
			case "converted":
				if _, err := f.d.Admin.Exec(t.Context(), `UPDATE nodes SET kind_id=(SELECT id FROM node_kinds WHERE tenant_id=$2 AND slug='guideline') WHERE id=$1`, f.ticket, f.person.TenantID); err != nil {
					t.Fatal(err)
				}
			case "permission_lost":
				actor = decideOnlyPerson(t, f)
			}
			q = question(t, request(t.Context(), f.mux, actor, "POST", "/api/questions/"+q.ID+"/decision", DecisionInput{RequestID: uid(), ExpectedRevision: q.Revision, Outcome: "once", Answer: "Only this question now"}), 200)
			f.advance(10 * time.Second)
			f.dispatch(t)
			e := outcomeRow(t, f.status(t, q.ID))
			if e.State != "delivered" || len(e.EffectData.ReviewRequired) != 1 || e.EffectData.ReviewRequired[0].Kind != "criterion" || e.EffectData.ReviewRequired[0].Ref != f.ticket {
				t.Fatalf("unavailable previous ticket blocked correction: %+v", e)
			}
			if f.criteria(t) != original {
				t.Fatal("inaccessible criterion was changed")
			}
			var old EffectData
			if err := f.d.Admin.QueryRow(t.Context(), `SELECT effect_data FROM desk_pending WHERE id=$1`, previous.ID).Scan(&old); err != nil {
				t.Fatal(err)
			}
			if old.TicketID != f.ticket || old.Criterion != previous.EffectData.Criterion || old.SupersededBy != e.ID {
				t.Fatal("correction lost previous criterion provenance")
			}
			f.advance(time.Minute)
			f.dispatch(t)
			if f.count(t, `SELECT count(*) FROM events WHERE type='question.outcome_applied' AND after->>'effect_id'=$1`, e.ID) != 1 {
				t.Fatal("correction replay duplicated")
			}
		})
	}
}

func TestFix3DecideOnlyCannotWithdrawActiveKnowledge(t *testing.T) {
	f := deliveryFixtureFor(t)
	q := f.outcome(t, f.ask(t, input()), "always", "Owner approved reusable answer")
	f.advance(10 * time.Second)
	f.dispatch(t)
	before := f.status(t, q.ID)
	actor := decideOnlyPerson(t, f)
	w := request(t.Context(), f.mux, actor, "POST", "/api/questions/"+q.ID+"/decision", DecisionInput{RequestID: uid(), ExpectedRevision: q.Revision, Outcome: "once", Answer: "Withdraw another person's answer"})
	if w.Code != 403 || !strings.Contains(w.Body.String(), "knowledge_access_lost") {
		t.Fatalf("Knowledge withdrawal without authority: %d: %s", w.Code, w.Body.String())
	}
	after := f.status(t, q.ID)
	if after.Revision != before.Revision || after.Answer.ID != before.Answer.ID || f.knowledge(t, q.Answer.ID).Status != "active" {
		t.Fatal("denied withdrawal changed answer or Knowledge")
	}
	if f.count(t, `SELECT count(*) FROM desk_answers WHERE question_id=$1`, q.ID) != 1 || f.count(t, `SELECT count(*) FROM events WHERE type='knowledge.updated' AND node_id=$1`, q.Answer.ID) != 0 {
		t.Fatal("denied withdrawal left writes or events")
	}
	// Decide-only authority remains sufficient when there is no Knowledge to withdraw.
	ordinary := f.ask(t, input())
	question(t, request(t.Context(), f.mux, actor, "POST", "/api/questions/"+ordinary.ID+"/decision", DecisionInput{RequestID: uid(), ExpectedRevision: ordinary.Revision, Outcome: "once", Answer: "An ordinary answer"}), 200)
	f.outcome(t, q, "once", "Authorized owner withdrawal")
	if f.knowledge(t, q.Answer.ID).Status != "archived" {
		t.Fatal("authorized withdrawal failed")
	}
}

// Index the synthetic private corpus through the real handler. No guard format
// is reimplemented here, and this transport can never publish or reach a network.
type fix3IndexTransport struct {
	t       *testing.T
	content []byte
	calls   atomic.Int32
}

func (r *fix3IndexTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	r.calls.Add(1)
	const commit = "1111111111111111111111111111111111111111"
	var body any
	switch {
	case req.Method == "GET" && strings.Contains(req.URL.Path, "/commits/"):
		body = map[string]any{"sha": commit, "commit": map[string]any{"committer": map[string]any{"date": "2026-10-02T12:00:00Z"}}}
	case req.Method == "GET" && strings.Contains(req.URL.Path, "/git/trees/"):
		body = map[string]any{"sha": commit, "truncated": false, "tree": []map[string]any{{"path": "AGENTS.md", "type": "blob", "mode": "100644", "sha": doctrine.BlobSHA(r.content), "size": len(r.content)}}}
	case req.Method == "GET" && strings.Contains(req.URL.Path, "/git/blobs/"):
		body = map[string]any{"encoding": "base64", "content": base64.StdEncoding.EncodeToString(r.content), "size": len(r.content)}
	default:
		r.t.Errorf("unexpected doctrine network action: %s %s", req.Method, req.URL.Path)
		return nil, os.ErrPermission
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(bytes.NewReader(raw)), Request: req}, nil
}
func fix3PublicTarget(t *testing.T, f *deliveryFixture, pool *pgxpool.Pool) (*DoctrineTarget, *fix3IndexTransport) {
	t.Helper()
	const private = "inspr-at/inspr-doctrine-private"
	const public = "inspr-at/inspr-modules"
	const commit = "1111111111111111111111111111111111111111"
	content := []byte("# AGENTS — Test\n\n## Work\n\n- 🟡 Keep commits small and review changes before shipping.\n")
	dir := t.TempDir()
	policy, _ := json.Marshal(map[string]any{"grants": []map[string]string{{"tenant_id": f.person.TenantID, "repository": private}}})
	if err := os.WriteFile(filepath.Join(dir, "fixture-read.allowlist.json"), policy, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "fixture-read"), []byte("synthetic-fixture-read-only"), 0600); err != nil {
		t.Fatal(err)
	}
	transport := &fix3IndexTransport{t: t, content: content}
	m := doctrine.New(pool, doctrine.Options{CredentialsDir: dir, GuardKey: bytes.Repeat([]byte{4}, 32), Client: &http.Client{Transport: transport}, App: doctrine.AppConfig{ID: "8", InstallationID: "9", KeyRef: "fixture-app", TenantID: f.person.TenantID, GateLogin: "fixture-gate", DCOAcknowledged: true}})
	f.m.WithDoctrine(m)
	m.Mount(f.mux)
	w := request(t.Context(), f.mux, f.person, "POST", "/api/rules/doctrine/sources", doctrine.SourceInput{Repository: private, Visibility: "private", Ref: "main", CredentialRef: "fixture-read"})
	if w.Code != 200 {
		t.Fatalf("private corpus indexing: %d: %s", w.Code, w.Body.String())
	}
	var source string
	if err := f.d.Admin.QueryRow(t.Context(), `INSERT INTO doctrine_sources(tenant_id,repository,visibility,commit_sha,paths,indexed_at) VALUES($1,$2,'public',$3,ARRAY['AGENTS.md'],clock_timestamp()) RETURNING id::text`, f.person.TenantID, public, commit).Scan(&source); err != nil {
		t.Fatal(err)
	}
	if _, err := f.d.Admin.Exec(t.Context(), `INSERT INTO doctrine_cache(tenant_id,source_id,commit_sha,path,blob_sha,content) VALUES($1,$2,$3,'AGENTS.md',$4,$5)`, f.person.TenantID, source, commit, doctrine.BlobSHA(content), content); err != nil {
		t.Fatal(err)
	}
	tree, _ := json.Marshal([]doctrine.Entry{{Path: "AGENTS.md", SHA: doctrine.BlobSHA(content), Size: len(content)}})
	if _, err := f.d.Admin.Exec(t.Context(), `INSERT INTO doctrine_public_main_cache(tenant_id,source_id,pin_commit,main_commit,observed_at,tree) VALUES($1,$2,$3,$3,clock_timestamp(),$4)`, f.person.TenantID, source, commit, tree); err != nil {
		t.Fatal(err)
	}
	rule := doctrine.Render(public, commit, false, []doctrine.File{{Path: "AGENTS.md", BlobSHA: doctrine.BlobSHA(content), Content: content}})[0].Rules[0]
	return &DoctrineTarget{SourceID: source, Path: "AGENTS.md", RuleKey: rule.Key, RuleSHA: rule.SHA256, TLDREN: "Keep commits small and reviewed."}, transport
}

// A source refresh between preparation and the fenced write forces a real
// stale_source refusal exactly once, without sleeps or changing the rule.
type fix3RefreshAdapter struct {
	DoctrineAdapter
	f         *deliveryFixture
	source    string
	refreshed bool
}

func (a *fix3RefreshAdapter) PrepareDeskDraft(ctx context.Context, p tenant.Principal, in doctrine.InboxInput) (*doctrine.PreparedInbox, error) {
	prepared, err := a.DoctrineAdapter.PrepareDeskDraft(ctx, p, in)
	if err == nil && !a.refreshed {
		a.refreshed = true
		_, err = a.f.d.Admin.Exec(ctx, `UPDATE doctrine_sources SET indexed_at=clock_timestamp() WHERE id=$1`, a.source)
	}
	return prepared, err
}
func TestFix3TransientDoctrineFailuresRetryWithoutNewDecision(t *testing.T) {
	for _, code := range []string{"stale_source", "public_main_unavailable"} {
		t.Run(code, func(t *testing.T) {
			f := deliveryFixtureFor(t)
			target, transport := fix3PublicTarget(t, f, f.d.App)
			in := input()
			in.Doctrine = target
			q := f.outcome(t, f.ask(t, in), "doctrine", "- 🟡 Keep commits small and review changes before shipping. Run tests.")
			calls := transport.calls.Load()
			if code == "stale_source" {
				f.m.doctrine = &fix3RefreshAdapter{DoctrineAdapter: f.m.doctrine, f: f, source: target.SourceID}
			} else {
				if _, err := f.d.Admin.Exec(t.Context(), `UPDATE doctrine_public_main_cache SET observed_at=clock_timestamp()-interval '6 minutes' WHERE source_id=$1`, target.SourceID); err != nil {
					t.Fatal(err)
				}
			}
			f.advance(10 * time.Second)
			f.dispatch(t)
			e := outcomeRow(t, f.status(t, q.ID))
			if e.State != "failed" || e.ErrorCode != code || e.EffectData.Retryable == nil || !*e.EffectData.Retryable {
				t.Fatalf("transient doctrine failure became final: %+v", e)
			}
			if f.count(t, `SELECT count(*) FROM desk_pending WHERE id=$1 AND retry_at IS NOT NULL`, e.ID) != 1 || f.count(t, `SELECT count(*) FROM doctrine_proposals`) != 0 {
				t.Fatal("missing retry schedule or partial draft")
			}
			if code == "public_main_unavailable" {
				if _, err := f.d.Admin.Exec(t.Context(), `UPDATE doctrine_public_main_cache SET observed_at=clock_timestamp() WHERE source_id=$1`, target.SourceID); err != nil {
					t.Fatal(err)
				}
			}
			f.advance(30 * time.Second)
			f.dispatch(t)
			e = outcomeRow(t, f.status(t, q.ID))
			if e.State != "delivered" || e.EffectData.DoctrineID == "" || f.count(t, `SELECT count(*) FROM desk_answers WHERE question_id=$1`, q.ID) != 1 {
				t.Fatalf("retry did not recover same decision: %+v", e)
			}
			f.dispatch(t)
			if f.count(t, `SELECT count(*) FROM doctrine_proposals`) != 1 || transport.calls.Load() != calls {
				t.Fatal("retry duplicated or performed network action")
			}
		})
	}
}

// Each exact rendering/guard read is a barrier. Decide intentionally holds a
// bounded fence; dispatch must prepare without it. Both paths sample both reads.
type fix3FenceProbe struct {
	t             *testing.T
	f             *deliveryFixture
	enabled, held bool
	cache, guard  atomic.Int32
}

func (p *fix3FenceProbe) TraceQueryStart(ctx context.Context, conn *pgx.Conn, d pgx.TraceQueryStartData) context.Context {
	if !p.enabled {
		return ctx
	}
	switch {
	case strings.Contains(d.SQL, "FROM doctrine_cache"):
		p.cache.Add(1)
	case strings.Contains(d.SQL, "FROM doctrine_private_guard"):
		p.guard.Add(1)
	default:
		return ctx
	}
	tx, err := p.f.d.Admin.Begin(ctx)
	if err != nil {
		p.t.Error(err)
		return ctx
	}
	defer tx.Rollback(ctx)
	_, err = tx.Exec(ctx, `SELECT id FROM tenants WHERE id=$1 FOR NO KEY UPDATE NOWAIT`, p.f.person.TenantID)
	if p.held {
		pgerr, ok := err.(*pgconn.PgError)
		if !ok || pgerr.Code != "55P03" {
			p.t.Errorf("decide preparation did not hold expected fence: %v", err)
		}
		var lock, statement string
		if err := conn.QueryRow(ctx, `SELECT current_setting('lock_timeout'),current_setting('statement_timeout')`).Scan(&lock, &statement); err != nil {
			p.t.Error(err)
		}
		if lock != "3s" || statement != "10s" {
			p.t.Errorf("decide preparation unbounded: lock=%s statement=%s", lock, statement)
		}
	} else if err != nil {
		p.t.Errorf("dispatch preparation held mutation fence: %v", err)
	}
	return ctx
}
func (*fix3FenceProbe) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}
func TestFix3DoctrineDecideAndDispatcherFenceProbes(t *testing.T) {
	f := deliveryFixtureFor(t)
	probe := &fix3FenceProbe{t: t, f: f}
	config := f.d.App.Config()
	config.ConnConfig.Tracer = probe
	pool, err := pgxpool.NewWithConfig(t.Context(), config)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	f.m.pool = pool
	target, _ := fix3PublicTarget(t, f, pool)
	in := input()
	in.Doctrine = target
	q := f.ask(t, in)
	probe.enabled, probe.held = true, true
	q = f.outcome(t, q, "doctrine", "- 🟡 Keep commits small and review changes before shipping. Run tests.")
	if probe.cache.Load() == 0 || probe.guard.Load() == 0 {
		t.Fatal("decide probe did not sample rendering and guard")
	}
	probe.held = false
	probe.cache.Store(0)
	probe.guard.Store(0)
	f.advance(10 * time.Second)
	// Exercise dispatchOne itself, including prepareDoctrineEffect before treeLock.
	var id string
	if err := f.d.Admin.QueryRow(t.Context(), `SELECT id::text FROM desk_pending WHERE question_id=$1 AND revision=$2 AND kind='outcome'`, q.ID, q.Revision).Scan(&id); err != nil {
		t.Fatal(err)
	}
	if err := f.m.dispatchOne(t.Context(), f.person.TenantID, id); err != nil {
		t.Fatal(err)
	}
	if probe.cache.Load() == 0 || probe.guard.Load() == 0 {
		t.Fatal("dispatcher probe did not sample rendering and guard")
	}
	if e := outcomeRow(t, f.status(t, q.ID)); e.State != "delivered" || e.EffectData.DoctrineID == "" {
		t.Fatalf("fence probe did not create draft: %+v", e)
	}
}

// Keep the fault hook on the production adapter contract.
var _ DoctrineAdapter = (*fix3RefreshAdapter)(nil)
