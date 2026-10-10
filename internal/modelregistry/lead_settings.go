// SPDX-License-Identifier: AGPL-3.0-only
package modelregistry

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/events"
	"github.com/inspr-at/paimos/internal/httpapi"
	"github.com/inspr-at/paimos/internal/modelprefs"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/inspr-at/paimos/internal/workorders"
	"github.com/jackc/pgx/v5"
)

// LeadRecovery caps the entire build/review/fix lineage. It does not authorize
// restarts, credential rotation, attached reconnects or publication.
type LeadRecovery struct {
	MaxAttempts int     `json:"max_attempts"`
	AgentHours  float64 `json:"agent_hours"`
}

// UnmarshalJSON keeps the wire contract explicit: both ceilings are required,
// including when the person disables recovery with zeroes.
func (r *LeadRecovery) UnmarshalJSON(raw []byte) error {
	var in struct {
		MaxAttempts *int     `json:"max_attempts"`
		AgentHours  *float64 `json:"agent_hours"`
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&in); err != nil {
		return err
	}
	if in.MaxAttempts == nil || in.AgentHours == nil {
		return errors.New("recovery ceilings required")
	}
	r.MaxAttempts = *in.MaxAttempts
	r.AgentHours = *in.AgentHours
	return nil
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
	// Stored selectors may outlive a work kind. Keep the policy readable and
	// resettable, but never substitute "other" for an unavailable chosen kind.
	kind := "other"
	selectorAvailable := true
	if out.Effective.WorkKindID != nil {
		level := "project"
		if project == "" {
			level = "default"
		}
		k, err := preferenceKind(ctx, tx, *out.Effective.WorkKindID, level, project)
		if err != nil {
			var unavailable *preferenceError
			if !errors.As(err, &unavailable) || (unavailable.code != "unknown_kind" && unavailable.code != "kind_not_in_project") {
				return out, err
			}
			selectorAvailable = false
			out.WaitReason = "selector_unavailable"
		} else {
			kind = k.Slug
		}
	}
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
				if selectorAvailable {
					selector := modelprefs.ResolveCell(chain, kind, *out.Effective.Bucket)
					out.ModelSelector = &selector
				}
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

// LoadRoutineLeadPolicyTx resolves the same owner/account/host intersection as
// lead settings, without exposing its private selectors to the requesting
// principal. Consumers retain this value internally and fence final writes.
func LoadRoutineLeadPolicyTx(ctx context.Context, tx pgx.Tx, tenantID, project string) (LeadSettings, error) {
	row, err := loadLeadRow(ctx, tx, project)
	if err != nil {
		return LeadSettings{}, err
	}
	if row.Owner == nil {
		return LeadSettings{}, authz.ErrForbidden
	}
	owner, err := modelprefs.CanonicalPerson(ctx, tx, *row.Owner)
	if err != nil {
		return LeadSettings{}, err
	}
	if owner == nil || *owner != *row.Owner {
		return LeadSettings{}, authz.ErrForbidden
	}
	p := tenant.Principal{ID: *owner, TenantID: tenantID, Kind: tenant.Person}
	for _, permission := range []string{"harness.control", "run.create"} {
		if err := authz.RequireTx(ctx, tx, p, permission, authz.Scope{ProjectID: project}); err != nil {
			return LeadSettings{}, err
		}
	}
	out, err := LoadLeadSettingsTx(ctx, tx, p, project)
	if err == nil && (out.DetailsRedacted || out.WaitReason == "owner_unavailable" || out.WaitReason == "selector_unavailable") {
		err = authz.ErrForbidden
	}
	return out, err
}

// ExecutionRuntime contains trusted, exact deployed artifact/capability/host
// pins, including adapter/browser/model versions in the capability digest.
// The reader is server-owned, bounded, and performs only local/transactional
// reads: no network, model calls, events or locks after the event counter.
// Missing observations never inherit the qualification's own assertions.
type ExecutionRuntime struct {
	ServerDigest      string   `json:"server_digest"`
	DaemonDigest      string   `json:"daemon_digest"`
	CapabilityDigest  string   `json:"capability_digest"`
	Capabilities      []string `json:"capabilities"`
	HostMappingDigest string   `json:"host_mapping_digest"`
	BudgetModes       []string `json:"budget_modes"`
	// ObservedAt is required on live facts, and is not part of qualification.
	ObservedAt time.Time `json:"-"`
}
type ExecutionRuntimeReader func(context.Context, pgx.Tx, string) (ExecutionRuntime, error)

type ExecutionSettings struct {
	Revision               int64   `json:"revision"`
	AutomaticLaunchEnabled bool    `json:"automatic_launch_enabled"`
	ConsentEnabled         bool    `json:"consent_enabled"`
	QualificationID        *string `json:"qualification_id"`
	WaitReason             string  `json:"wait_reason"`
}

// Qualification is immutable accepted evidence, never a caller-supplied grant.
// Only RecordQualificationTx (controlled release authority) persists it; the
// settings/consent APIs accept only its ID. Evidence is a redacted SHA-256 pin.
type Qualification struct {
	ID                    string
	ProjectID             string
	OwnerPersonID         string
	PolicyDigest          string
	Runtime               ExecutionRuntime
	CoordinatorAcceptance string
	OPSAttestation        string
}

func RoutinePolicyDigest(value any) (string, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}
func shaPin(s string) bool {
	if len(s) != 64 || strings.ToLower(s) != s {
		return false
	}
	_, err := hex.DecodeString(s)
	return err == nil
}
func validRuntime(r ExecutionRuntime) bool {
	for _, pin := range []string{r.ServerDigest, r.DaemonDigest, r.CapabilityDigest, r.HostMappingDigest} {
		if !shaPin(pin) {
			return false
		}
	}
	if len(r.BudgetModes) < 1 || len(r.BudgetModes) > 4 {
		return false
	}
	if len(r.Capabilities) < 1 || len(r.Capabilities) > 2 {
		return false
	}
	capabilities := map[string]bool{}
	for _, capability := range r.Capabilities {
		if capabilities[capability] || !slices.Contains([]string{"routine_native_coding_v1", "routine_native_browser_v1"}, capability) {
			return false
		}
		capabilities[capability] = true
	}
	seen := map[string]bool{}
	for _, mode := range r.BudgetModes {
		if seen[mode] || !slices.Contains([]string{"off", "tokens", "money", "both"}, mode) {
			return false
		}
		seen[mode] = true
	}
	return true
}
func runtimeMatches(a, b ExecutionRuntime) bool {
	if !validRuntime(a) || !validRuntime(b) {
		return false
	}
	aa, bb := slices.Clone(a.BudgetModes), slices.Clone(b.BudgetModes)
	slices.Sort(aa)
	slices.Sort(bb)
	ac, bc := slices.Clone(a.Capabilities), slices.Clone(b.Capabilities)
	slices.Sort(ac)
	slices.Sort(bc)
	return a.ServerDigest == b.ServerDigest && a.DaemonDigest == b.DaemonDigest && a.CapabilityDigest == b.CapabilityDigest && a.HostMappingDigest == b.HostMappingDigest && slices.Equal(aa, bb) && slices.Equal(ac, bc)
}

// FenceExecutionTx is the final-write entry: tenant -> tree -> project row.
// Access changes and identity linking serialize on the same tenant fence.
func FenceExecutionTx(ctx context.Context, tx pgx.Tx, tenantID, project string) error {
	if !workorders.UUID(project) {
		return workorders.Fail(400, "invalid project id")
	}
	var matches bool
	if err := tx.QueryRow(ctx, `SELECT current_setting('aeon.tenant_id')=$1`, tenantID).Scan(&matches); err != nil {
		return err
	}
	if !matches {
		return authz.ErrForbidden
	}
	if err := db.LockWorkTreeTx(ctx, tx); err != nil {
		return err
	}
	var active bool
	if err := tx.QueryRow(ctx, `SELECT n.state<>'archived' FROM nodes n JOIN node_kinds k ON k.tenant_id=n.tenant_id AND k.id=n.kind_id WHERE n.id=$1 AND n.deleted_at IS NULL AND k.slug='project' FOR NO KEY UPDATE OF n`, project).Scan(&active); err != nil {
		return err
	}
	if !active {
		return workorders.Fail(409, "project_unavailable")
	}
	return nil
}

// QualificationPolicyTx returns only a digest of live private policy, including
// workspace/project/model revisions. The raw account/host selectors stay local.
func QualificationPolicyTx(ctx context.Context, tx pgx.Tx, tenantID, project string) (string, string, error) {
	policy, err := LoadRoutineLeadPolicyTx(ctx, tx, tenantID, project)
	if err != nil {
		return "", "", err
	}
	pin, err := RoutinePolicyDigest(policy)
	return *policy.OwnerPersonID, pin, err
}

func LoadRoutineQualificationTx(ctx context.Context, tx pgx.Tx, project, id string) (Qualification, error) {
	var q Qualification
	var modes, capabilities []byte
	err := tx.QueryRow(ctx, `SELECT id::text,project_id::text,owner_person_id::text,policy_digest,server_digest,daemon_digest,capability_digest,host_mapping_digest,budget_modes,capabilities,coordinator_acceptance,ops_attestation FROM routine_execution_qualifications WHERE id=$1 AND project_id=$2 AND revoked_at IS NULL`, id, project).Scan(&q.ID, &q.ProjectID, &q.OwnerPersonID, &q.PolicyDigest, &q.Runtime.ServerDigest, &q.Runtime.DaemonDigest, &q.Runtime.CapabilityDigest, &q.Runtime.HostMappingDigest, &modes, &capabilities, &q.CoordinatorAcceptance, &q.OPSAttestation)
	if err == nil {
		if len(modes) > 1024 || len(capabilities) > 1024 {
			return q, workorders.Fail(409, "qualification_unavailable")
		}
		err = json.Unmarshal(modes, &q.Runtime.BudgetModes)
	}
	if err == nil {
		err = json.Unmarshal(capabilities, &q.Runtime.Capabilities)
	}
	return q, err
}

func qualifiedTx(ctx context.Context, tx pgx.Tx, tenantID, project, id string, reader ExecutionRuntimeReader) (Qualification, string, error) {
	q, err := LoadRoutineQualificationTx(ctx, tx, project, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return q, "qualification_unavailable", nil
	}
	if err != nil {
		return q, "", err
	}
	if !shaPin(q.CoordinatorAcceptance) || !shaPin(q.OPSAttestation) {
		return q, "qualification_unaccepted", nil
	}
	owner, policy, err := QualificationPolicyTx(ctx, tx, tenantID, project)
	if errors.Is(err, authz.ErrForbidden) || errors.Is(err, pgx.ErrNoRows) {
		return q, "owner_unavailable", nil
	}
	if err != nil {
		return q, "", err
	}
	if q.OwnerPersonID != owner || q.PolicyDigest != policy {
		return q, "qualification_policy_changed", nil
	}
	if reader == nil {
		return q, "runtime_unavailable", nil
	}
	runtime, err := reader(ctx, tx, project)
	if err != nil || !validRuntime(runtime) {
		return q, "runtime_unavailable", nil
	}
	var now time.Time
	if err := tx.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&now); err != nil {
		return q, "", err
	}
	if runtime.ObservedAt.IsZero() || runtime.ObservedAt.After(now) || now.Sub(runtime.ObservedAt) > 2*time.Minute {
		return q, "runtime_unavailable", nil
	}
	if !runtimeMatches(q.Runtime, runtime) {
		return q, "qualification_runtime_changed", nil
	}
	return q, "", nil
}

