// SPDX-License-Identifier: AGPL-3.0-only

package sessionusage

import (
	"encoding/json"
	"fmt"
)

func classifyCursor(fields map[string]json.RawMessage) (usageRecord, int, error) {
	if raw, ok := fields["method"]; ok {
		method, err := parseString(raw)
		if err != nil {
			return usageRecord{}, outcomeIgnore, err
		}
		if method != "session/update" {
			return usageRecord{}, outcomeIgnore, nil
		}
		return cursorUpdate(fields)
	}
	raw, ok := fields["type"]
	if !ok {
		return usageRecord{}, outcomeIgnore, nil
	}
	kind, err := parseString(raw)
	if err != nil {
		return usageRecord{}, outcomeIgnore, err
	}
	if kind != "result" {
		return usageRecord{}, outcomeIgnore, nil
	}
	if _, ok := fields["usage"]; !ok {
		return usageRecord{}, outcomeIgnore, fmt.Errorf("%w: terminal result has no usage", ErrRejected)
	}
	rec, err := cursorResult(fields)
	return rec, outcomeUse, err
}

func cursorResult(fields map[string]json.RawMessage) (usageRecord, error) {
	usage, err := decodeObject(fields["usage"], nil)
	if err != nil {
		return usageRecord{}, err
	}
	snap, err := parseCursorUsage(usage)
	if err != nil {
		return usageRecord{}, err
	}
	if err := validateCostFields(fields); err != nil {
		return usageRecord{}, err
	}
	if err := validateCostFields(usage); err != nil {
		return usageRecord{}, err
	}
	model, err := modelFields(fields, usage)
	if err != nil {
		return usageRecord{}, err
	}
	id := ""
	if raw, ok := fields["request_id"]; ok {
		id, err = parseString(raw)
		if err != nil || !validSourceID(id) {
			return usageRecord{}, fmt.Errorf("%w: invalid request identity", ErrRejected)
		}
	}
	if raw, ok := fields["is_error"]; ok && string(raw) != "false" {
		return usageRecord{}, fmt.Errorf("%w: unsuccessful Cursor result", ErrRejected)
	}
	if raw, ok := fields["subtype"]; ok {
		s, err := parseString(raw)
		if err != nil || s != "success" {
			return usageRecord{}, fmt.Errorf("%w: unsuccessful Cursor result", ErrRejected)
		}
	}
	return usageRecord{accounting: accountingDelta, model: model, id: id, delta: &snap}, nil
}

func cursorUpdate(fields map[string]json.RawMessage) (usageRecord, int, error) {
	params, err := objectField(fields, "params")
	if err != nil {
		return usageRecord{}, outcomeIgnore, err
	}
	update, err := objectField(params, "update")
	if err != nil {
		return usageRecord{}, outcomeIgnore, err
	}
	kind, err := parseString(update["sessionUpdate"])
	if err != nil || kind != "usage_update" {
		return usageRecord{}, outcomeIgnore, nil
	}
	if raw, ok := update["cost"]; ok {
		if err := validateCostObject(raw); err != nil {
			return usageRecord{}, outcomeIgnore, err
		}
	}
	if err := validateCostFields(update); err != nil {
		return usageRecord{}, outcomeIgnore, err
	}
	if hasUsageSignal(update) {
		return usageRecord{}, outcomeIgnore, fmt.Errorf("%w: Cursor ACP usage_update has no documented token record", ErrAmbiguous)
	}
	return usageRecord{}, outcomeSkip, nil
}

// Cursor's result usage covers the whole turn. inputTokens excludes both
// cache categories; US1 input includes them. Cache writes are ordinary input,
// not cache reads. Source: Cursor staff clarification, 2026-06-30:
// https://forum.cursor.com/t/discrepancies-between-cursor-cli-usage-and-billing/164471/7
func parseCursorUsage(fields map[string]json.RawMessage) (snapshot, error) {
	for key := range fields {
		switch key {
		case "inputTokens", "outputTokens", "cacheReadTokens", "cacheWriteTokens", "totalTokens", "reasoningTokens", "model", "modelId", "model_id":
		default:
			return snapshot{}, fmt.Errorf("%w: unsupported Cursor usage field", ErrAmbiguous)
		}
	}
	input, err := parseCount(fields["inputTokens"])
	if err != nil {
		return snapshot{}, err
	}
	output, err := parseCount(fields["outputTokens"])
	if err != nil {
		return snapshot{}, err
	}
	snap := snapshot{output: output}
	readRaw, hasRead := fields["cacheReadTokens"]
	writeRaw, hasWrite := fields["cacheWriteTokens"]
	var read, write int64
	if hasRead {
		read, err = parseCount(readRaw)
		if err != nil {
			return snapshot{}, err
		}
		snap.cached, snap.cachedKnown = read, true
	}
	if hasWrite {
		write, err = parseCount(writeRaw)
		if err != nil {
			return snapshot{}, err
		}
	}
	if input > maxToken-read || input+read > maxToken-write {
		return snapshot{}, fmt.Errorf("%w: inclusive input overflow", ErrRejected)
	}
	snap.input = input + read + write // known subtotal, even when full input is unknown
	snap.inputKnown = hasRead && hasWrite
	if raw, ok := fields["totalTokens"]; ok {
		total, err := parseCount(raw)
		if err != nil {
			return snapshot{}, err
		}
		// totalTokens cannot recover a missing category: that would hide an
		// unsupported or changed vendor accounting convention.
		if snap.inputKnown && total != snap.input+output {
			return snapshot{}, fmt.Errorf("%w: total tokens mismatch", ErrAmbiguous)
		}
	}
	if raw, ok := fields["reasoningTokens"]; ok {
		n, err := parseCount(raw)
		if err != nil {
			return snapshot{}, err
		}
		if n > output {
			return snapshot{}, fmt.Errorf("%w: reasoning exceeds output", ErrRejected)
		}
		snap.reasoning, snap.reasoningKnown = n, true
	}
	return snap, nil
}
