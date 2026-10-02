// SPDX-License-Identifier: AGPL-3.0-only
package cli

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestRecurCLIContract(t *testing.T) {
	const id = "11111111-1111-1111-1111-111111111111"
	for _, tc := range []struct {
		args         []string
		method, path string
		body         map[string]any
	}{
		{[]string{"recur", "list"}, "GET", "/api/recurrences", nil},
		{[]string{"recur", "get", id}, "GET", "/api/recurrences/" + id, nil},
		{[]string{"recur", "create", "--body-file", "-"}, "POST", "/api/recurrences", map[string]any{"project_id": id}},
		{[]string{"recur", "update", id, "--body-file", "-"}, "PUT", "/api/recurrences/" + id, map[string]any{"project_id": id}},
		{[]string{"recur", "pause", id, "--revision", "7"}, "POST", "/api/recurrences/" + id + "/pause", map[string]any{"expected_revision": float64(7)}},
		{[]string{"recur", "resume", id, "--revision", "7"}, "POST", "/api/recurrences/" + id + "/resume", map[string]any{"expected_revision": float64(7)}},
		{[]string{"recur", "run-now", id, "--idempotency-key", "once"}, "POST", "/api/recurrences/" + id + "/run-now", map[string]any{"idempotency_key": "once"}},
		{[]string{"recur", "preview", id, "--count", "3"}, "GET", "/api/recurrences/" + id + "/preview?count=3", nil},
	} {
		t.Run(strings.Join(tc.args, " "), func(t *testing.T) {
			isolate(t)
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if r.Method != tc.method || r.URL.RequestURI() != tc.path {
					t.Errorf("request %s %s", r.Method, r.URL.RequestURI())
					http.Error(w, "wrong request", 400)
					return
				}
				if tc.body != nil {
					var got map[string]any
					if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
						t.Error(err)
					}
					a, _ := json.Marshal(got)
					b, _ := json.Marshal(tc.body)
					if string(a) != string(b) {
						t.Errorf("body %s want %s", a, b)
					}
				}
				_, _ = w.Write([]byte(`{"id":"` + id + `","revision":7,"items":[],"times":[],"trigger_kind":"event"}`))
			}))
			defer server.Close()
			t.Setenv("AEON_URL", server.URL)
			t.Setenv("AEON_API_KEY", testKey)
			code, out, err := runCLI(append([]string{"aeon", "--json"}, tc.args...), `{"project_id":"`+id+`"}`)
			if code != 0 || calls != 1 {
				t.Fatalf("code %d calls %d out %s err %s", code, calls, out, err)
			}
			assertNoSecret(t, out+err)
		})
	}
}
func TestRecurCLIRevisionFetchAndLocalValidation(t *testing.T) {
	isolate(t)
	const id = "11111111-1111-1111-1111-111111111111"
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 1 {
			if r.Method != "GET" || r.URL.Path != "/api/recurrences/"+id {
				t.Error("revision fetch")
			}
		} else {
			var in map[string]int
			if r.Method != "POST" || json.NewDecoder(r.Body).Decode(&in) != nil || in["expected_revision"] != 9 {
				t.Error("revision write")
			}
		}
		_, _ = w.Write([]byte(`{"id":"` + id + `","revision":9}`))
	}))
	defer server.Close()
	t.Setenv("AEON_URL", server.URL)
	t.Setenv("AEON_API_KEY", testKey)
	code, _, err := runCLI([]string{"aeon", "recur", "pause", id}, "")
	if code != 0 || calls != 2 {
		t.Fatalf("pause %d %d %s", code, calls, err)
	}
	for _, args := range [][]string{{"recur", "get", "bad"}, {"recur", "run-now", id}, {"recur", "preview", id, "--count", "101"}, {"recur", "list", "--after", "bad"}, {"recur", "update", "bad"}, {"recur", "create"}} {
		before := calls
		code, _, _ := runCLI(append([]string{"aeon"}, args...), "")
		if code != 2 || calls != before {
			t.Fatalf("local validation %v code %d calls %d", args, code, calls)
		}
	}
}

