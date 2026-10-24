// SPDX-License-Identifier: AGPL-3.0-only

package outcomes

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"regexp"
	"sort"
	"time"

	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/events"
	"github.com/inspr-at/paimos/internal/httpapi"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

// ReviewRoundRecord is an observation, never an approval or delivery credential.
type ReviewRoundRecord struct {
	Ticket       string    `json:"ticket"`
	Kind         string    `json:"kind"`
	Round        int       `json:"round"`
	Verdict      string    `json:"verdict"`
	CitedRules   []string  `json:"cited_rules"`
	At           time.Time `json:"at"`
	ReviewerRole string    `json:"reviewer_role"`
	Artifact     string    `json:"artifact"`
	ReviewRunID  string    `json:"review_run_id,omitempty"`
	Source       string    `json:"source"`
}

type reviewRoundOutcome struct {
	ID           string            `json:"id"`
	EventKind    string            `json:"event_kind"`
	TicketNodeID string            `json:"ticket_node_id"`
	ProjectID    string            `json:"project_id"`
	RecordedAt   time.Time         `json:"recorded_at"`
	Review       ReviewRoundRecord `json:"review"`
}

var lensRuleID = regexp.MustCompile(`^[UA][1-9][0-9]{0,2}$`)

func (in *ReviewRoundRecord) validate(now time.Time) error {
	var err error
	if in.Ticket, err = plain("ticket", in.Ticket, 128, true); err != nil {
		return err
	}
	if in.Kind != "gate" && in.Kind != "design" || in.Round < 1 || in.Round > 999 || in.Verdict != "ok" && in.Verdict != "changes" {
		return invalid("kind, round or verdict is invalid")
	}
	if in.CitedRules == nil || len(in.CitedRules) > 32 {
		return invalid("cited_rules must be an array of at most 32 rule IDs")
	}
	seen := make(map[string]bool, len(in.CitedRules))
	for _, rule := range in.CitedRules {
		if !lensRuleID.MatchString(rule) || seen[rule] {
			return invalid("cited_rules must contain distinct lens rule IDs")
		}
		seen[rule] = true
	}
	sort.Strings(in.CitedRules)
	if in.At.IsZero() || in.At.Year() < 1970 || in.At.After(now) {
		return invalid("at must be an observation timestamp from 1970 through now")
	}
	in.At = in.At.UTC().Truncate(time.Microsecond)
	if in.ReviewerRole != "reviewer" && in.ReviewerRole != "design_reviewer" && in.ReviewerRole != "gate_reviewer" {
		return invalid("reviewer_role must be a reviewer role, never a name")
	}
	if in.Artifact, err = plain("artifact", in.Artifact, 200, true); err != nil {
		return err
	}
	if in.ReviewRunID != "" && !validUUID(in.ReviewRunID) {
		return invalid("review_run_id must be a UUID")
	}
	if in.Source == "" {
		in.Source = "recorded"
	}
	if in.Source != "recorded" && in.Source != "imported" || in.Source == "imported" && in.Kind != "design" {
		return invalid("source must be recorded or an imported design observation")
	}
	return nil
}

