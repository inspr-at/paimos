// SPDX-License-Identifier: AGPL-3.0-only

package sessionusage

import (
	"bufio"
	"bytes"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"
)

var uuidRE = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

// Parse reads Codex or Cursor usage JSONL and returns cumulative reports.
// Non-usage lines are ignored. Prompt and tool text is not retained.
func Parse(r io.Reader, opt Options) (Result, error) {
	norm, err := normalizeOptions(opt)
	if err != nil {
		return Result{}, err
	}
	p := &parser{model: norm.Model, models: map[string]*modelState{}, opt: norm}
	for _, prior := range norm.Priors {
		if _, ok := p.models[prior.Model]; ok {
			return Result{}, fmt.Errorf("%w: duplicate prior model", ErrAmbiguous)
		}
		p.models[prior.Model] = stateFromPrior(prior)
	}
	br := bufio.NewReaderSize(r, 4096)
	read, usage := 0, 0
	for {
		line, err := readLine(br)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return Result{}, err
		}
		read += len(line) + 1
		if read > maxInput {
			return Result{}, fmt.Errorf("%w: input exceeds %d bytes", ErrMalformed, maxInput)
		}
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		if bytes.IndexByte(line, 0) >= 0 || !utf8.Valid(line) {
			return Result{}, fmt.Errorf("%w: usage record is not UTF-8 text", ErrMalformed)
		}
		fields, err := decodeLine(line)
		if err != nil {
			return Result{}, err
		}
		rec, outcome, err := classify(norm.Source, fields)
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
			usage++
			if usage > maxUsage {
				return Result{}, fmt.Errorf("%w: too many usage records", ErrRejected)
			}
			if err := p.add(rec); err != nil {
				return Result{}, err
			}
		default:
			return Result{}, fmt.Errorf("%w: unrecognized usage record", ErrAmbiguous)
		}
	}
	return p.result()
}

func classify(source string, fields map[string]json.RawMessage) (usageRecord, int, error) {
	switch source {
	case "codex":
		return classifyCodex(fields)
	case "cursor":
		return classifyCursor(fields)
	default:
		return usageRecord{}, outcomeIgnore, fmt.Errorf("%w: source must be codex or cursor", ErrRejected)
	}
}

type parser struct {
	model      string
	accounting string
	models     map[string]*modelState
	opt        Options
}

type modelState struct {
	sequence   int64
	seen       bool
	accounting string
	input      counter
	output     counter
	cached     counter
}

func stateFromPrior(prior Prior) *modelState {
	return &modelState{
		sequence: prior.Sequence,
		input:    counterFrom(prior.InputTokens, prior.Sequence > 0),
		output:   counterFrom(prior.OutputTokens, prior.Sequence > 0),
		cached:   counterFrom(prior.CachedInputTokens, prior.Sequence > 0),
	}
}

func counterFrom(value *int64, marked bool) counter {
	if value != nil {
		return counter{set: true, known: true, value: *value}
	}
	if marked {
		return counter{set: true, known: false}
	}
	return counter{}
}

func (p *parser) add(rec usageRecord) error {
	if p.accounting == "" {
		p.accounting = rec.accounting
	} else if p.accounting != rec.accounting {
		return fmt.Errorf("%w: mixed cumulative and delta usage", ErrAmbiguous)
	}
	model := rec.model
	if model == "" {
		model = p.model
	} else if p.model != "" && model != p.model {
		return fmt.Errorf("%w: model does not match reporter model", ErrAmbiguous)
	}
	if model == "" && len(p.models) == 1 {
		for name := range p.models {
			model = name
		}
	}
	if model == "" {
		return fmt.Errorf("%w: model unknown", ErrRejected)
	}
	st := p.models[model]
	if st == nil {
		st = &modelState{}
		p.models[model] = st
	}
	st.seen = true
	st.accounting = rec.accounting
	if rec.cumulative != nil {
		return applyCumulative(st, rec)
	}
	if rec.delta == nil {
		return fmt.Errorf("%w: usage record has no counters", ErrMalformed)
	}
	return applyDelta(st, *rec.delta)
}

