// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package agentmode

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/inspr-at/paimos/backend/delivery"
	"github.com/inspr-at/paimos/backend/safetext"
)

const (
	agentModeFixtureContract         = "agent-mode.snapshot.v1"
	agentModeFixtureGeneratorVersion = 1
)

type agentModeFixtureManifest struct {
	SchemaVersion    int                             `json:"schema_version"`
	Contract         string                          `json:"contract"`
	MediaType        string                          `json:"media_type"`
	Encoding         string                          `json:"encoding"`
	GeneratorVersion int                             `json:"generator_version"`
	Fixtures         []agentModeFixtureManifestEntry `json:"fixtures"`
}

type agentModeFixtureManifestEntry struct {
	File   string `json:"file"`
	Rows   int    `json:"rows"`
	Bytes  int    `json:"bytes"`
	SHA256 string `json:"sha256"`
}

func TestCanonicalAgentModeSnapshotFixturesUseProductionReader(t *testing.T) {
	counts := []int{1, 10, 100}
	expected := make(map[string][]byte, len(counts))
	entries := make([]agentModeFixtureManifestEntry, 0, len(counts))
	for _, count := range counts {
		count := count
		t.Run(fmt.Sprintf("build_%d", count), func(t *testing.T) {
			snapshot := canonicalAgentModeFixtureSnapshot(t, count)
			validateCanonicalFixtureCases(t, count, snapshot)
			raw := marshalFixtureSnapshot(t, snapshot)
			name := fmt.Sprintf("snapshot-v1-%d.json", count)
			expected[name] = raw
			digest := sha256.Sum256(raw)
			entries = append(entries, agentModeFixtureManifestEntry{
				File: name, Rows: count, Bytes: len(raw), SHA256: hex.EncodeToString(digest[:]),
			})
		})
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].File < entries[j].File })
	manifest := agentModeFixtureManifest{
		SchemaVersion:    SchemaVersion,
		Contract:         agentModeFixtureContract,
		MediaType:        "application/json",
		Encoding:         "utf-8-json-lf",
		GeneratorVersion: agentModeFixtureGeneratorVersion,
		Fixtures:         entries,
	}
	manifestRaw, err := json.Marshal(manifest)
	if err != nil {
		t.Fatalf("marshal fixture manifest: %v", err)
	}
	manifestRaw = append(manifestRaw, '\n')

	// This opt-in output is stdout-only. Fixture updates still go through
	// repository review; the normal test path never writes source files.
	if os.Getenv("PAIMOS_EMIT_AGENT_MODE_FIXTURES") == "1" {
		for _, entry := range entries {
			fmt.Printf("PAIMOS_AGENT_MODE_FIXTURE %s %s\n", entry.File,
				base64.StdEncoding.EncodeToString(expected[entry.File]))
		}
		fmt.Printf("PAIMOS_AGENT_MODE_FIXTURE manifest-v1.json %s\n",
			base64.StdEncoding.EncodeToString(manifestRaw))
		return
	}

	directory := filepath.Join("..", "contracts", "fixtures", "agent-mode")
	assertFixtureInventory(t, directory, entries)
	schemas := loadAgentModeOpenAPISchemas(t)
	for _, entry := range entries {
		entry := entry
		t.Run(entry.File, func(t *testing.T) {
			raw, err := os.ReadFile(filepath.Join(directory, entry.File)) // #nosec G304 -- manifest-bound in-repo fixture.
			if err != nil {
				t.Fatalf("read canonical fixture: %v", err)
			}
			if !bytes.Equal(raw, expected[entry.File]) {
				t.Fatalf("%s drifted from deterministic DB-to-Reader encoding", entry.File)
			}
			if len(raw) != entry.Bytes {
				t.Fatalf("%s byte length=%d, want %d", entry.File, len(raw), entry.Bytes)
			}
			digest := sha256.Sum256(raw)
			if hex.EncodeToString(digest[:]) != entry.SHA256 {
				t.Fatalf("%s digest mismatch", entry.File)
			}
			if bytes.Contains(raw, []byte("selected_outside_results")) || bytes.Contains(raw, []byte("stream_cursor")) {
				t.Fatalf("%s contains a removed wire alias", entry.File)
			}
			decoder := json.NewDecoder(bytes.NewReader(raw))
			decoder.DisallowUnknownFields()
			var snapshot Snapshot
			if err := decoder.Decode(&snapshot); err != nil {
				t.Fatalf("decode through production Snapshot DTO: %v", err)
			}
			if err := requireJSONEOF(decoder); err != nil {
				t.Fatal(err)
			}
			if snapshot.SchemaVersion != SchemaVersion || len(snapshot.Rows) != entry.Rows ||
				len(snapshot.Cursor) != CursorEncodedLength || !snapshot.ServerTime.Equal(snapshot.Aggregates.CalculatedAt) {
				t.Fatalf("invalid fixture envelope: schema=%d rows=%d cursor=%d server=%s calculated=%s",
					snapshot.SchemaVersion, len(snapshot.Rows), len(snapshot.Cursor), snapshot.ServerTime, snapshot.Aggregates.CalculatedAt)
			}
			activeIDs := make(map[string]bool, len(snapshot.Rows))
			for _, row := range snapshot.Rows {
				if activeIDs[row.DeliveryID] {
					t.Fatalf("duplicate production delivery_id %q", row.DeliveryID)
				}
				activeIDs[row.DeliveryID] = true
			}
			if err := ValidateAggregates(snapshot.Aggregates, activeIDs); err != nil {
				t.Fatalf("validate production aggregate DTO: %v", err)
			}
			var tree any
			if err := decodeStrictJSON(raw, &tree); err != nil {
				t.Fatal(err)
			}
			if err := fixtureSafeTreeError("$", tree); err != nil {
				t.Fatal(err)
			}
			if err := validateFixtureSchema("$", tree, schemas["AgentModeSnapshot"], schemas); err != nil {
				t.Fatalf("validate fixture against recursive OpenAPI schema: %v", err)
			}
		})
	}

	committedManifest, err := os.ReadFile(filepath.Join(directory, "manifest-v1.json"))
	if err != nil {
		t.Fatalf("read fixture manifest: %v", err)
	}
	var decodedManifest agentModeFixtureManifest
	if err := decodeStrictJSON(committedManifest, &decodedManifest); err != nil {
		t.Fatalf("strict manifest decode: %v", err)
	}
	if !reflect.DeepEqual(decodedManifest, manifest) || !bytes.Equal(committedManifest, manifestRaw) {
		t.Fatal("manifest-v1.json does not exactly match sorted fixture inventory, lengths, and SHA-256 digests")
	}
}

