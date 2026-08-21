// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package contracts_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/inspr-at/paimos/backend/contracts"
	"github.com/inspr-at/paimos/backend/externalstage"
)

const (
	externalStageFixtureDomain  = "paimos.external-stage.fixtures.v1\x00"
	externalStageFreezeCommit   = "580c0bf50768582bcedaf09faceb5fcd56df1f46"
	externalStagePendingRelease = "PENDING_RELEASE_TAG"
)

type externalStageFixtureManifest struct {
	SchemaMajor   int                                 `json:"schema_major"`
	Contract      string                              `json:"contract"`
	MediaType     string                              `json:"media_type"`
	Encoding      string                              `json:"encoding"`
	PaimosCommit  string                              `json:"paimos_commit"`
	PaimosRelease string                              `json:"paimos_release"`
	FixtureDigest string                              `json:"fixture_digest"`
	Fixtures      []externalStageFixtureManifestEntry `json:"fixtures"`
}

type externalStageFixtureManifestEntry struct {
	File          string                      `json:"file"`
	ReporterClass externalstage.ReporterClass `json:"reporter_class"`
	ReporterRole  externalstage.ReporterRole  `json:"reporter_role"`
	Bytes         int                         `json:"bytes"`
	SHA256        string                      `json:"sha256"`
}

type externalStageFixture struct {
	SchemaMajor   int                         `json:"schema_major"`
	Name          string                      `json:"name"`
	DeliveryKey   string                      `json:"delivery_key"`
	AttemptNumber int64                       `json:"attempt_number"`
	ReporterClass externalstage.ReporterClass `json:"reporter_class"`
	ReporterRole  externalstage.ReporterRole  `json:"reporter_role"`
	DependencyKey string                      `json:"dependency_key,omitempty"`
	Cases         []externalStageFixtureCase  `json:"cases"`
}

type externalStageFixtureCase struct {
	Name                       string                      `json:"name"`
	StageKey                   string                      `json:"stage_key"`
	DeploymentServerReceivedAt string                      `json:"deployment_server_received_at,omitempty"`
	Accept                     externalstage.AcceptRequest `json:"accept"`
	Report                     externalstage.ReportRequest `json:"report"`
	ServerReceivedAt           string                      `json:"server_received_at"`
	ExpectedCanonicalState     string                      `json:"expected_canonical_state,omitempty"`
	ExpectedDependencyState    string                      `json:"expected_dependency_state,omitempty"`
	CanonicalStageCompleted    *bool                       `json:"canonical_stage_completed,omitempty"`
}

func TestCanonicalExternalStageFixturesAndDigestManifest(t *testing.T) {
	directory := filepath.Join("fixtures", "external-stage")
	expectedFixtures := map[string][]byte{
		"dependency-janus-v1.json": marshalExternalStageFixture(t, canonicalJanusFixture()),
		"owner-pharos-v1.json":     marshalExternalStageFixture(t, canonicalPharosFixture()),
	}
	manifest := expectedExternalStageManifest(t, expectedFixtures)
	expectedManifest := marshalExternalStageFixture(t, manifest)

	entries, err := os.ReadDir(directory)
	if err != nil {
		t.Fatal(err)
	}
	actualNames := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			t.Fatalf("unexpected external-stage fixture entry %q", entry.Name())
		}
		actualNames = append(actualNames, entry.Name())
	}
	sort.Strings(actualNames)
	wantNames := []string{"dependency-janus-v1.json", "manifest-v1.json", "owner-pharos-v1.json"}
	if !reflect.DeepEqual(actualNames, wantNames) {
		t.Fatalf("fixture inventory=%v want %v", actualNames, wantNames)
	}

	for name, expected := range expectedFixtures {
		raw, err := os.ReadFile(filepath.Join(directory, name))
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(raw, expected) {
			t.Fatalf("%s changed from the canonical deterministic bytes", name)
		}
		var fixture externalStageFixture
		if err := decodeStrictExternalStageFixture(raw, &fixture); err != nil {
			t.Fatalf("strict decode %s: %v", name, err)
		}
		validateExternalStageFixture(t, fixture)
		assertNoCredentialField(t, name, raw)
		if bytes.Contains(raw, []byte(`"requirement"`)) {
			t.Fatalf("%s leaked internal prerequisite policy into the frozen adapter fixture", name)
		}
	}

	committedManifest, err := os.ReadFile(filepath.Join(directory, "manifest-v1.json"))
	if err != nil {
		t.Fatal(err)
	}
	var decodedManifest externalStageFixtureManifest
	if err := decodeStrictExternalStageFixture(committedManifest, &decodedManifest); err != nil {
		t.Fatalf("strict manifest decode: %v", err)
	}
	if !reflect.DeepEqual(decodedManifest, manifest) || !bytes.Equal(committedManifest, expectedManifest) {
		t.Fatal("manifest-v1.json drifted from exact sorted inventory, bytes, digests, or contract pins")
	}
	digest := externalStageFixtureSetDigest(expectedFixtures)
	if got := hex.EncodeToString(digest[:]); got != contracts.ExternalStageV1FixtureDigestHex {
		t.Fatalf("fixture-set digest=%s want compile-time %s", got, contracts.ExternalStageV1FixtureDigestHex)
	}
	if accessor := contracts.ExternalStageV1FixtureDigest(); accessor != digest {
		t.Fatalf("digest accessor=%x want %x", accessor, digest)
	}
}