func applyCumulative(st *modelState, rec usageRecord) error {
	total := *rec.cumulative
	var last snapshot
	hasLast := rec.delta != nil
	if hasLast {
		last = *rec.delta
	}
	if err := checkStep(st.input, total.input, hasLast, last.input); err != nil {
		return err
	}
	if err := checkStep(st.output, total.output, hasLast, last.output); err != nil {
		return err
	}
	if err := checkStep(st.cached, total.cached, hasLast && last.cachedKnown, last.cached); err != nil {
		return err
	}
	if err := st.input.applyCumulative(total.input, true); err != nil {
		return err
	}
	if err := st.output.applyCumulative(total.output, true); err != nil {
		return err
	}
	return st.cached.applyCumulative(total.cached, total.cachedKnown)
}

func applyDelta(st *modelState, delta snapshot) error {
	if err := st.input.applyDelta(delta.input, true); err != nil {
		return err
	}
	if err := st.output.applyDelta(delta.output, true); err != nil {
		return err
	}
	return st.cached.applyDelta(delta.cached, delta.cachedKnown)
}

func checkStep(prev counter, total int64, hasLast bool, last int64) error {
	if !hasLast {
		return nil
	}
	if last > total {
		return fmt.Errorf("%w: delta exceeds cumulative", ErrRejected)
	}
	if !prev.set || !prev.known {
		return nil
	}
	if total < prev.value {
		return fmt.Errorf("%w: cumulative usage decreased", ErrRejected)
	}
	if total-prev.value != last {
		return fmt.Errorf("%w: delta does not match cumulative step", ErrAmbiguous)
	}
	return nil
}

func (c *counter) applyCumulative(value int64, present bool) error {
	if !present {
		if c.set && c.known {
			return fmt.Errorf("%w: known counter became unknown", ErrRejected)
		}
		c.set, c.known = true, false
		return nil
	}
	if c.set && c.known && value < c.value {
		return fmt.Errorf("%w: cumulative usage decreased", ErrRejected)
	}
	c.set, c.known, c.value = true, true, value
	return nil
}

func (c *counter) applyDelta(value int64, present bool) error {
	if !present {
		if c.set && c.known {
			return fmt.Errorf("%w: known counter became unknown", ErrRejected)
		}
		c.set, c.known = true, false
		return nil
	}
	if c.set && !c.known {
		return fmt.Errorf("%w: delta against unknown cumulative baseline", ErrAmbiguous)
	}
	base := int64(0)
	if c.set && c.known {
		base = c.value
	}
	if value > maxToken-base {
		return fmt.Errorf("%w: token overflow", ErrRejected)
	}
	c.set, c.known, c.value = true, true, base+value
	return nil
}

func (p *parser) result() (Result, error) {
	models := make([]string, 0)
	for model, st := range p.models {
		if st.seen {
			models = append(models, model)
		}
	}
	slices.Sort(models)
	out := Result{Reports: []UsageReport{}, Observations: []Observation{}}
	var accountID, accountLabel, subscription *string
	if p.opt.AccountID != "" {
		accountID = &p.opt.AccountID
	}
	if p.opt.AccountLabel != "" {
		accountLabel = &p.opt.AccountLabel
	}
	if p.opt.SubscriptionLabel != "" {
		subscription = &p.opt.SubscriptionLabel
	}
	for _, model := range models {
		st := p.models[model]
		if st.sequence >= maxToken {
			return Result{}, fmt.Errorf("%w: sequence overflow", ErrRejected)
		}
		id, err := p.opt.NewReportID()
		if err != nil {
			return Result{}, err
		}
		measure, provisional := measurementOf(st.input, st.output, st.cached)
		out.Reports = append(out.Reports, UsageReport{
			ReportID: id, Model: model, Sequence: st.sequence + 1,
			InputTokens: st.input.pointer(), OutputTokens: st.output.pointer(), CachedInputTokens: st.cached.pointer(),
			Provisional: provisional, AccountID: accountID, AccountLabel: accountLabel,
			BillingMode: p.opt.BillingMode, SubscriptionLabel: subscription,
		})
		out.Observations = append(out.Observations, Observation{
			Source: p.opt.Source, Model: model, ModelStatus: statusKnown,
			InputStatus: st.input.status(), OutputStatus: st.output.status(), CachedStatus: st.cached.status(),
			Measurement: measure, Accounting: st.accounting, Provisional: provisional,
		})
	}
	return out, nil
}