func TestCanonicalAgentModeEmptyHistoryKeepsSelectedDeliveryKey(t *testing.T) {
	snapshot := canonicalAgentModeFixtureSnapshot(t, 0)
	if len(snapshot.Rows) != 0 || snapshot.SelectedDelivery != "" || snapshot.SelectedOutside != nil {
		t.Fatalf("empty production snapshot=%+v", snapshot)
	}
	var wire map[string]json.RawMessage
	if err := json.Unmarshal(marshalFixtureSnapshot(t, snapshot), &wire); err != nil {
		t.Fatal(err)
	}
	if selected, ok := wire["selected_delivery"]; !ok || string(selected) != `""` {
		t.Fatalf("fixed selected_delivery key present=%t value=%s", ok, selected)
	}
	if _, ok := wire["selected_outside"]; ok {
		t.Fatal("selected_outside is the sole optional top-level field")
	}
}

func TestAgentModeFixtureSchemaValidatorRejectsNestedWireMutations(t *testing.T) {
	schemas := loadAgentModeOpenAPISchemas(t)
	ten := fixtureTree(t, canonicalAgentModeFixtureSnapshot(t, 10))
	assertValid := func(t *testing.T, tree any) {
		t.Helper()
		if err := validateFixtureSchema("$", tree, schemas["AgentModeSnapshot"], schemas); err != nil {
			t.Fatalf("production fixture must satisfy recursive OpenAPI schema: %v", err)
		}
	}
	assertValid(t, ten)

	tests := []struct {
		name string
		base any
		edit func(map[string]any)
	}{
		{name: "row_null_tags", base: ten, edit: func(root map[string]any) {
			root["rows"].([]any)[0].(map[string]any)["tags"] = nil
		}},
		{name: "outside_row_null_tags", base: ten, edit: func(root map[string]any) {
			root["selected_outside"].(map[string]any)["row"].(map[string]any)["tags"] = nil
		}},
		{name: "nested_required_owner_missing", base: ten, edit: func(root map[string]any) {
			delete(root["rows"].([]any)[0].(map[string]any)["stages"].([]any)[0].(map[string]any), "owner")
		}},
		{name: "required_array_null", base: ten, edit: func(root map[string]any) {
			root["rows"].([]any)[0].(map[string]any)["evidence"] = nil
		}},
		{name: "arbitrary_trust_suppression", base: ten, edit: func(root map[string]any) {
			root["rows"].([]any)[0].(map[string]any)["trust"].(map[string]any)["suppression"] = "arbitrary"
		}},
		{name: "arbitrary_trust_flag", base: ten, edit: func(root map[string]any) {
			root["rows"].([]any)[0].(map[string]any)["trust"].(map[string]any)["flags"] = []any{"arbitrary"}
		}},
		{name: "missing_aggregate_flag", base: ten, edit: func(root map[string]any) {
			flags := root["aggregates"].(map[string]any)["root"].(map[string]any)["flags"].(map[string]any)
			delete(flags, "blocked")
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			mutant := cloneFixtureTree(t, test.base)
			root, ok := mutant.(map[string]any)
			if !ok {
				t.Fatal("fixture root is not an object")
			}
			test.edit(root)
			if err := validateFixtureSchema("$", mutant, schemas["AgentModeSnapshot"], schemas); err == nil {
				t.Fatal("recursive schema validator accepted wire mutant")
			}
		})
	}
}

func TestAgentModeFixtureSafetyGuardRejectsInternalKeysAndUnsafeText(t *testing.T) {
	keys := make([]string, 0, len(forbiddenFixtureKeys))
	for key := range forbiddenFixtureKeys {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		t.Run("key_"+key, func(t *testing.T) {
			if err := fixtureSafeTreeError("$", map[string]any{"nested": map[string]any{key: "opaque"}}); err == nil {
				t.Fatalf("fixture safety guard accepted forbidden key %q", key)
			}
		})
	}
	for family, key := range map[string]string{
		"provider":        "provider_label",
		"adapter":         "adapter_version",
		"agent":           "agent_profile",
		"reporter":        "reporter_session",
		"capability":      "capability_metadata",
		"estimate":        "estimate_eta_seconds",
		"context":         "context_window",
		"log":             "log_excerpt",
		"followup":        "followup_prompt",
		"instrumentation": "instrumentation_version",
		"model":           "model_version",
		"profile":         "profile_name",
		"test":            "test_output",
		"repository":      "repo_url",
		"branch":          "branch_name",
		"commit":          "commit_sha",
		"token":           "input_tokens",
	} {
		t.Run("family_"+family, func(t *testing.T) {
			if err := fixtureSafeTreeError("$", map[string]any{"nested": map[string]any{key: "opaque"}}); err == nil {
				t.Fatalf("fixture safety guard accepted forbidden %s-family key %q", family, key)
			}
		})
	}
	for name, value := range map[string]string{
		"secret":  "DB_PASSWORD=fixture-secret",
		"nul":     "unsafe\x00text",
		"newline": "unsafe\ntext",
		"return":  "unsafe\rtext",
	} {
		t.Run(name, func(t *testing.T) {
			if err := fixtureSafeTreeError("$", map[string]any{"title": value}); err == nil {
				t.Fatalf("fixture safety guard accepted unsafe text %q", value)
			}
		})
	}
	for _, key := range []string{"status", "source", "capabilities"} {
		t.Run("path_"+key, func(t *testing.T) {
			if err := fixtureSafeTreeError("$", map[string]any{key: "raw-internal-value"}); err == nil {
				t.Fatalf("fixture safety guard accepted path-sensitive internal key %q at root", key)
			}
		})
	}
	if err := fixtureSafeTreeError("$", map[string]any{
		"title": "Passwordless credential rotation", "rows": []any{map[string]any{
			"capabilities": map[string]any{"view_issue": true},
			"stages":       []any{map[string]any{"status": "active"}},
			"evidence":     []any{map[string]any{"status": "passed"}},
			"progress":     map[string]any{"source": "owner_estimate"},
		}}, "reporter_kind": "external", "status_text": "Tooling ready",
	}); err != nil {
		t.Fatalf("fixture safety guard rejected public safe controls: %v", err)
	}
}