func (m *Module) recordReviewRound(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	key, err := cleanIdempotencyKey(r.Header.Get("Idempotency-Key"))
	if err != nil {
		writeErr(w, err)
		return
	}
	var in ReviewRoundRecord
	dec := httpapi.NewRequestDecoder(w, r, 8192)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&in); err != nil || dec.Decode(new(any)) != io.EOF {
		writeErr(w, invalid("invalid review round"))
		return
	}
	if err := in.validate(time.Now()); err != nil {
		writeErr(w, err)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	var out reviewRoundOutcome
	created := false
	err = db.InTenant(ctx, m.pool, p.TenantID, func(tx pgx.Tx) error {
		// Tenant -> tree -> current target and authority -> outcome -> event counter.
		if err := db.LockTree(ctx, tx, p.TenantID); err != nil {
			return err
		}
		node, err := resolveTicket(ctx, tx, in.Ticket)
		if err != nil {
			return err
		}
		if err := authz.RequireTx(ctx, tx, p, "outcome.write", authz.Scope{ProjectID: node.projectID}); err != nil {
			return err
		}
		if err := authorizeReviewRoundTx(ctx, tx, p, node, in); err != nil {
			return err
		}
		in.Ticket = node.id // keys and UUIDs identify the same immutable request.
		raw, err := json.Marshal(in)
		if err != nil {
			return err
		}
		digest := sha256.Sum256(raw)
		rows, err := tx.Query(ctx, `SELECT id::text,request_digest,recorded_at FROM outcome_review_rounds
 WHERE idempotency_key=$1 OR (ticket_node_id=$2 AND kind=$3 AND round=$4) OR review_run_id=$5::uuid LIMIT 2`, key, node.id, in.Kind, in.Round, nilUUID(in.ReviewRunID))
		if err != nil {
			return err
		}
		out = reviewRoundOutcome{EventKind: "review_round", TicketNodeID: node.id, ProjectID: node.projectID, Review: in}
		count := 0
		matches := true
		for rows.Next() {
			var old []byte
			if err := rows.Scan(&out.ID, &old, &out.RecordedAt); err != nil {
				rows.Close()
				return err
			}
			count++
			matches = matches && bytes.Equal(old, digest[:])
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		if count > 0 {
			if count != 1 || !matches {
				return &fail{status: 409, msg: "review round or idempotency key already has a different observation"}
			}
			return nil
		}
		err = tx.QueryRow(ctx, `INSERT INTO outcome_review_rounds
 (tenant_id,project_id,ticket_node_id,kind,round,verdict,cited_rules,at,reviewer_role,artifact,review_run_id,source,actor_principal_id,idempotency_key,request_digest)
 VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15) RETURNING id::text,recorded_at`,
			p.TenantID, node.projectID, node.id, in.Kind, in.Round, in.Verdict, in.CitedRules, in.At, in.ReviewerRole, in.Artifact, nilUUID(in.ReviewRunID), in.Source, p.ID, key, digest[:]).Scan(&out.ID, &out.RecordedAt)
		if err != nil {
			return err
		}
		created = true
		_, err = events.Append(ctx, tx, p, events.Change{NodeID: &node.id, Type: "outcome.review_round", After: out})
		return err
	})
	if err != nil {
		writeErr(w, err)
		return
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	w.Header().Set("Cache-Control", "no-store")
	httpapi.WriteJSON(w, status, out)
}

func authorizeReviewRoundTx(ctx context.Context, tx pgx.Tx, p tenant.Principal, node ticketRef, in ReviewRoundRecord) error {
	if p.Kind == tenant.Person {
		return nil
	}
	if p.Kind != tenant.Agent || in.Source == "imported" || in.ReviewRunID == "" {
		return &fail{status: 403, msg: "agents may record only their own submitted ticket reviews"}
	}
	runKind := "review"
	if in.Kind == "design" {
		runKind = "visual"
	}
	var matches bool
	err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM review_runs
 WHERE id=$1 AND ticket_node_id=$2 AND project_id=$3 AND reviewer_principal_id=$4
 AND kind=$5 AND head_sha=$6 AND verdict=$7 AND submitted_at IS NOT NULL
 AND NOT EXISTS(SELECT 1 FROM nodes child WHERE child.parent_id=$2 AND child.deleted_at IS NULL
   AND NOT EXISTS(SELECT 1 FROM work_orders wo WHERE wo.node_id=child.id)))`,
		in.ReviewRunID, node.id, node.projectID, p.ID, runKind, in.Artifact, in.Verdict).Scan(&matches)
	if err != nil {
		return err
	}
	if !matches {
		return &fail{status: 403, msg: "agents may record only their own submitted ticket reviews"}
	}
	return nil
}

func reviewReadError(w http.ResponseWriter, err error) {
	if db.IsStatementTimeout(err) || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		err = &fail{status: 503, msg: "review round read deadline exceeded; retry"}
	}
	writeErr(w, err)
}
