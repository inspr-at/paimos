// SPDX-License-Identifier: AGPL-3.0-only

package cli

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/inspr-at/paimos/internal/client"
	"github.com/inspr-at/paimos/internal/rulesimport"
)

const importSetID = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
const importProjectID = "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"
const importFixture = "## Workflow\n<!-- aeon-rule: workflow.check -->\n- Keep the scoped checks.\n  Why: changes need evidence.\n  Details: Full detail.\n  roles: builder\n  harnesses: codex, claude-code\n  expires: 2027-01-01T12:30:00+02:00\n  source: AEON-250\n"

func runImportCommand(t *testing.T, args []string, out io.Writer, connect func() (*client.Client, error)) error {
	t.Helper()
	rt := &runtime{program: "aeon", stdout: out, stderr: io.Discard}
	cmd := rt.rulesImportCommand(connect)
	pos, err := rt.parse(cmd, args)
	if err != nil {
		return err
	}
	if err = cmd.checkArgs(pos); err != nil {
		return err
	}
	return cmd.run(pos)
}

func importFixturePath(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "AGENTS.md")
	if err := os.WriteFile(path, []byte(importFixture), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestRulesImportPreviewOfflineAndForbiddenFlags(t *testing.T) {
	var requests, connections int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { requests++; w.WriteHeader(500) }))
	defer server.Close()
	connect := func() (*client.Client, error) {
		connections++
		return client.New(server.URL, "synthetic-test-token"), nil
	}
	path := importFixturePath(t)
	args := []string{"--context", "project", "--file", path, "--set", importSetID, "--revision", "7"}
	var out bytes.Buffer
	if err := runImportCommand(t, args, &out, connect); err != nil {
		t.Fatal(err)
	}
	var p rulesimport.Proposal
	if err := json.Unmarshal(out.Bytes(), &p); err != nil {
		t.Fatal(err)
	}
	if p.Mode != "preview" || !p.Adapter.Ready || requests != 0 || connections != 0 {
		t.Fatal("preview accessed API or credentials")
	}
	for _, flag := range []string{"--publish", "--restore", "--authorized", "--tenant"} {
		if err := runImportCommand(t, append(args, flag), io.Discard, connect); err == nil {
			t.Fatalf("accepted %s", flag)
		}
	}
	if err := runImportCommand(t, []string{"--apply", "--context", "project", "--file", path}, io.Discard, connect); err == nil {
		t.Fatal("apply accepted without target revision")
	}
	if requests != 0 || connections != 0 {
		t.Fatal("refusal accessed API")
	}
}

