// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	messageharness "github.com/inspr-at/paimos/backend/agentmessage/harness"
)

// TestAgentIntercomRunbookUsesShippedCLI makes the public runbook a CI-owned
// contract. A command or flag rename must update the executable surface and
// its GitHub documentation together.
func TestAgentIntercomRunbookUsesShippedCLI(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "docs", "AGENT_INTERCOM.md")) // #nosec G304 -- fixed in-repo documentation path.
	if err != nil {
		t.Fatal(err)
	}
	doc := string(raw)
	root := rootCmd()
	tests := []struct {
		path  []string
		flags []string
	}{
		{[]string{"project", "show"}, nil},
		{[]string{"session", "start"}, []string{"project", "agent"}},
		{[]string{"tell"}, []string{"project", "level", "message"}},
		{[]string{"message", "allow"}, []string{"project", "for"}},
		{[]string{"listen"}, []string{"as", "project", "follow", "deliver"}},
		{[]string{"message", "target", "set"}, []string{"project", "address", "adapter", "kind", "maximum-level", "role", "target-ref-file"}},
		{[]string{"message", "target", "list"}, []string{"project", "address"}},
		{[]string{"message", "target", "requeue"}, []string{"project", "address"}},
		{[]string{"message", "deliveries"}, []string{"project"}},
		{[]string{"harness", "list"}, []string{"project"}},
		{[]string{"harness", "status"}, []string{"project", "session"}},
		{[]string{"harness", "interrupt"}, []string{"project", "session"}},
		{[]string{"harness", "stop"}, []string{"project", "session"}},
		{[]string{"harness", "control", "get"}, []string{"project", "session", "control-id"}},
		{[]string{"harness", "mark-stopped"}, []string{"project", "session", "agent", "worker-lease-file"}},
	}
	for _, test := range tests {
		name := strings.Join(test.path, " ")
		t.Run(name, func(t *testing.T) {
			if !strings.Contains(doc, name) {
				t.Fatalf("runbook does not name shipped command %q", name)
			}
			command, remaining, findErr := root.Find(test.path)
			if findErr != nil || len(remaining) != 0 || command == root {
				t.Fatalf("command %q unavailable: command=%v remaining=%v error=%v", name, command, remaining, findErr)
			}
			for _, flag := range test.flags {
				if command.Flags().Lookup(flag) == nil && command.InheritedFlags().Lookup(flag) == nil {
					t.Errorf("documented command %q lost --%s", name, flag)
				}
			}
			if len(test.flags) > 0 && !documentedCommandHasFlags(doc, "paimos "+name, test.flags) {
				t.Errorf("runbook does not show %q with all required documented flags %v", name, test.flags)
			}
		})
	}
	if root.PersistentFlags().Lookup("json") == nil || !strings.Contains(doc, "paimos --json project show") {
		t.Error("numeric project lookup drifted from the documented --json project show command")
	}
}

