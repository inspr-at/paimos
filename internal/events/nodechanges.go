// SPDX-License-Identifier: AGPL-3.0-only

package events

import (
	"bytes"
	"context"
	"encoding/json"
	"reflect"
	"slices"
	"sort"

	"github.com/jackc/pgx/v5"
)

// NodeChange summarizes one node change for live views (AEON-326): which
// node, its project, which attributes changed and the node's revision after
// the change (its updated_at, the value If-Unmodified-Since takes). Clients
// refetch the node through the normal API to read values. The summary is
// derived from an event the reader already passed row-level security for,
// and the project from the nodes row under the same security, so it names
// nothing the reader could not read.
type NodeChange struct {
	ID        string   `json:"id"`
	ProjectID *string  `json:"project_id"`
	Change    string   `json:"change"` // created, updated or deleted
	Fields    []string `json:"fields"`
	Revision  *string  `json:"revision"`
}

// Event types whose snapshots are nodes (node.project_moved wraps it in
// "node", node.bulk_changed lists them in "items").
var nodeChangeTypes = map[string]bool{
	"status_autopilot.changed": true, "status_autopilot.undone": true,
	"node.created": true, "node.updated": true, "node.moved": true, "node.kind_changed": true,
	"node.project_moved": true, "node.deleted": true, "node.bulk_changed": true,
	"knowledge.created": true, "knowledge.updated": true, "knowledge.deleted": true,
	"knowledge.learning_accepted": true, "knowledge.learning_dismissed": true, "knowledge.learning_drafted": true,
}

// The node attributes a change names, in this order; custom fields follow
// as fields.<name>. Timestamps and event annotations are not attributes.
var nodeAttributes = []string{"key", "kind_id", "title", "body", "state", "human_check", "parent_id", "position", "deleted_at", "status_autopilot"}

type snapshotObject map[string]json.RawMessage

func objectOf(raw json.RawMessage) snapshotObject {
	if isNull(raw) {
		return nil
	}
	var out snapshotObject
	if json.Unmarshal(raw, &out) != nil {
		return nil
	}
	return out
}

func isNull(raw json.RawMessage) bool {
	trimmed := bytes.TrimSpace(raw)
	return len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null"))
}

// sameJSON compares two JSON values by meaning; absent equals null.
func sameJSON(a, b json.RawMessage) bool {
	if isNull(a) || isNull(b) {
		return isNull(a) == isNull(b)
	}
	if bytes.Equal(a, b) {
		return true
	}
	var x, y any
	if json.Unmarshal(a, &x) != nil || json.Unmarshal(b, &y) != nil {
		return false
	}
	return reflect.DeepEqual(x, y)
}

func stringOf(raw json.RawMessage) *string {
	var s string
	if isNull(raw) || json.Unmarshal(raw, &s) != nil || s == "" {
		return nil
	}
	return &s
}

// nodeChangeOf compares two node snapshots. ok is false when there is nothing
// to report: no node, or an update that changed no attribute.
func nodeChangeOf(before, after snapshotObject) (NodeChange, bool) {
	var c NodeChange
	source := after
	if source == nil {
		source = before
	}
	if source == nil {
		return c, false
	}
	id := stringOf(source["id"])
	if id == nil {
		return c, false
	}
	c.ID = *id
	c.Revision = stringOf(source["updated_at"])
	c.Fields = []string{}
	wasDeleted := before != nil && !isNull(before["deleted_at"])
	isDeleted := after == nil || !isNull(after["deleted_at"])
	switch {
	case before == nil || (wasDeleted && !isDeleted):
		// A restored node reappears to views exactly like a new one.
		c.Change = "created"
		return c, !isDeleted
	case isDeleted:
		c.Change = "deleted"
		if !wasDeleted {
			c.Fields = append(c.Fields, "deleted_at")
		}
		return c, !wasDeleted
	}
	c.Change = "updated"
	for _, key := range nodeAttributes {
		if !sameJSON(before[key], after[key]) {
			c.Fields = append(c.Fields, key)
		}
	}
	if !sameJSON(before["fields"], after["fields"]) {
		was, now := objectOf(before["fields"]), objectOf(after["fields"])
		names := make([]string, 0)
		for name, value := range now {
			if !sameJSON(was[name], value) {
				names = append(names, "fields."+name)
			}
		}
		for name, value := range was {
			if _, kept := now[name]; !kept && !isNull(value) {
				names = append(names, "fields."+name)
			}
		}
		sort.Strings(names)
		c.Fields = append(c.Fields, names...)
	}
	return c, len(c.Fields) > 0
}

