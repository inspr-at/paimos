// SPDX-License-Identifier: AGPL-3.0-only

package embedding

import (
	"context"
	"errors"
	"math"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
)

func TestContentHashAndVector(t *testing.T) {
	if got := ContentHash("English search title", "Markdown body"); len(got) != 64 {
		t.Fatalf("hash %q", got)
	}
	if ContentHash("English search title", "Markdown body") == ContentHash("English search title", "other") {
		t.Fatal("hash ignored body")
	}
	if Document("Title", "Body") != "Title\nBody" {
		t.Fatal(Document("Title", "Body"))
	}
	if err := Validate(make([]float32, Dimensions)); err != nil {
		t.Fatal(err)
	}
	if err := Validate([]float32{1}); err == nil {
		t.Fatal("short vector accepted")
	}
	bad := make([]float32, Dimensions)
	bad[0] = float32(math.NaN())
	if err := Validate(bad); err == nil {
		t.Fatal("NaN accepted")
	}
}

func TestWorkerStoresRewritesAndIsolates(t *testing.T) {
	d := dbtest.Open(t)
	ctx := t.Context()
	const (
		tenantA = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaa1"
		tenantB = "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbb2"
		nodeA   = "00000000-0000-4000-8000-000000000001"
		nodeB   = "00000000-0000-4000-8000-000000000002"
	)
	seedTenant(t, d, tenantA, "alpha")
	seedTenant(t, d, tenantB, "beta")
	insertNode(t, d.App, tenantA, nodeA, "PAI-1", "English search title", "Markdown body", "open")
	insertNode(t, d.App, tenantB, nodeB, "PAI-1", "Other tenant", "hidden", "open")
	baseline := countEvents(t, d.App, tenantA)

	provider := fixedProvider{model: "test-model", vec: unitVector()}
	w := NewWorker(d.App, provider, Options{})
	n, err := w.ProcessOnce(ctx)
	if err != nil || n != 2 {
		t.Fatalf("process %d %v", n, err)
	}
	hashA := ContentHash("English search title", "Markdown body")
	assertEmbedding(t, d.App, tenantA, nodeA, "test-model", hashA)
	assertEvent(t, d.App, tenantA, nodeA, true, "", hashA)
	assertNoJob(t, d.App, tenantA, nodeA)
	if visible(t, d.App, tenantA, "neverlexicalmatch", provider) != 1 {
		t.Fatal("stored vector hidden")
	}
	if visible(t, d.App, tenantB, "neverlexicalmatch", provider) != 1 {
		t.Fatal("tenant B vector missing")
	}
	var leaked int
	if err := d.App.QueryRow(ctx, `SELECT count(*) FROM node_embeddings`).Scan(&leaked); err != nil {
		t.Fatal(err)
	}
	if leaked != 0 {
		t.Fatalf("embeddings visible without tenant: %d", leaked)
	}
	if err := d.App.QueryRow(ctx, `SELECT count(*) FROM events`).Scan(&leaked); err != nil {
		t.Fatal(err)
	}
	if leaked != 0 {
		t.Fatalf("events visible without tenant: %d", leaked)
	}
	var other int
	if err := db.InTenant(dbtest.Seed(ctx), d.App, tenantA, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT count(*) FROM node_embeddings WHERE node_id = $1`, nodeB).Scan(&other)
	}); err != nil {
		t.Fatal(err)
	}
	if other != 0 {
		t.Fatal("cross-tenant embedding visible")
	}

	if err := db.InTenant(dbtest.Seed(ctx), d.App, tenantA, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE nodes SET title = $2 WHERE id = $1`, nodeA, "Renamed title")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	n, err = w.ProcessOnce(ctx)
	if err != nil || n != 1 {
		t.Fatalf("rewrite %d %v", n, err)
	}
	hashB := ContentHash("Renamed title", "Markdown body")
	assertEmbedding(t, d.App, tenantA, nodeA, "test-model", hashB)
	assertEvent(t, d.App, tenantA, nodeA, false, hashA, hashB)
	if countEvents(t, d.App, tenantA) != baseline+2 {
		t.Fatalf("events %d", countEvents(t, d.App, tenantA))
	}

	if err := db.InTenant(dbtest.Seed(ctx), d.App, tenantA, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE nodes SET title = title WHERE id = $1`, nodeA)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	n, err = w.ProcessOnce(ctx)
	if err != nil || n != 1 {
		t.Fatalf("same content %d %v", n, err)
	}
	if countEvents(t, d.App, tenantA) != baseline+2 {
		t.Fatal("unchanged content wrote an event")
	}
	assertNoJob(t, d.App, tenantA, nodeA)

	ctxStop, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		w.Run(ctxStop)
		close(done)
	}()
	time.Sleep(20 * time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("worker did not stop")
	}
}

func TestWorkerKeepsStaleVectorOffTheRow(t *testing.T) {
	d := dbtest.Open(t)
	ctx := t.Context()
	const (
		tenantID = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaa1"
		nodeID   = "00000000-0000-4000-8000-000000000001"
	)
	seedTenant(t, d, tenantID, "alpha")
	insertNode(t, d.App, tenantID, nodeID, "PAI-1", "Original title", "body", "open")
	inner := fixedProvider{model: "test-model", vec: unitVector()}
	racing := raceProvider{Provider: inner, pool: d.App, tenant: tenantID, node: nodeID, title: "Changed during embed"}
	w := NewWorker(d.App, racing, Options{})
	n, err := w.ProcessOnce(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("stale write counted %d", n)
	}
	if countEmbeddings(t, d.App, tenantID) != 0 {
		t.Fatal("stale vector stored")
	}
	var status string
	if err := db.InTenant(dbtest.Seed(ctx), d.App, tenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT status FROM node_embedding_jobs WHERE node_id = $1`, nodeID).Scan(&status)
	}); err != nil {
		t.Fatal(err)
	}
	if status != "queued" {
		t.Fatalf("status %s", status)
	}

	plain := NewWorker(d.App, inner, Options{})
	n, err = plain.ProcessOnce(ctx)
	if err != nil || n != 1 {
		t.Fatalf("retry %d %v", n, err)
	}
	assertEmbedding(t, d.App, tenantID, nodeID, "test-model", ContentHash("Changed during embed", "body"))
}

