// SPDX-License-Identifier: AGPL-3.0-only

package stream

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/inspr-at/paimos/internal/agentpairing"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/nativecontract"
	"github.com/inspr-at/paimos/internal/nativesessions/consent"
	"github.com/inspr-at/paimos/internal/nativestream"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

// The fake represents a separately qualified runtime and watch export. It
// supplies no production fallback, provider process or model call.
type viewerRuntime struct {
	session consent.Session
	refusal nativecontract.Reason
}

type viewerPageRecorder struct{ *httptest.ResponseRecorder }

func (*viewerPageRecorder) SetWriteDeadline(time.Time) error { return nil }

func (v *viewerRuntime) SessionTx(context.Context, pgx.Tx, nativecontract.Binding) (consent.Session, error) {
	if v.refusal != "" {
		return consent.Session{}, nativestream.Refuse(v.refusal)
	}
	return v.session, nil
}

type viewerExport struct {
	binding nativecontract.Binding
	consent string
}

func (v viewerExport) RequireViewTx(_ context.Context, _ pgx.Tx, _ tenant.Principal, binding nativecontract.Binding, id, _ string) error {
	if !binding.Equal(v.binding) || id != v.consent {
		return nativestream.Refuse(nativecontract.ReasonGrantMissing)
	}
	return nil
}

