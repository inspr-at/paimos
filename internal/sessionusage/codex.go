// SPDX-License-Identifier: AGPL-3.0-only

package sessionusage

import (
	"encoding/json"
	"fmt"
)

const (
	outcomeIgnore = iota
	outcomeSkip
	outcomeUse
)

func classifyCodex(fields map[string]json.RawMessage) (usageRecord, int, error) {
	if raw, ok := fields["method"]; ok {
		method, err := parseString(raw)
		if err != nil {
			return usageRecord{}, outcomeIgnore, err
		}
		if method == "thread/tokenUsage/updated" {
			rec, err := codexTokenUsage(fields)
			return rec, outcomeUse, err
		}
		return usageRecord{}, outcomeIgnore, nil
	}
	raw, ok := fields["type"]
	if !ok {
		return usageRecord{}, outcomeIgnore, nil
	}
	kind, err := parseString(raw)
	if err != nil {
		return usageRecord{}, outcomeIgnore, err
	}
	switch kind {
	case "event_msg":
		payload, err := objectField(fields, "payload")
		if err != nil {
			return usageRecord{}, outcomeIgnore, err
		}
		ptype, err := parseString(payload["type"])
		if err != nil {
			return usageRecord{}, outcomeIgnore, fmt.Errorf("%w: event_msg type", ErrMalformed)
		}
		if ptype != "token_count" {
			return usageRecord{}, outcomeIgnore, nil
		}
		rec, err := codexInfoUsage(payload, fields)
		return rec, outcomeUse, err
	case "token_count":
		rec, err := codexInfoUsage(fields, fields)
		return rec, outcomeUse, err
	case "turn.failed", "error":
		return usageRecord{}, outcomeIgnore, fmt.Errorf("%w: failed or incomplete source turn", ErrRejected)
	case "turn.completed":
		if _, ok := fields["usage"]; !ok {
			return usageRecord{}, outcomeIgnore, fmt.Errorf("%w: completed turn has no usage", ErrRejected)
		}
		rec, err := codexTurn(fields)
		return rec, outcomeUse, err
	default:
		return usageRecord{}, outcomeIgnore, nil
	}
}

func codexInfoUsage(holder, modelHolder map[string]json.RawMessage) (usageRecord, error) {
	info, err := objectField(holder, "info")
	if err != nil {
		return usageRecord{}, err
	}
	if _, ok := info["total_token_usage"]; !ok {
		return usageRecord{}, fmt.Errorf("%w: token_count has no cumulative total", ErrMalformed)
	}
	totalRaw, err := decodeObject(info["total_token_usage"], snakeUsageKeys)
	if err != nil {
		return usageRecord{}, err
	}
	total, err := parseSnakeUsage(totalRaw)
	if err != nil {
		return usageRecord{}, err
	}
	var last *snapshot
	if raw, ok := info["last_token_usage"]; ok {
		lastRaw, err := decodeObject(raw, snakeUsageKeys)
		if err != nil {
			return usageRecord{}, err
		}
		snap, err := parseSnakeUsage(lastRaw)
		if err != nil {
			return usageRecord{}, err
		}
		last = &snap
	}
	if err := validateCostFields(holder); err != nil {
		return usageRecord{}, err
	}
	if err := validateCostFields(info); err != nil {
		return usageRecord{}, err
	}
	model, err := modelFields(modelHolder, holder, info)
	if err != nil {
		return usageRecord{}, err
	}
	return usageRecord{accounting: accountingVendor, model: model, cumulative: &total, delta: last}, nil
}

func codexTurn(fields map[string]json.RawMessage) (usageRecord, error) {
	usage, err := decodeObject(fields["usage"], snakeUsageKeys)
	if err != nil {
		return usageRecord{}, err
	}
	snap, err := parseSnakeUsage(usage)
	if err != nil {
		return usageRecord{}, err
	}
	if err := validateCostFields(fields); err != nil {
		return usageRecord{}, err
	}
	model, err := modelFields(fields, nil)
	if err != nil {
		return usageRecord{}, err
	}
	return usageRecord{accounting: accountingDelta, model: model, delta: &snap}, nil
}