// ProjectExecutionTx is shared by preview, save, lead projection and final
// claims. This is effective policy only; dial/admission/process/budget grants
// remain independently required. Reads do not modify in-flight holds.
func ProjectExecutionTx(ctx context.Context, tx pgx.Tx, tenantID, project string, reader ExecutionRuntimeReader) (ExecutionSettings, error) {
	out := ExecutionSettings{WaitReason: "automatic_launch_disabled"}
	err := tx.QueryRow(ctx, `SELECT revision,automatic_launch_enabled,qualification_id::text FROM project_routine_execution_settings WHERE project_id=$1`, project).Scan(&out.Revision, &out.ConsentEnabled, &out.QualificationID)
	if errors.Is(err, pgx.ErrNoRows) {
		return out, nil
	}
	if err != nil || !out.ConsentEnabled {
		return out, err
	}
	var active bool
	if err = tx.QueryRow(ctx, `SELECT state<>'archived' AND deleted_at IS NULL FROM nodes WHERE id=$1`, project).Scan(&active); err != nil {
		return out, err
	}
	if !active {
		out.WaitReason = "project_unavailable"
		return out, nil
	}
	if out.QualificationID == nil {
		out.WaitReason = "qualification_unavailable"
		return out, nil
	}
	_, out.WaitReason, err = qualifiedTx(ctx, tx, tenantID, project, *out.QualificationID, reader)
	out.AutomaticLaunchEnabled = err == nil && out.WaitReason == ""
	return out, err
}

