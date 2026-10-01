// SPDX-License-Identifier: AGPL-3.0-only

package sessionusage

import "encoding/json"

// Gemini --output-format json includes complete per-model telemetry. Stream
// JSON omits thought tokens; its total cannot safely be treated as throughput.
// Schema: google-gemini/gemini-cli a5cdfab, telemetry/uiTelemetry.ts.
func classifyGeminiNative(fields map[string]json.RawMessage) (usageRecord, int, error) {
	if raw := fields["error"]; len(raw) > 0 && string(raw) != "null" {
		return usageRecord{}, outcomeIgnore, ErrRejected
	}
	stats, err := objectField(fields, "stats")
	if err != nil {
		return usageRecord{}, outcomeIgnore, err
	}
	models, err := objectField(stats, "models")
	if err != nil || len(models) != 1 {
		return usageRecord{}, outcomeIgnore, ErrAmbiguous
	}
	for model, raw := range models {
		metric, err := decodeObject(raw, nil)
		if err != nil {
			return usageRecord{}, outcomeIgnore, err
		}
		tokens, err := objectField(metric, "tokens")
		if err != nil {
			return usageRecord{}, outcomeIgnore, err
		}
		for key := range tokens {
			if _, ok := allow("input", "prompt", "candidates", "total", "cached", "thoughts", "tool")[key]; !ok {
				return usageRecord{}, outcomeIgnore, ErrAmbiguous
			}
		}
		values := map[string]int64{}
		for _, key := range []string{"input", "prompt", "candidates", "total", "cached", "thoughts", "tool"} {
			value, err := parseCount(tokens[key])
			if err != nil {
				return usageRecord{}, outcomeIgnore, err
			}
			values[key] = value
		}
		in, out, cached, thought := values["prompt"], values["candidates"]+values["thoughts"], values["cached"], values["thoughts"]
		if values["tool"] != 0 || values["input"]+cached != in || values["total"] != in+out || out > maxToken {
			return usageRecord{}, outcomeIgnore, ErrAmbiguous
		}
		snap := snapshot{input: in, output: out, cached: cached, reasoning: thought, inputKnown: true, cachedKnown: true, reasoningKnown: true}
		return usageRecord{accounting: accountingCumulative, model: model, cumulative: &snap}, outcomeUse, nil
	}
	return usageRecord{}, outcomeIgnore, ErrRejected
}

// OpenCode run --format json emits step_finish with a stable part.id. Its model
// is absent, so the caller must bind the capture to the exact provider/model.
// Schema: anomalyco/opencode v1.14.48, cli/cmd/run.ts and ACP buildUsage.
func classifyOpenCodeNative(fields map[string]json.RawMessage) (usageRecord, int, error) {
	part, err := objectField(fields, "part")
	if err != nil {
		return usageRecord{}, outcomeIgnore, err
	}
	id, err := parseString(part["id"])
	if err != nil || !validSourceID(id) {
		return usageRecord{}, outcomeIgnore, ErrRejected
	}
	tokens, err := objectField(part, "tokens")
	if err != nil {
		return usageRecord{}, outcomeIgnore, err
	}
	for key := range tokens {
		if _, ok := allow("input", "output", "reasoning", "cache", "total")[key]; !ok {
			return usageRecord{}, outcomeIgnore, ErrAmbiguous
		}
	}
	cache, err := objectField(tokens, "cache")
	if err != nil {
		return usageRecord{}, outcomeIgnore, err
	}
	if len(cache) != 2 {
		return usageRecord{}, outcomeIgnore, ErrAmbiguous
	}
	var in, out, thought, read, write int64
	for _, item := range []struct {
		raw json.RawMessage
		to  *int64
	}{{tokens["input"], &in}, {tokens["output"], &out}, {tokens["reasoning"], &thought}, {cache["read"], &read}, {cache["write"], &write}} {
		value, err := parseCount(item.raw)
		if err != nil {
			return usageRecord{}, outcomeIgnore, err
		}
		*item.to = value
	}
	if raw, exists := tokens["total"]; exists {
		total, err := parseCount(raw)
		if err != nil || total != in+out+thought+read+write {
			return usageRecord{}, outcomeIgnore, ErrAmbiguous
		}
	}
	if raw, exists := part["cost"]; exists {
		if _, ok := usdMicros(raw); !ok {
			return usageRecord{}, outcomeIgnore, ErrRejected
		}
	}
	if in+read+write > maxToken || out+thought > maxToken {
		return usageRecord{}, outcomeIgnore, ErrRejected
	}
	model, err := modelFields(fields, part)
	if err != nil {
		return usageRecord{}, outcomeIgnore, err
	}
	snap := snapshot{input: in + read + write, output: out + thought, cached: read, reasoning: thought, inputKnown: true, cachedKnown: true, reasoningKnown: true}
	return usageRecord{accounting: accountingDelta, model: model, id: id, delta: &snap}, outcomeUse, nil
}
