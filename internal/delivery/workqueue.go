// SPDX-License-Identifier: AGPL-3.0-only
package delivery

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/events"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/inspr-at/paimos/internal/workorders"
	"github.com/jackc/pgx/v5"
)

type RoundInput struct {
	Ticket   string `json:"ticket_node_id"`
	Slug     string `json:"slug"`
	Kind     string `json:"kind"`
	Number   int    `json:"round_number"`
	Brief    string `json:"brief_ref"`
	Base     string `json:"base_ref"`
	Estimate int    `json:"estimate_minutes"`
	PR       *int64 `json:"pull_request"`
}

type Round struct {
	RoundInput
	ID           string    `json:"id"`
	Project      string    `json:"project_id"`
	Key          string    `json:"key"`
	State        string    `json:"state"`
	Position     int64     `json:"position"`
	Hold         *string   `json:"hold_reason"`
	Reason       string    `json:"reason"`
	Claimant     *string   `json:"claimant_id"`
	ClaimRequest *string   `json:"claim_request_id"`
	Revision     int64     `json:"revision"`
	Updated      time.Time `json:"updated_at"`
}

type QueueSettings struct {
	Mode       string   `json:"mode"`
	Freeze     bool     `json:"freeze"`
	ReleaseSet []string `json:"release_set"`
	HeldSlugs  []string `json:"held_slugs"`
	HeldPRs    []int64  `json:"held_pull_requests"`
	Revision   int64    `json:"revision"`
}

func queueDefaults() QueueSettings {
	return QueueSettings{Mode: "off", ReleaseSet: []string{}, HeldSlugs: []string{}, HeldPRs: []int64{}}
}

type ClaimInput struct {
	Request     string  `json:"request_id"`
	ScriptRound *string `json:"script_round_id"`
}
type QueueDecision struct {
	Round  string `json:"round_id"`
	Action string `json:"action"`
	Reason string `json:"reason"`
}
type QueueClaim struct {
	Request     string          `json:"request_id"`
	Project     string          `json:"project_id"`
	Mode        string          `json:"mode"`
	Execute     bool            `json:"execute"`
	Reason      string          `json:"reason"`
	Round       *Round          `json:"round"`
	Decisions   []QueueDecision `json:"decisions"`
	ScanLimited bool            `json:"scan_limited"`
	ScriptRound *string         `json:"script_round_id"`
	Agreement   *bool           `json:"agreement"`
}

// QueueAdmission is the W2 admission integration point. Install once before
// serving. It must use this transaction, do no network I/O, append no events,
// and acquire no locks after the queue row locks. It evaluates current server
// facts, never a caller-supplied admission assertion. The queue defaults closed.
type QueueAdmission func(context.Context, pgx.Tx, tenant.Principal, Round) (allowed bool, reason string, err error)

func (m *Module) SetQueueAdmission(admit QueueAdmission) { m.queueAdmission = admit }

var queueSlug = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,119}$`)
var queueBase = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9/_.-]{0,199}$`)

func validateRound(in RoundInput) error {
	if !workorders.UUID(in.Ticket) || !queueSlug.MatchString(in.Slug) || !slices.Contains([]string{"first_build", "fix", "merge", "land"}, in.Kind) || in.Number < 1 || in.Number > 100 || in.Estimate < 1 || in.Estimate > 1440 || in.PR != nil && *in.PR < 1 {
		return fail(400, "invalid delivery round")
	}
	if in.Brief == "" || len(in.Brief) > 1024 || strings.HasPrefix(in.Brief, "/") || strings.Contains(in.Brief, "..") || strings.ContainsAny(in.Brief, "\\:\x00\r\n\t") || strings.TrimSpace(in.Brief) != in.Brief || !queueBase.MatchString(in.Base) || strings.Contains(in.Base, "..") || strings.HasSuffix(in.Base, "/") {
		return fail(400, "invalid brief or base reference")
	}
	return nil
}
func validateQueueSettings(in *QueueSettings) error {
	if in.Mode != "off" && in.Mode != "shadow" || in.Revision < 0 || len(in.ReleaseSet) > 100 || len(in.HeldSlugs) > 100 || len(in.HeldPRs) > 100 {
		return fail(400, "invalid queue settings")
	}
	for _, list := range [][]string{in.ReleaseSet, in.HeldSlugs} {
		seen := map[string]bool{}
		for _, slug := range list {
			if !queueSlug.MatchString(slug) || seen[slug] {
				return fail(400, "invalid queue slug list")
			}
			seen[slug] = true
		}
	}
	seen := map[int64]bool{}
	for _, pr := range in.HeldPRs {
		if pr < 1 || seen[pr] {
			return fail(400, "invalid held pull requests")
		}
		seen[pr] = true
	}
	if in.ReleaseSet == nil {
		in.ReleaseSet = []string{}
	}
	if in.HeldSlugs == nil {
		in.HeldSlugs = []string{}
	}
	if in.HeldPRs == nil {
		in.HeldPRs = []int64{}
	}
	return nil
}