// Risk: ticket access, a project-admin role, a public viewer ID or a stale
// capability must never leak the run person's Terminal/Expert stream.
func TestCurrentViewerAuthorityScopesEveryDelivery(t *testing.T) {
	d := dbtest.Open(t)
	tid, project := uuid.NewString(), uuid.NewString()
	consentWatcher := uuid.NewString()
	people := map[string]tenant.Principal{}
	for _, name := range []string{"owner", "workspace-admin", "project-admin", "member", "agent"} {
		kind := tenant.Person
		if name == "agent" {
			kind = tenant.Agent
		}
		people[name] = tenant.Principal{ID: uuid.NewString(), TenantID: tid, Kind: kind, BrowserSession: kind == tenant.Person, Scopes: []string{"harness.read", "harness.control", "harness.watch"}}
	}
	seed := func(fn func(pgx.Tx) error) {
		t.Helper()
		if err := db.InTenant(dbtest.Seed(t.Context()), d.App, tid, fn); err != nil {
			t.Fatal(err)
		}
	}
	seed(func(tx pgx.Tx) error {
		if _, err := tx.Exec(t.Context(), `INSERT INTO tenants(id,slug,name) VALUES($1,$2,'Native viewer authority')`, tid, "viewer-"+tid); err != nil {
			return err
		}
		for name, p := range people {
			if _, err := tx.Exec(t.Context(), `INSERT INTO principals(tenant_id,id,kind,name) VALUES($1,$2,$3,$4)`, tid, p.ID, string(p.Kind), name); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(t.Context(), `INSERT INTO nodes(tenant_id,id,key,kind_id,title) SELECT $1,$2,'VIEW-1',id,'Viewer project' FROM node_kinds WHERE slug='project'`, tid, project); err != nil {
			return err
		}
		_, err := tx.Exec(t.Context(), `INSERT INTO roles(tenant_id,id,key,name,builtin) VALUES($1,$2,'native_watcher','Native watcher',false)`, tid, consentWatcher)
		return err
	})
	for name, p := range people {
		role := "member"
		if name == "workspace-admin" {
			role = "admin"
		}
		dbtest.BindRole(t, d, tid, p.ID, role)
	}
	seed(func(tx pgx.Tx) error {
		if _, err := tx.Exec(t.Context(), `INSERT INTO role_permissions(tenant_id,role_id,permission) VALUES($1,$2,'harness.watch')`, tid, consentWatcher); err != nil {
			return err
		}
		for _, name := range []string{"owner", "workspace-admin"} {
			if _, err := tx.Exec(t.Context(), `INSERT INTO role_bindings(tenant_id,principal_id,role_id,scope_type,scope_id) VALUES($1,$2,$3,'project',$4)`, tid, people[name].ID, consentWatcher, project); err != nil {
				return err
			}
		}
		return nil
	})
	seed(func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `INSERT INTO role_bindings(tenant_id,principal_id,role_id,scope_type,scope_id) SELECT $1,$2,id,'project',$3 FROM roles WHERE tenant_id=$1 AND key='admin'`, tid, people["project-admin"].ID, project)
		return err
	})
	b := nativecontract.Binding{TenantID: tid, ProjectID: project, OwnedSessionID: uuid.NewString(), HarnessSessionID: uuid.NewString(), RuntimeEpoch: uuid.NewString(), DaemonID: "fixture-daemon", DaemonGeneration: "fixture-generation", BindingRevision: 1}
	snapshot := testSnapshot()
	snapshot.CurrentBinding = &b
	snapshot.OwnedSessionID = b.OwnedSessionID
	snapshot.OwnerPersonID = people["owner"].ID
	terminalURL := "https://terminal.example.ts.net/run"
	snapshot.TerminalViewerURL = &terminalURL
	used := 10.0
	snapshot.Quota = &nativecontract.Quota{AccountID: uuid.NewString(), ProfileBindingRevision: 1, Source: "claude_stream", Scope: "account", State: "known", UsedPercent: &used, ObservedAt: time.Now(), FreshUntil: time.Now().Add(time.Minute)}
	runtime := &viewerRuntime{session: consent.Session{Current: consent.Current{OwnerPersonID: snapshot.OwnerPersonID}, View: snapshot}}
	consentID := uuid.NewString()
	a, err := NewCurrentReadAuthority(d.App, runtime, snapshot.Capabilities.QualificationDigest, viewerExport{b, consentID})
	if err != nil {
		t.Fatal(err)
	}
	// Read the fixture's real database clock once. No fixed calendar date is
	// compared with the database-stamped viewer expiry.
	var now time.Time
	seed(func(tx pgx.Tx) error { return tx.QueryRow(t.Context(), `SELECT clock_timestamp()`).Scan(&now) })
	a.now = func() time.Time { return now }
	relay, err := NewRelay(testProjector(t), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err = relay.Register(snapshot); err != nil {
		t.Fatal(err)
	}
	expertRaw, _ := json.Marshal(nativecontract.ExpertFrame{Provider: "claude", EventName: "assistant", FrameJSON: `{"type":"assistant","message":{"content":[{"type":"text","text":"qualified expert output"}]}}`, RedactedPaths: []string{}, OriginalBytes: 128})
	terminalRaw, _ := json.Marshal(nativecontract.TerminalFrame{TerminalEpoch: b.RuntimeEpoch, Sequence: "1", DataBase64: base64.StdEncoding.EncodeToString([]byte("qualified terminal output")), Rows: 24, Cols: 80})
	err = relay.Publish(t.Context(), nativecontract.Publish{RequestID: uuid.NewString(), ExpectedBinding: b, Events: []nativecontract.StreamEvent{
		{Binding: b, Generation: 1, Seq: "1", Sequence: "1", Source: "native", Kind: "expert", Payload: expertRaw, ObservedAt: now, DroppedFrames: "0"},
		{Binding: b, Generation: 1, Seq: "2", Sequence: "2", Source: "native", Kind: "terminal", Payload: terminalRaw, ObservedAt: now, DroppedFrames: "0"},
	}}, func(_ context.Context, deliver func(nativecontract.ValidationContext) error) error {
		return deliver(nativecontract.ValidationContext{Binding: &b, Generation: 1, PrincipalKind: "agent", Private: true, Mode: snapshot.Mode, State: snapshot.State, Capabilities: &snapshot.Capabilities})
	})
	if err != nil {
		t.Fatal(err)
	}
	handler, err := NewHTTPHandler(relay, a)
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/projects/{projectId}/native-sessions/{ownedSessionId}/events", handler.LivePage)
	mux.HandleFunc("GET /api/projects/{projectId}/native-sessions/{ownedSessionId}/stream", handler.Stream)
	viewer := func(p tenant.Principal, view string) string {
		t.Helper()
		id := uuid.NewString()
		raw, _ := json.Marshal(b)
		digest := sha256.Sum256(raw)
		seed(func(tx pgx.Tx) error {
			_, err := tx.Exec(t.Context(), `INSERT INTO native_session_viewers(tenant_id,id,owned_session_id,actor_person_id,view_kind,binding_digest,consent_id,expires_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8)`, tid, id, b.OwnedSessionID, p.ID, view, digest[:], consentID, now.Add(time.Minute))
			return err
		})
		if err := a.Remember(nativecontract.Actor{TenantID: tid, PrincipalID: p.ID}, view, nativecontract.AttachResult{Binding: b, ViewerID: id, ExpiresInMS: 30000}); err != nil {
			t.Fatal(err)
		}
		return id
	}
	request := func(p tenant.Principal, view, id, endpoint string) *http.Request {
		q := url.Values{"view": {view}, "generation": {"1"}, "expected_runtime_epoch": {b.RuntimeEpoch}, "expected_harness_session_id": {b.HarnessSessionID}, "expected_binding_revision": {"1"}}
		r := httptest.NewRequest("GET", "/api/projects/"+project+"/native-sessions/"+b.OwnedSessionID+"/"+endpoint+"?"+q.Encode(), nil).WithContext(tenant.WithPrincipal(t.Context(), p))
		r.Header.Set(nativecontract.ViewerHeader, id)
		return r
	}
	for _, name := range []string{"owner", "workspace-admin", "project-admin", "member", "agent"} {
		for _, view := range []string{"terminal", "expert"} {
			t.Run(name+"/"+view, func(t *testing.T) {
				p := people[name]
				id := viewer(p, view)
				w := &viewerPageRecorder{httptest.NewRecorder()}
				mux.ServeHTTP(w, request(p, view, id, "events"))
				allowed := name == "owner" || name == "workspace-admin"
				if allowed && w.Code != 200 {
					t.Fatalf("authorized view refused: %d %s", w.Code, w.Body.String())
				}
				if allowed && !strings.Contains(w.Body.String(), `"kind":"`+view+`"`) {
					t.Fatalf("authorized view did not receive its actual frame: %s", w.Body.String())
				}
				if allowed {
					r := request(p, view, id, "stream")
					q := r.URL.Query()
					q.Set("after", "1:0")
					r.URL.RawQuery = q.Encode()
					ctx, cancel := context.WithCancel(r.Context())
					defer cancel()
					sse := &streamRecorder{ResponseRecorder: httptest.NewRecorder(), flushed: make(chan struct{}, 8)}
					done := make(chan struct{})
					go func() { mux.ServeHTTP(sse, r.WithContext(ctx)); close(done) }()
					barrier(t, sse.flushed) // Authorized snapshot.
					barrier(t, sse.flushed) // The actual selected native frame.
					cancel()
					barrier(t, done)
					if sse.Code != 200 || !strings.Contains(sse.Body.String(), "event: "+view+"\n") {
						t.Fatalf("authorized SSE withheld its selected frame: %d %s", sse.Code, sse.Body.String())
					}
				}
				if !allowed && (w.Code != 403 || !strings.Contains(w.Body.String(), `"reason":"wrong_principal"`)) {
					t.Fatalf("wrong visibility refusal: %d %s", w.Code, w.Body.String())
				}
				if !allowed {
					w = &viewerPageRecorder{httptest.NewRecorder()}
					mux.ServeHTTP(w, request(p, view, id, "stream"))
					if w.Code != 403 {
						t.Fatalf("SSE bypassed visibility: %d %s", w.Code, w.Body.String())
					}
				}
			})
		}
	}
	// A regular member retains the structured status/event path.
	member := people["member"]
	memberID := viewer(member, "structured")
	w := &viewerPageRecorder{httptest.NewRecorder()}
	mux.ServeHTTP(w, request(member, "structured", memberID, "events"))
	if w.Code != 200 {
		t.Fatalf("member structured read refused: %d %s", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), "qualified terminal") || strings.Contains(w.Body.String(), "qualified expert") || strings.Contains(w.Body.String(), `"kind":"terminal"`) {
		t.Fatal("member structured response leaked a sensitive frame")
	}
	memberReq := request(member, "structured", memberID, "stream")
	memberReq.SetPathValue("projectId", project)
	memberReq.SetPathValue("ownedSessionId", b.OwnedSessionID)
	_, memberGrant, err := a.AuthenticateViewer(memberReq)
	if err != nil {
		t.Fatal(err)
	}
	sub, err := relay.SubscribeView(memberReq.Context(), b, memberGrant)
	if err != nil {
		t.Fatal(err)
	}
	initial := requireEvent(t, sub, "snapshot")
	if err = sub.Close(); err != nil {
		t.Fatal(err)
	}
	var initialView nativecontract.SessionView
	if err = json.Unmarshal(initial.Payload, &initialView); err != nil {
		t.Fatal(err)
	}
	if initialView.TerminalViewerURL != nil || initialView.Quota != nil || initialView.Capabilities.TerminalView || initialView.Capabilities.Expert {
		t.Fatalf("member snapshot leaked private projection: %s", initial.Payload)
	}
	// A project admin can take but cannot export; ordinary members cannot take.
	for _, name := range []string{"owner", "workspace-admin", "project-admin", "member", "agent"} {
		p := people[name]
		ctx := tenant.WithPrincipal(t.Context(), p)
		err := db.InTenant(ctx, d.App, tid, func(tx pgx.Tx) error {
			if err := agentpairing.Lock(ctx, tx); err != nil {
				return err
			}
			return TakeAuthorityTx(ctx, tx, p, people["owner"].ID, project)
		})
		allowed := name == "owner" || name == "workspace-admin" || name == "project-admin"
		if allowed && err != nil {
			t.Fatalf("%s take refused: %v", name, err)
		}
		if !allowed {
			var denied *nativecontract.ValidationError
			if !errors.As(err, &denied) || denied.Reason != nativecontract.ReasonWrongPrincipal {
				t.Fatalf("%s wrong take refusal: %v", name, err)
			}
		}
	}
	owner := people["owner"]
	id := viewer(owner, "terminal")
	req := request(owner, "terminal", id, "events")
	req.SetPathValue("projectId", project)
	req.SetPathValue("ownedSessionId", b.OwnedSessionID)
	_, grant, err := a.AuthenticateViewer(req)
	if err != nil {
		t.Fatal(err)
	}
	assertDenied := func(want nativecontract.Reason) {
		t.Helper()
		called := false
		err := grant.Fence(req.Context(), func(nativecontract.ValidationContext) error { called = true; return nil })
		var denied *nativecontract.ValidationError
		if called || !errors.As(err, &denied) || denied.Reason != want {
			t.Fatalf("stale delivery was not fenced (%t): %v", called, err)
		}
	}
	runtime.refusal = nativecontract.ReasonPermissionRevoked
	assertDenied(nativecontract.ReasonPermissionRevoked)
	runtime.refusal = ""
	runtime.session.View.Capabilities.QualificationDigest = strings.Repeat("b", 64)
	assertDenied(nativecontract.ReasonQualificationMissing)
	runtime.session.View.Capabilities.QualificationDigest = snapshot.Capabilities.QualificationDigest
	// Revoking a role after authentication closes the already-issued grant.
	admin := people["workspace-admin"]
	adminID := viewer(admin, "terminal")
	adminReq := request(admin, "terminal", adminID, "events")
	adminReq.SetPathValue("projectId", project)
	adminReq.SetPathValue("ownedSessionId", b.OwnedSessionID)
	_, adminGrant, err := a.AuthenticateViewer(adminReq)
	if err != nil {
		t.Fatal(err)
	}
	dbtest.BindRole(t, d, tid, admin.ID, "member")
	called := false
	err = adminGrant.Fence(adminReq.Context(), func(nativecontract.ValidationContext) error { called = true; return nil })
	var denied *nativecontract.ValidationError
	if called || !errors.As(err, &denied) || denied.Reason != nativecontract.ReasonWrongPrincipal {
		t.Fatalf("revoked workspace admin retained its stream: %v", err)
	}
	keyOwner := owner
	keyOwner.AuthKeyID = uuid.NewString()
	bad := httptest.NewRecorder()
	mux.ServeHTTP(bad, request(keyOwner, "terminal", id, "events"))
	if bad.Code != 403 || !strings.Contains(bad.Body.String(), `"reason":"wrong_principal"`) {
		t.Fatalf("person API key exported: %d %s", bad.Code, bad.Body.String())
	}
	// Expiry is injected; the test never sleeps or relies on wall-clock speed.
	now = now.Add(31 * time.Second)
	assertDenied(nativecontract.ReasonControllerExpired)
}
