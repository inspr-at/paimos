// SPDX-License-Identifier: AGPL-3.0-only
package modelregistry

import (
	"bufio"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/modelprefs"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/inspr-at/paimos/internal/workorders"
	"github.com/jackc/pgx/v5"
)

// Read signals the exact stalled-input boundary; Close releases it, including
// on assertion failure. No sleep is needed to establish the interleaving.
type stalledEndpointBody struct {
	entered chan struct{}
	release chan struct{}
	read    sync.Once
	close   sync.Once
	reader  io.Reader
}

type observedEndpointBody struct {
	io.ReadCloser
	entered chan struct{}
	once    sync.Once
}

func (b *observedEndpointBody) Read(p []byte) (int, error) {
	b.once.Do(func() { close(b.entered) })
	return b.ReadCloser.Read(p)
}

func TestEndpointCancelledHTTPBodyInterruptsTransport(t *testing.T) {
	p := tenant.Principal{ID: "00000000-0000-0000-0000-000000000001", TenantID: "00000000-0000-0000-0000-000000000002", Kind: tenant.Person}
	ctx, cancel := context.WithCancel(tenant.WithPrincipal(t.Context(), p))
	defer cancel()
	entered := make(chan struct{})
	prepared := make(chan struct{}, 1)
	handler := workorders.EndpointPrepared(nil, "work_orders.write", false, 200, func(r *http.Request, _ tenant.Principal) (*http.Request, error) {
		prepared <- struct{}{}
		return nil, workorders.Fail(400, "preparation should not run")
	}, nil)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.Body = &observedEndpointBody{ReadCloser: r.Body, entered: entered}
		handler(w, r.WithContext(ctx))
	}))
	defer server.Close()
	conn, err := net.DialTimeout("tcp", server.Listener.Addr().String(), 10*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close() // Unblock server cleanup even if a regression stalls input.
	if err := conn.SetDeadline(time.Now().Add(10 * time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(conn, "POST /endpoint HTTP/1.1\r\nHost: localhost\r\nContent-Length: 100\r\nConnection: close\r\n\r\n{"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-entered:
	case <-time.After(10 * time.Second):
		t.Fatal("transport read not entered")
	}
	cancel()
	response, err := http.ReadResponse(bufio.NewReader(conn), nil)
	if err != nil {
		t.Fatalf("cancelled transport did not return a response: %v", err)
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(response.Body)
	if err != nil || response.StatusCode != http.StatusRequestTimeout || !strings.Contains(string(raw), "request body read timed out") {
		t.Fatalf("wrong transport cancellation result: status=%d error=%v body=%s", response.StatusCode, err, raw)
	}
	select {
	case <-prepared:
		t.Fatal("stalled transport reached preparation")
	default:
	}
}

type deadlineEndpointRecorder struct {
	*httptest.ResponseRecorder
	deadlines []time.Time
}

func (w *deadlineEndpointRecorder) SetReadDeadline(deadline time.Time) error {
	w.deadlines = append(w.deadlines, deadline)
	return nil
}

func (b *stalledEndpointBody) Read(p []byte) (int, error) {
	b.read.Do(func() { close(b.entered) })
	<-b.release
	return b.reader.Read(p)
}

func (b *stalledEndpointBody) Close() error {
	b.close.Do(func() { close(b.release) })
	return nil
}

func TestEndpointStalledBodyHoldsNoTenantFence(t *testing.T) {
	reset(t)
	p := makePrincipal(t, "stalled-body", "person", "Owner", []string{"admin"})
	for _, prepared := range []bool{false, true} {
		t.Run(map[bool]string{false: "ordinary", true: "prepared"}[prepared], func(t *testing.T) {
			body := &stalledEndpointBody{entered: make(chan struct{}), release: make(chan struct{}), reader: strings.NewReader(`{"value":"complete"}`)}
			r := httptest.NewRequest(http.MethodPost, "/endpoint", nil)
			r.Body = body
			r = r.WithContext(tenant.WithPrincipal(r.Context(), p))
			rec := httptest.NewRecorder()
			fn := func(r *http.Request, _ pgx.Tx, _ tenant.Principal) (any, error) {
				var in struct {
					Value string `json:"value"`
				}
				if err := workorders.Decode(r, &in); err != nil {
					return nil, err
				}
				return in, nil
			}
			handler := workorders.Endpoint(appPool, "work_orders.write", false, 200, fn)
			if prepared {
				handler = workorders.EndpointPrepared(appPool, "work_orders.write", false, 200, func(r *http.Request, _ tenant.Principal) (*http.Request, error) {
					return r, nil
				}, fn)
			}
			done := make(chan struct{})
			go func() { defer close(done); handler(rec, r) }()
			defer func() {
				_ = body.Close()
				select {
				case <-done:
				case <-time.After(10 * time.Second):
					t.Error("endpoint did not finish after body release")
				}
			}()
			select {
			case <-body.entered:
			case <-time.After(10 * time.Second):
				t.Fatal("endpoint never reached body read")
			}
			tx, err := adminPool.Begin(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback(context.Background())
			if _, err := tx.Exec(t.Context(), `SELECT id FROM tenants WHERE id=$1 FOR NO KEY UPDATE NOWAIT`, p.TenantID); err != nil {
				t.Fatalf("stalled input holds tenant write fence: %v", err)
			}
			if err := tx.Rollback(t.Context()); err != nil {
				t.Fatal(err)
			}
			_ = body.Close()
			select {
			case <-done:
			case <-time.After(10 * time.Second):
				t.Fatal("endpoint did not complete")
			}
			if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"value":"complete"`) {
				t.Fatalf("body was not replayed to handler: %d %s", rec.Code, rec.Body.String())
			}
		})
	}
}

func TestProjectQualifiedResolvePreservesLegacyLadder(t *testing.T) {
	var p tenant.Principal
	var project string
	prefsFixture(t, func(tx pgx.Tx, who tenant.Principal) error {
		p = who
		if err := tx.QueryRow(t.Context(), `INSERT INTO nodes(tenant_id,kind_id,key,title) SELECT $1,id,'LEGACY-1','Legacy project' FROM node_kinds WHERE slug='project' RETURNING id::text`, p.TenantID).Scan(&project); err != nil {
			return err
		}
		eu := "eu"
		_, err := modelprefs.SaveScope(t.Context(), tx, p, modelprefs.Scope{Level: "project", ProjectID: &project, Residency: &eu})
		return err
	})
	path := "/api/models/resolve?role=review-gate&author_family=openai&harness=claude"
	legacy := decode[Resolution](t, &p, http.MethodGet, path, "", 200)
	if legacy.Profile == nil || legacy.CommandTemplate == "" || legacy.OwnerRequired {
		t.Fatal("fixture must have a runnable legacy review ladder")
	}
	qualified := decode[struct {
		Resolution
		Trace PreferenceTrace `json:"trace"`
	}](t, &p, http.MethodGet, path+"&project_id="+project, "", 200)
	if !reflect.DeepEqual(legacy, qualified.Resolution) {
		t.Fatalf("project changed legacy resolution: before=%+v after=%+v", legacy, qualified.Resolution)
	}
	if qualified.Trace.PersonID == nil || *qualified.Trace.PersonID != p.ID || qualified.Trace.Residency.Value != "eu" {
		t.Fatalf("legacy project residency evidence lost: %+v", qualified.Trace)
	}
	placement := decode[struct {
		WorkResolution
		Preference PreferenceDecision `json:"preference"`
	}](t, &p, http.MethodGet, path+"&project_id="+project+"&mode=placement", "", 200)
	if placement.Preference.Role != "review-gate" || placement.Trace.Residency.Value != "eu" || placement.CommandTemplate != "" || !placement.OwnerRequired {
		t.Fatalf("explicit placement must enforce managed-review account requirements: %+v", placement)
	}
}

func TestEndpointBodyDeadlineBeforePreparation(t *testing.T) {
	reset(t)
	p := makePrincipal(t, "body-deadline", "person", "Owner", []string{"admin"})
	body := &stalledEndpointBody{entered: make(chan struct{}), release: make(chan struct{}), reader: strings.NewReader(`{}`)}
	deadline := time.Now().Add(15 * time.Second)
	ctx, cancel := context.WithDeadline(tenant.WithPrincipal(t.Context(), p), deadline)
	defer cancel()
	r := httptest.NewRequest(http.MethodPost, "/endpoint", nil).WithContext(ctx)
	r.Body = body
	rec := &deadlineEndpointRecorder{ResponseRecorder: httptest.NewRecorder()}
	prepared := make(chan struct{}, 1)
	handler := workorders.EndpointPrepared(appPool, "work_orders.write", false, 200, func(r *http.Request, _ tenant.Principal) (*http.Request, error) {
		prepared <- struct{}{}
		return r, nil
	}, func(r *http.Request, _ pgx.Tx, _ tenant.Principal) (any, error) {
		var in map[string]any
		return in, workorders.Decode(r, &in)
	})
	done := make(chan struct{})
	go func() { defer close(done); handler(rec, r) }()
	defer func() {
		_ = body.Close()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Error("endpoint did not finish after body release")
		}
	}()
	select {
	case <-body.entered:
	case <-time.After(10 * time.Second):
		t.Fatal("body read not entered")
	}
	cancel() // Deterministically expire the input phase; no wall-clock threshold.
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("cancelled input did not finish")
	}
	if rec.Code != http.StatusRequestTimeout {
		t.Fatalf("wrong body timeout status: %d %s", rec.Code, rec.Body.String())
	}
	// A failed read keeps its transport deadline (AEON-652): the interrupt is
	// the last deadline call, never followed by a clear.
	if len(rec.deadlines) != 2 || !rec.deadlines[0].Equal(deadline) || !rec.deadlines[1].Equal(time.Unix(1, 0)) {
		t.Fatalf("transport read deadline not installed and interrupted, or cleared after the failed read: %v", rec.deadlines)
	}
	select {
	case <-prepared:
		t.Fatal("preparation ran before body arrived")
	default:
	}
	if !errors.Is(ctx.Err(), context.Canceled) {
		t.Fatal("test did not cancel input")
	}
}

func TestExplicitPlacementModeSelectsManagedReview(t *testing.T) {
	reset(t)
	p := makePrincipal(t, "explicit-placement", "person", "Owner", []string{"admin"})
	got := decode[struct {
		WorkResolution
		Preference PreferenceDecision `json:"preference"`
	}](t, &p, http.MethodGet, "/api/models/resolve?role=review-gate&author_family=openai&harness=claude&mode=placement", "", http.StatusOK)
	if got.Preference.Role != "review-gate" || got.Preference.Kind.Slug != "review:openai" || got.CommandTemplate != "" || !got.OwnerRequired || got.Profile != nil {
		t.Fatalf("explicit placement bypassed managed-review requirements: %+v", got)
	}
}

func TestEndpointOversizedBodyBeforePreparation(t *testing.T) {
	p := tenant.Principal{ID: "00000000-0000-0000-0000-000000000001", TenantID: "00000000-0000-0000-0000-000000000002", Kind: tenant.Person}
	r := httptest.NewRequest(http.MethodPost, "/endpoint", strings.NewReader(strings.Repeat(" ", (1<<20)+1)))
	r = r.WithContext(tenant.WithPrincipal(r.Context(), p))
	rec := httptest.NewRecorder()
	prepared := false
	workorders.EndpointPrepared(nil, "work_orders.write", false, 200, func(r *http.Request, _ tenant.Principal) (*http.Request, error) {
		prepared = true
		return nil, workorders.Fail(400, "preparation should not run")
	}, nil)(rec, r)
	if prepared || rec.Code != http.StatusRequestEntityTooLarge || !strings.Contains(rec.Body.String(), "request body too large") {
		t.Fatalf("oversized input reached preparation: prepared=%v status=%d body=%s", prepared, rec.Code, rec.Body.String())
	}
}

func TestInvalidResolutionModeDoesNotPrepareCatalog(t *testing.T) {
	reset(t)
	p := makePrincipal(t, "invalid-mode", "person", "Owner", []string{"admin"})
	before := catalogSetupState(t, p)
	for _, mode := range []string{"", "unknown"} {
		status, body := call(t, &p, http.MethodGet, "/api/models/resolve?role=build&mode="+mode, "")
		if status != http.StatusBadRequest || !strings.Contains(string(body), "invalid resolution mode") {
			t.Fatalf("wrong mode refusal: %d %s", status, body)
		}
		if after := catalogSetupState(t, p); after != before {
			t.Fatal("invalid mode persisted catalog setup")
		}
	}
}