func TestAgentIntercomDocsMatchShippedControlOutcomeSurface(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "docs", "AGENT_INTERCOM.md")) // #nosec G304 -- fixed in-repo documentation path.
	if err != nil {
		t.Fatal(err)
	}
	doc := strings.Join(strings.Fields(string(raw)), " ")
	for _, claim := range []string{
		"paimos harness control get",
	} {
		if !strings.Contains(doc, claim) {
			t.Errorf("runbook lost control-outcome contract %q", claim)
		}
	}

	const sessionID = "11111111-1111-4111-8111-111111111111"
	const controlID = "22222222-2222-4222-8222-222222222222"
	var routeSeen bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case request.Method == http.MethodGet && request.URL.RequestURI() == "/api/projects?status=all":
			_, _ = w.Write([]byte(`[{"id":6,"key":"PAI"}]`))
		case request.Method == http.MethodGet && request.URL.Path == "/api/projects/6/harness-sessions/"+sessionID+"/controls/"+controlID:
			routeSeen = true
			if request.Header.Get("X-Paimos-Harness-Worker-Lease") != "" {
				t.Error("operator control read sent a worker lease")
			}
			_, _ = w.Write([]byte(`{"id":"` + controlID + `","project_id":6,"harness_session_id":"` + sessionID + `","correlation_id":"` + controlID + `","sequence":1,"kind":"interrupt","state":"applied","outcome":"applied","reason":"applied","requested_at":"2026-09-01T08:00:00Z","claimed_at":"2026-09-01T08:00:01Z","completed_at":"2026-09-01T08:00:02Z"}`))
		default:
			t.Errorf("unexpected request %s %s", request.Method, request.URL.String())
			http.Error(w, `{"error":"unexpected"}`, http.StatusNotFound)
		}
	}))
	t.Cleanup(server.Close)
	t.Setenv(envURL, server.URL)
	t.Setenv(envAPIKey, "test_key")

	out, _, err := executeCLIForTest(t, "--json", "harness", "control", "get", "--project", "PAI", "--session", sessionID, "--control-id", controlID)
	if err != nil {
		t.Fatal(err)
	}
	var outcome map[string]any
	if err := json.Unmarshal([]byte(out), &outcome); err != nil {
		t.Fatal(err)
	}
	if !routeSeen || outcome["id"] != controlID || outcome["correlation_id"] != controlID || outcome["state"] != "applied" {
		t.Fatalf("routeSeen=%v outcome=%v", routeSeen, outcome)
	}
	for _, forbidden := range []string{"worker_lease", "harness_session_ref", "message_target_id", "body", "requested_by_user_id"} {
		if _, exists := outcome[forbidden]; exists {
			t.Errorf("control outcome exposed forbidden field %q", forbidden)
		}
	}

	openAPIRaw, err := os.ReadFile(filepath.Join("..", "..", "handlers", "openapi.json")) // #nosec G304 -- fixed in-repo contract path.
	if err != nil {
		t.Fatal(err)
	}
	var openAPI map[string]any
	if err := json.Unmarshal(openAPIRaw, &openAPI); err != nil {
		t.Fatal(err)
	}
	paths := jsonObject(t, openAPI["paths"], "paths")
	controlRoute := jsonObject(t, paths["/api/projects/{id}/harness-sessions/{sessionID}/controls/{controlID}"], "control outcome route")
	get := jsonObject(t, controlRoute["get"], "control outcome GET")
	responses := jsonObject(t, get["responses"], "control outcome responses")
	okResponse := jsonObject(t, responses["200"], "control outcome 200")
	content := jsonObject(t, okResponse["content"], "control outcome content")
	media := jsonObject(t, content["application/json"], "control outcome JSON")
	responseSchema := jsonObject(t, media["schema"], "control outcome response schema")
	if responseSchema["$ref"] != "#/components/schemas/HarnessControlOutcome" {
		t.Fatalf("control outcome response schema=%v", responseSchema["$ref"])
	}
	components := jsonObject(t, openAPI["components"], "components")
	schemas := jsonObject(t, components["schemas"], "schemas")
	outcomeSchema := jsonObject(t, schemas["HarnessControlOutcome"], "HarnessControlOutcome")
	properties := jsonObject(t, outcomeSchema["properties"], "HarnessControlOutcome properties")
	expectedFields := []string{"id", "project_id", "harness_session_id", "correlation_id", "sequence", "kind", "state", "outcome", "reason", "requested_at", "claimed_at", "completed_at"}
	if len(properties) != len(expectedFields) {
		t.Fatalf("HarnessControlOutcome fields=%v", properties)
	}
	for _, field := range expectedFields {
		if _, exists := properties[field]; !exists {
			t.Errorf("HarnessControlOutcome lost field %q", field)
		}
	}

	for _, suffix := range []string{"heartbeat", "yield", "drain", "complete-delivery", "drain-steer", "complete-steer", "controls/{controlID}/complete", "stop"} {
		route := jsonObject(t, paths["/api/projects/{id}/harness-sessions/{sessionID}/"+suffix], suffix+" route")
		post := jsonObject(t, route["post"], suffix+" POST")
		postResponses := jsonObject(t, post["responses"], suffix+" responses")
		jsonObject(t, postResponses["403"], suffix+" 403")
	}
}

func jsonObject(t *testing.T, value any, name string) map[string]any {
	t.Helper()
	object, ok := value.(map[string]any)
	if !ok {
		t.Fatalf("%s is not an object: %T", name, value)
	}
	return object
}

