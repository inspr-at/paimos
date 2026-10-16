// SPDX-License-Identifier: AGPL-3.0-only

package goals

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/inspr-at/paimos/internal/eta"
)

// Field distinguishes absent (unchanged), null (clear) and a supplied value.
// omitzero retains that distinction in the canonical receipt digest.
type Field[T any] struct {
	Set   bool
	Value *T
}

func (f Field[T]) IsZero() bool { return !f.Set }

func (f Field[T]) MarshalJSON() ([]byte, error) { return json.Marshal(f.Value) }

func (f *Field[T]) UnmarshalJSON(raw []byte) error {
	f.Set, f.Value = true, nil
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return nil
	}
	var value T
	if err := decodeStrict(raw, &value); err != nil {
		return err
	}
	f.Value = &value
	return nil
}

type ETAInput struct {
	Text string     `json:"text,omitempty"`
	At   *time.Time `json:"at,omitempty"`
}

type ProgressOverrideInput struct {
	Pct  *int   `json:"pct"`
	Note string `json:"note,omitempty"`
}

type Update struct {
	IfReadThrough    Field[int64]                 `json:"if_read_through,omitzero"`
	ETA              Field[ETAInput]              `json:"eta,omitzero"`
	Steps            Field[[]Step]                `json:"steps,omitzero"`
	ProgressOverride Field[ProgressOverrideInput] `json:"progress_override,omitzero"`
	Items            Field[ItemsUpdate]           `json:"items,omitzero"`
}

type ItemsUpdate struct {
	Mode       string                 `json:"mode,omitempty"`
	Upsert     []OutcomeUpsert        `json:"upsert,omitempty"`
	Remove     []string               `json:"remove,omitempty"`
	Move       []OutcomeMove          `json:"move,omitempty"`
	HandedOver Field[HandedOverInput] `json:"handed_over,omitzero"`
}

type OutcomeUpsert struct {
	Ref      string          `json:"ref"`
	Label    Field[string]   `json:"label,omitzero"`
	Keys     Field[[]string] `json:"keys,omitzero"`
	State    Field[State]    `json:"state,omitzero"`
	Note     Field[string]   `json:"note,omitzero"`
	ETA      Field[ETAInput] `json:"eta,omitzero"`
	Position Field[Position] `json:"position,omitzero"`
}

// Position retains exact decimal precision for Postgres numeric(30,15), so
// distinct ordering requests cannot collapse to the same float64 digest.
type Position string

func (p *Position) UnmarshalJSON(raw []byte) error {
	if len(raw) == 0 || raw[0] == '"' {
		return errors.New("position must be a JSON number")
	}
	var number json.Number
	if err := json.Unmarshal(raw, &number); err != nil {
		return err
	}
	*p = Position(number)
	return nil
}

func (p Position) decimal() (*big.Rat, bool) {
	// Bound exponent parsing before big-number allocation.
	if len(p) > 64 || !positionPattern.MatchString(string(p)) {
		return nil, false
	}
	r, ok := new(big.Rat).SetString(string(p))
	if !ok || new(big.Rat).Abs(r).Cmp(big.NewRat(1_000_000_000_000_000, 1)) >= 0 {
		return nil, false
	}
	return r, new(big.Rat).Mul(r, big.NewRat(1_000_000_000_000_000, 1)).IsInt()
}

func (p Position) MarshalJSON() ([]byte, error) {
	r, ok := p.decimal()
	if !ok {
		return nil, errors.New("position must fit numeric(30,15)")
	}
	// Exactly representable at scale 15; discard spelling differences such as
	// 1, 1.0 and 1e0 in the canonical parsed-body digest.
	value := strings.TrimRight(strings.TrimRight(r.FloatString(15), "0"), ".")
	if value == "-0" {
		value = "0"
	}
	return []byte(value), nil
}

type OutcomeMove struct {
	OutcomeID string `json:"outcome_id"`
	ToGoal    string `json:"to_goal"`
	ToRef     string `json:"to_ref,omitempty"`
}

type HandedOverInput struct {
	Ref    string `json:"ref"`
	Detail string `json:"detail,omitempty"`
}

// ValidationError carries a field path so HTTP/CLI callers can report the
// actual rejected input without turning a partial write into apparent success.
type ValidationError struct {
	Path    string
	Message string
}

func (e *ValidationError) Error() string { return e.Path + ": " + e.Message }

func invalid(path, message string) error { return &ValidationError{Path: path, Message: message} }

