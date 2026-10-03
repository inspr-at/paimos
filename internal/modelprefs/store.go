// SPDX-License-Identifier: AGPL-3.0-only
package modelprefs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/inspr-at/paimos/internal/events"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

// CanonicalPersonSQL accepts a trusted SQL expression, never request text.
// Every person key uses the same active-person lookup, including stored stamps.
func CanonicalPersonSQL(expr string) string {
	return `(SELECT coalesce(cp.linked_to,cp.id) FROM principals cp WHERE cp.id=(` + expr + `)::uuid AND cp.kind='person' AND cp.status='active')`
}
func CanonicalPerson(ctx context.Context, tx pgx.Tx, id string) (*string, error) {
	cache, _ := ctx.Value(chainCacheKey{}).(*readCache)
	if cache != nil {
		if person, ok := cache.people[id]; ok {
			return copyPerson(person), nil
		}
	}
	var out *string
	err := tx.QueryRow(ctx, `SELECT `+CanonicalPersonSQL("$1")+`::text`, optional(id)).Scan(&out)
	if err == nil && cache != nil {
		cache.people[id] = copyPerson(out)
	}
	return out, err
}

// PrefsPerson is the starter (or the key creator). An operator key has no
// You slice. Lookup misses do not change authorization or return a 403.
func PrefsPerson(ctx context.Context, tx pgx.Tx, p tenant.Principal) *string {
	id := p.ID
	if p.Kind == tenant.Agent {
		id = p.KeyCreatorID
	}
	if id == "" {
		return nil
	}
	person, _ := CanonicalPerson(ctx, tx, id)
	return person
}
func optional(id string) any {
	if id == "" {
		return nil
	}
	return id
}

// scopesJSON is correlated to keys(person_id,project_id); it loads the entire
// sparse chain with one statement and ignores archived and foreign kinds.
const scopesJSON = `coalesce((SELECT jsonb_agg(jsonb_build_object(
 'id',s.id,'level',s.level,'person_id',s.person_id,'project_id',s.project_id,
 'residency',s.residency,'residency_locked',s.residency_locked,'prefs_locked',s.prefs_locked,'revision',s.revision,
 'rows',coalesce((SELECT jsonb_object_agg(k.slug,jsonb_build_object('locked',r.locked,'cells',
  coalesce((SELECT jsonb_object_agg(c.bucket,jsonb_strip_nulls(jsonb_build_object('mode',c.mode,'profile_id',c.profile_id,
   'family',c.family,'line',c.line,'effort',c.effort,'harness',c.harness)))
   FROM model_pref_cells c WHERE c.tenant_id=r.tenant_id AND c.scope_id=r.scope_id AND c.kind_id=r.kind_id),'{}'::jsonb)))
  FROM model_pref_rows r JOIN work_kinds k ON k.tenant_id=r.tenant_id AND k.id=r.kind_id
  WHERE r.tenant_id=s.tenant_id AND r.scope_id=s.id AND k.archived_at IS NULL
   AND (k.project_id IS NULL OR k.project_id=keys.project_id)),'{}'::jsonb)))
 FROM model_pref_scopes s WHERE s.level='default' OR (s.level='person' AND s.person_id=keys.person_id)
 OR (s.level='project' AND s.project_id=keys.project_id)), '[]'::jsonb)`

func orderedChain(raw []byte) ([]Scope, error) {
	var loaded []Scope
	if err := json.Unmarshal(raw, &loaded); err != nil {
		return nil, err
	}
	out := []Scope{{Level: "default", Rows: map[string]Row{}}, {Level: "person", Rows: map[string]Row{}}, {Level: "project", Rows: map[string]Row{}}}
	for _, s := range loaded {
		for i := range out {
			if out[i].Level == s.Level {
				out[i] = s
			}
		}
	}
	return out, nil
}
func LoadChain(ctx context.Context, tx pgx.Tx, personID *string, projectID string) ([]Scope, error) {
	read, _ := ctx.Value(chainCacheKey{}).(*readCache)
	var cache map[string][]Scope
	if read != nil {
		cache = read.chains
	}
	key := projectID + "|"
	if personID != nil {
		key += *personID
	}
	if chain, ok := cache[key]; ok {
		return chain, nil
	}
	var raw []byte
	err := tx.QueryRow(ctx, `SELECT `+scopesJSON+` FROM (SELECT `+CanonicalPersonSQL("$1")+` AS person_id,
 (SELECT n.id FROM nodes n JOIN node_kinds k ON k.tenant_id=n.tenant_id AND k.id=n.kind_id
  WHERE n.id=$2::uuid AND k.slug='project' AND n.deleted_at IS NULL) AS project_id) keys`, personID, optional(projectID)).Scan(&raw)
	if err != nil {
		return nil, err
	}
	chain, err := orderedChain(raw)
	if err == nil && cache != nil {
		cache[key] = chain
	}
	return chain, err
}

