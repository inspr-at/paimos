// SPDX-License-Identifier: AGPL-3.0-only

package usagedashboard

import (
	"context"
	"sort"

	"github.com/jackc/pgx/v5"

	"github.com/inspr-at/paimos/internal/deliveryvote"
)

const votesTable = "agent_delivery_votes"

type voteStat struct {
	rows, sum, scored int
}

type rateAcc struct {
	sessions               map[string]struct{}
	votes, sum, scored     int
	exceptions, deliveries int
}

func emptyRatings() Ratings {
	return Ratings{ByModel: []RatingGroup{}, ByHarness: []RatingGroup{}}
}

func deliveredSessions(rows []sessionRow) []sessionRow {
	out := make([]sessionRow, 0, len(rows))
	for _, row := range rows {
		if row.delivered {
			out = append(out, row)
		}
	}
	return out
}

func sessionIDs(rows []sessionRow) []string {
	seen := map[string]struct{}{}
	ids := []string{}
	for _, row := range rows {
		if _, ok := seen[row.id]; ok {
			continue
		}
		seen[row.id] = struct{}{}
		ids = append(ids, row.id)
	}
	return ids
}

// loadVoteRatings counts rework exceptions against stopped deliveries.
// Phase stopped is the delivery, whether the stop reason is completed or
// anything else. A session that is still starting, working, yielded or
// stopping is not in the denominator, and a vote on it is not an exception.
// An exception is a stopped session with at least one vote row. The rate is
// exceptions ÷ deliveries. Model and harness labels match the usage
// breakdown, so the rate sits on the same row. The model stored on the vote
// is a snapshot for the audit trail; it does not choose the group. A missing
// vote table leaves exception counts at zero and keeps the delivery total.
// Token and list-price totals are not touched here.
func loadVoteRatings(ctx context.Context, tx pgx.Tx, rows []sessionRow) (Ratings, error) {
	rows = deliveredSessions(rows)
	ids := sessionIDs(rows)
	out := emptyRatings()
	out.Deliveries = len(ids)
	if len(ids) == 0 {
		return out, nil
	}
	var present bool
	if err := tx.QueryRow(ctx, `SELECT to_regclass($1) IS NOT NULL`, votesTable).Scan(&present); err != nil {
		return Ratings{}, err
	}
	if !present {
		return out, nil
	}
	stats, err := loadVoteStats(ctx, tx, ids)
	if err != nil {
		return Ratings{}, err
	}
	return buildRework(rows, stats), nil
}

func loadVoteStats(ctx context.Context, tx pgx.Tx, ids []string) (map[string]voteStat, error) {
	rows, err := tx.Query(ctx, `
		SELECT session_id::text, score
		  FROM agent_delivery_votes
		 WHERE session_id = ANY($1::uuid[])`, ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]voteStat{}
	for rows.Next() {
		var sessionID string
		var score *int
		if err := rows.Scan(&sessionID, &score); err != nil {
			return nil, err
		}
		stat := out[sessionID]
		stat.rows++
		if score != nil {
			stat.sum += *score
			stat.scored++
		}
		out[sessionID] = stat
	}
	return out, rows.Err()
}

func buildRework(rows []sessionRow, stats map[string]voteStat) Ratings {
	seen := map[string]struct{}{}
	total := rateAcc{}
	models, harnesses := map[string]*rateAcc{}, map[string]*rateAcc{}
	for _, row := range rows {
		stat := stats[row.id]
		voted := stat.rows > 0
		if _, ok := seen[row.id]; !ok {
			seen[row.id] = struct{}{}
			total.deliveries++
			if voted {
				total.exceptions++
				total.votes += stat.rows
				total.sum += stat.sum
				total.scored += stat.scored
			}
		}
		addRate(models, deliveryModelLabel(row), row.id, stat, voted)
		addRate(harnesses, deliveryHarnessLabel(row), row.id, stat, voted)
	}
	return Ratings{
		Votes:      total.votes,
		Average:    deliveryvote.FormatAverage(total.sum, total.scored),
		Exceptions: total.exceptions,
		Deliveries: total.deliveries,
		ReworkRate: deliveryvote.FormatReworkRate(total.exceptions, total.deliveries),
		ByModel:    rateGroups(models),
		ByHarness:  rateGroups(harnesses),
	}
}

func addRate(set map[string]*rateAcc, label, session string, stat voteStat, voted bool) {
	bucket := set[label]
	if bucket == nil {
		bucket = &rateAcc{sessions: map[string]struct{}{}}
		set[label] = bucket
	}
	if _, ok := bucket.sessions[session]; ok {
		return
	}
	bucket.sessions[session] = struct{}{}
	bucket.deliveries++
	if !voted {
		return
	}
	bucket.exceptions++
	bucket.votes += stat.rows
	bucket.sum += stat.sum
	bucket.scored += stat.scored
}

func rateGroups(set map[string]*rateAcc) []RatingGroup {
	out := make([]RatingGroup, 0, len(set))
	for label, bucket := range set {
		if bucket.exceptions == 0 {
			continue
		}
		out = append(out, RatingGroup{
			Label:      label,
			Votes:      bucket.votes,
			Average:    deliveryvote.FormatAverage(bucket.sum, bucket.scored),
			Exceptions: bucket.exceptions,
			Deliveries: bucket.deliveries,
			ReworkRate: deliveryvote.FormatReworkRate(bucket.exceptions, bucket.deliveries),
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Exceptions != out[j].Exceptions {
			return out[i].Exceptions > out[j].Exceptions
		}
		if out[i].Deliveries != out[j].Deliveries {
			return out[i].Deliveries > out[j].Deliveries
		}
		return out[i].Label < out[j].Label
	})
	return out
}
