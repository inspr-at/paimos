// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package baselinebatch

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/inspr-at/paimos/backend/delivery"
	"github.com/inspr-at/paimos/backend/externalstage"
	"github.com/inspr-at/paimos/backend/lifecycleintents"
	"github.com/inspr-at/paimos/backend/managedharness"
)

type Service struct {
	DB        *sql.DB
	Clock     Clock
	Delivery  *delivery.Store
	Lifecycle *lifecycleintents.Service
	Harness   *managedharness.Service
	Verifier  OwnedVerifier
	// External is the existing PAI-810/876 service. The bridge never mints
	// credentials or forges a principal; missing config stays setup-required.
	External *externalstage.Service
	// failAfter is a test-only crash seam between already-committed external
	// steps. Production code leaves it empty.
	failAfter string
}

func NewService(database *sql.DB, clock Clock, store *delivery.Store, lifecycle *lifecycleintents.Service, harness *managedharness.Service, verifier OwnedVerifier) *Service {
	if clock == nil {
		clock = ClockFunc(time.Now)
	}
	if verifier == nil {
		verifier = LifecycleVerifier{}
	}
	return &Service{DB: database, Clock: clock, Delivery: store, Lifecycle: lifecycle, Harness: harness, Verifier: verifier}
}

// requireStreamEnabled keeps every write behind the project's explicit opt-in.
// A project that has not joined the INSPR stream behaves exactly as before.
func (s *Service) requireStreamEnabled(ctx context.Context, tx *sql.Tx, projectID int64) error {
	enabled, err := streamEnabled(ctx, tx, projectID)
	if err != nil {
		return err
	}
	if !enabled {
		return fmt.Errorf("%w: project has not opted into the INSPR delivery stream", ErrConflict)
	}
	return nil
}

func streamEnabled(ctx context.Context, tx *sql.Tx, projectID int64) (bool, error) {
	var enabled int
	err := tx.QueryRowContext(ctx, `SELECT inspr_stream_enabled FROM projects WHERE id=?`, projectID).Scan(&enabled)
	if errors.Is(err, sql.ErrNoRows) {
		return false, fmt.Errorf("%w: project", ErrNotFound)
	}
	if err != nil {
		return false, err
	}
	return enabled == 1, nil
}

// SetStreamEnabled records the project's explicit opt-in decision. Turning the
// stream off never touches existing batches, tickets or legacy behaviour.
func (s *Service) SetStreamEnabled(ctx context.Context, actor Actor, projectID int64, enabled bool) (Workflow, error) {
	if err := requireHuman(actor); err != nil {
		return Workflow{}, err
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return Workflow{}, err
	}
	defer tx.Rollback()
	if _, err := s.currentAuthority(ctx, tx, actor, projectID, true); err != nil {
		return Workflow{}, err
	}
	value := 0
	if enabled {
		value = 1
	}
	if _, err := tx.ExecContext(ctx, `UPDATE projects SET inspr_stream_enabled=? WHERE id=?`, value, projectID); err != nil {
		return Workflow{}, err
	}
	if err := tx.Commit(); err != nil {
		return Workflow{}, err
	}
	return s.Workflow(ctx, actor, projectID)
}

type ImportRequest struct {
	Handover json.RawMessage
	Selected []string
}

type PatchDraftRequest struct {
	Selected        *[]string            `json:"selected_requirement_refs"`
	ConstraintRefs  *[]string            `json:"selected_constraint_refs"`
	ExecutionMode   *string              `json:"execution_mode"`
	Worker          *WorkerSelection     `json:"worker"`
	DelegatedLaunch DelegatedLaunchPatch `json:"delegated_launch"`
}

// DelegatedLaunchPatch preserves the security-significant difference between
// an omitted field and an explicit JSON null. encoding/json collapses that
// distinction for **T, which would make the deliberate UI unable to turn an
// already-selected grant back off before review.
type DelegatedLaunchPatch struct {
	Set   bool
	Value *DelegatedLaunchSelection
}

func (p *DelegatedLaunchPatch) UnmarshalJSON(raw []byte) error {
	p.Set = true
	if string(raw) == "null" {
		p.Value = nil
		return nil
	}
	var value DelegatedLaunchSelection
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&value); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return fmt.Errorf("delegated_launch trailing JSON")
	}
	p.Value = &value
	return nil
}

type ReviewRequest struct {
	ExecutionMode   string                    `json:"execution_mode"`
	Worker          WorkerSelection           `json:"worker"`
	Selected        []string                  `json:"selected_requirement_refs"`
	DelegatedLaunch *DelegatedLaunchSelection `json:"delegated_launch,omitempty"`
}

