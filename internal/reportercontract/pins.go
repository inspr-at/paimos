// SPDX-License-Identifier: AGPL-3.0-only

package reportercontract

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// Pin is the version and structural response schema for one reporter surface.
// Shape is retained alongside its hash so a changed pin can distinguish an
// additive response property from a breaking response change. Its outer keys
// identify response operations as "METHOD /path status".
type Pin struct {
	Version string          `json:"version"`
	SHA256  string          `json:"sha256"`
	Shape   json.RawMessage `json:"shape"`
}

type operation struct{ path, method, status string }
type surface struct {
	version, header string
	ops             []operation
}

var surfaces = map[string]surface{
	"stage-handoffs": {StageHandoffs, "StageHandoffsContract", []operation{{"/stage-handoffs", "post", "201"}, {"/stage-handoffs/{handoffId}", "get", "200"}}},
	"stage-evidence": {StageEvidence, "StageEvidenceContract", []operation{{"/stage-handoffs/{handoffId}/evidence", "post", "201"}}},
	"stage-result":   {StageResult, "StageResultContract", []operation{{"/stage-handoffs/{handoffId}/result", "post", "200"}}},
	"stage-launch":   {StageLaunch, "StageLaunchContract", []operation{{"/stage-handoffs/{handoffId}/launch/admit", "post", "200"}, {"/stage-handoffs/{handoffId}/launch/consume", "post", "200"}}},
	"journey":        {Journey, "JourneyContract", []operation{{"/projects/{projectId}/journey", "get", "200"}}},
	"me":             {Me, "MeContract", []operation{{"/me", "get", "200"}}},
	"baseline-batches": {BaselineBatches, "BaselineBatchesContract", []operation{
		{"/stage-handoffs/{handoffId}/classic-batch-alias", "post", "201"},
		{"/projects/{projectId}/baseline-batches/batches/{batchId}/built-receipt", "post", "200"},
	}},
	"approvals": {Approvals, "ApprovalsContract", []operation{{"/approvals", "get", "200"}, {"/approvals", "post", "201"}, {"/approvals/{approvalId}/decision", "post", "200"}, {"/approvals/{approvalId}/revoke", "post", "200"}}},
	"harness-session": {HarnessSession, "HarnessSessionContract", []operation{
		{"/projects/{projectId}/harness-sessions/{sessionId}", "get", "200"},
		{"/projects/{projectId}/harness-sessions/{sessionId}/heartbeat", "post", "200"},
	}},
}

// Current derives each pin from the OpenAPI response schema, recursively
// including referenced schemas. Documentation prose is excluded from the hash.
func Current(openAPI []byte) (map[string]Pin, error) {
	var doc map[string]any
	if err := yaml.Unmarshal(openAPI, &doc); err != nil {
		return nil, fmt.Errorf("parse OpenAPI: %w", err)
	}
	components, err := child(doc, "components")
	if err != nil {
		return nil, err
	}
	headers, err := child(components, "headers")
	if err != nil {
		return nil, err
	}
	schemas, err := child(components, "schemas")
	if err != nil {
		return nil, err
	}
	paths, err := child(doc, "paths")
	if err != nil {
		return nil, err
	}
	out := make(map[string]Pin, len(surfaces))
	for name, s := range surfaces {
		h, err := child(headers, s.header, "schema")
		if err != nil {
			return nil, err
		}
		values, ok := h["enum"].([]any)
		if !ok || len(values) != 1 || values[0] != s.version {
			return nil, fmt.Errorf("%s: OpenAPI header version must be %s", name, s.version)
		}
		shape := make(map[string]any, len(s.ops))
		for _, op := range s.ops {
			response, err := child(paths, op.path, op.method, "responses", op.status)
			if err != nil {
				return nil, fmt.Errorf("%s: %w", name, err)
			}
			header, err := child(response, "headers", Header)
			if err != nil {
				return nil, fmt.Errorf("%s: %w", name, err)
			}
			if header["$ref"] != "#/components/headers/"+s.header {
				return nil, fmt.Errorf("%s %s: missing %s header reference", op.method, op.path, s.header)
			}
			body, err := child(response, "content", "application/json", "schema")
			if err != nil {
				return nil, err
			}
			resolved, err := expand(body, schemas, map[string]bool{})
			if err != nil {
				return nil, err
			}
			shape[strings.ToUpper(op.method)+" "+op.path+" "+op.status] = resolved
		}
		encoded, err := json.Marshal(shape)
		if err != nil {
			return nil, err
		}
		sum := sha256.Sum256(encoded)
		out[name] = Pin{Version: s.version, SHA256: hex.EncodeToString(sum[:]), Shape: encoded}
	}
	return out, nil
}

func child(root map[string]any, keys ...string) (map[string]any, error) {
	var current any = root
	for _, key := range keys {
		m, ok := current.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("OpenAPI path %s is not an object", key)
		}
		current, ok = m[key]
		if !ok {
			return nil, fmt.Errorf("OpenAPI path missing %s", key)
		}
	}
	m, ok := current.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("OpenAPI path %s is not an object", strings.Join(keys, "/"))
	}
	return m, nil
}

