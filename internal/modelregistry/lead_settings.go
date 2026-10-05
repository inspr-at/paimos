// SPDX-License-Identifier: AGPL-3.0-only
package modelregistry

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net/http"
	"slices"
	"strings"

	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/events"
	"github.com/inspr-at/paimos/internal/httpapi"
	"github.com/inspr-at/paimos/internal/modelprefs"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

// LeadRecovery caps the entire build/review/fix lineage. It does not authorize
// restarts, credential rotation, attached reconnects or publication.
type LeadRecovery struct {
	MaxAttempts int     `json:"max_attempts"`
	AgentHours  float64 `json:"agent_hours"`
}

// LeadPolicy is sparse: nil inherits; a nonnil empty target list denies all.
// Model selection references the existing preference matrix, never a new route.
type LeadPolicy struct {
	AllowedHostIDs    *[]string     `json:"allowed_host_ids,omitempty"`
	AllowedAccountIDs *[]string     `json:"allowed_account_ids,omitempty"`
	WorkKindID        *string       `json:"work_kind_id,omitempty"`
	Bucket            *string       `json:"bucket,omitempty"`
	Recovery          *LeadRecovery `json:"recovery,omitempty"`
}

type LeadSettings struct {
	Revision               int64                  `json:"revision"`
	WorkspaceRevision      int64                  `json:"workspace_revision"`
	OwnerPersonID          *string                `json:"owner_person_id"`
	Overrides              *LeadPolicy            `json:"overrides,omitempty"`
	Effective              LeadPolicy             `json:"effective"`
	ModelSelector          *modelprefs.CellResult `json:"model_selector,omitempty"`
	ModelRevisions         []int64                `json:"model_revisions"`
	Residency              string                 `json:"residency"`
	DetailsRedacted        bool                   `json:"details_redacted"`
	AutomaticLaunchEnabled bool                   `json:"automatic_launch_enabled"`
	WaitReason             string                 `json:"wait_reason"`
	DialPath               string                 `json:"dial_path"`
	RequiredStartGates     []string               `json:"required_start_gates"`
}

type leadSettingsRow struct {
	Revision int64
	Owner    *string
	Policy   LeadPolicy
	Editor   string
}

func validateLeadPolicy(p LeadPolicy) error {
	for _, ids := range []*[]string{p.AllowedHostIDs, p.AllowedAccountIDs} {
		if ids == nil {
			continue
		}
		if len(*ids) > 32 {
			return prefFail(422, "too_many_targets")
		}
		seen := map[string]bool{}
		for i, id := range *ids {
			id = strings.ToLower(id)
			if !uuidRE.MatchString(id) || seen[id] {
				return prefFail(422, "invalid_targets")
			}
			seen[id] = true
			(*ids)[i] = id
		}
	}
	if p.WorkKindID != nil && !uuidRE.MatchString(*p.WorkKindID) {
		return prefFail(422, "unknown_kind")
	}
	if p.Bucket != nil && *p.Bucket != "normal" && *p.Bucket != "complex" {
		return prefFail(422, "invalid_bucket")
	}
	if r := p.Recovery; r != nil {
		if r.MaxAttempts < 0 || r.MaxAttempts > 5 || math.IsNaN(r.AgentHours) || math.IsInf(r.AgentHours, 0) || r.AgentHours < 0 || r.AgentHours > 10000 || ((r.MaxAttempts == 0) != (r.AgentHours == 0)) {
			return prefFail(422, "invalid_recovery_budget")
		}
	}
	return nil
}

func intersectLeadTargets(parent, child *[]string) *[]string {
	if parent == nil {
		return child
	}
	if child == nil {
		return parent
	}
	out := []string{}
	for _, id := range *child {
		if slices.Contains(*parent, id) {
			out = append(out, id)
		}
	}
	return &out
}