type StartRequest struct {
	IdempotencyKey   string                    `json:"idempotency_key"`
	ReviewID         int64                     `json:"review_id"`
	DraftID          int64                     `json:"-"`
	DraftRevision    int64                     `json:"draft_revision"`
	Confirm          bool                      `json:"confirm"`
	ContentDigest    string                    `json:"content_digest"`
	RevisionSeal     string                    `json:"revision_seal"`
	ExecutionMode    string                    `json:"execution_mode"`
	Selected         []string                  `json:"selected_requirement_refs"`
	Worker           WorkerSelection           `json:"worker"`
	DelegatedLaunch  *DelegatedLaunchSelection `json:"delegated_launch,omitempty"`
	ClientReady      *bool                     `json:"ready"`
	ClientAuthorized *bool                     `json:"authorized"`
}

func (s *Service) Workflow(ctx context.Context, actor Actor, projectID int64) (Workflow, error) {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return Workflow{}, err
	}
	defer tx.Rollback()
	if _, err := s.currentAuthority(ctx, tx, actor, projectID, false); err != nil {
		return Workflow{}, err
	}
	enabled, err := streamEnabled(ctx, tx, projectID)
	if err != nil {
		return Workflow{}, err
	}
	out := Workflow{
		ProjectID:          projectID,
		INSPRStreamEnabled: enabled,
		// Gating is the opt-in, and nothing else: a project outside the stream is
		// never asked for an Aithema baseline.
		INSPRGating:      enabled,
		LegacyUnaffected: !enabled,
		Choices: WorkflowChoices{
			ExecutionModes: []string{ModeManual, ModeAssisted, ModeAutomatic},
			Note:           "Manual human work does not require an AI account. Assisted and automatic require a current owned readiness observation.",
		},
	}
	if !enabled {
		if err := tx.Commit(); err != nil {
			return Workflow{}, err
		}
		return out, nil
	}
	out.Choices.Runtimes, err = s.listRuntimeChoices(ctx, tx, projectID)
	if err != nil {
		return Workflow{}, err
	}
	out.Choices.DelegatedLaunchTargets, err = s.listDelegatedLaunchTargets(ctx, tx, projectID)
	if err != nil {
		return Workflow{}, err
	}
	draft, err := loadOpenDraft(ctx, tx, projectID)
	if err == nil {
		out.Draft = &draft
		if draft.ExecutionMode != "" && draft.ExecutionMode != ModeManual {
			// Show the human why an agent mode would block before they press
			// Start, from the same evidence Start will demand.
			evidence, verifyErr := s.Verifier.Verify(ctx, tx, projectID, draft.Worker, draft.Baseline.ContentDigest, s.now())
			if verifyErr != nil {
				return Workflow{}, verifyErr
			}
			out.Readiness = &evidence
		}
	} else if !errors.Is(err, sql.ErrNoRows) {
		return Workflow{}, err
	}
	stored, err := listBatches(ctx, tx, projectID)
	if err != nil {
		return Workflow{}, err
	}
	out.Batches = []Batch{}
	for _, row := range stored {
		batch, err := s.projectBatch(ctx, tx, row)
		if err != nil {
			return Workflow{}, err
		}
		out.Batches = append(out.Batches, batch)
	}
	for i := range out.Batches {
		switch out.Batches[i].Status {
		case BatchQueued, BatchActive, BatchPaused, BatchBlocked:
			b := out.Batches[i]
			out.ActiveBatch = &b
		}
		if out.ActiveBatch != nil {
			break
		}
	}
	if err := tx.Commit(); err != nil {
		return Workflow{}, err
	}
	normalizeWorkflow(&out)
	return out, nil
}

