// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package agentmode

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/inspr-at/paimos/backend/delivery"
	"github.com/inspr-at/paimos/backend/deliverytrust"
)

// BoundedTrustFact is the minimal, value-free delivery-trust projection used
// by adjacent read models. It deliberately omits actors, evidence, stage
// details, and delivery metadata.
type BoundedTrustFact struct {
	ProgressTrusted bool
	ETATrusted      bool
	Suppression     string
	TrustRevision   string
	ObservedAt      time.Time
	Progress        *int
	ETA             *time.Time
}

// LoadBoundedTrust evaluates delivery trust for an already-authorized,
// bounded issue set inside the caller's pinned read transaction. Callers must
// establish authorization before supplying issueIDs. Missing issue rows are
// omitted rather than turned into guessed trust.
func LoadBoundedTrust(ctx context.Context, database *sql.DB, tx *sql.Tx, issueIDs []int64,
	calculatedAt time.Time, freshness delivery.FreshnessPolicy,
) (map[int64]BoundedTrustFact, error) {
	if database == nil || tx == nil || calculatedAt.IsZero() || len(issueIDs) > 100 {
		return nil, ErrInvalid
	}
	unique := make(map[int64]struct{}, len(issueIDs))
	for _, issueID := range issueIDs {
		if issueID <= 0 {
			return nil, ErrInvalid
		}
		unique[issueID] = struct{}{}
	}
	ordered := make([]int64, 0, len(unique))
	for issueID := range unique {
		ordered = append(ordered, issueID)
	}
	sort.Slice(ordered, func(i, j int) bool { return ordered[i] < ordered[j] })
	if len(ordered) == 0 {
		return map[int64]BoundedTrustFact{}, nil
	}

	query := `SELECT id,project_id FROM issues WHERE id IN (` +
		strings.TrimRight(strings.Repeat("?,", len(ordered)), ",") + `) ORDER BY id`
	rows, err := tx.QueryContext(ctx, query, anyArgs(ordered)...)
	if err != nil {
		return nil, err
	}
	entries := make([]catalogEntry, 0, len(ordered))
	for rows.Next() {
		var entry catalogEntry
		if err := rows.Scan(&entry.IssueID, &entry.ProjectID); err != nil {
			rows.Close()
			return nil, err
		}
		entries = append(entries, entry)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(entries) == 0 {
		return map[int64]BoundedTrustFact{}, nil
	}

	selected := make([]int64, len(entries))
	for index := range entries {
		selected[index] = entries[index].IssueID
	}
	store := delivery.NewStore(database, delivery.Options{
		Clock:     delivery.ClockFunc(func() time.Time { return calculatedAt }),
		Freshness: freshness,
	})
	snapshots, err := store.BulkSnapshotsTx(ctx, tx, selected)
	if err != nil {
		return nil, err
	}
	facts, err := loadTrustFacts(ctx, tx, selected)
	if err != nil {
		return nil, err
	}
	history, err := loadDurationHistory(ctx, tx, entries)
	if err != nil {
		return nil, err
	}
	if len(snapshots) != len(entries) {
		return nil, fmt.Errorf("%w: bounded trust cardinality differs", ErrInvariant)
	}

	result := make(map[int64]BoundedTrustFact, len(entries))
	for index, entry := range entries {
		snapshot := snapshots[index]
		if snapshot.IssueID != entry.IssueID {
			return nil, fmt.Errorf("%w: bounded trust identity differs", ErrInvariant)
		}
		deliveryIdentity := "delivery:" + snapshot.DeliveryKey
		if snapshot.DeliveryID != nil {
			deliveryIdentity = fmt.Sprintf("delivery:%d", *snapshot.DeliveryID)
		}
		input, err := buildTrustInput(entry, snapshot, facts[entry.IssueID], history, calculatedAt, deliveryIdentity)
		if err != nil {
			return nil, fmt.Errorf("issue %d: %w", entry.IssueID, err)
		}
		output, err := deliverytrust.Evaluate(input)
		if err != nil {
			return nil, fmt.Errorf("%w: bounded delivery trust input: %v", ErrInvariant, err)
		}
		progress := publicProgress(output)
		eta := publicETA(output, calculatedAt)
		fact := BoundedTrustFact{Suppression: string(output.Suppression), TrustRevision: output.TrustRevision,
			ObservedAt: calculatedAt}
		if progress != nil && progress.Trusted {
			fact.ProgressTrusted, fact.Progress = true, progress.Percent
		}
		if eta != nil && eta.Trusted {
			fact.ETATrusted, fact.ETA = true, eta.LandingAt
		}
		result[entry.IssueID] = fact
	}
	return result, nil
}