func measurementOf(input, output, cached counter) (string, bool) {
	if input.status() == statusKnown && output.status() == statusKnown && cached.status() == statusKnown {
		return statusKnown, false
	}
	if input.status() == statusKnown && output.status() == statusKnown {
		return statusProvisional, true
	}
	return statusUnknown, true
}

func normalizeOptions(opt Options) (Options, error) {
	switch opt.Source {
	case "codex", "cursor":
	default:
		return Options{}, fmt.Errorf("%w: source must be codex or cursor", ErrRejected)
	}
	if opt.Model != "" {
		model, err := canonicalModel(opt.Model)
		if err != nil {
			return Options{}, err
		}
		opt.Model = model
	}
	if opt.BillingMode == "" {
		opt.BillingMode = "unknown"
	}
	switch opt.BillingMode {
	case "unknown", "api", "subscription":
	default:
		return Options{}, fmt.Errorf("%w: billing_mode", ErrRejected)
	}
	label, err := canonicalLabel(opt.SubscriptionLabel, 120)
	if err != nil {
		return Options{}, err
	}
	opt.SubscriptionLabel = label
	if label != "" && opt.BillingMode != "subscription" {
		return Options{}, fmt.Errorf("%w: subscription label requires billing_mode subscription", ErrAmbiguous)
	}
	account, err := canonicalLabel(opt.AccountLabel, 60)
	if err != nil {
		return Options{}, err
	}
	opt.AccountLabel = account
	id, err := canonicalUUID(opt.AccountID)
	if err != nil {
		return Options{}, err
	}
	opt.AccountID = id
	if opt.NewReportID == nil {
		opt.NewReportID = newReportID
	}
	for i := range opt.Priors {
		prior, err := normalizePrior(opt.Priors[i])
		if err != nil {
			return Options{}, err
		}
		opt.Priors[i] = prior
	}
	return opt, nil
}

func normalizePrior(prior Prior) (Prior, error) {
	model, err := canonicalModel(prior.Model)
	if err != nil {
		return Prior{}, err
	}
	prior.Model = model
	if prior.Sequence < 0 || prior.Sequence > maxToken {
		return Prior{}, fmt.Errorf("%w: prior sequence", ErrRejected)
	}
	for _, value := range []*int64{prior.InputTokens, prior.OutputTokens, prior.CachedInputTokens} {
		if value != nil && (*value < 0 || *value > maxToken) {
			return Prior{}, fmt.Errorf("%w: prior token", ErrRejected)
		}
	}
	if prior.CachedInputTokens != nil && prior.InputTokens != nil && *prior.CachedInputTokens > *prior.InputTokens {
		return Prior{}, fmt.Errorf("%w: prior cached tokens exceed input", ErrRejected)
	}
	known := prior.InputTokens != nil || prior.OutputTokens != nil || prior.CachedInputTokens != nil
	if prior.Sequence == 0 && known {
		return Prior{}, fmt.Errorf("%w: prior sequence", ErrRejected)
	}
	return prior, nil
}

func canonicalUUID(raw string) (string, error) {
	s := strings.ToLower(strings.TrimSpace(raw))
	if s == "" {
		return "", nil
	}
	if !uuidRE.MatchString(s) || s == "00000000-0000-0000-0000-000000000000" {
		return "", fmt.Errorf("%w: account_id", ErrRejected)
	}
	return s, nil
}