// projectBatch turns one durable row into the read model the browser sees:
// derived workflow state, stage progress, labelled forecasts, the controls that
// currently have an owned effect, and the readiness evidence behind the start.
func (s *Service) projectBatch(ctx context.Context, tx *sql.Tx, stored storedBatch) (Batch, error) {
	state, progress, snapshot, err := s.batchState(ctx, tx, stored)
	if err != nil {
		return Batch{}, err
	}
	execution, err := loadOwnedExecution(ctx, tx, stored.ProjectID, stored.LifecycleIntentID)
	if err != nil {
		return Batch{}, err
	}
	forecasts, err := s.forecasts(ctx, tx, stored, state, progress, snapshot, s.now())
	if err != nil {
		return Batch{}, err
	}
	batch := Batch{
		ID: stored.ID, ProjectID: stored.ProjectID, BatchKey: stored.BatchKey, DraftID: stored.DraftID,
		DraftRevision: stored.DraftRevision, ReviewID: stored.ReviewID, Baseline: stored.Baseline,
		ExecutionMode: stored.ExecutionMode, Scope: stored.Scope, Worker: stored.Worker, IssueID: stored.IssueID,
		DelegatedLaunch: stored.DelegatedLaunch,
		DeliveryID:      stored.DeliveryID, AttemptID: stored.AttemptID, LifecycleIntentID: stored.LifecycleIntentID,
		ReadinessIntentID: stored.ReadinessIntentID, ControlState: stored.ControlState, ControlReason: stored.ControlReason,
		Status: state, WorkflowState: state, Progress: progress, Forecasts: forecasts,
		Controls: controlOptions(stored, state, execution), StartedBy: stored.StartedBy, StartedAt: stored.StartedAt,
	}
	if stored.ExecutionMode != ModeManual {
		evidence, err := s.Verifier.Verify(ctx, tx, stored.ProjectID, stored.Worker, stored.Baseline.ContentDigest, s.now())
		if err != nil {
			return Batch{}, err
		}
		batch.Readiness = &evidence
	}
	batch.LaunchGrant, err = loadLaunchGrantView(ctx, tx, stored.ID, s.now())
	if err != nil {
		return Batch{}, err
	}
	if batch.LaunchGrant != nil {
		available := batch.LaunchGrant.State == "active"
		reason := ""
		if !available {
			reason = "launch grant is " + batch.LaunchGrant.State
		}
		batch.Controls = append(batch.Controls, ControlOption{Action: "revoke_launch", Available: available,
			Effect: "launch_grant_revoke", Reason: reason})
	}
	normalizeBatch(&batch)
	return batch, nil
}

