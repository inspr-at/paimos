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
		return usageRecord{}, outcomeIgnore, nil
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
	return usageRecord{accounting: accountingDelta, model: model, delta: &snap}, nil
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

func parseCursorUsage(fields map[string]json.RawMessage) (snapshot, error) {
	_, camelIn := fields["inputTokens"]
	_, snakeIn := fields["input_tokens"]
	if camelIn && snakeIn {
		return snapshot{}, fmt.Errorf("%w: mixed token field spellings", ErrAmbiguous)
	}
	if camelIn {
		return parseCursorPair(fields, "inputTokens", "outputTokens", "cacheReadTokens", "cacheWriteTokens", "totalTokens")
	}
	if snakeIn {
		return parseCursorPair(fields, "input_tokens", "output_tokens", "cache_read_tokens", "cache_write_tokens", "total_tokens")
	}
	return snapshot{}, fmt.Errorf("%w: Cursor usage has no input tokens", ErrMalformed)
}

func parseCursorPair(fields map[string]json.RawMessage, inputKey, outputKey, readKey, writeKey, totalKey string) (snapshot, error) {
	for key := range fields {
		switch key {
		case inputKey, outputKey, readKey, writeKey, totalKey, "model", "modelId", "model_id":
		default:
			return snapshot{}, fmt.Errorf("%w: unknown counter field %q", ErrAmbiguous, key)
		}
	}
	inputRaw, ok := fields[inputKey]
	outputRaw, okOut := fields[outputKey]
	if !ok || !okOut {
		return snapshot{}, fmt.Errorf("%w: input and output tokens are required", ErrMalformed)
	}
	input, err := parseCount(inputRaw)
	if err != nil {
		return snapshot{}, err
	}
	output, err := parseCount(outputRaw)
	if err != nil {
		return snapshot{}, err
	}
	_, hasRead := fields[readKey]
	_, hasWrite := fields[writeKey]
	snap := snapshot{input: input, output: output}
	switch {
	case hasRead && hasWrite:
		read, err := parseCount(fields[readKey])
		if err != nil {
			return snapshot{}, err
		}
		write, err := parseCount(fields[writeKey])
		if err != nil {
			return snapshot{}, err
		}
		if read > maxToken-write {
			return snapshot{}, fmt.Errorf("%w: cached tokens overflow", ErrRejected)
		}
		cached := read + write
		if cached > input {
			return snapshot{}, fmt.Errorf("%w: cached tokens exceed input", ErrRejected)
		}
		snap.cached, snap.cachedKnown = cached, true
	case hasRead || hasWrite:
		return snapshot{}, fmt.Errorf("%w: partial Cursor cache pair", ErrAmbiguous)
	}
	if raw, ok := fields[totalKey]; ok {
		total, err := parseCount(raw)
		if err != nil {
			return snapshot{}, err
		}
		if total != input+output {
			return snapshot{}, fmt.Errorf("%w: total_tokens is not input plus output", ErrAmbiguous)
		}
	}
	return snap, nil
}
