// SPDX-License-Identifier: AGPL-3.0-only

package rules

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/inspr-at/paimos/internal/tenant"
)

func TestRulePublicationCredentialFields(t *testing.T) {
	// Each form must be refused in every snapshot field before storage.
	for form, value := range publicationCredentialForms() {
		for name, change := range map[string]func(*Set, *string){
			"name":             func(s *Set, _ *string) { s.Name = value },
			"note":             func(_ *Set, note *string) { *note = value },
			"set-en":           func(s *Set, _ *string) { s.TLDR.EN = value },
			"set-de":           func(s *Set, _ *string) { s.TLDR.DE = value },
			"text":             func(s *Set, _ *string) { s.Rules[0].Text = value },
			"why":              func(s *Set, _ *string) { s.Rules[0].Why = value },
			"details":          func(s *Set, _ *string) { s.Rules[0].Details = value },
			"source-reference": func(s *Set, _ *string) { s.Rules[0].Source.Reference = value },
			"source-revision":  func(s *Set, _ *string) { s.Rules[0].Source.Revision = value },
			"source-identity":  func(s *Set, _ *string) { s.Rules[0].Source.Identity = value },
			"rule-en":          func(s *Set, _ *string) { s.Rules[0].TLDR.EN = value },
			"rule-de":          func(s *Set, _ *string) { s.Rules[0].TLDR.DE = value },
			"disabled":         func(s *Set, _ *string) { s.Rules[0].Enabled = false; s.Rules[0].Text = value },
		} {
			t.Run(form+"/"+name, func(t *testing.T) {
				s := Set{Name: "Rules", Revision: 1, Rules: []Rule{testRule("validation", "Run tests.")}, TLDR: &TLDR{EN: "Validation rules."}}
				s.Rules[0].TLDR = &TLDR{EN: "Validate before merging."}
				note := "Clarify validation."
				if err := guardPublicationCredentials(s, note); err != nil {
					t.Fatal("benign control refused")
				}
				change(&s, &note)
				// A nil transaction proves refusal precedes database reads/writes at
				// the boundary used by single, batch and restored publication.
				_, err := publishSetIn(t.Context(), nil, tenant.Principal{}, s, 1, "261002120000.0.0", note, "")
				var refusal *Error
				if !errors.As(err, &refusal) || refusal.Status != 422 || refusal.Code != "credential_text" || refusal.Message != "This publication contains credential-shaped text. Remove credentials before publishing rules." {
					t.Fatal("expected fixed credential refusal before storage")
				}
			})
		}
	}
}

func TestRulePublicationCredentialsRollback(t *testing.T) {
	for form, value := range publicationCredentialForms() {
		t.Run(form, func(t *testing.T) { testRulePublicationCredentialsRollback(t, value) })
	}
}

func publicationCredentialForms() map[string]string {
	return map[string]string{
		"raw":        "password=" + strings.Repeat("x", 5),
		"provider":   "sk-proj-" + strings.Repeat("Ab7q", 8),
		"fullwidth":  "ＡＫＩＡ" + strings.Repeat("ＡＢ１２", 4),
		"format":     strings.Join(strings.Split("AK"+"IA"+strings.Repeat("AB12", 4), ""), "\u200b"),
		"confusable": "AK" + "IA" + strings.Repeat("A\ua7b412", 4),
	}
}

func testRulePublicationCredentialsRollback(t *testing.T, value string) {
	w := newBatchWorld(t, "credential-publication")
	owner := w.principal(tenant.Person, "owner", "owner")
	layer := w.layer(owner, Scope{Layer: "person", OwnerID: owner.ID})
	clean := w.set(owner, layer, "Clean", testRule("clean", "Run tests before merging."))
	dirty := w.set(owner, layer, "Dirty", testRule("dirty", value))
	assertRefused := func(path string, body any) {
		t.Helper()
		status, raw := w.send(owner, "POST", path, body)
		var refusal Error
		if status != 422 || json.Unmarshal(raw, &refusal) != nil || refusal.Code != "credential_text" || refusal.Message != "This publication contains credential-shaped text. Remove credentials before publishing rules." {
			t.Fatal("publication must return only the fixed credential refusal")
		}
	}
	version := "261002120000.0.0"
	assertRefused("/api/rules/sets/"+dirty.ID+"/publish", map[string]any{"expected_revision": dirty.Revision, "version": version})
	assertRefused("/api/rules/publish", batchInput{Items: []batchItem{{SetID: clean.ID, ExpectedRevision: clean.Revision, Version: version}, {SetID: dirty.ID, ExpectedRevision: dirty.Revision, Version: version}}})
	if w.published(clean.ID) != "" || w.published(dirty.ID) != "" || w.events("") != 0 {
		t.Fatal("refused batch left a published set or audit event")
	}
	w.publish(owner, clean, version)
	before := w.call(owner, "GET", "/api/rules/sets/"+clean.ID, nil, 200)
	assertRefused("/api/rules/sets/"+clean.ID+"/restore", map[string]any{"expected_revision": clean.Revision, "version": version, "new_version": "261002120001.0.0", "note": value})
	after := w.call(owner, "GET", "/api/rules/sets/"+clean.ID, nil, 200)
	if string(before) != string(after) || w.events("") != 1 {
		t.Fatal("refused restoration modified the draft or its publication")
	}
}
