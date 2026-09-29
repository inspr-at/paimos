// SPDX-License-Identifier: AGPL-3.0-only

package usagedashboard

import (
	"context"
	"sort"

	"github.com/jackc/pgx/v5"

	"github.com/inspr-at/paimos/internal/deliveryvote"
)

const votesTable = "agent_delivery_votes"

type ratingAcc struct {
	votes, sum int
}

func emptyRatings() Ratings {
	return Ratings{ByModel: []RatingGroup{}, ByHarness: []RatingGroup{}}
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

// loadVoteRatings groups saved votes for the sessions already kept on the
// dashboard. The model and harness are the snapshot stored with the vote.
// A missing vote table leaves the usage totals in place and returns no ratings.
func loadVoteRatings(ctx context.Context, tx pgx.Tx, ids []string) (Ratings, error) {
	out := emptyRatings()
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
	rows, err := tx.Query(ctx, `
		SELECT coalesce(nullif(btrim(model), ''), 'Unreported'),
		       coalesce(nullif(btrim(harness), ''), 'Unreported'),
		       score
		  FROM agent_delivery_votes
		 WHERE session_id = ANY($1::uuid[])`, ids)
	if err != nil {
		return Ratings{}, err
	}
	defer rows.Close()
	models, harnesses := map[string]*ratingAcc{}, map[string]*ratingAcc{}
	total := ratingAcc{}
	for rows.Next() {
		var model, harness string
		var score int
		if err := rows.Scan(&model, &harness, &score); err != nil {
			return Ratings{}, err
		}
		total.votes++
		total.sum += score
		addRating(models, model, score)
		addRating(harnesses, harness, score)
	}
	if err := rows.Err(); err != nil {
		return Ratings{}, err
	}
	out.Votes = total.votes
	out.Average = deliveryvote.FormatAverage(total.sum, total.votes)
	out.ByModel = ratingGroups(models)
	out.ByHarness = ratingGroups(harnesses)
	return out, nil
}

func addRating(set map[string]*ratingAcc, label string, score int) {
	bucket := set[label]
	if bucket == nil {
		bucket = &ratingAcc{}
		set[label] = bucket
	}
	bucket.votes++
	bucket.sum += score
}

func ratingGroups(set map[string]*ratingAcc) []RatingGroup {
	out := make([]RatingGroup, 0, len(set))
	for label, bucket := range set {
		out = append(out, RatingGroup{Label: label, Votes: bucket.votes, Average: deliveryvote.FormatAverage(bucket.sum, bucket.votes)})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Votes != out[j].Votes {
			return out[i].Votes > out[j].Votes
		}
		return out[i].Label < out[j].Label
	})
	return out
}