func fixtureTree(t *testing.T, snapshot Snapshot) any {
	t.Helper()
	var tree any
	if err := decodeStrictJSON(marshalFixtureSnapshot(t, snapshot), &tree); err != nil {
		t.Fatal(err)
	}
	return tree
}

func cloneFixtureTree(t *testing.T, value any) any {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	var clone any
	if err := decodeStrictJSON(raw, &clone); err != nil {
		t.Fatal(err)
	}
	return clone
}

func canonicalAgentModeFixtureSnapshot(t *testing.T, count int) Snapshot {
	t.Helper()
	database := openAgentModeTestDB(t)
	calculatedAt := time.Date(2026, time.August, 20, 12, 0, 0, 0, time.UTC)
	userID := insertAgentModeUser(t, database, fmt.Sprintf("fixture-admin-%d", count), "admin", "admin")
	request := Request{UserID: userID, Filters: Filters{Attention: "all", Health: "all"}}

	switch count {
	case 0:
	case 1:
		projectID := insertFixtureProject(t, database, "Fixture One", "FX1")
		issueID := insertFixtureIssue(t, database, projectID, 1, "Instrumented fixture delivery", calculatedAt.Add(-30*time.Minute))
		seedFixtureTag(t, database, issueID, "agent-mode")
		seedCanonicalFixtureDelivery(t, database, userID, issueID, calculatedAt.Add(-30*time.Second), "rich")
		request.RouteProjectID = &projectID
		request.Filters.SelectedDelivery = fmt.Sprintf("issue:%d", issueID)
	case 10:
		projectID := insertFixtureProject(t, database, "Fixture Ten", "FX10")
		outsideProjectID := insertFixtureProject(t, database, "Fixture Ten Outside", "FX10O")
		modes := []string{"estimate_2h_high", "estimate_12h_medium", "estimate_48h_high", "estimate_96h_high",
			"estimate_10h_low", "mixed", "failed", "stale", "deployed", "legacy"}
		for index, mode := range modes {
			issueID := insertFixtureIssue(t, database, projectID, int64(index+1),
				fmt.Sprintf("Fixture state %02d %s", index+1, mode), calculatedAt.Add(-time.Duration(index+1)*time.Minute))
			if mode == "legacy" {
				seedLegacyFixtureRun(t, database, userID, issueID, projectID, calculatedAt.Add(-5*time.Minute), index+1)
				continue
			}
			eventAt := calculatedAt.Add(-30 * time.Second)
			if mode == "stale" {
				eventAt = calculatedAt.Add(-10 * time.Minute)
			}
			seedCanonicalFixtureDelivery(t, database, userID, issueID, eventAt, mode)
		}
		outsideIssueID := insertFixtureIssue(t, database, outsideProjectID, 1,
			"Explicit selection outside the project filter", calculatedAt.Add(-time.Minute))
		seedCanonicalFixtureDelivery(t, database, userID, outsideIssueID, calculatedAt.Add(-20*time.Second), "cancelled")
		request.Filters.ProjectID = &projectID
		request.Filters.SelectedDelivery = fmt.Sprintf("issue:%d", outsideIssueID)
	case 100:
		projectA := insertFixtureProject(t, database, "Fixture Hundred Alpha", "FX100A")
		projectB := insertFixtureProject(t, database, "Fixture Hundred Beta", "FX100B")
		outsideProject := insertFixtureProject(t, database, "Fixture Hundred Outside", "FX100O")
		for index := 0; index < count; index++ {
			projectID := projectA
			projectKey := "alpha"
			if index%2 == 1 {
				projectID, projectKey = projectB, "beta"
			}
			issueID := insertFixtureIssue(t, database, projectID, int64(index/2+1),
				fmt.Sprintf("Density fixture %s %03d", projectKey, index+1), calculatedAt.Add(-time.Duration(index+1)*time.Minute))
			seedLegacyFixtureRun(t, database, userID, issueID, projectID, calculatedAt.Add(-5*time.Minute), index+1)
		}
		outsideIssueID := insertFixtureIssue(t, database, outsideProject, 1,
			"Selected delivery excluded from density rows", calculatedAt.Add(-time.Minute))
		seedCanonicalFixtureDelivery(t, database, userID, outsideIssueID, calculatedAt.Add(-20*time.Second), "pending")
		request.Filters.States = []string{"unknown"}
		request.Filters.SelectedDelivery = fmt.Sprintf("issue:%d", outsideIssueID)
	default:
		t.Fatalf("unsupported canonical fixture row count %d", count)
	}

	codec := NewCursorCodecWithCrypt(delivery.ClockFunc(func() time.Time { return calculatedAt }), 15*time.Minute,
		func(_ string, plain []byte) ([]byte, error) {
			digest := sha256.Sum256(plain)
			sealed := make([]byte, cursorCiphertextLength)
			sealed[0] = 1
			for index := 1; index < len(sealed); index++ {
				sealed[index] = digest[(index-1)%len(digest)] ^ byte((index-1)/len(digest))
			}
			return sealed, nil
		}, nil)
	reader := NewReader(database, ReaderOptions{Clock: delivery.ClockFunc(func() time.Time { return calculatedAt }), Cursor: codec})
	snapshot, err := reader.Read(context.Background(), request)
	if err != nil {
		t.Fatalf("production Reader fixture %d: %v", count, err)
	}
	return snapshot
}

func insertFixtureProject(t *testing.T, database *sql.DB, name, key string) int64 {
	t.Helper()
	result, err := database.Exec(`INSERT INTO projects(name,key,status) VALUES(?,?,'active')`, name, key)
	if err != nil {
		t.Fatal(err)
	}
	id, _ := result.LastInsertId()
	return id
}

