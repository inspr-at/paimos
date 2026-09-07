// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package baselinebatch

import (
	"encoding/base64"
	"strings"
	"testing"
)

// aithemaUnicodeHandover is the byte-exact export produced by real Aithema
// lib/digest.js at a8c21e5 for a baseline whose refs separate UTF-16 order from
// UTF-8 order (U+1F642 vs U+F8FF) and whose acceptance criteria embed U+2028 and
// U+2029. Regenerate with:
//
//	node --input-type=module -e 'import {contentDigest,revisionSeal} from
//	  "file:///…/aithema/lib/digest.js"; …'
const aithemaUnicodeHandover = "eyJoYW5kb3Zlcl92ZXJzaW9uIjoiYWl0aGVtYS5oYW5kb3Zlci8wLjEiLCJzdHJlYW1fcmVmIjoic3RyZWFtOnVuaWNvZGUiLCJleHBvcnRlZF9hdCI6IjIwMjYtMDktMDdUMTI6MDA6MDAuMDAwWiIsImJhc2VsaW5lIjp7ImJhc2VsaW5lX3JlZiI6ImJhc2VsaW5lOnVuaWNvZGUiLCJyZXZpc2lvbiI6MiwiY29udGVudF9kaWdlc3QiOiJzaGEyNTY6N2U3ODMyZDZhZTQyNWE3NDQ5MWI2MzFjNTdmYWI2NWI2ZTQyZjQ5ZTFiMTA5ZWVjOWFmNGI2ODY1NzQ3MTZiNiIsInJldmlzaW9uX3NlYWwiOiJzaGEyNTY6MjViMzU2Y2FhNTgxODFhMzg0ZjhjYjM4ZDk0MGQ0ZjE3NzlhMWNjZmYyNjAyZDE4Yjc4NzkyMjllYjMwYzgyNCIsImFwcHJvdmVkX2J5IjoicGFydHk6dXBzdHJlYW0tY2xhaW0iLCJhcHByb3ZlZF9hdCI6IjIwMjYtMDktMDdUMTE6MDA6MDAuMDAwWiIsInJlcXVpcmVtZW50cyI6W3sicmVxdWlyZW1lbnRfcmVmIjoicmVxLu+jvy5wcml2YXRlIiwic3RhdGVtZW50IjoiUHJpdmF0ZS11c2UgcmVmIHNvcnRzIGFmdGVyIHRoZSBzdXBwbGVtZW50YXJ5IHBsYW5lIGluIFVURi0xNi4iLCJhY2NlcHRhbmNlX2NyaXRlcmlhIjpbIkxpbmUgb25l4oCobGluZSB0d28iLCJQYXJhIG9uZeKAqXBhcmEgdHdvIl0sImNvbnN0cmFpbnRfcmVmcyI6W119LHsicmVxdWlyZW1lbnRfcmVmIjoicmVxLvCfmYIuc21pbGUiLCJzdGF0ZW1lbnQiOiJTdXBwbGVtZW50YXJ5LXBsYW5lIHJlZiDwn5mCIHN0YXlzIGF1dGhlbnRpYy4iLCJhY2NlcHRhbmNlX2NyaXRlcmlhIjpbIu+jvyBhZnRlciDwn5mCIiwicGxhaW4iXSwiY29uc3RyYWludF9yZWZzIjpbImNvbi7wn5mCIiwiY29uLu+jvyJdfV0sImNvbnN0cmFpbnRzIjpbeyJjb25zdHJhaW50X3JlZiI6ImNvbi7vo78iLCJraW5kIjoidGVjaG5pY2FsIiwic3RhdGVtZW50IjoiUHJpdmF0ZS11c2UgY29uc3RyYWludC4ifSx7ImNvbnN0cmFpbnRfcmVmIjoiY29uLvCfmYIiLCJraW5kIjoiYWdyZWVtZW50Iiwic3RhdGVtZW50IjoiU3VwcGxlbWVudGFyeSBjb25zdHJhaW50LiJ9XX0sInBlbmRpbmdfcHJvcG9zYWxzIjpbXSwiZGVjaXNpb25zIjpbXX0="

