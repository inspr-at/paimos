// SPDX-License-Identifier: AGPL-3.0-only

package sessionusage

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"
)

var uuidRE = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

// Parse consumes a bounded, offline metadata capture, never opens a vendor file
// or executes a process. Deltas need a durable capture envelope {record_id: UUID,
// event: vendorRecord}, or Cursor's native request_id. Full prefixes are replayed
// from zero; previously reported totals are never added a second time.
func Parse(r io.Reader, opt Options) (Result, error) {
	opt, err := normalizeOptions(opt)
	if err != nil {
		return Result{}, err
	}
	raw, err := io.ReadAll(io.LimitReader(r, maxInput+1))
	if err != nil {
		return Result{}, fmt.Errorf("%w: capture read failed", ErrMalformed)
	}
	if len(raw) > maxInput {
		return Result{}, fmt.Errorf("%w: capture too large", ErrMalformed)
	}
	binding := captureBinding(opt)
	if err := checkPrefix(raw, opt, binding); err != nil {
		return Result{}, err
	}
	p := &parser{models: map[string]*modelState{}, seen: map[string]usageRecord{}, opt: opt}
	for _, line := range bytes.Split(raw, []byte{'\n'}) {
		if len(line) > maxLine {
			return Result{}, fmt.Errorf("%w: record too large", ErrMalformed)
		}
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		if bytes.IndexByte(line, 0) >= 0 || !utf8.Valid(line) {
			return Result{}, fmt.Errorf("%w: invalid text", ErrMalformed)
		}
		fields, err := decodeLine(line)
		if err != nil {
			return Result{}, err
		}
		fields, id, err := unwrapRecord(fields)
		if err != nil {
			return Result{}, err
		}
		if err := checkSourceIdentity(fields, opt); err != nil {
			return Result{}, err
		}
		rec, outcome, err := classify(opt.Source, fields)
		if err != nil {
			return Result{}, err
		}
		switch outcome {
		case outcomeSkip:
		case outcomeIgnore:
			if hasUsageSignal(fields) {
				return Result{}, fmt.Errorf("%w: unrecognized usage record", ErrAmbiguous)
			}
		case outcomeUse:
			if id != "" {
				if rec.id != "" {
					return Result{}, fmt.Errorf("%w: competing record identities", ErrAmbiguous)
				}
				rec.id = id
			}
			if err := p.add(rec); err != nil {
				return Result{}, err
			}
		default:
			return Result{}, ErrAmbiguous
		}
	}
	out := p.result()
	out.Checkpoint = Checkpoint{Binding: binding, Bytes: len(raw), Digest: digest(raw), Final: opt.Final}
	// Recompute the accepted prefix to enforce US1's known-counter monotonicity
	// and final immutability. Checkpoints are not arbitrary token baselines.
	if prev := opt.Previous; prev != nil {
		priorOpt := opt
		priorOpt.Previous, priorOpt.FromStart, priorOpt.Final = nil, true, prev.Final
		before, err := Parse(bytes.NewReader(raw[:prev.Bytes]), priorOpt)
		if err != nil {
			return Result{}, err
		}
		if err := checkReports(before.Reports, out.Reports); err != nil {
			return Result{}, err
		}
	}
	return out, nil
}

func classify(source string, fields map[string]json.RawMessage) (usageRecord, int, error) {
	if fields["method"] != nil && fields["type"] != nil {
		return usageRecord{}, outcomeIgnore, fmt.Errorf("%w: competing record formats", ErrAmbiguous)
	}
	if source == "codex" {
		return classifyCodex(fields)
	}
	return classifyCursor(fields)
}

type parser struct {
	accounting string
	models     map[string]*modelState
	seen       map[string]usageRecord
	count      int64
	opt        Options
}
type modelState struct {
	accounting string
	input      counter
	output     counter
	cached     counter
}