type chainCacheKey struct{}
type kindCacheKey struct{ area, project string }
type kindLookup struct {
	kind     Kind
	fallback bool
}
type readCache struct {
	chains map[string][]Scope
	people map[string]*string
	kinds  map[kindCacheKey]kindLookup
}

func copyPerson(person *string) *string {
	if person == nil {
		return nil
	}
	id := *person
	return &id
}

// WithChainCache is for one read-only transaction. It shares scope, canonical
// person and (area, project) kind reads. Discard it before a preference write
// or another transaction; cached scopes and kinds are immutable.
func WithChainCache(ctx context.Context) context.Context {
	return context.WithValue(ctx, chainCacheKey{}, &readCache{
		chains: map[string][]Scope{}, people: map[string]*string{}, kinds: map[kindCacheKey]kindLookup{},
	})
}

type Kind struct {
	ID        string  `json:"id"`
	Slug      string  `json:"slug"`
	Label     string  `json:"label"`
	Hint      string  `json:"hint"`
	ProjectID *string `json:"project_id,omitempty"`
	System    *string `json:"system,omitempty"`
	Position  int     `json:"position"`
}

func LookupKind(ctx context.Context, tx pgx.Tx, area, projectID string) (Kind, bool, error) {
	if area == "review" || area == "other" {
		area = ""
	}
	cache, _ := ctx.Value(chainCacheKey{}).(*readCache)
	key := kindCacheKey{area, projectID}
	if cache != nil {
		if found, ok := cache.kinds[key]; ok {
			return found.kind, found.fallback, nil
		}
	}
	var kind Kind
	err := tx.QueryRow(ctx, `SELECT id::text,slug,label,hint,project_id::text,system,position FROM work_kinds
 WHERE archived_at IS NULL AND ((slug=$1 AND (project_id IS NULL OR project_id=$2::uuid)) OR slug='other')
 ORDER BY (slug=$1) DESC,(project_id IS NOT NULL) DESC LIMIT 1`, area, optional(projectID)).
		Scan(&kind.ID, &kind.Slug, &kind.Label, &kind.Hint, &kind.ProjectID, &kind.System, &kind.Position)
	if errors.Is(err, pgx.ErrNoRows) {
		kind, err = Kind{Slug: "other"}, nil
	}
	fallback := kind.Slug != area
	if err == nil && cache != nil {
		cache.kinds[key] = kindLookup{kind, fallback}
	}
	return kind, fallback, err
}
func SeedKinds(ctx context.Context, tx pgx.Tx, tenantID string) error {
	_, err := tx.Exec(ctx, `SELECT aeon_seed_work_kinds($1::uuid)`, tenantID)
	return err
}
func Requirement(ctx context.Context, tx pgx.Tx, personID *string, projectID, ticketRequirement string) (string, error) {
	requirement, _, err := requirementTrace(ctx, tx, personID, projectID, ticketRequirement)
	return requirement, err
}

// RequirementTrace retains the residency decision as well as the enforced floor.
// A stamp alone cannot explain overrides that loosen an inherited lock.
type RequirementTrace struct {
	PersonID  *string         `json:"person_id"`
	Residency ResidencyResult `json:"residency"`
}

func requirementTrace(ctx context.Context, tx pgx.Tx, personID *string, projectID, ticketRequirement string) (string, RequirementTrace, error) {
	chain, err := LoadChain(ctx, tx, personID, projectID)
	if err != nil {
		return "", RequirementTrace{}, err
	}
	trace := RequirementTrace{PersonID: personID, Residency: ResolveResidency(chain)}
	return Strictest(trace.Residency.Value, ticketRequirement), trace, nil
}