func TestRecurCLITemplateFlags(t *testing.T) {
	const project = "10000000-0000-4000-8000-000000000001"
	const otherProject = "10000000-0000-4000-8000-000000000002"
	const id = "10000000-0000-4000-8000-000000000003"
	const tag = "10000000-0000-4000-8000-000000000004"
	const existingTag = "10000000-0000-4000-8000-000000000005"
	const foreignTag = "10000000-0000-4000-8000-000000000006"
	for _, action := range []string{"create", "update"} {
		for _, ref := range []string{"website-audit", tag} {
			for _, scope := range []string{"project", "workspace"} {
				t.Run(action+"/"+ref+"/"+scope, func(t *testing.T) {
					isolate(t)
					dir := t.TempDir()
					descFile, criteriaFile := filepath.Join(dir, "playbook.txt"), filepath.Join(dir, "criteria.txt")
					for path, content := range map[string]string{descFile: "Read the playbook\nKeep {{date}} literal.\n", criteriaFile: "Check links\r\n\n Verify findings {{occurrence}} \n"} {
						if err := os.WriteFile(path, []byte(content), 0600); err != nil {
							t.Fatal(err)
						}
					}
					body := map[string]any{"project_id": project, "parent_id": id, "template": map[string]any{"title": "Audit {{date}}", "description": "Old description", "acceptance_criteria": []string{"Old criterion"}, "tags": []string{existingTag}, "priority": "high", "estimate_hours": 2}, "trigger": map[string]string{"kind": "event", "event": "release.published"}}
					if action == "update" {
						body["expected_revision"] = 7
					}
					raw, _ := json.Marshal(body)
					want := map[string]any{}
					_ = json.Unmarshal(raw, &want)
					template := want["template"].(map[string]any)
					template["description"] = "Read the playbook\nKeep {{date}} literal.\n"
					template["acceptance_criteria"] = []any{"Check links", "Verify findings {{occurrence}}"}
					template["tags"] = []any{existingTag, tag}
					reads, writes := 0, 0
					server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						if r.Method == "GET" && r.URL.Path == "/api/nodes" {
							reads++
							q := r.URL.Query()
							if q.Get("kind") != "tag" || q.Get("limit") != "200" || (ref == tag && q.Get("ids") != tag) || (ref != tag && q.Get("q") != ref) {
								t.Errorf("tag query %s", r.URL.RawQuery)
							}
							if q.Get("cursor") == "" {
								_ = json.NewEncoder(w).Encode(map[string]any{"items": []any{map[string]any{"id": foreignTag, "title": "website-audit", "project": map[string]string{"id": otherProject}}}, "next_cursor": "next-tags"})
							} else {
								if q.Get("cursor") != "next-tags" {
									t.Error("tag cursor was not preserved")
								}
								var tagProject any
								if scope == "project" {
									tagProject = map[string]string{"id": project}
								}
								_ = json.NewEncoder(w).Encode(map[string]any{"items": []any{map[string]any{"id": tag, "title": "website-audit", "project": tagProject}}})
							}
							return
						}
						writes++
						path, method := "/api/recurrences", "POST"
						if action == "update" {
							path += "/" + id
							method = "PUT"
						}
						if r.URL.Path != path || r.Method != method {
							t.Errorf("mutation %s %s", r.Method, r.URL.Path)
						}
						var got map[string]any
						if err := json.NewDecoder(r.Body).Decode(&got); err != nil || !reflect.DeepEqual(got, want) {
							t.Errorf("merged body %v want %v; %v", got, want, err)
						}
						_, _ = w.Write([]byte(`{"id":"` + id + `","revision":8}`))
					}))
					defer server.Close()
					t.Setenv("AEON_URL", server.URL)
					t.Setenv("AEON_API_KEY", testKey)
					args := []string{"aeon", "--json", "recur", action}
					if action == "update" {
						args = append(args, id)
					}
					args = append(args, "--body-file", "-", "--tag", ref, "--description-file", descFile, "--criteria-file", criteriaFile)
					code, out, err := runCLI(args, string(raw))
					if code != 0 || reads != 2 || writes != 1 {
						t.Fatalf("code %d reads %d writes %d; %s %s", code, reads, writes, out, err)
					}
					assertNoSecret(t, out+err)
				})
			}
		}
	}
}

func TestRecurCLITemplateFlagValidation(t *testing.T) {
	isolate(t)
	const project = "10000000-0000-4000-8000-000000000001"
	const tag = "10000000-0000-4000-8000-000000000002"
	const other = "10000000-0000-4000-8000-000000000003"
	writes := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" || r.URL.Path != "/api/nodes" {
			writes++
			http.Error(w, "unexpected write", 400)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"items": []any{map[string]any{"id": tag, "title": "foreign", "project": map[string]string{"id": other}}}})
	}))
	defer server.Close()
	t.Setenv("AEON_URL", server.URL)
	t.Setenv("AEON_API_KEY", testKey)
	body := `{"project_id":"` + project + `","template":{"title":"Audit"}}`
	for _, ref := range []string{"foreign", tag} {
		code, _, err := runCLI([]string{"aeon", "recur", "create", "--tag", ref}, body)
		if code != 2 || writes != 0 || !strings.Contains(err, "not found in this project") {
			t.Fatalf("foreign tag code %d writes %d: %s", code, writes, err)
		}
	}
	dir := t.TempDir()
	for _, tc := range []struct {
		flag, content string
	}{
		{"--description-file", strings.Repeat("a", 65537)},
		{"--criteria-file", strings.Repeat("criterion\n", 101)},
		{"--criteria-file", strings.Repeat("a", 4097)},
	} {
		path := filepath.Join(dir, "template.txt")
		if err := os.WriteFile(path, []byte(tc.content), 0600); err != nil {
			t.Fatal(err)
		}
		code, _, err := runCLI([]string{"aeon", "recur", "create", tc.flag, path}, body)
		if code != 2 || writes != 0 {
			t.Fatalf("file bounds code %d writes %d: %s", code, writes, err)
		}
	}
	code, _, err := runCLI([]string{"aeon", "recur", "create", "--description-file", "-"}, body)
	if code != 2 || !strings.Contains(err, "only one input file") {
		t.Fatalf("stdin collision %d %s", code, err)
	}
}
