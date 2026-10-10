// SPDX-License-Identifier: AGPL-3.0-only
package delivery

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"time"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/events"
	"github.com/inspr-at/paimos/internal/reviewgate"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/inspr-at/paimos/internal/workorders"
	"github.com/jackc/pgx/v5"
)

type ShipSettings struct {
	Mode     string `json:"mode"`
	Revision int64  `json:"revision"`
}
type ShipInput struct {
	Request      string  `json:"request_id"`
	Source       string  `json:"source_round_id"`
	Head         string  `json:"head_sha"`
	ScriptAction *string `json:"script_action,omitempty"`
}
type ShipRun struct {
	ID         int64   `json:"id"`
	Attempt    int     `json:"attempt"`
	Head       string  `json:"head_sha"`
	Workflow   string  `json:"workflow"`
	Event      string  `json:"event"`
	Status     string  `json:"status"`
	Conclusion string  `json:"conclusion"`
	Jobs       []Check `json:"jobs"`
}

// ShipFacts are bounded, normalized read-only observations. They are never
// accepted in a request, or replayed as GitHub writes.
type ShipFacts struct {
	PushedHead     string    `json:"pushed_head"`
	Base           string    `json:"base_sha"`
	Behind         bool      `json:"behind"`
	PR             *int64    `json:"pull_request"`
	PullHead       string    `json:"pull_head"`
	Open           bool      `json:"open"`
	Merged         bool      `json:"merged"`
	Draft          bool      `json:"draft"`
	Conflict       bool      `json:"conflict"`
	MergeableKnown bool      `json:"mergeable_known"`
	Queued         bool      `json:"queued"`
	QueueHead      string    `json:"queue_head"`
	Checks         []Check   `json:"checks"`
	Runs           []ShipRun `json:"runs"`
}
type ShipDecision struct {
	ShipInput
	Project    string      `json:"project_id"`
	Claimant   string      `json:"claimant_id"`
	Position   int64       `json:"position"`
	Mode       string      `json:"mode"`
	Execute    bool        `json:"execute"`
	Action     string      `json:"action"`
	Reason     string      `json:"reason"`
	Repository string      `json:"repository"`
	Branch     string      `json:"branch"`
	Facts      ShipFacts   `json:"facts"`
	Run        *int64      `json:"run_id"`
	RunAttempt int         `json:"run_attempt"`
	Round      *RoundInput `json:"round"`
	Agreement  *bool       `json:"agreement"`
	Claimed    bool        `json:"claimed"`
	Updated    time.Time   `json:"updated_at"`
}

var shipActions = []string{"wait", "held", "noop", "open_pr", "update_pr", "enqueue", "rerun_failed", "fix", "merge", "land"}

func validateShipInput(in ShipInput) error {
	if !workorders.UUID(in.Request) || !workorders.UUID(in.Source) || !reviewgate.ValidSHA(in.Head) || in.ScriptAction != nil && !slices.Contains(shipActions, *in.ScriptAction) {
		return fail(400, "invalid shipping claim")
	}
	return nil
}
func emptyShipFacts() ShipFacts { return ShipFacts{Checks: []Check{}, Runs: []ShipRun{}} }

// Prefer infrastructure recovery over treating aggregate failures as code
// defects. A non-aggregate failure in the same latest attempt proves a real
// defect; unknown/pending jobs cannot prove either outcome.
func shipRunAction(run ShipRun) string {
	if run.Status != "completed" || len(run.Jobs) == 0 {
		return "wait"
	}
	infra, real, aggregate := false, false, false
	for _, job := range run.Jobs {
		if job.Status != "completed" {
			return "wait"
		}
		switch job.Conclusion {
		case "cancelled", "timed_out", "startup_failure", "stale":
			infra = true
		case "failure":
			if slices.Contains([]string{"go", "web", "e2e", "release-check", "migration-compat", "gate/policy-preview", "tier-measurements"}, job.Name) {
				aggregate = true
			} else {
				real = true
			}
		case "success", "skipped", "neutral":
		default:
			return "wait"
		}
	}
	if real {
		return "fix"
	}
	if infra {
		return "rerun_failed"
	}
	if aggregate || run.Conclusion == "failure" {
		return "fix"
	}
	if run.Conclusion == "cancelled" || run.Conclusion == "timed_out" {
		return "rerun_failed"
	}
	return "none"
}

