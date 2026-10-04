// SPDX-License-Identifier: AGPL-3.0-only

// Package journal owns the Aithema journal, generation store and budget ledger.
// Trusted lifecycle methods are in-process only; delegated HTTP callers can
// neither create authority nor advance generations, epochs or tombstones.
package journal

import (
	"bytes"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"encoding/json"
	"math/big"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/go-json-experiment/json/jsontext"
	"github.com/inspr-at/paimos/internal/aithema/tokens"
)

//go:embed contracts/*.json
var contractFiles embed.FS

type binding struct{ file, pointer string }
type Validator struct {
	schemas  map[string]binding
	files    map[string]map[string]any
	patterns map[string]*regexp.Regexp
}

func NewValidator() (*Validator, error) {
	v := &Validator{schemas: map[string]binding{
		"aithema.journal.record": {"journal-record.schema.json", "/$defs/record"},
		"aithema.spec.snapshot":  {"spec-snapshot.schema.json", "/$defs/snapshot"},
		"aithema.budget.message": {"budget.schema.json", "/$defs/message"},
		"aithema.authz":          {"authz-record.schema.json", "/$defs/record"},
	}, files: map[string]map[string]any{}, patterns: map[string]*regexp.Regexp{}}
	entries, err := contractFiles.ReadDir("contracts")
	if err != nil {
		return nil, err
	}
	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ".schema.json") {
			continue
		}
		raw, err := contractFiles.ReadFile("contracts/" + entry.Name())
		if err != nil {
			return nil, err
		}
		doc, err := decode(raw)
		if err != nil {
			return nil, err
		}
		v.files[entry.Name()] = doc
	}
	for _, schema := range v.files {
		if err := v.prepare(schema); err != nil {
			return nil, err
		}
	}
	return v, nil
}

func decode(raw []byte) (map[string]any, error) {
	if len(raw) > 1<<20 {
		return nil, fault(413, "too_large")
	}
	return decodeJSON(raw)
}

// Only logical snapshots and content may exceed the ordinary event bound.
// HTTP still admits at most 1 MiB; larger submissions arrive through staging.
func decodeDocument(raw []byte) (map[string]any, error) {
	obj, err := decodeJSON(raw)
	if err != nil {
		return nil, err
	}
	if len(raw) > 1<<20 && obj["contract"] != "aithema.spec.snapshot" && !(obj["contract"] == "aithema.journal.record" && obj["kind"] == "pending_op.content") {
		return nil, fault(413, "too_large")
	}
	return obj, nil
}

