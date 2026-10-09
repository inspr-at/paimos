// SPDX-License-Identifier: AGPL-3.0-only
package stepup

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	confirmation "github.com/inspr-at/paimos/internal/stepup"
	"io"
	"strings"

	"github.com/inspr-at/paimos/internal/features"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

const payloadLimit = 32 << 10

// Target is a code-owned, typed mutation adapter, never an agent-supplied route
// or SQL statement. Snapshot acquires all target locks before Apply. Apply must
// use this transaction, act as the supplied person, and append no audit event
// or acquire new locks (the request outcome is stored before the event counter).
type Target interface {
	Permission() string
	Parse(json.RawMessage) (json.RawMessage, string, error)
	Snapshot(context.Context, pgx.Tx, tenant.Principal, json.RawMessage) (json.RawMessage, json.RawMessage, error)
	Apply(context.Context, pgx.Tx, tenant.Principal, json.RawMessage, json.RawMessage) error
}

func decodeJSON(raw []byte, out any) error {
	if len(raw) == 0 || len(raw) > payloadLimit {
		return fault(400, "payload exceeds 32 KiB")
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	d.UseNumber()
	if d.Decode(out) != nil || d.Decode(new(any)) != io.EOF {
		return fault(400, "invalid payload")
	}
	return nil
}

// Hash hashes canonical JSON, including revision. Clients compute the same hash
// from the documented before snapshot; whitespace/key order are insignificant.
func Hash(v json.RawMessage) string {
	var value any
	d := json.NewDecoder(bytes.NewReader(v))
	d.UseNumber()
	if d.Decode(&value) != nil {
		return ""
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return ""
	}
	h := sha256.Sum256(raw)
	return hex.EncodeToString(h[:])
}

// FeatureTarget is the initial protected change. Only shipped catalogue keys
// are accepted. It shares the existing feature writer's per-tenant/key fence,
// revisions, inherit semantics and settings.manage authority.
type FeatureTarget struct{}
type featureChange struct {
	Kind      string          `json:"kind"`
	Key       string          `json:"key"`
	ProjectID string          `json:"project_id,omitempty"`
	Enabled   json.RawMessage `json:"enabled"`
	Revision  *int64          `json:"expected_revision"`
}
type featureSnapshot struct {
	Key       string  `json:"key"`
	ProjectID *string `json:"project_id"`
	Override  *bool   `json:"override"`
	Revision  int64   `json:"revision"`
}

func (FeatureTarget) Permission() string { return "settings.manage" }
func (FeatureTarget) Parse(raw json.RawMessage) (json.RawMessage, string, error) {
	var in featureChange
	if err := decodeJSON(raw, &in); err != nil {
		return nil, "", err
	}
	var enabled *bool
	if in.Kind != "feature" || in.Revision == nil || *in.Revision < 0 || *in.Revision == int64(^uint64(0)>>1) || len(in.Enabled) == 0 || json.Unmarshal(in.Enabled, &enabled) != nil || in.ProjectID != "" && !ValidID(in.ProjectID) {
		return nil, "", fault(400, "invalid feature change")
	}
	found := false
	for _, d := range features.Catalog() {
		found = found || d.Key == in.Key
	}
	if !found {
		return nil, "", fault(400, "unknown feature")
	}
	in.Enabled, _ = json.Marshal(enabled)
	canonical, err := json.Marshal(in)
	return canonical, in.ProjectID, err
}
func (FeatureTarget) Snapshot(ctx context.Context, tx pgx.Tx, p tenant.Principal, raw json.RawMessage) (json.RawMessage, json.RawMessage, error) {
	var in featureChange
	if err := decodeJSON(raw, &in); err != nil {
		return nil, nil, err
	}
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, "feature:"+p.TenantID+":"+in.Key); err != nil {
		return nil, nil, err
	}
	if in.ProjectID != "" {
		var found bool
		err := tx.QueryRow(ctx, `SELECT true FROM nodes n JOIN node_kinds k ON k.tenant_id=n.tenant_id AND k.id=n.kind_id WHERE n.tenant_id=$1 AND n.id=$2 AND k.slug='project' AND n.deleted_at IS NULL`, p.TenantID, in.ProjectID).Scan(&found)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil, fault(404, "target unavailable")
		}
		if err != nil {
			return nil, nil, err
		}
	}
	before := featureSnapshot{Key: in.Key, ProjectID: nullable(in.ProjectID)}
	err := tx.QueryRow(ctx, `SELECT enabled,revision FROM features WHERE tenant_id=$1 AND key=$2 AND project_id IS NOT DISTINCT FROM $3::uuid`, p.TenantID, in.Key, nullable(in.ProjectID)).Scan(&before.Override, &before.Revision)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return nil, nil, err
	}
	after := before
	if err := json.Unmarshal(in.Enabled, &after.Override); err != nil {
		return nil, nil, err
	}
	after.Revision++
	b, err := json.Marshal(before)
	if err != nil {
		return nil, nil, err
	}
	a, err := json.Marshal(after)
	return b, a, err
}
func (FeatureTarget) Apply(ctx context.Context, tx pgx.Tx, p tenant.Principal, raw json.RawMessage, after json.RawMessage) error {
	var in featureChange
	var snapshot featureSnapshot
	if err := decodeJSON(raw, &in); err != nil {
		return err
	}
	if err := decodeJSON(after, &snapshot); err != nil {
		return err
	}
	tag, err := tx.Exec(ctx, `INSERT INTO features(tenant_id,key,project_id,enabled) VALUES($1,$2,$3,$4) ON CONFLICT(tenant_id,key,project_id) DO UPDATE SET enabled=excluded.enabled,revision=features.revision+1,updated_at=clock_timestamp() WHERE features.revision=$5`, p.TenantID, in.Key, nullable(in.ProjectID), snapshot.Override, *in.Revision)
	if err == nil && tag.RowsAffected() != 1 {
		return fault(409, "target changed")
	}
	return err
}
func nullable(id string) *string {
	if id == "" {
		return nil
	}
	return &id
}

func ValidID(id string) bool { return confirmation.ValidID(id) }
func validHash(hash string) bool {
	return len(hash) == 64 && strings.Trim(hash, "0123456789abcdef") == ""
}