func expand(v any, schemas map[string]any, seen map[string]bool) (any, error) {
	switch value := v.(type) {
	case map[string]any:
		if ref, ok := value["$ref"].(string); ok {
			name := strings.TrimPrefix(ref, "#/components/schemas/")
			if name == ref || seen[name] {
				return nil, fmt.Errorf("invalid or recursive response schema %s", ref)
			}
			target, ok := schemas[name]
			if !ok {
				return nil, fmt.Errorf("missing response schema %s", ref)
			}
			seen[name] = true
			result, err := expand(target, schemas, seen)
			delete(seen, name)
			return result, err
		}
		out := make(map[string]any)
		for key, item := range value {
			if key == "description" || key == "summary" || key == "example" || key == "examples" || key == "title" {
				continue
			}
			resolved, err := expand(item, schemas, seen)
			if err != nil {
				return nil, err
			}
			out[key] = resolved
		}
		return out, nil
	case []any:
		out := make([]any, len(value))
		for i, item := range value {
			resolved, err := expand(item, schemas, seen)
			if err != nil {
				return nil, err
			}
			out[i] = resolved
		}
		return out, nil
	default:
		return value, nil
	}
}

// RequiredBump classifies differences conservatively. New optional properties
// and operations are additive. A new required property is also additive in a
// response: the server guarantees it, and existing readers tolerate extra fields.
// This exception applies only to response operation keys emitted by Current;
// request and unlabelled schemas still require a major bump. Existing properties
// and constraints must remain unchanged. It returns "none" when shapes match.
func RequiredBump(previous, current Pin) (string, error) {
	if previous.SHA256 == current.SHA256 {
		return "none", nil
	}
	var oldShape, newShape any
	if err := json.Unmarshal(previous.Shape, &oldShape); err != nil {
		return "", err
	}
	if err := json.Unmarshal(current.Shape, &newShape); err != nil {
		return "", err
	}
	if additive(oldShape, newShape, "", false) {
		return "minor", nil
	}
	return "major", nil
}

func additive(old, next any, parent string, response bool) bool {
	oldMap, oldOK := old.(map[string]any)
	newMap, newOK := next.(map[string]any)
	if oldOK || newOK {
		if !oldOK || !newOK {
			return false
		}
		for key, value := range oldMap {
			other, ok := newMap[key]
			if !ok {
				return false
			}
			if response && parent != "properties" && key == "required" {
				if !responseRequiredAddition(oldMap, newMap) {
					return false
				}
				continue
			}
			isResponse := response || parent == "" && responseOperation(key)
			if !additive(value, other, key, isResponse) {
				return false
			}
		}
		if oldProps, ok := oldMap["properties"].(map[string]any); ok {
			if newProps, ok := newMap["properties"].(map[string]any); ok {
				for field := range newProps {
					if _, exists := oldProps[field]; !exists && isRequired(newMap["required"], field) && !response {
						return false
					}
				}
			}
		}
		for key := range newMap {
			if _, ok := oldMap[key]; ok {
				continue
			}
			if response && parent != "properties" && key == "required" && responseRequiredAddition(oldMap, newMap) {
				continue
			}
			if parent != "properties" && parent != "" {
				return false
			}
		}
		return true
	}
	oldArray, oldOK := old.([]any)
	newArray, newOK := next.([]any)
	if oldOK || newOK {
		if !oldOK || !newOK || len(oldArray) != len(newArray) {
			return false
		}
		for i := range oldArray {
			if !additive(oldArray[i], newArray[i], strconv.Itoa(i), response) {
				return false
			}
		}
		return true
	}
	return old == next
}

// Only the outer response labels generated by Current establish direction.
// A POST response is still a response; an unlabelled or request schema is not.
func responseOperation(key string) bool {
	parts := strings.Fields(key)
	if len(parts) != 3 || !strings.HasPrefix(parts[1], "/") || len(parts[2]) != 3 {
		return false
	}
	switch parts[0] {
	case "GET", "POST", "PUT", "PATCH", "DELETE", "HEAD", "OPTIONS", "TRACE":
	default:
		return false
	}
	status, err := strconv.Atoi(parts[2])
	return err == nil && status >= 100 && status <= 599
}

// The required set may grow only by properties newly guaranteed by the server.
// Tightening an existing optional property, removing a guarantee, or adding to
// an object whose previous schema rejected extra properties remains breaking.
func responseRequiredAddition(old, next map[string]any) bool {
	oldRequired, _ := old["required"].([]any)
	newRequired, ok := next["required"].([]any)
	if !ok {
		return false
	}
	oldProps, _ := old["properties"].(map[string]any)
	newProps, _ := next["properties"].(map[string]any)
	for _, item := range oldRequired {
		field, ok := item.(string)
		if !ok || !isRequired(newRequired, field) {
			return false
		}
	}
	for _, item := range newRequired {
		field, ok := item.(string)
		if !ok {
			return false
		}
		if isRequired(oldRequired, field) {
			continue
		}
		_, existed := oldProps[field]
		_, exists := newProps[field]
		if existed || !exists || old["additionalProperties"] == false || old["unevaluatedProperties"] == false {
			return false
		}
	}
	return true
}

func isRequired(value any, field string) bool {
	items, ok := value.([]any)
	if !ok {
		return false
	}
	for _, item := range items {
		if item == field {
			return true
		}
	}
	return false
}