func queueSettingsTx(ctx context.Context, tx pgx.Tx, project string) (QueueSettings, error) {
	out := queueDefaults()
	var raw []byte
	err := tx.QueryRow(ctx, `SELECT snapshot FROM delivery_work_settings WHERE project_id=$1`, project).Scan(&raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return out, nil
	}
	if err == nil {
		err = json.Unmarshal(raw, &out)
	}
	return out, err
}
func saveQueueSettingsTx(ctx context.Context, tx pgx.Tx, tid, project string, s QueueSettings) error {
	raw, err := json.Marshal(s)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO delivery_work_settings(tenant_id,project_id,snapshot) VALUES($1,$2,$3) ON CONFLICT(tenant_id,project_id) DO UPDATE SET snapshot=EXCLUDED.snapshot`, tid, project, raw)
	return err
}
func scanRound(row pgx.Row) (Round, error) {
	var out Round
	var raw []byte
	err := row.Scan(&raw)
	if err == nil {
		err = json.Unmarshal(raw, &out)
	}
	return out, err
}
func loadRoundTx(ctx context.Context, tx pgx.Tx, project, id string) (Round, error) {
	return scanRound(tx.QueryRow(ctx, `SELECT snapshot FROM delivery_work_rounds WHERE project_id=$1 AND id=$2 FOR NO KEY UPDATE`, project, id))
}
func saveRoundTx(ctx context.Context, tx pgx.Tx, tid string, r Round) error {
	raw, err := json.Marshal(r)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO delivery_work_rounds(tenant_id,project_id,id,ticket_node_id,slug,kind,round_number,state,position,snapshot)
	 VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10) ON CONFLICT(tenant_id,id) DO UPDATE SET state=EXCLUDED.state,position=EXCLUDED.position,snapshot=EXCLUDED.snapshot`, tid, r.Project, r.ID, r.Ticket, r.Slug, r.Kind, r.Number, r.State, r.Position, raw)
	return err
}
func nextPositionTx(ctx context.Context, tx pgx.Tx, project string) (int64, error) {
	var out int64
	err := tx.QueryRow(ctx, `SELECT coalesce((SELECT position FROM delivery_work_rounds WHERE project_id=$1 ORDER BY position DESC LIMIT 1),0)+1`, project).Scan(&out)
	return out, err
}

// A complete queue snapshot makes each projection replayable. The project
// node scopes its audit records even when the round's ticket later moves.
type queueSnapshot struct {
	Project  string         `json:"project_id"`
	Round    *Round         `json:"round,omitempty"`
	Settings *QueueSettings `json:"settings,omitempty"`
	Claim    *QueueClaim    `json:"claim,omitempty"`
	Claimant string         `json:"claimant_id,omitempty"`
}

func queueChange(project, kind string, before any, after queueSnapshot, at time.Time) events.Change {
	return events.Change{NodeID: &project, Type: "delivery.work_queue." + kind, Before: before, After: after, At: &at}
}
func appendQueueChanges(ctx context.Context, tx pgx.Tx, p tenant.Principal, changes []events.Change) error {
	// All resource writes finish before the event counter is acquired.
	for _, change := range changes {
		if _, err := events.Append(ctx, tx, p, change); err != nil {
			return err
		}
	}
	return nil
}
func currentRoundTargetTx(ctx context.Context, tx pgx.Tx, r Round) error {
	var leaf bool
	err := tx.QueryRow(ctx, `SELECT NOT EXISTS(SELECT 1 FROM nodes c WHERE c.parent_id=n.id AND c.deleted_at IS NULL)
	 FROM nodes n JOIN node_kinds k ON k.id=n.kind_id WHERE n.id=$1 AND n.project_id=$2 AND n.deleted_at IS NULL AND k.slug='work'
	 AND NOT EXISTS(SELECT 1 FROM work_orders w WHERE w.node_id=n.id)`, r.Ticket, r.Project).Scan(&leaf)
	if err != nil {
		return err
	}
	if !leaf {
		return fail(409, "round target is not a leaf work item")
	}
	return nil
}

