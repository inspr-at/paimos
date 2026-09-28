// SPDX-License-Identifier: AGPL-3.0-only

package rules

import (
	"context"
	"encoding/hex"
	"errors"
	"net/http"
	"slices"
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

// publishBatch publishes several sets under one person approval. Everything runs
// in the request transaction, so it is all or nothing: permission, revision and
// validation are checked for every set before the first write, and a later
// failure (a version conflict, the merged byte budget) rolls every set back.
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
	sets := make([]Set, len(in.Items))
	for _, item := range in.Items {
		if !workorders.UUID(item.SetID) {
			return nil, fail(400, "invalid_scope", "invalid set UUID")
		}
		if seen[item.SetID] {
			return nil, fail(400, "invalid_request", "a set appears more than once in the batch")
		}
		seen[item.SetID] = true
		if item.Version != AutoVersion && !releasehistory.ValidVersion(item.Version) {
			return nil, fail(400, "invalid_version", "version must be auto or a valid YYMMDDhhmmss.0.0 UTC calendar coordinate")
		}
	}
	// Authorization first, for every set, before anything else can differ.
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
	for i, item := range in.Items {
		if sets[i].Revision != item.ExpectedRevision {
			return nil, &Error{Status: 409, Code: "revision_conflict", Message: "draft revision does not match for set " + sets[i].Name}
		}
		if err := ValidateRules(sets[i].Rules); err != nil {
			return nil, err
		}
	}
	batchID := batchIdentity(p, in, note)
	now := time.Now().UTC()
	out := BatchResult{BatchID: batchID, Versions: make([]Snapshot, len(in.Items))}
	for i, item := range in.Items {
		s := sets[i]
		version := item.Version
		if version == AutoVersion {
			current, replay, err := autoReplay(ctx, tx, s, note)
			if err != nil {
				return nil, err
			}
			if replay {
				out.Versions[i] = current
				continue
			}
			version = nextVersion(now, s.PublishedVersion)
		}
		snap, err := publishSetIn(ctx, tx, p, s, item.ExpectedRevision, version, note, batchID)
		if err != nil {
			return nil, err
		}
		out.Versions[i] = snap
	}
	out.MaxBytes, err = budgetCheck(ctx, tx, p, sets, now)
	if err != nil {
		return nil, err
	}
	return out, nil
}

// autoReplay recognises an exact replay of an auto-versioned batch: the set's
// current publication already holds this saved revision with the same note, so
// publishing again would only repeat identical bytes under a new version.
func autoReplay(ctx context.Context, tx pgx.Tx, s Set, note string) (Snapshot, bool, error) {
	if s.PublishedVersion == "" {
		return Snapshot{}, false, nil
	}
	current, err := loadVersion(ctx, tx, s.ID, s.PublishedVersion)
	if err != nil {
		return Snapshot{}, false, err
	}
	same := current.Revision == s.Revision && current.Note == note && current.Name == s.Name && jsonDigest(canonicalRules(current.Rules)) == jsonDigest(canonicalRules(s.Rules))
	return current, same, nil
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

// batchIdentity is deterministic, so an exact replay of the same request by the
// same person answers with the same batch id without storing anything new.
func batchIdentity(p tenant.Principal, in batchInput, note string) string {
	items := slices.Clone(in.Items)
	slices.SortFunc(items, func(a, b batchItem) int {
		if a.SetID < b.SetID {
			return -1
		}
		if a.SetID > b.SetID {
			return 1
		}
		return 0
	})
	raw, _ := hex.DecodeString(jsonDigest(map[string]any{"tenant": p.TenantID, "person": p.ID, "items": items, "note": note}))
	raw[6] = raw[6]&0x0f | 0x80 // UUID version 8: derived, not random
	raw[8] = raw[8]&0x3f | 0x80
	h := hex.EncodeToString(raw[:16])
	return h[0:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:32]
}

// budgetCheck renders the merged file, after the batch's writes, for every
// context the batch can change and fails with 422 when one exceeds the budget.
// Contexts: the caller as person, every role and harness, every project with
// published project rules (or none), and every named agent or task in the batch.
func budgetCheck(ctx context.Context, tx pgx.Tx, p tenant.Principal, batch []Set, now time.Time) (int, error) {
	owner, err := actorOwner(ctx, tx, p)
	if err != nil {
		return 0, err
	}
	all, err := allSets(ctx, tx, "")
	if err != nil {
		return 0, err
	}
	snapshots := []Snapshot{}
	projects := []string{}
	for _, s := range all {
		if s.PublishedVersion == "" {
			continue
		}
		snap, err := loadVersion(ctx, tx, s.ID, s.PublishedVersion)
		if err != nil {
			return 0, err
		}
		snapshots = append(snapshots, snap)
		if s.Scope.Layer == "project" && !slices.Contains(projects, s.Scope.ProjectID) {
			projects = append(projects, s.Scope.ProjectID)
		}
	}
	if len(projects) == 0 {
		// No project rules at all: any project sees the same merge.
		projects = append(projects, "00000000-0000-4000-8000-000000000000")
	}
	type agentCtx struct{ agent, task, project string }
	agents := []agentCtx{{}}
	for _, s := range batch {
		if s.Scope.AgentID != "" && s.Scope.OwnerID == owner {
			agents = append(agents, agentCtx{s.Scope.AgentID, s.Scope.TaskID, s.Scope.ProjectID})
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
					c := Context{TenantID: p.TenantID, ProjectID: project, PersonID: owner, AgentID: a.agent, Role: role, Harness: harness, TaskID: a.task}
					m, err := Merge(c, snapshots, now)
					var e *Error
					if errors.As(err, &e) && e.Code == "rules_budget_exceeded" {
						return 0, err
					}
					// A missing floor or an ambiguity does not concern the budget;
					// session start reports those on its own.
					if m.ByteSize > largest {
						largest = m.ByteSize
					}
				}
			}
		}
	}
	return largest, nil
}
