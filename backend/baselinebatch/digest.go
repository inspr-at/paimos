// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package baselinebatch

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"
	"strings"
	"unicode/utf16"
)

// Canonical payload and seals match Aithema aithema.handover/0.1:
// content_digest is SHA-256 of compact UTF-8 JSON {requirements, constraints}
// with refs and nested string arrays sorted by UTF-16 code units.
// revision_seal binds baseline_ref + revision + content_digest and does not
// authenticate approved_by.

type Requirement struct {
	Ref                string   `json:"requirement_ref"`
	Statement          string   `json:"statement"`
	AcceptanceCriteria []string `json:"acceptance_criteria"`
	ConstraintRefs     []string `json:"constraint_refs"`
}

type Constraint struct {
	Ref       string `json:"constraint_ref"`
	Kind      string `json:"kind"`
	Statement string `json:"statement"`
}

type canonicalRequirement struct {
	RequirementRef     string   `json:"requirement_ref"`
	Statement          string   `json:"statement"`
	AcceptanceCriteria []string `json:"acceptance_criteria"`
	ConstraintRefs     []string `json:"constraint_refs"`
}

type canonicalConstraint struct {
	ConstraintRef string `json:"constraint_ref"`
	Kind          string `json:"kind"`
	Statement     string `json:"statement"`
}

type canonicalPayload struct {
	Requirements []canonicalRequirement `json:"requirements"`
	Constraints  []canonicalConstraint  `json:"constraints"`
}

type canonicalSeal struct {
	BaselineRef   string `json:"baseline_ref"`
	Revision      int    `json:"revision"`
	ContentDigest string `json:"content_digest"`
}

// compareCodeUnits orders exactly like JavaScript's `<` on strings: by UTF-16
// code units. Go's own `<` is bytewise UTF-8 and disagrees whenever a
// supplementary-plane character meets a BMP character above U+E000, so an
// authentic Aithema baseline would otherwise digest differently here.
func compareCodeUnits(left, right string) int {
	if left == right {
		return 0
	}
	l := utf16.Encode([]rune(left))
	r := utf16.Encode([]rune(right))
	for i := 0; i < len(l) && i < len(r); i++ {
		if l[i] != r[i] {
			if l[i] < r[i] {
				return -1
			}
			return 1
		}
	}
	switch {
	case len(l) < len(r):
		return -1
	case len(l) > len(r):
		return 1
	}
	return 0
}

func sortedCopy(values []string) []string {
	out := make([]string, 0, len(values))
	out = append(out, values...)
	sort.Slice(out, func(i, j int) bool { return compareCodeUnits(out[i], out[j]) < 0 })
	return out
}

func compactJSON(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	// Encoder appends a newline; Aithema JSON.stringify does not.
	return stringifyEscapes(bytes.TrimSuffix(buf.Bytes(), []byte{'\n'})), nil
}

// stringifyEscapes rewrites the three escapes where encoding/json disagrees
// with JSON.stringify regardless of SetEscapeHTML: U+2028 and U+2029 are
// escaped unconditionally by Go and never by JavaScript, and Go spells
// backspace and form feed as the six-character \u0008 / \u000c forms
// instead of \b / \f. Scanning
// escape pairs keeps `\\u2028` (a literal backslash followed by text) intact.
//
// A lone surrogate cannot survive Go's JSON decoder, so a handover carrying one
// fails the digest comparison and is rejected rather than silently accepted.
func stringifyEscapes(in []byte) []byte {
	if !bytes.Contains(in, []byte(`\u`)) {
		return in
	}
	out := make([]byte, 0, len(in))
	for i := 0; i < len(in); {
		if in[i] != '\\' || i+1 >= len(in) {
			out = append(out, in[i])
			i++
			continue
		}
		if in[i+1] == 'u' && i+5 < len(in) {
			switch string(in[i+2 : i+6]) {
			case "2028":
				out = append(out, "\u2028"...)
				i += 6
				continue
			case "2029":
				out = append(out, "\u2029"...)
				i += 6
				continue
			case "0008":
				out = append(out, '\\', 'b')
				i += 6
				continue
			case "000c":
				out = append(out, '\\', 'f')
				i += 6
				continue
			}
		}
		out = append(out, in[i], in[i+1])
		i += 2
	}
	return out
}

func sha256Digest(payload []byte) string {
	sum := sha256.Sum256(payload)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func ContentDigest(requirements []Requirement, constraints []Constraint) (string, error) {
	reqs := append([]Requirement(nil), requirements...)
	cons := append([]Constraint(nil), constraints...)
	sort.Slice(reqs, func(i, j int) bool { return compareCodeUnits(reqs[i].Ref, reqs[j].Ref) < 0 })
	sort.Slice(cons, func(i, j int) bool { return compareCodeUnits(cons[i].Ref, cons[j].Ref) < 0 })
	payload := canonicalPayload{
		Requirements: make([]canonicalRequirement, 0, len(reqs)),
		Constraints:  make([]canonicalConstraint, 0, len(cons)),
	}
	for _, req := range reqs {
		payload.Requirements = append(payload.Requirements, canonicalRequirement{
			RequirementRef:     req.Ref,
			Statement:          req.Statement,
			AcceptanceCriteria: sortedCopy(req.AcceptanceCriteria),
			ConstraintRefs:     sortedCopy(req.ConstraintRefs),
		})
	}
	for _, c := range cons {
		payload.Constraints = append(payload.Constraints, canonicalConstraint{
			ConstraintRef: c.Ref,
			Kind:          c.Kind,
			Statement:     c.Statement,
		})
	}
	raw, err := compactJSON(payload)
	if err != nil {
		return "", err
	}
	return sha256Digest(raw), nil
}

func RevisionSeal(baselineRef string, revision int, contentDigest string) (string, error) {
	raw, err := compactJSON(canonicalSeal{
		BaselineRef:   baselineRef,
		Revision:      revision,
		ContentDigest: contentDigest,
	})
	if err != nil {
		return "", err
	}
	return sha256Digest(raw), nil
}

func DigestSHA256(parts ...string) string {
	sum := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return hex.EncodeToString(sum[:])
}