func insertFixtureIssue(t *testing.T, database *sql.DB, projectID, number int64, title string, updatedAt time.Time) int64 {
	t.Helper()
	timestamp := updatedAt.UTC().Format(time.RFC3339Nano)
	result, err := database.Exec(`INSERT INTO issues(project_id,issue_number,type,title,status,created_at,updated_at)
		VALUES(?,?,'ticket',?,'in-progress',?,?)`, projectID, number, title, timestamp, timestamp)
	if err != nil {
		t.Fatal(err)
	}
	id, _ := result.LastInsertId()
	return id
}

func seedFixtureTag(t *testing.T, database *sql.DB, issueID int64, name string) {
	t.Helper()
	result, err := database.Exec(`INSERT INTO tags(name) VALUES(?)`, name)
	if err != nil {
		t.Fatal(err)
	}
	tagID, _ := result.LastInsertId()
	if _, err := database.Exec(`INSERT INTO issue_tags(issue_id,tag_id) VALUES(?,?)`, issueID, tagID); err != nil {
		t.Fatal(err)
	}
}

func seedLegacyFixtureRun(t *testing.T, database *sql.DB, userID, issueID, projectID int64, eventAt time.Time, ordinal int) {
	t.Helper()
	timestamp := eventAt.UTC().Format(time.RFC3339Nano)
	if _, err := database.Exec(`INSERT INTO agent_runs(issue_id,project_id,requested_by,status,agent_name,
		delivery_instrumentation_version,created_at,started_at) VALUES(?,?,?,'running',?,0,?,?)`,
		issueID, projectID, userID, fmt.Sprintf("fixture-agent-%03d", ordinal), timestamp, timestamp); err != nil {
		t.Fatal(err)
	}
}