// summarizeNodeChanges derives the node changes one event describes, without
// projects. Unknown types and shapes describe none.
func summarizeNodeChanges(e Event) []NodeChange {
	if !nodeChangeTypes[e.Type] {
		return nil
	}
	var out []NodeChange
	add := func(before, after snapshotObject, extra ...string) {
		c, ok := nodeChangeOf(before, after)
		if !ok {
			return
		}
		if e.NodeID != nil && c.ID != *e.NodeID {
			return
		}
		if c.Change == "updated" {
			for _, field := range extra {
				if !slices.Contains(c.Fields, field) {
					c.Fields = append(c.Fields, field)
				}
			}
		}
		out = append(out, c)
	}
	switch e.Type {
	case "knowledge.learning_dismissed", "knowledge.learning_drafted":
		// These audit snapshots (including Undo's reopened decision) do not
		// mutate the source node. Resolve its project under the reader's RLS
		// below rather than trusting the project named in an audit snapshot.
		if e.NodeID != nil {
			out = append(out, NodeChange{ID: *e.NodeID, Change: "updated", Fields: []string{"learning"}})
		}
	case "node.project_moved":
		add(objectOf(objectOf(e.Before)["node"]), objectOf(objectOf(e.After)["node"]), "project_id")
	case "node.bulk_changed":
		// A batch has no node of its own: pair its items by id.
		items := func(raw json.RawMessage) ([]snapshotObject, map[string]snapshotObject) {
			var batch struct {
				Items []snapshotObject `json:"items"`
			}
			if isNull(raw) || json.Unmarshal(raw, &batch) != nil {
				return nil, nil
			}
			byID := make(map[string]snapshotObject, len(batch.Items))
			for _, item := range batch.Items {
				if id := stringOf(item["id"]); id != nil {
					byID[*id] = item
				}
			}
			return batch.Items, byID
		}
		_, was := items(e.Before)
		now, _ := items(e.After)
		for _, item := range now {
			if id := stringOf(item["id"]); id != nil {
				add(was[*id], item)
			}
		}
	default:
		add(objectOf(e.Before), objectOf(e.After))
	}
	return out
}

// attachNodeChanges sets NodeChanges on the node events of a page and reads
// each node's project in the reader's transaction. A node the reader cannot
// see has no project here (the event would not be visible either).
func attachNodeChanges(ctx context.Context, tx pgx.Tx, tenantID string, items []Event) error {
	ids := make([]string, 0)
	seen := make(map[string]bool)
	for i := range items {
		changes := summarizeNodeChanges(items[i])
		if len(changes) == 0 {
			continue
		}
		items[i].NodeChanges = changes
		for _, c := range changes {
			if !seen[c.ID] {
				seen[c.ID] = true
				ids = append(ids, c.ID)
			}
		}
	}
	if len(ids) == 0 {
		return nil
	}
	rows, err := tx.Query(ctx, `SELECT id::text, project_id::text FROM nodes WHERE tenant_id=$1 AND id = ANY($2::uuid[])`, tenantID, ids)
	if err != nil {
		return err
	}
	projects := make(map[string]*string, len(ids))
	for rows.Next() {
		var id string
		var project *string
		if err := rows.Scan(&id, &project); err != nil {
			rows.Close()
			return err
		}
		projects[id] = project
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for i := range items {
		for j := range items[i].NodeChanges {
			items[i].NodeChanges[j].ProjectID = projects[items[i].NodeChanges[j].ID]
		}
	}
	return nil
}
