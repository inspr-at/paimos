// SPDX-License-Identifier: AGPL-3.0-only

package rules

import (
	"context"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/inspr-at/paimos/internal/events"
	"github.com/inspr-at/paimos/internal/releasehistory"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/inspr-at/paimos/internal/workorders"
	"github.com/jackc/pgx/v5"
)

func audit(ctx context.Context, tx pgx.Tx, p tenant.Principal, id, kind string, before, after any) error {
	_, err := events.Append(ctx, tx, p, events.Change{NodeID: &id, Type: "rules." + kind, Before: before, After: after})
	return err
}
func (m *Module) layers(r *http.Request, tx pgx.Tx, p tenant.Principal) (any, error) {
	rows, err := tx.Query(r.Context(), `SELECT id::text,fields FROM nodes WHERE rule_resource='layer' AND deleted_at IS NULL ORDER BY id`)
	if err != nil {
		return nil, err
	}
	all := []Layer{}
	for rows.Next() {
		var id string
		var raw []byte
		if err = rows.Scan(&id, &raw); err != nil {
			rows.Close()
			return nil, err
		}
		f, err := decodeFields(raw)
		if err != nil {
			rows.Close()
			return nil, err
		}
		all = append(all, Layer{id, f.Scope})
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	out := []Layer{}
	for _, l := range all {
		err = permission(r.Context(), tx, p, l.Scope, "rules.read")
		if isDenied(err) {
			continue
		}
		if err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return map[string]any{"layers": out}, nil
}
func (m *Module) createLayer(r *http.Request, tx pgx.Tx, p tenant.Principal) (any, error) {
	var s Scope
	if err := workorders.Decode(r, &s); err != nil {
		return nil, err
	}
	if err := permission(r.Context(), tx, p, s, "rules.write"); err != nil {
		return nil, err
	}
	id, err := insertNode(r.Context(), tx, p, s.ProjectID, s.Layer+" rules", fields{Resource: "layer", Scope: s})
	if err != nil {
		return nil, err
	}
	l := Layer{id, s}
	if err = audit(r.Context(), tx, p, id, "layer_created", nil, l); err != nil {
		return nil, err
	}
	return l, nil
}
func allSets(ctx context.Context, tx pgx.Tx, layerID string) ([]Set, error) {
	rows, err := tx.Query(ctx, `SELECT id::text,parent_id::text,fields FROM nodes WHERE rule_resource='set' AND deleted_at IS NULL AND ($1='' OR parent_id::text=$1) ORDER BY id`, layerID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Set{}
	for rows.Next() {
		var id, parent string
		var raw []byte
		if err = rows.Scan(&id, &parent, &raw); err != nil {
			return nil, err
		}
		f, err := decodeFields(raw)
		if err != nil {
			return nil, err
		}
		out = append(out, Set{ID: id, LayerID: parent, Scope: f.Scope, Name: f.Name, Revision: f.Revision, Rules: []Rule{}, PublishedVersion: f.PublishedVersion})
	}
	return out, rows.Err()
}
func loadSet(ctx context.Context, tx pgx.Tx, id string) (Set, error) {
	f, parent, err := nodeFields(ctx, tx, id, "set")
	if err != nil {
		return Set{}, err
	}
	s := Set{ID: id, LayerID: parent, Scope: f.Scope, Name: f.Name, Revision: f.Revision, Rules: []Rule{}, PublishedVersion: f.PublishedVersion}
	rows, err := tx.Query(ctx, `SELECT fields FROM nodes WHERE parent_id=$1 AND rule_resource='rule' AND deleted_at IS NULL ORDER BY fields->'rule'->>'identity'`, id)
	if err != nil {
		return s, err
	}
	defer rows.Close()
	for rows.Next() {
		var raw []byte
		if err = rows.Scan(&raw); err != nil {
			return s, err
		}
		f, err := decodeFields(raw)
		if err != nil {
			return s, err
		}
		if f.Rule == nil {
			return s, fail(409, "snapshot_integrity", "rule node missing rule")
		}
		s.Rules = append(s.Rules, *f.Rule)
	}
	return s, rows.Err()
}
func (m *Module) sets(r *http.Request, tx pgx.Tx, p tenant.Principal) (any, error) {
	id := r.URL.Query().Get("layer_id")
	f, _, err := nodeFields(r.Context(), tx, id, "layer")
	if err != nil {
		return nil, err
	}
	if err = permission(r.Context(), tx, p, f.Scope, "rules.read"); err != nil {
		return nil, err
	}
	out, err := allSets(r.Context(), tx, id)
	if err != nil {
		return nil, err
	}
	for i := range out {
		out[i], err = loadSet(r.Context(), tx, out[i].ID)
		if err != nil {
			return nil, err
		}
	}
	return map[string]any{"sets": out}, nil
}
func (m *Module) createSet(r *http.Request, tx pgx.Tx, p tenant.Principal) (any, error) {
	var in struct {
		LayerID string `json:"layer_id"`
		Name    string `json:"name"`
	}
	if err := workorders.Decode(r, &in); err != nil {
		return nil, err
	}
	if err := validateName(in.Name); err != nil {
		return nil, err
	}
	f, _, err := nodeFields(r.Context(), tx, in.LayerID, "layer")
	if err != nil {
		return nil, err
	}
	if err = permission(r.Context(), tx, p, f.Scope, "rules.write"); err != nil {
		return nil, err
	}
	id, err := insertNode(r.Context(), tx, p, in.LayerID, in.Name, fields{Resource: "set", Scope: f.Scope, Name: in.Name, Revision: 1})
	if err != nil {
		return nil, err
	}
	s, err := loadSet(r.Context(), tx, id)
	if err != nil {
		return nil, err
	}
	if err = audit(r.Context(), tx, p, id, "set_created", nil, s); err != nil {
		return nil, err
	}
	return s, nil
}
func (m *Module) authorizedSet(r *http.Request, tx pgx.Tx, p tenant.Principal, action string) (Set, error) {
	s, err := loadSet(r.Context(), tx, r.PathValue("setId"))
	if err != nil {
		return s, err
	}
	return s, permission(r.Context(), tx, p, s.Scope, action)
}
func (m *Module) getSet(r *http.Request, tx pgx.Tx, p tenant.Principal) (any, error) {
	return m.authorizedSet(r, tx, p, "rules.read")
}

type draftInput struct {
	ExpectedRevision int64  `json:"expected_revision"`
	Name             string `json:"name"`
	Rules            []Rule `json:"rules"`
}

func canonicalRules(in []Rule) []Rule {
	out := slices.Clone(in)
	if out == nil {
		out = []Rule{}
	}
	for i := range out {
		out[i].Roles = slices.Clone(out[i].Roles)
		out[i].Harnesses = slices.Clone(out[i].Harnesses)
		slices.Sort(out[i].Roles)
		out[i].Roles = slices.Compact(out[i].Roles)
		slices.Sort(out[i].Harnesses)
		out[i].Harnesses = slices.Compact(out[i].Harnesses)
		if out[i].ExpiresAt != nil {
			at := out[i].ExpiresAt.UTC()
			out[i].ExpiresAt = &at
		}
	}
	slices.SortFunc(out, func(a, b Rule) int { return strings.Compare(a.Identity, b.Identity) })
	return out
}
func replaceDraft(ctx context.Context, tx pgx.Tx, p tenant.Principal, s Set, in draftInput) (Set, error) {
	if in.Rules == nil {
		return s, fail(400, "invalid_rule", "rules must be an explicit array; use [] to intentionally clear the draft")
	}
	if s.Revision != in.ExpectedRevision {
		return s, fail(409, "revision_conflict", "draft changed; reload and retry with its current revision")
	}
	if err := validateName(in.Name); err != nil {
		return s, err
	}
	if err := ValidateRules(in.Rules); err != nil {
		return s, err
	}
	before := s
	s.Name = in.Name
	s.Rules = canonicalRules(in.Rules)
	s.Revision++
	// Preserve stable node IDs for retained identities; removed rules are soft
	// deleted, with the full before/after set recorded in the append-only event.
	rows, err := tx.Query(ctx, `SELECT id::text,fields->'rule'->>'identity' FROM nodes WHERE parent_id=$1 AND rule_resource='rule' AND deleted_at IS NULL`, s.ID)
	if err != nil {
		return s, err
	}
	ids := map[string]string{}
	for rows.Next() {
		var id, identity string
		if err = rows.Scan(&id, &identity); err != nil {
			rows.Close()
			return s, err
		}
		ids[identity] = id
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return s, err
	}
	for _, rule := range s.Rules {
		f := fields{Resource: "rule", Scope: s.Scope, Rule: &rule}
		if id, ok := ids[rule.Identity]; ok {
			_, err = tx.Exec(ctx, `UPDATE nodes SET title=$2,fields=$3,updated_at=clock_timestamp() WHERE id=$1`, id, rule.Text, jsonBytes(f))
			delete(ids, rule.Identity)
		} else {
			_, err = insertNode(ctx, tx, p, s.ID, rule.Text, f)
		}
		if err != nil {
			return s, err
		}
	}
	for _, id := range ids {
		if _, err = tx.Exec(ctx, `UPDATE nodes SET deleted_at=clock_timestamp() WHERE id=$1`, id); err != nil {
			return s, err
		}
	}
	if err = saveSet(ctx, tx, s); err != nil {
		return s, err
	}
	return s, audit(ctx, tx, p, s.ID, "draft_replaced", before, s)
}
func saveSet(ctx context.Context, tx pgx.Tx, s Set) error {
	_, err := tx.Exec(ctx, `UPDATE nodes SET title=$2,fields=$3,updated_at=clock_timestamp() WHERE id=$1`, s.ID, s.Name, jsonBytes(fields{Resource: "set", Scope: s.Scope, Name: s.Name, Revision: s.Revision, PublishedVersion: s.PublishedVersion}))
	return err
}
func (m *Module) draft(r *http.Request, tx pgx.Tx, p tenant.Principal) (any, error) {
	var in draftInput
	if err := workorders.Decode(r, &in); err != nil {
		return nil, err
	}
	s, err := m.authorizedSet(r, tx, p, "rules.write")
	if err != nil {
		return nil, err
	}
	return replaceDraft(r.Context(), tx, p, s, in)
}
func loadVersion(ctx context.Context, tx pgx.Tx, setID, version string) (Snapshot, error) {
	var raw []byte
	err := tx.QueryRow(ctx, `SELECT fields FROM nodes WHERE parent_id=$1 AND rule_resource='version' AND fields->>'version'=$2`, setID, version).Scan(&raw)
	if err != nil {
		return Snapshot{}, err
	}
	f, err := decodeFields(raw)
	if err != nil {
		return Snapshot{}, err
	}
	if f.Snapshot == nil {
		return Snapshot{}, fail(409, "snapshot_integrity", "snapshot missing")
	}
	return *f.Snapshot, nil
}
func publishSet(ctx context.Context, tx pgx.Tx, p tenant.Principal, s Set, revision int64, version, note string) (Snapshot, error) {
	return publishSetIn(ctx, tx, p, s, revision, version, note, "")
}

// publishedEvent is the rules.published event payload: the snapshot, plus the
// shared batch id when the set was published as part of one batch approval.
type publishedEvent struct {
	Snapshot
	BatchID string `json:"batch_id,omitempty"`
}

func publishSetIn(ctx context.Context, tx pgx.Tx, p tenant.Principal, s Set, revision int64, version, note, batchID string) (Snapshot, error) {
	if !releasehistory.ValidVersion(version) {
		return Snapshot{}, fail(400, "invalid_version", "version must be a valid YYMMDDhhmmss.0.0 UTC calendar coordinate")
	}
	if s.Revision != revision {
		return Snapshot{}, fail(409, "revision_conflict", "draft revision does not match")
	}
	if err := ValidateRules(s.Rules); err != nil {
		return Snapshot{}, err
	}
	snap := Snapshot{SetID: s.ID, Scope: s.Scope, Name: s.Name, Revision: s.Revision, Version: version, Rules: canonicalRules(s.Rules), PublishedAt: time.Now().UTC(), Note: note}
	snap.SHA256 = SnapshotDigest(snap)
	old, err := loadVersion(ctx, tx, s.ID, version)
	if err == nil {
		if old.SHA256 == snap.SHA256 {
			return old, nil
		}
		return Snapshot{}, fail(409, "version_conflict", "version already identifies different immutable bytes")
	}
	if err != pgx.ErrNoRows {
		return Snapshot{}, err
	}
	if s.PublishedVersion != "" && version <= s.PublishedVersion {
		return Snapshot{}, fail(409, "version_conflict", "new version must be strictly later than the current publication")
	}
	// Company locked floor is unconditional across role/harness selectors.
	if s.Scope.Layer == "company" {
		for _, rule := range s.Rules {
			if rule.Strength == "locked" && (len(rule.Roles) > 0 || len(rule.Harnesses) > 0) {
				return Snapshot{}, fail(400, "invalid_rule", "locked company floor cannot be conditional on a role or harness")
			}
		}
	}
	_, err = insertNode(ctx, tx, p, s.ID, s.Name+" "+version, fields{Resource: "version", Scope: s.Scope, Version: version, Snapshot: &snap})
	if err != nil {
		return Snapshot{}, err
	}
	before := s.PublishedVersion
	s.PublishedVersion = version
	if err = saveSet(ctx, tx, s); err != nil {
		return Snapshot{}, err
	}
	if err = audit(ctx, tx, p, s.ID, "published", map[string]string{"version": before}, publishedEvent{snap, batchID}); err != nil {
		return Snapshot{}, err
	}
	return snap, nil
}
func (m *Module) publish(r *http.Request, tx pgx.Tx, p tenant.Principal) (any, error) {
	var in struct {
		ExpectedRevision int64  `json:"expected_revision"`
		Version          string `json:"version"`
		Note             string `json:"note"`
	}
	if err := workorders.Decode(r, &in); err != nil {
		return nil, err
	}
	s, err := m.authorizedSet(r, tx, p, "rules.publish")
	if err != nil {
		return nil, err
	}
	note, err := normalizeNote(in.Note)
	if err != nil {
		return nil, err
	}
	return publishSet(r.Context(), tx, p, s, in.ExpectedRevision, in.Version, note)
}
func (m *Module) restore(r *http.Request, tx pgx.Tx, p tenant.Principal) (any, error) {
	var in struct {
		ExpectedRevision int64  `json:"expected_revision"`
		Version          string `json:"version"`
		NewVersion       string `json:"new_version"`
		Note             string `json:"note"`
	}
	if err := workorders.Decode(r, &in); err != nil {
		return nil, err
	}
	s, err := m.authorizedSet(r, tx, p, "rules.publish")
	if err != nil {
		return nil, err
	}
	note, err := normalizeNote(in.Note)
	if err != nil {
		return nil, err
	}
	if in.NewVersion <= s.PublishedVersion {
		return nil, fail(409, "version_conflict", "restoration must publish a new later version")
	}
	old, err := loadVersion(r.Context(), tx, s.ID, in.Version)
	if err != nil {
		return nil, err
	}
	if old.SHA256 != SnapshotDigest(old) {
		return nil, fail(409, "snapshot_integrity", "historical digest mismatch")
	}
	s, err = replaceDraft(r.Context(), tx, p, s, draftInput{in.ExpectedRevision, old.Name, old.Rules})
	if err != nil {
		return nil, err
	}
	snap, err := publishSet(r.Context(), tx, p, s, s.Revision, in.NewVersion, note)
	if err != nil {
		return nil, err
	}
	restored := map[string]string{"version": snap.Version, "sha256": snap.SHA256}
	if snap.Note != "" {
		restored["note"] = snap.Note
	}
	if err = audit(r.Context(), tx, p, s.ID, "restored", map[string]string{"source_version": old.Version}, restored); err != nil {
		return nil, err
	}
	return snap, nil
}

func (m *Module) version(r *http.Request, tx pgx.Tx, p tenant.Principal) (any, error) {
	s, err := m.authorizedSet(r, tx, p, "rules.read")
	if err != nil {
		return nil, err
	}
	return loadVersion(r.Context(), tx, s.ID, r.PathValue("version"))
}
func (m *Module) versions(r *http.Request, tx pgx.Tx, p tenant.Principal) (any, error) {
	s, err := m.authorizedSet(r, tx, p, "rules.read")
	if err != nil {
		return nil, err
	}
	rows, err := tx.Query(r.Context(), `SELECT fields FROM nodes WHERE parent_id=$1 AND rule_resource='version' ORDER BY fields->>'version' DESC`, s.ID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Snapshot{}
	for rows.Next() {
		var raw []byte
		if err = rows.Scan(&raw); err != nil {
			return nil, err
		}
		f, err := decodeFields(raw)
		if err != nil {
			return nil, err
		}
		if f.Snapshot == nil {
			return nil, fail(409, "snapshot_integrity", "snapshot missing")
		}
		out = append(out, *f.Snapshot)
	}
	return map[string]any{"versions": out}, rows.Err()
}
