// SPDX-License-Identifier: AGPL-3.0-only

package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/inspr-at/paimos/internal/inbox"
)

// Risk: a friendly name can select another generation or instance, drop the
// sender's cross-project binding, or turn a queued receipt into delivery.
// Kept in the registered tell-receipt suite; the brief reserves tier manifests.
func testPeerCLI(t *testing.T) {
	const target = "00000000-0000-4000-8000-000000000021"
	const source = "00000000-0000-4000-8000-000000000022"
	const principal = "00000000-0000-4000-8000-000000000023"
	const sourceProject = "00000000-0000-4000-8000-000000000024"
	const targetProject = "00000000-0000-4000-8000-000000000025"
	const message = "00000000-0000-4000-8000-000000000026"
	peer := inbox.Peer{SessionID: target, PrincipalID: principal, Name: "ops-lead", ProjectID: targetProject, Project: "OPS", Harness: "claude", State: "working/idle", Address: "session:" + target}
	for _, tc := range []struct {
		name, instance, wantError string
		args                      []string
		ambient, explicit, json   bool
		items                     int
		page                      bool
	}{
		{name: "plain directory", args: []string{"peers"}, items: 1},
		{name: "JSON directory with cursor", args: []string{"peers", "--limit", "1"}, json: true, items: 1, page: true},
		{name: "ambient cross-project tell", args: []string{"tell", "ops-lead", "-m", "coordinate"}, ambient: true, json: true, items: 1},
		{name: "exact session address", args: []string{"tell", "session:" + target, "--sender-session", source, "-m", "coordinate"}, explicit: true, json: true, items: 1},
		{name: "project disambiguation", args: []string{"tell", "ops-lead", "--project", "OPS", "--sender-session", source, "-m", "coordinate"}, explicit: true, json: true, items: 1},
		{name: "ambiguous name", args: []string{"tell", "ops-lead", "-m", "coordinate"}, ambient: true, items: 2, wantError: "ambiguous"},
		{name: "missing name", args: []string{"tell", "ops-lead", "-m", "coordinate"}, ambient: true, wantError: "no live owned peer"},
		{name: "other instance never falls back", instance: "pma", args: []string{"tell", "ops-lead", "-m", "coordinate"}, ambient: true, wantError: "no live owned peer"},
		{name: "foreign instance session never falls back", instance: "pma", args: []string{"tell", "session:" + target, "--sender-session", source, "-m", "coordinate"}, explicit: true, wantError: "no live owned peer"},
		{name: "unreadable directory", args: []string{"tell", "ops-lead", "-m", "coordinate"}, ambient: true, wantError: "403"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			isolate(t)
			var ppmCalls, pmaCalls, sends atomic.Int64
			server := func(instance string, calls *atomic.Int64) *httptest.Server {
				return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					calls.Add(1)
					if r.Header.Get("Authorization") != "Bearer synthetic-"+instance {
						t.Error("credential crossed its selected instance")
						http.Error(w, `{"error":"unauthorized"}`, 401)
						return
					}
					switch {
					case r.Method == "GET" && r.URL.Path == "/api/peers":
						if tc.name == "unreadable directory" {
							http.Error(w, `{"error":"forbidden"}`, 403)
							return
						}
						if strings.HasPrefix(tc.args[0], "tell") {
							if strings.HasPrefix(tc.args[1], "session:") {
								if r.URL.Query().Get("session") != target || r.URL.Query().Get("name") != "" {
									t.Error("exact session was not resolved exactly")
								}
							} else if r.URL.Query().Get("name") != peer.Name {
								t.Error("peer name did not use the directory's exact filter")
							}
							if r.URL.Query().Get("limit") != "2" {
								t.Error("name lookup must check for a second match")
							}
						}
						if tc.name == "project disambiguation" && r.URL.Query().Get("project") != targetProject {
							t.Error("recipient project filter was lost")
						}
						page := inbox.PeerPage{Items: []inbox.Peer{}}
						for range tc.items {
							page.Items = append(page.Items, peer)
						}
						if tc.page {
							page.NextAfter = ptr(target)
						}
						_ = json.NewEncoder(w).Encode(page)
					case r.URL.Path == "/api/projects/lookup":
						if tc.name != "project disambiguation" || r.URL.Query().Get("ref") != "OPS" {
							t.Error("unexpected project discovery for a peer")
						}
						fmt.Fprintf(w, `{"id":%q}`, targetProject)
					case r.URL.Path == "/api/me":
						fmt.Fprintf(w, `{"principal":{"id":%q,"name":"aeon-lead"}}`, principal)
					case r.Method == "POST" && r.URL.Path == "/api/peers/"+target+"/messages":
						sends.Add(1)
						var body map[string]any
						if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
							t.Error(err)
						}
						if body["sender_session_id"] != source || body["body"] != "coordinate" || body["to"] != nil || body["recipient_session_id"] != nil {
							t.Errorf("peer send lost exact attribution: %+v", body)
						}
						w.WriteHeader(201)
						fmt.Fprintf(w, `{"id":%q,"recipient_principal_id":%q,"recipient_session_id":%q,"sender_session_id":%q,"sender_project_id":%q,"sender_project":"AEON","status":"accepted"}`, message, principal, target, source, sourceProject)
					case r.URL.Path == "/api/inbox/messages/"+message+"/receipt":
						fmt.Fprintf(w, `{"message_id":%q,"state":"queued"}`, message)
					default:
						t.Errorf("unexpected peer request: %s %s", r.Method, r.URL.Path)
						http.NotFound(w, r)
					}
				}))
			}
			ppm := server("ppm", &ppmCalls)
			defer ppm.Close()
			pma := server("pma", &pmaCalls)
			defer pma.Close()
			config := filepath.Join(t.TempDir(), "config.yaml")
			if err := saveConfig(config, fileConfig{DefaultInstance: "ppm", Instances: map[string]fileInstance{"ppm": {URL: ppm.URL}, "pma": {URL: pma.URL}}}); err != nil {
				t.Fatal(err)
			}
			for _, instance := range []string{"ppm", "pma"} {
				if err := writeKey(keyPath(config, instance), "synthetic-"+instance); err != nil {
					t.Fatal(err)
				}
			}
			if tc.ambient {
				t.Setenv("AEON_SESSION_ID", source)
			}
			args := []string{"aeon", "--config", config}
			if tc.instance != "" {
				args = append(args, "--instance", tc.instance)
			}
			if tc.json {
				args = append(args, "--json")
			}
			args = append(args, tc.args...)
			var out, errOut bytes.Buffer
			code := runMessaging(args, strings.NewReader(""), &out, &errOut, nil)
			if tc.wantError != "" {
				if code == 0 || !strings.Contains(errOut.String(), tc.wantError) || sends.Load() != 0 {
					t.Fatalf("failed resolution sent or reported success: code=%d out=%q err=%q sends=%d", code, out.String(), errOut.String(), sends.Load())
				}
			} else {
				if code != 0 || errOut.Len() != 0 {
					t.Fatalf("code=%d out=%q err=%q", code, out.String(), errOut.String())
				}
				if tc.args[0] == "tell" {
					var sent map[string]any
					if err := json.Unmarshal(out.Bytes(), &sent); err != nil || sent["stored"] != true || sent["receipt_state"] != "queued" || sent["sender_session_id"] != source || sent["sender_project_id"] != sourceProject || sends.Load() != 1 {
						t.Fatalf("stored peer output: %s (%v)", out.String(), err)
					}
				} else if tc.json {
					var page inbox.PeerPage
					if err := json.Unmarshal(out.Bytes(), &page); err != nil || len(page.Items) != 1 || page.NextAfter == nil || *page.NextAfter != target {
						t.Fatalf("directory page lost continuation: %s (%v)", out.String(), err)
					}
				} else {
					for _, want := range []string{"ops-lead", "OPS", "claude", "working/idle", "session:" + target} {
						if !strings.Contains(out.String(), want) {
							t.Fatalf("directory omitted %s: %q", want, out.String())
						}
					}
				}
			}
			if tc.instance == "pma" {
				if ppmCalls.Load() != 0 || pmaCalls.Load() != 1 {
					t.Fatal("selected instance consulted another trust context")
				}
			} else if pmaCalls.Load() != 0 || ppmCalls.Load() == 0 {
				t.Fatal("peer lookup crossed instance boundary")
			}
		})
	}
	frame := messagingFrame(inbox.CompatMessage{SenderProjectID: ptr(sourceProject), SenderProject: "AEON", Body: "untrusted"}, "OPS")
	if !strings.Contains(frame, `project="OPS" sender_project="AEON" sender_project_id="`+sourceProject+`"`) {
		t.Fatalf("recipient frame lost sender project: %s", frame)
	}
}
