// SPDX-License-Identifier: AGPL-3.0-only
package cli

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/inspr-at/paimos/internal/client"
	"github.com/inspr-at/paimos/internal/rules"
)

func runTLDRCommand(t *testing.T, args []string, out io.Writer, connect func() (*client.Client, error)) error {
	t.Helper()
	rt := &runtime{program: "aeon", stdout: out, stderr: io.Discard}
	cmd := rt.rulesTLDRCommand(connect)
	pos, err := rt.parse(cmd, args)
	if err != nil {
		return err
	}
	if err = cmd.checkArgs(pos); err != nil {
		return err
	}
	return cmd.run(pos)
}

func tldrFixture(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "tldrs.json")
	if err := os.WriteFile(path, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestRulesTLDRDryRunApplyAndTemplate(t *testing.T) {
	rule := func(id, text string) rules.Rule {
		return rules.Rule{Identity: id, Text: text, Why: "w", Strength: "normal", Enabled: true, Source: rules.Source{Reference: "AEON-314"}}
	}
	current := rules.Set{ID: importSetID, LayerID: importProjectID, Scope: rules.Scope{Layer: "company"}, Name: "Git", Revision: 4, Rules: []rules.Rule{rule("git.force", "Never force-push main."), rule("git.sign", "Sign commits.")}}
	var puts []rules.TLDRInput
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/rules/sets/"+importSetID:
			json.NewEncoder(w).Encode(current)
		case r.Method == http.MethodPut && r.URL.Path == "/api/rules/sets/"+importSetID+"/tldr":
			var in rules.TLDRInput
			dec := json.NewDecoder(r.Body)
			dec.DisallowUnknownFields()
			if err := dec.Decode(&in); err != nil {
				t.Error(err)
				w.WriteHeader(400)
				return
			}
			puts = append(puts, in)
			next, _, err := rules.ApplyTLDRs(current, in)
			if err != nil {
				t.Error(err)
				w.WriteHeader(400)
				return
			}
			next.Revision++
			current = next
			json.NewEncoder(w).Encode(current)
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
			w.WriteHeader(500)
		}
	}))
	defer server.Close()
	connect := func() (*client.Client, error) { return client.New(server.URL, "synthetic-test-token"), nil }
	file := tldrFixture(t, `{"set":{"en":"Keeps shared history safe.","de":"Schützt die gemeinsame Historie."},"rules":{"git.force":{"text":"Never force-push main.","en":"Rewriting main breaks everyone's clones."},"git.sign":{"text":"Sign commits.","en":"","de":""}}}`)

	var out bytes.Buffer
	if err := runTLDRCommand(t, []string{"--set", importSetID, "--revision", "4", "--file", file}, &out, connect); err != nil {
		t.Fatal(err)
	}
	var plan tldrReport
	json.Unmarshal(out.Bytes(), &plan)
	if plan.Mode != "dry-run" || plan.Set != "added" || strings.Join(plan.Added, ",") != "git.force" || strings.Join(plan.Missing, ",") != "git.sign" || len(puts) != 0 {
		t.Fatalf("dry run: %s", out.String())
	}
	out.Reset()
	if err := runTLDRCommand(t, []string{"--set", importSetID, "--revision", "4", "--file", file, "--apply"}, &out, connect); err != nil {
		t.Fatal(err)
	}
	json.Unmarshal(out.Bytes(), &plan)
	if plan.Mode != "draft" || plan.Revision != 5 || len(puts) != 1 || puts[0].ExpectedRevision != 4 || puts[0].Rules["git.force"] == nil || len(puts[0].Rules) != 1 {
		t.Fatalf("apply: %s %+v", out.String(), puts)
	}
	if current.Rules[0].TLDR == nil || current.Rules[0].Text != "Never force-push main." || current.TLDR.DE == "" {
		t.Fatalf("explanations only: %+v", current)
	}
	// Applying the same file again changes nothing.
	out.Reset()
	if err := runTLDRCommand(t, []string{"--set", importSetID, "--revision", "5", "--file", file, "--apply"}, &out, connect); err != nil || !strings.Contains(out.String(), `"mode":"unchanged"`) || len(puts) != 1 {
		t.Fatalf("idempotent: %v %s", err, out.String())
	}
	// A stale revision and changed wording are refused before anything is sent.
	if err := runTLDRCommand(t, []string{"--set", importSetID, "--revision", "4", "--file", file, "--apply"}, io.Discard, connect); err == nil || !strings.Contains(err.Error(), "revision 5") {
		t.Fatalf("stale revision: %v", err)
	}
	moved := tldrFixture(t, `{"rules":{"git.force":{"text":"Old wording.","en":"x"}}}`)
	if err := runTLDRCommand(t, []string{"--set", importSetID, "--revision", "5", "--file", moved, "--apply"}, io.Discard, connect); err == nil || !strings.Contains(err.Error(), "git.force") {
		t.Fatalf("wording check: %v", err)
	}
	for _, bad := range []string{`{"rules":{"git.force":{"en":"x","why":"no"}}}`, `{}`, `{"rules":{}} {}`, `not json`} {
		if err := runTLDRCommand(t, []string{"--set", importSetID, "--revision", "5", "--file", tldrFixture(t, bad)}, io.Discard, connect); err == nil {
			t.Fatalf("accepted %s", bad)
		}
	}
	if len(puts) != 1 {
		t.Fatal("refusals must not write")
	}
	// The template round-trips: filled in, it is a valid file for the draft.
	out.Reset()
	if err := runTLDRCommand(t, []string{"--set", importSetID, "--template"}, &out, connect); err != nil {
		t.Fatal(err)
	}
	var tpl map[string]any
	if json.Unmarshal(out.Bytes(), &tpl) != nil || tpl["revision"] != float64(5) || !strings.Contains(out.String(), `"text":"Sign commits."`) || !strings.Contains(out.String(), "Rewriting main") {
		t.Fatalf("template: %s", out.String())
	}
	tpl["rules"].(map[string]any)["git.sign"].(map[string]any)["en"] = "Proves who wrote a commit."
	raw, _ := json.Marshal(tpl)
	out.Reset()
	if err := runTLDRCommand(t, []string{"--set", importSetID, "--revision", "5", "--file", tldrFixture(t, string(raw)), "--apply"}, &out, connect); err != nil {
		t.Fatal(err)
	}
	json.Unmarshal(out.Bytes(), &plan)
	if plan.Mode != "draft" || strings.Join(plan.Added, ",") != "git.sign" || len(plan.Missing) != 0 {
		t.Fatalf("template apply: %s", out.String())
	}
	if err := runTLDRCommand(t, []string{"--set", "not-a-uuid", "--template"}, io.Discard, connect); err == nil {
		t.Fatal("bad set id")
	}
}