func decodeJSON(raw []byte) (map[string]any, error) {
	if !jsontext.Value(raw).IsValid() {
		return nil, fault(400, "invalid_request")
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	var obj map[string]any
	if d.Decode(&obj) != nil || obj == nil {
		return nil, fault(400, "invalid_request")
	}
	if !boundedJSON(obj, 0) {
		return nil, fault(400, "invalid_request")
	}
	return obj, nil
}
func boundedJSON(value any, depth int) bool {
	if depth > 64 {
		return false
	}
	switch value := value.(type) {
	case map[string]any:
		for k, v := range value {
			if k == "__proto__" || k == "constructor" || k == "prototype" || !boundedJSON(v, depth+1) {
				return false
			}
		}
	case []any:
		for _, v := range value {
			if !boundedJSON(v, depth+1) {
				return false
			}
		}
	case json.Number:
		if len(value) > 64 {
			return false
		}
		if _, err := strconv.ParseFloat(string(value), 64); err != nil {
			return false
		}
		if i := strings.IndexAny(string(value), "eE"); i >= 0 {
			exponent, err := strconv.Atoi(string(value)[i+1:])
			if err != nil || exponent < -400 || exponent > 400 {
				return false
			}
		}
	}
	return true
}
func integer(v any) (int64, bool) {
	n, ok := v.(json.Number)
	if !ok {
		return 0, false
	}
	r, ok := new(big.Rat).SetString(string(n))
	if !ok || !r.IsInt() || !r.Num().IsInt64() {
		return 0, false
	}
	i := r.Num().Int64()
	return i, i >= 0 && i <= tokens.MaxSafeInteger
}
func number(v any) int64          { n, _ := integer(v); return n }
func object(v any) map[string]any { o, _ := v.(map[string]any); return o }
func array(v any) []any           { a, _ := v.([]any); return a }
func text(v any) string           { s, _ := v.(string); return s }
func marshal(v any) []byte        { b, _ := json.Marshal(v); return b }
func digest(raw []byte) string    { d := sha256.Sum256(raw); return hex.EncodeToString(d[:]) }
func canonicalDigest(value any, expected any) bool {
	c, err := tokens.CanonicalJSON(marshal(value))
	return err == nil && digest(c) == text(expected)
}
func in(s string, choices ...string) bool {
	for _, c := range choices {
		if s == c {
			return true
		}
	}
	return false
}
func envelope(contract string, fields map[string]any) map[string]any {
	doc := map[string]any{"contract": contract, "major": 1, "minor": 0, "min_reader": 0}
	for k, v := range fields {
		doc[k] = v
	}
	return doc
}
func budget(kind string, body map[string]any) []byte {
	return marshal(envelope("aithema.budget.message", map[string]any{"type": kind, "body": body}))
}

func (v *Validator) Validate(raw []byte, contract string) (map[string]any, error) {
	obj, err := decodeDocument(raw)
	if err != nil {
		return nil, err
	}
	if text(obj["contract"]) != contract {
		return nil, fault(400, "invalid_request")
	}
	major, majOK := integer(obj["major"])
	minor, minOK := integer(obj["minor"])
	reader, readOK := integer(obj["min_reader"])
	if !majOK || !minOK || !readOK {
		return nil, fault(400, "invalid_request")
	}
	supported := int64(1)
	if in(contract, "aithema.journal.record", "aithema.spec.snapshot") {
		supported = 2
	}
	if contract == "aithema.authz" {
		supported = 0
	}
	if major != 1 || reader > supported {
		return nil, fault(422, "contract_too_new")
	}
	if reader > minor {
		return nil, fault(400, "invalid_request")
	}
	schema, exists := v.schemas[contract]
	if !exists || !v.check(v.resolve(schema.file, schema.pointer), obj, schema.file, 0) {
		return nil, fault(400, "invalid_request")
	}
	for _, field := range []string{"recorded_at", "created_at", "withdrawn_at"} {
		if s, ok := obj[field].(string); ok {
			if _, err := time.Parse(time.RFC3339Nano, s); err != nil {
				return nil, fault(400, "invalid_request")
			}
		}
	}
	valid := true
	switch contract {
	case "aithema.journal.record":
		valid = recordValid(obj)
		if valid && obj["kind"] == "pending_op.content" {
			valid = v.contentValid(obj)
		}
	case "aithema.spec.snapshot":
		valid = snapshotValid(obj)
		for _, value := range array(obj["pending_ops"]) {
			op := object(value)
			if op["payload_kind"] == "pending_op.content" {
				_, err := v.pendingReference(text(op["payload"]))
				valid = valid && err == nil && number(obj["minor"]) >= 2 && number(obj["min_reader"]) >= 2
			}
		}
	case "aithema.budget.message":
		valid = budgetValid(obj)
	}
	if !valid {
		return nil, fault(400, "invalid_request")
	}
	return obj, nil
}
func recordValid(doc map[string]any) bool {
	writer, data := object(doc["writer"]), object(doc["data"])
	kind, w := text(doc["kind"]), text(writer["kind"])
	if w == "worker" && number(writer["generation"]) < 1 {
		return false
	}
	allowed := w == "worker"
	switch kind {
	case "authz.epoch", "session.control":
		allowed = w == "host"
	case "session.end":
		allowed = in(w, "worker", "host")
	case "turn", "reaction":
		allowed = in(w, "browser", "worker")
	}
	if !allowed {
		return false
	}
	switch kind {
	case "turn":
		if (data["speaker"] == "assistant") != (data["trust"] == "assistant") || w == "browser" && data["speaker"] != "person" {
			return false
		}
	case "reaction":
		if !strings.HasPrefix(text(data["text"]), text(data["delivered_prefix"])) || data["complete"] == true && data["text"] != data["delivered_prefix"] {
			return false
		}
	case "source":
		seen := map[string]bool{}
		length := int64(utf8.RuneCountInString(text(data["text"])))
		for _, value := range array(data["segments"]) {
			segment := object(value)
			id := text(segment["id"])
			if seen[id] || number(segment["start"]) > number(segment["end"]) || number(segment["end"]) > length {
				return false
			}
			seen[id] = true
		}
	case "design.input":
		ir, irErr := tokens.CanonicalJSON(marshal(data["screen_ir"]))
		tokensJSON, tokensErr := tokens.CanonicalJSON(marshal(data["tokens"]))
		return irErr == nil && tokensErr == nil && len(ir) <= 512<<10 && len(tokensJSON) <= 64<<10 && digest(ir) == text(data["screen_ir_sha256"]) && digest(tokensJSON) == text(data["tokens_sha256"])
	case "session.end":
		if data["host_mode"] == "working_spec_only" && data["export"] != "exported" {
			return false
		}
	case "budget.hold":
		parts := strings.Split(text(data["attempt_id"]), ":")
		if len(parts) != 4 || parts[0] != text(doc["sid"]) || parts[1] != strconv.FormatInt(number(writer["generation"]), 10) || parts[2] != text(data["lane"]) {
			return false
		}
	case "budget.settle":
		_, claimed := data["claim_id"]
		if data["outcome"] == "void" && (claimed || number(data["charged_micro"]) != 0) || data["outcome"] != "void" && !claimed {
			return false
		}
		if data["outcome"] == "unknown" && number(data["charged_micro"]) == 0 && data["lane_kind"] != "operator_local" {
			return false
		}
	}
	return true
}
func budgetValid(doc map[string]any) bool {
	body := object(doc["body"])
	switch doc["type"] {
	case "admit_request":
		parts := strings.Split(text(body["attempt_id"]), ":")
		if len(parts) != 4 || parts[0] != text(body["sid"]) || parts[1] != strconv.FormatInt(number(body["worker_generation"]), 10) || parts[2] != text(body["lane"]) {
			return false
		}
		n, err := strconv.ParseInt(parts[3], 10, 64)
		return err == nil && n <= tokens.MaxSafeInteger
	case "settle_request":
		_, actual := body["actual_micro"]
		return (body["outcome"] == "settled") == actual
	case "recover_response":
		if body["closed_reason"] == "void" && number(body["charged_micro"]) != 0 {
			return false
		}
		if body["closed_reason"] == "unknown" && number(body["charged_micro"]) == 0 && body["lane_kind"] != "operator_local" {
			return false
		}
	case "holds_list":
		seen := map[string]bool{}
		attempts := map[string]bool{}
		for _, value := range array(body["holds"]) {
			h := object(value)
			id, attempt := text(h["hold_id"]), text(h["attempt_id"])
			if seen[id] || attempts[attempt] || !strings.HasPrefix(attempt, text(body["sid"])+":") {
				return false
			}
			seen[id] = true
			attempts[attempt] = true
		}
	}
	return true
}
func snapshotValid(doc map[string]any) bool {
	if number(doc["expected_prev_rev"]) == tokens.MaxSafeInteger || number(doc["working_rev"]) != number(doc["expected_prev_rev"])+1 {
		return false
	}
	patch := object(doc["patch"])
	if digest([]byte(text(patch["canonical"]))) != text(patch["sha256"]) {
		return false
	}
	items := array(object(doc["spec"])["items"])
	byKey := map[string]map[string]any{}
	byRef := map[string][]map[string]any{}
	for _, value := range items {
		item := object(value)
		ref := text(item["item_ref"])
		key := ref + "@" + strconv.FormatInt(number(item["version"]), 10)
		if byKey[key] != nil || !canonicalDigest(item["content"], item["content_sha256"]) {
			return false
		}
		byKey[key] = item
		byRef[ref] = append(byRef[ref], item)
		state := text(item["state"])
		if doc["host_mode"] == "working_spec_only" && (!in(state, "draft", "confirmed", "superseded") || item["host"] != nil) {
			return false
		}
		if in(state, "proposed", "accepted", "invalidated", "rejected") && item["host"] == nil || in(state, "draft", "confirmed") && item["host"] != nil {
			return false
		}
		if ex, ok := item["extensions"]; ok {
			if number(doc["minor"]) < 1 || !extensionsValid(object(ex)) {
				return false
			}
		}
	}
	for _, value := range items {
		item := object(value)
		if sup := object(item["supersedes_item_version"]); sup != nil {
			ref := text(sup["item_ref"])
			prior := byKey[ref+"@"+strconv.FormatInt(number(sup["version"]), 10)]
			if ref != text(item["item_ref"]) || number(sup["version"]) >= number(item["version"]) || prior == nil || prior["state"] != "superseded" {
				return false
			}
		}
	}
	for _, versions := range byRef {
		live, accepted := 0, 0
		var terminal int64
		for _, item := range versions {
			state := text(item["state"])
			if !in(state, "superseded", "accepted", "invalidated", "rejected") {
				live++
			}
			if state == "accepted" {
				accepted++
				terminal = number(item["version"])
			}
		}
		if live > 1 || accepted > 1 || accepted == 1 && live > 0 {
			return false
		}
		if accepted == 1 {
			for _, item := range versions {
				if number(item["version"]) > terminal {
					return false
				}
			}
		}
	}
	keys := map[string]bool{}
	for _, value := range array(doc["pending_ops"]) {
		op := object(value)
		key := text(op["op_key"])
		if keys[key] || digest([]byte(text(op["payload"]))) != text(op["payload_sha256"]) || doc["host_mode"] == "working_spec_only" && in(text(op["op"]), "submit", "replace") {
			return false
		}
		keys[key] = true
	}
	return true
}
func extensionsValid(ex map[string]any) bool {
	raw, err := tokens.CanonicalJSON(marshal(ex))
	if ex == nil || len(ex) > 8 || err != nil || len(raw) > 64<<10 {
		return false
	}
	for key, value := range ex {
		instance := object(value)
		raw, err := tokens.CanonicalJSON(marshal(instance))
		parts := strings.Split(text(instance["version"]), ".")
		keyParts := strings.Split(key, "@")
		if len(parts) != 2 || len(keyParts) != 2 || parts[0] != keyParts[1] || err != nil || len(raw) > 16<<10 {
			return false
		}
		for _, part := range parts {
			n, err := strconv.ParseInt(part, 10, 64)
			if err != nil || n > tokens.MaxSafeInteger {
				return false
			}
		}
	}
	return true
}
