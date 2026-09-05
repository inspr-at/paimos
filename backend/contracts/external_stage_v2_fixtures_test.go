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

type externalStageFixtureManifestV2 struct {
	SchemaMajor   int                                 `json:"schema_major"`
	Contract      string                              `json:"contract"`
	MediaType     string                              `json:"media_type"`
	Encoding      string                              `json:"encoding"`
	PaimosCommit  string                              `json:"paimos_commit"`
	PaimosRelease string                              `json:"paimos_release"`
	FixtureDigest string                              `json:"fixture_digest"`
	SchemaFile    string                              `json:"schema_file"`
	SchemaSHA256  string                              `json:"schema_sha256"`
	Fixtures      []externalStageFixtureManifestEntry `json:"fixtures"`
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
	if want := []string{"manifest-v2.json", "owner-pharos-v2.json"}; !reflect.DeepEqual(actual, want) {
		t.Fatalf("fixture inventory=%v want=%v", actual, want)
	}
	raw, err := os.ReadFile(filepath.Join(directory, "owner-pharos-v2.json"))
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
	_, _ = hash.Write([]byte("owner-pharos-v2.json"))
	_, _ = hash.Write([]byte{0})
	_, _ = hash.Write(raw)
	_, _ = hash.Write([]byte{0})
	if got := hex.EncodeToString(hash.Sum(nil)); got != contracts.ExternalStageV2FixtureDigestHex {
		t.Fatalf("fixture-set digest=%s want %s", got, contracts.ExternalStageV2FixtureDigestHex)
	}
	if got := contracts.ExternalStageV2FixtureDigest(); hex.EncodeToString(got[:]) != contracts.ExternalStageV2FixtureDigestHex {
		t.Fatalf("digest accessor=%x", got)
	}
	assertNoCredentialField(t, "owner-pharos-v2.json", raw)

	manifestRaw, err := os.ReadFile(filepath.Join(directory, "manifest-v2.json"))
	if err != nil {
		t.Fatal(err)
	}
	decoder = json.NewDecoder(bytes.NewReader(manifestRaw))
	decoder.DisallowUnknownFields()
	var manifest externalStageFixtureManifestV2
	if err := decoder.Decode(&manifest); err != nil {
		t.Fatal(err)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		t.Fatalf("manifest has trailing JSON: %v", err)
	}
	if manifest.SchemaMajor != externalstage.ContractMajorV2 || manifest.Contract != "paimos.external-stage.v2" ||
		manifest.MediaType != externalstage.MediaTypeV2 || manifest.Encoding != "utf-8-json-lf" ||
		manifest.PaimosCommit != "d606f7be1c988555bb9937eb78298eed4f997cb1" || manifest.PaimosRelease != "v26.09.05.10.30" ||
		manifest.FixtureDigest != "sha256:"+contracts.ExternalStageV2FixtureDigestHex ||
		manifest.SchemaFile != "backend/contracts/external-stage-v2.schema.json" ||
		manifest.SchemaSHA256 != externalStageV2StandaloneSchemaSHA256 || len(manifest.Fixtures) != 1 {
		t.Fatalf("v2 manifest certification tuple=%+v", manifest)
	}
	entry := manifest.Fixtures[0]
	fixtureDigest := sha256.Sum256(raw)
	if entry.File != "owner-pharos-v2.json" || entry.ReporterClass != externalstage.ReporterClassPharos ||
		entry.ReporterRole != externalstage.ReporterRoleOwner || entry.Bytes != len(raw) || entry.SHA256 != hex.EncodeToString(fixtureDigest[:]) {
		t.Fatalf("v2 manifest fixture entry=%+v", entry)
	}
	schemaRaw, err := os.ReadFile(filepath.Join("external-stage-v2.schema.json"))
	if err != nil {
		t.Fatal(err)
	}
	schemaDigest := sha256.Sum256(schemaRaw)
	if manifest.SchemaSHA256 != hex.EncodeToString(schemaDigest[:]) {
		t.Fatalf("v2 manifest schema digest=%s actual=%x", manifest.SchemaSHA256, schemaDigest)
	}
}
