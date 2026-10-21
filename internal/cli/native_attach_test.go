// SPDX-License-Identifier: AGPL-3.0-only

package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/inspr-at/paimos/internal/client"
	"github.com/inspr-at/paimos/internal/nativecontract"
)

func TestNativeAttachCommandPreservesAttachmentMeaning(t *testing.T) {
	rt := &runtime{program: "aeon", stdout: &bytes.Buffer{}}
	cmd := rt.cmdSessionAttach()
	if cmd.Name != "attach" || !strings.HasPrefix(cmd.Use, "session attach OWNED_ID") || cmd.minArgs != 1 || cmd.maxArgs != 1 {
		t.Fatal("canonical command node changed")
	}
	legacy := rt.cmdAttach()
	if legacy.Use != "attach <issue-ref> <file>" || legacy.minArgs != 2 || legacy.maxArgs != 2 {
		t.Fatal("legacy attachment upload meaning changed")
	}
	for _, name := range []string{"list", "get", "rm"} {
		if legacy.child(name) == nil {
			t.Fatalf("legacy attachment %s disappeared", name)
		}
	}
	if rt.cmdSession().child("start") == nil {
		t.Fatal("legacy attribution start disappeared")
	}
}

func TestNativeAttachCommandValidation(t *testing.T) {
	for _, args := range [][]string{
		{"not-a-UUID", "--project", "AEON"},
		{uuid.NewString()},
		{uuid.NewString(), "--project", "AEON", "--socket", "/x", "--setup-root", "/y"},
		{uuid.NewString(), "--project", "AEON", "--input-mode", "unsupported"},
		{uuid.NewString(), "--project", "AEON", "--json"},
		{uuid.NewString(), "--project", "AEON"},
	} {
		rt := &runtime{stdin: strings.NewReader(""), stdout: &bytes.Buffer{}, stderr: &bytes.Buffer{}}
		cmd := rt.cmdSessionAttach()
		pos, err := rt.parse(cmd, args)
		if err == nil {
			err = cmd.run(pos)
		}
		if err == nil {
			t.Fatal("invalid command reached native I/O")
		}
	}
}

// The project/session fetched for the screen must match the user's target
// before even opening the local socket. A valid but foreign projection cannot
// become a control request merely because its UUIDs are well formed.
func TestNativeAttachRejectsMismatchedProjection(t *testing.T) {
	projectID, wanted := uuid.NewString(), uuid.NewString()
	binding := nativecontract.Binding{TenantID: uuid.NewString(), ProjectID: uuid.NewString(), OwnedSessionID: wanted, HarnessSessionID: uuid.NewString(), RuntimeEpoch: uuid.NewString(), DaemonID: "fixture-daemon", DaemonGeneration: "fixture-generation", BindingRevision: 1}
	view := nativecontract.SessionView{OwnedSessionID: wanted, CurrentBinding: &binding, OwnerPersonID: uuid.NewString(), Role: "coordinator", Profile: "native", Harness: "claude", State: "running", Mode: "structured", ConversationRef: "fixture-conversation", Generation: 1, Capabilities: nativecontract.Capabilities{Steer: "unsupported", Quota: "unsupported", QualificationDigest: strings.Repeat("a", 64)}}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/me":
			_ = json.NewEncoder(w).Encode(client.Me{Principal: client.Principal{ID: view.OwnerPersonID, TenantID: view.CurrentBinding.TenantID, Kind: "person"}, Tenant: client.Tenant{ID: view.CurrentBinding.TenantID}})
		case "/api/projects/lookup":
			_ = json.NewEncoder(w).Encode(map[string]string{"id": projectID})
		case "/api/projects/" + projectID + "/native-sessions/" + wanted:
			_ = json.NewEncoder(w).Encode(view)
		default:
			t.Error("unexpected API call")
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	input, err := os.CreateTemp(t.TempDir(), "stdin")
	if err != nil {
		t.Fatal(err)
	}
	defer input.Close()
	rt := &runtime{stdin: input, stdout: &bytes.Buffer{}, stderr: &bytes.Buffer{}, personClient: client.NewSession(server.URL, strings.Repeat("e", 64))}
	cmd := rt.cmdSessionAttach()
	pos, err := rt.parse(cmd, []string{wanted, "--project", "AEON", "--socket", "/must-not-be-opened"})
	if err == nil {
		err = cmd.run(pos)
	}
	if err == nil || err.Error() != "owned session projection identity mismatch" {
		t.Fatalf("wrong failure for foreign projection: %v", err)
	}
}

// Risk: native commands silently use an agent key or use a socket's presence
// as person identity. Both command paths reject before any session/socket I/O.
func TestNativeCommandsRequireIndependentPersonSession(t *testing.T) {
	for _, take := range []bool{false, true} {
		for _, kind := range []string{"missing", "api-key", "agent", "attributed-person"} {
			t.Run(fmt.Sprintf("take=%t/%s", take, kind), func(t *testing.T) {
				calls := 0
				personID, tenantID := uuid.NewString(), uuid.NewString()
				srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					calls++
					if r.URL.Path != "/api/me" || r.Header.Get("Authorization") != "" {
						t.Error("native command reached non-person API")
					}
					_ = json.NewEncoder(w).Encode(client.Me{Principal: client.Principal{ID: personID, TenantID: tenantID, Kind: "agent"}, Tenant: client.Tenant{ID: tenantID}})
				}))
				defer srv.Close()
				input, err := os.CreateTemp(t.TempDir(), "stdin")
				if err != nil {
					t.Fatal(err)
				}
				defer input.Close()
				rt := &runtime{stdin: input, stdout: &bytes.Buffer{}, stderr: &bytes.Buffer{}}
				switch kind {
				case "api-key":
					rt.personClient = client.New(srv.URL, "synthetic-agent-key")
				case "agent":
					rt.personClient = client.NewSession(srv.URL, strings.Repeat("e", 64))
				case "attributed-person":
					rt.personClient = client.NewSession(srv.URL, strings.Repeat("e", 64))
					rt.agentName = "fixture-agent"
				}
				cmd := rt.cmdSessionAttach()
				args := []string{uuid.NewString(), "--project", uuid.NewString(), "--socket", "/must-not-open"}
				if take {
					cmd = rt.cmdSessionTake()
					args = append(args, "--hand-back")
				}
				pos, err := rt.parse(cmd, args)
				if err == nil {
					err = cmd.run(pos)
				}
				if err == nil {
					t.Fatal("non-person reached native I/O")
				}
				expected := "native attach requires an existing person session; API keys are refused"
				switch kind {
				case "missing":
					expected = "--session-cookie-file is required for native attach/take"
				case "agent":
					expected = "native attach/take requires a verified person; agents and API keys are refused"
				case "attributed-person":
					expected = "native attach/take cannot use agent attribution"
				}
				if !strings.Contains(err.Error(), expected) {
					t.Fatalf("wrong refusal: %v", err)
				}
				wantCalls := 0
				if kind == "agent" {
					wantCalls = 1
				}
				if calls != wantCalls {
					t.Fatalf("unexpected API work: %d", calls)
				}
			})
		}
	}
}