func TestExternalStageFixtureAndManifestUnknownFieldsFail(t *testing.T) {
	fixture := marshalExternalStageFixture(t, canonicalJanusFixture())
	mutatedFixture := bytes.Replace(fixture, []byte(`{"schema_major":1`), []byte(`{"unknown":true,"schema_major":1`), 1)
	var decodedFixture externalStageFixture
	if err := decodeStrictExternalStageFixture(mutatedFixture, &decodedFixture); err == nil || !strings.Contains(err.Error(), "unknown") {
		t.Fatalf("unknown fixture field error=%v", err)
	}

	nestedMutation := bytes.Replace(fixture, []byte(`"heartbeat":false`), []byte(`"heartbeat":false,"free_text":"forbidden"`), 1)
	if err := decodeStrictExternalStageFixture(nestedMutation, &decodedFixture); err == nil || !strings.Contains(err.Error(), "free_text") {
		t.Fatalf("unknown nested report field error=%v", err)
	}

	manifest := expectedExternalStageManifest(t, map[string][]byte{
		"dependency-janus-v1.json": fixture,
		"owner-pharos-v1.json":     marshalExternalStageFixture(t, canonicalPharosFixture()),
	})
	manifestRaw := marshalExternalStageFixture(t, manifest)
	mutatedManifest := bytes.Replace(manifestRaw, []byte(`{"schema_major":1`), []byte(`{"extra":0,"schema_major":1`), 1)
	var decodedManifest externalStageFixtureManifest
	if err := decodeStrictExternalStageFixture(mutatedManifest, &decodedManifest); err == nil || !strings.Contains(err.Error(), "extra") {
		t.Fatalf("unknown manifest field error=%v", err)
	}
}

func TestExternalStageFixtureDigestMismatchFails(t *testing.T) {
	fixtures := map[string][]byte{
		"dependency-janus-v1.json": marshalExternalStageFixture(t, canonicalJanusFixture()),
		"owner-pharos-v1.json":     marshalExternalStageFixture(t, canonicalPharosFixture()),
	}
	manifest := expectedExternalStageManifest(t, fixtures)
	fixtures["dependency-janus-v1.json"] = append([]byte(nil), fixtures["dependency-janus-v1.json"]...)
	fixtures["dependency-janus-v1.json"][100] ^= 1
	if err := verifyExternalStageManifest(manifest, fixtures); err == nil || !strings.Contains(err.Error(), "digest mismatch") {
		t.Fatalf("mutated fixture verification error=%v", err)
	}
	manifest.FixtureDigest = "sha256:" + strings.Repeat("0", 64)
	if err := verifyExternalStageManifest(manifest, map[string][]byte{
		"dependency-janus-v1.json": marshalExternalStageFixture(t, canonicalJanusFixture()),
		"owner-pharos-v1.json":     marshalExternalStageFixture(t, canonicalPharosFixture()),
	}); err == nil || !strings.Contains(err.Error(), "set digest mismatch") {
		t.Fatalf("mutated set digest verification error=%v", err)
	}
}

