// SPDX-License-Identifier: AGPL-3.0-only

package harness_test

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"testing"

	"github.com/inspr-at/paimos/internal/rules"
	"github.com/jackc/pgx/v5"
)

// Republishing a lower-version set that does not change the rendered bytes
// keeps the merged version, because that version is only the maximum
// constituent. The receipt must not adopt that new set.
func TestReceiptOmitsSetsWhenALowerVersionSetLeavesTheBytesUnchanged(t *testing.T) {
	const sentence = "synthetic-floor-sentence-must-not-leak"
	f, session, lease, in := receiptFixture(t)
	rules.New(f.db.App).Mount(f.mux)
	w := f.call(f.person, "POST", "/api/rules/layers", map[string]any{"layer": "company"}, "")
	expect(t, w, 200)
	layer := decode(t, w)["id"].(string)
	high := publishSet(t, f, layer, "Safety", "260929120000.0.0", rules.Rule{
		Identity: "synthetic-floor", Text: sentence, Why: "The floor stays out of provenance.",
		Strength: "locked", Enabled: true, Source: rules.Source{Reference: "AEON-219"},
	})
	q := url.Values{
		"project_id": {in.Context.ProjectID}, "person_id": {in.Context.PersonID}, "agent_id": {in.Context.AgentID},
		"role": {in.Context.Role}, "harness": {in.Context.Harness}, "task_id": {in.Context.TaskID},
	}
	first := getMerged(t, f, q)
	if len(first.Versions) != 1 || first.Versions[0].SetID != high || first.Version != "260929120000.0.0" {
		t.Fatalf("first serve %#v", first.Versions)
	}
	in.BodySHA256, in.Version, in.ByteSize = first.SHA256, first.Version, &first.ByteSize
	receiptResponse(t, f.call(f.agent, "POST", session+"/rules-receipts", in, lease), false)
	served := provenanceNames(t, f, session, 0)
	if served["merged-rules"] != first.Version || served[high] != first.Version {
		t.Fatalf("served manifest was not recorded: %#v", served)
	}

	low := publishSet(t, f, layer, "Quiet", "260929110000.0.0", rules.Rule{
		Identity: "quiet-note", Text: "disabled text must not change the rendered bytes",
		Why: "A disabled rule is outside the rendered body.", Strength: "normal", Enabled: false,
		Source: rules.Source{Reference: "AEON-219"},
	})
	again := getMerged(t, f, q)
	if again.SHA256 != first.SHA256 || again.Version != first.Version || again.ByteSize != first.ByteSize || len(again.Versions) != 2 {
		t.Fatalf("republish changed the rendered identity or omitted the lower set: %#v version %s", again.Versions, again.Version)
	}
	var sawLow bool
	for _, set := range again.Versions {
		if set.SetID == low && set.Version == "260929110000.0.0" {
			sawLow = true
		}
	}
	if !sawLow {
		t.Fatal("current merge did not include the lower-version set")
	}
	rev := int64(1)
	in.RequestID, in.ExpectedRevision = uid(), &rev
	receiptResponse(t, f.call(f.agent, "POST", session+"/rules-receipts", in, lease), false)
	latest := provenanceNames(t, f, session, 0)
	if latest["merged-rules"] != first.Version || latest[low] != "" || latest[high] != "" {
		t.Fatalf("ambiguous serve attributed a set: %#v", latest)
	}
	original := provenanceNames(t, f, session, 1)
	if original[high] != first.Version || original[low] != "" {
		t.Fatalf("first revision lost the served set: %#v", original)
	}
}

func TestReceiptRecordsSixtyFourServedSetsWithoutRollback(t *testing.T) {
	f, session, lease, in := receiptFixture(t)
	sets := make([]map[string]string, 64)
	for i := range sets {
		sets[i] = map[string]string{
			"set_id":  fmt.Sprintf("00000000-0000-4000-8000-%012x", i+1),
			"version": "260929100000.0.0",
			"sha256":  strings.Repeat("ab", 32),
		}
	}
	raw, err := json.Marshal(sets)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(raw)
	f.tx(t, f.person, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `INSERT INTO rule_served_manifests(
			tenant_id, project_id, person_id, agent_id, task_id, role, harness, version, body_sha256, byte_size, sets_digest, sets)
			VALUES($1,$2,$3,$4,$5,'builder','codex',$6,$7,$8,$9,$10::jsonb)`,
			f.person.TenantID, f.project, f.person.ID, f.agent.ID, f.ticket, in.Version, in.BodySHA256, *in.ByteSize, sum[:], string(raw))
		return err
	})
	receiptResponse(t, f.call(f.agent, "POST", session+"/rules-receipts", in, lease), false)
	names := provenanceNames(t, f, session, 0)
	if names["merged-rules"] != in.Version {
		t.Fatalf("merged identity lost: %#v", names)
	}
	setsN := 0
	for name := range names {
		if name != "merged-rules" {
			setsN++
		}
	}
	if setsN != 64 {
		t.Fatalf("stored %d sets", setsN)
	}
}

func publishSet(t *testing.T, f *harnessFixture, layer, name, version string, rule rules.Rule) string {
	t.Helper()
	w := f.call(f.person, "POST", "/api/rules/sets", map[string]any{"layer_id": layer, "name": name}, "")
	expect(t, w, 200)
	setID := decode(t, w)["id"].(string)
	expect(t, f.call(f.person, "PUT", "/api/rules/sets/"+setID+"/draft", map[string]any{
		"expected_revision": 1, "name": name, "rules": []rules.Rule{rule},
	}, ""), 200)
	expect(t, f.call(f.person, "POST", "/api/rules/sets/"+setID+"/publish", map[string]any{
		"expected_revision": 2, "version": version,
	}, ""), 200)
	return setID
}

func getMerged(t *testing.T, f *harnessFixture, q url.Values) rules.Merged {
	t.Helper()
	w := f.call(f.person, "GET", "/api/rules/merged?"+q.Encode(), nil, "")
	expect(t, w, 200)
	var merged rules.Merged
	if err := json.Unmarshal(w.Body.Bytes(), &merged); err != nil {
		t.Fatal(err)
	}
	return merged
}

func provenanceNames(t *testing.T, f *harnessFixture, session string, revisionIndex int) map[string]string {
	t.Helper()
	w := f.call(f.person, "GET", session+"/provenance", nil, "")
	expect(t, w, 200)
	revisions := decode(t, w)["revisions"].([]any)
	if revisionIndex >= len(revisions) {
		t.Fatalf("revision index %d of %d", revisionIndex, len(revisions))
	}
	out := map[string]string{}
	for _, raw := range revisions[revisionIndex].(map[string]any)["items"].([]any) {
		item := raw.(map[string]any)
		version, _ := item["version"].(string)
		out[item["logical_name"].(string)] = version
	}
	return out
}