func codexTokenUsage(fields map[string]json.RawMessage) (usageRecord, error) {
	if raw, ok := fields["jsonrpc"]; ok {
		ver, err := parseString(raw)
		if err != nil || ver != "2.0" {
			return usageRecord{}, fmt.Errorf("%w: jsonrpc version", ErrMalformed)
		}
	}
	params, err := objectField(fields, "params")
	if err != nil {
		return usageRecord{}, err
	}
	tokenUsage, err := objectField(params, "tokenUsage")
	if err != nil {
		return usageRecord{}, err
	}
	if _, ok := tokenUsage["total"]; !ok {
		return usageRecord{}, fmt.Errorf("%w: tokenUsage has no cumulative total", ErrMalformed)
	}
	totalRaw, err := decodeObject(tokenUsage["total"], camelUsageKeys)
	if err != nil {
		return usageRecord{}, err
	}
	total, err := parseCamelUsage(totalRaw)
	if err != nil {
		return usageRecord{}, err
	}
	var last *snapshot
	if raw, ok := tokenUsage["last"]; ok {
		lastRaw, err := decodeObject(raw, camelUsageKeys)
		if err != nil {
			return usageRecord{}, err
		}
		snap, err := parseCamelUsage(lastRaw)
		if err != nil {
			return usageRecord{}, err
		}
		last = &snap
	}
	model, err := modelFields(fields, params, tokenUsage)
	if err != nil {
		return usageRecord{}, err
	}
	return usageRecord{accounting: accountingVendor, model: model, cumulative: &total, delta: last}, nil
}

var snakeUsageKeys = allow(
	"input_tokens", "output_tokens", "cached_input_tokens",
	"reasoning_output_tokens", "total_tokens",
)

var camelUsageKeys = allow(
	"inputTokens", "outputTokens", "cachedInputTokens",
	"reasoningOutputTokens", "totalTokens",
)

func parseSnakeUsage(fields map[string]json.RawMessage) (snapshot, error) {
	return parseUsage(fields, "input_tokens", "output_tokens", "cached_input_tokens", "reasoning_output_tokens", "total_tokens")
}

func parseCamelUsage(fields map[string]json.RawMessage) (snapshot, error) {
	return parseUsage(fields, "inputTokens", "outputTokens", "cachedInputTokens", "reasoningOutputTokens", "totalTokens")
}

func parseUsage(fields map[string]json.RawMessage, inputKey, outputKey, cachedKey, reasonKey, totalKey string) (snapshot, error) {
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
	var snap snapshot
	snap.input, snap.output, snap.inputKnown = input, output, true
	if raw, ok := fields[cachedKey]; ok {
		cached, err := parseCount(raw)
		if err != nil {
			return snapshot{}, err
		}
		if cached > input {
			return snapshot{}, fmt.Errorf("%w: cached tokens exceed input", ErrRejected)
		}
		snap.cached, snap.cachedKnown = cached, true
	}
	if raw, ok := fields[reasonKey]; ok {
		reasoning, err := parseCount(raw)
		if err != nil {
			return snapshot{}, err
		}
		if reasoning > output {
			return snapshot{}, fmt.Errorf("%w: reasoning tokens exceed output", ErrRejected)
		}
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

func objectField(fields map[string]json.RawMessage, key string) (map[string]json.RawMessage, error) {
	raw, ok := fields[key]
	if !ok {
		return nil, fmt.Errorf("%w: missing %s", ErrMalformed, key)
	}
	return decodeObject(raw, nil)
}

func modelFields(groups ...map[string]json.RawMessage) (string, error) {
	found := ""
	for _, fields := range groups {
		if fields == nil {
			continue
		}
		for _, key := range []string{"model", "model_id", "modelId"} {
			raw, ok := fields[key]
			if !ok {
				continue
			}
			text, err := parseString(raw)
			if err != nil {
				return "", err
			}
			model, err := canonicalModel(text)
			if err != nil {
				return "", err
			}
			if found != "" && found != model {
				return "", fmt.Errorf("%w: conflicting model identifiers", ErrAmbiguous)
			}
			found = model
		}
	}
	return found, nil
}
