// SPDX-License-Identifier: AGPL-3.0-only

package cli

import (
	"bytes"
	"encoding/json"
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
	rt := &runtime{stdin: input, stdout: &bytes.Buffer{}, stderr: &bytes.Buffer{}, personClient: client.New(server.URL, testKey)}
	cmd := rt.cmdSessionAttach()
	pos, err := rt.parse(cmd, []string{wanted, "--project", "AEON", "--socket", "/must-not-be-opened"})
	if err == nil {
		err = cmd.run(pos)
	}
	if err == nil || err.Error() != "owned session projection identity mismatch" {
		t.Fatalf("wrong failure for foreign projection: %v", err)
	}
}