func TestExternalStageDTOAndOpenAPISchemaFieldsStayAligned(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "handlers", "openapi.json"))
	if err != nil {
		t.Fatal(err)
	}
	var document struct {
		Components struct {
			Schemas map[string]struct {
				Properties map[string]any `json:"properties"`
				Required   []string       `json:"required"`
			} `json:"schemas"`
		} `json:"components"`
	}
	if err := json.Unmarshal(raw, &document); err != nil {
		t.Fatal(err)
	}
	contractsToCheck := []struct {
		name string
		typ  reflect.Type
	}{
		{"ExternalStageCreateRequest", reflect.TypeOf(externalstage.CreateHandoffRequest{})},
		{"ExternalStageCredentialEpochRequest", reflect.TypeOf(externalstage.CredentialEpochRequest{})},
		{"ExternalStageRevokeRequest", reflect.TypeOf(externalstage.RevokeHandoffRequest{})},
		{"ExternalStageHandoffMetadata", reflect.TypeOf(externalstage.HandoffMetadata{})},
		{"ExternalStagePullResponse", reflect.TypeOf(externalstage.PullResponse{})},
		{"ExternalStageAcceptRequest", reflect.TypeOf(externalstage.AcceptRequest{})},
		{"ExternalStageArtifactEvidence", reflect.TypeOf(externalstage.ArtifactEvidence{})},
		{"ExternalStagePharosEvidence", reflect.TypeOf(externalstage.PharosEvidence{})},
		{"ExternalStageJanusEvidence", reflect.TypeOf(externalstage.JanusEvidence{})},
		{"ExternalStageReportRequest", reflect.TypeOf(externalstage.ReportRequest{})},
		{"ExternalStageReportReceipt", reflect.TypeOf(externalstage.ReportReceipt{})},
	}
	for _, item := range contractsToCheck {
		schema, ok := document.Components.Schemas[item.name]
		if !ok {
			t.Fatalf("OpenAPI schema %s missing", item.name)
		}
		properties, required := externalStageJSONFieldSets(item.typ)
		openAPIProperties := make([]string, 0, len(schema.Properties))
		for name := range schema.Properties {
			openAPIProperties = append(openAPIProperties, name)
		}
		sort.Strings(openAPIProperties)
		sort.Strings(schema.Required)
		if !reflect.DeepEqual(properties, openAPIProperties) {
			t.Fatalf("%s properties DTO=%v OpenAPI=%v", item.name, properties, openAPIProperties)
		}
		if !reflect.DeepEqual(required, schema.Required) {
			t.Fatalf("%s required DTO=%v OpenAPI=%v", item.name, required, schema.Required)
		}
	}
}

func TestExternalStageFixtureSchemaPinsBothCommitDigestWidths(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "handlers", "openapi.json"))
	if err != nil {
		t.Fatal(err)
	}
	var document struct {
		Components struct {
			Schemas map[string]struct {
				Properties map[string]struct {
					Pattern string `json:"pattern"`
				} `json:"properties"`
			} `json:"schemas"`
		} `json:"components"`
	}
	if err := json.Unmarshal(raw, &document); err != nil {
		t.Fatal(err)
	}
	pattern := document.Components.Schemas["ExternalStageArtifactEvidence"].Properties["commit_digest"].Pattern
	compiled, err := regexp.Compile(pattern)
	if err != nil {
		t.Fatalf("compile commit_digest schema pattern: %v", err)
	}
	for _, width := range []int{40, 64} {
		if !compiled.MatchString(strings.Repeat("a", width)) {
			t.Fatalf("fixture schema rejects lowercase %d-hex commit digest", width)
		}
	}
	for _, invalid := range []string{strings.Repeat("A", 40), strings.Repeat("a", 39), strings.Repeat("a", 41), strings.Repeat("a", 63), strings.Repeat("a", 65)} {
		if compiled.MatchString(invalid) {
			t.Fatalf("fixture schema accepts invalid commit digest len=%d", len(invalid))
		}
	}
}

