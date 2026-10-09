// SPDX-License-Identifier: AGPL-3.0-only
package modelregistry

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/inspr-at/paimos/internal/modelprefs"
)

// Risk: selecting German changes old clients' fields, a legacy English patch
// erases a translation, or a tenant-created kind inherits built-in copy by slug.
func TestDisplayLanguageWordsPreserveLegacyFieldsAndEdits(t *testing.T) {
	p, h := editorFixture(t)
	english := editorDecode[workKindPage](t, h.call(t, p, "GET", "/api/work-kinds", "", nil))
	german := editorDecode[workKindPage](t, h.call(t, p, "GET", "/api/work-kinds?lang=de", "", nil))
	var design workKind
	for i, kind := range english.Items {
		other := german.Items[i]
		if kind.ID != other.ID || kind.Label != other.Label || kind.Hint != other.Hint || !reflect.DeepEqual(kind.Examples, other.Examples) {
			t.Fatal("language selection changed legacy fields", kind, other)
		}
		if kind.WordsDe == nil || kind.DisplayWords == nil || other.DisplayWords == nil || !reflect.DeepEqual(other.DisplayWords, kind.WordsDe) || kind.DisplayWords.Label != kind.Label {
			t.Fatal("built-in words did not select a complete language", kind, other)
		}
		if kind.Slug == "design" {
			design = kind
		}
	}
	if design.ID == "" || design.WordsDe.Label != "UI-Design" {
		t.Fatal("missing migrated built-in design", design)
	}
	de := modelprefs.KindText{Label: "Gestaltung", Hint: "Eigene freigegebene Entwürfe.", Examples: []string{"Ein eigener Bildschirm"}}
	patch := editorJSON(map[string]any{"label": "Custom design", "hint": "User-edited English sentence.", "examples": []string{"A custom screen"}, "words_de": de})
	saved := editorDecode[workKind](t, h.call(t, p, "PATCH", "/api/work-kinds/"+design.ID, patch, nil))
	legacy := editorDecode[workKind](t, h.call(t, p, "PATCH", "/api/work-kinds/"+design.ID, `{"hint":"Updated by an older client."}`, nil))
	if saved.Label != "Custom design" || legacy.Hint != "Updated by an older client." || !reflect.DeepEqual(legacy.WordsDe, &de) {
		t.Fatal("patch lost independent wording", saved, legacy)
	}
	created := h.call(t, p, "POST", "/api/work-kinds", `{"label":"Data analysis","hint":"","examples":["A chart"]}`, nil)
	if created.Code != 201 {
		t.Fatal("create kind", created.Code, created.Body.String())
	}
	var custom workKind
	if err := json.Unmarshal(created.Body.Bytes(), &custom); err != nil {
		t.Fatal(err)
	}
	page := editorDecode[workKindPage](t, h.call(t, p, "GET", "/api/work-kinds?lang=de", "", nil))
	for _, kind := range page.Items {
		if kind.ID == design.ID && !reflect.DeepEqual(kind.DisplayWords, &de) {
			t.Fatal("German edit not used", kind)
		}
		if kind.ID == custom.ID && (kind.WordsDe != nil || kind.DisplayWords.Label != custom.Label) {
			t.Fatal("custom kind did not fall back as a whole", kind)
		}
	}
	for _, path := range []string{"/api/work-kinds?lang=fr", "/api/model-preferences/situations?lang=fr"} {
		editorError(t, h.call(t, p, "GET", path, "", nil), 400, "invalid_language")
	}
	editorError(t, h.call(t, p, "PATCH", "/api/work-kinds/"+design.ID, `{"words_de":{"label":"Name","hint":"Sentence","examples":["a","b","c","d"]}}`, nil), 422, "invalid_kind_translation")
	var situations struct {
		Words []modelprefs.SituationWords `json:"words"`
	}
	situations = editorDecode[struct {
		Words []modelprefs.SituationWords `json:"words"`
	}](t, h.call(t, p, "GET", "/api/model-preferences/situations?lang=de", "", nil))
	if len(situations.Words) != 6 {
		t.Fatal("missing bilingual situations", situations)
	}
	for _, word := range situations.Words {
		if word.WordsEn.Label == "" || word.WordsDe.Hint == "" || len(word.WordsDe.Examples) == 0 || !reflect.DeepEqual(word.DisplayWords, word.WordsDe) {
			t.Fatal("incomplete situation wording", word)
		}
	}
}
