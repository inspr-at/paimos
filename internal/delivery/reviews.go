// SPDX-License-Identifier: AGPL-3.0-only
package delivery

import (
	"context"
	"encoding/json"
	"errors"
	"path"
	"reflect"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/inspr-at/paimos/internal/harnesslaunch"
	"github.com/inspr-at/paimos/internal/modelprefs"
	"github.com/inspr-at/paimos/internal/modelregistry"
	"github.com/inspr-at/paimos/internal/reviewgate"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/inspr-at/paimos/internal/workorders"
	"github.com/jackc/pgx/v5"
)

const gateFixCap = 2

type ReviewSettings struct {
	Mode     string `json:"mode"`
	Revision int64  `json:"revision"`
}
type ReviewInput struct {
	SourceRound string `json:"source_round_id"`
	AuthorRun   string `json:"author_run_id"`
	Repository  string `json:"repository"`
	Base        string `json:"base_sha"`
	Head        string `json:"head_sha"`
}
type ReviewRoute struct {
	Profile  string `json:"profile_id"`
	Account  string `json:"account_id"`
	Reviewer string `json:"reviewer_principal_id"`
	Family   string `json:"family"`
	Harness  string `json:"harness"`
	Model    string `json:"model"`
	Effort   string `json:"effort"`
}
type ReviewVerdictInput struct {
	Request      string               `json:"request_id"`
	Revision     int64                `json:"revision"`
	Head         string               `json:"head_sha"`
	Profile      string               `json:"reviewer_profile_id"`
	Verdict      string               `json:"verdict"`
	Findings     []reviewgate.Finding `json:"findings"`
	ScriptAction *string              `json:"script_action"`
}
type ReviewFollowUp struct {
	ID          string               `json:"id"`
	Parent      *string              `json:"parent_id"`
	Title       string               `json:"title"`
	Description string               `json:"description"`
	Status      string               `json:"status"`
	Priority    string               `json:"priority"`
	Hidden      bool                 `json:"hide_from_release_notes"`
	PillEN      string               `json:"pill_en"`
	PillDE      string               `json:"pill_de"`
	BenefitEN   string               `json:"benefit_en"`
	BenefitDE   string               `json:"benefit_de"`
	Findings    []reviewgate.Finding `json:"findings"`
}
type ReviewLeadDecision struct {
	ID      string   `json:"id"`
	Title   string   `json:"title"`
	Options []string `json:"options"`
}
type ReviewRound struct {
	ReviewInput
	ID               string                  `json:"id"`
	Project          string                  `json:"project_id"`
	Ticket           string                  `json:"ticket_node_id"`
	Slug             string                  `json:"slug"`
	Key              string                  `json:"key"`
	Position         int64                   `json:"position"`
	State            string                  `json:"state"`
	GateFixRounds    int                     `json:"gate_fix_rounds"`
	Previous         *string                 `json:"previous_review_id"`
	DeltaBase        *string                 `json:"delta_base_sha"`
	AuthorFamily     string                  `json:"author_family"`
	Route            *ReviewRoute            `json:"route"`
	Policy           reviewgate.FamilyPolicy `json:"policy"`
	Reason           string                  `json:"reason"`
	Claimant         *string                 `json:"claimant_id"`
	ClaimRequest     *string                 `json:"claim_request_id"`
	Revision         int64                   `json:"revision"`
	Updated          time.Time               `json:"updated_at"`
	Action           string                  `json:"action"`
	EffectiveVerdict string                  `json:"effective_verdict"`
	VerdictInput     *ReviewVerdictInput     `json:"verdict_input"`
	Fix              *RoundInput             `json:"fix_round"`
	FollowUp         *ReviewFollowUp         `json:"follow_up"`
	Lead             *ReviewLeadDecision     `json:"lead_decision"`
	Agreement        *bool                   `json:"agreement"`
}
type ReviewClaimInput struct {
	Request      string  `json:"request_id"`
	ScriptFamily *string `json:"script_family"`
}
type ReviewClaim struct {
	ReviewClaimInput
	Project   string       `json:"project_id"`
	Review    string       `json:"review_id"`
	Claimant  string       `json:"claimant_id"`
	Mode      string       `json:"mode"`
	Execute   bool         `json:"execute"`
	Reason    string       `json:"reason"`
	Round     *ReviewRound `json:"round"`
	Agreement *bool        `json:"agreement"`
}

func validateReviewInput(in ReviewInput) error {
	if !workorders.UUID(in.SourceRound) || !workorders.UUID(in.AuthorRun) || !reviewgate.ValidRepository(in.Repository) || !reviewgate.ValidSHA(in.Base) || !reviewgate.ValidSHA(in.Head) || in.Base == in.Head {
		return fail(400, "invalid review build binding")
	}
	return nil
}

