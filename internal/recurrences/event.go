// SPDX-License-Identifier: AGPL-3.0-only
package recurrences

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/inspr-at/paimos/internal/workorders"
	"github.com/inspr-at/paimos/internal/workstate"
	"github.com/jackc/pgx/v5"
)

type EventFilter struct {
	ProjectIDs     []string `json:"project_ids,omitempty"`
	HasReleaseCopy bool     `json:"has_release_copy,omitempty"`
	ExcludeHidden  bool     `json:"exclude_hidden,omitempty"`
	EntryID        string   `json:"entry_id,omitempty"`
	KnowledgeType  string   `json:"knowledge_type,omitempty"`
	Tag            string   `json:"tag,omitempty"`
}

type ExternalSender struct {
	PrincipalID string `json:"principal_id"`
	PublicKey   string `json:"public_key"`
}

// EventContext contains only bounded source identifiers, never source content.
type EventContext struct {
	Event      string  `json:"event"`
	EventID    int64   `json:"event_id"`
	ProjectID  string  `json:"project_id,omitempty"`
	NodeID     string  `json:"node_id,omitempty"`
	NodeKey    string  `json:"node_key,omitempty"`
	EntryID    string  `json:"entry_id,omitempty"`
	ReleaseID  *string `json:"release_id,omitempty"`
	Name       string  `json:"release_name,omitempty"`
	Version    string  `json:"release_version,omitempty"`
	DeliveryID string  `json:"delivery_id,omitempty"`
	Source     string  `json:"source,omitempty"`
	Ref        string  `json:"ref,omitempty"`
}

func (t *Trigger) normalizeEvent() error {
	switch t.Event {
	case "release.published":
		if t.Filter != nil || t.External != nil {
			return fmt.Errorf("release trigger does not accept filters or sender configuration")
		}
	case "node.done", "knowledge.changed":
		if t.External != nil {
			return fmt.Errorf("internal events do not accept sender configuration")
		}
		if t.Filter == nil {
			return nil
		}
		f := t.Filter
		if len(f.ProjectIDs) > 20 {
			return fmt.Errorf("at most 20 source projects are allowed")
		}
		seen := map[string]bool{}
		for i, id := range f.ProjectIDs {
			id = strings.ToLower(id)
			if !workorders.UUID(id) || seen[id] {
				return fmt.Errorf("source projects must be unique UUIDs")
			}
			seen[id] = true
			f.ProjectIDs[i] = id
		}
		if t.Event == "node.done" && (f.EntryID != "" || f.KnowledgeType != "" || f.Tag != "") {
			return fmt.Errorf("knowledge filters require knowledge.changed")
		}
		if t.Event == "knowledge.changed" && (f.HasReleaseCopy || f.ExcludeHidden) {
			return fmt.Errorf("release-copy filters require node.done")
		}
		if f.EntryID != "" {
			if !workorders.UUID(f.EntryID) {
				return fmt.Errorf("entry_id must be a UUID")
			}
			f.EntryID = strings.ToLower(f.EntryID)
		}
		if len(f.Tag) > 128 || (f.Tag != "" && strings.TrimSpace(f.Tag) == "") {
			return fmt.Errorf("tag must be nonblank and at most 128 bytes")
		}
		if f.KnowledgeType != "" && knowledgeKind(f.KnowledgeType) == "" {
			return fmt.Errorf("unknown knowledge type")
		}
	case "external.tag", "external.deploy":
		if t.Filter != nil || t.External == nil || !workorders.UUID(t.External.PrincipalID) {
			return fmt.Errorf("external event requires sender principal and public key, without a filter")
		}
		t.External.PrincipalID = strings.ToLower(t.External.PrincipalID)
		if len(t.External.PublicKey) != 44 {
			return fmt.Errorf("public_key must be a base64 Ed25519 public key")
		}
		key, err := base64.StdEncoding.DecodeString(t.External.PublicKey)
		if err != nil || len(key) != ed25519.PublicKeySize || len(t.External.PublicKey) != 44 {
			return fmt.Errorf("public_key must be a base64 Ed25519 public key")
		}
	default:
		return fmt.Errorf("unsupported recurrence event")
	}
	return nil
}

