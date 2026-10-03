// SPDX-License-Identifier: AGPL-3.0-only

package portal

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

func TestDisplayedProductBindingRejectsDefaultSwitch(t *testing.T) {
	f := productFixture(t)
	tid := makeTenant(t, f.d, "binding", "Binding")
	admin := makePerson(t, f.d, tid, "Admin", "admin")
	a := insertNode(t, f.d, tid, "PPR-1", "portal_product", "First", "first", "published", "", "{}")
	b := insertNode(t, f.d, tid, "PPR-2", "portal_product", "Second", "second", "published", "", "{}")
	configureFixtureProduct(t, f.d, tid, a, "first", "legacy", true)
	configureFixtureProduct(t, f.d, tid, b, "second", "legacy", true)
	insertNode(t, f.d, tid, "PWS-1", "portal_wish", "Wish", "first wish", "published", a, "{}")
	insertNode(t, f.d, tid, "PWS-2", "portal_wish", "Other", "second wish", "published", b, "{}")
	setPortal(t, f.d, tid, true)
	base, ip := "/api/public/portal/binding", "203.0.113.171:1"
	catalog := f.do("GET", base, "", ip, nil, nil, nil)
	if catalog.Code != 200 || !strings.Contains(catalog.Body.String(), `"title":"First"`) {
		t.Fatalf("catalog %d %s", catalog.Code, catalog.Body)
	}
	// Obtain the expected binding from the existing settings API, so the
	// baseline still reaches the default-switch behavior if its header is absent.
	settings := productSettings{}
	readSettings := f.do("GET", "/api/portal/products/"+a+"/settings", "", ip, &admin, nil, nil)
	if err := json.NewDecoder(readSettings.Body).Decode(&settings); err != nil {
		t.Fatal(err)
	}
	binding := fmt.Sprintf("%s:%d", a, settings.Revision)
	headers := map[string]string{"X-Portal-Binding": binding}
	policy := f.do("GET", base+"/participation", "", ip, nil, nil, headers)
	if policy.Code != 200 {
		t.Fatalf("initial policy %d %s", policy.Code, policy.Body)
	}
	if catalog.Header().Get("X-Portal-Binding") != binding || policy.Header().Get("X-Portal-Binding") != binding {
		t.Error("catalog and participation must expose the same product/settings binding")
	}
	// The switch completes before any subsequent request, without timing races.
	switchDefault := f.do("PUT", "/api/portal/products/"+b+"/settings", `{"revision":1,"slug":"second","published":true,"is_default":true,"participation_policy":"legacy"}`, ip, &admin, nil, nil)
	if switchDefault.Code != 200 {
		t.Fatalf("switch %d %s", switchDefault.Code, switchDefault.Body)
	}
	beforeWishes, beforeVotes, beforeCorrections := wishCount(t, f.d, tid), voteCount(t, f.d, tid), correctionCount(t, f.d, tid)
	var beforeEvents int
	if err := db.InTenant(dbtest.Seed(t.Context()), f.d.App, tid, func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), `SELECT count(*) FROM events`).Scan(&beforeEvents)
	}); err != nil {
		t.Fatal(err)
	}
	for _, entry := range []struct{ method, suffix, body string }{
		{"GET", "/participation", ""},
		{"POST", "/wishes", `{"title":"Draft for First","summary":"Must never arrive in Second"}`},
		{"POST", "/corrections", `{"competitor":"Northwind","aspect":"One","statement":"A correction for First","source_url":"https://example.com"}`},
		{"POST", "/wishes/PWS-2/votes", "{}"},
		{"POST", "/products/first/wishes", `{"title":"Stale settings","summary":"The settings revision changed"}`},
	} {
		rec := f.do(entry.method, base+entry.suffix, entry.body, ip, nil, nil, headers)
		if rec.Code != 409 || !strings.Contains(rec.Body.String(), "product changed") || rec.Header().Get("Set-Cookie") != "" {
			t.Errorf("stale %s: %d %s", entry.suffix, rec.Code, rec.Body)
		}
	}
	if wishCount(t, f.d, tid) != beforeWishes || voteCount(t, f.d, tid) != beforeVotes || correctionCount(t, f.d, tid) != beforeCorrections {
		t.Error("stale product binding wrote participation")
	}
	var afterEvents int
	if err := db.InTenant(dbtest.Seed(t.Context()), f.d.App, tid, func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), `SELECT count(*) FROM events`).Scan(&afterEvents)
	}); err != nil {
		t.Fatal(err)
	}
	if afterEvents != beforeEvents {
		t.Error("stale binding appended an event")
	}
	// Reloading gets the new binding and permits a submission to exactly B.
	fresh := f.do("GET", base, "", ip, nil, nil, nil)
	rec := f.do("POST", base+"/wishes", `{"title":"Fresh draft","summary":"Now explicitly for Second"}`, "203.0.113.172:1", nil, nil, map[string]string{"X-Portal-Binding": fresh.Header().Get("X-Portal-Binding")})
	if rec.Code != 201 {
		t.Fatalf("fresh write %d %s", rec.Code, rec.Body)
	}
	if err := db.InTenant(dbtest.Seed(t.Context()), f.d.App, tid, func(tx pgx.Tx) error {
		var parent string
		if err := tx.QueryRow(t.Context(), `SELECT parent_id::text FROM nodes WHERE title='Fresh draft'`).Scan(&parent); err != nil {
			return err
		}
		if parent != b {
			return errors.New("fresh wish targeted the wrong product")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

// These readers/writers let the test decide exactly when reading starts and
// when the socket deadline expires. Wall time is only a hang guard.
type controlledPortalBody struct {
	entered, release, expired chan struct{}
	once                      sync.Once
	reader                    io.Reader
}

func (b *controlledPortalBody) Read(p []byte) (int, error) {
	b.once.Do(func() { close(b.entered) })
	select {
	case <-b.release:
		return b.reader.Read(p)
	case <-b.expired:
		return 0, os.ErrDeadlineExceeded
	}
}
func (b *controlledPortalBody) Close() error { return nil }

type controlledDeadlineWriter struct {
	*httptest.ResponseRecorder
	deadlines chan time.Time
}

func (w *controlledDeadlineWriter) SetReadDeadline(at time.Time) error { w.deadlines <- at; return nil }

func TestPortalBodyReadPrecedesTenantAndTreeLocks(t *testing.T) {
	f := productFixture(t)
	tid := makeTenant(t, f.d, "body-locks", "Body locks")
	admin := makePerson(t, f.d, tid, "Admin", "admin")
	p := insertNode(t, f.d, tid, "PPR-1", "portal_product", "Product", "summary", "published", "", "{}")
	wish := insertNode(t, f.d, tid, "PWS-1", "portal_wish", "Wish", "summary", "pending", p, "{}")
	for _, entry := range []struct{ method, path string }{
		{"PUT", "/api/portal/products/" + p + "/settings"},
		{"PATCH", "/api/portal/products/" + p},
		{"PATCH", "/api/portal/features/" + p},
		{"POST", "/api/portal/wishes/" + wish + "/publish"},
		{"POST", "/api/portal/competitors"},
		{"PATCH", "/api/portal/competitors/" + p},
		{"POST", "/api/portal/aspects"},
		{"PATCH", "/api/portal/aspects/" + p},
		{"PUT", "/api/portal/cells"},
		{"POST", "/api/portal/cells/" + p + "/approve"},
		{"POST", "/api/portal/corrections/" + p + "/close"},
		{"PUT", "/api/portal/pace"},
		{"PUT", "/api/portal/wishes/" + wish + "/fulfillment"},
	} {
		t.Run(entry.path, func(t *testing.T) {
			body := &controlledPortalBody{make(chan struct{}), make(chan struct{}), make(chan struct{}), sync.Once{}, strings.NewReader("{")}
			req := httptest.NewRequest(entry.method, entry.path, nil)
			req.Body = body
			req.Header.Set("Content-Type", "application/json")
			req = req.WithContext(tenant.WithPrincipal(t.Context(), admin))
			writer := &controlledDeadlineWriter{httptest.NewRecorder(), make(chan time.Time, 3)}
			done := make(chan struct{})
			go func() { defer close(done); f.h.ServeHTTP(writer, req) }()
			defer func() {
				close(body.release)
				select {
				case <-done:
				case <-time.After(10 * time.Second):
					t.Error("handler did not finish")
				}
			}()
			select {
			case <-body.entered:
			case <-time.After(10 * time.Second):
				t.Fatal("handler never read body")
			}
			// NOWAIT rejects exactly the missing predecode boundary on baseline.
			err := db.InTenant(dbtest.Seed(t.Context()), f.d.App, tid, func(tx pgx.Tx) error {
				if _, err := tx.Exec(t.Context(), `SELECT id FROM tenants WHERE id=$1::uuid FOR NO KEY UPDATE NOWAIT`, tid); err != nil {
					return err
				}
				var got bool
				if err := tx.QueryRow(t.Context(), `SELECT pg_try_advisory_xact_lock(hashtextextended($1,0))`, tid).Scan(&got); err != nil {
					return err
				}
				if !got {
					return errors.New("tree lock held while reading body")
				}
				return nil
			})
			if err != nil {
				t.Errorf("request body blocked tenant/tree work: %v", err)
			}
			select {
			case at := <-writer.deadlines:
				if at.IsZero() {
					t.Error("body has no read deadline")
				}
			default:
				t.Error("body read started without a deadline")
			}
			// Trigger the deadline explicitly; the decoder must reject timeout.
			close(body.expired)
			select {
			case <-done:
			case <-time.After(10 * time.Second):
				t.Fatal("deadline did not interrupt body read")
			}
			if writer.Code != 400 || !strings.Contains(writer.Body.String(), "invalid") {
				t.Errorf("timeout %d %s", writer.Code, writer.Body)
			}
			select {
			case at := <-writer.deadlines:
				if !at.IsZero() {
					t.Error("read deadline was not cleared")
				}
			default:
				t.Error("read deadline not reset after decoding")
			}
		})
	}
}

func TestPortalProductMoveAndRestoreRegistersOnce(t *testing.T) {
	f := productFixture(t)
	tid := makeTenant(t, f.d, "lifecycle", "Lifecycle")
	admin := makePerson(t, f.d, tid, "Admin", "admin")
	parent := insertNode(t, f.d, tid, "PRJ-1", "project", "Parent", "", "open", "", "{}")
	nested := insertNode(t, f.d, tid, "PPR-1", "portal_product", "Nested", "summary", "published", parent, "{}")
	setPortal(t, f.d, tid, true)
	update := func(query string, args ...any) {
		t.Helper()
		if err := db.InTenant(dbtest.Seed(t.Context()), f.d.App, tid, func(tx pgx.Tx) error {
			if _, err := tx.Exec(t.Context(), `SELECT set_config('aeon.portal_moderation','on',true)`); err != nil {
				return err
			}
			_, err := tx.Exec(t.Context(), query, args...)
			return err
		}); err != nil {
			t.Fatal(err)
		}
	}
	update(`UPDATE nodes SET parent_id=NULL WHERE id=$1::uuid`, nested)
	path := "/api/portal/products/" + nested + "/settings"
	rec := f.do("GET", path, "", "203.0.113.173:1", &admin, nil, nil)
	if rec.Code != 200 {
		t.Fatalf("moved product settings %d %s", rec.Code, rec.Body)
	}
	var settings productSettings
	if err := json.NewDecoder(rec.Body).Decode(&settings); err != nil {
		t.Fatal(err)
	}
	if settings.Published || settings.Policy != "disabled" {
		t.Fatalf("move enabled participation %+v", settings)
	}
	configureFixtureProduct(t, f.d, tid, nested, "kept-slug", "disabled", false)
	update(`UPDATE nodes SET parent_id=$2::uuid WHERE id=$1::uuid`, nested, parent)
	update(`UPDATE nodes SET deleted_at=clock_timestamp() WHERE id=$1::uuid`, nested)
	update(`UPDATE nodes SET parent_id=NULL WHERE id=$1::uuid`, nested)
	update(`UPDATE nodes SET deleted_at=NULL WHERE id=$1::uuid`, nested)
	rec = f.do("GET", path, "", "203.0.113.173:1", &admin, nil, nil)
	if rec.Code != 200 {
		t.Fatalf("restored product settings %d %s", rec.Code, rec.Body)
	}
	if err := json.NewDecoder(rec.Body).Decode(&settings); err != nil {
		t.Fatal(err)
	}
	if settings.Slug != "kept-slug" || settings.Published || settings.Policy != "disabled" || settings.Revision != 1 {
		t.Fatalf("restore replaced settings %+v", settings)
	}
	if rec := f.do("GET", "/api/public/portal/lifecycle/products/kept-slug", "", "203.0.113.173:1", nil, nil, nil); rec.Code != 404 {
		t.Fatalf("restore published product %d", rec.Code)
	}
	// A product inserted while deleted has never been eligible. Restoring it
	// must create settings too, without replacing the earlier explicit default.
	var deleted string
	if err := db.InTenant(dbtest.Seed(t.Context()), f.d.App, tid, func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), `INSERT INTO nodes(tenant_id,key,kind_id,title,state,deleted_at) SELECT $1::uuid,'PPR-2',id,'Deleted','published',clock_timestamp() FROM node_kinds WHERE slug='portal_product' RETURNING id::text`, tid).Scan(&deleted)
	}); err != nil {
		t.Fatal(err)
	}
	update(`UPDATE nodes SET deleted_at=NULL WHERE id=$1::uuid`, deleted)
	rec = f.do("GET", "/api/portal/products/"+deleted+"/settings", "", "203.0.113.173:1", &admin, nil, nil)
	if rec.Code != 200 {
		t.Fatalf("first restore settings %d %s", rec.Code, rec.Body)
	}
	if err := json.NewDecoder(rec.Body).Decode(&settings); err != nil {
		t.Fatal(err)
	}
	if settings.Published || settings.Policy != "disabled" || settings.Default {
		t.Fatalf("restore changed publication/default %+v", settings)
	}
}