func TestRulesImportHTTPDraftMappingAndFailures(t *testing.T) {
	for _, scenario := range []string{"authorized", "get403", "put403", "put409", "stale revision", "wrong layer", "redirect", "unchanged", "identity conflict", "hash update"} {
		t.Run(scenario, func(t *testing.T) {
			path := importFixturePath(t)
			base := []string{"--context", "project", "--file", path}
			var preview bytes.Buffer
			if err := runImportCommand(t, base, &preview, nil); err != nil {
				t.Fatal(err)
			}
			var plan rulesimport.Proposal
			if err := json.Unmarshal(preview.Bytes(), &plan); err != nil {
				t.Fatal(err)
			}
			var calls []string
			existing := rulesimport.DraftRule{Identity: "existing-rule", Text: "Retain this unrelated rule.", Why: "Manual edit.", Strength: "locked", Enabled: true, Source: rulesimport.DraftSource{Reference: "AEON-248", EditedHere: true}}
			current := rulesimport.DraftSet{ID: importSetID, LayerID: "cccccccc-cccc-4ccc-8ccc-cccccccccccc", Scope: rulesimport.DraftScope{Layer: rulesimport.LayerProject, ProjectID: importProjectID}, Name: "Existing draft", Revision: 7, Rules: []rulesimport.DraftRule{existing}, PublishedVersion: "260927120000.0.0"}
			if scenario == "stale revision" {
				current.Revision = 8
			}
			if scenario == "wrong layer" {
				current.Scope.Layer = rulesimport.LayerCompany
			}
			if scenario == "unchanged" || scenario == "identity conflict" || scenario == "hash update" {
				mapped, err := rulesimport.MapDraft(plan)
				if err != nil {
					t.Fatal(err)
				}
				if scenario == "identity conflict" || scenario == "hash update" {
					mapped[0].Why = "Locally edited on server."
				}
				if scenario == "identity conflict" {
					mapped[0].Source.EditedHere = true
				}
				current.Rules = append(current.Rules, mapped...)
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls = append(calls, r.Method+" "+r.URL.Path)
				if r.Header.Get("Authorization") != "Bearer synthetic-test-token" {
					t.Error("configured client authorization missing")
					w.WriteHeader(403)
					return
				}
				if strings.Contains(r.URL.Path, "publish") || strings.Contains(r.URL.Path, "restore") {
					t.Error("publication/restore reached")
					w.WriteHeader(500)
					return
				}
				if r.Method == http.MethodGet && r.URL.Path == "/api/rules/sets/"+importSetID {
					if scenario == "get403" {
						w.WriteHeader(403)
						io.WriteString(w, `{"error":"SERVER-PRIVATE-SENTINEL"}`)
						return
					}
					if scenario == "redirect" {
						http.Redirect(w, r, "/api/rules/sets/"+importSetID+"/publish", http.StatusTemporaryRedirect)
						return
					}
					json.NewEncoder(w).Encode(current)
					return
				}
				if r.Method != http.MethodPut || r.URL.Path != "/api/rules/sets/"+importSetID+"/draft" {
					t.Error("unexpected operation")
					w.WriteHeader(500)
					return
				}
				var fields map[string]json.RawMessage
				if err := json.NewDecoder(r.Body).Decode(&fields); err != nil {
					t.Error(err)
					return
				}
				if len(fields) != 3 || fields["expected_revision"] == nil || fields["name"] == nil || fields["rules"] == nil {
					t.Error("wrong draft body fields")
				}
				raw, _ := json.Marshal(fields)
				var body rulesimport.DraftBody
				if err := json.Unmarshal(raw, &body); err != nil {
					t.Error(err)
					return
				}
				if body.ExpectedRevision != 7 || body.Name != "Existing draft" || len(body.Rules) != 2 {
					t.Error("revision/name/rule count mapping differs")
					return
				}
				if !reflect.DeepEqual(body.Rules[0], existing) {
					t.Error("existing rule changed")
				}
				imported := body.Rules[1]
				sourceHash := sha256.Sum256([]byte(importFixture))
				identityHash := sha256.Sum256([]byte("project/workflow/builder/workflow.check"))
				if imported.Identity != "import-"+hex.EncodeToString(identityHash[:]) || imported.Text != "Keep the scoped checks." || imported.Why != "changes need evidence." || imported.Strength != "normal" || !imported.Enabled || imported.ExpiresAt == nil || imported.ExpiresAt.Format("2006-01-02T15:04:05Z07:00") != "2027-01-01T12:30:00+02:00" || strings.Join(imported.Roles, ",") != "builder" || strings.Join(imported.Harnesses, ",") != "claude-code,codex" {
					t.Error("exact rule mapping differs")
				}
				if imported.Source.Reference != "AEON-250" || imported.Source.Revision != hex.EncodeToString(sourceHash[:]) || imported.Source.Identity != "workflow.check" || imported.Source.EditedHere {
					t.Error("source mapping differs")
				}
				if !strings.HasPrefix(imported.Details, "Full detail.\n\n[aeon doctrine lineage]\n") || !strings.Contains(imported.Details, `"start_line":2`) || !strings.Contains(imported.Details, `"end_line":9`) || !strings.Contains(imported.Details, hex.EncodeToString(sourceHash[:])) || !strings.Contains(imported.Details, path) {
					t.Error("full source lineage lost")
				}
				if scenario == "put403" {
					w.WriteHeader(403)
					io.WriteString(w, `{"error":"SERVER-PRIVATE-SENTINEL"}`)
					return
				}
				if scenario == "put409" {
					w.WriteHeader(409)
					io.WriteString(w, `{"code":"revision_conflict","error":"SERVER-PRIVATE-SENTINEL"}`)
					return
				}
				current.Rules = body.Rules
				current.Revision++
				json.NewEncoder(w).Encode(current)
			}))
			defer server.Close()
			c := client.New(server.URL, "synthetic-test-token")
			var out bytes.Buffer
			err := runImportCommand(t, append(base, "--apply", "--set", importSetID, "--revision", "7"), &out, func() (*client.Client, error) { return c, nil })
			dec := json.NewDecoder(&out)
			var retained rulesimport.Proposal
			if e := dec.Decode(&retained); e != nil {
				t.Fatal(e)
			}
			if !reflect.DeepEqual(plan, retained) {
				t.Fatal("apply/error changed local proposal")
			}
			if scenario == "authorized" || scenario == "unchanged" || scenario == "hash update" {
				if err != nil {
					t.Fatal(err)
				}
				var receipt rulesimport.DraftResult
				if e := dec.Decode(&receipt); e != nil {
					t.Fatal(e)
				}
				expectedRevision := int64(8)
				expectedMode := "draft"
				if scenario == "unchanged" {
					expectedRevision = 7
					expectedMode = "unchanged"
				}
				if receipt.Revision != expectedRevision || receipt.Mode != expectedMode || receipt.PlanID != plan.PlanID {
					t.Fatal("wrong draft receipt")
				}
				switch scenario {
				case "authorized":
					if receipt.Added != 1 || receipt.Updated != 0 || receipt.Unchanged != 0 {
						t.Fatalf("receipt counts %+v", receipt)
					}
				case "unchanged":
					if receipt.Added != 0 || receipt.Updated != 0 || receipt.Unchanged != 1 {
						t.Fatalf("receipt counts %+v", receipt)
					}
				case "hash update":
					if receipt.Added != 0 || receipt.Updated != 1 || receipt.Unchanged != 0 {
						t.Fatalf("receipt counts %+v", receipt)
					}
				}
			} else {
				if err == nil {
					t.Fatal("refusal succeeded")
				}
				if strings.Contains(err.Error(), "SERVER-PRIVATE-SENTINEL") {
					t.Fatal("server body leaked into error")
				}
				var receipt any
				if e := dec.Decode(&receipt); e != io.EOF {
					t.Fatal("failure printed a success receipt")
				}
				if scenario == "put403" || scenario == "get403" || scenario == "put409" {
					var se *client.StatusError
					want := 403
					if scenario == "put409" {
						want = 409
					}
					if !errors.As(err, &se) || se.Status != want {
						t.Fatal("API status was hidden")
					}
				}
			}
			expected := 1
			if scenario == "authorized" || scenario == "put403" || scenario == "put409" || scenario == "hash update" {
				expected = 2
			}
			if len(calls) != expected {
				t.Fatalf("unexpected requests/fallback/retry: %v", calls)
			}
		})
	}
}

func TestRulesImportReportAndLayerFlag(t *testing.T) {
	path := importFixturePath(t)
	dir := t.TempDir()
	var out bytes.Buffer
	if err := runImportCommand(t, []string{"--context", "project", "--file", path, "--layer", path + "=person", "--report", dir}, &out, nil); err != nil {
		t.Fatal(err)
	}
	var p rulesimport.Proposal
	if err := json.Unmarshal(out.Bytes(), &p); err != nil {
		t.Fatal(err)
	}
	if len(p.Rules) != 1 || p.Rules[0].Layer != rulesimport.LayerPerson || !strings.HasPrefix(p.Rules[0].Identity, "person/") {
		t.Fatalf("layer flag %+v", p.Rules)
	}
	if p.Rules[0].Sources[0].HeadingPath != "Workflow" {
		t.Fatalf("heading %q", p.Rules[0].Sources[0].HeadingPath)
	}
	if _, err := os.Stat(filepath.Join(dir, "contradictions.md")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "contradictions.json")); err != nil {
		t.Fatal(err)
	}
	if err := runImportCommand(t, []string{"--context", "project", "--file", path, "--layer", path + "=workspace"}, io.Discard, nil); err == nil {
		t.Fatal("unknown layer accepted")
	}
}