func (p *parser) add(rec usageRecord) error {
	if p.accounting != "" && p.accounting != rec.accounting {
		return fmt.Errorf("%w: mixed cumulative and delta usage", ErrAmbiguous)
	}
	p.accounting = rec.accounting
	if rec.model == "" {
		rec.model = p.opt.Model
	}
	if rec.model == "" {
		return fmt.Errorf("%w: model unknown; explicit model binding required", ErrRejected)
	}
	if p.opt.Model != "" && rec.model != p.opt.Model {
		return fmt.Errorf("%w: model does not match capture binding", ErrAmbiguous)
	}
	if rec.cumulative != nil && p.opt.Model == "" {
		return fmt.Errorf("%w: thread totals require fixed model binding", ErrAmbiguous)
	}
	if rec.cumulative == nil && rec.id == "" {
		return fmt.Errorf("%w: delta requires durable record identity", ErrAmbiguous)
	}
	if rec.id != "" {
		if old, ok := p.seen[rec.id]; ok {
			if !sameRecord(old, rec) {
				return fmt.Errorf("%w: record identity reused with different usage", ErrAmbiguous)
			}
			return nil
		}
		p.seen[rec.id] = rec
	}
	p.count++
	if p.count > maxUsage {
		return fmt.Errorf("%w: too many usage records", ErrRejected)
	}
	st := p.models[rec.model]
	if st == nil {
		st = &modelState{accounting: rec.accounting}
		p.models[rec.model] = st
	}
	if rec.cumulative != nil {
		return applyCumulative(st, rec)
	}
	if rec.delta == nil {
		return ErrMalformed
	}
	st.input.add(rec.delta.input, rec.delta.inputKnown)
	st.output.add(rec.delta.output, true)
	st.cached.add(rec.delta.cached, rec.delta.cachedKnown)
	for _, c := range []counter{st.input, st.output, st.cached} {
		if c.value > maxToken {
			return fmt.Errorf("%w: token overflow", ErrRejected)
		}
	}
	return nil
}

func sameRecord(a, b usageRecord) bool {
	return a.model == b.model && a.accounting == b.accounting && sameSnapshot(a.cumulative, b.cumulative) && sameSnapshot(a.delta, b.delta)
}
func sameSnapshot(a, b *snapshot) bool {
	return a == nil && b == nil || a != nil && b != nil && *a == *b
}

func applyCumulative(st *modelState, rec usageRecord) error {
	total := *rec.cumulative
	// 'last' describes the latest vendor call, not necessarily the difference
	// since a previous notification. Duplicate and coalesced notifications exist.
	if last := rec.delta; last != nil {
		if last.input > total.input || last.output > total.output || last.cachedKnown && total.cachedKnown && last.cached > total.cached {
			return fmt.Errorf("%w: last usage exceeds total", ErrRejected)
		}
	}
	if st.input.known && st.cached.known && total.cachedKnown && total.input-total.cached < st.input.value-st.cached.value {
		return fmt.Errorf("%w: uncached cumulative usage decreased", ErrRejected)
	}
	if err := st.input.replace(total.input, total.inputKnown); err != nil {
		return err
	}
	if err := st.output.replace(total.output, true); err != nil {
		return err
	}
	return st.cached.replace(total.cached, total.cachedKnown)
}
func (c *counter) replace(value int64, known bool) error {
	if c.set && c.known && (!known || value < c.value) {
		return fmt.Errorf("%w: cumulative counter decreased or became unknown", ErrRejected)
	}
	*c = counter{set: true, known: known, value: value}
	return nil
}
func (c *counter) add(value int64, known bool) {
	// Once any summand is unknown the sum stays unknown. Still bound the known
	// subtotal to catch overflow; maxUsage * maxToken fits in int64.
	c.known = known && (!c.set || c.known)
	c.set = true
	c.value += value
}