func TestWorkerBackoffAndDeletedNodes(t *testing.T) {
	d := dbtest.Open(t)
	ctx := t.Context()
	const (
		tenantID = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaa1"
		liveID   = "00000000-0000-4000-8000-000000000001"
		deadID   = "00000000-0000-4000-8000-000000000002"
	)
	seedTenant(t, d, tenantID, "alpha")
	insertNode(t, d.App, tenantID, liveID, "PAI-1", "Retry me", "body", "open")
	insertNode(t, d.App, tenantID, deadID, "PAI-2", "Delete me", "body", "open")
	if err := db.InTenant(dbtest.Seed(ctx), d.App, tenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE nodes SET deleted_at = now() WHERE id = $1`, deadID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	baseline := countEvents(t, d.App, tenantID)

	failing := NewWorker(d.App, fixedProvider{model: "test-model", err: errors.New("boom")}, Options{})
	if _, err := failing.ProcessOnce(ctx); err == nil {
		t.Fatal("expected embed error")
	}
	attempts, status, last := jobState(t, d.App, tenantID, liveID)
	if attempts != 1 || status != "failed" || last == "" {
		t.Fatalf("after failure %d %s %q", attempts, status, last)
	}
	n, err := failing.ProcessOnce(ctx)
	if err != nil || n != 0 {
		t.Fatalf("backoff claimed the job %d %v", n, err)
	}
	attempts, _, _ = jobState(t, d.App, tenantID, liveID)
	if attempts != 1 {
		t.Fatalf("backoff attempts %d", attempts)
	}

	retry := NewWorker(d.App, fixedProvider{model: "test-model", err: errors.New("boom")}, Options{ImmediateRetry: true, MaxAttempts: 2})
	if _, err := retry.ProcessOnce(ctx); err == nil {
		t.Fatal("expected second failure")
	}
	attempts, status, _ = jobState(t, d.App, tenantID, liveID)
	if attempts != 2 || status != "failed" {
		t.Fatalf("capped %d %s", attempts, status)
	}
	n, err = retry.ProcessOnce(ctx)
	if err != nil || n != 0 {
		t.Fatalf("past cap %d %v", n, err)
	}
	attempts, _, _ = jobState(t, d.App, tenantID, liveID)
	if attempts != 2 {
		t.Fatalf("attempts moved after cap %d", attempts)
	}
	if err := db.InTenant(dbtest.Seed(ctx), d.App, tenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE nodes SET title = title || ' again' WHERE id = $1`, liveID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := retry.ProcessOnce(ctx); err == nil {
		t.Fatal("expected failure after content reset")
	}
	attempts, status, _ = jobState(t, d.App, tenantID, liveID)
	if attempts != 1 || status != "failed" {
		t.Fatalf("reset %d %s", attempts, status)
	}
	assertNoJob(t, d.App, tenantID, deadID)
	if countEmbeddings(t, d.App, tenantID) != 0 {
		t.Fatal("failed or deleted node stored a vector")
	}
	if countEvents(t, d.App, tenantID) != baseline {
		t.Fatal("failure wrote an event")
	}
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

type raceProvider struct {
	Provider
	pool   *pgxpool.Pool
	tenant string
	node   string
	title  string
}

func (p raceProvider) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	if err := db.InTenant(dbtest.Seed(ctx), p.pool, p.tenant, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE nodes SET title = $2 WHERE id = $1`, p.node, p.title)
		return err
	}); err != nil {
		return nil, err
	}
	return p.Provider.Embed(ctx, texts)
}

func unitVector() []float32 {
	v := make([]float32, Dimensions)
	v[0] = 1
	return v
}

func seedTenant(t *testing.T, d *dbtest.DB, id, slug string) {
	t.Helper()
	if _, err := d.Admin.Exec(t.Context(), `INSERT INTO tenants (id, slug, name) VALUES ($1, $2, $3)`, id, slug, slug); err != nil {
		t.Fatal(err)
	}
}

func insertNode(t *testing.T, pool *pgxpool.Pool, tenant, id, key, title, body, state string) {
	t.Helper()
	err := db.InTenant(dbtest.Seed(t.Context()), pool, tenant, func(tx pgx.Tx) error {
		tag, err := tx.Exec(t.Context(), `
			INSERT INTO nodes (tenant_id, id, key, kind_id, title, body, state)
			SELECT $1, $2, $3, k.id, $4, $5, $6
			FROM node_kinds k WHERE k.slug = 'project'`,
			tenant, id, key, title, body, state)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return errors.New("project kind missing")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func assertEmbedding(t *testing.T, pool *pgxpool.Pool, tenant, node, model, hash string) {
	t.Helper()
	var gotModel, gotHash string
	var dims int
	err := db.InTenant(dbtest.Seed(t.Context()), pool, tenant, func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), `
			SELECT model, content_hash, vector_dims(embedding::vector)
			FROM node_embeddings WHERE node_id = $1`, node).Scan(&gotModel, &gotHash, &dims)
	})
	if err != nil {
		t.Fatal(err)
	}
	if gotModel != model || gotHash != hash || dims != Dimensions {
		t.Fatalf("embedding %s %s %d", gotModel, gotHash, dims)
	}
}

func assertEvent(t *testing.T, pool *pgxpool.Pool, tenant, node string, beforeNull bool, beforeHash, afterHash string) {
	t.Helper()
	var beforeEmpty bool
	var gotBefore, gotAfter string
	var actor string
	err := db.InTenant(dbtest.Seed(t.Context()), pool, tenant, func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), `
			SELECT actor_principal_id::text, before IS NULL,
			       coalesce(before->>'content_hash', ''), after->>'content_hash'
			FROM events
			WHERE node_id = $1 AND type = 'node.embedded'
			ORDER BY id DESC LIMIT 1`, node).Scan(&actor, &beforeEmpty, &gotBefore, &gotAfter)
	})
	if err != nil {
		t.Fatal(err)
	}
	if beforeEmpty != beforeNull || gotBefore != beforeHash || gotAfter != afterHash || actor == "" {
		t.Fatalf("event beforeNull=%v %q -> %q actor %q", beforeEmpty, gotBefore, gotAfter, actor)
	}
}

func assertNoJob(t *testing.T, pool *pgxpool.Pool, tenant, node string) {
	t.Helper()
	var n int
	if err := db.InTenant(dbtest.Seed(t.Context()), pool, tenant, func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), `SELECT count(*) FROM node_embedding_jobs WHERE node_id = $1`, node).Scan(&n)
	}); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("job remains %d", n)
	}
}

func countEvents(t *testing.T, pool *pgxpool.Pool, tenant string) int {
	t.Helper()
	var n int
	if err := db.InTenant(dbtest.Seed(t.Context()), pool, tenant, func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), `SELECT count(*) FROM events`).Scan(&n)
	}); err != nil {
		t.Fatal(err)
	}
	return n
}

func countEmbeddings(t *testing.T, pool *pgxpool.Pool, tenant string) int {
	t.Helper()
	var n int
	if err := db.InTenant(dbtest.Seed(t.Context()), pool, tenant, func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), `SELECT count(*) FROM node_embeddings`).Scan(&n)
	}); err != nil {
		t.Fatal(err)
	}
	return n
}

func jobState(t *testing.T, pool *pgxpool.Pool, tenant, node string) (int, string, string) {
	t.Helper()
	var attempts int
	var status, last string
	err := db.InTenant(dbtest.Seed(t.Context()), pool, tenant, func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), `
			SELECT attempts, status, coalesce(last_error, '')
			FROM node_embedding_jobs WHERE node_id = $1`, node).Scan(&attempts, &status, &last)
	})
	if err != nil {
		t.Fatal(err)
	}
	return attempts, status, last
}

func visible(t *testing.T, pool *pgxpool.Pool, tenant, q string, p fixedProvider) int {
	t.Helper()
	var n int
	err := db.InTenant(dbtest.Seed(t.Context()), pool, tenant, func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), `
			SELECT count(*) FROM aeon_search_nodes($1, $2::real[]::halfvec(1536), $3)`,
			q, pgtype.FlatArray[float32](p.vec), p.model).Scan(&n)
	})
	if err != nil {
		t.Fatal(err)
	}
	return n
}