func knowledgeKind(value string) string {
	switch value {
	case "runbook", "guideline", "memory", "decision":
		return value
	case "external-system", "related-project":
		return strings.ReplaceAll(value, "-", "_")
	}
	return ""
}

func sourceProjects(in Input) []string {
	if in.Trigger.Filter != nil && len(in.Trigger.Filter.ProjectIDs) > 0 {
		return in.Trigger.Filter.ProjectIDs
	}
	return []string{in.ProjectID}
}

// Called under the existing access fence for writes. Explicit source IDs must
// resolve through the caller's RLS, even when that caller has a workspace grant.
func authorizeSources(ctx context.Context, tx pgx.Tx, p tenant.Principal, in Input) error {
	if in.Trigger.Kind != "event" {
		return nil
	}
	if sender := in.Trigger.External; sender != nil {
		var valid bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM principals WHERE id=$1 AND kind='agent' AND status='active')`, sender.PrincipalID).Scan(&valid); err != nil {
			return err
		}
		if !valid {
			return workorders.Fail(404, "external sender not found")
		}
	}
	if in.Trigger.Event != "node.done" && in.Trigger.Event != "knowledge.changed" {
		return nil
	}
	permission := "nodes.read"
	if in.Trigger.Event == "knowledge.changed" {
		permission = "knowledge.read"
	}
	projects := sourceProjects(in)
	for _, id := range projects {
		if err := authz.RequireTx(ctx, tx, p, permission, authz.Scope{ProjectID: id}); err != nil {
			return err
		}
		var valid bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM nodes n JOIN node_kinds k ON k.id=n.kind_id AND k.tenant_id=n.tenant_id WHERE n.id=$1 AND n.deleted_at IS NULL AND k.slug='project')`, id).Scan(&valid); err != nil {
			return err
		}
		if !valid {
			return pgx.ErrNoRows
		}
	}
	if f := in.Trigger.Filter; f != nil && f.EntryID != "" {
		var valid bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM nodes n JOIN node_kinds k ON k.id=n.kind_id AND k.tenant_id=n.tenant_id WHERE n.id=$1 AND n.project_id=ANY($2::uuid[]) AND n.deleted_at IS NULL AND k.slug IN ('runbook','guideline','memory','external_system','related_project','decision'))`, f.EntryID, projects).Scan(&valid); err != nil {
			return err
		}
		if !valid {
			return pgx.ErrNoRows
		}
	}
	return nil
}

const sourceProjectSQL = `EXISTS(SELECT 1 FROM nodes source WHERE source.id=e.node_id AND ((coalesce(r.trigger#>'{filter,project_ids}','[]'::jsonb)='[]'::jsonb AND source.project_id=r.project_id) OR (r.trigger#>'{filter,project_ids}') ? source.project_id::text))`

const sourceTypeSQL = `(CASE r.trigger->>'event'
 WHEN 'release.published' THEN e.type='release.published' AND e.node_id=r.project_id
 WHEN 'node.done' THEN e.type IN ('node.updated','status_autopilot.changed','status_autopilot.undone','status_autopilot.derived','status_autopilot.causal_undo') AND e.before->>'state'<>e.after->>'state' AND ` + sourceProjectSQL + `
 WHEN 'knowledge.changed' THEN e.type IN ('knowledge.created','knowledge.updated','knowledge.deleted','knowledge.learning_accepted','node.updated') AND ` + sourceProjectSQL + ` AND EXISTS(SELECT 1 FROM nodes source JOIN node_kinds k ON k.tenant_id=source.tenant_id AND k.id=source.kind_id WHERE source.id=e.node_id AND k.slug IN ('runbook','guideline','memory','external_system','related_project','decision'))
 WHEN 'external.tag' THEN e.type='recurrence.external_received' AND e.node_id=r.project_id AND e.metadata->>'recurrence_id'=r.id::text
 WHEN 'external.deploy' THEN e.type='recurrence.external_received' AND e.node_id=r.project_id AND e.metadata->>'recurrence_id'=r.id::text
 ELSE false END)`

const sourceBatchSize = 100

type sourceCandidate struct {
	id int64
	at time.Time
}

func (m *Module) consumeSource(ctx context.Context, tx pgx.Tx, actor tenant.Principal, r Recurrence, now time.Time, deferred *[]string) error {
	rows, err := tx.Query(ctx, `SELECT e.id,e.at FROM events e JOIN recurrences r ON r.id=$1 WHERE e.id>$2 AND `+sourceTypeSQL+` ORDER BY e.id LIMIT $3`, r.ID, r.EventCursor, sourceBatchSize)
	if err != nil {
		return err
	}
	candidates := make([]sourceCandidate, 0, sourceBatchSize)
	for rows.Next() {
		var candidate sourceCandidate
		if err := rows.Scan(&candidate.id, &candidate.at); err != nil {
			rows.Close()
			return err
		}
		candidates = append(candidates, candidate)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	// Skip filtered events in one bounded claim, with only one owner/access
	// setup. Stop at the first match: occurSource appends events, so no later
	// candidate may acquire target or recurrence locks in this transaction.
	cursor := r.EventCursor
	pending := make([]sourceCandidate, 0, len(candidates))
	waiting := false
	for _, candidate := range candidates {
		if eventTime(r.Trigger, candidate.at).After(now) {
			waiting = true
			break
		}
		cursor = candidate.id
		if !candidate.at.Before(r.ActiveSince) {
			pending = append(pending, candidate)
		}
	}
	if len(pending) > 0 {
		source, key, err := sourceContext(ctx, tx, actor.TenantID, r, pending)
		if err != nil {
			return err
		}
		if source != nil {
			cursor = source.EventID
			waiting = false
			var at time.Time
			for _, candidate := range pending {
				if candidate.id == cursor {
					at = candidate.at
					break
				}
			}
			if _, err := occurSource(ctx, tx, actor, r, key, eventTime(r.Trigger, at), &cursor, "", "", source); err != nil {
				return err
			}
		}
	}
	if waiting {
		*deferred = append(*deferred, r.ID)
	}
	if cursor == r.EventCursor {
		return nil
	}
	_, err = tx.Exec(ctx, `UPDATE recurrences SET event_cursor=$2 WHERE id=$1`, r.ID, cursor)
	return err
}

// The service discovers only event IDs and times. Payload inspection then uses the
// definition owner's CURRENT RLS and permission bindings in this same locked
// transaction, including event-reference visibility. Restore service visibility
// before target writes; there are no event-counter locks in this read step.
func sourceContext(ctx context.Context, tx pgx.Tx, tenantID string, r Recurrence, candidates []sourceCandidate) (*EventContext, string, error) {
	owner := tenant.Principal{ID: r.CreatedBy, TenantID: tenantID, FullAccess: true}
	if err := tx.QueryRow(ctx, `SELECT kind FROM principals WHERE id=$1 AND status='active'`, owner.ID).Scan(&owner.Kind); err != nil {
		return nil, "", err
	}
	if _, err := tx.Exec(ctx, `SELECT aeon_enter_principal($1::uuid,$2::uuid,NULL::uuid)`, tenantID, owner.ID); err != nil {
		return nil, "", err
	}
	var source *EventContext
	var key string
	err := authorizeDefinition(ctx, tx, owner, r.Input)
	if err == nil {
		for _, candidate := range candidates {
			source, key, err = inspectSource(ctx, tx, owner, r, candidate.id)
			if err != nil || source != nil {
				break
			}
		}
	}
	_, restoreErr := tx.Exec(ctx, `SELECT set_config('aeon.visible_projects','*',true),set_config('aeon.principal_ids','',true),set_config('aeon.system','on',true)`)
	return source, key, errors.Join(err, restoreErr)
}

func inspectSource(ctx context.Context, tx pgx.Tx, owner tenant.Principal, r Recurrence, id int64) (*EventContext, string, error) {
	if r.Trigger.External != nil {
		var raw []byte
		var sender string
		err := tx.QueryRow(ctx, `SELECT after,actor_principal_id::text FROM events WHERE id=$1 AND type='recurrence.external_received' AND metadata->>'recurrence_id'=$2 AND octet_length(after::text)<=8192`, id, r.ID).Scan(&raw, &sender)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, "", nil
		}
		if err != nil {
			return nil, "", err
		}
		var e ExternalEvent
		if err = json.Unmarshal(raw, &e); err != nil {
			return nil, "", err
		}
		if sender != r.Trigger.External.PrincipalID || e.Event != r.Trigger.Event {
			return nil, "", nil
		}
		// Sender grants can be revoked after intake but before the scheduler.
		p := tenant.Principal{ID: sender, TenantID: owner.TenantID, Kind: tenant.Agent, FullAccess: true}
		if err = authz.RequireTx(ctx, tx, p, "nodes.read", authz.Scope{ProjectID: r.ProjectID}); err != nil {
			return nil, "", err
		}
		if err = manage(ctx, tx, p, r.ProjectID); err != nil {
			return nil, "", err
		}
		return &EventContext{Event: e.Event, EventID: id, DeliveryID: e.DeliveryID, Source: e.Source, Ref: e.Ref}, externalKey(sender, e.DeliveryID), nil
	}
	filter := EventFilter{}
	if r.Trigger.Filter != nil {
		filter = *r.Trigger.Filter
	}
	var nodeID, nodeKey, projectID, kind string
	var done, copy, hidden, tag bool
	query := `WITH ` + workstate.CategoryCTE() + `
 SELECT n.id::text,left(coalesce(e.after->>'key',n.key),80),n.project_id::text,k.slug,
 e.before->>'state' IS NOT NULL AND e.after->>'state' IS NOT NULL AND ` + workstate.CountBucketSQL("e.before->>'state'", "cb") + `<>'done' AND ` + workstate.CountBucketSQL("e.after->>'state'", "ca") + `='done',
 btrim(coalesce(e.after#>>'{fields,pill_en}',''))<>'' AND btrim(coalesce(e.after#>>'{fields,pill_de}',''))<>'' AND btrim(coalesce(e.after#>>'{fields,benefit_en}',''))<>'' AND btrim(coalesce(e.after#>>'{fields,benefit_de}',''))<>'',
 coalesce(e.after#>>'{fields,hide_from_release_notes}','false')<>'false',
 coalesce(jsonb_typeof(e.after#>'{fields,tags}')='array' AND (e.after#>'{fields,tags}') ? $3,false)
 FROM events e JOIN nodes n ON n.tenant_id=e.tenant_id AND n.id=e.node_id JOIN node_kinds k ON k.tenant_id=n.tenant_id AND k.id=n.kind_id
 LEFT JOIN configured cb ON cb.kind_id=n.kind_id AND cb.norm=` + workstate.NormSQL("e.before->>'state'") + `
 LEFT JOIN configured ca ON ca.kind_id=n.kind_id AND ca.norm=` + workstate.NormSQL("e.after->>'state'") + `
 WHERE e.id=$1 AND n.deleted_at IS NULL AND n.project_id=ANY($2::uuid[])
 AND (e.after->>'kind_id' IS NULL OR e.after->>'kind_id'=n.kind_id::text)
 AND (e.after->>'project_id' IS NULL OR e.after->>'project_id'=n.project_id::text)`
	err := tx.QueryRow(ctx, query, id, sourceProjects(r.Input), filter.Tag).Scan(&nodeID, &nodeKey, &projectID, &kind, &done, &copy, &hidden, &tag)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, "", nil
	}
	if err != nil {
		return nil, "", err
	}
	source := &EventContext{Event: r.Trigger.Event, EventID: id, ProjectID: projectID, NodeID: nodeID, NodeKey: nodeKey}
	if r.Trigger.Event == "node.done" {
		if (kind != "work" && kind != "ticket" && kind != "task" && kind != "epic") || !done || (filter.HasReleaseCopy && !copy) || (filter.ExcludeHidden && hidden) {
			return nil, "", nil
		}
	} else {
		if knowledgeKind(strings.ReplaceAll(kind, "_", "-")) == "" || (filter.EntryID != "" && filter.EntryID != nodeID) || (filter.KnowledgeType != "" && knowledgeKind(filter.KnowledgeType) != kind) || (filter.Tag != "" && !tag) {
			return nil, "", nil
		}
		source.EntryID = nodeID
	}
	return source, fmt.Sprintf("event:%d", id), nil
}

func boundedText(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	value = value[:limit]
	for !utf8.ValidString(value) {
		value = value[:len(value)-1]
	}
	return value
}