// withRoutingVisibility reads only routing metadata under tenant RLS. Routing
// daemons need the requirement even without a project grant. Restore the exact
// project setting before returning; this does not authorize a node response.
func withRoutingVisibility(ctx context.Context, tx pgx.Tx, fn func() error) error {
	var prior string
	if err := tx.QueryRow(ctx, `SELECT coalesce(current_setting('aeon.visible_projects',true),'')`).Scan(&prior); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `SELECT set_config('aeon.visible_projects','*',true)`); err != nil {
		return err
	}
	err := fn()
	_, restoreErr := tx.Exec(ctx, `SELECT set_config('aeon.visible_projects',$1,true)`, prior)
	return errors.Join(err, restoreErr)
}

// OrderRequirement returns the live ticket floor for the work-order ancestry.
// AEON-473 may supply residency presets; absence is any on day one.
func OrderRequirement(ctx context.Context, tx pgx.Tx, orderID string, personID *string) (string, error) {
	requirement, _, err := OrderRequirementTrace(ctx, tx, orderID, personID)
	return requirement, err
}

func OrderRequirementTrace(ctx context.Context, tx pgx.Tx, orderID string, personID *string) (string, RequirementTrace, error) {
	var project *string
	var fields []byte
	err := tx.QueryRow(ctx, `WITH RECURSIVE up AS (
  SELECT n.id,n.parent_id,n.project_id,n.fields,k.slug,0 AS depth FROM nodes n
  JOIN node_kinds k ON k.tenant_id=n.tenant_id AND k.id=n.kind_id WHERE n.id=$1::uuid
  UNION ALL SELECT n.id,n.parent_id,n.project_id,n.fields,k.slug,up.depth+1 FROM up
  JOIN nodes n ON n.id=up.parent_id JOIN node_kinds k ON k.tenant_id=n.tenant_id AND k.id=n.kind_id
  WHERE up.slug<>'ticket' AND up.depth<32)
 SELECT (SELECT project_id::text FROM up ORDER BY depth LIMIT 1),
 coalesce((SELECT fields FROM up WHERE slug='ticket' ORDER BY depth LIMIT 1),'{}'::jsonb)`, orderID).Scan(&project, &fields)
	if err != nil {
		return "", RequirementTrace{}, err
	}
	pid := ""
	if project != nil {
		pid = *project
	}
	return requirementTrace(ctx, tx, personID, pid, PlacementFields(fields).Residency)
}

type RunPolicy struct {
	Residency         string
	PersonID          *string // Historical starter, copied verbatim by retries.
	CanonicalPersonID *string
}

// RunRequirement maps the stored starter on every read, and computes the live
// chain plus ticket requirement in one query. The saved stamp is always a floor.
func RunRequirement(ctx context.Context, tx pgx.Tx, runID string) (RunPolicy, error) {
	var out RunPolicy
	var stamp string
	var fields, raw []byte
	err := withRoutingVisibility(ctx, tx, func() error {
		return tx.QueryRow(ctx, `WITH RECURSIVE up AS (
   SELECT n.id,n.parent_id,n.project_id,n.fields,k.slug,0 AS depth FROM agent_runs r
   JOIN nodes n ON n.tenant_id=r.tenant_id AND n.id=r.work_order_id
   JOIN node_kinds k ON k.tenant_id=n.tenant_id AND k.id=n.kind_id WHERE r.id=$1::uuid
   UNION ALL SELECT n.id,n.parent_id,n.project_id,n.fields,k.slug,up.depth+1 FROM up
   JOIN nodes n ON n.id=up.parent_id JOIN node_kinds k ON k.tenant_id=n.tenant_id AND k.id=n.kind_id
   WHERE up.slug<>'ticket' AND up.depth<32), keys AS (
   SELECT `+CanonicalPersonSQL("r.prefs_person_id")+` AS person_id,
   (SELECT n.id FROM up JOIN nodes n ON n.id=up.project_id JOIN node_kinds k ON k.tenant_id=n.tenant_id AND k.id=n.kind_id
    WHERE k.slug='project' AND n.deleted_at IS NULL ORDER BY up.depth LIMIT 1) AS project_id,
   r.residency,r.prefs_person_id FROM agent_runs r WHERE r.id=$1::uuid)
  SELECT coalesce(keys.residency,'any'),keys.prefs_person_id::text,keys.person_id::text,
   coalesce((SELECT fields FROM up WHERE slug='ticket' ORDER BY depth LIMIT 1),'{}'::jsonb),`+scopesJSON+` FROM keys`, runID).
			Scan(&stamp, &out.PersonID, &out.CanonicalPersonID, &fields, &raw)
	})
	if err != nil {
		return out, err
	}
	chain, err := orderedChain(raw)
	if err != nil {
		return out, err
	}
	out.Residency = Strictest(stamp, Strictest(ResolveResidency(chain).Value, PlacementFields(fields).Residency))
	return out, nil
}