func saveClaimTx(ctx context.Context, tx pgx.Tx, tid, claimant string, out QueueClaim) error {
	raw, err := json.Marshal(out)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO delivery_work_claims(tenant_id,project_id,request_id,claimant_id,snapshot) VALUES($1,$2,$3,$4,$5)`, tid, out.Project, out.Request, claimant, raw)
	return err
}
func (m *Module) claimQueueTx(ctx context.Context, tx pgx.Tx, p tenant.Principal, project string, in ClaimInput) (QueueClaim, error) {
	var old []byte
	var claimant string
	err := tx.QueryRow(ctx, `SELECT claimant_id::text,snapshot FROM delivery_work_claims WHERE project_id=$1 AND request_id=$2`, project, in.Request).Scan(&claimant, &old)
	if err == nil {
		var out QueueClaim
		if err = json.Unmarshal(old, &out); err != nil {
			return out, err
		}
		if claimant != p.ID || !reflect.DeepEqual(out.ScriptRound, in.ScriptRound) {
			return out, fail(409, "claim request already belongs to another input or principal")
		}
		return out, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return QueueClaim{}, err
	}
	s, err := queueSettingsTx(ctx, tx, project)
	if err != nil {
		return QueueClaim{}, err
	}
	out := QueueClaim{Request: in.Request, Project: project, Mode: s.Mode, Reason: "queue_empty", Decisions: []QueueDecision{}, ScriptRound: in.ScriptRound}
	changes := []events.Change{}
	if s.Mode == "off" {
		out.Reason = "queue_off"
	} else {
		rows, err := tx.Query(ctx, `SELECT snapshot FROM delivery_work_rounds WHERE project_id=$1 AND state='queued' ORDER BY position LIMIT 101 FOR NO KEY UPDATE`, project)
		if err != nil {
			return out, err
		}
		rounds := []Round{}
		for rows.Next() {
			r, err := scanRound(rows)
			if err != nil {
				rows.Close()
				return out, err
			}
			rounds = append(rounds, r)
		}
		rows.Close()
		if err = rows.Err(); err != nil {
			return out, err
		}
		out.ScanLimited = len(rounds) > 100
		if out.ScanLimited {
			rounds = rounds[:100]
		}
		for _, r := range rounds {
			before := r
			action, reason := "rotate", ""
			inset := !s.Freeze
			for _, prefix := range s.ReleaseSet {
				if strings.HasPrefix(r.Slug, prefix) {
					inset = true
					break
				}
			}
			switch {
			case !inset:
				action, reason = "park", "release_freeze"
			case r.Hold != nil:
				reason = "round_hold"
			case slices.Contains(s.HeldSlugs, r.Slug):
				reason = "slug_hold"
			case r.PR != nil && slices.Contains(s.HeldPRs, *r.PR):
				reason = "pull_request_hold"
			default:
				if err = currentRoundTargetTx(ctx, tx, r); errors.Is(err, pgx.ErrNoRows) {
					action, reason = "park", "target_unavailable"
				} else if err != nil {
					var e *apiError
					if errors.As(err, &e) && e.status == 409 {
						action, reason = "park", "target_not_leaf"
					} else {
						return out, err
					}
				}
				if reason == "" {
					var active, merged, held bool
					if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM delivery_work_rounds WHERE project_id=$1 AND slug=$2 AND state IN ('claimed','running')),
					 (EXISTS(SELECT 1 FROM delivery_items WHERE project_id=$1 AND (ticket_node_id=$3 OR ($4::bigint IS NOT NULL AND pull_request=$4)) AND state='merged')
					 AND NOT EXISTS(SELECT 1 FROM delivery_items WHERE project_id=$1 AND (ticket_node_id=$3 OR ($4::bigint IS NOT NULL AND pull_request=$4)) AND pull_request IS NOT NULL AND state<>'merged')),
					 EXISTS(SELECT 1 FROM delivery_items WHERE project_id=$1 AND (ticket_node_id=$3 OR ($4::bigint IS NOT NULL AND pull_request=$4)) AND state='held')`, project, r.Slug, r.Ticket, r.PR).Scan(&active, &merged, &held); err != nil {
						return out, err
					}
					switch {
					case active:
						reason = "slug_running"
					case merged && (r.Kind == "fix" || r.Kind == "merge"):
						action, reason = "park", "already_merged"
					case held:
						reason = "delivery_hold"
					case m.queueAdmission == nil:
						reason = "admission_unavailable"
					default:
						allowed, why, e := m.queueAdmission(ctx, tx, p, r)
						if e != nil {
							return out, e
						}
						if why == "" || len(why) > 200 || strings.ContainsAny(why, "\x00\r\n") {
							return out, fmt.Errorf("invalid admission reason")
						}
						reason = why
						if allowed {
							action = "claim"
						}
					}
				}
			}
			if action == "rotate" {
				r.Position, err = nextPositionTx(ctx, tx, project)
				if err != nil {
					return out, err
				}
			} else if action == "park" {
				r.State = "parked"
			} else {
				r.State = "claimed"
				r.Claimant = &p.ID
				r.ClaimRequest = &in.Request
			}
			r.Reason = reason
			r.Revision++
			r.Updated = m.now()
			if err = saveRoundTx(ctx, tx, p.TenantID, r); err != nil {
				return out, err
			}
			out.Decisions = append(out.Decisions, QueueDecision{Round: r.ID, Action: action, Reason: reason})
			changes = append(changes, queueChange(project, action, queueSnapshot{Project: project, Round: &before}, queueSnapshot{Project: project, Round: &r}, r.Updated))
			if action == "claim" {
				out.Round = &r
				out.Reason = reason
				break
			}
		}
		if out.Round == nil && len(out.Decisions) > 0 {
			out.Reason = "no_startable_round"
			if out.ScanLimited {
				out.Reason = "scan_limit"
			}
		}
	}
	if in.ScriptRound != nil {
		agree := out.Round != nil && out.Round.ID == *in.ScriptRound
		out.Agreement = &agree
	}
	if err = saveClaimTx(ctx, tx, p.TenantID, p.ID, out); err != nil {
		return out, err
	}
	changes = append(changes, queueChange(project, "decision", nil, queueSnapshot{Project: project, Claim: &out, Claimant: p.ID}, m.now()))
	return out, appendQueueChanges(ctx, tx, p, changes)
}

