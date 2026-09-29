// SPDX-License-Identifier: AGPL-3.0-only

package sessionusage

import (
	"bytes"
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
		if method == "turn/completed" || method == "turn/failed" {
			_, clean, err := codexTerminalStatus(fields)
			if err != nil {
				return usageRecord{}, outcomeIgnore, err
			}
			if !clean {
				return usageRecord{}, outcomeIgnore, fmt.Errorf("%w: failed source turn", ErrRejected)
			}
			return usageRecord{}, outcomeSkip, nil
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

// CodexTerminal contains only bounded identity and outcome evidence. Raw items,
// source errors and other terminal fields are never retained.
type CodexTerminal struct {
	ThreadID, TurnID string
	Clean            bool
}

// ParseCodexTerminal validates the app-server envelope, including turn identity.
func ParseCodexTerminal(raw []byte) (CodexTerminal, error) {
	if len(raw) > maxLine {
		return CodexTerminal{}, ErrMalformed
	}
	fields, err := decodeLine(raw)
	if err != nil {
		return CodexTerminal{}, err
	}
	return parseCodexTerminal(fields)
}

func parseCodexTerminal(fields map[string]json.RawMessage) (CodexTerminal, error) {
	var out CodexTerminal
	method, err := parseString(fields["method"])
	if err != nil || method != "turn/completed" && method != "turn/failed" {
		return out, ErrRejected
	}
	if fields["type"] != nil {
		return out, fmt.Errorf("%w: competing terminal formats", ErrAmbiguous)
	}
	if raw, ok := fields["jsonrpc"]; ok {
		version, err := parseString(raw)
		if err != nil || version != "2.0" {
			return out, fmt.Errorf("%w: jsonrpc version", ErrMalformed)
		}
	}
	params, err := objectField(fields, "params")
	if err != nil {
		return out, err
	}
	out.ThreadID, err = parseString(params["threadId"])
	if err != nil || !validSourceID(out.ThreadID) {
		return CodexTerminal{}, fmt.Errorf("%w: terminal thread identity missing", ErrMalformed)
	}
	turn, err := objectField(params, "turn")
	if err != nil {
		return CodexTerminal{}, err
	}
	out.TurnID, err = parseString(turn["id"])
	if err != nil || !validSourceID(out.TurnID) {
		return CodexTerminal{}, fmt.Errorf("%w: terminal turn identity missing", ErrMalformed)
	}
	status, err := parseString(turn["status"])
	if err != nil || status == "" {
		return CodexTerminal{}, fmt.Errorf("%w: terminal status missing", ErrMalformed)
	}
	out.Clean = method == "turn/completed" && status == "completed"
	if raw := turn["error"]; out.Clean && raw != nil && !bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return CodexTerminal{}, fmt.Errorf("%w: completed turn carries failure", ErrAmbiguous)
	}
	return out, nil
}

func codexTerminalStatus(fields map[string]json.RawMessage) (terminal, clean bool, err error) {
	method, err := parseString(fields["method"])
	if err != nil || method != "turn/completed" && method != "turn/failed" {
		return false, false, nil
	}
	out, err := parseCodexTerminal(fields)
	if err == nil && !out.Clean {
		err = fmt.Errorf("%w: failed or incomplete source turn", ErrRejected)
	}
	return true, out.Clean, err
}

// CodexStartedTurn validates the result of this connection's turn/start request.
// The protocol returns the initial turn, not another thread identity. The caller
// binds it to the exact threadId it sent in that correlated request.
func CodexStartedTurn(raw []byte) (string, error) {
	if len(raw) > maxLine {
		return "", ErrMalformed
	}
	fields, err := decodeLine(raw)
	if err != nil {
		return "", err
	}
	turn, err := objectField(fields, "turn")
	if err != nil {
		return "", err
	}
	id, err := parseString(turn["id"])
	if err != nil || !validSourceID(id) {
		return "", ErrMalformed
	}
	status, err := parseString(turn["status"])
	if err != nil || status != "inProgress" {
		return "", ErrRejected
	}
	return id, nil
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
		snap.reasoning, snap.reasoningKnown = reasoning, true
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