// This is a structured observation from the script's read-only gate. It never
// satisfies reviewgate.VerifiedOKTx or grants delivery/merge authority.
func validateReviewVerdict(in ReviewVerdictInput) error {
	if !workorders.UUID(in.Request) || in.Revision < 1 || !reviewgate.ValidSHA(in.Head) || !workorders.UUID(in.Profile) || in.Verdict != "ok" && in.Verdict != "changes" || in.Findings == nil || len(in.Findings) > 100 || in.Verdict == "changes" && len(in.Findings) == 0 {
		return fail(400, "invalid review verdict")
	}
	if in.ScriptAction != nil && *in.ScriptAction != "fix" && *in.ScriptAction != "lead_decision" && *in.ScriptAction != "ready_to_ship" {
		return fail(400, "invalid script action")
	}
	for _, f := range in.Findings {
		if f.Severity != "high" && f.Severity != "medium" && f.Severity != "low" && f.Severity != "nit" || len(f.File) == 0 || len(f.File) > 500 || path.IsAbs(f.File) || path.Clean(f.File) != f.File || f.File == "." || f.File == ".." || strings.HasPrefix(f.File, "../") || strings.ContainsAny(f.File, "\\\x00\r\n:") || f.Line < 1 || f.Line > 10_000_000 || strings.TrimSpace(f.Message) == "" || len(f.Message) > 2000 || !utf8.ValidString(f.File+f.Message) || strings.ContainsRune(f.Message, '\x00') || reviewgate.SensitiveText(f.File+f.Message) {
			return fail(400, "invalid bounded review finding")
		}
		if in.Verdict == "ok" && (f.Severity == "high" || f.Severity == "medium") {
			return fail(400, "blocking findings contradict an ok verdict")
		}
	}
	return nil
}

