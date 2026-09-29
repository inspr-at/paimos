// SPDX-License-Identifier: AGPL-3.0-only

package rules

import (
	"encoding/json"
	"net/http"
	"slices"
	"strings"

	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/inspr-at/paimos/internal/workorders"
	"github.com/jackc/pgx/v5"
)

// TLDRText is one explanation as a person or an agent writes it.
type TLDRText struct {
	EN string `json:"en"`
	DE string `json:"de,omitempty"`
}

// TLDRInput writes explanations into a set's draft (AEON-314) and nothing
// else: rule text, strength and selectors cannot change through it. Set absent
// keeps the set's explanation, null removes it. Rules maps identities to their
// new explanation; null removes one, an identity left out is unchanged. Every
// written explanation is stamped as fitting the current text. Publishing stays
// the normal person approval.
type TLDRInput struct {
	ExpectedRevision int64                `json:"expected_revision"`
	Set              json.RawMessage      `json:"set,omitempty"`
	Rules            map[string]*TLDRText `json:"rules"`
}

func (t *TLDRText) tldr() *TLDR {
	if t == nil {
		return nil
	}
	return &TLDR{EN: strings.TrimSpace(t.EN), DE: strings.TrimSpace(t.DE)}
}

// ApplyTLDRs returns the set with the requested explanations written, and
// whether anything changed. It validates everything before changing anything.
func ApplyTLDRs(s Set, in TLDRInput) (Set, bool, error) {
	if s.Revision != in.ExpectedRevision {
		return s, false, fail(409, "revision_conflict", "draft changed; reload and retry with its current revision")
	}
	if len(in.Set) == 0 && len(in.Rules) == 0 {
		return s, false, fail(400, "invalid_tldr", "name the set explanation or at least one rule")
	}
	index := map[string]int{}
	for i, r := range s.Rules {
		index[r.Identity] = i
	}
	for identity, text := range in.Rules {
		if _, ok := index[identity]; !ok {
			return s, false, fail(400, "unknown_rule", "no rule "+identity+" in this set's draft")
		}
		if err := ValidateTLDR(text.tldr()); err != nil {
			return s, false, err
		}
	}
	next := s
	next.Rules = slices.Clone(s.Rules)
	for identity, text := range in.Rules {
		i := index[identity]
		next.Rules[i].TLDR = storedTLDR(text.tldr(), RuleBasis(next.Rules[i]))
	}
	if len(in.Set) > 0 {
		var set *TLDR
		if string(in.Set) != "null" {
			var text TLDRText
			dec := json.NewDecoder(strings.NewReader(string(in.Set)))
			dec.DisallowUnknownFields()
			if err := dec.Decode(&text); err != nil {
				return s, false, fail(400, "invalid_tldr", "the set explanation must be an object with en and optional de")
			}
			set = text.tldr()
			if err := ValidateTLDR(set); err != nil {
				return s, false, err
			}
		}
		next.TLDR = storedTLDR(set, SetBasis(next.Rules))
	}
	changed := string(jsonBytes(next.TLDR)) != string(jsonBytes(storedTLDR(s.TLDR, SetBasis(s.Rules))))
	for i := range next.Rules {
		if string(jsonBytes(next.Rules[i].TLDR)) != string(jsonBytes(s.Rules[i].TLDR)) {
			changed = true
		}
	}
	return next, changed, nil
}

func (m *Module) tldr(r *http.Request, tx pgx.Tx, p tenant.Principal) (any, error) {
	var in TLDRInput
	if err := workorders.Decode(r, &in); err != nil {
		return nil, err
	}
	s, err := m.authorizedSet(r, tx, p, "rules.write")
	if err != nil {
		return nil, err
	}
	next, changed, err := ApplyTLDRs(s, in)
	if err != nil {
		return nil, err
	}
	if !changed {
		return present(s), nil
	}
	s, err = replaceDraftAs(r.Context(), tx, p, s, draftInput{ExpectedRevision: s.Revision, Name: s.Name, Rules: next.Rules, TLDR: jsonBytes(next.TLDR)}, "tldr_drafted")
	if err != nil {
		return nil, err
	}
	return present(s), nil
}