func (p *parser) result() Result {
	models := make([]string, 0, len(p.models))
	for m := range p.models {
		models = append(models, m)
	}
	slices.Sort(models)
	out := Result{Reports: []UsageReport{}, Observations: []Observation{}}
	seq := p.count * 2
	if p.opt.Final {
		seq++
	}
	for _, model := range models {
		st := p.models[model]
		measure := statusKnown
		if !st.input.known || !st.cached.known {
			measure = statusProvisional
		}
		provisional := !p.opt.Final || measure != statusKnown
		out.Reports = append(out.Reports, UsageReport{
			ReportID: reportID(p.opt.SessionID, model, seq), Model: model, Sequence: seq,
			InputTokens: st.input.pointer(), OutputTokens: st.output.pointer(), CachedInputTokens: st.cached.pointer(),
			Provisional: provisional, AccountID: optional(p.opt.AccountID), AccountLabel: optional(p.opt.AccountLabel),
			BillingMode: p.opt.BillingMode, SubscriptionLabel: optional(p.opt.SubscriptionLabel),
		})
		out.Observations = append(out.Observations, Observation{Source: p.opt.Source, Model: model, ModelStatus: statusKnown,
			InputStatus: st.input.status(), OutputStatus: st.output.status(), CachedStatus: st.cached.status(), Measurement: measure, Accounting: st.accounting, Provisional: provisional})
	}
	return out
}
func optional(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
func reportID(session, model string, seq int64) string {
	// Body intentionally excluded: changing a payload under the same identity
	// must conflict at US1 rather than quietly acquiring a fresh receipt.
	b := sha256.Sum256([]byte(fmt.Sprintf("aeon-usage-v2\x00%s\x00%s\x00%d", session, model, seq)))
	b[6] = (b[6] & 0x0f) | 0x80
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

func normalizeOptions(opt Options) (Options, error) {
	if opt.Source != "codex" && opt.Source != "cursor" {
		return Options{}, fmt.Errorf("%w: source must be codex or cursor", ErrRejected)
	}
	if !opt.FromStart && opt.Previous == nil {
		return Options{}, fmt.Errorf("%w: explicit from-start assertion or checkpoint required", ErrRejected)
	}
	id, err := canonicalUUID(opt.SessionID)
	if err != nil || id == "" {
		return Options{}, fmt.Errorf("%w: Aeon session UUID required", ErrRejected)
	}
	opt.SessionID = id
	if !validSourceID(opt.SourceSessionID) {
		return Options{}, fmt.Errorf("%w: source session identity required", ErrRejected)
	}
	if opt.Model != "" {
		opt.Model, err = canonicalModel(opt.Model)
		if err != nil {
			return Options{}, err
		}
	}
	if opt.BillingMode == "" {
		opt.BillingMode = "unknown"
	}
	if opt.BillingMode != "unknown" && opt.BillingMode != "api" && opt.BillingMode != "subscription" {
		return Options{}, fmt.Errorf("%w: billing mode", ErrRejected)
	}
	opt.SubscriptionLabel, err = canonicalLabel(opt.SubscriptionLabel, 120)
	if err != nil {
		return Options{}, err
	}
	if opt.SubscriptionLabel != "" && opt.BillingMode != "subscription" {
		return Options{}, fmt.Errorf("%w: subscription label requires subscription mode", ErrAmbiguous)
	}
	opt.AccountLabel, err = canonicalLabel(opt.AccountLabel, 128)
	if err != nil {
		return Options{}, err
	}
	opt.AccountID, err = canonicalUUID(opt.AccountID)
	return opt, err
}
func canonicalUUID(raw string) (string, error) {
	s := strings.ToLower(raw)
	if s == "" {
		return "", nil
	}
	if !uuidRE.MatchString(s) || s == "00000000-0000-0000-0000-000000000000" {
		return "", fmt.Errorf("%w: invalid UUID", ErrRejected)
	}
	return s, nil
}
func canonicalLabel(s string, max int) (string, error) {
	if !utf8.ValidString(s) || utf8.RuneCountInString(s) > max || strings.TrimSpace(s) != s || strings.ContainsFunc(s, unicode.IsControl) {
		return "", fmt.Errorf("%w: public label", ErrRejected)
	}
	return s, nil
}
