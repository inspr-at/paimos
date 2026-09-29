// SPDX-License-Identifier: AGPL-3.0-only

package sessionusage

import (
	"fmt"
	"slices"
)

// ManagedCodex consumes individual app-server notifications from a newly created
// thread. It retains only bounded normalized counters, never the capture prefix
// required by the offline Parse API. It is called serially by the wire reader.
// The caller must bind the requested exact model before starting the first turn.
type ManagedCodex struct {
	thread, model, requested  string
	turn                      string
	rerouteModel, rerouteTurn string
	requireModel              bool
	previous                  snapshot
	models                    map[string]UsageReport
	failed, ended             bool
}

func NewManagedCodex(thread, model string) (*ManagedCodex, error) {
	model, err := canonicalModel(model)
	if err != nil || !validSourceID(thread) {
		return nil, fmt.Errorf("%w: managed thread/model binding", ErrRejected)
	}
	return &ManagedCodex{thread: thread, model: model, requested: model, previous: snapshot{cachedKnown: true}, models: map[string]UsageReport{}}, nil
}

// BindTurn checks any pre-ack usage identity against the acknowledged turn.
// Thread totals without turnId remain bound to this fresh, single-turn thread.
func (c *ManagedCodex) BindTurn(turn string) error {
	if !validSourceID(turn) || c.turn != "" && c.turn != turn {
		c.failed = true
		return ErrRejected
	}
	c.turn = turn
	return nil
}

// NextTurn is called only after an acknowledged clean terminal on a live owned
// thread and before its next turn/start. Cumulative counters remain unchanged.
func (c *ManagedCodex) NextTurn() error {
	if c.failed || c.ended || c.turn == "" || c.rerouteModel != "" {
		c.failed = true
		return ErrRejected
	}
	c.turn = ""
	c.model = c.requested
	return nil
}

// Observe accepts thread/tokenUsage/updated and model/rerouted. A model switch needs the
// vendor's last-usage snapshot to equal the entire increase since the previous
// total; otherwise attributing a thread-wide total to a new model is ambiguous.
// After a switch, each increased total needs explicit model evidence. Native
// reroutes are request-scoped, not a sticky thread default. Protocol reference:
// https://developers.openai.com/codex/app-server#turn-events
// Errors permanently stop this capture; previously emitted values stay provisional.
func (c *ManagedCodex) Observe(raw []byte) (report *UsageReport, err error) {
	if c.failed || c.ended {
		return nil, ErrRejected
	}
	defer func() {
		if err != nil {
			c.failed = true
		}
	}()
	if len(raw) > maxLine {
		return nil, ErrMalformed
	}
	fields, err := decodeLine(raw)
	if err != nil {
		return nil, err
	}
	method, err := parseString(fields["method"])
	if err != nil || method != "thread/tokenUsage/updated" && method != "model/rerouted" || fields["type"] != nil {
		return nil, ErrRejected
	}
	params, err := objectField(fields, "params")
	if err != nil {
		return nil, err
	}
	id, err := parseString(params["threadId"])
	if err != nil || id != c.thread {
		return nil, ErrRejected
	}
	if err := checkSourceIdentity(fields, Options{SourceSessionID: c.thread}); err != nil {
		return nil, err
	}
	if raw, ok := params["turnId"]; ok {
		turn, err := parseString(raw)
		if err != nil || c.BindTurn(turn) != nil {
			return nil, ErrRejected
		}
	}
	if method == "model/rerouted" {
		from, err := parseString(params["fromModel"])
		if err != nil {
			return nil, err
		}
		to, err := parseString(params["toModel"])
		if err != nil {
			return nil, err
		}
		to, err = canonicalModel(to)
		if err != nil {
			return nil, err
		}
		turn, err := parseString(params["turnId"])
		if err != nil || !validSourceID(turn) || from != c.model && from != c.requested ||
			c.rerouteModel != "" && (c.rerouteModel != to || c.rerouteTurn != turn) {
			return nil, ErrAmbiguous
		}
		c.rerouteModel, c.rerouteTurn = to, turn
		return nil, nil
	}
	rec, err := codexTokenUsage(fields)
	if err != nil {
		return nil, err
	}
	next := *rec.cumulative
	prev := c.previous
	if next.input < prev.input || next.output < prev.output {
		return nil, ErrRejected
	}
	if prev.inputKnown && prev.cachedKnown && !next.cachedKnown {
		return nil, ErrRejected
	}
	if prev.cachedKnown && next.cachedKnown && (next.cached < prev.cached || next.input-next.cached < prev.input-prev.cached) {
		return nil, ErrRejected
	}
	delta := snapshot{input: next.input - prev.input, output: next.output - prev.output,
		cached: next.cached - prev.cached, inputKnown: true, cachedKnown: next.cachedKnown && prev.cachedKnown}
	// Replayed totals do not consume a pending reroute or need new attribution.
	if next == prev {
		return nil, nil
	}
	model := rec.model
	if c.rerouteModel != "" {
		turn, err := parseString(params["turnId"])
		if err != nil || turn != c.rerouteTurn || model != "" && model != c.rerouteModel {
			return nil, ErrAmbiguous
		}
		model = c.rerouteModel
	}
	if model == "" {
		// Rerouting is documented per request, not as a permanent thread model.
		// Never guess whether later model-less usage stayed rerouted or returned.
		if c.requireModel {
			return nil, ErrAmbiguous
		}
		model = c.model
	}
	if model != c.model || c.rerouteModel != "" {
		last := rec.delta
		if last == nil || last.input != delta.input || last.output != delta.output ||
			last.cachedKnown != delta.cachedKnown || last.cachedKnown && last.cached != delta.cached {
			return nil, ErrAmbiguous
		}
	}
	old, exists := c.models[model]
	if !exists && len(c.models) >= maxUsage {
		return nil, ErrRejected
	}
	input, output := delta.input, delta.output
	cached := delta.cached
	if exists {
		input += *old.InputTokens
		output += *old.OutputTokens
		if old.CachedInputTokens != nil {
			cached += *old.CachedInputTokens
		}
	}
	if input > maxToken || output > maxToken || cached > maxToken {
		return nil, ErrRejected
	}
	out := UsageReport{Model: model, InputTokens: &input, OutputTokens: &output, Provisional: true, BillingMode: "unknown"}
	if delta.cachedKnown && (!exists || old.CachedInputTokens != nil) {
		out.CachedInputTokens = &cached
	}
	c.requireModel = c.requireModel || model != c.model || c.rerouteModel != ""
	c.previous, c.model = next, model
	c.rerouteModel, c.rerouteTurn = "", ""
	c.models[model] = out
	return &out, nil
}

// Finish seals only a clean completed stream with all counters known. Missing
// cache, an interrupted turn or malformed usage remains explicitly provisional.
func (c *ManagedCodex) Finish(clean bool) []UsageReport {
	if c.ended {
		return nil
	}
	c.ended = true
	keys := make([]string, 0, len(c.models))
	for model := range c.models {
		keys = append(keys, model)
	}
	slices.Sort(keys)
	out := make([]UsageReport, 0, len(keys))
	for _, model := range keys {
		r := c.models[model]
		r.Provisional = !clean || c.failed || c.rerouteModel != "" || r.CachedInputTokens == nil
		out = append(out, r)
	}
	return out
}