func (s *Service) Import(ctx context.Context, actor Actor, projectID int64, req ImportRequest) (Draft, error) {
	if err := requireHuman(actor); err != nil {
		return Draft{}, err
	}
	parsed, err := parseHandover(req.Handover)
	if err != nil {
		return Draft{}, err
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return Draft{}, err
	}
	defer tx.Rollback()
	if _, err := s.currentAuthority(ctx, tx, actor, projectID, true); err != nil {
		return Draft{}, err
	}
	if err := s.requireStreamEnabled(ctx, tx, projectID); err != nil {
		return Draft{}, err
	}
	selected := req.Selected
	if len(selected) == 0 {
		for _, r := range parsed.Baseline.Requirements {
			selected = append(selected, r.Ref)
		}
	}
	if err := validateSelected(parsed.Baseline.Requirements, parsed.Baseline.Constraints, selected, nil); err != nil {
		return Draft{}, err
	}
	now := s.stamp()
	draft, err := loadOpenDraft(ctx, tx, projectID)
	if errors.Is(err, sql.ErrNoRows) {
		content, _ := compactJSON(map[string]any{
			"handover_version":             parsed.Version,
			"stream_ref":                   parsed.StreamRef,
			"exported_at":                  parsed.ExportedAt,
			"baseline_ref":                 parsed.Baseline.BaselineRef,
			"revision":                     parsed.Baseline.Revision,
			"content_digest":               parsed.Baseline.ContentDigest,
			"revision_seal":                parsed.Baseline.RevisionSeal,
			"requirements":                 parsed.Baseline.Requirements,
			"constraints":                  parsed.Baseline.Constraints,
			"pending_proposals":            parsed.PendingProposals,
			"imported_claimed_approved_by": parsed.Baseline.ApprovedBy,
			"authenticity":                 ImportedClaimAuthenticity,
		})
		res, err := tx.ExecContext(ctx, `INSERT INTO baseline_batch_drafts(
			project_id,revision,status,baseline_ref,baseline_revision,content_digest,revision_seal,stream_ref,
			imported_claimed_approved_by,imported_claimed_approved_at,imported_authenticity,bounded_content_json,
			selected_requirement_refs_json,selected_constraint_refs_json,execution_mode,worker_json,created_by,created_at,updated_at)
			VALUES(?,1,'open',?,?,?,?,?,?,?,?,?,?,?,'','{}',?,?,?)`,
			projectID, parsed.Baseline.BaselineRef, parsed.Baseline.Revision, parsed.Baseline.ContentDigest, parsed.Baseline.RevisionSeal,
			parsed.StreamRef, parsed.Baseline.ApprovedBy, parsed.Baseline.ApprovedAt, ImportedClaimAuthenticity, string(content),
			encodeStrings(selected), encodeStrings(nil), actor.UserID, now, now)
		if err != nil {
			return Draft{}, err
		}
		id, _ := res.LastInsertId()
		draft, err = loadDraftByID(ctx, tx, projectID, id)
		if err != nil {
			return Draft{}, err
		}
	} else if err != nil {
		return Draft{}, err
	} else {
		if draft.Baseline.ContentDigest != parsed.Baseline.ContentDigest || draft.Baseline.RevisionSeal != parsed.Baseline.RevisionSeal {
			return Draft{}, fmt.Errorf("%w: open draft already binds a different baseline; finish or close it first", ErrConflict)
		}
		merged := uniqueStrings(append(draft.Selected.RequirementRefs, selected...))
		if err := s.patchDraftTx(ctx, tx, actor, &draft, PatchDraftRequest{Selected: &merged}); err != nil {
			return Draft{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return Draft{}, err
	}
	normalizeDraft(&draft)
	return draft, nil
}

func (s *Service) PatchDraft(ctx context.Context, actor Actor, projectID, draftID int64, req PatchDraftRequest) (Draft, error) {
	if err := requireHuman(actor); err != nil {
		return Draft{}, err
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return Draft{}, err
	}
	defer tx.Rollback()
	if _, err := s.currentAuthority(ctx, tx, actor, projectID, true); err != nil {
		return Draft{}, err
	}
	if err := s.requireStreamEnabled(ctx, tx, projectID); err != nil {
		return Draft{}, err
	}
	draft, err := loadDraftByID(ctx, tx, projectID, draftID)
	if err != nil {
		return Draft{}, err
	}
	if err := s.patchDraftTx(ctx, tx, actor, &draft, req); err != nil {
		return Draft{}, err
	}
	if err := tx.Commit(); err != nil {
		return Draft{}, err
	}
	normalizeDraft(&draft)
	return draft, nil
}

func (s *Service) patchDraftTx(ctx context.Context, tx *sql.Tx, actor Actor, draft *Draft, req PatchDraftRequest) error {
	if draft.Status != DraftOpen && draft.Status != DraftReviewing {
		return fmt.Errorf("%w: draft is closed", ErrConflict)
	}
	changed := false
	if req.Selected != nil {
		if err := validateSelected(draft.Requirements, draft.Constraints, *req.Selected, nil); err != nil {
			return err
		}
		draft.Selected.RequirementRefs = uniqueStrings(*req.Selected)
		changed = true
	}
	if req.ConstraintRefs != nil {
		if err := validateSelected(draft.Requirements, draft.Constraints, draft.Selected.RequirementRefs, *req.ConstraintRefs); err != nil {
			return err
		}
		draft.Selected.ConstraintRefs = uniqueStrings(*req.ConstraintRefs)
		changed = true
	}
	if req.ExecutionMode != nil {
		mode := strings.TrimSpace(*req.ExecutionMode)
		if mode != "" && mode != ModeManual && mode != ModeAssisted && mode != ModeAutomatic {
			return fmt.Errorf("%w: execution_mode", ErrInvalid)
		}
		if draft.ExecutionMode != mode {
			draft.ExecutionMode = mode
			changed = true
		}
	}
	if req.Worker != nil {
		draft.Worker = *req.Worker
		changed = true
	}
	if req.DelegatedLaunch.Set {
		draft.DelegatedLaunch = req.DelegatedLaunch.Value
		changed = true
	}
	if !changed {
		return nil
	}
	draft.Revision++
	draft.Status = DraftOpen
	draft.ReviewValid = false
	now := s.stamp()
	if _, err := tx.ExecContext(ctx, `UPDATE baseline_batch_reviews SET invalidated_at=?,invalidate_reason='draft_changed'
		WHERE draft_id=? AND invalidated_at IS NULL`, now, draft.ID); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `UPDATE baseline_batch_drafts SET revision=?,status='open',selected_requirement_refs_json=?,
		selected_constraint_refs_json=?,execution_mode=?,worker_json=?,delegated_launch_json=?,updated_at=? WHERE id=? AND project_id=?`,
		draft.Revision, encodeStrings(draft.Selected.RequirementRefs), encodeStrings(draft.Selected.ConstraintRefs),
		draft.ExecutionMode, encodeJSON(draft.Worker), encodeDelegatedLaunch(draft.DelegatedLaunch), now, draft.ID, draft.ProjectID)
	if err != nil {
		return err
	}
	reloaded, err := loadDraftByID(ctx, tx, draft.ProjectID, draft.ID)
	if err != nil {
		return err
	}
	*draft = reloaded
	return nil
}

func (s *Service) Review(ctx context.Context, actor Actor, projectID, draftID int64, req ReviewRequest) (Draft, error) {
	if err := requireHuman(actor); err != nil {
		return Draft{}, err
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return Draft{}, err
	}
	defer tx.Rollback()
	if _, err := s.currentAuthority(ctx, tx, actor, projectID, true); err != nil {
		return Draft{}, err
	}
	if err := s.requireStreamEnabled(ctx, tx, projectID); err != nil {
		return Draft{}, err
	}
	draft, err := loadDraftByID(ctx, tx, projectID, draftID)
	if err != nil {
		return Draft{}, err
	}
	// A consumed draft is finished. Re-reviewing one would resurrect an
	// already-delivered baseline and collide with the one-open-draft index.
	if draft.Status == DraftClosed {
		return Draft{}, fmt.Errorf("%w: draft is closed", ErrConflict)
	}
	if req.ExecutionMode != ModeManual && req.ExecutionMode != ModeAssisted && req.ExecutionMode != ModeAutomatic {
		return Draft{}, fmt.Errorf("%w: execution_mode", ErrInvalid)
	}
	selected := req.Selected
	if len(selected) == 0 {
		selected = draft.Selected.RequirementRefs
	}
	if err := validateSelected(draft.Requirements, draft.Constraints, selected, draft.Selected.ConstraintRefs); err != nil {
		return Draft{}, err
	}
	selected = canonicalRequirementRefs(selected)
	if req.ExecutionMode != ModeManual {
		if err := requireNamedWorker(req.Worker); err != nil {
			return Draft{}, err
		}
		if err := s.validateAgentWorker(ctx, tx, projectID, req.Worker); err != nil {
			return Draft{}, err
		}
	} else {
		req.Worker = WorkerSelection{}
	}
	if err := s.validateDelegatedLaunch(ctx, tx, projectID, req.ExecutionMode, req.DelegatedLaunch); err != nil {
		return Draft{}, err
	}
	now := s.stamp()
	if _, err := tx.ExecContext(ctx, `UPDATE baseline_batch_reviews SET invalidated_at=?,invalidate_reason='superseded'
		WHERE draft_id=? AND invalidated_at IS NULL`, now, draft.ID); err != nil {
		return Draft{}, err
	}
	binding := reviewBinding(draft, req.ExecutionMode, selected, req.Worker, req.DelegatedLaunch)
	res, err := tx.ExecContext(ctx, `INSERT INTO baseline_batch_reviews(
		draft_id,draft_revision,binding_hash,execution_mode,worker_json,selected_requirement_refs_json,
		human_user_id,session_credential_id,created_at,delegated_launch_json) VALUES(?,?,?,?,?,?,?,?,?,?)`,
		draft.ID, draft.Revision, binding, req.ExecutionMode, encodeJSON(req.Worker), encodeStrings(selected),
		actor.UserID, actor.SessionCredentialID, now, encodeDelegatedLaunch(req.DelegatedLaunch))
	if err != nil {
		return Draft{}, err
	}
	reviewID, _ := res.LastInsertId()
	if _, err := tx.ExecContext(ctx, `UPDATE baseline_batch_drafts SET status='reviewing',execution_mode=?,worker_json=?,
		selected_requirement_refs_json=?,delegated_launch_json=?,updated_at=? WHERE id=?`, req.ExecutionMode, encodeJSON(req.Worker),
		encodeStrings(selected), encodeDelegatedLaunch(req.DelegatedLaunch), now, draft.ID); err != nil {
		return Draft{}, err
	}
	_ = reviewID
	draft, err = loadDraftByID(ctx, tx, projectID, draftID)
	if err != nil {
		return Draft{}, err
	}
	if err := tx.Commit(); err != nil {
		return Draft{}, err
	}
	normalizeDraft(&draft)
	return draft, nil
}

func (s *Service) Export(ctx context.Context, actor Actor, projectID, draftID int64) (map[string]any, error) {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if _, err := s.currentAuthority(ctx, tx, actor, projectID, false); err != nil {
		return nil, err
	}
	draft, err := loadDraftByID(ctx, tx, projectID, draftID)
	if err != nil {
		return nil, err
	}
	payload := map[string]any{
		"handover_version": HandoverVersion,
		"stream_ref":       draft.Baseline.StreamRef,
		"exported_at":      s.stamp(),
		"baseline": map[string]any{
			"baseline_ref":   draft.Baseline.BaselineRef,
			"revision":       draft.Baseline.Revision,
			"content_digest": draft.Baseline.ContentDigest,
			"revision_seal":  draft.Baseline.RevisionSeal,
			"authenticity":   ImportedClaimAuthenticity,
			"requirements":   draft.Requirements,
			"constraints":    draft.Constraints,
		},
		"selected":   draft.Selected,
		"unresolved": draft.Unresolved,
		"note":       "Imported approved_by remains an untrusted claim; current Paimos human attestation is required to start.",
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return payload, nil
}

func (s *Service) listRuntimeChoices(ctx context.Context, tx *sql.Tx, projectID int64) ([]RuntimeChoice, error) {
	rows, err := tx.QueryContext(ctx, `SELECT id,generation,registration_json,expires_at FROM lifecycle_runtimes
		WHERE project_id=? AND expires_at>? ORDER BY created_at DESC LIMIT 32`, projectID, s.stamp())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []RuntimeChoice{}
	for rows.Next() {
		var id, generation, body, expires string
		if err := rows.Scan(&id, &generation, &body, &expires); err != nil {
			return nil, err
		}
		var reg lifecycleintents.Registration
		if json.Unmarshal([]byte(body), &reg) != nil {
			continue
		}
		choice := RuntimeChoice{RuntimeID: id, RuntimeGeneration: generation, AccountLabel: reg.AccountLabel, ExpiresAt: expires, SchemaVersion: reg.SchemaVersion}
		if reg.SchemaVersion == lifecycleintents.AccountScopeSchemaV3 {
			choice.AccountScopes = append([]lifecycleintents.AccountScope(nil), reg.AccountScopes...)
		} else {
			for _, a := range reg.Accounts {
				choice.Accounts = append(choice.Accounts, AccountChoice{Key: a.Key, Label: a.Label})
			}
			for _, p := range reg.Profiles {
				choice.Profiles = append(choice.Profiles, ProfileChoice{ID: p.ID, Version: p.Version})
			}
		}
		for _, w := range reg.Workspaces {
			choice.Workspaces = append(choice.Workspaces, WorkspaceChoice{Handle: w.Handle, Identity: w.Identity, Label: w.Label})
		}
		out = append(out, choice)
	}
	return out, rows.Err()
}

func reviewBinding(draft Draft, mode string, selected []string, worker WorkerSelection, delegated ...*DelegatedLaunchSelection) string {
	var launch *DelegatedLaunchSelection
	if len(delegated) > 0 {
		launch = delegated[0]
	}
	return DigestSHA256(
		draft.Baseline.ContentDigest,
		draft.Baseline.RevisionSeal,
		fmt.Sprintf("%d", draft.Revision),
		mode,
		canonicalScopeKey(selected),
		worker.RuntimeID, worker.RuntimeGeneration, worker.AccountKey, worker.AccountLabel,
		worker.ProfileID, worker.ProfileVersion, worker.WorkspaceHandle, worker.WorkerName,
		encodeDelegatedLaunch(launch),
	)
}

func validateSelected(reqs []Requirement, cons []Constraint, selected, constraints []string) error {
	if len(selected) == 0 {
		return fmt.Errorf("%w: at least one requirement", ErrInvalid)
	}
	knownReq := map[string]bool{}
	for _, r := range reqs {
		knownReq[r.Ref] = true
	}
	knownCon := map[string]bool{}
	for _, c := range cons {
		knownCon[c.Ref] = true
	}
	for _, ref := range selected {
		if !knownReq[ref] {
			return fmt.Errorf("%w: unknown requirement", ErrInvalid)
		}
	}
	for _, ref := range constraints {
		if !knownCon[ref] {
			return fmt.Errorf("%w: unknown constraint", ErrInvalid)
		}
	}
	return nil
}

func uniqueStrings(in []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(in))
	for _, v := range in {
		v = strings.TrimSpace(v)
		if v == "" || seen[v] {
			continue
		}
		seen[v] = true
		out = append(out, v)
	}
	return out
}

// canonicalRequirementRefs is the reviewed-scope set: first-seen unique
// membership, then stable order. uniqueStrings stays first-seen for draft
// merge/patch display; authority binding must not treat click order as a
// different scope. Refs may contain commas; membership is the array, not a
// delimiter-joined string.
func canonicalRequirementRefs(in []string) []string {
	out := uniqueStrings(in)
	sort.Strings(out)
	return out
}

func sameCanonicalScope(a, b []string) bool {
	return slices.Equal(canonicalRequirementRefs(a), canonicalRequirementRefs(b))
}

func canonicalScopeKey(in []string) string {
	return encodeStrings(canonicalRequirementRefs(in))
}

func encodeStrings(v []string) string {
	if v == nil {
		v = []string{}
	}
	b, _ := json.Marshal(v)
	return string(b)
}

func encodeJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

func loadOpenDraft(ctx context.Context, tx *sql.Tx, projectID int64) (Draft, error) {
	var id int64
	err := tx.QueryRowContext(ctx, `SELECT id FROM baseline_batch_drafts WHERE project_id=? AND status IN ('open','reviewing') ORDER BY id DESC LIMIT 1`, projectID).Scan(&id)
	if err != nil {
		return Draft{}, err
	}
	return loadDraftByID(ctx, tx, projectID, id)
}

func loadDraftByID(ctx context.Context, tx *sql.Tx, projectID, id int64) (Draft, error) {
	var d Draft
	var content, selectedJSON, constraintJSON, workerJSON, delegatedJSON string
	var reviewID sql.NullInt64
	err := tx.QueryRowContext(ctx, `SELECT id,project_id,revision,status,baseline_ref,baseline_revision,content_digest,revision_seal,stream_ref,
		imported_claimed_approved_by,imported_claimed_approved_at,imported_authenticity,bounded_content_json,
		selected_requirement_refs_json,selected_constraint_refs_json,execution_mode,worker_json,delegated_launch_json,created_at,updated_at
		FROM baseline_batch_drafts WHERE id=? AND project_id=?`, id, projectID).Scan(
		&d.ID, &d.ProjectID, &d.Revision, &d.Status, &d.Baseline.BaselineRef, &d.Baseline.Revision, &d.Baseline.ContentDigest,
		&d.Baseline.RevisionSeal, &d.Baseline.StreamRef, &d.Baseline.ImportedClaimedApprovedBy, &d.Baseline.ImportedClaimedApprovedAt,
		&d.Baseline.Authenticity, &content, &selectedJSON, &constraintJSON, &d.ExecutionMode, &workerJSON, &delegatedJSON, &d.CreatedAt, &d.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Draft{}, fmt.Errorf("%w: draft", ErrNotFound)
	}
	if err != nil {
		return Draft{}, err
	}
	d.Baseline.Authenticity = ImportedClaimAuthenticity
	_ = json.Unmarshal([]byte(selectedJSON), &d.Selected.RequirementRefs)
	_ = json.Unmarshal([]byte(constraintJSON), &d.Selected.ConstraintRefs)
	_ = json.Unmarshal([]byte(workerJSON), &d.Worker)
	decodeDelegatedLaunch(delegatedJSON, &d.DelegatedLaunch)
	var boxed struct {
		Requirements []Requirement    `json:"requirements"`
		Constraints  []Constraint     `json:"constraints"`
		Pending      []parsedProposal `json:"pending_proposals"`
	}
	_ = json.Unmarshal([]byte(content), &boxed)
	d.Requirements, d.Constraints = boxed.Requirements, boxed.Constraints
	known := map[string]bool{}
	for _, r := range d.Selected.RequirementRefs {
		known[r] = true
	}
	for _, p := range boxed.Pending {
		if !known[p.Ref] {
			d.Unresolved = append(d.Unresolved, UnresolvedItem{Kind: "pending_proposal", Ref: p.Ref, Summary: p.Summary})
		}
	}
	// A confirmed review belongs to a delivered batch: it is not a pending
	// review of this draft, and it never makes a closed draft look reviewable.
	_ = tx.QueryRowContext(ctx, `SELECT id FROM baseline_batch_reviews WHERE draft_id=? AND draft_revision=?
		AND invalidated_at IS NULL AND confirmed=0 ORDER BY id DESC LIMIT 1`, d.ID, d.Revision).Scan(&reviewID)
	if reviewID.Valid && d.Status != DraftClosed {
		id := reviewID.Int64
		d.ReviewID = &id
		d.ReviewValid = true
		d.Status = DraftReviewing
	}
	d.Impact = impactEstimate(d)
	normalizeDraft(&d)
	return d, nil
}

// impactEstimate discloses the size of the reviewed selection and how much of
// it is unresolved. Its forecast is explicitly an educated guess: no worker has
// observed anything yet, and this number gates nothing.
func impactEstimate(d Draft) ImpactEstimate {
	selected := map[string]bool{}
	for _, ref := range d.Selected.RequirementRefs {
		selected[ref] = true
	}
	out := ImpactEstimate{UnresolvedCount: len(d.Unresolved), ConstraintCount: len(d.Selected.ConstraintRefs)}
	for _, requirement := range d.Requirements {
		if !selected[requirement.Ref] {
			continue
		}
		out.RequirementCount++
		out.AcceptanceCriteriaCount += len(requirement.AcceptanceCriteria)
	}
	eta := int64(out.RequirementCount * 1800)
	out.Forecast = Forecast{
		Subject: "overall", Percent: 0, ETASeconds: &eta, Kind: ForecastGuess,
		Basis:    fmt.Sprintf("%d selected requirements, %d acceptance criteria, %d unresolved items", out.RequirementCount, out.AcceptanceCriteriaCount, out.UnresolvedCount),
		AsOf:     d.UpdatedAt,
		Label:    forecastLabel(ForecastGuess),
		Fresh:    false,
		Observed: false,
	}
	return out
}

// storedBatch is the immutable durable row. Everything a reader sees beyond
// this — state, progress, forecasts, controls, readiness — is derived from live
// feeds at read time, so no projection can drift from its source.
type storedBatch struct {
	ID                int64
	ProjectID         int64
	BatchKey          string
	DraftID           int64
	DraftRevision     int64
	ReviewID          int64
	Baseline          BaselineClaim
	ExecutionMode     string
	Scope             Scope
	Worker            WorkerSelection
	DelegatedLaunch   *DelegatedLaunchSelection
	IssueID           int64
	DeliveryID        *int64
	AttemptID         *int64
	LifecycleIntentID string
	ReadinessIntentID string
	ControlState      string
	ControlReason     string
	StartedBy         int64
	StartedAt         string
}

func listBatches(ctx context.Context, tx *sql.Tx, projectID int64) ([]storedBatch, error) {
	rows, err := tx.QueryContext(ctx, `SELECT id FROM baseline_batch_batches WHERE project_id=? ORDER BY id DESC LIMIT 32`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := []storedBatch{}
	for _, id := range ids {
		b, err := loadBatchByID(ctx, tx, projectID, id)
		if err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, nil
}

func loadBatchByID(ctx context.Context, tx *sql.Tx, projectID, id int64) (storedBatch, error) {
	var b storedBatch
	var deliveryID, attemptID sql.NullInt64
	var scopeJSON, workerJSON, delegatedJSON, claimedBy, claimedAt, authenticity, streamRef string
	err := tx.QueryRowContext(ctx, `SELECT id,project_id,batch_key,draft_id,draft_revision,review_id,baseline_ref,content_digest,revision_seal,
		execution_mode,scope_json,worker_json,issue_id,delivery_id,attempt_id,lifecycle_intent_id,readiness_intent_id,
		control_state,control_reason,imported_claimed_approved_by,imported_claimed_approved_at,imported_authenticity,
		stream_ref,started_by,started_at,delegated_launch_json
		FROM baseline_batch_batches WHERE id=? AND project_id=?`, id, projectID).Scan(
		&b.ID, &b.ProjectID, &b.BatchKey, &b.DraftID, &b.DraftRevision, &b.ReviewID, &b.Baseline.BaselineRef,
		&b.Baseline.ContentDigest, &b.Baseline.RevisionSeal, &b.ExecutionMode, &scopeJSON, &workerJSON, &b.IssueID,
		&deliveryID, &attemptID, &b.LifecycleIntentID, &b.ReadinessIntentID, &b.ControlState, &b.ControlReason,
		&claimedBy, &claimedAt, &authenticity, &streamRef, &b.StartedBy, &b.StartedAt, &delegatedJSON)
	if errors.Is(err, sql.ErrNoRows) {
		return storedBatch{}, fmt.Errorf("%w: batch", ErrNotFound)
	}
	if err != nil {
		return storedBatch{}, err
	}
	b.Baseline.ImportedClaimedApprovedBy = claimedBy
	b.Baseline.ImportedClaimedApprovedAt = claimedAt
	b.Baseline.Authenticity = ImportedClaimAuthenticity
	b.Baseline.StreamRef = streamRef
	_ = json.Unmarshal([]byte(scopeJSON), &b.Scope)
	_ = json.Unmarshal([]byte(workerJSON), &b.Worker)
	decodeDelegatedLaunch(delegatedJSON, &b.DelegatedLaunch)
	if deliveryID.Valid {
		v := deliveryID.Int64
		b.DeliveryID = &v
	}
	if attemptID.Valid {
		v := attemptID.Int64
		b.AttemptID = &v
	}
	return b, nil
}

func loadForecasts(ctx context.Context, tx *sql.Tx, batchID int64) ([]Forecast, error) {
	rows, err := tx.QueryContext(ctx, `SELECT subject,percent,eta_seconds,kind,basis,as_of FROM baseline_batch_forecasts WHERE batch_id=? ORDER BY id`, batchID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Forecast{}
	for rows.Next() {
		var f Forecast
		var eta sql.NullInt64
		if err := rows.Scan(&f.Subject, &f.Percent, &eta, &f.Kind, &f.Basis, &f.AsOf); err != nil {
			return nil, err
		}
		if eta.Valid {
			v := eta.Int64
			f.ETASeconds = &v
		}
		f.Label = forecastLabel(f.Kind)
		out = append(out, f)
	}
	return out, rows.Err()
}

func forecastLabel(kind string) string {
	switch kind {
	case ForecastMeasured:
		return "observed"
	case ForecastWorker:
		return "worker estimate"
	default:
		return "guessed"
	}
}

func newBatchKey() string {
	return "batch-" + uuid.NewString()
}

func validIdempotency(v string) bool {
	return len(v) >= 8 && len(v) <= 80 && deliveryOpaque(v)
}

func deliveryOpaque(v string) bool {
	if v == "" || len(v) > 128 {
		return false
	}
	for i, r := range v {
		ok := (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '.' || r == '_' || r == ':' || r == '/' || r == '-'
		if i == 0 && !((r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9')) {
			return false
		}
		if !ok {
			return false
		}
	}
	return true
}

func isUniqueConstraint(err error) bool {
	return err != nil && strings.Contains(err.Error(), "UNIQUE constraint failed")
}
