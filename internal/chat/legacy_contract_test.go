// SPDX-License-Identifier: AGPL-3.0-only

package chat

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"regexp"
	"testing"
	"time"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/inspr-at/paimos/internal/inbox"

	"gopkg.in/yaml.v3"
)

// Reportercontract pins cover /me, approvals, journey, stage and harness
// responses. These pinned message/receipt snapshots close the remaining gap.
func TestStrictLegacyResponseSnapshotsUnchanged(t *testing.T) {
	raw, err := os.ReadFile("../../api/openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]any
	if err = yaml.Unmarshal(raw, &document); err != nil {
		t.Fatal(err)
	}
	snapshot, err := os.ReadFile("testdata/legacy-response-snapshots.json")
	if err != nil {
		t.Fatal(err)
	}
	var pinned struct {
		Baseline string            `json:"baseline"`
		SHA256   map[string]string `json:"sha256"`
	}
	if err = json.Unmarshal(snapshot, &pinned); err != nil {
		t.Fatal(err)
	}
	if len(pinned.SHA256) != 3 || pinned.Baseline != "1a8942d2" {
		t.Fatal("incomplete baseline snapshot")
	}
	blocks := regexp.MustCompile(`(?m)^    ([A-Za-z][A-Za-z0-9_]*):`)
	indexes := blocks.FindAllSubmatchIndex(raw, -1)
	found := map[string]bool{}
	for i, index := range indexes {
		name := string(raw[index[2]:index[3]])
		expected, ok := pinned.SHA256[name]
		if !ok {
			continue
		}
		end := len(raw)
		if i+1 < len(indexes) {
			end = indexes[i+1][0]
		}
		sum := sha256.Sum256(raw[index[0]:end])
		actual := hex.EncodeToString(sum[:])
		if actual != expected {
			t.Errorf("legacy %s response differs from %s snapshot", name, pinned.Baseline)
		}
		found[name] = true
	}
	if len(found) != 3 {
		t.Fatal("legacy snapshot schema missing")
	}
}

// Validate the real wire types against both response branches. Attached sends
// return metadata with an empty body, unlike the durable InboxSend shape.
func TestAttachedResponsesUseSeparateSchemas(t *testing.T) {
	raw, err := os.ReadFile("../../api/openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Paths      map[string]any `yaml:"paths"`
		Components struct {
			Schemas map[string]any `yaml:"schemas"`
		} `yaml:"components"`
	}
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	defs := map[string]any{}
	for _, name := range []string{"InboxSend", "InboxMessage", "InboxReceipt", "InboxSendResponse", "InboxReceiptResponse", "AttachedInboxMessage", "AttachedInboxReceipt", "AttachedMessageStatus"} {
		shape, ok := doc.Components.Schemas[name]
		if !ok {
			t.Fatalf("missing separate response schema %s", name)
		}
		defs[name] = shape
	}
	resolve := func(shape map[string]any) *jsonschema.Resolved {
		t.Helper()
		root := map[string]any{"$defs": defs}
		for key, value := range shape {
			root[key] = value
		}
		raw, err := json.Marshal(root)
		if err != nil {
			t.Fatal(err)
		}
		raw = bytes.ReplaceAll(raw, []byte("#/components/schemas/"), []byte("#/$defs/"))
		var schema jsonschema.Schema
		if err := json.Unmarshal(raw, &schema); err != nil {
			t.Fatal(err)
		}
		resolved, err := schema.Resolve(nil)
		if err != nil {
			t.Fatal(err)
		}
		return resolved
	}
	response := func(path, method, status, expected string) *jsonschema.Resolved {
		t.Helper()
		var cursor any = doc.Paths[path]
		for _, key := range []string{method, "responses", status, "content", "application/json", "schema"} {
			object, ok := cursor.(map[string]any)
			if !ok {
				t.Fatalf("%s %s missing response object at %s", method, path, key)
			}
			cursor = object[key]
		}
		shape, ok := cursor.(map[string]any)
		if !ok {
			t.Fatalf("%s %s missing response schema", method, path)
		}
		if shape["$ref"] != "#/components/schemas/"+expected {
			t.Fatalf("%s %s does not select %s", method, path, expected)
		}
		return resolve(shape)
	}
	wire := func(v any) map[string]any {
		t.Helper()
		raw, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		var value map[string]any
		if err := json.Unmarshal(raw, &value); err != nil {
			t.Fatal(err)
		}
		return value
	}
	send := response("/inbox/messages", "post", "201", "InboxSendResponse")
	receipt := response("/inbox/messages/{messageId}/receipt", "get", "200", "InboxReceiptResponse")
	attachedSend := resolve(map[string]any{"$ref": "#/components/schemas/AttachedInboxMessage"})
	legacyReceipt := resolve(map[string]any{"$ref": "#/components/schemas/InboxReceipt"})
	id := "11111111-1111-4111-8111-111111111111"
	at := time.Date(2026, 10, 6, 0, 0, 0, 0, time.UTC)
	msg := inbox.Message{ID: id, SenderPrincipalID: id, RecipientPrincipalID: id, Body: "durable fixture", IdempotencyKey: "fixture", SentEventID: 1, CreatedAt: at}
	rec := inbox.Receipt{MessageID: id, IdempotencyKey: "fixture", Tenant: "fixture", SenderPrincipalID: id, RecipientPrincipalID: id, State: "queued"}
	for name, tc := range map[string]struct {
		schema *jsonschema.Resolved
		value  any
	}{"durable send": {send, msg}, "durable receipt": {receipt, rec}} {
		if err := tc.schema.Validate(wire(tc.value)); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
	for _, mode := range []string{"attached_volatile", "attached_notification"} {
		t.Run(mode, func(t *testing.T) {
			msg.ContentMode, msg.Body, msg.RecipientSessionID, msg.MessageDeadline = mode, "", &id, &at
			if mode == "attached_volatile" {
				msg.MessageGrantID, msg.RecipientMessageGeneration = &id, &id
			} else {
				msg.MessageGrantID, msg.RecipientMessageGeneration = nil, nil
			}
			value := wire(msg)
			if err := send.Validate(value); err != nil {
				t.Fatalf("attached send response: %v", err)
			}
			if err := attachedSend.Validate(value); err != nil {
				t.Fatalf("separate attached send: %v", err)
			}
			value["body"] = "must never disclose text"
			if err := attachedSend.Validate(value); err == nil {
				t.Fatal("attached response schema permits text disclosure")
			}
			if err := send.Validate(value); err == nil {
				t.Fatal("send response permits attached text through its legacy branch")
			}
			rec.Attached = &inbox.AttachedStatus{Protocol: "attached_messages_v1", Outcome: "queued", ContentMode: mode}
			value = wire(rec)
			if err := receipt.Validate(value); err != nil {
				t.Fatalf("attached receipt response: %v", err)
			}
			if err := legacyReceipt.Validate(value); err == nil {
				t.Fatal("pinned legacy receipt accepts attached fields")
			}
			value["unexpected"] = true
			if err := receipt.Validate(value); err == nil {
				t.Fatal("receipt response lost its closed shape")
			}
		})
	}
}