func seedCanonicalFixtureDelivery(t *testing.T, database *sql.DB, userID, issueID int64, eventAt time.Time, mode string) {
	t.Helper()
	store := delivery.NewStore(database, delivery.Options{Clock: delivery.ClockFunc(func() time.Time { return eventAt }),
		Authorizer: delivery.AuthorizerFunc(func(context.Context, delivery.AuthorizationRequest) error { return nil })})
	policies := delivery.DefaultPolicy()
	if strings.HasPrefix(mode, "estimate_") {
		for index := 1; index < len(policies); index++ {
			policies[index].Applicability = "not_applicable"
			policies[index].PolicyReference = "policy:fixture-trust-range"
			policies[index].ReasonCode = "fixture_not_applicable"
			policies[index].ReasonText = "Fixture isolates the current owner estimate"
		}
	}
	if mode == "rich" {
		for index := 2; index < len(policies); index++ {
			policies[index].Applicability = "not_applicable"
			policies[index].PolicyReference = "policy:fixture-rich-estimate"
			policies[index].ReasonCode = "fixture_not_applicable"
			policies[index].ReasonText = "Fixture keeps specification evidence and an implementation estimate"
		}
	}
	attempt, err := store.StartAttempt(context.Background(), delivery.AttemptRequest{IssueID: issueID,
		Actor: delivery.Actor{Type: "user", OpaqueKey: fmt.Sprintf("user:%d", userID)}, Policies: policies,
		ReasonCode: "instrumentation", IdempotencyKey: fmt.Sprintf("fixture-%d-attempt", issueID)})
	if err != nil {
		t.Fatal(err)
	}
	if mode == "pending" {
		return
	}
	reporter := delivery.Actor{Type: "external", OpaqueKey: fmt.Sprintf("external:fixture-%d", issueID)}
	if mode == "user" {
		reporter = delivery.Actor{Type: "user", OpaqueKey: fmt.Sprintf("user:%d", userID)}
	}
	stage, err := store.StartStageRetry(context.Background(), delivery.StageStartRequest{IssueID: issueID,
		AttemptNumber: attempt.AttemptNumber, StageKey: delivery.StageSpecification, Reporter: reporter,
		ReasonCode: "specification_start", IdempotencyKey: fmt.Sprintf("fixture-%d-spec-start", issueID)})
	if err != nil {
		t.Fatal(err)
	}
	sequence := int64(1)
	report := func(value delivery.StageReport) {
		t.Helper()
		value.IssueID = issueID
		value.AttemptNumber = attempt.AttemptNumber
		value.StageKey = stage.StageKey
		value.ExecutionNumber = stage.ExecutionNumber
		value.AuthorityEpoch = stage.AuthorityEpoch
		value.Reporter = reporter
		value.SourceSequence = &sequence
		if _, err := store.ReportStage(context.Background(), value); err != nil {
			t.Fatal(err)
		}
		sequence++
	}
	switch mode {
	case "rich":
		var title, description, criteria string
		if err := database.QueryRow(`SELECT title,description,acceptance_criteria FROM issues WHERE id=?`, issueID).
			Scan(&title, &description, &criteria); err != nil {
			t.Fatal(err)
		}
		digest := sha256.Sum256([]byte(strings.Join([]string{"paimos-issue-spec-v1", title, description, criteria}, "\x00")))
		report(delivery.StageReport{IdempotencyKey: fmt.Sprintf("fixture-%d-spec-pass", issueID), Kind: "semantic",
			State: "succeeded", Activity: "Specification accepted", Evidence: []delivery.Evidence{{Type: "spec_acceptance",
				Outcome: "passed", ReferenceKind: "digest", DigestSHA256: hex.EncodeToString(digest[:])}}})
		implReporter := delivery.Actor{Type: "external", OpaqueKey: fmt.Sprintf("external:fixture-%d-implementation", issueID)}
		implementation, err := store.StartStageRetry(context.Background(), delivery.StageStartRequest{IssueID: issueID,
			AttemptNumber: attempt.AttemptNumber, StageKey: delivery.StageImplementation, Reporter: implReporter,
			ReasonCode: "implementation_start", IdempotencyKey: fmt.Sprintf("fixture-%d-impl-start", issueID)})
		if err != nil {
			t.Fatal(err)
		}
		revision, progress, confidence := int64(1), float64(35), float64(.85)
		eta, etaMin, etaMax := int64(6*60*60), int64(5*60*60), int64(8*60*60)
		if _, err := store.ReportStage(context.Background(), delivery.StageReport{IssueID: issueID,
			AttemptNumber: attempt.AttemptNumber, StageKey: delivery.StageImplementation,
			ExecutionNumber: implementation.ExecutionNumber, AuthorityEpoch: implementation.AuthorityEpoch,
			Reporter: implReporter, SourceSequence: fixtureInt64Pointer(1), IdempotencyKey: fmt.Sprintf("fixture-%d-impl-estimate", issueID),
			Kind: "estimate", Estimate: delivery.EstimateEvidence{Revision: &revision, Progress: &progress,
				ETASeconds: &eta, ETAMin: &etaMin, ETAMax: &etaMax, Source: "external", Confidence: &confidence,
				Basis: "Fixture implementation estimate"}}); err != nil {
			t.Fatal(err)
		}
	case "waiting_only":
		report(delivery.StageReport{IdempotencyKey: fmt.Sprintf("fixture-%d-human-wait", issueID), Kind: "semantic",
			State: "waiting", Activity: "Waiting for approval", NeedsInput: true,
			Blockers: []delivery.Blocker{{Key: "fixture-approval", Class: "input",
				Summary: "Waiting for fixture approval", HumanWait: true}}})
	case "dependency":
		report(delivery.StageReport{IdempotencyKey: fmt.Sprintf("fixture-%d-dependency", issueID), Kind: "semantic",
			State: "waiting", Activity: "Waiting for a dependency", Blockers: []delivery.Blocker{{Key: "fixture-dependency",
				Class: "dependency", Summary: "Waiting on fixture dependency"}}})
	case "mixed":
		report(delivery.StageReport{IdempotencyKey: fmt.Sprintf("fixture-%d-mixed-wait", issueID), Kind: "semantic",
			State: "waiting", Activity: "Waiting for approval", NeedsInput: true,
			Blockers: []delivery.Blocker{{Key: "fixture-approval", Class: "input",
				Summary: "Waiting for fixture approval", HumanWait: true}, {Key: "fixture-dependency", Class: "dependency",
				Summary: "Waiting on fixture dependency"}}})
	case "failed":
		report(delivery.StageReport{IdempotencyKey: fmt.Sprintf("fixture-%d-failed", issueID), Kind: "semantic",
			State: "failed", Activity: "Specification retry required"})
	case "estimate_2h_high", "estimate_12h_medium", "estimate_48h_high", "estimate_96h_high", "estimate_10h_low":
		config := map[string]struct {
			hours      int64
			confidence float64
		}{
			"estimate_2h_high":    {2, .90},
			"estimate_12h_medium": {12, .65},
			"estimate_48h_high":   {48, .90},
			"estimate_96h_high":   {96, .90},
			"estimate_10h_low":    {10, .25},
		}[mode]
		confidence := config.confidence
		revision, progress := int64(1), float64(45)
		eta := config.hours * 60 * 60
		etaMin, etaMax := eta-30*60, eta+30*60
		report(delivery.StageReport{IdempotencyKey: fmt.Sprintf("fixture-%d-%s", issueID, mode), Kind: "estimate",
			Estimate: delivery.EstimateEvidence{Revision: &revision, Progress: &progress, ETASeconds: &eta,
				ETAMin: &etaMin, ETAMax: &etaMax, Source: "external", Confidence: &confidence, Basis: "Fixture owner estimate"}})
		if mode == "estimate_2h_high" {
			report(delivery.StageReport{IdempotencyKey: fmt.Sprintf("fixture-%d-estimate-heartbeat", issueID), Kind: "heartbeat"})
		}
	case "deployed":
		var title, description, criteria string
		if err := database.QueryRow(`SELECT title,description,acceptance_criteria FROM issues WHERE id=?`, issueID).
			Scan(&title, &description, &criteria); err != nil {
			t.Fatal(err)
		}
		digest := sha256.Sum256([]byte(strings.Join([]string{"paimos-issue-spec-v1", title, description, criteria}, "\x00")))
		report(delivery.StageReport{IdempotencyKey: fmt.Sprintf("fixture-%d-deployed-spec", issueID), Kind: "semantic",
			State: "succeeded", Evidence: []delivery.Evidence{{Type: "spec_acceptance", Outcome: "passed",
				ReferenceKind: "digest", DigestSHA256: hex.EncodeToString(digest[:])}}})
		for _, next := range []struct{ stage, evidence string }{
			{delivery.StageImplementation, "implementation_result"},
			{delivery.StageQA, "test_result"},
			{delivery.StageDeployment, "deployment_result"},
		} {
			owner := delivery.Actor{Type: "external", OpaqueKey: fmt.Sprintf("external:fixture-%d-%s", issueID, next.stage)}
			ref, err := store.StartStageRetry(context.Background(), delivery.StageStartRequest{IssueID: issueID,
				AttemptNumber: attempt.AttemptNumber, StageKey: next.stage, Reporter: owner,
				ReasonCode: next.stage + "_start", IdempotencyKey: fmt.Sprintf("fixture-%d-%s-start", issueID, next.stage)})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := store.ReportStage(context.Background(), delivery.StageReport{IssueID: issueID,
				AttemptNumber: attempt.AttemptNumber, StageKey: next.stage, ExecutionNumber: ref.ExecutionNumber,
				AuthorityEpoch: ref.AuthorityEpoch, Reporter: owner, SourceSequence: fixtureInt64Pointer(1),
				IdempotencyKey: fmt.Sprintf("fixture-%d-%s-pass", issueID, next.stage), Kind: "semantic", State: "succeeded",
				Evidence: []delivery.Evidence{{Type: next.evidence, Outcome: "passed", ReferenceKind: "external_ref",
					ReferenceValue: fmt.Sprintf("fixture:%d:%s", issueID, next.stage)}}}); err != nil {
				t.Fatal(err)
			}
		}
	case "cancelled":
		report(delivery.StageReport{IdempotencyKey: fmt.Sprintf("fixture-%d-cancelled", issueID), Kind: "semantic",
			State: "cancelled", Activity: "Fixture cancelled"})
	case "stale", "user":
		// The production freshness/trust reducers derive stale/no-signal and
		// unknown-reporter truth from the immutable execution start.
	default:
		t.Fatalf("unsupported canonical fixture mode %q", mode)
	}
}