func InteractivePerson(p tenant.Principal) bool {
	return p.Kind == tenant.Person && p.BrowserSession && p.KeyID == "" && p.AuthKeyID == ""
}
func RoutineAdministratorTx(ctx context.Context, tx pgx.Tx, p tenant.Principal, project string) (bool, error) {
	effective, err := authz.EffectiveTx(ctx, tx, p, project)
	admin := func(role *authz.RoleRef) bool { return role != nil && (role.Key == "owner" || role.Key == "admin") }
	return admin(effective.Workspace.Role) || effective.Project != nil && admin(effective.Project.Role), err
}

// RecordQualificationTx is an internal controlled-release seam, deliberately
// absent from the routine HTTP surface. Existing workspace releases.deploy
// authority and an interactive canonical person must record both separately
// obtained acceptance pins; routine/custom grants cannot attest qualification.
// The controller/OPS integration owns the evidence behind those immutable pins.
func RecordQualificationTx(ctx context.Context, tx pgx.Tx, p tenant.Principal, q Qualification) error {
	if !InteractivePerson(p) {
		return authz.ErrForbidden
	}
	if err := FenceExecutionTx(ctx, tx, p.TenantID, q.ProjectID); err != nil {
		return err
	}
	canonical, err := routineCanonicalPerson(ctx, tx, p)
	if err != nil {
		return err
	}
	if canonical != p.ID {
		return authz.ErrForbidden
	}
	if err := authz.RequireTx(ctx, tx, p, "releases.deploy", authz.Scope{}); err != nil {
		return err
	}
	admin, err := RoutineAdministratorTx(ctx, tx, p, "")
	if err != nil {
		return err
	}
	if !admin {
		return authz.ErrForbidden
	}
	if !workorders.UUID(q.ID) || !workorders.UUID(q.OwnerPersonID) || !shaPin(q.PolicyDigest) || !validRuntime(q.Runtime) || !shaPin(q.CoordinatorAcceptance) || !shaPin(q.OPSAttestation) {
		return workorders.Fail(400, "invalid_qualification")
	}
	owner, policy, err := QualificationPolicyTx(ctx, tx, p.TenantID, q.ProjectID)
	if err != nil {
		return err
	}
	if q.OwnerPersonID != owner || q.PolicyDigest != policy {
		return workorders.Fail(409, "qualification_policy_changed")
	}
	modes, _ := json.Marshal(q.Runtime.BudgetModes)
	capabilities, _ := json.Marshal(q.Runtime.Capabilities)
	_, err = tx.Exec(ctx, `INSERT INTO routine_execution_qualifications(tenant_id,id,project_id,owner_person_id,policy_digest,server_digest,daemon_digest,capability_digest,host_mapping_digest,budget_modes,capabilities,coordinator_acceptance,ops_attestation,recorded_by) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14)`, p.TenantID, q.ID, q.ProjectID, q.OwnerPersonID, q.PolicyDigest, q.Runtime.ServerDigest, q.Runtime.DaemonDigest, q.Runtime.CapabilityDigest, q.Runtime.HostMappingDigest, modes, capabilities, q.CoordinatorAcceptance, q.OPSAttestation, p.ID)
	if err != nil {
		return err
	}
	_, err = events.Append(ctx, tx, p, events.Change{NodeID: &q.ProjectID, Type: "routine.qualification_recorded", After: map[string]any{"qualification_id": q.ID}})
	return err
}