func effectiveLeadPolicy(workspace, project LeadPolicy) LeadPolicy {
	out := workspace
	out.AllowedHostIDs = intersectLeadTargets(workspace.AllowedHostIDs, project.AllowedHostIDs)
	out.AllowedAccountIDs = intersectLeadTargets(workspace.AllowedAccountIDs, project.AllowedAccountIDs)
	if project.WorkKindID != nil {
		out.WorkKindID = project.WorkKindID
	}
	if project.Bucket != nil {
		out.Bucket = project.Bucket
	}
	if out.Bucket == nil {
		b := "normal"
		out.Bucket = &b
	}
	if out.Recovery == nil {
		out.Recovery = &LeadRecovery{}
	}
	if project.Recovery != nil {
		out.Recovery = &LeadRecovery{min(out.Recovery.MaxAttempts, project.Recovery.MaxAttempts), min(out.Recovery.AgentHours, project.Recovery.AgentHours)}
	}
	return out
}

func loadLeadRow(ctx context.Context, tx pgx.Tx, project string) (leadSettingsRow, error) {
	var out leadSettingsRow
	var raw []byte
	err := tx.QueryRow(ctx, `SELECT revision,owner_person_id::text,overrides,updated_by::text FROM project_lead_settings WHERE project_id IS NOT DISTINCT FROM $1::uuid`, optionalUUID(project)).Scan(&out.Revision, &out.Owner, &raw, &out.Editor)
	if errors.Is(err, pgx.ErrNoRows) {
		return out, nil
	}
	if err != nil {
		return out, err
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err = dec.Decode(&out.Policy); err != nil {
		return out, err
	}
	return out, validateLeadPolicy(out.Policy)
}

// LoadLeadSettingsTx resolves live policy without taking a cached snapshot or
// granting a start. Execution consumers must recheck the owner's authority,
// revisions, consent, dial, harness, account room and host load under their final
// claim/start locks. Owner-only selectors are redacted for project members.
func LoadLeadSettingsTx(ctx context.Context, tx pgx.Tx, p tenant.Principal, project string) (LeadSettings, error) {
	out := LeadSettings{ModelRevisions: []int64{}, Residency: "any", WaitReason: "acceptance_pending", DialPath: "/api/agents/plan", RequiredStartGates: []string{"dial", "harness", "account_room", "host_load"}}
	if project == "" {
		if p.Kind != tenant.Person {
			return out, prefFail(403, "person_required")
		}
		if err := authz.RequireTx(ctx, tx, p, "model_prefs.manage", authz.Scope{}); err != nil {
			return out, err
		}
	} else if err := readableProject(ctx, tx, p, project); err != nil {
		return out, err
	}
	workspace, err := loadLeadRow(ctx, tx, "")
	if err != nil {
		return out, err
	}
	row := workspace
	if project != "" {
		row, err = loadLeadRow(ctx, tx, project)
		if err != nil {
			return out, err
		}
	}
	out.Revision = row.Revision
	out.WorkspaceRevision = workspace.Revision
	out.OwnerPersonID = row.Owner
	local := row.Policy
	if project == "" {
		local = LeadPolicy{}
	}
	out.Effective = effectiveLeadPolicy(workspace.Policy, local)
	// Only an active owning person sees private IDs. An agent creator grants no
	// implicit account visibility; agents consume redacted public explanations.
	person, err := modelprefs.CanonicalPerson(ctx, tx, p.ID)
	if err != nil {
		return out, err
	}
	var canonicalOwner *string
	if row.Owner != nil {
		canonicalOwner, err = modelprefs.CanonicalPerson(ctx, tx, *row.Owner)
		if err != nil {
			return out, err
		}
	}
	editor, err := modelprefs.CanonicalPerson(ctx, tx, row.Editor)
	if err != nil {
		return out, err
	}
	private := p.Kind == tenant.Person && person != nil && (project == "" && (row.Revision == 0 || editor != nil && *editor == *person) || canonicalOwner != nil && *canonicalOwner == *person)
	if private {
		out.Overrides = &row.Policy
	}
	if project != "" && row.Owner != nil {
		owner, err := modelprefs.CanonicalPerson(ctx, tx, *row.Owner)
		if err != nil {
			return out, err
		}
		if owner == nil {
			out.WaitReason = "owner_unavailable"
		} else {
			op := tenant.Principal{ID: *owner, TenantID: p.TenantID, Kind: tenant.Person}
			for _, permission := range []string{"nodes.read", "model_prefs.manage"} {
				if err := authz.RequireTx(ctx, tx, op, permission, authz.Scope{ProjectID: project}); err != nil {
					if !errors.Is(err, authz.ErrForbidden) {
						return out, err
					}
					out.WaitReason = "owner_unavailable"
				}
			}
			if private {
				chain, err := modelprefs.LoadChain(ctx, tx, owner, project)
				if err != nil {
					return out, err
				}
				kind := "other"
				if out.Effective.WorkKindID != nil {
					k, err := preferenceKind(ctx, tx, *out.Effective.WorkKindID, "project", project)
					if err != nil {
						return out, err
					}
					kind = k.Slug
				}
				selector := modelprefs.ResolveCell(chain, kind, *out.Effective.Bucket)
				out.ModelSelector = &selector
				out.Residency = modelprefs.ResolveResidency(chain).Value
				for _, scope := range chain {
					out.ModelRevisions = append(out.ModelRevisions, scope.Revision)
				}
			}
		}
	}
	if private {
		// Inherited selectors may belong to a different workspace editor. A later
		// account relink or computer revocation must not reveal another person's IDs.
		for _, policy := range []LeadPolicy{out.Effective, row.Policy} {
			targets := LeadPolicy{AllowedHostIDs: policy.AllowedHostIDs, AllowedAccountIDs: policy.AllowedAccountIDs}
			if err := validateLeadBindings(ctx, tx, targets, project, person); err != nil {
				var denied *preferenceError
				if !errors.As(err, &denied) || denied.code != "invalid_owner_binding" {
					return out, err
				}
				out.DetailsRedacted = true
				out.Effective.AllowedHostIDs = nil
				out.Effective.AllowedAccountIDs = nil
				out.Overrides = nil
			}
		}
	}
	if !private {
		out.DetailsRedacted = true
		out.Effective.AllowedHostIDs = nil
		out.Effective.AllowedAccountIDs = nil
		out.Effective.WorkKindID = nil
	}
	return out, nil
}

func (m *Module) leadSettings(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	p, ok := principal(w, r)
	if !ok {
		return
	}
	project := r.PathValue("projectId")
	if project != "" && !uuidRE.MatchString(project) {
		writePreferenceError(w, prefFail(400, "invalid_project_id"))
		return
	}
	var out LeadSettings
	err := m.in(r.Context(), p.TenantID, func(tx pgx.Tx) error {
		var err error
		out, err = LoadLeadSettingsTx(r.Context(), tx, p, project)
		return err
	})
	if err != nil {
		writePreferenceError(w, err)
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, out)
}

func (m *Module) writeLeadSettings(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	if p.Kind != tenant.Person {
		writePreferenceError(w, prefFail(403, "person_required"))
		return
	}
	project := r.PathValue("projectId")
	if project != "" && !uuidRE.MatchString(project) {
		writePreferenceError(w, prefFail(400, "invalid_project_id"))
		return
	}
	var in struct {
		Revision  *int64      `json:"revision"`
		Overrides *LeadPolicy `json:"overrides"`
	}
	var revision int64
	var policy LeadPolicy
	var err error
	if r.Method == http.MethodDelete {
		revision, err = requestedRevision(r)
	} else {
		r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
		dec := json.NewDecoder(r.Body)
		dec.DisallowUnknownFields()
		err = dec.Decode(&in)
		if err != nil {
			err = prefFail(400, "invalid_json")
		} else if dec.Decode(new(any)) != io.EOF {
			err = prefFail(400, "invalid_json")
		}
		if err == nil {
			if in.Revision == nil || *in.Revision < 0 {
				err = prefFail(400, "revision_required")
			} else if in.Overrides == nil {
				err = prefFail(400, "overrides_required")
			} else {
				revision = *in.Revision
				policy = *in.Overrides
				err = validateLeadPolicy(policy)
			}
		}
	}
	if err != nil {
		writePreferenceError(w, err)
		return
	}
	var out LeadSettings
	err = m.in(r.Context(), p.TenantID, func(tx pgx.Tx) error {
		ctx := r.Context()
		if err := preferenceFence(ctx, tx, p); err != nil {
			return err
		}
		person, err := modelprefs.CanonicalPerson(ctx, tx, p.ID)
		if err != nil {
			return err
		}
		if person == nil {
			return prefFail(403, "person_required")
		}
		if project != "" {
			if err := readableProject(ctx, tx, p, project); err != nil {
				return err
			}
			var active bool
			if err := tx.QueryRow(ctx, `SELECT state='active' FROM nodes WHERE id=$1 FOR NO KEY UPDATE`, project).Scan(&active); err != nil {
				return err
			}
			if !active {
				return prefFail(409, "project_unavailable")
			}
		}
		if err := authz.RequireTx(ctx, tx, p, "model_prefs.manage", authz.Scope{ProjectID: project}); err != nil {
			return err
		}
		row, err := loadLeadRow(ctx, tx, project)
		if err != nil {
			return err
		}
		if row.Owner != nil {
			currentOwner, err := modelprefs.CanonicalPerson(ctx, tx, *row.Owner)
			if err != nil {
				return err
			}
			if currentOwner == nil || *currentOwner != *person {
				return prefFail(403, "owner_required")
			}
		}
		if row.Revision != revision {
			return prefFail(409, "stale_revision")
		}
		if err := validateLeadBindings(ctx, tx, policy, project, person); err != nil {
			return err
		}
		var owner *string
		if project != "" {
			owner = person
		}
		raw, err := json.Marshal(policy)
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `INSERT INTO project_lead_settings(tenant_id,project_id,owner_person_id,overrides,updated_by)
   VALUES($1,$2,$3,$4,$5)
   ON CONFLICT (tenant_id,(coalesce(project_id,'00000000-0000-0000-0000-000000000000'::uuid))) DO UPDATE
   SET overrides=EXCLUDED.overrides,revision=project_lead_settings.revision+1,updated_by=EXCLUDED.updated_by,updated_at=clock_timestamp()`, p.TenantID, optionalUUID(project), owner, raw, p.ID)
		if err != nil {
			return err
		}
		out, err = LoadLeadSettingsTx(ctx, tx, p, project)
		if err != nil {
			return err
		}
		// Public audit records revisions and redacted policy only, never private
		// host/account selectors. All queries/writes finish before event counter.
		_, err = events.Append(ctx, tx, p, events.Change{NodeID: func() *string {
			if project == "" {
				return nil
			}
			return &project
		}(), Type: "lead.settings_changed", Before: map[string]any{"revision": row.Revision}, After: map[string]any{"revision": out.Revision, "reset": r.Method == http.MethodDelete}})
		return err
	})
	if err != nil {
		writePreferenceError(w, err)
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, out)
}