func fixtureInt64Pointer(value int64) *int64 { return &value }

func marshalFixtureSnapshot(t *testing.T, snapshot Snapshot) []byte {
	t.Helper()
	raw, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatalf("marshal canonical Agent Mode fixture: %v", err)
	}
	return append(raw, '\n')
}

func validateCanonicalFixtureCases(t *testing.T, count int, snapshot Snapshot) {
	t.Helper()
	if len(snapshot.Rows) != count || len(snapshot.Cursor) != CursorEncodedLength ||
		!snapshot.ServerTime.Equal(snapshot.Aggregates.CalculatedAt) {
		t.Fatalf("fixture %d envelope=%+v", count, snapshot)
	}
	switch count {
	case 1:
		row := snapshot.Rows[0]
		if snapshot.SelectedDelivery != row.DeliveryID || snapshot.SelectedOutside != nil || row.AttemptID == nil ||
			len(row.Stages) != len(delivery.CanonicalStages) || len(row.Evidence) == 0 || row.Trust.Scope == nil ||
			row.Capabilities.LiveNote || row.Actor == nil || row.Actor.Name != "external" || row.Progress == nil ||
			row.ETA == nil || row.Progress.Percent == nil || row.Progress.Source == nil || row.Progress.Basis == nil ||
			row.ETA.LandingAt == nil || row.ETA.OptimisticAt == nil || row.ETA.PessimisticAt == nil ||
			!row.Progress.Trusted || !row.ETA.Trusted || row.Trust.Suppression != "" || row.Trust.Basis == "" ||
			row.Trust.LandingAt == nil || row.Trust.OptimisticLandingAt == nil || row.Trust.PessimisticLandingAt == nil {
			t.Fatalf("one-row fixture is not a complete production projection: %+v", row)
		}
		if !row.ETA.OptimisticAt.Before(*row.ETA.LandingAt) || !row.ETA.LandingAt.Before(*row.ETA.PessimisticAt) {
			t.Fatalf("one-row fixture ETA bounds are not ordered: %+v", row.ETA)
		}
		for index, stage := range row.Stages {
			if stage.Key != delivery.CanonicalStages[index] {
				t.Fatalf("canonical stage %d=%q", index, stage.Key)
			}
		}
	case 10:
		if snapshot.SelectedOutside == nil || snapshot.SelectedOutside.Reason != SelectedTerminal ||
			snapshot.SelectedDelivery != snapshot.SelectedOutside.Row.DeliveryID {
			t.Fatalf("ten-row fixture selection=%q outside=%+v", snapshot.SelectedDelivery, snapshot.SelectedOutside)
		}
		if snapshot.SelectedOutside.Row.AttemptStatus != "cancelled" {
			t.Fatalf("ten-row terminal selection status=%q", snapshot.SelectedOutside.Row.AttemptStatus)
		}
		statuses, reasons, confidences, sources := map[string]bool{}, map[string]bool{}, map[string]bool{}, map[string]bool{}
		health, freshness, activities, stageStatuses := map[Health]bool{}, map[string]bool{}, map[string]bool{}, map[string]bool{}
		hasSuppression, hasFlag, blockedActivity := false, false, false
		for _, row := range snapshot.Rows {
			statuses[row.AttemptStatus], reasons[row.Attention.Reason] = true, true
			confidences[row.Trust.ConfidenceLabel], sources[row.Trust.SourceKind] = true, true
			health[row.Health], freshness[row.Freshness.State], activities[row.Activity.Kind] = true, true, true
			for _, stage := range row.Stages {
				stageStatuses[stage.Status] = true
			}
			hasSuppression = hasSuppression || row.Trust.Suppression != ""
			hasFlag = hasFlag || len(row.Trust.Flags) != 0
			blockedActivity = blockedActivity || (row.Attention.Reason == "blocked" && row.Activity.Kind == "blocked")
		}
		for _, value := range []string{"active", "failed_needs_retry", "deployed_unverified", "unknown"} {
			if !statuses[value] {
				t.Errorf("ten-row fixture lacks attempt status %q: %v", value, statuses)
			}
		}
		for _, value := range []string{"blocked", "failed_needs_retry", "stale_no_signal", "unknown_reporter", "deployed_unverified"} {
			if !reasons[value] {
				t.Errorf("ten-row fixture lacks attention reason %q: %v", value, reasons)
			}
		}
		for _, value := range []Health{HealthHealthy, HealthAttention, HealthBlocked, HealthStale, HealthUnknown} {
			if !health[value] {
				t.Errorf("ten-row fixture lacks health %q: %v", value, health)
			}
		}
		for _, value := range []string{"fresh", "aging", "stale", "unknown"} {
			if !freshness[value] {
				t.Errorf("ten-row fixture lacks freshness %q: %v", value, freshness)
			}
		}
		for _, value := range []string{"working", "blocked", "idle", "unknown"} {
			if !activities[value] {
				t.Errorf("ten-row fixture lacks activity %q: %v", value, activities)
			}
		}
		for _, value := range []string{"active", "blocked", "failed", "pending", "not_applicable", "succeeded"} {
			if !stageStatuses[value] {
				t.Errorf("ten-row fixture lacks stage status %q: %v", value, stageStatuses)
			}
		}
		root := snapshot.Aggregates.Root
		if root.ActiveTotal != 10 || root.CurrentStage.Specification != 8 || root.CurrentStage.Verification != 1 ||
			root.CurrentStage.Unknown != 1 || root.Landing.Within4Hours != 1 || root.Landing.Within24Hours != 1 ||
			root.Landing.Within3Days != 1 || root.Landing.Later != 1 || root.Landing.RangeOnly != 1 ||
			root.Landing.SuppressedOrUnknown != 5 || root.Flags.Attention != 5 || root.Flags.WaitingNeedsInput != 1 ||
			root.Flags.Blocked != 1 || root.Flags.StaleNoSignal != 1 || root.Flags.FailedNeedsRetry != 1 ||
			root.Flags.DeployedUnverified != 1 || root.Flags.Unverified != 1 || root.Flags.UnknownReporter != 1 {
			t.Fatalf("ten-row exact aggregate matrix=%+v", root)
		}
		if !confidences["low"] || !confidences["medium"] || !confidences["high"] ||
			!sources["owner_estimate"] || !sources["stage_evidence"] ||
			!hasSuppression || !hasFlag || !blockedActivity {
			t.Fatalf("ten-row trust/activity matrix confidence=%v source=%v suppression=%t flag=%t blocked=%t",
				confidences, sources, hasSuppression, hasFlag, blockedActivity)
		}
	case 100:
		if snapshot.SelectedOutside == nil || snapshot.SelectedOutside.Reason != SelectedFilterExcluded ||
			snapshot.SelectedDelivery != snapshot.SelectedOutside.Row.DeliveryID || len(snapshot.Aggregates.Projects) != 2 ||
			snapshot.Aggregates.Attention.Total != count || len(snapshot.Aggregates.Attention.Items) != MaxAttentionItems {
			t.Fatalf("hundred-row density/selection/aggregate contract: selected=%q outside=%+v projects=%d attention=%+v",
				snapshot.SelectedDelivery, snapshot.SelectedOutside, len(snapshot.Aggregates.Projects), snapshot.Aggregates.Attention)
		}
	}
}

