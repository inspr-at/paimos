// SPDX-License-Identifier: AGPL-3.0-only

package harness

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/inspr-at/paimos/internal/rules"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/inspr-at/paimos/internal/workorders"
)

// recordReceiptProvenance appends the instruction sources attested by one new
// receipt. The merged version and body hash always come from that receipt.
// Constituent set versions are copied only from the single manifest recorded
// when those bytes were served. The merged version is the maximum constituent
// version, so a later lower-version set that leaves the rendered bytes
// unchanged is not proof of what this session received. Replay of a receipt
// never reaches here. Rule text is not stored.
func recordReceiptProvenance(ctx context.Context, tx pgx.Tx, p tenant.Principal, s Session, in RulesReceiptWrite) error {
	if in.ByteSize == nil {
		return errors.New("instruction provenance could not be recorded")
	}
	version := in.Version
	hash := in.BodySHA256
	size := int64(*in.ByteSize)
	items := []ProvenanceItem{{
		Kind:          "rules_merged",
		LogicalName:   "merged-rules",
		HashKind:      "content",
		ContentSHA256: &hash,
		Version:       &version,
		ByteSize:      &size,
	}}
	sets, proven, err := rules.ServedSetVersions(ctx, tx, in.Context, in.Version, in.BodySHA256, *in.ByteSize)
	if err != nil {
		return err
	}
	if proven {
		for _, set := range sets {
			setVersion := set.Version
			setHash := set.SHA256
			setID := strings.ToLower(set.SetID)
			items = append(items, ProvenanceItem{
				Kind:          "rules_set",
				LogicalName:   setID,
				HashKind:      "content",
				ContentSHA256: &setHash,
				Version:       &setVersion,
			})
		}
	}
	_, err = appendInstructionSources(ctx, tx, p, s, items, map[string]bool{"rules_merged": true, "rules_set": true})
	return err
}

// InstructionSource is one stored identity a later analysis can query.
// It has no rule text and no filesystem path.
type InstructionSource struct {
	SessionID     string    `json:"session_id"`
	Revision      int64     `json:"revision"`
	Kind          string    `json:"kind"`
	LogicalName   string    `json:"logical_name"`
	HashKind      string    `json:"hash_kind"`
	ContentSHA256 *string   `json:"content_sha256"`
	Version       *string   `json:"version"`
	ByteSize      *int64    `json:"byte_size"`
	RecordedAt    time.Time `json:"recorded_at"`
}

// InstructionSourcePage is a bounded project query for AEON-220.
type InstructionSourcePage struct {
	ProjectID string              `json:"project_id"`
	Items     []InstructionSource `json:"items"`
	Truncated bool                `json:"truncated"`
}

func (m *Module) queryInstructionSources(r *http.Request, tx pgx.Tx, _ tenant.Principal) (any, error) {
	ctx := r.Context()
	projectID := r.PathValue("projectId")
	if err := project(ctx, tx, projectID); err != nil {
		return nil, err
	}
	var version, hash, kind, name string
	for key, values := range r.URL.Query() {
		if len(values) != 1 {
			return nil, workorders.Fail(400, "invalid instruction provenance query")
		}
		switch key {
		case "version":
			version = values[0]
		case "content_sha256":
			hash = values[0]
		case "kind":
			kind = values[0]
		case "logical_name":
			name = values[0]
		default:
			return nil, workorders.Fail(400, "invalid instruction provenance query")
		}
	}
	if version != "" && !validProvenanceVersion(&version, true) {
		return nil, workorders.Fail(400, "invalid instruction provenance query")
	}
	if hash != "" && !provenanceSHA256.MatchString(hash) {
		return nil, workorders.Fail(400, "invalid instruction provenance query")
	}
	switch kind {
	case "", "agents", "claude", "skill", "prompt_template", "rules_merged", "rules_set":
	default:
		return nil, workorders.Fail(400, "invalid instruction provenance query")
	}
	if name != "" && !validSourceName(name) {
		return nil, workorders.Fail(400, "invalid instruction provenance query")
	}
	rows, err := tx.Query(ctx, `SELECT s.id::text, p.revision, i.kind, i.logical_name, i.hash_kind, i.content_sha256, i.version, i.byte_size, p.created_at
		FROM harness_instruction_provenance_items i
		JOIN harness_instruction_provenance p ON p.tenant_id=i.tenant_id AND p.id=i.provenance_id
		JOIN harness_sessions s ON s.tenant_id=p.tenant_id AND s.id=p.session_id
		WHERE s.project_id=$1
		  AND ($2='' OR i.version=$2)
		  AND ($3='' OR i.content_sha256=$3)
		  AND ($4='' OR i.kind=$4)
		  AND ($5='' OR i.logical_name=$5)
		ORDER BY p.created_at DESC, p.revision DESC, i.ordinal
		LIMIT $6`, projectID, version, hash, kind, name, maxInstructionSourceHits+1)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	page := InstructionSourcePage{ProjectID: projectID, Items: []InstructionSource{}}
	for rows.Next() {
		var item InstructionSource
		if err = rows.Scan(&item.SessionID, &item.Revision, &item.Kind, &item.LogicalName, &item.HashKind, &item.ContentSHA256, &item.Version, &item.ByteSize, &item.RecordedAt); err != nil {
			return nil, err
		}
		page.Items = append(page.Items, item)
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	if len(page.Items) > maxInstructionSourceHits {
		page.Truncated = true
		page.Items = page.Items[:maxInstructionSourceHits]
	}
	return page, nil
}

func validSourceName(name string) bool {
	if name != strings.TrimSpace(name) {
		return false
	}
	if validProvenanceVersion(&name, true) {
		return true
	}
	return provenanceSkill.MatchString(name)
}
