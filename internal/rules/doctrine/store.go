// SPDX-License-Identifier: AGPL-3.0-only

package doctrine

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"
)

// MaxSources bounds the doctrine repositories one workspace indexes.
const MaxSources = 8

// Source is one configured doctrine repository and its pin. CredentialRef
// names a read-only credential; the credential itself is never stored.
type Source struct {
	ID            string
	Repository    string
	Visibility    string
	Ref           string
	Commit        string
	CommittedAt   *time.Time
	PinnedAt      time.Time
	Paths         []string
	CredentialRef string
	IndexedAt     *time.Time
	IndexError    string
	Skipped       []Skip
}

// audit is what an event records about a source: configuration only, never
// doctrine text and never a credential.
func (s Source) audit() map[string]any {
	return map[string]any{
		"source_id": s.ID, "repository": s.Repository, "visibility": s.Visibility, "ref": s.Ref,
		"commit": s.Commit, "paths": s.Paths, "credential_ref": s.CredentialRef,
	}
}

const sourceColumns = `id::text, repository, visibility, ref, commit_sha, committed_at, pinned_at, paths, credential_ref, indexed_at, index_error, skipped`

func scanSource(row pgx.Row) (Source, error) {
	var s Source
	var skipped []byte
	err := row.Scan(&s.ID, &s.Repository, &s.Visibility, &s.Ref, &s.Commit, &s.CommittedAt, &s.PinnedAt, &s.Paths, &s.CredentialRef, &s.IndexedAt, &s.IndexError, &skipped)
	if err != nil {
		return Source{}, err
	}
	if err := json.Unmarshal(skipped, &s.Skipped); err != nil {
		return Source{}, err
	}
	return s, nil
}

func listSources(ctx context.Context, tx pgx.Tx) ([]Source, error) {
	rows, err := tx.Query(ctx, `SELECT `+sourceColumns+` FROM doctrine_sources ORDER BY visibility DESC, repository`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Source
	for rows.Next() {
		s, err := scanSource(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

var errNoSource = errors.New("no such doctrine source")

func getSource(ctx context.Context, tx pgx.Tx, id string, lock bool) (Source, error) {
	query := `SELECT ` + sourceColumns + ` FROM doctrine_sources WHERE id=$1`
	if lock {
		query += ` FOR UPDATE`
	}
	s, err := scanSource(tx.QueryRow(ctx, query, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return Source{}, errNoSource
	}
	return s, err
}

func insertSource(ctx context.Context, tx pgx.Tx, tenantID string, s Source) (Source, error) {
	return scanSource(tx.QueryRow(ctx, `
		INSERT INTO doctrine_sources(tenant_id, repository, visibility, ref, commit_sha, committed_at, paths, credential_ref)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8)
		RETURNING `+sourceColumns, tenantID, s.Repository, s.Visibility, s.Ref, s.Commit, s.CommittedAt, s.Paths, s.CredentialRef))
}

// updateSource replaces the pin and selection. A new commit restarts
// "pinned since" and clears the index; the database drops the old commit's
// cached bytes in the same statement. New paths drop the cache too.
func updateSource(ctx context.Context, tx pgx.Tx, old, s Source) (Source, error) {
	if !slices.Equal(old.Paths, s.Paths) {
		if _, err := tx.Exec(ctx, `DELETE FROM doctrine_cache WHERE source_id=$1`, s.ID); err != nil {
			return Source{}, err
		}
	}
	return scanSource(tx.QueryRow(ctx, `
		UPDATE doctrine_sources SET
			visibility=$2, ref=$3, committed_at=$5, paths=$6, credential_ref=$7,
			pinned_at = CASE WHEN commit_sha=$4 THEN pinned_at ELSE clock_timestamp() END,
			indexed_at = CASE WHEN commit_sha=$4 AND paths=$6 THEN indexed_at END,
			index_error = CASE WHEN commit_sha=$4 AND paths=$6 THEN index_error ELSE '' END,
			skipped = CASE WHEN commit_sha=$4 AND paths=$6 THEN skipped ELSE '[]' END,
			commit_sha=$4, updated_at=clock_timestamp()
		WHERE id=$1
		RETURNING `+sourceColumns, s.ID, s.Visibility, s.Ref, s.Commit, s.CommittedAt, s.Paths, s.CredentialRef))
}

func deleteSource(ctx context.Context, tx pgx.Tx, id string) error {
	_, err := tx.Exec(ctx, `DELETE FROM doctrine_sources WHERE id=$1`, id)
	return err
}

// storeIndex replaces the cached bytes of s with files at s.Commit.
func storeIndex(ctx context.Context, tx pgx.Tx, tenantID string, s Source, files []File, skipped []Skip) error {
	if _, err := tx.Exec(ctx, `DELETE FROM doctrine_cache WHERE source_id=$1`, s.ID); err != nil {
		return err
	}
	for _, f := range files {
		if _, err := tx.Exec(ctx, `INSERT INTO doctrine_cache(tenant_id, source_id, commit_sha, path, blob_sha, content) VALUES ($1,$2,$3,$4,$5,$6)`,
			tenantID, s.ID, s.Commit, f.Path, f.BlobSHA, f.Content); err != nil {
			return err
		}
	}
	raw, err := json.Marshal(skipped)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `UPDATE doctrine_sources SET indexed_at=clock_timestamp(), index_error='', skipped=$2 WHERE id=$1`, s.ID, raw)
	return err
}

// recordIndexError keeps the files of an earlier successful index at this
// commit, if any, and records why the latest attempt failed.
func recordIndexError(ctx context.Context, tx pgx.Tx, id, message string) error {
	_, err := tx.Exec(ctx, `UPDATE doctrine_sources SET index_error=$2 WHERE id=$1`, id, message)
	return err
}

// storeGuardCorpus replaces the hash corpus for a private source. A nil corpus
// clears it (public sources have none). The bytes are hashes only.
func storeGuardCorpus(ctx context.Context, tx pgx.Tx, tenantID string, s Source, corpus []byte) error {
	if corpus == nil {
		_, err := tx.Exec(ctx, `DELETE FROM doctrine_private_guard WHERE source_id=$1`, s.ID)
		return err
	}
	_, err := tx.Exec(ctx, `INSERT INTO doctrine_private_guard(tenant_id, source_id, commit_sha, corpus) VALUES ($1,$2,$3,$4)
		ON CONFLICT (tenant_id, source_id) DO UPDATE SET commit_sha=EXCLUDED.commit_sha, corpus=EXCLUDED.corpus`,
		tenantID, s.ID, s.Commit, corpus)
	return err
}

func loadGuardCorpus(ctx context.Context, tx pgx.Tx, s Source) ([]byte, error) {
	var raw []byte
	err := tx.QueryRow(ctx, `SELECT corpus FROM doctrine_private_guard WHERE source_id=$1 AND commit_sha=$2`, s.ID, s.Commit).Scan(&raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return raw, err
}

func cachedFiles(ctx context.Context, tx pgx.Tx, s Source) ([]File, error) {
	rows, err := tx.Query(ctx, `SELECT path, blob_sha, content FROM doctrine_cache WHERE source_id=$1 AND commit_sha=$2 ORDER BY path`, s.ID, s.Commit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []File
	for rows.Next() {
		var f File
		if err := rows.Scan(&f.Path, &f.BlobSHA, &f.Content); err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}