func proposeShip(out *ShipDecision, source Round, settings Settings, quarantine bool) {
	f := out.Facts
	set := func(action, reason string) { out.Action, out.Reason = action, reason }
	switch {
	case f.Merged:
		set("noop", "already_merged")
		return
	case f.PR != nil && !f.Open:
		set("held", "pull_request_closed")
		return
	case f.PushedHead != out.Head:
		if source.Kind == "merge" || source.Kind == "land" {
			set("land", "gated_round_requires_launcher_push")
			round := source.RoundInput
			round.Kind = "land"
			out.Round = &round
		} else {
			set("wait", "launcher_push_required")
		}
		return
	case f.Queued && f.PullHead != out.Head:
		set("held", "queued_head_changed")
		return
	case f.Queued:
		for _, run := range f.Runs {
			if run.Head == f.QueueHead && run.Event == "merge_group" && settings.RequiredWorkflow != nil && run.Workflow == *settings.RequiredWorkflow {
				action := shipRunAction(run)
				if action == "fix" || action == "rerun_failed" {
					out.Run, out.RunAttempt = &run.ID, run.Attempt
					set(action, "queue_"+action)
					return
				}
			}
		}
		set("noop", "already_queued")
		return
	case f.Conflict || f.Behind:
		set("merge", "main_moved")
		return
	case f.PR == nil:
		set("open_pr", "gated_head_pushed")
		return
	case f.PullHead != out.Head:
		set("wait", "pull_head_pending")
		return
	}
	for _, run := range f.Runs {
		if run.Head != out.Head || settings.RequiredWorkflow == nil || run.Workflow != *settings.RequiredWorkflow || run.Event != "pull_request" && run.Event != "push" {
			continue
		}
		action := shipRunAction(run)
		if action == "fix" || action == "rerun_failed" {
			out.Run = &run.ID
			out.RunAttempt = run.Attempt
			set(action, "ci_"+action)
			return
		}
	}
	if quarantine {
		set("held", "queue_failure_quarantine")
		return
	}
	// A contradictory/unfinished current run cannot be overridden by a stale
	// green rollup. Checks independently prove every configured required name.
	for _, run := range f.Runs {
		if run.Head == out.Head && settings.RequiredWorkflow != nil && run.Workflow == *settings.RequiredWorkflow && (run.Event == "pull_request" || run.Event == "push") && shipRunAction(run) == "wait" {
			set("wait", "ci_pending")
			return
		}
	}
	if !allSuccess(Observation{Checks: f.Checks, Settings: settings}) {
		set("wait", "required_checks_pending")
		return
	}
	if f.Draft {
		set("update_pr", "gated_draft_ready")
		return
	}
	if !f.MergeableKnown {
		set("wait", "mergeability_unknown")
		return
	}
	set("enqueue", "current_head_green")
}

func shipActionKey(out ShipDecision) *string {
	if !out.Claimed {
		return nil
	}
	// Fixes are fenced by head, even if a failed run is retried or the source
	// round changes without new commits. Infrastructure reruns use run+attempt.
	key := out.Repository + "/" + out.Branch + "/" + out.Head + "/" + out.Action
	if out.Action == "rerun_failed" {
		key += fmt.Sprintf("/%d/%d", *out.Run, out.RunAttempt)
	}
	if out.Action == "merge" {
		key += "/" + out.Facts.Base
	}
	return &key
}
func shipSettingsTx(ctx context.Context, tx pgx.Tx, project string) (ShipSettings, error) {
	out := ShipSettings{Mode: "off"}
	var raw []byte
	err := tx.QueryRow(ctx, `SELECT snapshot FROM delivery_ship_settings WHERE project_id=$1`, project).Scan(&raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return out, nil
	}
	if err == nil {
		err = json.Unmarshal(raw, &out)
	}
	return out, err
}
func saveShipSettingsTx(ctx context.Context, tx pgx.Tx, tid, project string, out ShipSettings) error {
	raw, err := json.Marshal(out)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO delivery_ship_settings(tenant_id,project_id,snapshot) VALUES($1,$2,$3) ON CONFLICT(tenant_id,project_id) DO UPDATE SET snapshot=EXCLUDED.snapshot`, tid, project, raw)
	return err
}
func scanShip(row pgx.Row) (ShipDecision, error) {
	var out ShipDecision
	var raw []byte
	err := row.Scan(&raw)
	if err == nil {
		err = json.Unmarshal(raw, &out)
	}
	return out, err
}
func saveShipTx(ctx context.Context, tx pgx.Tx, tid string, out ShipDecision) error {
	raw, err := json.Marshal(out)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO delivery_ship_decisions(tenant_id,project_id,request_id,claimant_id,source_round_id,head_sha,action_key,position,snapshot) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9) ON CONFLICT(tenant_id,project_id,request_id) DO UPDATE SET snapshot=EXCLUDED.snapshot`, tid, out.Project, out.Request, out.Claimant, out.Source, out.Head, shipActionKey(out), out.Position, raw)
	return err
}
func loadShipRetryTx(ctx context.Context, tx pgx.Tx, p tenant.Principal, project string, in ShipInput) (*ShipDecision, error) {
	old, err := scanShip(tx.QueryRow(ctx, `SELECT snapshot FROM delivery_ship_decisions WHERE project_id=$1 AND request_id=$2`, project, in.Request))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if old.Claimant != p.ID || !reflect.DeepEqual(old.ShipInput, in) {
		return nil, fail(409, "shipping request has different inputs or owner")
	}
	return &old, nil
}