func canonicalPharosFixture() externalStageFixture {
	artifact := externalstage.ArtifactEvidence{
		Version: "1.2.3", Digest: "sha256:" + strings.Repeat("1", 64), CommitDigest: strings.Repeat("a", 40),
	}
	return externalStageFixture{
		SchemaMajor:   externalstage.ContractMajor,
		Name:          "pharos-owner-deployment-and-verification",
		DeliveryKey:   "issue:4664",
		AttemptNumber: 1,
		ReporterClass: externalstage.ReporterClassPharos,
		ReporterRole:  externalstage.ReporterRoleOwner,
		Cases: []externalStageFixtureCase{
			{
				Name: "deployment", StageKey: "deployment",
				Accept: externalstage.AcceptRequest{Sequence: 1, ObservedAt: "2026-08-20T10:00:00Z"},
				Report: externalstage.ReportRequest{
					Sequence: 2, State: externalstage.HandoffStateSucceeded,
					ObservedAt: "2026-08-20T10:01:00Z", Heartbeat: false,
					PharosEvidence: &externalstage.PharosEvidence{
						Kind: externalstage.EvidenceKindDeployment, Workflow: "deploy-production",
						Environment: "production-eu1", Artifact: artifact,
						Result: externalstage.EvidenceResultSucceeded, ObservedAt: "2026-08-20T10:01:00Z",
					},
				},
				ServerReceivedAt: "2026-08-20T10:01:01Z", ExpectedCanonicalState: "deployed_unverified",
			},
			{
				Name: "fresh-exact-verification", StageKey: "verification",
				DeploymentServerReceivedAt: "2026-08-20T10:01:01Z",
				Accept:                     externalstage.AcceptRequest{Sequence: 1, ObservedAt: "2026-08-20T10:02:00Z"},
				Report: externalstage.ReportRequest{
					Sequence: 2, State: externalstage.HandoffStateSucceeded,
					ObservedAt: "2026-08-20T10:03:00Z", Heartbeat: false,
					PharosEvidence: &externalstage.PharosEvidence{
						Kind: externalstage.EvidenceKindVerification, Workflow: "verify-production",
						Environment: "production-eu1", Artifact: artifact,
						Result: externalstage.EvidenceResultSucceeded, ObservedAt: "2026-08-20T10:03:00Z",
					},
				},
				ServerReceivedAt: "2026-08-20T10:03:01Z", ExpectedCanonicalState: "verified",
			},
		},
	}
}

func canonicalJanusFixture() externalStageFixture {
	completed := false
	authorized := true
	credentialReady := true
	return externalStageFixture{
		SchemaMajor: externalstage.ContractMajor, DeliveryKey: "issue:4664", AttemptNumber: 1,
		Name: "janus-value-free-dependencies", ReporterClass: externalstage.ReporterClassJanus,
		ReporterRole: externalstage.ReporterRoleDependency, DependencyKey: "privileged-handoff",
		Cases: []externalStageFixtureCase{
			{
				Name: "authorization", StageKey: "deployment",
				Accept: externalstage.AcceptRequest{Sequence: 1, ObservedAt: "2026-08-20T09:55:00Z"},
				Report: externalstage.ReportRequest{
					Sequence: 2, State: externalstage.HandoffStateSucceeded,
					ObservedAt: "2026-08-20T09:56:00Z", Heartbeat: false,
					JanusEvidence: &externalstage.JanusEvidence{
						Kind: externalstage.EvidenceKindAuthorization, Result: externalstage.EvidenceResultSatisfied,
						Authorized: &authorized, ObservedAt: "2026-08-20T09:56:00Z",
					},
				},
				ServerReceivedAt: "2026-08-20T09:56:01Z", ExpectedDependencyState: "satisfied",
				CanonicalStageCompleted: &completed,
			},
			{
				Name: "credential-handoff", StageKey: "deployment",
				Accept: externalstage.AcceptRequest{Sequence: 1, ObservedAt: "2026-08-20T09:57:00Z"},
				Report: externalstage.ReportRequest{
					Sequence: 2, State: externalstage.HandoffStateSucceeded,
					ObservedAt: "2026-08-20T09:58:00Z", Heartbeat: false,
					JanusEvidence: &externalstage.JanusEvidence{
						Kind: externalstage.EvidenceKindCredentialHandoff, Result: externalstage.EvidenceResultSatisfied,
						CredentialReady: &credentialReady, ObservedAt: "2026-08-20T09:58:00Z",
					},
				},
				ServerReceivedAt: "2026-08-20T09:58:01Z", ExpectedDependencyState: "satisfied",
				CanonicalStageCompleted: &completed,
			},
		},
	}
}