// RebuildWorkQueue restores projections and idempotency receipts from complete
// events. The caller needs management authority on this project. Overflow is
// an explicit failure, before any projection is changed.
func (m *Module) RebuildWorkQueue(ctx context.Context, p tenant.Principal, project string) error {
	ctx, cancel := context.WithTimeout(tenant.WithPrincipal(ctx, p), 30*time.Second)
	defer cancel()
	// Replay must see all actors' snapshots after checking this project's authority.
	service := db.AllProjects(ctx, "delivery work queue replay of explicitly authorized project")
	return db.InTenant(service, m.pool, p.TenantID, func(tx pgx.Tx) error {
		if err := db.LockTenant(ctx, tx, p.TenantID); err != nil {
			return err
		}
		if _, err := projectSettings(ctx, tx, project); err != nil {
			return err
		}
		if err := authz.RequireTx(ctx, tx, p, "delivery_queue.manage", authz.Scope{ProjectID: project}); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `SELECT after FROM events WHERE node_id=$1 AND type IN ('delivery.work_queue.enqueued','delivery.work_queue.settings','delivery.work_queue.rotate','delivery.work_queue.park','delivery.work_queue.claim','delivery.work_queue.decision','delivery.work_queue.transition') ORDER BY id LIMIT 10001`, project)
		if err != nil {
			return err
		}
		items := []queueSnapshot{}
		bytes := 0
		for rows.Next() {
			var raw []byte
			var snap queueSnapshot
			if err = rows.Scan(&raw); err == nil {
				bytes += len(raw)
				if bytes > 32<<20 {
					rows.Close()
					return fail(409, "queue replay byte limit exceeded")
				}
				err = json.Unmarshal(raw, &snap)
			}
			if err != nil {
				rows.Close()
				return err
			}
			items = append(items, snap)
		}
		rows.Close()
		if err = rows.Err(); err != nil {
			return err
		}
		if len(items) > 10000 {
			return fail(409, "queue replay limit exceeded")
		}
		for _, table := range []string{"delivery_work_claims", "delivery_work_rounds", "delivery_work_settings"} {
			if _, err = tx.Exec(ctx, `DELETE FROM `+table+` WHERE project_id=$1`, project); err != nil {
				return err
			}
		}
		for _, snap := range items {
			if snap.Project != project {
				return fmt.Errorf("queue replay project mismatch")
			}
			if snap.Settings != nil {
				err = saveQueueSettingsTx(ctx, tx, p.TenantID, project, *snap.Settings)
			}
			if snap.Round != nil {
				err = saveRoundTx(ctx, tx, p.TenantID, *snap.Round)
			}
			if snap.Claim != nil {
				err = saveClaimTx(ctx, tx, p.TenantID, snap.Claimant, *snap.Claim)
			}
			if err != nil {
				return err
			}
		}
		return appendQueueChanges(ctx, tx, p, []events.Change{queueChange(project, "rebuilt", nil, queueSnapshot{Project: project}, m.now())})
	})
}
