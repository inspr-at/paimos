// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package baselinebatch

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/inspr-at/paimos/backend/safetext"
)

var forbiddenImportKeys = map[string]bool{
	"command": true, "argv": true, "argv0": true, "shell": true, "executable": true,
	"exec": true, "password": true, "passwd": true, "token": true, "secret": true,
	"credential": true, "credentials": true, "api_key": true, "apikey": true,
	"account_path": true, "home": true, "home_path": true, "workspace_path": true,
	"private_key": true, "target_ref": true, "target_secret": true, "env": true,
	"ready": true, "authorized": true, "actor": true, "session_token": true,
}

type parsedHandover struct {
	Version          string
	StreamRef        string
	ExportedAt       string
	Baseline         parsedBaseline
	PendingProposals []parsedProposal
	Decisions        []parsedDecision
}

type parsedBaseline struct {
	BaselineRef   string
	Revision      int
	ContentDigest string
	RevisionSeal  string
	ApprovedBy    string
	ApprovedAt    string
	Requirements  []Requirement
	Constraints   []Constraint
}

type parsedProposal struct {
	Ref     string
	Kind    string
	Summary string
}

type parsedDecision struct {
	Ref         string
	ProposalRef string
	Outcome     string
}

func parseHandover(raw []byte) (parsedHandover, error) {
	if len(raw) == 0 || len(raw) > maxImportBytes {
		return parsedHandover{}, fmt.Errorf("%w: handover size", ErrInvalid)
	}
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || raw[0] != '{' {
		return parsedHandover{}, fmt.Errorf("%w: handover must be an object", ErrInvalid)
	}
	var tree any
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if err := dec.Decode(&tree); err != nil {
		return parsedHandover{}, fmt.Errorf("%w: malformed handover", ErrInvalid)
	}
	if err := walkForbidden(tree); err != nil {
		return parsedHandover{}, err
	}
	obj, _ := tree.(map[string]any)
	if obj == nil {
		return parsedHandover{}, fmt.Errorf("%w: handover must be an object", ErrInvalid)
	}
	out := parsedHandover{
		Version:    asString(obj["handover_version"]),
		StreamRef:  asString(obj["stream_ref"]),
		ExportedAt: asString(obj["exported_at"]),
	}
	if out.Version != HandoverVersion {
		return parsedHandover{}, fmt.Errorf("%w: unsupported handover_version", ErrInvalid)
	}
	if !validRef(out.StreamRef) || out.ExportedAt == "" {
		return parsedHandover{}, fmt.Errorf("%w: stream identity", ErrInvalid)
	}
	baselineRaw, ok := obj["baseline"]
	if !ok || baselineRaw == nil {
		return parsedHandover{}, fmt.Errorf("%w: baseline is required", ErrInvalid)
	}
	baselineObj, _ := baselineRaw.(map[string]any)
	if baselineObj == nil {
		return parsedHandover{}, fmt.Errorf("%w: baseline must be an object", ErrInvalid)
	}
	baseline, err := parseBaseline(baselineObj)
	if err != nil {
		return parsedHandover{}, err
	}
	out.Baseline = baseline
	for _, item := range asArray(obj["pending_proposals"]) {
		prop, err := parseProposal(item)
		if err != nil {
			return parsedHandover{}, err
		}
		out.PendingProposals = append(out.PendingProposals, prop)
		if len(out.PendingProposals) > maxProposals {
			return parsedHandover{}, fmt.Errorf("%w: too many proposals", ErrInvalid)
		}
	}
	for _, item := range asArray(obj["decisions"]) {
		decn, err := parseDecision(item)
		if err != nil {
			return parsedHandover{}, err
		}
		out.Decisions = append(out.Decisions, decn)
	}
	return out, nil
}

func parseBaseline(obj map[string]any) (parsedBaseline, error) {
	out := parsedBaseline{
		BaselineRef:   asString(obj["baseline_ref"]),
		ContentDigest: asString(obj["content_digest"]),
		RevisionSeal:  asString(obj["revision_seal"]),
		ApprovedBy:    asString(obj["approved_by"]),
		ApprovedAt:    asString(obj["approved_at"]),
	}
	rev, err := asInt(obj["revision"])
	if err != nil || rev < 1 {
		return parsedBaseline{}, fmt.Errorf("%w: baseline revision", ErrInvalid)
	}
	out.Revision = rev
	if !validRef(out.BaselineRef) {
		return parsedBaseline{}, fmt.Errorf("%w: baseline_ref", ErrInvalid)
	}
	for _, item := range asArray(obj["requirements"]) {
		req, err := parseRequirement(item)
		if err != nil {
			return parsedBaseline{}, err
		}
		out.Requirements = append(out.Requirements, req)
		if len(out.Requirements) > maxRequirements {
			return parsedBaseline{}, fmt.Errorf("%w: too many requirements", ErrInvalid)
		}
	}
	for _, item := range asArray(obj["constraints"]) {
		c, err := parseConstraint(item)
		if err != nil {
			return parsedBaseline{}, err
		}
		out.Constraints = append(out.Constraints, c)
		if len(out.Constraints) > maxConstraints {
			return parsedBaseline{}, fmt.Errorf("%w: too many constraints", ErrInvalid)
		}
	}
	if len(out.Requirements) == 0 {
		return parsedBaseline{}, fmt.Errorf("%w: baseline requires requirements", ErrInvalid)
	}
	digest, err := ContentDigest(out.Requirements, out.Constraints)
	if err != nil {
		return parsedBaseline{}, err
	}
	if out.ContentDigest != digest {
		return parsedBaseline{}, fmt.Errorf("%w: tampered content_digest", ErrInvalid)
	}
	seal, err := RevisionSeal(out.BaselineRef, out.Revision, out.ContentDigest)
	if err != nil {
		return parsedBaseline{}, err
	}
	if out.RevisionSeal != seal {
		return parsedBaseline{}, fmt.Errorf("%w: tampered revision_seal", ErrInvalid)
	}
	if safetext.ContainsSecretLike(out.ApprovedBy) || safetext.ContainsSecretLike(out.ApprovedAt) {
		return parsedBaseline{}, fmt.Errorf("%w: secret-like imported claim", ErrInvalid)
	}
	return out, nil
}

