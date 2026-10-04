// SPDX-License-Identifier: AGPL-3.0-only

package events

import (
	"encoding/json"
	"reflect"
	"testing"
)

const changeNode = "11111111-1111-4111-8111-111111111111"

func nodeSnapshot(extra string) json.RawMessage {
	base := `"id":"` + changeNode + `","key":"AEON-1","kind_id":"k","title":"Title","body":"Body","state":"new","parent_id":"p","position":"1","created_at":"2026-09-29T10:00:00Z","updated_at":"2026-09-29T10:00:00Z","deleted_at":null,"fields":{"priority":"low","notes":"n"}`
	if extra == "" {
		return json.RawMessage("{" + base + "}")
	}
	// Later keys win when decoded, so extra overrides the base.
	return json.RawMessage("{" + base + "," + extra + "}")
}

func TestSummarizeNodeChanges(t *testing.T) {
	id := changeNode
	rev := func(s string) *string { return &s }
	cases := []struct {
		name  string
		event Event
		want  []NodeChange
	}{
		{"created", Event{Type: "node.created", NodeID: &id, After: nodeSnapshot("")},
			[]NodeChange{{ID: id, Change: "created", Fields: []string{}, Revision: rev("2026-09-29T10:00:00Z")}}},
		{"title, state and fields change in attribute order",
			Event{Type: "node.updated", NodeID: &id, Before: nodeSnapshot(""), After: nodeSnapshot(`"title":"New","state":"done","updated_at":"2026-09-29T10:00:01.5Z","fields":{"priority":"high","notes":"n","assignee":"a"}`)},
			[]NodeChange{{ID: id, Change: "updated", Fields: []string{"title", "state", "fields.assignee", "fields.priority"}, Revision: rev("2026-09-29T10:00:01.5Z")}}},
		{"generated benefit changes refresh the parent",
			Event{Type: "node.benefits_generated", NodeID: &id, Before: nodeSnapshot(`"fields":{"benefit_en":"Old benefit"}`), After: nodeSnapshot(`"fields":{"benefit_en":"Generated benefit"}`)},
			[]NodeChange{{ID: id, Change: "updated", Fields: []string{"fields.benefit_en"}, Revision: rev("2026-09-29T10:00:00Z")}}},
		{"a removed field is named; key order and null do not count",
			Event{Type: "node.updated", NodeID: &id, Before: nodeSnapshot(`"fields":{"priority":"low","notes":"n","gone":null}`), After: nodeSnapshot(`"fields":{"notes":"n"}`)},
			[]NodeChange{{ID: id, Change: "updated", Fields: []string{"fields.priority"}, Revision: rev("2026-09-29T10:00:00Z")}}},
		{"nested field values compare by meaning",
			Event{Type: "node.updated", NodeID: &id, Before: nodeSnapshot(`"fields":{"tags":[{"a":1,"b":2}]}`), After: nodeSnapshot(`"fields":{"tags":[{"b":2,"a":1}]}`)},
			nil},
		{"operator annotations and timestamps alone are no change",
			Event{Type: "node.updated", NodeID: &id, Before: nodeSnapshot(""), After: nodeSnapshot(`"updated_at":"2026-09-29T11:00:00Z","production":true,"brief":"2","warnings":["w"]`)},
			nil},
		{"kind conversion names the kind and archived fields",
			Event{Type: "node.kind_changed", NodeID: &id, Before: nodeSnapshot(""), After: nodeSnapshot(`"kind_id":"epic","updated_at":"2026-09-29T10:00:02Z","fields":{"priority":"low"}`)},
			[]NodeChange{{ID: id, Change: "updated", Fields: []string{"kind_id", "fields.notes"}, Revision: rev("2026-09-29T10:00:02Z")}}},
		{"moved", Event{Type: "node.moved", NodeID: &id, Before: nodeSnapshot(""), After: nodeSnapshot(`"parent_id":"q","position":"2","journey_tickets":[{"x":1}]`)},
			[]NodeChange{{ID: id, Change: "updated", Fields: []string{"parent_id", "position"}, Revision: rev("2026-09-29T10:00:00Z")}}},
		{"deleted", Event{Type: "node.deleted", NodeID: &id, Before: nodeSnapshot(""), After: nodeSnapshot(`"deleted_at":"2026-09-29T12:00:00Z","updated_at":"2026-09-29T12:00:00Z"`)},
			[]NodeChange{{ID: id, Change: "deleted", Fields: []string{"deleted_at"}, Revision: rev("2026-09-29T12:00:00Z")}}},
		{"restored reads as created", Event{Type: "node.updated", NodeID: &id, Before: nodeSnapshot(`"deleted_at":"2026-09-29T12:00:00Z"`), After: nodeSnapshot(`"updated_at":"2026-09-29T13:00:00Z"`)},
			[]NodeChange{{ID: id, Change: "created", Fields: []string{}, Revision: rev("2026-09-29T13:00:00Z")}}},
		{"project move names the project",
			Event{Type: "node.project_moved", NodeID: &id, Before: json.RawMessage(`{"node":` + string(nodeSnapshot("")) + `,"journey":null}`), After: json.RawMessage(`{"node":` + string(nodeSnapshot(`"key":"OTHER-4","parent_id":"r"`)) + `}`)},
			[]NodeChange{{ID: id, Change: "updated", Fields: []string{"key", "parent_id", "project_id"}, Revision: rev("2026-09-29T10:00:00Z")}}},
		{"a batch pairs its items by id",
			Event{Type: "node.bulk_changed", Before: json.RawMessage(`{"items":[{"id":"b","state":"new","fields":{},"parent_id":null,"position":"1","updated_at":"2026-09-29T10:00:00Z"},{"id":"` + id + `","state":"new","fields":{"priority":"low"},"parent_id":null,"position":"1","updated_at":"2026-09-29T10:00:00Z"}]}`),
				After: json.RawMessage(`{"items":[{"id":"` + id + `","state":"done","fields":{"priority":"low"},"parent_id":null,"position":"1","updated_at":"2026-09-29T10:00:02Z"},{"id":"b","state":"new","fields":{},"parent_id":null,"position":"1","updated_at":"2026-09-29T10:00:00Z"}]}`)},
			[]NodeChange{{ID: id, Change: "updated", Fields: []string{"state"}, Revision: rev("2026-09-29T10:00:02Z")}}},
		{"another resource type describes no node", Event{Type: "comment.created", NodeID: &id, After: nodeSnapshot("")}, nil},
		{"a snapshot about another node is ignored", Event{Type: "node.updated", NodeID: new(string), Before: nodeSnapshot(""), After: nodeSnapshot(`"title":"x"`)}, nil},
		{"garbage snapshots describe nothing", Event{Type: "node.updated", NodeID: &id, Before: json.RawMessage(`[1]`), After: json.RawMessage(`"x"`)}, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := summarizeNodeChanges(tc.event)
			if !reflect.DeepEqual(got, tc.want) {
				gb, _ := json.Marshal(got)
				wb, _ := json.Marshal(tc.want)
				t.Fatalf("got %s\nwant %s", gb, wb)
			}
		})
	}
}

func TestNodeChangesAreOmittedFromOtherEvents(t *testing.T) {
	b, err := json.Marshal(Event{ID: 1, Type: "test.changed"})
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(b, &fields); err != nil {
		t.Fatal(err)
	}
	if _, present := fields["node_changes"]; present {
		t.Fatalf("node_changes on a non-node event: %s", b)
	}
}