func reviewSettingsTx(ctx context.Context, tx pgx.Tx, project string) (ReviewSettings, error) {
	out := ReviewSettings{Mode: "off"}
	var raw []byte
	err := tx.QueryRow(ctx, `SELECT snapshot FROM delivery_review_settings WHERE project_id=$1`, project).Scan(&raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return out, nil
	}
	if err != nil {
		return out, err
	}
	err = json.Unmarshal(raw, &out)
	return out, err
}
func saveReviewSettingsTx(ctx context.Context, tx pgx.Tx, tid, project string, out ReviewSettings) error {
	raw, err := json.Marshal(out)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO delivery_review_settings(tenant_id,project_id,snapshot) VALUES($1,$2,$3) ON CONFLICT(tenant_id,project_id) DO UPDATE SET snapshot=EXCLUDED.snapshot`, tid, project, raw)
	return err
}
func scanReview(row pgx.Row) (ReviewRound, error) {
	var out ReviewRound
	var raw []byte
	if err := row.Scan(&raw); err != nil {
		return out, err
	}
	err := json.Unmarshal(raw, &out)
	return out, err
}
func loadReviewTx(ctx context.Context, tx pgx.Tx, project, id string) (ReviewRound, error) {
	return scanReview(tx.QueryRow(ctx, `SELECT snapshot FROM delivery_review_rounds WHERE project_id=$1 AND id=$2 FOR NO KEY UPDATE`, project, id))
}
func saveReviewTx(ctx context.Context, tx pgx.Tx, tid string, out ReviewRound) error {
	raw, err := json.Marshal(out)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO delivery_review_rounds(tenant_id,project_id,id,source_round_id,ticket_node_id,slug,repository,head_sha,author_run_id,state,position,snapshot)
 VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12) ON CONFLICT(tenant_id,id) DO UPDATE SET state=EXCLUDED.state,snapshot=EXCLUDED.snapshot`, tid, out.Project, out.ID, out.SourceRound, out.Ticket, out.Slug, out.Repository, out.Head, out.AuthorRun, out.State, out.Position, raw)
	return err
}
func saveReviewClaimTx(ctx context.Context, tx pgx.Tx, tid string, out ReviewClaim) error {
	raw, err := json.Marshal(out)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO delivery_review_claims(tenant_id,project_id,request_id,snapshot) VALUES($1,$2,$3,$4) ON CONFLICT(tenant_id,project_id,request_id) DO UPDATE SET snapshot=EXCLUDED.snapshot`, tid, out.Project, out.Request, raw)
	return err
}

func currentReviewTargetTx(ctx context.Context, tx pgx.Tx, out ReviewRound) (json.RawMessage, *string, error) {
	var fields json.RawMessage
	var parent *string
	// Execution work orders are children of a business leaf, not nested work
	// items. A completed build order must not make its ticket ineligible.
	err := tx.QueryRow(ctx, `SELECT n.fields,n.parent_id::text FROM nodes n JOIN node_kinds k ON k.tenant_id=n.tenant_id AND k.id=n.kind_id
 WHERE n.id=$1 AND n.project_id=$2 AND n.deleted_at IS NULL AND k.slug='work'
 AND NOT EXISTS(SELECT 1 FROM work_orders w WHERE w.node_id=n.id)
 AND NOT EXISTS(SELECT 1 FROM nodes c WHERE c.parent_id=n.id AND c.deleted_at IS NULL AND NOT EXISTS(SELECT 1 FROM work_orders w WHERE w.node_id=c.id))`, out.Ticket, out.Project).Scan(&fields, &parent)
	return fields, parent, err
}
func authorFamilyTx(ctx context.Context, tx pgx.Tx, out ReviewRound) (string, error) {
	var harness, model, family string
	err := tx.QueryRow(ctx, `SELECT p.harness,p.model,p.family FROM agent_runs r
 JOIN work_orders w ON w.tenant_id=r.tenant_id AND w.node_id=r.work_order_id
 JOIN nodes n ON n.tenant_id=w.tenant_id AND n.id=w.node_id
 JOIN model_profiles p ON p.tenant_id=r.tenant_id AND p.id=r.model_profile_id
 WHERE r.id=$1 AND r.status='completed' AND w.kind='build' AND w.status='done' AND n.parent_id=$2 AND n.project_id=$3 AND n.deleted_at IS NULL`, out.AuthorRun, out.Ticket, out.Project).Scan(&harness, &model, &family)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", fail(409, "author run is not a completed build on this ticket")
	}
	if err != nil {
		return "", err
	}
	if !harnesslaunch.FamilyMatches(harness, model, family) {
		return "", fail(409, "author run profile does not establish its family")
	}
	return family, nil
}
func (m *Module) resolveReviewTx(ctx context.Context, tx pgx.Tx, p tenant.Principal, out *ReviewRound, fields json.RawMessage) error {
	policy, err := reviewgate.LoadFamilyPolicyTx(ctx, tx, &out.Project)
	if err != nil {
		return err
	}
	out.Policy = policy.Effective
	out.Route = nil
	placement := modelprefs.PlacementFields(fields)
	route, err := modelregistry.ResolveReviewWithPolicyFor(ctx, tx, p, modelregistry.WorkQuery{AuthorFamily: out.AuthorFamily, ProjectID: out.Project, PersonID: modelprefs.PrefsPerson(ctx, tx, p), Area: placement.Area, Complexity: placement.Complexity, ComplexitySource: placement.ComplexitySource, TicketRole: placement.RouteRole, TicketResidency: placement.Residency}, m.now(), policy.Effective)
	if errors.Is(err, modelregistry.ErrCatalogNotReady) {
		out.Reason = "catalog_unavailable"
		return nil
	}
	if err != nil {
		return err
	}
	if route.Profile == nil || route.Account == nil {
		out.Reason = "reviewer_unavailable"
		return nil
	}
	profile := route.Profile
	allowed, why := policy.Effective.Decision(out.AuthorFamily, profile.Family)
	if !allowed {
		out.Reason = why
		return nil
	}
	out.Route = &ReviewRoute{Profile: profile.ID, Account: route.Account.ID, Reviewer: route.Account.RegisteredBy, Family: profile.Family, Harness: profile.Harness, Model: profile.Model, Effort: profile.Effort}
	out.Reason = "review_ready"
	return nil
}

func (m *Module) queueReviewTx(ctx context.Context, tx pgx.Tx, p tenant.Principal, project string, in ReviewInput) (ReviewRound, bool, error) {
	source, err := loadRoundTx(ctx, tx, project, in.SourceRound)
	if err != nil {
		return ReviewRound{}, false, err
	}
	out := ReviewRound{ReviewInput: in, ID: stableID(p.TenantID, project, "review/"+in.SourceRound), Project: project, Ticket: source.Ticket, Slug: source.Slug, Key: source.Key, State: "queued", Revision: 1, Updated: m.now(), Policy: reviewgate.DefaultFamilyPolicy(), Reason: "review_queued"}
	fields, _, err := currentReviewTargetTx(ctx, tx, out)
	if err != nil {
		return out, false, err
	}
	if source.State != "done" {
		return out, false, fail(409, "review requires a completed delivery round")
	}
	out.AuthorFamily, err = authorFamilyTx(ctx, tx, out)
	if err != nil {
		return out, false, err
	}
	old, err := loadReviewTx(ctx, tx, project, out.ID)
	if err == nil {
		if !reflect.DeepEqual(old.ReviewInput, in) {
			return out, false, fail(409, "source round already has a different review binding")
		}
		return old, false, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return out, false, err
	}
	var reused bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM delivery_review_rounds WHERE project_id=$1 AND (author_run_id=$2 OR (slug=$3 AND repository=$4 AND head_sha=$5)))`, project, in.AuthorRun, out.Slug, in.Repository, in.Head).Scan(&reused); err != nil {
		return out, false, err
	}
	if reused {
		return out, false, fail(409, "build run or commit head already belongs to another review")
	}
	previous, err := scanReview(tx.QueryRow(ctx, `SELECT snapshot FROM delivery_review_rounds WHERE project_id=$1 AND slug=$2 ORDER BY position DESC LIMIT 1`, project, out.Slug))
	if err == nil {
		if previous.Ticket != out.Ticket || previous.Repository != out.Repository {
			return out, false, fail(409, "slug belongs to a different review target")
		}
		if previous.State != "completed" || previous.Action == "lead_decision" {
			return out, false, fail(409, "previous review awaits a verdict or lead decision")
		}
		if previous.Head == out.Head || previous.AuthorRun == out.AuthorRun {
			return out, false, fail(409, "next review requires a new build and head")
		}
		if previous.Action == "fix" {
			out.Previous = &previous.ID
			out.DeltaBase = &previous.Head
			out.GateFixRounds = previous.GateFixRounds + 1
			if out.GateFixRounds > gateFixCap || source.Kind != "fix" || previous.Fix == nil || source.Number < previous.Fix.Number {
				return out, false, fail(409, "invalid gate fix continuation")
			}
		}
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return out, false, err
	}
	if err = tx.QueryRow(ctx, `SELECT coalesce(max(position),0)+1 FROM delivery_review_rounds WHERE project_id=$1`, project).Scan(&out.Position); err != nil {
		return out, false, err
	}
	// Off projects retain only the build observation; route decisions begin in shadow.
	s, err := reviewSettingsTx(ctx, tx, project)
	if err != nil {
		return out, false, err
	}
	if s.Mode == "shadow" {
		err = m.resolveReviewTx(ctx, tx, p, &out, fields)
	}
	if err == nil {
		err = saveReviewTx(ctx, tx, p.TenantID, out)
	}
	return out, true, err
}

