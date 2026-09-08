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

	"github.com/inspr-at/paimos/backend/baselinebatch"
	"github.com/inspr-at/paimos/backend/externalstage"
)

func TestBaselineBatchReportBuiltDryRun(t *testing.T) {
	out, errOut, err := executeCLIForTest(t,
		"--json", "baseline-batch", "report-built",
		"--project", "PAI", "--batch-id", "12", "--dry-run",
		"--idempotency-key", "receipt-cli-01",
		"--expected-attempt-id", "1", "--expected-plan-revision", "1",
		"--commit", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		"--oci-config-digest", "sha256:3333333333333333333333333333333333333333333333333333333333333333",
		"--release-manifest-digest", "sha256:6666666666666666666666666666666666666666666666666666666666666666",
		"--release-coordinate", "ghcr:inspr-at/pharos/releases/26.09.07.12.00.00",
		"--oci-index-digest", "sha256:4444444444444444444444444444444444444444444444444444444444444444",
		"--scheme", string(externalstage.VersionSchemeINSPRCalendar),
		"--channel", "stable", "--sequence", "260907120000",
		"--version", "26.09.07.12.00.00",
		"--qa-digest", "5555555555555555555555555555555555555555555555555555555555555555",
	)
	if err != nil {
		t.Fatalf("dry-run: %v err=%s", err, errOut)
	}
	var got struct {
		Method string                            `json:"method"`
		Path   string                            `json:"path"`
		Body   baselinebatch.BuiltReceiptRequest `json:"body"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatal(err)
	}
	if got.Method != http.MethodPost || !strings.Contains(got.Path, "/baseline-batches/batches/12/built-receipt") {
		t.Fatalf("dry-run route=%+v", got)
	}
	if got.Body.Commit == "" || got.Body.VersionScheme != string(externalstage.VersionSchemeINSPRCalendar) || got.Body.QADigest == "" {
		t.Fatalf("dry-run body=%+v", got.Body)
	}
	if strings.Contains(out, "secret") || strings.Contains(errOut, "Bearer") {
		t.Fatal("dry-run leaked a secret")
	}
}

func TestBaselineBatchReportBuiltUnknownFieldFailsBeforeNetwork(t *testing.T) {
	path := filepath.Join(t.TempDir(), "receipt.json")
	if err := os.WriteFile(path, []byte(`{
		"idempotency_key":"receipt-cli-unknown",
		"expected_attempt_id":1,
		"expected_plan_revision":1,
		"expected_implementation_execution":0,
		"expected_implementation_authority_epoch":0,
		"commit":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		"oci_config_digest":"sha256:3333333333333333333333333333333333333333333333333333333333333333",
		"release_manifest_digest":"sha256:6666666666666666666666666666666666666666666666666666666666666666",
		"release_manifest_coordinate":"ghcr:inspr-at/pharos/releases/26.09.07.12.00.00",
		"version_scheme":"inspr-calendar-v1",
		"release_channel":"stable",
		"release_sequence":260907120000,
		"version":"26.09.07.12.00.00",
		"qa_digest":"5555555555555555555555555555555555555555555555555555555555555555",
		"implementation_result_digest":"3333333333333333333333333333333333333333333333333333333333333333",
		"handoff_secret":"should-not-be-accepted"
	}`), 0o600); err != nil {
		t.Fatal(err)
	}
	out, errOut, err := executeCLIForTest(t,
		"baseline-batch", "report-built",
		"--project", "PAI", "--batch-id", "12",
		"--receipt-file", path,
	)
	if err == nil {
		t.Fatal("unknown field accepted")
	}
	combined := out + errOut + err.Error()
	if !strings.Contains(strings.ToLower(combined), "unknown field") && !strings.Contains(strings.ToLower(combined), "invalid receipt") {
		t.Fatalf("unknown field error=%q out=%q err=%q", err, out, errOut)
	}
	if strings.Contains(combined, "should-not-be-accepted") || strings.Contains(combined, "handoff_secret") {
		t.Fatal("rejected receipt reflected a secret")
	}
}

func TestBaselineBatchReportBuiltDuplicateFieldsFailBeforeNetwork(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	hits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits++
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)
	t.Setenv(envURL, srv.URL)
	t.Setenv(envAPIKey, "test_key")

	valid := `{
		"idempotency_key":"receipt-cli-dup",
		"expected_attempt_id":1,
		"expected_plan_revision":1,
		"expected_implementation_execution":0,
		"expected_implementation_authority_epoch":0,
		"commit":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		"oci_config_digest":"sha256:3333333333333333333333333333333333333333333333333333333333333333",
		"release_manifest_digest":"sha256:6666666666666666666666666666666666666666666666666666666666666666",
		"release_manifest_coordinate":"ghcr:inspr-at/pharos/releases/26.09.07.12.00.00",
		"version_scheme":"inspr-calendar-v1",
		"release_channel":"stable",
		"release_sequence":260907120000,
		"version":"26.09.07.12.00.00",
		"qa_digest":"5555555555555555555555555555555555555555555555555555555555555555"
	}`
	for _, tc := range []struct {
		name string
		raw  string
	}{
		{name: "commit", raw: strings.Replace(valid, `"commit":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"`,
			`"commit":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","commit":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"`, 1)},
		{name: "cas", raw: strings.Replace(valid, `"expected_implementation_execution":0`,
			`"expected_implementation_execution":0,"expected_implementation_execution":7`, 1)},
		{name: "idempotency", raw: strings.Replace(valid, `"idempotency_key":"receipt-cli-dup"`,
			`"idempotency_key":"receipt-cli-dup","idempotency_key":"receipt-cli-other"`, 1)},
	} {
		path := filepath.Join(t.TempDir(), tc.name+".json")
		if err := os.WriteFile(path, []byte(tc.raw), 0o600); err != nil {
			t.Fatal(err)
		}
		out, errOut, err := executeCLIForTest(t,
			"baseline-batch", "report-built",
			"--project", "PAI", "--batch-id", "12",
			"--receipt-file", path,
		)
		if err == nil {
			t.Fatalf("%s duplicate accepted out=%s", tc.name, out)
		}
		combined := out + errOut + err.Error()
		if !strings.Contains(strings.ToLower(combined), "invalid receipt") {
			t.Fatalf("%s error=%q out=%q err=%q", tc.name, err, out, errOut)
		}
		if strings.Contains(combined, "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb") {
			t.Fatalf("%s reflected duplicate commit", tc.name)
		}
	}
	if hits != 0 {
		t.Fatalf("CLI transport called %d times", hits)
	}
}

func TestBaselineBatchReportBuiltReceiptFileIsBoundedBeforeAllocating(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	hits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits++
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)
	t.Setenv(envURL, srv.URL)
	t.Setenv(envAPIKey, "test_key")
	path := filepath.Join(t.TempDir(), "huge.json")
	if err := os.WriteFile(path, []byte(strings.Repeat("a", baselinebatch.BuiltReceiptMaxJSONBytes+4096)), 0o600); err != nil {
		t.Fatal(err)
	}
	_, errOut, err := executeCLIForTest(t,
		"baseline-batch", "report-built",
		"--project", "PAI", "--batch-id", "12",
		"--receipt-file", path,
	)
	if err == nil {
		t.Fatal("oversize receipt accepted")
	}
	if !strings.Contains(strings.ToLower(err.Error()+" "+errOut), "invalid receipt") {
		t.Fatalf("oversize error=%v errOut=%s", err, errOut)
	}
	if hits != 0 {
		t.Fatalf("oversize CLI transport called %d times", hits)
	}
}
