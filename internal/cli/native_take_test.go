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

func TestNativeTakeCommandPreservesLegacyCommands(t *testing.T) {
	rt := &runtime{program: "aeon", stdout: &bytes.Buffer{}}
	cmd := rt.cmdSessionTake()
	if cmd.Name != "take" || !strings.HasPrefix(cmd.Use, "session take OWNED_ID") || cmd.minArgs != 1 || cmd.maxArgs != 1 {
		t.Fatal("take node has wrong canonical meaning")
	}
	legacy := rt.cmdAttach()
	if legacy.Use != "attach <issue-ref> <file>" || legacy.minArgs != 2 || legacy.maxArgs != 2 || rt.cmdSession().child("start") == nil {
		t.Fatal("legacy attachment/start meaning changed")
	}
	if !strings.Contains(cmd.Long, "Esc belongs to the TUI") || !strings.Contains(cmd.Long, "EOF and signals leave the owned PTY running") {
		t.Fatal("take help promises unsafe keyboard/lifetime behavior")
	}
}
func TestNativeTakeCommandValidation(t *testing.T) {
	for _, args := range [][]string{{"bad", "--project", "AEON"}, {uuid.NewString()}, {uuid.NewString(), "--project", "AEON", "--socket", "/x", "--setup-root", "/y"}, {uuid.NewString(), "--project", "AEON", "--json"}, {uuid.NewString(), "--project", "AEON"}} {
		rt := &runtime{stdin: strings.NewReader(""), stdout: &bytes.Buffer{}, stderr: &bytes.Buffer{}}
		cmd := rt.cmdSessionTake()
		pos, err := rt.parse(cmd, args)
		if err == nil {
			err = cmd.run(pos)
		}
		if err == nil {
			t.Fatal("invalid/nonterminal take reached native I/O")
		}
	}
}
func TestNativeTakeRejectsForeignProjectionBeforeSocket(t *testing.T) {
	project, wanted := uuid.NewString(), uuid.NewString()
	b := nativecontract.Binding{TenantID: uuid.NewString(), ProjectID: uuid.NewString(), OwnedSessionID: wanted, HarnessSessionID: uuid.NewString(), RuntimeEpoch: uuid.NewString(), DaemonID: "fixture-daemon", DaemonGeneration: "fixture-generation", BindingRevision: 1}
	view := nativecontract.SessionView{OwnedSessionID: wanted, CurrentBinding: &b, OwnerPersonID: uuid.NewString(), Role: "coordinator", Profile: "native", Harness: "claude", State: "taken", Mode: "terminal", ConversationRef: "fixture-conversation", Generation: 1, Capabilities: nativecontract.Capabilities{Steer: "unsupported", Quota: "unsupported", QualificationDigest: strings.Repeat("a", 64)}}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/projects/lookup":
			_ = json.NewEncoder(w).Encode(map[string]string{"id": project})
		case "/api/projects/" + project + "/native-sessions/" + wanted:
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
	cmd := rt.cmdSessionTake()
	pos, err := rt.parse(cmd, []string{wanted, "--project", "AEON", "--hand-back", "--socket", "/must-not-open"})
	if err == nil {
		err = cmd.run(pos)
	}
	if err == nil || err.Error() != "owned session projection identity mismatch" {
		t.Fatalf("wrong refusal for foreign projection: %v", err)
	}
}