func parseRequirement(item any) (Requirement, error) {
	obj, _ := item.(map[string]any)
	if obj == nil {
		return Requirement{}, fmt.Errorf("%w: requirement", ErrInvalid)
	}
	out := Requirement{
		Ref:                asString(obj["requirement_ref"]),
		Statement:          asString(obj["statement"]),
		AcceptanceCriteria: []string{},
		ConstraintRefs:     []string{},
	}
	if !validRef(out.Ref) || !validStatement(out.Statement) {
		return Requirement{}, fmt.Errorf("%w: requirement fields", ErrInvalid)
	}
	for _, c := range asArray(obj["acceptance_criteria"]) {
		s := asString(c)
		if !validStatement(s) {
			return Requirement{}, fmt.Errorf("%w: acceptance criterion", ErrInvalid)
		}
		out.AcceptanceCriteria = append(out.AcceptanceCriteria, s)
	}
	for _, c := range asArray(obj["constraint_refs"]) {
		s := asString(c)
		if !validRef(s) {
			return Requirement{}, fmt.Errorf("%w: constraint_ref", ErrInvalid)
		}
		out.ConstraintRefs = append(out.ConstraintRefs, s)
	}
	return out, nil
}

func parseConstraint(item any) (Constraint, error) {
	obj, _ := item.(map[string]any)
	if obj == nil {
		return Constraint{}, fmt.Errorf("%w: constraint", ErrInvalid)
	}
	out := Constraint{
		Ref:       asString(obj["constraint_ref"]),
		Kind:      asString(obj["kind"]),
		Statement: asString(obj["statement"]),
	}
	switch out.Kind {
	case "technical", "data", "authority", "cost", "agreement":
	default:
		return Constraint{}, fmt.Errorf("%w: constraint kind", ErrInvalid)
	}
	if !validRef(out.Ref) || !validStatement(out.Statement) {
		return Constraint{}, fmt.Errorf("%w: constraint fields", ErrInvalid)
	}
	return out, nil
}

func parseProposal(item any) (parsedProposal, error) {
	obj, _ := item.(map[string]any)
	if obj == nil {
		return parsedProposal{}, fmt.Errorf("%w: proposal", ErrInvalid)
	}
	out := parsedProposal{
		Ref:     asString(obj["proposal_ref"]),
		Kind:    asString(obj["kind"]),
		Summary: asString(obj["summary"]),
	}
	if !validRef(out.Ref) || !validStatement(out.Summary) {
		return parsedProposal{}, fmt.Errorf("%w: proposal fields", ErrInvalid)
	}
	return out, nil
}

func parseDecision(item any) (parsedDecision, error) {
	obj, _ := item.(map[string]any)
	if obj == nil {
		return parsedDecision{}, fmt.Errorf("%w: decision", ErrInvalid)
	}
	out := parsedDecision{
		Ref:         asString(obj["decision_ref"]),
		ProposalRef: asString(obj["proposal_ref"]),
		Outcome:     asString(obj["outcome"]),
	}
	if !validRef(out.Ref) || !validRef(out.ProposalRef) {
		return parsedDecision{}, fmt.Errorf("%w: decision fields", ErrInvalid)
	}
	return out, nil
}

func walkForbidden(node any) error {
	switch v := node.(type) {
	case map[string]any:
		for key, child := range v {
			lower := strings.ToLower(key)
			if forbiddenImportKeys[lower] {
				return fmt.Errorf("%w: imported field %s is not executable authority", ErrInvalid, lower)
			}
			if err := walkForbidden(child); err != nil {
				return err
			}
		}
	case []any:
		for _, child := range v {
			if err := walkForbidden(child); err != nil {
				return err
			}
		}
	case string:
		if safetext.ContainsSecretLike(v) {
			return fmt.Errorf("%w: secret-like imported content", ErrInvalid)
		}
	}
	return nil
}

func asString(v any) string {
	s, _ := v.(string)
	return strings.TrimSpace(s)
}

func asArray(v any) []any {
	a, _ := v.([]any)
	if a == nil {
		return []any{}
	}
	return a
}

func asInt(v any) (int, error) {
	switch n := v.(type) {
	case json.Number:
		i, err := n.Int64()
		return int(i), err
	case float64:
		return int(n), nil
	default:
		return 0, fmt.Errorf("not an int")
	}
}

func validRef(v string) bool {
	if v == "" || len(v) > maxRefBytes || !utf8.ValidString(v) {
		return false
	}
	if strings.ContainsAny(v, "\x00\r\n") || safetext.ContainsSecretLike(v) {
		return false
	}
	return true
}

func validStatement(v string) bool {
	if v == "" || len(v) > maxStatementBytes || !utf8.ValidString(v) {
		return false
	}
	if strings.ContainsRune(v, 0) || safetext.ContainsSecretLike(v) {
		return false
	}
	return true
}
