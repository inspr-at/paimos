// SPDX-License-Identifier: AGPL-3.0-only

package rules

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/releasehistory"
	"github.com/inspr-at/paimos/internal/rules/doctrine"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/inspr-at/paimos/internal/workorders"
	"github.com/jackc/pgx/v5"
)

// MaxBatch bounds one batch publication. A tenant's whole rule store stays far below it.
const MaxBatch = 100

// AutoVersion asks the server to assign the calendar version at publication.
const AutoVersion = "auto"

type batchItem struct {
	SetID            string `json:"set_id"`
	ExpectedRevision int64  `json:"expected_revision"`
	Version          string `json:"version"`
}
type batchInput struct {
	Items []batchItem `json:"items"`
	Note  string      `json:"note"`
}

// BatchResult answers one batch publication: every set's immutable snapshot in
// request order, the shared batch id recorded on each set's event, and the
// largest merged file the batch produced for the checked contexts.
type BatchResult struct {
	BatchID  string     `json:"batch_id"`
	Versions []Snapshot `json:"versions"`
	MaxBytes int        `json:"max_bytes"`
}

// Placeholders stand for "a project without project rules" and "a person without
// person or agent rules" in the budget check; they never match a rule scope.
const (
	noProject = "00000000-0000-4000-8000-000000000000"
	noPerson  = "00000000-0000-4000-8000-000000000001"
)