func validateLeadBindings(ctx context.Context, tx pgx.Tx, p LeadPolicy, project string, person *string) error {
	if p.WorkKindID != nil {
		if _, err := preferenceKind(ctx, tx, *p.WorkKindID, func() string {
			if project == "" {
				return "default"
			}
			return "project"
		}(), project); err != nil {
			return err
		}
	}
	for _, targets := range []struct {
		ids *[]string
		sql string
	}{
		{p.AllowedAccountIDs, `SELECT count(*) FROM agent_accounts a WHERE a.id=ANY($1::uuid[]) AND a.archived_at IS NULL AND ($2::uuid IS NULL OR ` + modelprefs.CanonicalPersonSQL("a.owner_person_id") + `=$2::uuid)`},
		{p.AllowedHostIDs, `SELECT count(*) FROM agent_pairing_computers c JOIN agent_pairing_requests r ON r.tenant_id=c.tenant_id AND r.id=c.request_id WHERE c.id=ANY($1::uuid[]) AND c.state<>'revoked' AND ($2::uuid IS NULL OR ` + modelprefs.CanonicalPersonSQL("r.approved_by") + `=$2::uuid)`},
	} {
		if targets.ids == nil {
			continue
		}
		owner := person
		var count int
		if err := tx.QueryRow(ctx, targets.sql, *targets.ids, owner).Scan(&count); err != nil {
			return err
		}
		if count != len(*targets.ids) {
			return prefFail(422, "invalid_owner_binding")
		}
	}
	return nil
}
