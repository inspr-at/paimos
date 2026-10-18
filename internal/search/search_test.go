// SPDX-License-Identifier: AGPL-3.0-only

package search

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"net/url"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/embedding"
	"github.com/inspr-at/paimos/internal/tenant"
)

func TestLexicalPaginationFiltersAndTenants(t *testing.T) {
	d := dbtest.Open(t)
	ctx := t.Context()
	const (
		tenantA = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaa1"
		tenantB = "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbb2"
		n1      = "00000000-0000-4000-8000-000000000001"
		n2      = "00000000-0000-4000-8000-000000000002"
		n3      = "00000000-0000-4000-8000-000000000003"
		german  = "00000000-0000-4000-8000-000000000004"
		other   = "00000000-0000-4000-8000-000000000005"
		ticket  = "00000000-0000-4000-8000-000000000006"
		doneID  = "00000000-0000-4000-8000-000000000007"
		gone    = "00000000-0000-4000-8000-000000000008"
		bNode   = "00000000-0000-4000-8000-000000000009"
	)
	seedTenant(t, d, tenantA, "alpha")
	seedTenant(t, d, tenantB, "beta")
	insertNode(t, d.App, tenantA, n1, "PAI-1", "project", "Alpha signal", "Alpha signal", "open")
	insertNode(t, d.App, tenantA, n2, "PAI-2", "project", "Alpha signal", "Alpha signal", "open")
	insertNode(t, d.App, tenantA, n3, "PAI-3", "project", "Alpha signal", "Alpha signal", "open")
	insertNode(t, d.App, tenantA, german, "PAI-4", "project", "Deutscher Begriff", "Text", "open")
	insertNode(t, d.App, tenantA, other, "PAI-5", "project", "English search title", "Markdown body", "open")
	insertNode(t, d.App, tenantA, ticket, "TKT-1", "work", "Sharedtoken project", "body", "open")
	insertNode(t, d.App, tenantA, doneID, "PAI-6", "project", "Sharedtoken project", "body", "done")
	insertNode(t, d.App, tenantA, gone, "PAI-7", "project", "Alpha signal", "Alpha signal", "open")
	insertNode(t, d.App, tenantB, bNode, "PAI-1", "project", "Alpha signal", "Alpha signal", "open")
	if err := db.InTenant(dbtest.Seed(ctx), d.App, tenantA, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE nodes SET deleted_at = now() WHERE id = $1`, gone)
		return err
	}); err != nil {
		t.Fatal(err)
	}

	mod := New(d.App, nil)
	mux := http.NewServeMux()
	mod.Mount(mux)
	ada := seedPerson(t, d, tenantA, "cccccccc-cccc-4ccc-8ccc-ccccccccccc1", "Ada")
	bob := seedPerson(t, d, tenantB, "dddddddd-dddd-4ddd-8ddd-ddddddddddd2", "Bob")

	var cursor string
	var seen []string
	wants := []float64{1.0 / 61, 1.0 / 62, 1.0 / 63}
	ids := []string{n1, n2, n3}
	for {
		path := "/api/search?q=Alpha&limit=1"
		if cursor != "" {
			path += "&cursor=" + url.QueryEscape(cursor)
		}
		status, raw := call(mux, &ada, path)
		if status != http.StatusOK {
			t.Fatalf("page %d: %d %s", len(seen), status, raw)
		}
		page := decodePage(t, raw)
		if len(page.Items) == 0 {
			break
		}
		if len(page.Items) != 1 || len(seen) >= len(ids) {
			t.Fatalf("page %+v seen %v", page.Items, seen)
		}
		item := page.Items[0]
		if item.Node.ID != ids[len(seen)] {
			t.Fatalf("order %s", item.Node.ID)
		}
		if math.Abs(item.Score-wants[len(seen)]) > 1e-9 {
			t.Fatalf("score %g want %g", item.Score, wants[len(seen)])
		}
		if item.Node.Key == "" || item.Node.KindID == "" || item.Node.Title != "Alpha signal" || item.Node.Position == "" || len(item.Node.Fields) == 0 || item.Node.ParentID != nil || item.Node.CreatedAt == "" {
			t.Fatalf("node %+v", item.Node)
		}
		seen = append(seen, item.Node.ID)
		if page.Next == nil {
			break
		}
		cursor = *page.Next
	}
	if len(seen) != 3 {
		t.Fatalf("seen %v", seen)
	}

	status, raw := call(mux, &ada, "/api/search?q="+url.QueryEscape("Deutscher Begriff"))
	page := mustOK(t, status, raw)
	if len(page.Items) != 1 || page.Items[0].Node.ID != german {
		t.Fatalf("german %+v", page.Items)
	}
	status, raw = call(mux, &ada, "/api/search?q="+url.QueryEscape("English search"))
	page = mustOK(t, status, raw)
	if len(page.Items) != 1 || page.Items[0].Node.ID != other {
		t.Fatalf("english %+v", page.Items)
	}

	kindID := kindBySlug(t, d.App, tenantA, "work")
	status, raw = call(mux, &ada, "/api/search?q=Sharedtoken&kind_id="+kindID)
	page = mustOK(t, status, raw)
	if len(page.Items) != 1 || page.Items[0].Node.ID != ticket {
		t.Fatalf("kind %+v", page.Items)
	}
	status, raw = call(mux, &ada, "/api/search?q=Sharedtoken&state=done")
	page = mustOK(t, status, raw)
	if len(page.Items) != 1 || page.Items[0].Node.ID != doneID || page.Items[0].Node.State != "done" {
		t.Fatalf("state %+v", page.Items)
	}
	status, raw = call(mux, &bob, "/api/search?q=Alpha")
	page = mustOK(t, status, raw)
	if len(page.Items) != 1 || page.Items[0].Node.ID != bNode {
		t.Fatalf("tenant B %+v", page.Items)
	}
}

func TestHybridMaskFallbackAndWorker(t *testing.T) {
	d := dbtest.Open(t)
	ctx := t.Context()
	const (
		tenantA = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaa1"
		nodeID  = "00000000-0000-4000-8000-000000000021"
		lexID   = "00000000-0000-4000-8000-000000000022"
	)
	seedTenant(t, d, tenantA, "alpha")
	insertNode(t, d.App, tenantA, nodeID, "PAI-1", "project", "zzzzunique", "vector body", "open")
	insertNode(t, d.App, tenantA, lexID, "PAI-2", "project", "English search title", "Markdown body", "open")
	vec := make([]float32, embedding.Dimensions)
	vec[0] = 1
	provider := fixedProvider{model: "test-model", vec: vec}
	hash := embedding.ContentHash("zzzzunique", "vector body")
	if err := db.InTenant(dbtest.Seed(ctx), d.App, tenantA, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `
			INSERT INTO node_embeddings (tenant_id, node_id, model, content_hash, embedding)
			VALUES ($1, $2, 'test-model', $3, $4::real[]::halfvec(1536))`,
			tenantA, nodeID, hash, pgtype.FlatArray[float32](vec))
		return err
	}); err != nil {
		t.Fatal(err)
	}

	mux := http.NewServeMux()
	New(d.App, provider).Mount(mux)
	ada := seedPerson(t, d, tenantA, "cccccccc-cccc-4ccc-8ccc-ccccccccccc1", "Ada")
	status, raw := call(mux, &ada, "/api/search?q=neverlexicalmatch")
	page := mustOK(t, status, raw)
	if len(page.Items) != 0 {
		t.Fatalf("queued job exposed vector %+v", page.Items)
	}

	if err := db.InTenant(dbtest.Seed(ctx), d.App, tenantA, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `DELETE FROM node_embedding_jobs WHERE node_id = $1`, nodeID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	status, raw = call(mux, &ada, "/api/search?q=neverlexicalmatch")
	page = mustOK(t, status, raw)
	if len(page.Items) != 1 || page.Items[0].Node.ID != nodeID || math.Abs(page.Items[0].Score-1.0/61) > 1e-9 {
		t.Fatalf("vector hit %+v", page.Items)
	}
	status, raw = call(mux, &ada, "/api/search?q=zzzzunique")
	page = mustOK(t, status, raw)
	if len(page.Items) != 1 || math.Abs(page.Items[0].Score-2.0/61) > 1e-9 {
		t.Fatalf("fused %+v", page.Items)
	}

	broken := http.NewServeMux()
	New(d.App, fixedProvider{model: "test-model", err: errors.New("down")}).Mount(broken)
	status, raw = call(broken, &ada, "/api/search?q="+url.QueryEscape("English search"))
	page = mustOK(t, status, raw)
	if len(page.Items) != 1 || page.Items[0].Node.ID != lexID {
		t.Fatalf("lexical fallback %+v", page.Items)
	}

	worker := embedding.NewWorker(d.App, provider, embedding.Options{})
	n, err := worker.ProcessOnce(ctx)
	if err != nil || n != 1 {
		t.Fatalf("worker %d %v", n, err)
	}
	if err := db.InTenant(dbtest.Seed(ctx), d.App, tenantA, func(tx pgx.Tx) error {
		var jobs int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM node_embedding_jobs`).Scan(&jobs); err != nil {
			return err
		}
		if jobs != 0 {
			return errors.New("job left after worker")
		}
		var events int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM events WHERE type = 'node.embedded'`).Scan(&events); err != nil {
			return err
		}
		if events != 1 {
			return errors.New("missing embedding event")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

type pageBody struct {
	Items []struct {
		Node struct {
			ID        string          `json:"id"`
			Key       string          `json:"key"`
			KindID    string          `json:"kind_id"`
			Title     string          `json:"title"`
			Body      string          `json:"body"`
			Fields    json.RawMessage `json:"fields"`
			State     string          `json:"state"`
			ParentID  *string         `json:"parent_id"`
			Position  string          `json:"position"`
			CreatedAt string          `json:"created_at"`
			UpdatedAt string          `json:"updated_at"`
		} `json:"node"`
		Score float64 `json:"score"`
	} `json:"items"`
	Next *string `json:"next_cursor"`
}

func decodePage(t *testing.T, raw string) pageBody {
	t.Helper()
	var page pageBody
	if err := json.Unmarshal([]byte(raw), &page); err != nil {
		t.Fatal(err)
	}
	return page
}

func mustOK(t *testing.T, status int, raw string) pageBody {
	t.Helper()
	if status != http.StatusOK {
		t.Fatalf("%d %s", status, raw)
	}
	return decodePage(t, raw)
}

// seedPerson stores a workspace member: search shows only what the caller's
// bindings make visible (ADR-003 P2).
func seedPerson(t *testing.T, d *dbtest.DB, tenantID, id, name string) tenant.Principal {
	t.Helper()
	if _, err := d.Admin.Exec(t.Context(), `INSERT INTO principals (tenant_id, id, kind, name) VALUES ($1, $2, 'person', $3)`, tenantID, id, name); err != nil {
		t.Fatal(err)
	}
	dbtest.BindRole(t, d, tenantID, id, "member")
	return tenant.Principal{ID: id, TenantID: tenantID, Kind: tenant.Person, Name: name}
}

func seedTenant(t *testing.T, d *dbtest.DB, id, slug string) {
	t.Helper()
	if _, err := d.Admin.Exec(t.Context(), `INSERT INTO tenants (id, slug, name) VALUES ($1, $2, $3)`, id, slug, slug); err != nil {
		t.Fatal(err)
	}
}

func insertNode(t *testing.T, pool *pgxpool.Pool, tenant, id, key, slug, title, body, state string) {
	t.Helper()
	err := db.InTenant(dbtest.Seed(t.Context()), pool, tenant, func(tx pgx.Tx) error {
		tag, err := tx.Exec(t.Context(), `
			INSERT INTO nodes (tenant_id, id, key, kind_id, title, body, state)
			SELECT $1, $2, $3, k.id, $4, $5, $6
			FROM node_kinds k WHERE k.slug = $7`,
			tenant, id, key, title, body, state, slug)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return errors.New("kind missing")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func kindBySlug(t *testing.T, pool *pgxpool.Pool, tenant, slug string) string {
	t.Helper()
	var id string
	err := db.InTenant(dbtest.Seed(t.Context()), pool, tenant, func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), `SELECT id::text FROM node_kinds WHERE slug = $1`, slug).Scan(&id)
	})
	if err != nil {
		t.Fatal(err)
	}
	return id
}

type fixedProvider struct {
	model string
	vec   []float32
	err   error
}

func (p fixedProvider) Model() string { return p.model }

func (p fixedProvider) Embed(_ context.Context, texts []string) ([][]float32, error) {
	if p.err != nil {
		return nil, p.err
	}
	out := make([][]float32, len(texts))
	for i := range texts {
		out[i] = append([]float32(nil), p.vec...)
	}
	return out, nil
}
