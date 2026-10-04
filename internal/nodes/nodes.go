// SPDX-License-Identifier: AGPL-3.0-only

package nodes

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/inspr-at/paimos/internal/agentpairing"
	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/eta"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/inspr-at/paimos/internal/ticketbenefits"
	"github.com/inspr-at/paimos/internal/workqueue"
)

const nodeReturning = `id::text, key, kind_id::text, title, body, fields, state, parent_id::text, position::text, created_at, updated_at, deleted_at, human_check`

const nodeCols = `n.id::text, n.key, n.kind_id::text, n.title, n.body, n.fields, n.state, n.parent_id::text, n.position::text, n.created_at, n.updated_at, n.deleted_at, n.human_check`

type nodeJSON struct {
	Queued     *workqueue.Queued `json:"queued,omitempty"`
	HumanCheck *string           `json:"human_check"`
	Estimate   *estimateView     `json:"estimate,omitempty"`
	Warnings   []string          `json:"warnings,omitempty"`
	ID         string            `json:"id"`
	Key        string            `json:"key"`
	KindID     string            `json:"kind_id"`
	Title      string            `json:"title"`
	Body       string            `json:"body"`
	Fields     json.RawMessage   `json:"fields"`
	State      string            `json:"state"`
	ParentID   *string           `json:"parent_id"`
	Position   string            `json:"position"`
	CreatedAt  time.Time         `json:"created_at"`
	UpdatedAt  time.Time         `json:"updated_at"`
	DeletedAt  *time.Time        `json:"deleted_at"`
}

type nodeCreate struct {
	HumanCheck json.RawMessage `json:"human_check"`
	KindID     string          `json:"kind_id"`
	Key        *string         `json:"key"`
	KeyPrefix  *string         `json:"key_prefix"`
	Title      string          `json:"title"`
	Body       *string         `json:"body"`
	Fields     json.RawMessage `json:"fields"`
	State      *string         `json:"state"`
	ParentID   *string         `json:"parent_id"`
	BeforeID   *string         `json:"before_id"`
}

func (m *Module) handleCreateNode(w http.ResponseWriter, r *http.Request) {
	p, ok := requirePrincipal(w, r)
	if !ok {
		return
	}
	body, ok := readBody(w, r)
	if !ok {
		return
	}
	var in nodeCreate
	if err := decodeJSON(body, &in); err != nil {
		writeErr(w, err)
		return
	}
	node, err := m.createNode(r.Context(), p, in)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, node)
}

func (m *Module) handleGetNode(w http.ResponseWriter, r *http.Request) {
	p, ok := requirePrincipal(w, r)
	if !ok {
		return
	}
	id, ok := pathUUID(w, r.PathValue("nodeId"), "invalid node id")
	if !ok {
		return
	}
	node, err := m.getNode(r.Context(), p.TenantID, id)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, node)
}

func (m *Module) handleUpdateNode(w http.ResponseWriter, r *http.Request) {
	p, ok := requirePrincipal(w, r)
	if !ok {
		return
	}
	id, ok := pathUUID(w, r.PathValue("nodeId"), "invalid node id")
	if !ok {
		return
	}
	body, ok := readBody(w, r)
	if !ok {
		return
	}
	raw, err := decodeObject(body)
	if err != nil {
		writeErr(w, err)
		return
	}
	expected, err := parseUnmodifiedSince(r.Header)
	if err != nil {
		writeErr(w, err)
		return
	}
	node, err := m.updateNode(r.Context(), p, id, raw, expected)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, node)
}