func canonicalLabel(raw string, max int) (string, error) {
	if strings.TrimSpace(raw) == "" {
		if strings.TrimSpace(raw) != raw && raw != "" {
			return "", fmt.Errorf("%w: label", ErrRejected)
		}
		return "", nil
	}
	s := strings.TrimSpace(raw)
	if len(s) > max || !utf8.ValidString(s) {
		return "", fmt.Errorf("%w: label", ErrRejected)
	}
	for _, r := range s {
		if r < 0x20 || r == 0x7f {
			return "", fmt.Errorf("%w: label", ErrRejected)
		}
	}
	return s, nil
}

func newReportID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16]), nil
}

func readLine(r *bufio.Reader) ([]byte, error) {
	var buf []byte
	for {
		chunk, err := r.ReadSlice('\n')
		buf = append(buf, chunk...)
		if len(buf) > maxLine {
			return nil, fmt.Errorf("%w: record exceeds %d bytes", ErrMalformed, maxLine)
		}
		if errors.Is(err, bufio.ErrBufferFull) {
			continue
		}
		if errors.Is(err, io.EOF) {
			if len(buf) == 0 {
				return nil, io.EOF
			}
			return buf, nil
		}
		if err != nil {
			return nil, err
		}
		return buf, nil
	}
}

// ParsePriors reads a JSON array of prior cumulative snapshots.
func ParsePriors(raw []byte) ([]Prior, error) {
	dec := json.NewDecoder(bytes.NewReader(bytes.TrimSpace(raw)))
	dec.UseNumber()
	tok, err := dec.Token()
	if err != nil || tok != json.Delim('[') {
		return nil, fmt.Errorf("%w: priors must be a JSON array", ErrMalformed)
	}
	var out []Prior
	for dec.More() {
		var obj json.RawMessage
		if err := dec.Decode(&obj); err != nil {
			return nil, fmt.Errorf("%w: prior", ErrMalformed)
		}
		prior, err := decodePrior(obj)
		if err != nil {
			return nil, err
		}
		out = append(out, prior)
	}
	tok, err = dec.Token()
	if err != nil || tok != json.Delim(']') {
		return nil, fmt.Errorf("%w: priors must be a JSON array", ErrMalformed)
	}
	var extra any
	if err := dec.Decode(&extra); !isEOF(err) {
		return nil, fmt.Errorf("%w: trailing JSON", ErrMalformed)
	}
	return out, nil
}

func decodePrior(raw json.RawMessage) (Prior, error) {
	fields, err := decodeObject(raw, allow("model", "sequence", "input_tokens", "output_tokens", "cached_input_tokens"))
	if err != nil {
		return Prior{}, err
	}
	for _, key := range []string{"model", "sequence", "input_tokens", "output_tokens", "cached_input_tokens"} {
		if _, ok := fields[key]; !ok {
			return Prior{}, fmt.Errorf("%w: prior missing %s", ErrMalformed, key)
		}
	}
	model, err := parseString(fields["model"])
	if err != nil {
		return Prior{}, err
	}
	seq, err := parseCount(fields["sequence"])
	if err != nil {
		return Prior{}, err
	}
	input, err := parseNullableCount(fields["input_tokens"])
	if err != nil {
		return Prior{}, err
	}
	output, err := parseNullableCount(fields["output_tokens"])
	if err != nil {
		return Prior{}, err
	}
	cached, err := parseNullableCount(fields["cached_input_tokens"])
	if err != nil {
		return Prior{}, err
	}
	return Prior{Model: model, Sequence: seq, InputTokens: input, OutputTokens: output, CachedInputTokens: cached}, nil
}

func parseNullableCount(raw json.RawMessage) (*int64, error) {
	if string(bytes.TrimSpace(raw)) == "null" {
		return nil, nil
	}
	n, err := parseCount(raw)
	if err != nil {
		return nil, err
	}
	return &n, nil
}
