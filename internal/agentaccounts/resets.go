// SPDX-License-Identifier: AGPL-3.0-only
package agentaccounts

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/inspr-at/paimos/internal/accountprivacy"
	"github.com/inspr-at/paimos/internal/agentpairing"
	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/capacity"
	"github.com/inspr-at/paimos/internal/httpapi"
	"github.com/inspr-at/paimos/internal/modelprefs"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ResetVendor is an explicit capability supplied by an existing vendor adapter.
// Calls use opaque account identity and a durable idempotency key, never server
// credentials. Implementations must honor ctx and deduplicate ActionID. No
// adapter is installed by default: unsupported actions fail closed with 422.
type ResetVendor interface {
	Use(context.Context, ResetRequest) (ResetResult, error)
	Undo(context.Context, ResetRequest) (ResetResult, error)
}
type ResetRequest struct {
	ActionID        string
	AccountID       string
	Harness         string
	Provider        string
	DaemonID        string
	BindingRevision int64
	ExpectedCount   int
}
type ResetResult struct {
	ActionID  string               `json:"action_id"`
	AccountID string               `json:"account_id"`
	Window    capacity.Reading     `json:"window"`
	Report    capacity.ResetReport `json:"resets"`
	UndoUntil *time.Time           `json:"undo_until"`
}

func NewWithResetVendor(pool *pgxpool.Pool, vendor ResetVendor) httpapi.Module {
	return &Module{pool: pool, resetVendor: vendor}
}

type resetState struct {
	AccountID       string                 `json:"account_id"`
	Policy          string                 `json:"reset_policy"`
	Revision        int64                  `json:"revision"`
	BindingRevision int64                  `json:"binding_revision"`
	Credits         *capacity.ResetCredits `json:"resets"`
	Plan            *capacity.ResetPlan    `json:"reset_plan"`
	UndoSupported   bool                   `json:"undo_supported"`
	report          *capacity.ResetReport
	reading         *capacity.Reading
}