type ExecutionSettingsInput struct {
	ExpectedRevision       *int64  `json:"expected_revision"`
	AutomaticLaunchEnabled *bool   `json:"automatic_launch_enabled"`
	QualificationID        *string `json:"qualification_id"`
}

// WriteExecutionSettingsTx takes its fences before current authority and rows.
// No process control or accounting deletion is performed on disable.
func WriteExecutionSettingsTx(ctx context.Context, tx pgx.Tx, p tenant.Principal, project string, in ExecutionSettingsInput, reader ExecutionRuntimeReader) (ExecutionSettings, error) {
	var out ExecutionSettings
	if !InteractivePerson(p) {
		return out, workorders.Fail(403, "interactive_person_required")
	}
	if in.ExpectedRevision == nil || *in.ExpectedRevision < 0 || in.AutomaticLaunchEnabled == nil || in.QualificationID != nil && !workorders.UUID(*in.QualificationID) {
		return out, workorders.Fail(400, "invalid_execution_settings")
	}
	if err := FenceExecutionTx(ctx, tx, p.TenantID, project); err != nil {
		return out, err
	}
	person, err := routineCanonicalPerson(ctx, tx, p)
	if err != nil {
		return out, err
	}
	for _, permission := range []string{"harness.control", "run.create"} {
		if err := authz.RequireTx(ctx, tx, p, permission, authz.Scope{ProjectID: project}); err != nil {
			return out, err
		}
	}
	admin, err := RoutineAdministratorTx(ctx, tx, p, project)
	if err != nil {
		return out, err
	}
	var owner *string
	err = tx.QueryRow(ctx, `SELECT owner_person_id::text FROM project_lead_settings WHERE project_id=$1`, project).Scan(&owner)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return out, err
	}
	if !admin && (owner == nil || *owner != person) {
		return out, authz.ErrForbidden
	}
	var revision int64
	err = tx.QueryRow(ctx, `SELECT revision FROM project_routine_execution_settings WHERE project_id=$1 FOR NO KEY UPDATE`, project).Scan(&revision)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return out, err
	}
	if revision != *in.ExpectedRevision {
		return out, workorders.Fail(409, "stale_revision")
	}
	if *in.AutomaticLaunchEnabled {
		if in.QualificationID == nil {
			return out, workorders.Fail(409, "qualification_unavailable")
		}
		_, reason, err := qualifiedTx(ctx, tx, p.TenantID, project, *in.QualificationID, reader)
		if err != nil {
			return out, err
		}
		if reason != "" {
			return out, workorders.Fail(409, reason)
		}
	}
	_, err = tx.Exec(ctx, `INSERT INTO project_routine_execution_settings(tenant_id,project_id,automatic_launch_enabled,qualification_id,updated_by) VALUES($1,$2,$3,$4,$5) ON CONFLICT(tenant_id,project_id) DO UPDATE SET revision=project_routine_execution_settings.revision+1,automatic_launch_enabled=EXCLUDED.automatic_launch_enabled,qualification_id=EXCLUDED.qualification_id,updated_by=EXCLUDED.updated_by,updated_at=clock_timestamp()`, p.TenantID, project, *in.AutomaticLaunchEnabled, in.QualificationID, p.ID)
	if err != nil {
		return out, err
	}
	out, err = ProjectExecutionTx(ctx, tx, p.TenantID, project, reader)
	if err != nil {
		return out, err
	}
	_, err = events.Append(ctx, tx, p, events.Change{NodeID: &project, Type: "routine.execution_settings_changed", Before: map[string]any{"revision": revision}, After: out})
	return out, err
}

func routineCanonicalPerson(ctx context.Context, tx pgx.Tx, p tenant.Principal) (string, error) {
	id, err := modelprefs.CanonicalPerson(ctx, tx, p.ID)
	if err != nil {
		return "", err
	}
	if p.Kind != tenant.Person || id == nil {
		return "", authz.ErrForbidden
	}
	return *id, nil
}