// publishBatch publishes several sets under one person approval. It runs in the
// request transaction, which already holds the tenant rules lock and the tenant
// row lock that access changes take (see lockAccess), so the permission checks
// below read current grants and stay true until commit. All or nothing: every
// set is authorized, then revisions and rules are checked, before the first
// write; any later failure (a version conflict, the byte budget) rolls back all.
// The complete answer is stored with the writes; an exact replay by the same
// person returns it, after the same authorization, and writes nothing.
func (m *Module) publishBatch(r *http.Request, tx pgx.Tx, p tenant.Principal) (any, error) {
	if p.Kind != tenant.Person && !authz.OwnerWorkstation(p) {
		return nil, authz.ErrForbidden
	}
	var in batchInput
	if err := workorders.Decode(r, &in); err != nil {
		return nil, err
	}
	if len(in.Items) == 0 || len(in.Items) > MaxBatch {
		return nil, fail(400, "invalid_request", "a batch publishes 1 to 100 sets")
	}
	note, err := normalizeNote(in.Note)
	if err != nil {
		return nil, err
	}
	ctx := r.Context()
	seen := map[string]bool{}
	for _, item := range in.Items {
		// Only the canonical lowercase spelling is accepted, so one set can never
		// appear twice under two spellings (or be versioned twice in one batch).
		if !workorders.UUID(item.SetID) || strings.ToLower(item.SetID) != item.SetID {
			return nil, fail(400, "invalid_scope", "set ids must be canonical lowercase UUIDs")
		}
		if seen[item.SetID] {
			return nil, fail(400, "invalid_request", "a set appears more than once in the batch")
		}
		seen[item.SetID] = true
		if item.Version != AutoVersion && !releasehistory.ValidVersion(item.Version) {
			return nil, fail(400, "invalid_version", "version must be auto or a valid YYMMDDhhmmss.0.0 UTC calendar coordinate")
		}
	}
	owner, err := actorOwner(ctx, tx, p)
	if err != nil {
		return nil, err
	}
	// Authorization first, for every set, on the grants current under the lock.
	sets := make([]Set, len(in.Items))
	for i, item := range in.Items {
		s, err := loadSet(ctx, tx, item.SetID)
		if err != nil {
			return nil, err
		}
		if err = permission(ctx, tx, p, s.Scope, "rules.publish"); err != nil {
			return nil, err
		}
		sets[i] = s
	}
	digest := requestDigest(p.TenantID, owner, in.Items, note)
	onUncertain(r, owner, digest)
	if stored, found, err := storedBatch(ctx, tx, p.TenantID, owner, digest); err != nil || found {
		return stored, err
	}
	for i, item := range in.Items {
		if sets[i].Revision != item.ExpectedRevision {
			return nil, &Error{Status: 409, Code: "revision_conflict", Message: "draft revision does not match for set " + sets[i].Name}
		}
		if err := ValidateRules(sets[i].Rules); err != nil {
			return nil, err
		}
	}
	out := BatchResult{BatchID: uuidFrom(digest), Versions: make([]Snapshot, len(in.Items))}
	now := m.versionNow().UTC()
	for i, item := range in.Items {
		s := sets[i]
		version := item.Version
		if version == AutoVersion {
			version = nextVersion(now, s.PublishedVersion)
		} else if _, err := loadVersion(ctx, tx, s.ID, version); err == nil {
			// A new request never reuses a stored version: only a stored batch
			// answer is a replay, and that was answered above.
			return nil, &Error{Status: 409, Code: "version_conflict", Message: "version " + version + " is already published for set " + s.Name}
		} else if !errors.Is(err, pgx.ErrNoRows) {
			return nil, err
		}
		snap, err := publishSetIn(ctx, tx, p, s, item.ExpectedRevision, version, note, out.BatchID)
		if err != nil {
			return nil, err
		}
		out.Versions[i] = snap
	}
	limits, err := LoadBudget(ctx, tx)
	if err != nil {
		return nil, err
	}
	if out.MaxBytes, err = budgetCheck(ctx, tx, p, owner, sets, now, limits); err != nil {
		return nil, err
	}
	raw, err := json.Marshal(out)
	if err != nil {
		return nil, err
	}
	if err = beforeStore(ctx, tx); err != nil {
		return nil, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO rule_publish_batches(tenant_id,person_id,request_digest,batch_id,result) VALUES($1,$2,$3,$4,$5)`, p.TenantID, owner, digest, out.BatchID, raw); err != nil {
		return nil, err
	}
	return out, nil
}

// storedBatch returns the stored answer of an earlier identical request by this person.
func storedBatch(ctx context.Context, tx pgx.Tx, tenantID, owner string, digest []byte) (any, bool, error) {
	var raw []byte
	err := tx.QueryRow(ctx, `SELECT result FROM rule_publish_batches WHERE tenant_id=$1 AND person_id=$2 AND request_digest=$3`, tenantID, owner, digest).Scan(&raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	var out BatchResult
	if err = json.Unmarshal(raw, &out); err != nil {
		return nil, false, err
	}
	return out, true, nil
}

// requestDigest identifies one request by one person: tenant, person, the items
// in set order and the normalized note.
func requestDigest(tenantID, owner string, items []batchItem, note string) []byte {
	sorted := slices.Clone(items)
	slices.SortFunc(sorted, func(a, b batchItem) int { return strings.Compare(a.SetID, b.SetID) })
	raw, _ := json.Marshal(map[string]any{"tenant": tenantID, "person": owner, "items": sorted, "note": note})
	sum := sha256.Sum256(raw)
	return sum[:]
}

// uuidFrom derives the batch id from the request digest (UUID version 8).
func uuidFrom(digest []byte) string {
	raw := slices.Clone(digest[:16])
	raw[6] = raw[6]&0x0f | 0x80
	raw[8] = raw[8]&0x3f | 0x80
	h := hex.EncodeToString(raw)
	return h[0:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:32]
}

// nextVersion is now as a calendar version, or one second after the current
// publication when that is not earlier (versions must strictly increase).
func nextVersion(now time.Time, published string) string {
	format := func(t time.Time) string { return t.UTC().Format("060102150405") + ".0.0" }
	version := format(now)
	if published == "" || version > published {
		return version
	}
	last, err := time.Parse("060102150405", published[:12])
	if err != nil {
		return version
	}
	return format(last.Add(time.Second))
}

// The budget check's work is bounded three ways: by the request deadline,
// checked in every loop (loading, validation, authorization, enumeration and
// rendering) and enforced by the server (transaction_timeout); by the size of
// the store it may load (maxBudgetRules rules, an empty set counting as one,
// and maxBudgetBytes bytes of rule text across every snapshot it reads); and by
// the number of session files it may render (maxBudgetContexts). The caps and
// the server-side timeout are the protection; the measurements only show that
// ordinary stores stay far from them. Render cost depends on the store and the
// machine (a CI runner is several times slower than a workstation), so the caps
// keep the worst check to a fraction of the deadline there too, and a check
// that would still outrun it answers 503 and commits nothing
// (BenchmarkBudgetCheck, TestBudgetCapFitsTheDeadline). Past a bound the
// publication is refused (422 budget_check_too_large).
//
// Product limit: 1,000 files per check. A company-wide set touches every
// role and harness (20 files) of every project with project rules plus one
// without, for a person without person rules and for each person who has
// some, so one company set fits while (projects + 1) × (people with rules + 1)
// stays at or below 50. Variables so tests can change them; beforeRender and
// beforeStore are test hooks.
var (
	maxBudgetContexts       = 1000
	maxBudgetRules          = 2000
	maxBudgetBytes    int64 = 2 << 20
	beforeRender            = func() {}
	beforeStore             = func(context.Context, pgx.Tx) error { return nil }
)

// Errors of the budget check. The one about another person's context carries
// no size and no identity.
var (
	errHiddenBudget = &Error{Status: 422, Code: "rules_budget_exceeded", Message: "A session file for another person or agent would exceed the limit. Shorten always-on text or move it to details."}
	errTooManyCtx   = &Error{Status: 422, Code: "budget_check_too_large", Message: "This publication touches more session files (limit 1,000) or rules (limit 2,000) than one check can cover. Publish fewer sets together."}
	// One merged file is a single session file. The 1,000-file cap stays on
	// budgetCheck. This read refuses the same store caps that check uses.
	errMergeStoreTooLarge = &Error{Status: 422, Code: "budget_check_too_large", Message: "This session's rule snapshots exceed 2,000 rules or 2 MiB of rule text."}
)

// storeBudget applies the publication store caps (2,000 rules, 2 MiB of rule
// text) to snapshots already in memory. An empty snapshot still counts as one
// rule, matching budgetCheck. The merge handler calls admit after each read
// so a crossing snapshot is the last one loaded.
func storeBudget(ctx context.Context, snapshots []Snapshot) error {
	w := &work{ctx: ctx}
	for _, s := range snapshots {
		if err := w.admit(s); err != nil {
			return err
		}
	}
	return nil
}

// admit counts one snapshot against the running store total. Crossing 2,000
// rules or 2 MiB returns the merge refusal.
func (w *work) admit(s Snapshot) error {
	if err := w.load(s); err != nil {
		if err == errTooManyCtx {
			return errMergeStoreTooLarge
		}
		return err
	}
	return nil
}

// work tracks one budget check against the deadline and the store bounds.
type work struct {
	ctx   context.Context
	rules int
	bytes int64
}

// step is called once per iteration of every loop.
func (w *work) step() error {
	if expired(w.ctx) {
		return errStopped
	}
	return nil
}

// load counts one loaded snapshot against the store bounds.
func (w *work) load(s Snapshot) error {
	w.rules += max(len(s.Rules), 1) // an empty set still costs one step per render
	for _, r := range s.Rules {
		w.bytes += int64(len(r.Identity) + len(r.Text) + len(r.Why) + len(r.Details))
	}
	if w.rules > maxBudgetRules || w.bytes > maxBudgetBytes {
		return errTooManyCtx
	}
	return w.step()
}

// budgetCheck renders, after the batch's writes, every session file that
// includes one of the batch's sets, counting every existing contribution to
// that file, and fails with 422 when one exceeds the budget. Files the batch
// does not touch are neither rendered nor judged, so an oversized layer
// elsewhere never vetoes an unrelated publication.
//
// Candidate files: every project with published project rules and a project
// without any; every person with person or agent rules, other owners' private
// layers included, and a person without any; each owner's named agents and
// tasks; every role and harness.
//
// Other owners' layers are hidden from the caller by row-level security. To
// read them, the owner setting is pointed at one owner at a time and restored,
// in this same transaction, before anything is rendered or decided. The texts
// stay in memory and never leave this function. A file counts as the caller's
// own only when the caller holds rules.read, now (under the access lock), on
// every layer in it; sizes are reported only for such files, and any other
// file yields a generic refusal without size or identity.
func budgetCheck(ctx context.Context, tx pgx.Tx, p tenant.Principal, caller string, batch []Set, now time.Time, limits Budget) (int, error) {
	w := &work{ctx: ctx}
	shared, owners, projects, err := gatherBudget(w, tx)
	if err != nil {
		return 0, err
	}
	// Validate every snapshot once; a snapshot that fails its own integrity
	// check is left out here, and session start refuses it on its own.
	keep := func(list []Snapshot) ([]Snapshot, error) {
		out := []Snapshot{}
		for _, s := range list {
			if err := w.step(); err != nil {
				return nil, err
			}
			if validSnapshot(s) == nil {
				out = append(out, s)
			}
		}
		return out, nil
	}
	if shared, err = keep(shared); err != nil {
		return 0, err
	}
	for i := range owners {
		if owners[i].snapshots, err = keep(owners[i].snapshots); err != nil {
			return 0, err
		}
	}
	readable := map[string]bool{}
	for _, list := range append([][]Snapshot{shared}, ownerLists(owners)...) {
		for _, s := range list {
			if err := w.step(); err != nil {
				return 0, err
			}
			key := jsonDigest(s.Scope)
			if _, known := readable[key]; known {
				continue
			}
			err := permission(ctx, tx, p, s.Scope, "rules.read")
			if err != nil && !isDenied(err) {
				return 0, err
			}
			readable[key] = err == nil
		}
	}
	cat, err := loadDoctrineCatalog(ctx, tx)
	if err != nil {
		return 0, err
	}
	check := budget{work: w, tenant: p.TenantID, batch: batch, now: now, shared: sortSnapshots(shared), projects: projects, readable: readable, limits: limits, doctrine: cat}
	if err = check.person(noPerson, nil); err != nil {
		return 0, err
	}
	for _, o := range owners {
		if err = check.person(o.person, o.snapshots); err != nil {
			return 0, err
		}
	}
	if o := check.ownOver; o != nil {
		if o.Layer != "" {
			return 0, &Error{Status: 422, Code: "rules_budget_exceeded", Message: fmt.Sprintf("after this publication %s rules in one of your session files would need %d UTF-8 bytes (their cap is %d); shorten always-on text or move it to details", o.Layer, o.ActualBytes, o.MaxBytes), ActualBytes: o.ActualBytes, MaxBytes: o.MaxBytes, Layer: o.Layer}
		}
		return 0, &Error{Status: 422, Code: "rules_budget_exceeded", Message: fmt.Sprintf("after this publication one of your session files would need %d UTF-8 bytes (limit %d); shorten always-on text or move it to details", o.ActualBytes, o.MaxBytes), ActualBytes: o.ActualBytes, MaxBytes: o.MaxBytes}
	}
	if check.hiddenOver {
		return 0, errHiddenBudget
	}
	return check.ownMax, nil
}

type ownerSnapshots struct {
	person    string
	snapshots []Snapshot
}

func ownerLists(owners []ownerSnapshots) [][]Snapshot {
	out := [][]Snapshot{}
	for _, o := range owners {
		out = append(out, o.snapshots)
	}
	return out
}

// gatherBudget reads every live snapshot the budget may need: the shared layers
// of every project, and each active person's own layers, switching the owner
// setting one person at a time. It restores the caller's settings before it
// returns, whatever happens.
func gatherBudget(w *work, tx pgx.Tx) (shared []Snapshot, owners []ownerSnapshots, projects []string, err error) {
	ctx := w.ctx
	var savedOwner, savedProjects string
	if err = tx.QueryRow(ctx, `SELECT coalesce(current_setting('aeon.rules_owner',true),''),coalesce(current_setting('aeon.rules_projects',true),'')`).Scan(&savedOwner, &savedProjects); err != nil {
		return nil, nil, nil, err
	}
	see := func(owner, projects string) error {
		_, err := tx.Exec(ctx, `SELECT set_config('aeon.rules_owner',$1,true),set_config('aeon.rules_projects',$2,true)`, owner, projects)
		return err
	}
	defer func() {
		if restore := see(savedOwner, savedProjects); restore != nil && err == nil {
			err = restore
		}
	}()
	if err = see("", "*"); err != nil {
		return
	}
	if shared, err = publishedSnapshots(w, tx); err != nil {
		return
	}
	projects = []string{noProject}
	for _, s := range shared {
		if s.Scope.Layer == "project" && !slices.Contains(projects, s.Scope.ProjectID) {
			projects = append(projects, s.Scope.ProjectID)
		}
	}
	people, err := activePeople(ctx, tx)
	if err != nil {
		return
	}
	for _, person := range people {
		if err = w.step(); err != nil {
			return
		}
		if err = see(person, "*"); err != nil {
			return
		}
		var owned []Snapshot
		if owned, err = ownedSnapshots(w, tx, person); err != nil {
			return
		}
		if len(owned) > 0 {
			owners = append(owners, ownerSnapshots{person, owned})
		}
	}
	return
}

type budget struct {
	*work
	tenant     string
	batch      []Set
	now        time.Time
	shared     []Snapshot
	projects   []string
	readable   map[string]bool
	limits     Budget
	rendered   int
	ownMax     int
	ownOver    *Error
	hiddenOver bool
	doctrine   doctrine.Catalog
}

// sortSnapshots orders snapshots as merge does, once, so each render can skip it.
func sortSnapshots(list []Snapshot) []Snapshot {
	out := slices.Clone(list)
	slices.SortFunc(out, func(a, b Snapshot) int {
		if d := a.Scope.rank() - b.Scope.rank(); d != 0 {
			return d
		}
		return strings.Compare(a.SetID, b.SetID)
	})
	return out
}

// person renders one person's files that the batch touches: with no agent and
// with each of the person's named agents and tasks, across projects, roles and
// harnesses. Every candidate, touched or not, is one step of work.
func (b *budget) person(person string, owned []Snapshot) error {
	snapshots := sortSnapshots(append(slices.Clone(b.shared), owned...))
	type agentCtx struct{ agent, task, project string }
	agents := []agentCtx{{}}
	for _, s := range owned {
		if err := b.step(); err != nil {
			return err
		}
		if s.Scope.AgentID == "" {
			continue
		}
		a := agentCtx{s.Scope.AgentID, s.Scope.TaskID, s.Scope.ProjectID}
		if !slices.Contains(agents, a) {
			agents = append(agents, a)
		}
	}
	stop := func() bool { return expired(b.ctx) }
	readable := make([]bool, len(snapshots))
	for i, s := range snapshots {
		readable[i] = b.readable[jsonDigest(s.Scope)]
	}
	for _, a := range agents {
		checked := b.projects
		if a.task != "" {
			checked = []string{a.project}
		}
		for _, project := range checked {
			for _, role := range Roles {
				for _, harness := range Harnesses {
					if err := b.step(); err != nil {
						return err
					}
					c := Context{TenantID: b.tenant, ProjectID: project, PersonID: person, AgentID: a.agent, Role: role, Harness: harness, TaskID: a.task}
					if !slices.ContainsFunc(b.batch, func(s Set) bool { return s.Scope.matches(c) }) {
						continue
					}
					if b.rendered++; b.rendered > maxBudgetContexts {
						return errTooManyCtx
					}
					beforeRender()
					own := true
					for i, s := range snapshots {
						if s.Scope.matches(c) && !readable[i] {
							own = false
							break
						}
					}
					m, err := merge(c, snapshots, b.now, true, stop, b.limits, b.doctrine)
					if errors.Is(err, errStopped) {
						return err
					}
					var e *Error
					over := errors.As(err, &e) && e.Code == "rules_budget_exceeded"
					// A missing floor or an ambiguity does not concern the budget;
					// session start reports those on its own. The worst own file is
					// reported: the largest excess over its limit.
					switch {
					case over && own:
						if b.ownOver == nil || e.ActualBytes-e.MaxBytes > b.ownOver.ActualBytes-b.ownOver.MaxBytes {
							b.ownOver = e
						}
					case over:
						b.hiddenOver = true
					case own:
						b.ownMax = max(b.ownMax, m.ByteSize)
					}
				}
			}
		}
	}
	return b.step()
}

// ownedSnapshots loads the live snapshots of the sets one person owns (their
// person layer, named agents and tasks), as visible under the current setting.
func ownedSnapshots(w *work, tx pgx.Tx, person string) ([]Snapshot, error) {
	rows, err := tx.Query(w.ctx, `SELECT id::text,fields->>'published_version' FROM nodes WHERE rule_resource='set' AND deleted_at IS NULL AND fields->'scope'->>'owner_id'=$1 AND coalesce(fields->>'published_version','')<>'' ORDER BY id`, person)
	if err != nil {
		return nil, err
	}
	type ref struct{ id, version string }
	refs, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (ref, error) {
		var r ref
		return r, row.Scan(&r.id, &r.version)
	})
	if err != nil {
		return nil, err
	}
	out := []Snapshot{}
	for _, r := range refs {
		snap, err := loadVersion(w.ctx, tx, r.id, r.version)
		if err != nil {
			return nil, err
		}
		if err = w.load(snap); err != nil {
			return nil, err
		}
		out = append(out, snap)
	}
	return out, nil
}

// publishedSnapshots loads the live snapshot of every set visible now.
func publishedSnapshots(w *work, tx pgx.Tx) ([]Snapshot, error) {
	all, err := allSets(w.ctx, tx, "")
	if err != nil {
		return nil, err
	}
	out := []Snapshot{}
	for _, s := range all {
		if err := w.step(); err != nil {
			return nil, err
		}
		if s.PublishedVersion == "" {
			continue
		}
		snap, err := loadVersion(w.ctx, tx, s.ID, s.PublishedVersion)
		if err != nil {
			return nil, err
		}
		if err = w.load(snap); err != nil {
			return nil, err
		}
		out = append(out, snap)
	}
	return out, nil
}

func activePeople(ctx context.Context, tx pgx.Tx) ([]string, error) {
	rows, err := tx.Query(ctx, `SELECT id::text FROM principals WHERE kind='person' AND status='active' AND linked_to IS NULL ORDER BY id`)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowTo[string])
}
