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
	"sort"
	"testing"

	"github.com/inspr-at/paimos/backend/contracts"
	"github.com/inspr-at/paimos/backend/externalstage"
)

const externalStageFixtureDomainV2 = "paimos.external-stage.fixtures.v2\x00"

type externalStageFixtureV2 struct {
	SchemaMajor   int                          `json:"schema_major"`
	Name          string                       `json:"name"`
	DeliveryKey   string                       `json:"delivery_key"`
	AttemptNumber int64                        `json:"attempt_number"`
	ReporterClass externalstage.ReporterClass  `json:"reporter_class"`
	ReporterRole  externalstage.ReporterRole   `json:"reporter_role"`
	Cases         []externalStageFixtureCaseV2 `json:"cases"`
}

type externalStageFixtureCaseV2 struct {
	Name                   string                        `json:"name"`
	StageKey               string                        `json:"stage_key"`
	Accept                 externalstage.AcceptRequest   `json:"accept"`
	Report                 externalstage.ReportRequestV2 `json:"report"`
	ServerReceivedAt       string                        `json:"server_received_at"`
	ExpectedCanonicalState string                        `json:"expected_canonical_state"`
}

func TestCanonicalExternalStageV2FixtureAndDigest(t *testing.T) {
	directory := filepath.Join("fixtures", "external-stage-v2")
	entries, err := os.ReadDir(directory)
	if err != nil {
		t.Fatal(err)
	}
	actual := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			t.Fatalf("unexpected v2 fixture entry %q", entry.Name())
		}
		actual = append(actual, entry.Name())
	}
	sort.Strings(actual)
	if want := []string{"owner-pharos-v2.json"}; !reflect.DeepEqual(actual, want) {
		t.Fatalf("fixture inventory=%v want=%v", actual, want)
	}
	raw, err := os.ReadFile(filepath.Join(directory, actual[0]))
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) == 0 || raw[len(raw)-1] != '\n' || bytes.Count(raw, []byte{'\n'}) != 1 {
		t.Fatal("v2 fixture must be compact UTF-8 JSON with exactly one trailing LF")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var fixture externalStageFixtureV2
	if err := decoder.Decode(&fixture); err != nil {
		t.Fatal(err)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		t.Fatalf("fixture has trailing JSON: %v", err)
	}
	if fixture.SchemaMajor != externalstage.ContractMajorV2 || fixture.ReporterClass != externalstage.ReporterClassPharos ||
		fixture.ReporterRole != externalstage.ReporterRoleOwner || len(fixture.Cases) != 3 {
		t.Fatalf("unexpected fixture identity: %+v", fixture)
	}
	wantSchemes := []externalstage.VersionScheme{externalstage.VersionSchemeLegacy, externalstage.VersionSchemeINSPRCalendar, externalstage.VersionSchemeLegacy}
	for i, item := range fixture.Cases {
		if item.Report.PharosEvidence == nil || item.Report.PharosEvidence.Artifact.VersionScheme != wantSchemes[i] ||
			item.Report.PharosEvidence.Artifact.ReleaseManifestCoordinate == "" || item.Report.PharosEvidence.Artifact.ReleaseManifestDigest == "" {
			t.Fatalf("case %d lacks explicit immutable identity: %+v", i, item)
		}
	}
	hash := sha256.New()
	_, _ = hash.Write([]byte(externalStageFixtureDomainV2))
	_, _ = hash.Write([]byte(actual[0]))
	_, _ = hash.Write([]byte{0})
	_, _ = hash.Write(raw)
	_, _ = hash.Write([]byte{0})
	if got := hex.EncodeToString(hash.Sum(nil)); got != contracts.ExternalStageV2FixtureDigestHex {
		t.Fatalf("fixture-set digest=%s want %s", got, contracts.ExternalStageV2FixtureDigestHex)
	}
	if got := contracts.ExternalStageV2FixtureDigest(); hex.EncodeToString(got[:]) != contracts.ExternalStageV2FixtureDigestHex {
		t.Fatalf("digest accessor=%x", got)
	}
	assertNoCredentialField(t, actual[0], raw)
}
