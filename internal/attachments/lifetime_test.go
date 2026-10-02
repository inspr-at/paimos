// SPDX-License-Identifier: AGPL-3.0-only
package attachments

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/events"
	"github.com/inspr-at/paimos/internal/tenant"
)

func ageBlob(t *testing.T, store Store, tenantID, hash string, image bool) {
	t.Helper()
	variants := []string{"original"}
	if image {
		variants = append(variants, "thumb", "preview")
	}
	for _, v := range variants {
		path, err := store.path(tenantID, hash, v)
		if err != nil {
			t.Fatal(err)
		}
		old := time.Now().Add(-8 * 24 * time.Hour)
		if err := os.Chtimes(path, old, old); err != nil {
			t.Fatal(err)
		}
	}
}

func TestInventoryRetainsProfileAssetsAndHistory(t *testing.T) {
	d, p, node := setup(t)
	store := Store{FilesDir: t.TempDir()}
	var imageBlob, fontBlob, svgBlob, historyBlob Prepared
	err := db.InTenant(dbtest.Seed(t.Context()), d.App, p.TenantID, func(tx pgx.Tx) error {
		var err error
		imageBlob, err = store.Put(t.Context(), tx, p.TenantID, OwnerQuoteProfile, bytes.NewReader(fixturePNG(t)))
		if err != nil {
			return err
		}
		fontBlob, err = store.PutProfileAsset(t.Context(), tx, p.TenantID, []byte("validated font bytes"), "font/woff2")
		if err != nil {
			return err
		}
		svgBlob, err = store.PutProfileAsset(t.Context(), tx, p.TenantID, []byte(`<svg xmlns="http://www.w3.org/2000/svg"/>`), "image/svg+xml")
		if err != nil {
			return err
		}
		for _, blob := range []Prepared{imageBlob, fontBlob, svgBlob} {
			if _, err := tx.Exec(t.Context(), `INSERT INTO quote_document_profile_assets(tenant_id,sha256,content_type,size,created_by_principal_id) VALUES($1,$2,$3,$4,$5)`, p.TenantID, blob.SHA256, blob.ContentType, blob.Size, p.ID); err != nil {
				return err
			}
		}
		historyBlob, err = store.Put(t.Context(), tx, p.TenantID, OwnerAttachment, strings.NewReader("prior attachment bytes"))
		if err != nil {
			return err
		}
		_, err = events.Append(t.Context(), tx, p, events.Change{NodeID: &node, Type: "attachment.updated", Before: map[string]any{"sha256": historyBlob.SHA256, "content_type": historyBlob.ContentType}, After: map[string]any{}})
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, blob := range []Prepared{imageBlob, fontBlob, svgBlob, historyBlob} {
		ageBlob(t, store, p.TenantID, blob.SHA256, blob.Image)
	}
	for _, apply := range []bool{false, true} {
		report, err := GC(t.Context(), d.App, store, p.TenantID, apply)
		if err != nil || report.Candidates != 0 || report.Removed != 0 {
			t.Fatalf("GC apply=%v: %+v %v", apply, report, err)
		}
	}
	report, err := Verify(t.Context(), d.App, store, p.TenantID)
	if err != nil || report.Checked != 4 || len(report.Issues) != 0 {
		t.Fatalf("verify %+v %v", report, err)
	}
	// Inventory includes raw fonts/SVGs without inventing image derivatives.
	path, _ := store.path(p.TenantID, fontBlob.SHA256, "original")
	if err := os.WriteFile(path, []byte("corrupt font"), 0600); err != nil {
		t.Fatal(err)
	}
	path, _ = store.path(p.TenantID, svgBlob.SHA256, "original")
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	report, err = Verify(t.Context(), d.App, store, p.TenantID)
	if err != nil || report.Checked != 4 || len(report.Issues) != 2 {
		t.Fatalf("verify damaged owners %+v %v", report, err)
	}
	issues := map[string]string{}
	for _, issue := range report.Issues {
		issues[issue.SHA256+":"+issue.Variant] = issue.Problem
	}
	if issues[fontBlob.SHA256+":original"] != "corrupt" || issues[svgBlob.SHA256+":original"] != "missing" {
		t.Fatalf("issues %v", issues)
	}
}

func oldOrphan(t *testing.T, d *dbtest.DB, p tenant.Principal, store Store, body []byte) Prepared {
	t.Helper()
	var blob Prepared
	// A failed transaction leaves immutable bytes but no durable owner.
	rollback := errors.New("rollback upload")
	err := db.InTenant(dbtest.Seed(t.Context()), d.App, p.TenantID, func(tx pgx.Tx) error {
		var err error
		blob, err = store.Put(t.Context(), tx, p.TenantID, OwnerAttachment, bytes.NewReader(body))
		if err != nil {
			return err
		}
		return rollback
	})
	if !errors.Is(err, rollback) {
		t.Fatal(err)
	}
	ageBlob(t, store, p.TenantID, blob.SHA256, blob.Image)
	return blob
}

// This is a database barrier, not a timing guess. The test database is private;
// its only contending advisory lock is the writer/GC lifetime under test.
func waitForBlobWaiter(ctx context.Context, pool *pgxpool.Pool) error {
	for {
		var waiting bool
		if err := pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_locks WHERE locktype='advisory' AND database=(SELECT oid FROM pg_database WHERE datname=current_database()) AND NOT granted)`).Scan(&waiting); err != nil {
			return err
		}
		if waiting {
			return nil
		}
		if err := ctx.Err(); err != nil {
			return err
		}
	}
}

func TestGCWaitsForReusedBlobReferenceCommit(t *testing.T) {
	d, p, node := setup(t)
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	store := Store{FilesDir: t.TempDir()}
	body := []byte("old orphan reused while cleanup discovers it")
	blob := oldOrphan(t, d, p, store, body)
	written, commit := make(chan struct{}), make(chan struct{})
	writerDone := make(chan error, 1)
	go func() {
		writerDone <- db.InTenant(dbtest.Seed(ctx), d.App, p.TenantID, func(tx pgx.Tx) error {
			if _, err := store.Put(ctx, tx, p.TenantID, OwnerAttachment, bytes.NewReader(body)); err != nil {
				return err
			}
			close(written)
			select {
			case <-commit:
			case <-ctx.Done():
				return ctx.Err()
			}
			_, err := tx.Exec(ctx, `INSERT INTO attachments(tenant_id,node_id,sha256,name,content_type,size,position,created_by) VALUES($1,$2,$3,'reused.txt',$4,$5,1,$6)`, p.TenantID, node, blob.SHA256, blob.ContentType, blob.Size, p.ID)
			return err
		})
	}()
	select {
	case <-written:
	case err := <-writerDone:
		t.Fatalf("writer before publish: %v", err)
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	type result struct {
		report GCReport
		err    error
	}
	gcDone := make(chan result, 1)
	go func() { r, e := GC(ctx, d.App, store, p.TenantID, true); gcDone <- result{r, e} }()
	if err := waitForBlobWaiter(ctx, d.Admin); err != nil {
		t.Fatal(err)
	}
	close(commit)
	if err := <-writerDone; err != nil {
		t.Fatal(err)
	}
	r := <-gcDone
	if r.err != nil || r.report.Removed != 0 || r.report.Candidates != 0 {
		t.Fatalf("GC %+v %v", r.report, r.err)
	}
	assertBlobBytes(t, store, p.TenantID, blob.SHA256, body)
}

func TestUploadWaitsThroughGCUnlinkAndRecreatesBlob(t *testing.T) {
	d, p, node := setup(t)
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	store := Store{FilesDir: t.TempDir()}
	body := fixturePNG(t)
	blob := oldOrphan(t, d, p, store, body)
	checked, unlink := make(chan struct{}), make(chan struct{})
	gcDone := make(chan error, 1)
	go func() {
		_, err := gc(ctx, d.App, store, p.TenantID, true, func(path string) error {
			if filepath.Base(path) == blob.SHA256 {
				close(checked)
				select {
				case <-unlink:
				case <-ctx.Done():
					return ctx.Err()
				}
			}
			return os.Remove(path)
		})
		gcDone <- err
	}()
	select {
	case <-checked:
	case err := <-gcDone:
		t.Fatalf("GC before unlink: %v", err)
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	mux := http.NewServeMux()
	New(d.App, store).Mount(mux)
	uploadDone := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		r := httptest.NewRequest("POST", "/api/nodes/"+node+"/attachments", bytes.NewReader(body)).WithContext(tenant.WithPrincipal(ctx, p))
		r.Header.Set("Content-Type", "image/png")
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		uploadDone <- w
	}()
	if err := waitForBlobWaiter(ctx, d.Admin); err != nil {
		t.Fatal(err)
	}
	close(unlink)
	if err := <-gcDone; err != nil {
		t.Fatal(err)
	}
	w := <-uploadDone
	if w.Code != 201 {
		t.Fatalf("upload %d %s", w.Code, w.Body.String())
	}
	assertBlobBytes(t, store, p.TenantID, blob.SHA256, body)
	verified, err := Verify(ctx, d.App, store, p.TenantID)
	if err != nil || verified.Checked != 1 || len(verified.Issues) != 0 {
		t.Fatalf("verify after interleaving %+v %v", verified, err)
	}
}

func assertBlobBytes(t *testing.T, store Store, tenantID, hash string, expected []byte) {
	t.Helper()
	path, err := store.path(tenantID, hash, "original")
	if err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(got, expected) {
		t.Fatalf("blob bytes: %v", err)
	}
}

func TestBlobLifetimeRejectsWrongTenantAndSnapshotIsolation(t *testing.T) {
	d, p, _ := setup(t)
	store := Store{FilesDir: t.TempDir()}
	staged, err := store.Stage(t.Context(), p.TenantID, strings.NewReader("private bytes"))
	if err != nil {
		t.Fatal(err)
	}
	defer staged.Close()
	if err := Publish(t.Context(), nil, Owner("unregistered"), staged); err == nil {
		t.Fatal("unknown owner accepted")
	}
	if err := Publish(t.Context(), nil, OwnerAttachment, staged); err == nil {
		t.Fatal("nil transaction accepted")
	}
	for _, isolation := range []pgx.TxIsoLevel{pgx.RepeatableRead, pgx.Serializable} {
		tx, err := d.App.BeginTx(t.Context(), pgx.TxOptions{IsoLevel: isolation})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(t.Context(), `SELECT set_config('aeon.tenant_id',$1,true)`, p.TenantID); err != nil {
			t.Fatal(err)
		}
		if err := Publish(t.Context(), tx, OwnerAttachment, staged); err == nil {
			t.Errorf("snapshot %s accepted", isolation)
		}
		_ = tx.Rollback(t.Context())
	}
	err = db.InTenant(dbtest.Seed(t.Context()), d.App, "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", func(tx pgx.Tx) error {
		if err := Publish(t.Context(), tx, OwnerAttachment, staged); err == nil {
			return fmt.Errorf("wrong tenant accepted")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	path, _ := store.path(p.TenantID, staged.SHA256, "original")
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("published without valid transaction: %v", err)
	}
}

func TestGCOnlyCollectsOldOrphansForSelectedTenant(t *testing.T) {
	d, p, _ := setup(t)
	store := Store{FilesDir: t.TempDir()}
	body := fixturePNG(t)
	old := oldOrphan(t, d, p, store, body)
	var young Prepared
	err := db.InTenant(dbtest.Seed(t.Context()), d.App, p.TenantID, func(tx pgx.Tx) error {
		var err error
		young, err = store.Put(t.Context(), tx, p.TenantID, OwnerAttachment, strings.NewReader("recent orphan"))
		return err // no reference: the seven-day grace still applies
	})
	if err != nil {
		t.Fatal(err)
	}
	other := "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"
	err = db.InTenant(dbtest.Seed(t.Context()), d.App, other, func(tx pgx.Tx) error {
		if _, err := tx.Exec(t.Context(), `INSERT INTO tenants(id,slug,name) VALUES($1,'other-files','Other files')`, other); err != nil {
			return err
		}
		var actor string
		if err := tx.QueryRow(t.Context(), `INSERT INTO principals(tenant_id,kind,name) VALUES($1,'person','Other person') RETURNING id::text`, other).Scan(&actor); err != nil {
			return err
		}
		blob, err := store.Put(t.Context(), tx, other, OwnerQuoteProfile, bytes.NewReader(body))
		if err != nil {
			return err
		}
		_, err = tx.Exec(t.Context(), `INSERT INTO quote_document_profile_assets(tenant_id,sha256,content_type,size,created_by_principal_id) VALUES($1,$2,$3,$4,$5)`, other, blob.SHA256, blob.ContentType, blob.Size, actor)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	ageBlob(t, store, other, old.SHA256, true)
	report, err := GC(t.Context(), d.App, store, p.TenantID, false)
	if err != nil || report.Candidates != 3 || report.Removed != 0 {
		t.Fatalf("dry run %+v %v", report, err)
	}
	assertBlobBytes(t, store, p.TenantID, old.SHA256, body)
	report, err = GC(t.Context(), d.App, store, p.TenantID, true)
	if err != nil || report.Candidates != 3 || report.Removed != 3 {
		t.Fatalf("apply %+v %v", report, err)
	}
	assertBlobBytes(t, store, p.TenantID, young.SHA256, []byte("recent orphan"))
	assertBlobBytes(t, store, other, old.SHA256, body)
	verified, err := Verify(t.Context(), d.App, store, other)
	if err != nil || verified.Checked != 1 || len(verified.Issues) != 0 {
		t.Fatalf("other tenant verify %+v %v", verified, err)
	}
}