func assertFixtureInventory(t *testing.T, directory string, entries []agentModeFixtureManifestEntry) {
	t.Helper()
	directoryEntries, err := os.ReadDir(directory)
	if err != nil {
		t.Fatalf("read fixture directory: %v", err)
	}
	actual := make([]string, 0, len(directoryEntries))
	for _, entry := range directoryEntries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			t.Fatalf("unexpected non-JSON fixture entry %q", entry.Name())
		}
		actual = append(actual, entry.Name())
	}
	sort.Strings(actual)
	want := []string{"manifest-v1.json"}
	for _, entry := range entries {
		want = append(want, entry.File)
	}
	sort.Strings(want)
	if !reflect.DeepEqual(actual, want) {
		t.Fatalf("fixture inventory actual=%v want=%v", actual, want)
	}
}

func loadAgentModeOpenAPISchemas(t *testing.T) map[string]any {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "handlers", "openapi.json"))
	if err != nil {
		t.Fatal(err)
	}
	var document struct {
		Components struct {
			Schemas map[string]any `json:"schemas"`
		} `json:"components"`
	}
	if err := json.Unmarshal(raw, &document); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{
		"AgentModeSnapshot", "AgentModeSelectedOutside", "AgentModeDelivery", "AgentModeActor", "AgentModeActivity",
		"AgentModeStageSummary", "AgentModeStage", "AgentModeEvidence", "AgentModeBlocker", "AgentModeCapabilities",
		"AgentModeAttention", "AgentModeFreshness", "AgentModeProgress", "AgentModeETA", "AgentModeTrustScope",
		"AgentModeTrust", "AgentModeStageCounts", "AgentModeLandingCounts", "AgentModeCountFlags", "AgentModeCountSet",
		"AgentModeLaneAggregate", "AgentModeProjectAggregate", "AgentModeAttentionItem", "AgentModeAggregates",
	} {
		if _, ok := document.Components.Schemas[name].(map[string]any); !ok {
			t.Fatalf("OpenAPI schema %s is missing or not an object", name)
		}
	}
	return document.Components.Schemas
}

