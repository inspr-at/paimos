// SPDX-License-Identifier: AGPL-3.0-only

package sessionusage

import (
	"encoding/json"
)

// ACPPromptUsage decodes completed turn throughput, never usage_update.used
// (context occupancy). Gemini includes cache reads in input; both vendors keep
// reasoning separate from visible output. OpenCode also excludes cache from
// input. The omitted optional fields are zero in these vendor prompt schemas.
// Sources: Gemini a5cdfab/acpSession.ts and OpenCode v1.14.48/acp/agent.ts.
func ACPPromptUsage(source string, raw json.RawMessage, model string) (UsageReport, bool) {
	fields, err := decodeObject(raw, nil)
	if err != nil {
		return UsageReport{}, false
	}
	snap, err := parseACPUsage(source, fields)
	if err != nil {
		return UsageReport{}, false
	}
	r, ok := CountReport(model, snap.input, snap.output, snap.cached, snap.cachedKnown)
	r.ReasoningTokens = &snap.reasoning
	return r, ok
}

func parseACPUsage(source string, fields map[string]json.RawMessage) (snapshot, error) {
	if source != "gemini" && source != "opencode" {
		return snapshot{}, ErrRejected
	}
	for key := range fields {
		if _, ok := allow("inputTokens", "outputTokens", "totalTokens", "cachedReadTokens", "cachedWriteTokens", "thoughtTokens")[key]; !ok {
			return snapshot{}, ErrAmbiguous
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
	total, err := parseCount(fields["totalTokens"])
	if err != nil {
		return snapshot{}, err
	}
	optional := func(key string) (int64, error) {
		if raw, exists := fields[key]; exists {
			return parseCount(raw)
		}
		return 0, nil
	}
	read, err := optional("cachedReadTokens")
	if err != nil {
		return snapshot{}, err
	}
	write, err := optional("cachedWriteTokens")
	if err != nil {
		return snapshot{}, err
	}
	thought, err := optional("thoughtTokens")
	if err != nil {
		return snapshot{}, err
	}
	if source == "gemini" {
		if total != input+output || read > input || write != 0 {
			return snapshot{}, ErrAmbiguous
		}
	} else {
		if total != input+output+read+write+thought {
			return snapshot{}, ErrAmbiguous
		}
		input += read + write
	}
	output += thought
	if input > maxToken || output > maxToken {
		return snapshot{}, ErrRejected
	}
	return snapshot{input: input, output: output, cached: read, reasoning: thought, inputKnown: true, cachedKnown: true, reasoningKnown: true}, nil
}

func classifyACP(source string, fields map[string]json.RawMessage) (usageRecord, int, error) {
	// Captures wrap the prompt response with a source session id and durable
	// record_id, using the same envelope as other turn-delta adapters.
	if fields["stats"] != nil && source == "gemini" {
		return classifyGeminiNative(fields)
	}
	if kind, _ := parseString(fields["type"]); source == "opencode" && kind == "step_finish" {
		return classifyOpenCodeNative(fields)
	}
	if fields["method"] != nil || fields["type"] != nil {
		return usageRecord{}, outcomeIgnore, nil
	}
	if _, exists := fields["usage"]; !exists {
		return usageRecord{}, outcomeIgnore, nil
	}
	stop, err := parseString(fields["stopReason"])
	if err != nil || stop != "end_turn" {
		return usageRecord{}, outcomeIgnore, ErrRejected
	}
	usage, err := objectField(fields, "usage")
	if err != nil {
		return usageRecord{}, outcomeIgnore, err
	}
	snap, err := parseACPUsage(source, usage)
	if err != nil {
		return usageRecord{}, outcomeIgnore, err
	}
	model, err := modelFields(fields)
	if err != nil {
		return usageRecord{}, outcomeIgnore, err
	}

	return usageRecord{accounting: accountingDelta, model: model, delta: &snap}, outcomeUse, nil
}
