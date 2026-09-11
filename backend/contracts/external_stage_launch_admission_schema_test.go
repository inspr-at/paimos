// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package contracts_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/inspr-at/paimos/backend/externalstage"
)

const launchAdmissionSchemaSHA256 = "3b15130cddc9461d038f06332f066220274c265bfd15a3d2aaa636b8b229415c"

func TestExternalStageLaunchAdmissionSchemaAndFixturesAreClosedAndPinned(t *testing.T) {
	schemaRaw, err := os.ReadFile("external-stage-launch-admission-v1.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(schemaRaw)
	if got := hex.EncodeToString(digest[:]); got != launchAdmissionSchemaSHA256 {
		t.Fatalf("launch schema digest=%s want=%s", got, launchAdmissionSchemaSHA256)
	}
	var schema map[string]any
	if err := json.Unmarshal(schemaRaw, &schema); err != nil {
		t.Fatal(err)
	}
	metadata, _ := schema["x-paimos-contract"].(map[string]any)
	definitions, _ := schema["$defs"].(map[string]any)
	if metadata["media_type"] != externalstage.LaunchAdmissionMediaType || len(definitions) != 9 {
		t.Fatalf("launch schema metadata=%v definitions=%d", metadata, len(definitions))
	}
	for _, name := range []string{"ExternalStageLaunchArtifact", "ExternalStageLaunchCandidate", "ExternalStageLaunchAdmission",
		"ExternalStageLaunchConsumeRequest", "ExternalStageLaunchReceipt"} {
		definition, ok := definitions[name].(map[string]any)
		if !ok || definition["additionalProperties"] != false {
			t.Fatalf("definition %s is not closed: %#v", name, definition)
		}
	}

	fixtures := []struct {
		name   string
		sha256 string
		value  any
	}{
		{"admission.json", "00a564686330328c119f645f5ea9e16e1fab1569b76092b0822b773c8e84b248", &externalstage.LaunchAdmission{}},
		{"candidate.json", "4eed040a5bef85899994c30751bb37ec135f81d42e5ff58a764cf51a87f0775b", &externalstage.LaunchCandidate{}},
		{"consume.json", "014ced0d247386e30267b0c129c4c7d8abcc3e3987841c966a05ed0cc336159c", &externalstage.ConsumeLaunchAdmissionRequest{}},
		{"receipt.json", "bd57a2ae684af37a6d43f1888a402189aa48089f733ffb7ef73b40c2a9f3da01", &externalstage.LaunchReceipt{}},
	}
	for _, fixture := range fixtures {
		raw, err := os.ReadFile(filepath.Join("fixtures", "external-stage-launch-admission-v1", fixture.name))
		if err != nil {
			t.Fatal(err)
		}
		actual := sha256.Sum256(raw)
		if got := hex.EncodeToString(actual[:]); got != fixture.sha256 {
			t.Fatalf("fixture %s digest=%s want=%s", fixture.name, got, fixture.sha256)
		}
		if len(raw) == 0 || raw[len(raw)-1] != '\n' || bytes.Count(raw, []byte{'\n'}) != 1 {
			t.Fatalf("fixture %s must be compact JSON with one LF", fixture.name)
		}
		decoder := json.NewDecoder(bytes.NewReader(raw))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(fixture.value); err != nil {
			t.Fatalf("fixture %s: %v", fixture.name, err)
		}
		if err := decoder.Decode(&struct{}{}); err != io.EOF {
			t.Fatalf("fixture %s trailing JSON: %v", fixture.name, err)
		}
		encoded, err := json.Marshal(fixture.value)
		if err != nil || !bytes.Equal(encoded, bytes.TrimSuffix(raw, []byte{'\n'})) {
			t.Fatalf("fixture %s does not preserve exact DTO field order", fixture.name)
		}
	}

	openAPIRaw, err := os.ReadFile(filepath.Join("..", "handlers", "openapi.json"))
	if err != nil {
		t.Fatal(err)
	}
	var openAPI struct {
		Components struct {
			Schemas map[string]any `json:"schemas"`
		} `json:"components"`
		Paths map[string]any `json:"paths"`
	}
	if err := json.Unmarshal(openAPIRaw, &openAPI); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"ExternalStageLaunchCandidate", "ExternalStageLaunchAdmission",
		"ExternalStageLaunchConsumeRequest", "ExternalStageLaunchReceipt"} {
		if published, ok := openAPI.Components.Schemas[name].(map[string]any); !ok || published["additionalProperties"] != false {
			t.Fatalf("OpenAPI schema %s missing or open", name)
		}
	}
	for _, path := range []string{externalstage.LaunchCandidatePath, externalstage.LaunchConsumePath} {
		if _, ok := openAPI.Paths[path]; !ok {
			t.Fatalf("OpenAPI launch path missing: %s", path)
		}
	}
}

func TestExternalStageLaunchWireFieldsStayExact(t *testing.T) {
	want := map[reflect.Type][]string{
		reflect.TypeOf(externalstage.LaunchCandidate{}):               {"schema", "version", "target_ref", "workflow", "environment", "artifact", "reviewed_plan_digest", "operation_binding_digest", "observed_at"},
		reflect.TypeOf(externalstage.ConsumeLaunchAdmissionRequest{}): {"schema", "version", "admission_digest"},
		reflect.TypeOf(externalstage.LaunchReceipt{}):                 {"schema", "version", "admission_id", "admission_digest", "handoff_id", "credential_epoch", "launch_number", "state", "consumed_at"},
	}
	for typ, expected := range want {
		actual := make([]string, typ.NumField())
		for index := range actual {
			actual[index] = typ.Field(index).Tag.Get("json")
		}
		if !reflect.DeepEqual(actual, expected) {
			t.Fatalf("%s fields=%v want=%v", typ.Name(), actual, expected)
		}
	}
}