func TestAgentIntercomREADMEQuickstartUsesShippedCLI(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "README.md")) // #nosec G304 -- fixed in-repo documentation path.
	if err != nil {
		t.Fatal(err)
	}
	// Join shell continuations, but keep command boundaries: a flag on the
	// next example must not satisfy the current command's scope contract.
	doc := strings.ReplaceAll(string(raw), "\\\n", " ")
	oldInstance := flagInstance
	t.Cleanup(func() { flagInstance = oldInstance })
	tests := []struct {
		path  []string
		flags []string
	}{
		{[]string{"runtime", "doctor"}, []string{"instance", "project"}},
		{[]string{"runtime", "setup"}, []string{"instance", "project"}},
		{[]string{"orchestrator", "start"}, []string{"instance", "project", "guided", "workspace"}},
		{[]string{"worker", "start"}, []string{"instance", "project", "guided", "workspace", "ticket", "work-shape"}},
	}
	var instance, project, coordinatorWorkspace string
	for _, test := range tests {
		name := strings.Join(test.path, " ")
		var example []string
		for _, line := range strings.Split(doc, "\n") {
			line = strings.Join(strings.Fields(line), " ")
			if strings.HasPrefix(line, "paimos ") && strings.Contains(line, " "+name+" ") {
				example = strings.Fields(line)[1:]
				break
			}
		}
		if len(example) == 0 {
			t.Fatalf("README does not show a scoped %q command", name)
		}
		root := rootCmd()
		command, remaining, findErr := root.Find(example)
		if findErr != nil || command.CommandPath() != root.Name()+" "+name {
			t.Fatalf("README command %q unavailable: command=%v remaining=%v error=%v", name, command, remaining, findErr)
		}
		// Parse only: this validates the documented executable flags without
		// loading credentials, contacting a runtime or starting a child.
		if err := command.ParseFlags(remaining); err != nil {
			t.Fatalf("README command %q has invalid flags: %v", name, err)
		}
		if args := command.Flags().Args(); len(args) != 0 {
			t.Fatalf("README command %q has unexpected arguments: %v", name, args)
		}
		values := map[string]string{}
		for _, name := range test.flags {
			flag := command.Flags().Lookup(name)
			if flag == nil || !flag.Changed || strings.TrimSpace(flag.Value.String()) == "" {
				t.Fatalf("README command %q needs an explicit --%s value", command.CommandPath(), name)
			}
			values[name] = flag.Value.String()
		}
		if instance == "" {
			instance, project = values["instance"], values["project"]
		}
		if values["instance"] != instance || values["project"] != project {
			t.Errorf("README %q leaves the instance/project selected during diagnosis", name)
		}
		if test.path[1] == "start" && values["guided"] != "true" {
			t.Errorf("README %q must guide unresolved agent/profile choices", name)
		}
		if test.path[0] == "orchestrator" {
			coordinatorWorkspace = values["workspace"]
		}
		if test.path[0] == "worker" {
			if values["workspace"] == coordinatorWorkspace {
				t.Error("README coordinator and worker must use distinct exclusive workspaces")
			}
			if !friendlyTicketKey.MatchString(values["ticket"]) || !strings.HasPrefix(values["ticket"], project+"-") || (values["work-shape"] != "ship" && values["work-shape"] != "scout") {
				t.Error("README worker must name a ticket in the selected project and a supported work shape")
			}
		}
	}
}

func TestAgentIntercomRunbookPinsReleaseAndAdministratorBoundaries(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "docs", "AGENT_INTERCOM.md")) // #nosec G304 -- fixed in-repo documentation path.
	if err != nil {
		t.Fatal(err)
	}
	doc := strings.Join(strings.Fields(string(raw)), " ")

	handlerRaw, err := os.ReadFile(filepath.Join("..", "..", "handlers", "agent_messages.go")) // #nosec G304 -- fixed in-repo authorization source.
	if err != nil {
		t.Fatal(err)
	}
	handler := string(handlerRaw)
	for _, route := range []string{
		`r.With(auth.RequireAdmin, auth.RequireProjectView).Post("/projects/{id}/message-targets"`,
		`r.With(auth.RequireAdmin, auth.RequireProjectView).Get("/projects/{id}/message-targets"`,
		`r.With(auth.RequireAdmin, auth.RequireProjectView).Post("/projects/{id}/message-targets/requeue"`,
		`r.With(auth.RequireAdmin, auth.RequireProjectView).Get("/projects/{id}/message-deliveries"`,
		`r.With(auth.RequireAdmin, auth.RequireProjectView).Post("/projects/{id}/message-deliveries/{deliveryID}/requeue"`,
	} {
		if !strings.Contains(handler, route) {
			t.Errorf("shipped message administration route lost RequireAdmin: %s", route)
		}
	}

	for _, command := range []string{
		"go test -count=1 ./db -run '^TestMigration168RetiresUnboundGenerationsAndEnforcesLeaseDigest$'",
		"go test -count=1 ./db -run '^TestMigration169BackfillsTerminalTruthAndRejectsInconsistentActivity$'",
		"go test -count=1 ./handlers -run '^(TestHarnessWorkerMutationsUseUniformNonEnumeratingAuthorization|TestGetHarnessControlReturnsScopedNonSecretOutcome)$'",
	} {
		if !strings.Contains(doc, command) {
			t.Errorf("runbook executable evidence lost focused command %q", command)
		}
	}
}