var (
	refPattern      = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)
	stepPattern     = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,31}$`)
	uuidPattern     = regexp.MustCompile(`(?i)^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
	keyPattern      = regexp.MustCompile(`^[A-Z][A-Z0-9]{1,9}-[1-9][0-9]*$`)
	idemPattern     = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/-]{7,199}$`)
	positionPattern = regexp.MustCompile(`^-?(?:0|[1-9][0-9]*)(?:\.[0-9]+)?(?:[eE][+-]?[0-9]{1,2})?$`)
)

func nodeIdentity(value string) bool {
	return value != "00000000-0000-0000-0000-000000000000" && (uuidPattern.MatchString(value) || len(value) <= 30 && keyPattern.MatchString(value))
}

// CleanIdempotencyKey matches the existing agent-outcome receipt grammar.
func CleanIdempotencyKey(value string) (string, error) {
	value = strings.TrimSpace(value)
	if strings.HasPrefix(value, "auto:") || !idemPattern.MatchString(value) {
		return "", invalid("Idempotency-Key", "use 8–200 permitted characters, starting with a letter or digit; auto: is reserved")
	}
	return value, nil
}

// DecodeUpdate bounds bytes before JSON allocation or digest work. Handlers
// must also use MaxBytesReader/body deadlines at the HTTP boundary.
func DecodeUpdate(reader io.Reader, now time.Time) (Update, error) {
	return decodeUpdate(reader, now, true)
}

// DecodeRequest validates bounded patch structure before receipt lookup. The
// final transaction checks relative ETA clock bounds only for a fresh receipt;
// an old, valid receipt remains replayable after its ETA ages out.
func DecodeRequest(reader io.Reader) (Update, error) { return decodeUpdate(reader, time.Time{}, false) }

func decodeUpdate(reader io.Reader, now time.Time, checkETAClock bool) (Update, error) {
	var update Update
	raw, err := io.ReadAll(io.LimitReader(reader, MaxBodyBytes+1))
	if err != nil {
		return update, err
	}
	if len(raw) > MaxBodyBytes {
		return update, invalid("body", "exceeds 256 KiB")
	}
	if !utf8.Valid(raw) {
		return update, invalid("body", "must be UTF-8 JSON")
	}
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || raw[0] != '{' {
		return update, invalid("body", "must be a JSON object")
	}
	if err := uniqueObjectKeys(raw); err != nil {
		return update, err
	}
	if err := decodeStrict(raw, &update); err != nil {
		return update, invalid("body", err.Error())
	}
	return update, update.validate(now, checkETAClock)
}

func decodeStrict(raw []byte, target any) error {
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(target); err != nil {
		return err
	}
	var trailing any
	if err := d.Decode(&trailing); !errors.Is(err, io.EOF) {
		return errors.New("expected exactly one JSON value")
	}
	return nil
}

// Duplicate keys would give one request multiple interpretations at different
// boundaries. Reject them, including inside optional objects, before decoding.
func uniqueObjectKeys(raw []byte) error {
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	var visit func(string, int) error
	visit = func(path string, depth int) error {
		if depth > 16 {
			return invalid(path, "JSON nesting exceeds 16")
		}
		token, err := d.Token()
		if err != nil {
			return invalid(path, "invalid JSON")
		}
		delim, composite := token.(json.Delim)
		if !composite {
			return nil
		}
		switch delim {
		case '{':
			seen := map[string]bool{}
			for d.More() {
				key, err := d.Token()
				if err != nil {
					return invalid(path, "invalid JSON object")
				}
				name, ok := key.(string)
				identity := strings.ToLower(name) // encoding/json also matches fields case-insensitively.
				if !ok || seen[identity] {
					return invalid(path, "duplicate JSON field")
				}
				seen[identity] = true
				if err := visit(path+"."+name, depth+1); err != nil {
					return err
				}
			}
		case '[':
			for i := 0; d.More(); i++ {
				if err := visit(fmt.Sprintf("%s[%d]", path, i), depth+1); err != nil {
					return err
				}
			}
		default:
			return invalid(path, "invalid JSON delimiter")
		}
		if _, err = d.Token(); err != nil {
			return invalid(path, "invalid JSON delimiter")
		}
		return nil
	}
	return visit("body", 0)
}

// Digest is the canonical JSON of the parsed, validated update. Omitted
// fields remain distinct from explicit nulls, and JSON member order does not
// affect the digest. Authorization and method/target matching precede replay
// in the mutation transaction; a digest alone never authorizes a receipt.
func (u Update) Digest() ([32]byte, error) {
	raw, err := json.Marshal(u)
	if err != nil {
		return [32]byte{}, err
	}
	return sha256.Sum256(raw), nil
}

func (u Update) Validate(now time.Time) error { return u.validate(now, true) }

// ValidateStructure leaves receipt-dependent time validity to the server.
func (u Update) ValidateStructure() error { return u.validate(time.Time{}, false) }

func (u Update) validate(now time.Time, checkETAClock bool) error {
	if u.IfReadThrough.Set && (u.IfReadThrough.Value == nil || *u.IfReadThrough.Value < 0) {
		return invalid("if_read_through", "must be a non-negative event id")
	}
	if err := validateETA(u.ETA.Value, "eta", now, checkETAClock); err != nil {
		return err
	}
	if u.Steps.Value != nil {
		if err := validateSteps(*u.Steps.Value); err != nil {
			return err
		}
	}
	if override := u.ProgressOverride.Value; override != nil {
		if override.Pct == nil || *override.Pct < 0 || *override.Pct > 100 {
			return invalid("progress_override.pct", "must be between 0 and 100")
		}
		if utf8.RuneCountInString(override.Note) > 200 {
			return invalid("progress_override.note", "must be at most 200 characters")
		}
	}
	if !u.Items.Set {
		return nil
	}
	if u.Items.Value == nil {
		return invalid("items", "use mode replace to remove all outcomes")
	}
	items := u.Items.Value
	if items.Mode != "" && items.Mode != "merge" && items.Mode != "replace" {
		return invalid("items.mode", "must be merge or replace")
	}
	if items.Mode == "replace" && !u.IfReadThrough.Set {
		return invalid("if_read_through", "is required for replace")
	}
	if len(items.Upsert)+len(items.Remove)+len(items.Move) > MaxOperations {
		return invalid("items", "at most 200 operations per call")
	}
	refs := map[string]bool{}
	for i, outcome := range items.Upsert {
		path := fmt.Sprintf("items.upsert[%d]", i)
		if !refPattern.MatchString(outcome.Ref) || refs[outcome.Ref] {
			return invalid(path+".ref", "a unique client ref is required")
		}
		refs[outcome.Ref] = true
		if err := outcome.validate(path, now, checkETAClock); err != nil {
			return err
		}
	}
	for i, ref := range items.Remove {
		if !refPattern.MatchString(ref) || refs[ref] {
			return invalid(fmt.Sprintf("items.remove[%d]", i), "must be a unique ref, absent from upsert")
		}
		refs[ref] = true
	}
	moved := map[string]bool{}
	for i, move := range items.Move {
		path := fmt.Sprintf("items.move[%d]", i)
		id := strings.ToLower(move.OutcomeID)
		if !uuidPattern.MatchString(id) || id == "00000000-0000-0000-0000-000000000000" || moved[id] {
			return invalid(path+".outcome_id", "must be a unique outcome UUID")
		}
		moved[id] = true
		if !nodeIdentity(move.ToGoal) {
			return invalid(path+".to_goal", "must be a goal key or UUID")
		}
		if move.ToRef != "" && !refPattern.MatchString(move.ToRef) {
			return invalid(path+".to_ref", "must be a client ref")
		}
	}
	if handover := items.HandedOver.Value; handover != nil {
		if !refPattern.MatchString(handover.Ref) {
			return invalid("items.handed_over.ref", "a client ref is required")
		}
		if utf8.RuneCountInString(handover.Detail) > 2000 {
			return invalid("items.handed_over.detail", "must be at most 2000 characters")
		}
	}
	return nil
}

func (o OutcomeUpsert) validate(path string, now time.Time, checkETAClock bool) error {
	if o.Label.Set && (o.Label.Value == nil || strings.TrimSpace(*o.Label.Value) == "" || utf8.RuneCountInString(*o.Label.Value) > 300) {
		return invalid(path+".label", "must contain 1–300 characters")
	}
	if o.Note.Value != nil && utf8.RuneCountInString(*o.Note.Value) > 200 {
		return invalid(path+".note", "must be at most 200 characters")
	}
	if o.State.Value != nil && !o.State.Value.valid() {
		return invalid(path+".state", "must be moving, queued, paused or done")
	}
	if o.Position.Value != nil {
		if _, ok := o.Position.Value.decimal(); !ok {
			return invalid(path+".position", "must fit numeric(30,15) without rounding")
		}
	}
	if o.Keys.Value != nil {
		keys := *o.Keys.Value
		if len(keys) > MaxTargets {
			return invalid(path+".keys", "at most 20 serves targets")
		}
		seen := map[string]bool{}
		for i, key := range keys {
			if !nodeIdentity(key) || seen[strings.ToLower(key)] {
				return invalid(fmt.Sprintf("%s.keys[%d]", path, i), "must be a unique node key or UUID")
			}
			seen[strings.ToLower(key)] = true
		}
	}
	return validateETA(o.ETA.Value, path+".eta", now, checkETAClock)
}

func validateETA(value *ETAInput, path string, now time.Time, checkETAClock bool) error {
	if value == nil {
		return nil
	}
	if utf8.RuneCountInString(value.Text) > 40 || strings.TrimSpace(value.Text) == "" && value.At == nil {
		return invalid(path+".text", "use text of at most 40 characters or a clock time")
	}
	if checkETAClock && value.At != nil && !eta.Allowed(*value.At, now) {
		return invalid(path+".at", "outside the allowed ETA range")
	}
	return nil
}

func validateSteps(steps []Step) error {
	if len(steps) > MaxSteps {
		return invalid("steps", "at most 20 steps")
	}
	seen := map[string]bool{}
	current := false
	for i, step := range steps {
		path := fmt.Sprintf("steps[%d]", i)
		if !stepPattern.MatchString(step.ID) || seen[step.ID] {
			return invalid(path+".id", "must be a unique step id")
		}
		seen[step.ID] = true
		if strings.TrimSpace(step.Text) == "" || utf8.RuneCountInString(step.Text) > 200 {
			return invalid(path+".text", "must contain 1–200 characters")
		}
		if step.State != "done" && step.State != "now" && step.State != "later" {
			return invalid(path+".state", "must be done, now or later")
		}
		if step.State == "now" {
			if current {
				return invalid(path+".state", "only one step may be now")
			}
			current = true
		}
	}
	return nil
}
