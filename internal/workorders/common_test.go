// SPDX-License-Identifier: AGPL-3.0-only

package workorders

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/events"
	"github.com/inspr-at/paimos/internal/httpapi"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

type bodyDeadlineRecorder struct {
	*httptest.ResponseRecorder
	deadlines []time.Time
}

// Risk: work-order creation silently drops the fix binding, or a direct store
// caller bypasses exact branch/repository and active-builder exclusion.
func TestBuildAdmissionContinuationStore(t *testing.T) {
	d := dbtest.Open(t)
	p := tenant.Principal{ID: "11111111-1111-4111-8111-111111111111", Kind: tenant.Person}
	if err := d.Admin.QueryRow(t.Context(), `INSERT INTO tenants(slug,name) VALUES('continuation-store','Continuation store') RETURNING id::text`).Scan(&p.TenantID); err != nil {
		t.Fatal(err)
	}
	if err := db.InTenant(dbtest.Seed(t.Context()), d.App, p.TenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `INSERT INTO principals(tenant_id,id,name,kind) VALUES($1,$2,'Store caller','person')`, p.TenantID, p.ID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	dbtest.BindRole(t, d, p.TenantID, p.ID, "admin")
	ctx := tenant.WithPrincipal(t.Context(), p)
	var project, ticket, key string
	if err := db.InTenant(ctx, d.App, p.TenantID, func(tx pgx.Tx) error {
		if err := db.LockWorkTreeTx(ctx, tx); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `INSERT INTO nodes(tenant_id,kind_id,key,title,fields) SELECT $1,id,aeon_next_node_key($1,short_prefix),'Store project','{"project_key":"STO"}' FROM node_kinds WHERE slug='project' RETURNING id::text`, p.TenantID).Scan(&project); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `INSERT INTO nodes(tenant_id,kind_id,key,title,parent_id) SELECT $1,id,aeon_next_node_key($1,short_prefix),'Store ticket',$2 FROM node_kinds WHERE slug='work' RETURNING id::text,key`, p.TenantID, project).Scan(&ticket, &key); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO delivery_metric_sources(tenant_id,project_id,repository,ci_workflow,nightly_workflow,updated_at) VALUES($1,$2,'owner/linked','CI','Nightly',clock_timestamp())`, p.TenantID, project); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `INSERT INTO delivery_metric_pulls(tenant_id,repository,pull_request,head_branch,opened_at,source,recorded_at) VALUES($1,'owner/linked',42,$2,clock_timestamp(),'webhook',clock_timestamp())`, p.TenantID, "work/"+strings.ToLower(key)+"-fix")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	branch := "work/" + strings.ToLower(key) + "-fix"
	for _, scenario := range []string{"same-branch", "other-branch", "no-branch", "unknown-repository", "active-builder"} {
		t.Run(scenario, func(t *testing.T) {
			if err := db.InTenant(ctx, d.App, p.TenantID, func(tx pgx.Tx) error {
				if err := db.LockWorkTreeTx(ctx, tx); err != nil {
					return err
				}
				candidate := &BuildCandidate{Repository: "owner/linked", Branch: branch}
				if scenario == "other-branch" {
					candidate.Branch += "-other"
				} else if scenario == "unknown-repository" {
					candidate.Repository = "owner/unknown"
				} else if scenario == "no-branch" {
					candidate = nil
				}
				order, changes, err := CreateDeferred(ctx, tx, p, CreateInput{Title: "Fix", Parent: &ticket, Criteria: []string{"Continue"}, BuildDelivery: candidate})
				if err != nil {
					return err
				}
				admission, err := OrderBuildAdmissionTx(ctx, tx, order.NodeID)
				if err != nil || !sameStoreCandidate(admission.Candidate, candidate) || !sameStoreCandidate(order.BuildDelivery, candidate) {
					t.Fatalf("create/load binding: %+v %+v %v", order, admission, err)
				}
				if scenario == "active-builder" {
					if _, err := tx.Exec(ctx, `INSERT INTO harness_sessions(tenant_id,project_id,agent_principal_id,ticket_node_id,harness,host,management,role,work_shape,capabilities,ref_digest,lease_digest,branch) VALUES($1,$2,$3,$4,'codex','store','unmanaged','worker','ship','{}','store-ref','store-lease',$5)`, p.TenantID, project, p.ID, ticket, branch); err != nil {
						return err
					}
				}
				err = RequireBuildAdmissionTx(ctx, tx, p, order.NodeID, "", nil, &admission)
				if scenario != "same-branch" {
					var conflict *BuildConflict
					want := "built_unmerged"
					if scenario == "active-builder" {
						want = "active_builder"
					}
					if !errors.As(err, &conflict) || conflict.Reason != want || admission.Continuation != nil {
						t.Fatalf("store %s: admission %+v, error %v", scenario, admission, err)
					}
					return nil
				}
				if err != nil || admission.Continuation == nil {
					t.Fatalf("same-branch admission: %+v %v", admission, err)
				}
				// All order writes/locks precede the audit counter.
				for _, change := range changes {
					if _, err := events.Append(ctx, tx, p, change); err != nil {
						return err
					}
				}
				return admission.Record(ctx, tx, p, "", "store-run")
			}); err != nil {
				t.Fatal(err)
			}
		})
	}
	if err := db.InTenant(ctx, d.App, p.TenantID, func(tx pgx.Tx) error {
		var count int
		err := tx.QueryRow(ctx, `SELECT count(*) FROM events WHERE node_id=$1 AND type='delivery.continued' AND after->>'message'=$2`, ticket, "continues delivery PR #42 on "+branch).Scan(&count)
		if err == nil && count != 1 {
			t.Errorf("store continuation audit count = %d", count)
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
}

func sameStoreCandidate(a, b *BuildCandidate) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

// Risk: a supplied binding amplifies decoding or introduces multiline audit
// content. Creation rejects invalid bindings before acquiring store locks.
func TestBuildDeliveryBindingBounds(t *testing.T) {
	for _, candidate := range []*BuildCandidate{
		{Repository: "", Branch: "work/ticket"},
		{Repository: "owner/repo", Branch: ""},
		{Repository: "owner/repo", Branch: "work/"},
		{Repository: "owner/repo", Branch: "main"},
		{Repository: "owner/repo\n", Branch: "work/ticket"},
		{Repository: "owner/repo", Branch: "work/ticket\u2028injected"},
		{Repository: strings.Repeat("a", 256), Branch: "work/ticket"},
		{Repository: "owner/repo", Branch: "work/" + strings.Repeat("a", 196)},
	} {
		raw, err := json.Marshal(map[string]any{"title": "Fix", "criteria": []string{"Continue"}, "build_delivery": candidate})
		if err != nil {
			t.Fatal(err)
		}
		var in CreateInput
		if err := json.Unmarshal(raw, &in); err != nil {
			t.Fatal(err)
		}
		_, _, err = CreateDeferred(t.Context(), nil, tenant.Principal{}, in)
		var failure interface{ HTTPStatus() int }
		if !errors.As(err, &failure) || failure.HTTPStatus() != 400 {
			t.Fatalf("invalid binding reached store: %v", err)
		}
	}
}

func (w *bodyDeadlineRecorder) SetReadDeadline(deadline time.Time) error {
	w.deadlines = append(w.deadlines, deadline)
	return nil
}

// Risk: endpoint buffering shortens the 30-second upload budget or extends an
// earlier caller deadline. Clock samples bound the installed deadline; no
// elapsed-time performance threshold or wait establishes the behavior.
func TestBufferEndpointBodyDeadlineBudget(t *testing.T) {
	for _, earlier := range []bool{false, true} {
		name := "30-second endpoint budget"
		if earlier {
			name = "earlier caller deadline"
		}
		t.Run(name, func(t *testing.T) {
			ctx := t.Context()
			callerDeadline := time.Now().Add(15 * time.Second)
			if earlier {
				var cancel context.CancelFunc
				ctx, cancel = context.WithDeadline(ctx, callerDeadline)
				defer cancel()
			}
			r := httptest.NewRequest(http.MethodPost, "/api/work-orders", strings.NewReader(`{"reason":"process_exited"}`)).WithContext(ctx)
			w := &bodyDeadlineRecorder{ResponseRecorder: httptest.NewRecorder()}
			before := time.Now()
			err := BufferEndpointBody(w, r)
			after := time.Now()
			if err != nil {
				t.Fatal(err)
			}
			if len(w.deadlines) != 2 || w.deadlines[0].IsZero() || !w.deadlines[1].IsZero() {
				t.Fatalf("successful read must install then clear deadline: %v", w.deadlines)
			}
			got := w.deadlines[0]
			if earlier {
				if !got.Equal(callerDeadline) {
					t.Fatalf("caller deadline changed: got %s, want %s", got, callerDeadline)
				}
			} else if got.Before(before.Add(30*time.Second)) || got.After(after.Add(30*time.Second)) {
				t.Fatalf("body deadline %s is outside the 30-second budget [%s, %s]", got, before.Add(30*time.Second), after.Add(30*time.Second))
			}
			var body struct {
				Reason string `json:"reason"`
			}
			if err := Decode(r, &body); err != nil || body.Reason != "process_exited" {
				t.Fatalf("buffered body changed: %+v, %v", body, err)
			}
		})
	}
}

func TestBufferBodyBoundsAndDecode(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		tooLarge   bool
		invalid    bool
	}{
		{name: "valid", body: `{"reason":"process_exited"}`},
		{name: "at limit", body: `{}` + strings.Repeat(" ", (1<<20)-2)},
		{name: "over limit", body: `{}` + strings.Repeat(" ", (1<<20)-1), tooLarge: true},
		{name: "unknown field", body: `{"unknown":true}`, invalid: true},
		{name: "trailing JSON", body: `{} {}`, invalid: true},
		{name: "trailing garbage", body: `{} x`, invalid: true},
		{name: "empty", invalid: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest("POST", "/", strings.NewReader(tc.body))
			w := &bodyDeadlineRecorder{ResponseRecorder: httptest.NewRecorder()}
			err := BufferBody(w, r)
			if tc.tooLarge {
				var failure *Error
				if !errors.As(err, &failure) || failure.Status != 400 || failure.Message != "invalid JSON" {
					t.Fatalf("oversized body: %v", err)
				}
				if len(w.deadlines) != 1 || w.deadlines[0].IsZero() {
					t.Fatalf("failed read lost its drain deadline: %v", w.deadlines)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if tc.body != "" && (len(w.deadlines) != 2 || w.deadlines[0].IsZero() || !w.deadlines[1].IsZero()) {
				t.Fatalf("successful read must set then clear deadline: %v", w.deadlines)
			}
			var body struct {
				Reason string `json:"reason"`
			}
			if err := Decode(r, &body); (err != nil) != tc.invalid {
				t.Fatalf("buffered decode=%v, want invalid=%t", err, tc.invalid)
			}
			if tc.name == "valid" && body.Reason != "process_exited" {
				t.Fatalf("body changed: %+v", body)
			}
		})
	}
}

type expiredBodyDeadlineWriter struct {
	http.ResponseWriter
	requested chan<- time.Time
}

func (w *expiredBodyDeadlineWriter) SetReadDeadline(deadline time.Time) error {
	// Inject expiry at the actual network connection, avoiding a sleep or
	// wall-clock performance assertion. An absent deadline cannot unblock Read.
	w.requested <- deadline
	return http.NewResponseController(w.ResponseWriter).SetReadDeadline(time.Unix(1, 0))
}

func TestEndpointBodyNetworkDeadline(t *testing.T) {
	mux := http.NewServeMux()
	// A body timeout must return before any database connection is needed.
	mux.HandleFunc("POST /api/work-orders", Endpoint(nil, "work_orders.write", false, 201, nil))
	handler := (&httpapi.Server{Mux: mux}).Handler()
	p := tenant.Principal{ID: "11111111-1111-4111-8111-111111111111", TenantID: "22222222-2222-4222-8222-222222222222", Kind: tenant.Person}
	assertEndpointBodyNetworkDeadline(t, handler, p, "POST /api/work-orders")
}

func TestEndpointPositionedBodyNetworkDeadline(t *testing.T) {
	d := dbtest.Open(t)
	p := tenant.Principal{ID: "11111111-1111-4111-8111-111111111111", Kind: tenant.Person}
	if err := d.Admin.QueryRow(t.Context(), `INSERT INTO tenants(slug,name) VALUES('body-deadline','Body deadline') RETURNING id::text`).Scan(&p.TenantID); err != nil {
		t.Fatal(err)
	}
	for _, pattern := range []string{"GET /api/runs", "GET /api/harness-sessions"} {
		t.Run(pattern, func(t *testing.T) {
			mux := http.NewServeMux()
			// The watermark uses the real database, but the endpoint must time
			// out before opening its own transaction or calling its handler.
			mux.HandleFunc(pattern, Endpoint(nil, "", false, http.StatusOK, nil))
			handler := (&httpapi.Server{
				Mux: mux, Middleware: []func(http.Handler) http.Handler{events.PositionMiddleware(d.App)},
			}).Handler()
			assertEndpointBodyNetworkDeadline(t, handler, p, pattern)
		})
	}
}

func assertEndpointBodyNetworkDeadline(t *testing.T, handler http.Handler, p tenant.Principal, pattern string) {
	t.Helper()
	requested := make(chan time.Time, 2)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// The production middleware wraps this writer; its Unwrap must let the
		// response controller reach the connection deadline implementation.
		handler.ServeHTTP(&expiredBodyDeadlineWriter{w, requested}, r.WithContext(tenant.WithPrincipal(r.Context(), p)))
	}))
	defer server.Close()
	conn, err := net.DialTimeout("tcp", server.Listener.Addr().String(), 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if err := conn.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatal(err)
	}
	// Announce a body but send no bytes. Only the server's read deadline ends
	// its read; the client deadline is merely a guard against a hung test.
	if _, err := io.WriteString(conn, pattern+" HTTP/1.1\r\nHost: test\r\nContent-Length: 1\r\nConnection: close\r\n\r\n"); err != nil {
		t.Fatal(err)
	}
	response, err := http.ReadResponse(bufio.NewReader(conn), nil)
	if err != nil {
		t.Fatalf("stalled body did not receive a timeout response: %v", err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusRequestTimeout || !strings.Contains(string(body), "request body read timed out") {
		t.Fatalf("timeout response=%d %s", response.StatusCode, body)
	}
	select {
	case deadline := <-requested:
		if deadline.IsZero() {
			t.Fatal("body read did not install a deadline")
		}
	default:
		t.Fatal("body read did not reach the network deadline controller")
	}
	select {
	case <-requested:
		t.Fatal("failed read cleared its drain deadline")
	default:
	}
}

// stalledBody signals its first Read and blocks until Close releases it, so
// the interleaving is established by barriers rather than sleeps.
type stalledBody struct {
	entered, release chan struct{}
	read, close      sync.Once
}

func (b *stalledBody) Read([]byte) (int, error) {
	b.read.Do(func() { close(b.entered) })
	<-b.release
	return 0, io.EOF
}

func (b *stalledBody) Close() error {
	b.close.Do(func() { close(b.release) })
	return nil
}

// A cancelled request with a stalled body must still be interrupted. With a
// transport deadline the failed read keeps it: the expired interrupt deadline
// is the last deadline call, never followed by a clear that would let net/http
// drain the unread body without a bound. Without one, Close alone releases it.
func TestBufferEndpointBodyCancellationKeepsInterruptDeadline(t *testing.T) {
	for _, tc := range []struct {
		name      string
		transport bool
	}{{"transport deadline", true}, {"no transport deadline", false}} {
		t.Run(tc.name, func(t *testing.T) {
			body := &stalledBody{entered: make(chan struct{}), release: make(chan struct{})}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			deadline := time.Now().Add(15 * time.Second)
			if tc.transport {
				var stop context.CancelFunc
				ctx, stop = context.WithDeadline(ctx, deadline)
				defer stop()
			}
			r := httptest.NewRequest("POST", "/", nil).WithContext(ctx)
			r.Body = body
			recorder := httptest.NewRecorder()
			var w http.ResponseWriter = recorder
			var transportWriter *bodyDeadlineRecorder
			if tc.transport {
				transportWriter = &bodyDeadlineRecorder{ResponseRecorder: recorder}
				w = transportWriter
			} else if err := http.NewResponseController(w).SetReadDeadline(deadline); !errors.Is(err, http.ErrNotSupported) {
				t.Fatalf("fixture unexpectedly supports transport deadlines: %v", err)
			}
			result := make(chan error, 1)
			go func() { result <- BufferEndpointBody(w, r) }()
			defer body.Close()
			select {
			case <-body.entered:
			case err := <-result:
				t.Fatalf("buffer returned before reading the body: %v", err)
			case <-time.After(10 * time.Second):
				t.Fatal("buffer never started reading the body")
			}
			cancel()
			var err error
			select {
			case err = <-result:
			case <-time.After(10 * time.Second):
				t.Fatal("cancellation did not interrupt the stalled body")
			}
			var failure *Error
			if !errors.As(err, &failure) || failure.Status != http.StatusRequestTimeout || failure.Message != "request body read timed out" {
				t.Fatalf("cancelled read result: %v", err)
			}
			if recorder.Header().Get("Connection") != "close" {
				t.Fatal("cancelled body left the connection reusable")
			}
			select {
			case <-body.release:
			default:
				t.Fatal("cancelled body reader was not closed")
			}
			if !tc.transport {
				return
			}
			if len(transportWriter.deadlines) != 2 || !transportWriter.deadlines[0].Equal(deadline) || !transportWriter.deadlines[1].Equal(time.Unix(1, 0)) {
				t.Fatalf("expected the installed deadline, one expired interrupt and no clear: %v", transportWriter.deadlines)
			}
		})
	}
}
