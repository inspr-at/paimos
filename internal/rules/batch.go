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
	if p.Kind != tenant.Person {
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
	now := time.Now().UTC()
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
	if out.MaxBytes, err = budgetCheck(ctx, tx, p.TenantID, now); err != nil {
		return nil, err
	}
	raw, err := json.Marshal(out)
	if err != nil {
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

// budgetCheck renders, after the batch's writes, the session file of every
// context the publication can change and fails with 422 when one exceeds the
// budget. Contexts: every project with published project rules and a project
// without any; every person with person or agent rules (other owners' private
// layers included) and a person without any; each owner's named agents and
// tasks; every role and harness.
//
// Other owners' layers are hidden from the caller by row-level security. To see
// them, the owner setting is pointed at one owner at a time and restored before
// returning. This window only reads: the texts stay in memory, never leave this
// function, and only the largest byte count and pass or fail come back. The
// transaction holds the tenant rules lock, so no write happens in between.
func budgetCheck(ctx context.Context, tx pgx.Tx, tenantID string, now time.Time) (int, error) {
	var savedOwner, savedProjects string
	if err := tx.QueryRow(ctx, `SELECT coalesce(current_setting('aeon.rules_owner',true),''),coalesce(current_setting('aeon.rules_projects',true),'')`).Scan(&savedOwner, &savedProjects); err != nil {
		return 0, err
	}
	see := func(owner, projects string) error {
		_, err := tx.Exec(ctx, `SELECT set_config('aeon.rules_owner',$1,true),set_config('aeon.rules_projects',$2,true)`, owner, projects)
		return err
	}
	largest, err := func() (int, error) {
		if err := see("", "*"); err != nil {
			return 0, err
		}
		shared, err := publishedSnapshots(ctx, tx)
		if err != nil {
			return 0, err
		}
		projects := []string{noProject}
		for _, s := range shared {
			if s.Scope.Layer == "project" && !slices.Contains(projects, s.Scope.ProjectID) {
				projects = append(projects, s.Scope.ProjectID)
			}
		}
		people, err := activePeople(ctx, tx)
		if err != nil {
			return 0, err
		}
		largest, err := budgetFor(tenantID, noPerson, shared, nil, projects, now)
		if err != nil {
			return 0, err
		}
		for _, person := range people {
			if err := see(person, "*"); err != nil {
				return 0, err
			}
			visible, err := publishedSnapshots(ctx, tx)
			if err != nil {
				return 0, err
			}
			owned := []Snapshot{}
			for _, s := range visible {
				if s.Scope.OwnerID == person {
					owned = append(owned, s)
				}
			}
			if len(owned) == 0 {
				continue
			}
			size, err := budgetFor(tenantID, person, shared, owned, projects, now)
			if err != nil {
				return 0, err
			}
			largest = max(largest, size)
		}
		return largest, nil
	}()
	if restore := see(savedOwner, savedProjects); restore != nil && err == nil {
		err = restore
	}
	return largest, err
}

// budgetFor checks one person's contexts: with no agent, and with each of the
// person's named agents and tasks, across projects, roles and harnesses.
func budgetFor(tenantID, person string, shared, owned []Snapshot, projects []string, now time.Time) (int, error) {
	snapshots := append(slices.Clone(shared), owned...)
	type agentCtx struct{ agent, task, project string }
	agents := []agentCtx{{}}
	for _, s := range owned {
		if s.Scope.AgentID == "" {
			continue
		}
		a := agentCtx{s.Scope.AgentID, s.Scope.TaskID, s.Scope.ProjectID}
		if !slices.Contains(agents, a) {
			agents = append(agents, a)
		}
	}
	largest := 0
	for _, a := range agents {
		checked := projects
		if a.task != "" {
			checked = []string{a.project}
		}
		for _, project := range checked {
			for _, role := range Roles {
				for _, harness := range Harnesses {
					c := Context{TenantID: tenantID, ProjectID: project, PersonID: person, AgentID: a.agent, Role: role, Harness: harness, TaskID: a.task}
					m, err := Merge(c, snapshots, now)
					var e *Error
					if errors.As(err, &e) && e.Code == "rules_budget_exceeded" {
						// Name no context: it may be another person's private file.
						return 0, &Error{Status: 422, Code: e.Code, Message: fmt.Sprintf("after this publication a session file would need %d UTF-8 bytes (limit %d); shorten always-on text or move it to details", m.ByteSize, MaxBytes), ActualBytes: m.ByteSize, MaxBytes: MaxBytes}
					}
					// A missing floor or an ambiguity does not concern the budget;
					// session start reports those on its own.
					largest = max(largest, m.ByteSize)
				}
			}
		}
	}
	return largest, nil
}

// publishedSnapshots loads the live snapshot of every set visible now.
func publishedSnapshots(ctx context.Context, tx pgx.Tx) ([]Snapshot, error) {
	all, err := allSets(ctx, tx, "")
	if err != nil {
		return nil, err
	}
	out := []Snapshot{}
	for _, s := range all {
		if s.PublishedVersion == "" {
			continue
		}
		snap, err := loadVersion(ctx, tx, s.ID, s.PublishedVersion)
		if err != nil {
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