func documentedCommandHasFlags(doc, command string, flags []string) bool {
	for offset := 0; offset < len(doc); {
		index := strings.Index(doc[offset:], command)
		if index < 0 {
			return false
		}
		start := offset + index
		end := len(doc)
		if paragraph := strings.Index(doc[start:], "\n\n"); paragraph >= 0 {
			end = start + paragraph
		}
		snippet := doc[start:end]
		complete := true
		for _, flag := range flags {
			complete = complete && strings.Contains(snippet, "--"+flag)
		}
		if complete {
			return true
		}
		offset = start + len(command)
	}
	return false
}

func TestAgentIntercomPublicDocsDoNotContainResolvedLocalCapabilities(t *testing.T) {
	paths := []string{
		filepath.Join("..", "..", "..", "README.md"),
		filepath.Join("..", "..", "..", "docs", "AGENT_INTERCOM.md"),
	}
	for _, path := range paths {
		raw, err := os.ReadFile(path) // #nosec G304 -- fixed in-repo documentation paths.
		if err != nil {
			t.Fatal(err)
		}
		body := string(raw)
		for _, forbidden := range []string{"/Users/", "/home/", `"target_ref":`, `"target_secret":`, `"message_id":"`, `"delivery_id":"`} {
			if strings.Contains(body, forbidden) {
				t.Errorf("%s contains resolved or raw private example marker %q", path, forbidden)
			}
		}
	}
}

func TestAgentIntercomRunbookKeepsUnmanagedClaudeAndGrokSimpleOnly(t *testing.T) {
	plugins := []messageharness.Plugin{
		messageharness.ClaudePlugin{},
		messageharness.ClaudePlugin{Channel: true},
		messageharness.GrokRoutinePlugin{},
	}
	for _, plugin := range plugins {
		if level := plugin.MaximumLevel(); level != messageharness.LevelSimple {
			t.Fatalf("unmanaged adapter %s now advertises %q; review the intercom truth matrix", plugin.Name(), level)
		}
	}

}

func TestAgentIntercomDocsKeepWorkerLeaseTrustAndRecoveryContract(t *testing.T) {
	paths := map[string]string{
		"runbook":     filepath.Join("..", "..", "..", "docs", "AGENT_INTERCOM.md"),
		"integration": filepath.Join("..", "..", "..", "docs", "AGENT_INTEGRATION.md"),
	}
	docs := make(map[string]string, len(paths))
	for name, path := range paths {
		raw, err := os.ReadFile(path) // #nosec G304 -- fixed in-repo documentation paths.
		if err != nil {
			t.Fatal(err)
		}
		docs[name] = string(raw)
	}
	for name, doc := range docs {
		normalized := strings.Join(strings.Fields(doc), " ")
		for _, invariant := range []string{
			"ownership_lost",
			"remote_closed",
			"lease_deleted",
			"prunable",
		} {
			if !strings.Contains(normalized, invariant) {
				t.Errorf("%s lost worker-lease invariant %q", name, invariant)
			}
		}
	}
	if !documentedCommandHasFlags(docs["integration"], "paimos harness register", []string{"project", "agent", "harness", "host", "harness-session-file", "worker-lease-file"}) {
		t.Error("integration guide registration example lost its protected generation lease")
	}
	if !documentedCommandHasFlags(docs["runbook"], "paimos harness mark-stopped", []string{"project", "session", "agent", "worker-lease-file"}) {
		t.Error("runbook stop example lost its exact generation lease")
	}
	apiRaw, err := os.ReadFile(filepath.Join("..", "..", "..", "docs", "api-minimal.md")) // #nosec G304 -- fixed in-repo documentation path.
	if err != nil {
		t.Fatal(err)
	}
	api := strings.Join(strings.Fields(string(apiRaw)), " ")
	for _, claim := range []string{
		"X-Paimos-Harness-Worker-Lease",
		"--worker-lease-file",
	} {
		if !strings.Contains(api, claim) {
			t.Errorf("REST reference lost worker authorization claim %q", claim)
		}
	}
}