func applyReviewVerdict(tid string, out *ReviewRound, in ReviewVerdictInput, source Round, parent *string, nextFix int) {
	out.VerdictInput = &in
	out.State = "completed"
	out.EffectiveVerdict = "ok"
	out.Action = "ready_to_ship"
	out.Reason = "severity_floor_passed"
	notes := []reviewgate.Finding{}
	for _, f := range in.Findings {
		if f.Severity == "high" || f.Severity == "medium" {
			out.EffectiveVerdict = "changes"
		} else {
			notes = append(notes, f)
		}
	}
	if out.EffectiveVerdict == "changes" {
		out.Action = "fix"
		out.Reason = "blocking_findings"
		if out.GateFixRounds >= gateFixCap || nextFix > 100 {
			out.Action = "lead_decision"
			out.Reason = "gate_fix_cap"
			out.Lead = &ReviewLeadDecision{ID: stableID(tid, out.Project, "review-lead/"+out.ID), Title: out.Key + ": review needs a lead decision", Options: []string{"ship", "cut_scope", "one_more_round"}}
		} else {
			fix := source.RoundInput
			fix.Kind = "fix"
			fix.Number = nextFix
			fix.Brief = "delivery-review:" + out.ID
			fix.Estimate = 60
			out.Fix = &fix
		}
	}
	if len(notes) > 0 {
		out.FollowUp = &ReviewFollowUp{ID: stableID(tid, out.Project, "review-followup/"+out.ID), Parent: parent, Title: out.Key + " follow-up: non-blocking review notes", Description: "Non-blocking notes from review " + out.ID + " on " + out.Key + ". Fix when cheap; never blocks this change.", Status: "backlog", Priority: "low", Hidden: true, PillEN: "Review follow-up", PillDE: "Review-Nacharbeit", BenefitEN: "Small quality improvements from a code review.", BenefitDE: "Kleine Qualitätsverbesserungen aus einer Code-Prüfung.", Findings: notes}
	}
	if in.ScriptAction != nil {
		agree := out.Action == *in.ScriptAction
		out.Agreement = &agree
	}
}