func marshalExternalStageFixture(t *testing.T, value any) []byte {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return append(raw, '\n')
}

func expectedExternalStageManifest(t *testing.T, fixtures map[string][]byte) externalStageFixtureManifest {
	t.Helper()
	entries := make([]externalStageFixtureManifestEntry, 0, len(fixtures))
	for name, raw := range fixtures {
		var fixture externalStageFixture
		if err := decodeStrictExternalStageFixture(raw, &fixture); err != nil {
			t.Fatal(err)
		}
		digest := sha256.Sum256(raw)
		entries = append(entries, externalStageFixtureManifestEntry{
			File: name, ReporterClass: fixture.ReporterClass, ReporterRole: fixture.ReporterRole,
			Bytes: len(raw), SHA256: hex.EncodeToString(digest[:]),
		})
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].File < entries[j].File })
	setDigest := externalStageFixtureSetDigest(fixtures)
	return externalStageFixtureManifest{
		SchemaMajor: externalstage.ContractMajor, Contract: "paimos.external-stage.v1",
		MediaType: externalstage.MediaTypeV1, Encoding: "utf-8-json-lf",
		PaimosCommit: externalStageFreezeCommit, PaimosRelease: externalStagePendingRelease,
		FixtureDigest: "sha256:" + hex.EncodeToString(setDigest[:]), Fixtures: entries,
	}
}

func externalStageFixtureSetDigest(fixtures map[string][]byte) [sha256.Size]byte {
	names := make([]string, 0, len(fixtures))
	for name := range fixtures {
		names = append(names, name)
	}
	sort.Strings(names)
	hash := sha256.New()
	_, _ = hash.Write([]byte(externalStageFixtureDomain))
	for _, name := range names {
		_, _ = hash.Write([]byte(name))
		_, _ = hash.Write([]byte{0})
		_, _ = hash.Write(fixtures[name])
		_, _ = hash.Write([]byte{0})
	}
	var digest [sha256.Size]byte
	copy(digest[:], hash.Sum(nil))
	return digest
}

func verifyExternalStageManifest(manifest externalStageFixtureManifest, fixtures map[string][]byte) error {
	for _, entry := range manifest.Fixtures {
		raw, ok := fixtures[entry.File]
		if !ok {
			return fmt.Errorf("missing fixture %s", entry.File)
		}
		digest := sha256.Sum256(raw)
		if len(raw) != entry.Bytes || hex.EncodeToString(digest[:]) != entry.SHA256 {
			return fmt.Errorf("fixture digest mismatch for %s", entry.File)
		}
	}
	setDigest := externalStageFixtureSetDigest(fixtures)
	if manifest.FixtureDigest != "sha256:"+hex.EncodeToString(setDigest[:]) {
		return errors.New("fixture set digest mismatch")
	}
	return nil
}

func decodeStrictExternalStageFixture(raw []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("expected one JSON value")
		}
		return err
	}
	return nil
}