func loadResetState(ctx context.Context, tx pgx.Tx, a Account, now time.Time) (resetState, error) {
	out := resetState{AccountID: a.ID, Policy: "suggest", BindingRevision: a.LinkRevision}
	var raw []byte
	var policy *string
	var active bool
	err := tx.QueryRow(ctx, `SELECT reset_report,COALESCE(usage_revision,0),daily_reset_policy,
 COALESCE(reset_policy_person_id=`+modelprefs.CanonicalPersonSQL("owner_person_id")+` AND reset_policy_link_revision=link_revision,false)
 FROM agent_accounts WHERE id=$1`, a.ID).Scan(&raw, &out.Revision, &policy, &active)
	if err != nil {
		return out, err
	}
	if active && policy != nil {
		if *policy != "suggest" && *policy != "auto_before_expiry" {
			return out, errors.New("invalid stored reset policy")
		}
		out.Policy = *policy
	}
	if len(raw) == 0 {
		return out, nil
	}
	var report capacity.ResetReport
	if err := json.Unmarshal(raw, &report); err != nil {
		return out, err
	}
	if report.BindingRevision != a.LinkRevision {
		return out, nil
	}
	if report.Validate(now) != nil {
		return out, errors.New("invalid stored vendor reset report")
	}
	// Rebinding, consent withdrawal and uncertain vendor outcomes suppress credit
	// advice too. A later ordinary report cannot turn an uncertain spend into a retry.
	var blocked bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM account_reset_actions WHERE account_id=$1 AND state IN ('pending','unknown','undo_pending','undo_unknown'))`, a.ID).Scan(&blocked); err != nil {
		return out, err
	}
	if blocked || !a.UsageProbeEnabled || now.Sub(report.ReadAt) > ProbeFreshness {
		return out, nil
	}
	credits := report.Credits(now)
	out.Credits, out.report, out.UndoSupported = &credits, &report, report.UndoSupported
	schedule, err := routingSchedule(ctx, tx, a)
	if err != nil {
		return out, err
	}
	windows, err := overviewWindows(ctx, tx, a, now, schedule)
	if err != nil {
		return out, err
	}
	w := dailyWindow(windows, a.LinkedAt)
	if w == nil || w.ReadAt == nil || w.UsedPercent == nil || w.Source != "vendor_reported" {
		return out, nil
	}
	// Readings in the legacy ledger retain the precise window duration and
	// bucket used by learning. Fact-only windows do not invent a burn cohort.
	readings, err := readCapacity(ctx, tx, a.ID, true)
	if err != nil {
		return out, err
	}
	for _, r := range readings {
		if r.WindowKind == w.Kind && r.Bucket == w.Bucket && r.ResetsAt.Equal(w.ResetsAt) && r.ReadAt.Equal(*w.ReadAt) && r.Source != "estimate" {
			out.reading = &r
			learning, err := loadLearning(ctx, tx, a.ID)
			if err != nil {
				return out, err
			}
			metric := learning.metric(r, now, schedule, "")
			out.Plan = capacity.PlanReset(now, report, r, metric.BurnRate)
			break
		}
	}
	return out, nil
}

func storeResetReport(ctx context.Context, tx pgx.Tx, a Account, report capacity.ResetReport, now time.Time) error {
	if report.BindingRevision != a.LinkRevision || report.Validate(now) != nil {
		return fail(400, "invalid reset report or binding")
	}
	raw, err := json.Marshal(report)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `UPDATE agent_accounts SET reset_report=$2 WHERE id=$1 AND
 (reset_report IS NULL OR (reset_report->>'binding_revision')::bigint<>link_revision OR (reset_report->>'read_at')::timestamptz<$3)`, a.ID, raw, report.ReadAt)
	return err
}

func resetOwner(ctx context.Context, tx pgx.Tx, p tenant.Principal, id string, binding int64) (Account, error) {
	if p.Kind != tenant.Person || authz.RequireTx(ctx, tx, p, "account.manage", authz.Scope{}) != nil {
		return Account{}, fail(403, "person account management required")
	}
	a, err := lockAccount(ctx, tx, id)
	if err != nil {
		return a, err
	}
	var owns bool
	if err := tx.QueryRow(ctx, `SELECT COALESCE(`+modelprefs.CanonicalPersonSQL("$1::uuid")+`=`+modelprefs.CanonicalPersonSQL("$2::uuid")+`,false)`, p.ID, a.OwnerPersonID).Scan(&owns); err != nil {
		return a, err
	}
	if !owns {
		return a, fail(403, "account owner required")
	}
	if a.LinkRevision != binding {
		return a, &httpError{status: 409, code: "stale_binding", msg: "account binding changed"}
	}
	var current bool
	if err := tx.QueryRow(ctx, `SELECT NOT `+retiredSQL+` FROM agent_accounts WHERE id=$1`, id).Scan(&current); err != nil {
		return a, err
	}
	if !current {
		return a, fail(409, "account removed")
	}
	return a, nil
}

func (m *Module) resetPolicy(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	var in struct {
		Policy   string `json:"reset_policy"`
		Revision *int64 `json:"revision"`
		Binding  *int64 `json:"binding_revision"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, 4096)
	if err := decodeJSON(w, r, &in); err != nil {
		writeErr(w, err)
		return
	}
	if in.Revision == nil || in.Binding == nil || *in.Revision < 0 || *in.Binding < 0 || in.Policy != "suggest" && in.Policy != "auto_before_expiry" {
		writeErr(w, fail(400, "reset_policy, revision and binding_revision required"))
		return
	}
	id := r.PathValue("accountId")
	if !uuidRE.MatchString(id) {
		writeErr(w, fail(404, "account not found"))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	var out resetState
	err := m.inReadinessWrite(ctx, p, func(tx pgx.Tx) error {
		a, err := resetOwner(ctx, tx, p, id, *in.Binding)
		if err != nil {
			return err
		}
		now, err := dbNow(ctx, tx)
		if err != nil {
			return err
		}
		before, err := loadResetState(ctx, tx, a, now)
		if err != nil {
			return err
		}
		if before.Revision != *in.Revision {
			return fail(409, "account policy changed")
		}
		if before.Policy == in.Policy {
			out = before
			return nil
		}
		if _, err := tx.Exec(ctx, `UPDATE agent_accounts SET daily_reset_policy=$2,reset_policy_person_id=`+modelprefs.CanonicalPersonSQL("owner_person_id")+`,reset_policy_link_revision=link_revision,usage_revision=$3 WHERE id=$1`, id, in.Policy, *in.Revision+1); err != nil {
			return err
		}
		out, err = loadResetState(ctx, tx, a, now)
		if err != nil {
			return err
		}
		return writeEvent(ctx, tx, p, "account.reset_policy_changed", before, out)
	})
	if err != nil {
		writeErr(w, err)
		return
	}
	httpapi.WriteJSON(w, 200, out)
}

func resetExecutionAllowed(ctx context.Context, tx pgx.Tx, p tenant.Principal, a Account) error {
	if err := agentpairing.AccountFence(ctx, tx, a.ID, false); err != nil {
		return err
	}
	if a.State != "available" || !a.OngoingUseApproved || !a.UsageProbeEnabled {
		return fail(409, "account not approved for reset execution")
	}
	visible, err := accountprivacy.Load(ctx, tx, p, []string{a.ID})
	if err != nil {
		return err
	}
	if !visible[a.ID] {
		return fail(403, "account usage is private")
	}
	return nil
}

func activeResetPace(ctx context.Context, tx pgx.Tx, a Account, now time.Time) (float64, error) {
	var points float64
	err := tx.QueryRow(ctx, `SELECT COALESCE(max(raised_pace_points),0) FROM account_reset_actions
 WHERE account_id=$1 AND binding_revision=$2 AND person_id=`+modelprefs.CanonicalPersonSQL("$3::uuid")+`
 AND state='succeeded' AND by_kind='auto' AND raised_pace_until>$4`, a.ID, a.LinkRevision, a.OwnerPersonID, now).Scan(&points)
	return points, err
}