func (m *Module) handleDeleteNode(w http.ResponseWriter, r *http.Request) {
	p, ok := requirePrincipal(w, r)
	if !ok {
		return
	}
	id, ok := pathUUID(w, r.PathValue("nodeId"), "invalid node id")
	if !ok {
		return
	}
	revision, err := m.deleteNode(r.Context(), p, id)
	if err != nil {
		writeErr(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	// The node's updated_at after the delete: the revision its node.deleted
	// event carries, so the caller knows that event as its own (AEON-326).
	w.Header().Set("Aeon-Revision", revision.Format(time.RFC3339Nano))
	w.WriteHeader(http.StatusNoContent)
}

func (m *Module) handleMoveNode(w http.ResponseWriter, r *http.Request) {
	p, ok := requirePrincipal(w, r)
	if !ok {
		return
	}
	id, ok := pathUUID(w, r.PathValue("nodeId"), "invalid node id")
	if !ok {
		return
	}
	body, ok := readBody(w, r)
	if !ok {
		return
	}
	raw, err := decodeObject(body)
	if err != nil {
		writeErr(w, err)
		return
	}
	parentID, beforeID, err := parseMove(raw)
	if err != nil {
		writeErr(w, err)
		return
	}
	expected, err := parseUnmodifiedSince(r.Header)
	if err != nil {
		writeErr(w, err)
		return
	}
	node, err := m.moveNode(r.Context(), p, id, parentID, beforeID, expected)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, node)
}

func (m *Module) getNode(ctx context.Context, tenantID, id string) (nodeJSON, error) {
	ctx, cancel := context.WithTimeout(ctx, eta.AggregateReadTimeout)
	defer cancel()
	var node nodeJSON
	err := m.tx(ctx, tenantID, func(ctx context.Context, tx pgx.Tx) error {
		loaded, err := loadNode(ctx, tx, id, false)
		if err != nil {
			return err
		}
		node = loaded
		views, err := loadEstimates(ctx, tx, []string{id})
		if err != nil {
			return err
		}
		node.Estimate = views[id]
		queued, err := workqueue.Load(ctx, tx, []string{id})
		if err != nil {
			return err
		}
		node.Queued = queued[id]
		return nil
	})
	return node, err
}

func (m *Module) createNode(ctx context.Context, p tenant.Principal, in nodeCreate) (nodeJSON, error) {
	humanCheck, err := parseHumanCheck(in.HumanCheck)
	if err != nil {
		return nodeJSON{}, err
	}
	kindID, ok := parseUUID(in.KindID)
	if !ok {
		return nodeJSON{}, badRequest("invalid kind_id")
	}
	if !nonBlank(in.Title) {
		return nodeJSON{}, badRequest("title is required")
	}
	explicit, prefix, err := createKeyChoice(in.Key, in.KeyPrefix)
	if err != nil {
		return nodeJSON{}, err
	}
	body := ""
	if in.Body != nil {
		body = *in.Body
	}
	state := "open"
	if in.State != nil {
		if !nonBlank(*in.State) {
			return nodeJSON{}, badRequest("invalid state")
		}
		state = *in.State
	}
	parentID, err := optionalUUID(in.ParentID, "invalid parent_id")
	if err != nil {
		return nodeJSON{}, err
	}
	beforeID, err := optionalUUID(in.BeforeID, "invalid before_id")
	if err != nil {
		return nodeJSON{}, err
	}
	var node nodeJSON
	err = m.tx(ctx, p.TenantID, func(ctx context.Context, tx pgx.Tx) error {
		if err := armPortalModeration(ctx, tx, p); err != nil {
			return err
		}
		if err := lockTree(ctx, tx); err != nil {
			return err
		}
		kind, schema, err := loadKind(ctx, tx, kindID)
		if he, ok := err.(*httpError); ok && he.status == http.StatusNotFound {
			return badRequest("kind not found")
		}
		if err != nil {
			return err
		}
		fields, err := validateFields(schema, in.Fields)
		if err == nil && (kind.Slug == "work" || kind.Slug == "ticket" || kind.Slug == "task") {
			fields, err = humanCheckFields(p, fields, nil, nil, humanCheck, true)
		}
		if humanCheck != nil && kind.Slug != "work" && kind.Slug != "ticket" && kind.Slug != "task" {
			return badRequest("human_check is for work items, tickets and tasks")
		}
		if err == nil {
			fields, err = canonicalEstimate(ctx, tx, p, "", fields, nil)
		}
		if err == nil {
			fields, err = canonicalTicketRouteFields(ctx, tx, p, kind.Slug, parentID, fields, nil)
		}
		if err == nil {
			fields, err = suggestEstimateRoute(ctx, tx, p, kind.Slug, in.Title, parentID, fields, nil)
		}
		if err == nil {
			fields, err = canonicalRoadmapPublication(p, kind.Slug, fields, nil)
		}
		if err == nil {
			fields, err = canonicalAssignments(ctx, tx, p.TenantID, fields, nil)
		}
		if err != nil {
			return err
		}
		if issues := ticketbenefits.Transition(kind.Slug, "", state, fields); len(issues) > 0 {
			return unprocessableCoded("before done: "+strings.Join(issues, "; "), ticketbenefits.RequiredCode)
		}
		if parentID != nil {
			if err := ensureParentAllows(ctx, tx, *parentID, kind.Slug); err != nil {
				return err
			}
		}
		if err := requireCreateTarget(ctx, tx, p, kind.Slug, parentID); err != nil {
			return err
		}
		key := explicit
		if key == "" {
			usePrefix := kind.ShortPrefix
			if prefix != "" {
				usePrefix = prefix
			}
			if err := tx.QueryRow(ctx, `
				SELECT aeon_next_node_key(NULLIF(current_setting('aeon.tenant_id', true), '')::uuid, $1)`,
				usePrefix).Scan(&key); err != nil {
				return dbErr("allocate node key", err)
			}
		}
		siblings, err := loadSiblings(ctx, tx, parentID)
		if err != nil {
			return err
		}
		position, updates, err := Place(siblings, "", beforeID)
		if err != nil {
			return err
		}
		if err := applyPositions(ctx, tx, updates); err != nil {
			return err
		}
		row := tx.QueryRow(ctx, `
			INSERT INTO nodes (tenant_id, key, kind_id, title, body, fields, state, parent_id, position, human_check)
			VALUES (
				NULLIF(current_setting('aeon.tenant_id', true), '')::uuid,
    $1, $2::uuid, $3, $4, $5::jsonb, $6, $7::uuid, $8::numeric, $9
			)
			RETURNING `+nodeReturning,
			key, kindID, in.Title, body, string(fields), state, parentID, position, humanCheck)
		loaded, scanErr := scanNode(row)
		if scanErr != nil {
			return dbErr("insert node", scanErr)
		}
		if err := m.record(ctx, tx, p.ID, &loaded.ID, evNodeCreated, nil, loaded); err != nil {
			return err
		}
		node = loaded
		if kind.Slug == "work" || kind.Slug == "ticket" {
			node.Warnings = ticketbenefits.Issues(fields)
		}
		if p.Kind == tenant.Agent && (kind.Slug == "work" || kind.Slug == "ticket" || kind.Slug == "task") {
			var f map[string]any
			_ = json.Unmarshal(fields, &f)
			if f["estimate_hours"] == nil {
				node.Warnings = append(node.Warnings, missingEstimateWarning())
			}
		}
		views, err := loadEstimates(ctx, tx, []string{loaded.ID})
		if err != nil {
			return err
		}
		node.Estimate = views[loaded.ID]
		return nil
	})
	return node, err
}

func (m *Module) updateNode(ctx context.Context, p tenant.Principal, id string, raw map[string]json.RawMessage, expected *time.Time) (nodeJSON, error) {
	if len(raw) == 0 {
		return nodeJSON{}, badRequest("patch is empty")
	}
	for key := range raw {
		switch key {
		case "title", "body", "fields", "estimate_hours", "state", "human_check", "kind_id", "type", "kind":
		case "key":
			return nodeJSON{}, badRequest("key is immutable")
		case "parent_id":
			return nodeJSON{}, badRequest("parent is immutable; use move")
		default:
			return nodeJSON{}, badRequest("unknown field")
		}
	}
	var node nodeJSON
	err := m.tx(ctx, p.TenantID, func(ctx context.Context, tx pgx.Tx) error {
		if value, ok := raw["state"]; ok {
			state, _ := parsePatchString(value)
			if workqueue.Terminal(state) {
				if err := agentpairing.Lock(ctx, tx); err != nil {
					return err
				}
			}
		}
		// Match queue writes and delete: tree before any node row lock. Terminal
		// edits additionally take pairing first to serialize cancellation and claim.
		if err := lockTree(ctx, tx); err != nil {
			return err
		}
		if err := armPortalModeration(ctx, tx, p); err != nil {
			return err
		}
		current, err := loadNode(ctx, tx, id, true)
		if err != nil {
			return err
		}
		if _, renaming := raw["title"]; renaming {
			kind, _, err := loadKind(ctx, tx, current.KindID)
			if err != nil {
				return err
			}
			if kind.Slug == "tag" {
				return badRequest("rename tags through /api/tags/{tagId}")
			}
		}
		if _, statusWrite := raw["state"]; statusWrite {
			if err := requireLeafStatusWrite(ctx, tx, id); err != nil {
				return err
			}
		}
		// Compare after SELECT FOR UPDATE, so competing patches cannot both
		// consume the same timestamp. Advance even on equal clock readings.
		if expected != nil && !current.UpdatedAt.Equal(*expected) {
			return &httpError{status: http.StatusPreconditionFailed, msg: "node has changed"}
		}
		kindOnly, err := refuseKindChange(ctx, tx, current, raw)
		if err != nil {
			return err
		}
		if kindOnly {
			node = current
			views, err := loadEstimates(ctx, tx, []string{id})
			if err != nil {
				return err
			}
			node.Estimate = views[id]
			return nil
		}
		if hours, ok := raw["estimate_hours"]; ok {
			if _, replaces := raw["fields"]; replaces {
				return badRequest("estimate_hours cannot be combined with fields")
			}
			var fields map[string]json.RawMessage
			if err := json.Unmarshal(current.Fields, &fields); err != nil {
				return err
			}
			for _, key := range []string{"estimate_source", "estimate_by", "estimate_at", "estimate_confirmed"} {
				delete(fields, key)
			}
			fields["estimate_hours"] = hours
			merged, err := json.Marshal(fields)
			if err != nil {
				return err
			}
			// Use the normal schema, attribution, event and revision paths.
			raw["fields"] = merged
		}
		nextState, nextFields := current.State, current.Fields
		sets := []string{"updated_at = greatest(clock_timestamp(), updated_at + interval '1 microsecond')"}
		args := []any{}
		add := func(v any) string {
			args = append(args, v)
			return fmt.Sprintf("$%d", len(args))
		}
		if v, ok := raw["title"]; ok {
			s, err := parsePatchString(v)
			if err != nil || !nonBlank(s) {
				return badRequest("title is required")
			}
			sets = append(sets, "title = "+add(s))
		}
		if v, ok := raw["body"]; ok {
			s, err := parsePatchString(v)
			if err != nil {
				return err
			}
			sets = append(sets, "body = "+add(s))
		}
		if v, ok := raw["state"]; ok {
			s, err := parsePatchString(v)
			if err != nil || !nonBlank(s) {
				return badRequest("invalid state")
			}
			nextState = s
			sets = append(sets, "state = "+add(s))
		}
		if v, ok := raw["fields"]; ok {
			kind, schema, err := loadKind(ctx, tx, current.KindID)
			if err != nil {
				return err
			}
			fields, err := validateFields(schema, v)
			if err == nil {
				fields, err = canonicalEstimate(ctx, tx, p, id, fields, current.Fields)
			}
			if err == nil {
				fields, err = canonicalTicketRouteFields(ctx, tx, p, kind.Slug, current.ParentID, fields, current.Fields)
			}
			if err == nil {
				title := current.Title
				if value, ok := raw["title"]; ok {
					title, _ = parsePatchString(value)
				}
				fields, err = suggestEstimateRoute(ctx, tx, p, kind.Slug, title, current.ParentID, fields, current.Fields)
			}
			if err == nil {
				fields, err = canonicalRoadmapPublication(p, kind.Slug, fields, current.Fields)
			}
			if err == nil {
				fields, err = canonicalAssignments(ctx, tx, p.TenantID, fields, current.Fields)
			}
			if err != nil {
				return err
			}
			nextFields = fields
		}
		kind, _, err := loadKind(ctx, tx, current.KindID)
		if err != nil {
			return err
		}
		nextCheck := current.HumanCheck
		rawCheck, checkChanged := raw["human_check"]
		if checkChanged {
			if kind.Slug != "work" && kind.Slug != "ticket" && kind.Slug != "task" {
				return badRequest("human_check is for work items, tickets and tasks")
			}
			nextCheck, err = parseHumanCheck(rawCheck)
			if err != nil {
				return err
			}
			sets = append(sets, "human_check = "+add(nextCheck))
		}
		if kind.Slug == "work" || kind.Slug == "ticket" || kind.Slug == "task" {
			nextFields, err = humanCheckFields(p, nextFields, current.Fields, current.HumanCheck, nextCheck, checkChanged)
			if err != nil {
				return err
			}
		}
		if _, fieldsChanged := raw["fields"]; fieldsChanged || checkChanged {
			sets = append(sets, "fields = "+add(string(nextFields))+"::jsonb")
		}
		if ticketbenefits.Completed(nextState) && !ticketbenefits.Completed(current.State) {
			kind, _, err := loadKind(ctx, tx, current.KindID)
			if err != nil {
				return err
			}
			if issues := ticketbenefits.Transition(kind.Slug, current.State, nextState, nextFields); len(issues) > 0 {
				return unprocessableCoded("before done: "+strings.Join(issues, "; "), ticketbenefits.RequiredCode)
			}
		}
		if _, hasState := raw["state"]; hasState && workqueue.Terminal(nextState) {
			if _, err := workqueue.RemoveQueued(ctx, tx, p, id); err != nil {
				return err
			}
		}
		idPh := add(id)
		q := fmt.Sprintf(`UPDATE nodes SET %s WHERE id = %s::uuid AND deleted_at IS NULL RETURNING %s`,
			strings.Join(sets, ", "), idPh, nodeReturning)
		loaded, scanErr := scanNode(tx.QueryRow(ctx, q, args...))
		if errors.Is(scanErr, pgx.ErrNoRows) {
			return notFound("node not found")
		}
		if scanErr != nil {
			return dbErr("update node", scanErr)
		}
		if err := m.record(ctx, tx, p.ID, &loaded.ID, evNodeUpdated, current, loaded); err != nil {
			return err
		}
		node = loaded
		views, err := loadEstimates(ctx, tx, []string{id})
		if err != nil {
			return err
		}
		node.Estimate = views[id]
		return nil
	})
	return node, dbErr("update node", err)
}

func (m *Module) deleteNode(ctx context.Context, p tenant.Principal, id string) (time.Time, error) {
	var revision time.Time
	err := m.tx(ctx, p.TenantID, func(ctx context.Context, tx pgx.Tx) error {
		if err := agentpairing.Lock(ctx, tx); err != nil {
			return err
		}
		if err := armPortalModeration(ctx, tx, p); err != nil {
			return err
		}
		if err := lockTree(ctx, tx); err != nil {
			return err
		}
		current, err := loadNode(ctx, tx, id, true)
		if err != nil {
			return err
		}
		kind, _, err := loadKind(ctx, tx, current.KindID)
		if err != nil {
			return err
		}
		if kind.Slug == "tag" {
			return badRequest("delete tags through /api/tags/{tagId}")
		}
		if _, err := workqueue.RemoveQueued(ctx, tx, p, id); err != nil {
			return err
		}
		// Like every other write, the delete moves updated_at forward: a
		// transaction that started before a later update committed must not
		// give the deletion an older revision than that update (AEON-326).
		loaded, scanErr := scanNode(tx.QueryRow(ctx, `
			UPDATE nodes SET deleted_at = now(), updated_at = greatest(clock_timestamp(), updated_at + interval '1 microsecond')
			WHERE id = $1::uuid AND deleted_at IS NULL
			RETURNING `+nodeReturning, id))
		if errors.Is(scanErr, pgx.ErrNoRows) {
			return notFound("node not found")
		}
		if scanErr != nil {
			return dbErr("delete node", scanErr)
		}
		revision = loaded.UpdatedAt
		return m.record(ctx, tx, p.ID, &loaded.ID, evNodeDeleted, current, loaded)
	})
	return revision, err
}

func (m *Module) moveNode(ctx context.Context, p tenant.Principal, id string, parentID, beforeID *string, expected *time.Time) (nodeJSON, error) {
	var node nodeJSON
	err := m.tx(ctx, p.TenantID, func(ctx context.Context, tx pgx.Tx) error {
		if err := armPortalModeration(ctx, tx, p); err != nil {
			return err
		}
		if err := lockTree(ctx, tx); err != nil {
			return err
		}
		current, err := loadNode(ctx, tx, id, true)
		if err != nil {
			return err
		}
		// Check the locked snapshot before any position or parent changes.
		if expected != nil && !current.UpdatedAt.Truncate(time.Second).Equal(expected.Truncate(time.Second)) {
			return &httpError{status: http.StatusPreconditionFailed, msg: "node has changed", node: &current}
		}
		if parentID != nil && *parentID == current.ID {
			return conflict("node cannot parent itself")
		}
		if !sameString(parentID, current.ParentID) {
			if err := requireMoveTarget(ctx, tx, p, current.ID, parentID); err != nil {
				return err
			}
		}
		if parentID != nil && !sameString(parentID, current.ParentID) {
			kind, _, err := loadKind(ctx, tx, current.KindID)
			if err != nil {
				return err
			}
			if err := ensureParentAllows(ctx, tx, *parentID, kind.Slug); err != nil {
				if he, ok := err.(*httpError); ok && he.msg == "parent node does not exist or is deleted" {
					return notFound("parent not found")
				}
				return err
			}
		} else if parentID != nil {
			if err := parentExists(ctx, tx, *parentID); err != nil {
				return err
			}
		}
		journeyBefore, err := subtreeJourneyTickets(ctx, tx, id)
		if err != nil {
			return err
		}
		siblings, err := loadSiblings(ctx, tx, parentID)
		if err != nil {
			return err
		}
		position, updates, err := Place(siblings, current.ID, beforeID)
		if err != nil {
			return err
		}
		if err := applyPositions(ctx, tx, updates); err != nil {
			return err
		}
		loaded, scanErr := scanNode(tx.QueryRow(ctx, `
			UPDATE nodes
			SET parent_id = $1::uuid, position = $2::numeric,
			    updated_at = greatest(clock_timestamp(), date_trunc('second', updated_at) + interval '1 second')
			WHERE id = $3::uuid AND deleted_at IS NULL
			RETURNING `+nodeReturning, parentID, position, id))
		if errors.Is(scanErr, pgx.ErrNoRows) {
			return notFound("node not found")
		}
		if scanErr != nil {
			return dbErr("move node", scanErr)
		}
		changedBefore, changedAfter, err := movedJourneyTickets(ctx, tx, journeyBefore)
		if err != nil {
			return err
		}
		var beforeEvent, afterEvent any = current, loaded
		if len(changedBefore) > 0 {
			beforeEvent = movedNodeSnapshot{nodeJSON: current, JourneyTickets: changedBefore}
			afterEvent = movedNodeSnapshot{nodeJSON: loaded, JourneyTickets: changedAfter}
		}
		if err := m.record(ctx, tx, p.ID, &loaded.ID, evNodeMoved, beforeEvent, afterEvent); err != nil {
			return err
		}
		node = loaded
		return nil
	})
	return node, err
}

// parseUnmodifiedSince shares PATCH's accepted timestamp syntax. The caller
// chooses the comparison precision while holding the node row lock.
func parseUnmodifiedSince(header http.Header) (*time.Time, error) {
	values, present := header["If-Unmodified-Since"]
	if !present {
		return nil, nil
	}
	if len(values) != 1 {
		return nil, badRequest("invalid If-Unmodified-Since")
	}
	at, err := time.Parse(time.RFC3339Nano, values[0])
	if err != nil {
		at, err = http.ParseTime(values[0])
	}
	if err != nil {
		return nil, badRequest("invalid If-Unmodified-Since")
	}
	return &at, nil
}

func createKeyChoice(key, prefix *string) (explicit, generatedPrefix string, err error) {
	if key != nil && *key != "" && prefix != nil && *prefix != "" {
		return "", "", badRequest("set key or key_prefix, not both")
	}
	if key != nil && *key != "" {
		if !validKey(*key) {
			return "", "", badRequest("invalid node key")
		}
		return *key, "", nil
	}
	if prefix != nil && *prefix != "" {
		if !validPrefix(*prefix) {
			return "", "", badRequest("invalid key prefix")
		}
		return "", *prefix, nil
	}
	return "", "", nil
}

func optionalUUID(id *string, msg string) (*string, error) {
	if id == nil || *id == "" {
		return nil, nil
	}
	parsed, ok := parseUUID(*id)
	if !ok {
		return nil, badRequest(msg)
	}
	return &parsed, nil
}

func parseMove(raw map[string]json.RawMessage) (parentID, beforeID *string, err error) {
	if len(raw) == 0 {
		return nil, nil, badRequest("parent_id is required")
	}
	if _, ok := raw["parent_id"]; !ok {
		return nil, nil, badRequest("parent_id is required")
	}
	for key := range raw {
		switch key {
		case "parent_id", "before_id":
		default:
			return nil, nil, badRequest("unknown field")
		}
	}
	parentID, err = parseNullableUUID(raw["parent_id"], "parent_id must be a uuid or null")
	if err != nil {
		return nil, nil, err
	}
	if v, ok := raw["before_id"]; ok {
		beforeID, err = parseNullableUUID(v, "before_id must be a uuid or null")
		if err != nil {
			return nil, nil, err
		}
	}
	return parentID, beforeID, nil
}

func parseNullableUUID(raw json.RawMessage, msg string) (*string, error) {
	if string(raw) == "null" {
		return nil, nil
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return nil, badRequest(msg)
	}
	id, ok := parseUUID(s)
	if !ok {
		return nil, badRequest(msg)
	}
	return &id, nil
}

func ensureParentAllows(ctx context.Context, tx pgx.Tx, parentID, childSlug string) error {
	var kindID string
	err := tx.QueryRow(ctx, `
		SELECT kind_id::text FROM nodes WHERE id = $1::uuid AND deleted_at IS NULL`, parentID).Scan(&kindID)
	if errors.Is(err, pgx.ErrNoRows) {
		return badRequest("parent node does not exist or is deleted")
	}
	if err != nil {
		return err
	}
	parentKind, _, err := loadKind(ctx, tx, kindID)
	if err != nil {
		return err
	}
	if !childAllowed(parentKind.AllowedChildKinds, childSlug) {
		return conflict("child kind is not allowed under parent kind")
	}
	return nil
}

func parentExists(ctx context.Context, tx pgx.Tx, parentID string) error {
	var one int
	err := tx.QueryRow(ctx, `SELECT 1 FROM nodes WHERE id = $1::uuid AND deleted_at IS NULL`, parentID).Scan(&one)
	if errors.Is(err, pgx.ErrNoRows) {
		return notFound("parent not found")
	}
	return err
}

func loadNode(ctx context.Context, tx pgx.Tx, id string, forUpdate bool) (nodeJSON, error) {
	q := `SELECT ` + nodeCols + ` FROM nodes n WHERE n.id = $1::uuid AND n.deleted_at IS NULL`
	if forUpdate {
		q += ` FOR UPDATE`
	}
	node, err := scanNode(tx.QueryRow(ctx, q, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return nodeJSON{}, notFound("node not found")
	}
	if err != nil {
		return nodeJSON{}, err
	}
	return node, nil
}

func loadSiblings(ctx context.Context, tx pgx.Tx, parentID *string) ([]sibling, error) {
	rows, err := tx.Query(ctx, `
		SELECT id::text, position::text
		FROM nodes
		WHERE deleted_at IS NULL AND parent_id IS NOT DISTINCT FROM $1::uuid
		ORDER BY position, id
		FOR UPDATE`, parentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []sibling
	for rows.Next() {
		var s sibling
		if err := rows.Scan(&s.ID, &s.Position); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

func applyPositions(ctx context.Context, tx pgx.Tx, updates []sibling) error {
	if len(updates) == 0 {
		return nil
	}
	ids := make([]string, len(updates))
	pos := make([]string, len(updates))
	for i, u := range updates {
		ids[i] = u.ID
		pos[i] = u.Position
	}
	_, err := tx.Exec(ctx, `
		UPDATE nodes AS n
		SET position = u.pos::numeric
		FROM (SELECT unnest($1::uuid[]) AS id, unnest($2::text[]) AS pos) AS u
		WHERE n.id = u.id`, ids, pos)
	return dbErr("place siblings", err)
}

func scanNode(row pgx.Row) (nodeJSON, error) {
	var n nodeJSON
	var fields string
	var position string
	if err := row.Scan(
		&n.ID, &n.Key, &n.KindID, &n.Title, &n.Body, &fields, &n.State,
		&n.ParentID, &position, &n.CreatedAt, &n.UpdatedAt, &n.DeletedAt, &n.HumanCheck,
	); err != nil {
		return nodeJSON{}, err
	}
	if fields == "" {
		fields = "{}"
	}
	n.Fields = json.RawMessage(fields)
	n.Position = trimDecimal(position)
	return n, nil
}

// errMoveForbidden: the caller lacks nodes.move in a project a move changes.
var errMoveForbidden = errors.New("move not permitted")

// moveScopes lists the projects a re-parenting changes, "" standing for the
// workspace: the node's own project, the project of its current parent and
// the project of its new parent (no parent, or a parent outside every
// project, is the workspace). A new parent the caller cannot see is
// pgx.ErrNoRows. A current parent the caller cannot see (a nested project
// under another project) refuses the move: detaching a node changes its
// parent's project, which the caller cannot even see (fail closed).
func moveScopes(ctx context.Context, tx pgx.Tx, nodeID string, newParent *string) ([]string, error) {
	var own, currentParentProject *string
	var hasParent, parentVisible bool
	if err := tx.QueryRow(ctx, `SELECT n.project_id::text, n.parent_id IS NOT NULL, p.id IS NOT NULL, p.project_id::text
		FROM nodes n LEFT JOIN nodes p ON p.tenant_id=n.tenant_id AND p.id=n.parent_id
		WHERE n.id=$1::uuid`, nodeID).Scan(&own, &hasParent, &parentVisible, &currentParentProject); err != nil {
		return nil, err
	}
	if hasParent && !parentVisible {
		return nil, errMoveForbidden
	}
	scopes := []string{deref(own), deref(currentParentProject)}
	if newParent == nil {
		return append(scopes, ""), nil
	}
	var target *string
	if err := tx.QueryRow(ctx, `SELECT project_id::text FROM nodes WHERE id=$1::uuid AND deleted_at IS NULL`, *newParent).Scan(&target); err != nil {
		return nil, err
	}
	return append(scopes, deref(target)), nil
}

// requireMove decides a re-parenting in every project it changes (ADR-003
// P2): moving work out of a project, into another or back by undo needs
// nodes.move in each, so a project binding never carries work into (or out
// of) a project where the caller holds less.
func requireMove(ctx context.Context, tx pgx.Tx, p tenant.Principal, nodeID string, newParent *string) error {
	scopes, err := moveScopes(ctx, tx, nodeID, newParent)
	if err != nil {
		return err
	}
	if authz.RequireInProjects(ctx, tx, p, "nodes.move", scopes...) != nil {
		return errMoveForbidden
	}
	return nil
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// requireMoveTarget is requireMove for the move routes, as an API error.
func requireMoveTarget(ctx context.Context, tx pgx.Tx, p tenant.Principal, nodeID string, parentID *string) error {
	err := requireMove(ctx, tx, p, nodeID, parentID)
	switch {
	case errors.Is(err, errMoveForbidden):
		return &httpError{status: http.StatusForbidden, msg: "permission denied"}
	case errors.Is(err, pgx.ErrNoRows):
		return notFound("parent not found")
	}
	return err
}

// requireCreateTarget decides node creation in the project the new node joins
// (ADR-003 P2): its parent's project, or the workspace for a new project or a
// node outside every project.
func requireCreateTarget(ctx context.Context, tx pgx.Tx, p tenant.Principal, kindSlug string, parentID *string) error {
	scope := authz.Scope{}
	if kindSlug != "project" && parentID != nil {
		var project *string
		if err := tx.QueryRow(ctx, `SELECT project_id::text FROM nodes WHERE id=$1::uuid`, *parentID).Scan(&project); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return notFound("parent not found")
			}
			return err
		}
		if project != nil {
			scope.ProjectID = *project
		}
	}
	if authz.RequireTx(ctx, tx, p, "nodes.write", scope) != nil {
		return &httpError{status: http.StatusForbidden, msg: "permission denied"}
	}
	return nil
}