func validateFixtureSchema(path string, value any, rawSchema any, schemas map[string]any) error {
	schema, ok := rawSchema.(map[string]any)
	if !ok {
		return fmt.Errorf("%s schema is not an object", path)
	}
	if ref, ok := schema["$ref"].(string); ok {
		const prefix = "#/components/schemas/"
		if !strings.HasPrefix(ref, prefix) {
			return fmt.Errorf("%s has unsupported schema reference %q", path, ref)
		}
		name := strings.TrimPrefix(ref, prefix)
		target, exists := schemas[name]
		if !exists {
			return fmt.Errorf("%s references missing schema %q", path, name)
		}
		return validateFixtureSchema(path, value, target, schemas)
	}
	if branches, ok := schema["oneOf"].([]any); ok {
		matches := 0
		for _, branch := range branches {
			if validateFixtureSchema(path, value, branch, schemas) == nil {
				matches++
			}
		}
		if matches != 1 {
			return fmt.Errorf("%s matches %d oneOf branches, want exactly one", path, matches)
		}
		return nil
	}
	if expected, ok := schema["const"]; ok && !reflect.DeepEqual(value, expected) {
		return fmt.Errorf("%s value %v does not match const %v", path, value, expected)
	}
	if values, ok := schema["enum"].([]any); ok {
		matched := false
		for _, candidate := range values {
			matched = matched || reflect.DeepEqual(value, candidate)
		}
		if !matched {
			return fmt.Errorf("%s value %v is outside enum %v", path, value, values)
		}
	}
	typeName, _ := schema["type"].(string)
	switch typeName {
	case "null":
		if value != nil {
			return fmt.Errorf("%s is %T, want null", path, value)
		}
	case "object":
		object, ok := value.(map[string]any)
		if !ok {
			return fmt.Errorf("%s is %T, want object", path, value)
		}
		if required, ok := schema["required"].([]any); ok {
			for _, rawKey := range required {
				key, _ := rawKey.(string)
				if _, exists := object[key]; !exists {
					return fmt.Errorf("%s lacks required key %q", path, key)
				}
			}
		}
		properties, _ := schema["properties"].(map[string]any)
		if additional, ok := schema["additionalProperties"].(bool); ok && !additional {
			for key := range object {
				if _, exists := properties[key]; !exists {
					return fmt.Errorf("%s contains undocumented key %q", path, key)
				}
			}
		}
		for key, childSchema := range properties {
			if child, exists := object[key]; exists {
				if err := validateFixtureSchema(path+"."+key, child, childSchema, schemas); err != nil {
					return err
				}
			}
		}
	case "array":
		items, ok := value.([]any)
		if !ok {
			return fmt.Errorf("%s is %T, want array", path, value)
		}
		if maximum, ok := schema["maxItems"].(float64); ok && len(items) > int(maximum) {
			return fmt.Errorf("%s has %d items, maximum %d", path, len(items), int(maximum))
		}
		if minimum, ok := schema["minItems"].(float64); ok && len(items) < int(minimum) {
			return fmt.Errorf("%s has %d items, minimum %d", path, len(items), int(minimum))
		}
		if itemSchema, exists := schema["items"]; exists {
			for index, child := range items {
				if err := validateFixtureSchema(fmt.Sprintf("%s[%d]", path, index), child, itemSchema, schemas); err != nil {
					return err
				}
			}
		}
	case "string":
		text, ok := value.(string)
		if !ok {
			return fmt.Errorf("%s is %T, want string", path, value)
		}
		if pattern, ok := schema["pattern"].(string); ok {
			compiled, err := regexp.Compile(pattern)
			if err != nil {
				return fmt.Errorf("%s has invalid OpenAPI pattern %q: %w", path, pattern, err)
			}
			if !compiled.MatchString(text) {
				return fmt.Errorf("%s value %q does not match %q", path, text, pattern)
			}
		}
		if format, _ := schema["format"].(string); format == "date-time" {
			if _, err := time.Parse(time.RFC3339Nano, text); err != nil {
				return fmt.Errorf("%s is not RFC3339 date-time: %w", path, err)
			}
		}
	case "integer":
		number, ok := value.(float64)
		if !ok || math.Trunc(number) != number {
			return fmt.Errorf("%s is %v (%T), want integer", path, value, value)
		}
		if minimum, ok := schema["minimum"].(float64); ok && number < minimum {
			return fmt.Errorf("%s integer %v is below minimum %v", path, number, minimum)
		}
		if maximum, ok := schema["maximum"].(float64); ok && number > maximum {
			return fmt.Errorf("%s integer %v exceeds maximum %v", path, number, maximum)
		}
	case "number":
		if _, ok := value.(float64); !ok {
			return fmt.Errorf("%s is %T, want number", path, value)
		}
	case "boolean":
		if _, ok := value.(bool); !ok {
			return fmt.Errorf("%s is %T, want boolean", path, value)
		}
	case "":
		// A pure enum/const schema is valid without an explicit type.
	default:
		return fmt.Errorf("%s has unsupported OpenAPI type %q", path, typeName)
	}
	return nil
}

var forbiddenFixtureKeys = map[string]bool{
	"reporter_id": true, "run_id": true, "agent_run_id": true, "run_link_id": true,
	"reporter_key": true, "reporter_opaque_key": true, "reporter_metadata": true, "run_link": true, "run_link_key": true,
	"provider": true, "provider_id": true, "adapter": true, "adapter_id": true,
	"provider_metadata": true, "adapter_metadata": true, "agent_id": true, "agent_name": true,
	"contributor": true, "contributors": true, "sample": true, "samples": true,
	"evidence_reference": true, "evidence_references": true, "reference_kind": true, "reference_value": true,
	"digest_sha256": true, "attachment_id": true, "prompt": true, "prompts": true, "log": true,
	"logs": true, "output": true, "command_output": true, "tool_arguments": true, "payload": true,
	"environment": true, "credentials": true, "error": true, "failure": true, "tool": true,
	"capability_metadata": true, "capability_details": true,
	"phase": true, "needs_input": true, "blocker_state": true, "correlation_id": true,
	"agent_reported_at": true, "server_received_at": true, "heartbeat": true,
	"session_id": true, "device_id": true, "requested_by": true, "claimed_by": true, "claimer_id": true,
	"action_key": true, "model": true, "run_mode": true, "profile_id": true, "effort": true,
	"finish_reason": true, "tests_summary": true,
}

func forbiddenFixtureKey(path, key string) bool {
	if forbiddenFixtureKeys[key] {
		return true
	}
	switch key {
	case "status":
		return !strings.Contains(path, ".stages[") && !strings.Contains(path, ".evidence[")
	case "source":
		return !strings.HasSuffix(path, ".progress")
	case "capabilities":
		isTopLevelRow := strings.HasPrefix(path, "$.rows[") && strings.Count(path, ".") == 1
		return !isTopLevelRow && path != "$.selected_outside.row"
	}
	for _, prefix := range []string{
		"provider_", "adapter_", "agent_", "estimate_", "context_", "log_", "followup", "instrumentation",
		"capability_", "model_", "profile_", "test_", "repo_", "repository_", "branch_", "commit_", "token_",
	} {
		if strings.HasPrefix(key, prefix) {
			return true
		}
	}
	if strings.HasPrefix(key, "reporter_") && key != "reporter_kind" {
		return true
	}
	if strings.HasSuffix(key, "_tokens") || key == "repo" || key == "repository" || key == "branch" || key == "commit" {
		return true
	}
	return false
}

func fixtureSafeTreeError(path string, value any) error {
	switch typed := value.(type) {
	case map[string]any:
		for key, child := range typed {
			if forbiddenFixtureKey(path, key) {
				return fmt.Errorf("%s contains forbidden internal key %q", path, key)
			}
			if err := fixtureSafeTreeError(path+"."+key, child); err != nil {
				return err
			}
		}
	case []any:
		for index, child := range typed {
			if err := fixtureSafeTreeError(fmt.Sprintf("%s[%d]", path, index), child); err != nil {
				return err
			}
		}
	case string:
		if safetext.ContainsSecretLike(typed) || strings.ContainsAny(typed, "\x00\r\n") {
			return fmt.Errorf("%s contains secret-like or control text", path)
		}
	}
	return nil
}

func decodeStrictJSON(raw []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	return requireJSONEOF(decoder)
}

func requireJSONEOF(decoder *json.Decoder) error {
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return fmt.Errorf("fixture has trailing JSON value")
		}
		return fmt.Errorf("fixture has trailing data: %w", err)
	}
	return nil
}
