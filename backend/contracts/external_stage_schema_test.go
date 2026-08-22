// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package contracts_test

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/inspr-at/paimos/backend/externalstage"
)

const externalStageStandaloneSchemaSHA256 = "c9de59698e68cb7c21dd84ff8d8a9a209eef1188a54bdca8f766613f540182ff"

var externalStageStandaloneRoots = []string{
	"ExternalStageCreateRequest",
	"ExternalStageCredentialEpochRequest",
	"ExternalStageRevokeRequest",
	"ExternalStageReporterRegistrationRequest",
	"ExternalStageReporterRegistration",
	"ExternalStageReporterRegistrationList",
	"ExternalStagePrerequisiteSetRequest",
	"ExternalStagePrerequisiteSet",
	"ExternalStageOwnerActivationRequest",
	"ExternalStageOwnerActivation",
	"ExternalStageHandoffMetadata",
	"ExternalStagePullResponse",
	"ExternalStageAcceptRequest",
	"ExternalStageReportRequest",
	"ExternalStageReportReceipt",
}

func TestExternalStageStandaloneSchemaIsPinnedAndAligned(t *testing.T) {
	raw, err := os.ReadFile("external-stage-v1.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) == 0 || raw[len(raw)-1] != '\n' || (len(raw) > 1 && raw[len(raw)-2] == '\n') {
		t.Fatal("standalone schema must be UTF-8 JSON with exactly one trailing LF")
	}
	digest := sha256.Sum256(raw)
	if got := hex.EncodeToString(digest[:]); got != externalStageStandaloneSchemaSHA256 {
		t.Fatalf("standalone schema digest=%s want immutable v1 digest %s", got, externalStageStandaloneSchemaSHA256)
	}

	var schema map[string]any
	if err := json.Unmarshal(raw, &schema); err != nil {
		t.Fatalf("decode standalone schema: %v", err)
	}
	assertExactKeys(t, "standalone schema", schema, []string{
		"$defs", "$id", "$schema", "anyOf", "description", "title", "x-paimos-contract",
	})
	if schema["$schema"] != "https://json-schema.org/draft/2020-12/schema" ||
		schema["$id"] != "https://paimos.example/contracts/external-stage-v1.schema.json" {
		t.Fatalf("standalone schema identity drifted: schema=%v id=%v", schema["$schema"], schema["$id"])
	}

	metadata, ok := schema["x-paimos-contract"].(map[string]any)
	if !ok {
		t.Fatal("x-paimos-contract must be an object")
	}
	assertExactKeys(t, "x-paimos-contract", metadata, []string{
		"certified_commit", "encoding", "first_release", "fixture_digest", "media_type", "schema_major",
	})
	if metadata["schema_major"] != float64(externalstage.ContractMajor) ||
		metadata["media_type"] != externalstage.MediaTypeV1 ||
		metadata["encoding"] != "utf-8-json-lf" ||
		metadata["certified_commit"] != externalStagePinnedCommit ||
		metadata["first_release"] != externalStagePinnedRelease ||
		metadata["fixture_digest"] != "sha256:"+externalStageFixtureDigestHex(t) {
		t.Fatalf("standalone schema certification tuple drifted: %#v", metadata)
	}

	definitions, ok := schema["$defs"].(map[string]any)
	if !ok {
		t.Fatal("$defs must be an object")
	}
	openAPIDefinitions := externalStageOpenAPIDefinitions(t)
	if !reflect.DeepEqual(definitions, openAPIDefinitions) {
		t.Fatal("standalone $defs drifted from the complete ExternalStage OpenAPI component inventory")
	}

	rootRefs := schemaReferenceList(t, schema["anyOf"])
	wantRootRefs := make([]string, 0, len(externalStageStandaloneRoots))
	for _, name := range externalStageStandaloneRoots {
		wantRootRefs = append(wantRootRefs, "#/$defs/"+name)
	}
	if !reflect.DeepEqual(rootRefs, wantRootRefs) {
		t.Fatalf("standalone root inventory=%v want %v", rootRefs, wantRootRefs)
	}
	assertStandaloneSchemaReferencesClosed(t, schema, definitions)
}

func externalStageOpenAPIDefinitions(t *testing.T) map[string]any {
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
	definitions := make(map[string]any)
	for name, definition := range document.Components.Schemas {
		if strings.HasPrefix(name, "ExternalStage") {
			definitions[name] = rewriteExternalStageSchemaReferences(definition)
		}
	}
	if len(definitions) != 22 {
		t.Fatalf("ExternalStage OpenAPI component count=%d want 22", len(definitions))
	}
	return definitions
}

func rewriteExternalStageSchemaReferences(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		for key, child := range typed {
			if key == "$ref" {
				if reference, ok := child.(string); ok {
					typed[key] = strings.Replace(reference, "#/components/schemas/", "#/$defs/", 1)
				}
				continue
			}
			typed[key] = rewriteExternalStageSchemaReferences(child)
		}
	case []any:
		for index, child := range typed {
			typed[index] = rewriteExternalStageSchemaReferences(child)
		}
	}
	return value
}

func schemaReferenceList(t *testing.T, value any) []string {
	t.Helper()
	items, ok := value.([]any)
	if !ok {
		t.Fatal("anyOf must be an array")
	}
	references := make([]string, 0, len(items))
	for index, item := range items {
		entry, ok := item.(map[string]any)
		if !ok || len(entry) != 1 {
			t.Fatalf("anyOf[%d] must contain exactly one $ref", index)
		}
		reference, ok := entry["$ref"].(string)
		if !ok {
			t.Fatalf("anyOf[%d] has no string $ref", index)
		}
		references = append(references, reference)
	}
	return references
}

func assertStandaloneSchemaReferencesClosed(t *testing.T, value any, definitions map[string]any) {
	t.Helper()
	var visit func(path string, value any)
	visit = func(path string, value any) {
		switch typed := value.(type) {
		case map[string]any:
			for key, child := range typed {
				childPath := path + "/" + key
				if key == "$ref" {
					reference, ok := child.(string)
					if !ok || !strings.HasPrefix(reference, "#/$defs/") {
						t.Fatalf("%s contains non-local reference %v", childPath, child)
					}
					name := strings.TrimPrefix(reference, "#/$defs/")
					if _, ok := definitions[name]; !ok {
						t.Fatalf("%s references missing definition %q", childPath, name)
					}
					continue
				}
				visit(childPath, child)
			}
		case []any:
			for index, child := range typed {
				visit(fmt.Sprintf("%s/%d", path, index), child)
			}
		}
	}
	visit("#", value)
}

func externalStageFixtureDigestHex(t *testing.T) string {
	t.Helper()
	manifestRaw, err := os.ReadFile(filepath.Join("fixtures", "external-stage", "manifest-v1.json"))
	if err != nil {
		t.Fatal(err)
	}
	var manifest externalStageFixtureManifest
	if err := json.Unmarshal(manifestRaw, &manifest); err != nil {
		t.Fatal(err)
	}
	return strings.TrimPrefix(manifest.FixtureDigest, "sha256:")
}

func assertExactKeys(t *testing.T, label string, value map[string]any, expected []string) {
	t.Helper()
	actual := make([]string, 0, len(value))
	for key := range value {
		actual = append(actual, key)
	}
	sort.Strings(actual)
	sort.Strings(expected)
	if !reflect.DeepEqual(actual, expected) {
		t.Fatalf("%s keys=%v want %v", label, actual, expected)
	}
}
