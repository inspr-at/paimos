// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package contracts_test

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"testing"

	"github.com/inspr-at/paimos/backend/contracts"
	"github.com/inspr-at/paimos/backend/externalstage"
)

const externalStageV2StandaloneSchemaSHA256 = "c7dd884c2d90b7044f7882caef9ada10869c4b843b1b37c1d8769ed9ba6c81dd"

func TestExternalStageV2StandaloneSchemaIsPinnedAndClosed(t *testing.T) {
	raw, err := os.ReadFile("external-stage-v2.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) == 0 || raw[len(raw)-1] != '\n' {
		t.Fatal("v2 schema must have one trailing LF")
	}
	digest := sha256.Sum256(raw)
	if got := hex.EncodeToString(digest[:]); got != externalStageV2StandaloneSchemaSHA256 {
		t.Fatalf("v2 schema digest=%s want %s", got, externalStageV2StandaloneSchemaSHA256)
	}
	var schema map[string]any
	if err := json.Unmarshal(raw, &schema); err != nil {
		t.Fatal(err)
	}
	metadata, ok := schema["x-paimos-contract"].(map[string]any)
	if !ok || metadata["schema_major"] != float64(externalstage.ContractMajorV2) ||
		metadata["media_type"] != externalstage.MediaTypeV2 ||
		metadata["fixture_digest"] != "sha256:"+contracts.ExternalStageV2FixtureDigestHex {
		t.Fatalf("v2 schema certification tuple=%#v", metadata)
	}
	definitions, ok := schema["$defs"].(map[string]any)
	if !ok || len(definitions) != 7 {
		t.Fatalf("v2 schema definition inventory=%d", len(definitions))
	}
	assertStandaloneSchemaReferencesClosed(t, schema, definitions)
	for _, required := range []string{"ExternalStageArtifactEvidenceV2", "ExternalStagePharosEvidenceV2", "ExternalStageReportRequestV2", "ExternalStagePullResponseV2"} {
		if definitions[required] == nil {
			t.Fatalf("missing v2 schema definition %s", required)
		}
	}
}