// SaveScope is an internal store operation. HTTP permission and revision gates
// belong to 502b. It serializes writers and durably re-stamps active runs before
// returning. Caller and audit actor remain the session principal.
func SaveScope(ctx context.Context, tx pgx.Tx, p tenant.Principal, s Scope) (Scope, error) {
	s, err := SaveScopeOnly(ctx, tx, p, s)
	if err != nil {
		return s, err
	}
	_, err = Restamp(ctx, tx, p, s)
	return s, err
}

// SaveScopeOnly lets an atomic editor write finish its rows before restamping
// runs and taking the event counter (the final lock in the transaction).
func SaveScopeOnly(ctx context.Context, tx pgx.Tx, p tenant.Principal, s Scope) (Scope, error) {
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('aeon-model-prefs:'||current_setting('aeon.tenant_id'),0))`); err != nil {
		return s, err
	}
	err := tx.QueryRow(ctx, `INSERT INTO model_pref_scopes(tenant_id,level,person_id,project_id,residency,residency_locked,prefs_locked,updated_by)
 VALUES($1,$2,$3,$4,$5,$6,$7,$8)
 ON CONFLICT (tenant_id,level,(coalesce(person_id,'00000000-0000-0000-0000-000000000000'::uuid)),(coalesce(project_id,'00000000-0000-0000-0000-000000000000'::uuid)))
 DO UPDATE SET residency=EXCLUDED.residency,residency_locked=EXCLUDED.residency_locked,prefs_locked=EXCLUDED.prefs_locked,
 revision=model_pref_scopes.revision+1,updated_by=EXCLUDED.updated_by,updated_at=now()
 RETURNING id::text,revision`, p.TenantID, s.Level, s.PersonID, s.ProjectID, s.Residency, s.ResidencyLocked, s.PrefsLocked, p.ID).Scan(&s.ID, &s.Revision)
	if err != nil {
		return s, err
	}
	return s, err
}
func PutRow(ctx context.Context, tx pgx.Tx, p tenant.Principal, scope Scope, kindID string, row Row) error {
	if _, err := tx.Exec(ctx, `INSERT INTO model_pref_rows(tenant_id,scope_id,kind_id,level,locked,updated_by)
 VALUES($1,$2,$3,$4,$5,$6) ON CONFLICT(tenant_id,scope_id,kind_id) DO UPDATE
 SET locked=EXCLUDED.locked,updated_by=EXCLUDED.updated_by,updated_at=now()`, p.TenantID, scope.ID, kindID, scope.Level, row.Locked, p.ID); err != nil {
		return err
	}
	for bucket, cell := range row.Cells {
		_, err := tx.Exec(ctx, `INSERT INTO model_pref_cells(tenant_id,scope_id,kind_id,bucket,mode,profile_id,family,line,effort,harness)
  VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10) ON CONFLICT(tenant_id,scope_id,kind_id,bucket) DO UPDATE
  SET mode=EXCLUDED.mode,profile_id=EXCLUDED.profile_id,family=EXCLUDED.family,line=EXCLUDED.line,effort=EXCLUDED.effort,harness=EXCLUDED.harness`,
			p.TenantID, scope.ID, kindID, bucket, cell.Mode, optional(cell.ProfileID), optional(cell.Family), optional(cell.Line), optional(cell.Effort), optional(cell.Harness))
		if err != nil {
			return err
		}
	}
	return nil
}

type RestampedRun struct {
	ID, Status, Residency string
	AccountID, ProfileID  *string
}

// activeScopeRuns locks the bounded active set before any updates or events.
func activeScopeRuns(ctx context.Context, tx pgx.Tx, scope Scope) ([]RestampedRun, error) {
	rows, err := tx.Query(ctx, `SELECT r.id::text,r.status,r.account_id::text,r.model_profile_id::text,coalesce(r.residency,'any')
   FROM agent_runs r JOIN nodes n ON n.tenant_id=r.tenant_id AND n.id=r.work_order_id
   WHERE r.status IN ('queued','starting','running','waiting') AND r.purpose='managed' AND
   ($1='default' OR ($1='person' AND `+CanonicalPersonSQL("r.prefs_person_id")+`=$2::uuid)
    OR ($1='project' AND n.project_id=$3::uuid)) ORDER BY r.id LIMIT 10001 FOR NO KEY UPDATE OF r`, scope.Level, scope.PersonID, scope.ProjectID)
	if err != nil {
		return nil, err
	}
	var runs []RestampedRun
	for rows.Next() {
		var r RestampedRun
		if err = rows.Scan(&r.ID, &r.Status, &r.AccountID, &r.ProfileID, &r.Residency); err != nil {
			rows.Close()
			return nil, err
		}
		runs = append(runs, r)
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		return nil, err
	}
	if len(runs) > 10000 {
		return nil, &ScopeTooLarge{}
	}
	return runs, nil
}

// SnapshotRunRequirements records the effective floor before a preference write.
// This prevents a loosening from catching up a stale stamp after a person link.
func SnapshotRunRequirements(ctx context.Context, tx pgx.Tx, scope Scope) (map[string]string, error) {
	before := map[string]string{}
	err := withRoutingVisibility(ctx, tx, func() error {
		runs, err := activeScopeRuns(ctx, tx, scope)
		if err != nil {
			return err
		}
		for _, run := range runs {
			policy, err := RunRequirement(ctx, tx, run.ID)
			if err != nil {
				return err
			}
			before[run.ID] = policy.Residency
		}
		return nil
	})
	return before, err
}

// Restamp includes pre-link starter ids through the canonical lookup. It never
// stops a turn; callers can report starting/running accounts outside the fence.
func Restamp(ctx context.Context, tx pgx.Tx, p tenant.Principal, scope Scope) ([]RestampedRun, error) {
	return RestampAfter(ctx, tx, p, scope, nil)
}

// RestampAfter only advances a stamp when the write tightens the previous live
// requirement. A nil snapshot preserves the internal catch-up operation.
func RestampAfter(ctx context.Context, tx pgx.Tx, p tenant.Principal, scope Scope, before map[string]string) ([]RestampedRun, error) {
	out := []RestampedRun{}
	err := withRoutingVisibility(ctx, tx, func() error {
		runs, err := activeScopeRuns(ctx, tx, scope)
		if err != nil {
			return err
		}
		changes := []events.Change{}
		for _, r := range runs {
			policy, err := RunRequirement(ctx, tx, r.ID)
			if err != nil {
				return err
			}
			if old, ok := before[r.ID]; ok && Strictness(policy.Residency) <= Strictness(old) {
				continue
			}
			if Strictness(policy.Residency) <= Strictness(r.Residency) {
				continue
			}
			before := r.Residency
			r.Residency = policy.Residency
			if _, err = tx.Exec(ctx, `UPDATE agent_runs SET residency=$2 WHERE id=$1 AND
    status IN ('queued','starting','running','waiting')`, r.ID, Stamp(r.Residency)); err != nil {
				return err
			}
			changes = append(changes, events.Change{Type: "run.residency_restamped", Before: map[string]string{"run_id": r.ID, "residency": before}, After: map[string]string{"run_id": r.ID, "residency": r.Residency}})
			out = append(out, r)
		}
		for _, change := range changes {
			if _, err := events.Append(ctx, tx, p, change); err != nil {
				return fmt.Errorf("record restamp: %w", err)
			}
		}
		return nil
	})
	return out, err
}