const (
	aithemaUnicodeDigest = "sha256:7e7832d6ae425a74491b631c57fab65b6e42f49e1b109eec9af4b686574716b6"
	aithemaUnicodeSeal   = "sha256:25b356caa58181a384f8cb38d940d4f1779a1ccff2602d18b7879229eb30c824"
)

func aithemaUnicodeBytes(t *testing.T) []byte {
	t.Helper()
	raw, err := base64.StdEncoding.DecodeString(aithemaUnicodeHandover)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestContentDigestMatchesAithemaCanonicalPayload(t *testing.T) {
	reqs := []Requirement{{
		Ref:                "requirement_advisory",
		Statement:          "Document the agreed delivery approach for this engagement.",
		AcceptanceCriteria: []string{"The written approach names the authorized scope."},
		ConstraintRefs:     []string{},
	}}
	digest, err := ContentDigest(reqs, nil)
	if err != nil {
		t.Fatal(err)
	}
	if digest != "sha256:16ae0941380964ae9267b0d9bad1df788bbdd71dc83e6e46856a284716c01eea" {
		t.Fatalf("content_digest=%s", digest)
	}
	seal, err := RevisionSeal("baseline_advisory", 1, digest)
	if err != nil {
		t.Fatal(err)
	}
	if seal != "sha256:07c6c40c326be33348be4f8d4f6575a11b065980f61728bb5422ebe830b45830" {
		t.Fatalf("revision_seal=%s", seal)
	}
}

// The rejected implementation sorted bytewise and escaped U+2028/U+2029, so it
// rejected this authentic export as tampered. Both halves are asserted here:
// the recomputed identity must equal Aithema's, and the exact exported bytes
// must import.
func TestContentDigestMatchesAithemaForSupplementaryAndLineSeparators(t *testing.T) {
	reqs := []Requirement{
		{
			Ref:                "req.\uf8ff.private",
			Statement:          "Private-use ref sorts after the supplementary plane in UTF-16.",
			AcceptanceCriteria: []string{"Line one\u2028line two", "Para one\u2029para two"},
			ConstraintRefs:     []string{},
		},
		{
			Ref:                "req.\U0001f642.smile",
			Statement:          "Supplementary-plane ref \U0001f642 stays authentic.",
			AcceptanceCriteria: []string{"\uf8ff after \U0001f642", "plain"},
			ConstraintRefs:     []string{"con.\U0001f642", "con.\uf8ff"},
		},
	}
	cons := []Constraint{
		{Ref: "con.\uf8ff", Kind: "technical", Statement: "Private-use constraint."},
		{Ref: "con.\U0001f642", Kind: "agreement", Statement: "Supplementary constraint."},
	}
	digest, err := ContentDigest(reqs, cons)
	if err != nil {
		t.Fatal(err)
	}
	if digest != aithemaUnicodeDigest {
		t.Fatalf("content_digest=%s want %s", digest, aithemaUnicodeDigest)
	}
	seal, err := RevisionSeal("baseline:unicode", 2, digest)
	if err != nil {
		t.Fatal(err)
	}
	if seal != aithemaUnicodeSeal {
		t.Fatalf("revision_seal=%s want %s", seal, aithemaUnicodeSeal)
	}
	parsed, err := parseHandover(aithemaUnicodeBytes(t))
	if err != nil {
		t.Fatalf("authentic Aithema export rejected: %v", err)
	}
	if parsed.Baseline.ContentDigest != aithemaUnicodeDigest || parsed.Baseline.RevisionSeal != aithemaUnicodeSeal {
		t.Fatal("imported baseline identity does not match the exported identity")
	}
	if parsed.Baseline.Requirements[0].Ref != "req.\uf8ff.private" {
		t.Fatalf("import order changed: %q", parsed.Baseline.Requirements[0].Ref)
	}
}

func TestParseHandoverRejectsTamperedUnicodeContent(t *testing.T) {
	raw := string(aithemaUnicodeBytes(t))
	// One line separator swapped for a paragraph separator: same length, same
	// seal, different canonical content.
	tampered := strings.Replace(raw, "Line one\u2028line two", "Line one\u2029line two", 1)
	if tampered == raw {
		t.Fatal("fixture does not contain the expected separator")
	}
	if _, err := parseHandover([]byte(tampered)); err == nil {
		t.Fatal("tampered content_digest accepted")
	}
}

func TestCompactJSONMatchesJavaScriptStringifyEscapes(t *testing.T) {
	raw, err := compactJSON(map[string]string{"v": "a\u2028b\u2029c<&>\b\f\n\"\\"})
	if err != nil {
		t.Fatal(err)
	}
	// JSON.stringify('a\u2028b\u2029c<&>\b\f\n"\\') in node.
	const want = "{\"v\":\"a\u2028b\u2029c<&>\\b\\f\\n\\\"\\\\\"}"
	if string(raw) != want {
		t.Fatalf("compactJSON=%q want %q", raw, want)
	}
	literal, err := compactJSON(map[string]string{"v": `\u2028`})
	if err != nil {
		t.Fatal(err)
	}
	if string(literal) != `{"v":"\\u2028"}` {
		t.Fatalf("escaped backslash rewritten: %q", literal)
	}
}

func TestParseHandoverRejectsTamperedSealAndSecrets(t *testing.T) {
	reqs := []Requirement{{
		Ref:                "req.login",
		Statement:          "Users sign in",
		AcceptanceCriteria: []string{"Magic links expire"},
		ConstraintRefs:     []string{},
	}}
	digest, _ := ContentDigest(reqs, nil)
	seal, _ := RevisionSeal("baseline:v1", 1, digest)
	good, _ := compactJSON(map[string]any{
		"handover_version": HandoverVersion,
		"stream_ref":       "stream:export",
		"exported_at":      "2026-09-07T11:05:00.000Z",
		"baseline": map[string]any{
			"baseline_ref":   "baseline:v1",
			"revision":       1,
			"content_digest": digest,
			"revision_seal":  seal,
			"approved_by":    "party:forged",
			"approved_at":    "2026-09-07T11:00:00.000Z",
			"requirements":   reqs,
			"constraints":    []Constraint{},
		},
		"pending_proposals": []any{},
		"decisions":         []any{},
	})
	parsed, err := parseHandover(good)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Baseline.ApprovedBy != "party:forged" {
		t.Fatal("imported claim dropped")
	}
	tampered, _ := compactJSON(map[string]any{
		"handover_version": HandoverVersion,
		"stream_ref":       "stream:export",
		"exported_at":      "2026-09-07T11:05:00.000Z",
		"baseline": map[string]any{
			"baseline_ref":   "baseline:v1",
			"revision":       1,
			"content_digest": digest,
			"revision_seal":  "sha256:0000000000000000000000000000000000000000000000000000000000000000",
			"approved_by":    "party:forged",
			"approved_at":    "2026-09-07T11:00:00.000Z",
			"requirements":   reqs,
			"constraints":    []Constraint{},
		},
		"pending_proposals": []any{},
		"decisions":         []any{},
	})
	if _, err := parseHandover(tampered); err == nil {
		t.Fatal("tampered seal accepted")
	}
	withReady, _ := compactJSON(map[string]any{
		"handover_version": HandoverVersion,
		"stream_ref":       "stream:export",
		"exported_at":      "2026-09-07T11:05:00.000Z",
		"ready":            true,
		"baseline": map[string]any{
			"baseline_ref":   "baseline:v1",
			"revision":       1,
			"content_digest": digest,
			"revision_seal":  seal,
			"approved_by":    "party:forged",
			"approved_at":    "2026-09-07T11:00:00.000Z",
			"requirements":   reqs,
			"constraints":    []Constraint{},
		},
		"pending_proposals": []any{},
		"decisions":         []any{},
	})
	if _, err := parseHandover(withReady); err == nil {
		t.Fatal("imported ready:true accepted as authority")
	}
}

func TestForbiddenImportKeys(t *testing.T) {
	raw, err := compactJSON(map[string]any{
		"handover_version": HandoverVersion,
		"stream_ref":       "stream:export",
		"exported_at":      "2026-09-07T11:05:00.000Z",
		"command":          "rm -rf /",
		"baseline": map[string]any{
			"baseline_ref": "baseline:v1",
			"revision":     1,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := parseHandover(raw); err == nil {
		t.Fatal("command key accepted")
	}
}