func validateExternalStageFixture(t *testing.T, fixture externalStageFixture) {
	t.Helper()
	if fixture.SchemaMajor != externalstage.ContractMajor || fixture.Name == "" || fixture.DeliveryKey == "" ||
		fixture.AttemptNumber < 1 || len(fixture.Cases) == 0 {
		t.Fatalf("invalid fixture envelope: %+v", fixture)
	}
	for _, item := range fixture.Cases {
		if item.Accept.Sequence != 1 || item.Report.Sequence < 2 {
			t.Fatalf("%s sequence contract drifted", item.Name)
		}
		for field, raw := range map[string]string{
			"accept observed_at": item.Accept.ObservedAt,
			"report observed_at": item.Report.ObservedAt,
			"server_received_at": item.ServerReceivedAt,
		} {
			if _, err := time.Parse(time.RFC3339Nano, raw); err != nil {
				t.Fatalf("%s %s invalid: %v", item.Name, field, err)
			}
		}
		if item.Report.PharosEvidence != nil && item.Report.JanusEvidence != nil {
			t.Fatalf("%s combines reporter evidence variants", item.Name)
		}
	}
	if fixture.ReporterClass == externalstage.ReporterClassPharos {
		if fixture.ReporterRole != externalstage.ReporterRoleOwner || len(fixture.Cases) != 2 {
			t.Fatalf("Pharos fixture must contain owner deployment plus verification")
		}
		deployment, verification := fixture.Cases[0], fixture.Cases[1]
		// Both ordered cases inherit this one envelope's delivery/attempt. They
		// represent distinct stage handoffs, never isolated or same-row facts.
		if deployment.StageKey != "deployment" || deployment.Report.PharosEvidence == nil ||
			deployment.Report.PharosEvidence.Kind != externalstage.EvidenceKindDeployment ||
			deployment.ExpectedCanonicalState != "deployed_unverified" {
			t.Fatalf("deployment fixture can only establish deployed_unverified: %+v", deployment)
		}
		if verification.StageKey != "verification" || verification.Report.PharosEvidence == nil ||
			verification.Report.PharosEvidence.Kind != externalstage.EvidenceKindVerification ||
			verification.ExpectedCanonicalState != "verified" {
			t.Fatalf("verification must be a separate exact-evidence case: %+v", verification)
		}
		deployedAt, _ := time.Parse(time.RFC3339Nano, verification.DeploymentServerReceivedAt)
		observedAt, _ := time.Parse(time.RFC3339Nano, verification.Report.PharosEvidence.ObservedAt)
		receivedAt, _ := time.Parse(time.RFC3339Nano, verification.ServerReceivedAt)
		if !observedAt.After(deployedAt) || !receivedAt.After(deployedAt) {
			t.Fatal("verification observation and receipt must both be fresh after deployment receipt")
		}
		deployedEvidence, verifiedEvidence := deployment.Report.PharosEvidence, verification.Report.PharosEvidence
		if deployedEvidence.Environment != verifiedEvidence.Environment || deployedEvidence.Artifact != verifiedEvidence.Artifact {
			t.Fatal("verification must bind the exact deployed artifact and environment")
		}
		return
	}
	if fixture.ReporterClass != externalstage.ReporterClassJanus || fixture.ReporterRole != externalstage.ReporterRoleDependency || fixture.DependencyKey == "" {
		t.Fatalf("unknown or invalid reporter fixture: %+v", fixture)
	}
	for _, item := range fixture.Cases {
		if item.Report.JanusEvidence == nil || item.Report.PharosEvidence != nil || item.CanonicalStageCompleted == nil || *item.CanonicalStageCompleted {
			t.Fatalf("Janus dependency case must remain value-free and never complete canonical state: %+v", item)
		}
	}
}

func assertNoCredentialField(t *testing.T, name string, raw []byte) {
	t.Helper()
	var tree any
	if err := json.Unmarshal(raw, &tree); err != nil {
		t.Fatal(err)
	}
	var walk func(string, any)
	walk = func(path string, value any) {
		switch typed := value.(type) {
		case map[string]any:
			for key, child := range typed {
				lower := strings.ToLower(key)
				if strings.Contains(lower, "secret") || strings.Contains(lower, "token") || strings.Contains(lower, "credential_value") {
					t.Fatalf("%s contains forbidden credential field %s.%s", name, path, key)
				}
				walk(path+"."+key, child)
			}
		case []any:
			for index, child := range typed {
				walk(fmt.Sprintf("%s[%d]", path, index), child)
			}
		}
	}
	walk("$", tree)
}

func externalStageJSONFieldSets(typ reflect.Type) (properties, required []string) {
	for index := 0; index < typ.NumField(); index++ {
		tag := typ.Field(index).Tag.Get("json")
		parts := strings.Split(tag, ",")
		if parts[0] == "" || parts[0] == "-" {
			continue
		}
		properties = append(properties, parts[0])
		optional := false
		for _, option := range parts[1:] {
			optional = optional || option == "omitempty"
		}
		if !optional {
			required = append(required, parts[0])
		}
	}
	sort.Strings(properties)
	sort.Strings(required)
	return properties, required
}