type shipSnapshot struct {
	Project  string        `json:"project_id"`
	Settings *ShipSettings `json:"settings,omitempty"`
	Decision *ShipDecision `json:"decision,omitempty"`
}

func appendShipChange(ctx context.Context, tx pgx.Tx, p tenant.Principal, kind string, before any, after shipSnapshot, at time.Time) error {
	_, err := events.Append(ctx, tx, p, events.Change{NodeID: &after.Project, Type: "delivery.ship." + kind, Before: before, After: after, At: &at})
	return err
}

// RebuildShipping restores only proposals and ownership receipts. It cannot
// replay any network action, queue start, gate approval or publication.
func (m *Module) RebuildShipping(ctx context.Context, p tenant.Principal, project string) error {
	project = strings.ToLower(project)
	if !workorders.UUID(project) {
		return fail(400, "invalid shipping project")
	}
	ctx, cancel := context.WithTimeout(tenant.WithPrincipal(ctx, p), 30*time.Second)
	defer cancel()
	return db.InTenant(db.AllProjects(ctx, "replay explicitly authorized shadow shipping project"), m.pool, p.TenantID, func(tx pgx.Tx) error {
		if err := reviewProjectTx(ctx, tx, p, project, "delivery_ship.manage", true); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `SELECT after FROM events WHERE node_id=$1 AND type IN ('delivery.ship.settings','delivery.ship.decision') ORDER BY id LIMIT 10001`, project)
		if err != nil {
			return err
		}
		snapshots := []shipSnapshot{}
		total := 0
		for rows.Next() {
			var raw []byte
			var snap shipSnapshot
			if err = rows.Scan(&raw); err != nil {
				rows.Close()
				return err
			}
			total += len(raw)
			if total > 32<<20 {
				rows.Close()
				return fail(409, "shipping replay byte limit exceeded")
			}
			if err = json.Unmarshal(raw, &snap); err != nil {
				rows.Close()
				return err
			}
			if snap.Project != project || snap.Decision != nil && (snap.Decision.Project != project || snap.Decision.Execute) {
				rows.Close()
				return fail(409, "invalid shadow shipping replay")
			}
			snapshots = append(snapshots, snap)
		}
		rows.Close()
		if err = rows.Err(); err != nil {
			return err
		}
		if len(snapshots) > 10000 {
			return fail(409, "shipping replay limit exceeded")
		}
		for _, table := range []string{"delivery_ship_decisions", "delivery_ship_settings"} {
			if _, err = tx.Exec(ctx, `DELETE FROM `+table+` WHERE project_id=$1`, project); err != nil {
				return err
			}
		}
		for _, snap := range snapshots {
			if snap.Settings != nil {
				err = saveShipSettingsTx(ctx, tx, p.TenantID, project, *snap.Settings)
			}
			if err == nil && snap.Decision != nil {
				err = saveShipTx(ctx, tx, p.TenantID, *snap.Decision)
			}
			if err != nil {
				return err
			}
		}
		return appendShipChange(ctx, tx, p, "rebuilt", nil, shipSnapshot{Project: project}, m.now())
	})
}
