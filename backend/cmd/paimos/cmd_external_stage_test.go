// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/inspr-at/paimos/backend/externalstage"
)

const externalStageTestHandoffID = "01ARZ3NDEKTSV4RRFFQ69G5FAV"

var externalStageTestSecret = []byte("0123456789abcdefghijklmnopqrstuv")

func TestExternalStageCreateDryRunUsesFrozenRouteAndDTO(t *testing.T) {
	out, _, err := executeCLIForTest(t,
		"--json", "external-stage", "create", "issue:4664",
		"--stage", "deployment", "--execution", "2", "--plan-revision", "4",
		"--authority-epoch", "3", "--reporter-registration-id", "9",
		"--expires-at", "2026-08-21T12:00:00Z", "--dry-run",
	)
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Method string                             `json:"method"`
		Path   string                             `json:"path"`
		Body   externalstage.CreateHandoffRequest `json:"body"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatal(err)
	}
	if got.Method != http.MethodPost || got.Path != "/api/agent-mode/deliveries/issue:4664/external-stage-handoffs" ||
		got.Body.StageKey != "deployment" || got.Body.ExecutionNumber != 2 || got.Body.ExpectedPlanRevision != 4 ||
		got.Body.ExpectedAuthorityEpoch != 3 || got.Body.ReporterRegistrationID != 9 {
		t.Fatalf("dry-run contract=%+v", got)
	}
}

func TestExternalStageRegistrationAdminCLIUsesLiteralRoutesAndSafeDTOs(t *testing.T) {
	registration := externalStageRegistration{
		RegistrationID: 9, ReporterID: 7, APIKeyID: 5,
		ReporterClass: externalstage.ReporterClassPharos, ReporterRole: externalstage.ReporterRoleOwner,
		Workflow: "deploy-production", Environment: "production-eu1",
		EvidenceCeiling: []externalstage.EvidenceKind{externalstage.EvidenceKindDeployment, externalstage.EvidenceKindVerification},
		CreatedAt:       "2026-08-21T09:00:00Z",
	}
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.Header.Get(externalstage.HandoffSecretHeader) != "" {
			t.Error("admin plane unexpectedly received handoff credential")
		}
		w.Header().Set("Content-Type", externalStageAdminMediaType)
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/agent-mode/deliveries/issue:4664/external-reporter-registrations":
			if r.Header.Get(idempotencyHeader) != "" {
				t.Error("registration discovery unexpectedly sent idempotency header")
			}
			_ = json.NewEncoder(w).Encode(externalStageRegistrationList{Registrations: []externalStageRegistration{registration}})
		case r.Method == http.MethodPost && r.URL.Path == "/api/agent-mode/deliveries/issue:4664/external-reporter-registrations":
			if r.Header.Get(idempotencyHeader) == "" || r.Header.Get("Content-Type") != externalStageAdminMediaType || r.Header.Get("Accept") != externalStageAdminMediaType {
				t.Errorf("registration create headers=%v", r.Header)
			}
			var request externalStageRegistrationRequest
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				t.Error(err)
			}
			if request.APIKeyID != 5 || request.ReporterClass != "pharos" || request.ReporterRole != "owner" ||
				request.Workflow != "deploy-production" || request.Environment != "production-eu1" || request.DependencyKey != "" {
				t.Errorf("registration request=%+v", request)
			}
			_ = json.NewEncoder(w).Encode(registration)
		case r.Method == http.MethodPost && r.URL.Path == "/api/agent-mode/deliveries/issue:4664/external-reporter-registrations/9/revoke":
			if r.Header.Get(idempotencyHeader) == "" {
				t.Error("registration revoke omitted idempotency header")
			}
			raw, _ := io.ReadAll(r.Body)
			if string(raw) != `{}` {
				t.Errorf("revoke body=%s want {}", raw)
			}
			revoked := registration
			revoked.RevokedAt = "2026-08-21T10:00:00Z"
			_ = json.NewEncoder(w).Encode(revoked)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	setExternalStageTestInstance(t, server.URL)

	listOut, listErrOut, err := executeCLIForTest(t, "--json", "external-stage", "registrations", "list", "issue:4664")
	if err != nil {
		t.Fatalf("list: %v stderr=%s", err, listErrOut)
	}
	var list externalStageRegistrationList
	if err := json.Unmarshal([]byte(listOut), &list); err != nil || len(list.Registrations) != 1 || list.Registrations[0].RegistrationID != 9 {
		t.Fatalf("registration discovery=%s error=%v", listOut, err)
	}

	createOut, createErrOut, err := executeCLIForTest(t,
		"--json", "external-stage", "registrations", "create", "issue:4664",
		"--api-key-id", "5", "--class", "pharos", "--role", "owner",
		"--workflow", "deploy-production", "--environment", "production-eu1")
	if err != nil {
		t.Fatalf("create: %v stderr=%s", err, createErrOut)
	}
	var created externalStageRegistration
	if err := json.Unmarshal([]byte(createOut), &created); err != nil || created.RegistrationID != 9 {
		t.Fatalf("created registration=%s error=%v", createOut, err)
	}
	_, _, err = executeCLIForTest(t,
		"--json", "external-stage", "create", "issue:4664", "--stage", "deployment",
		"--execution", "1", "--plan-revision", "3", "--authority-epoch", "2",
		"--reporter-registration-id", fmt.Sprint(created.RegistrationID),
		"--expires-at", "2026-08-22T12:00:00Z", "--dry-run")
	if err != nil {
		t.Fatalf("handoff create did not consume discovered registration id: %v", err)
	}

	revokeOut, revokeErrOut, err := executeCLIForTest(t,
		"--json", "external-stage", "registrations", "revoke", "issue:4664", "9")
	if err != nil {
		t.Fatalf("revoke: %v stderr=%s", err, revokeErrOut)
	}
	var revoked externalStageRegistration
	if err := json.Unmarshal([]byte(revokeOut), &revoked); err != nil || revoked.RevokedAt == "" {
		t.Fatalf("revoked registration=%s error=%v", revokeOut, err)
	}
	if requests.Load() != 3 {
		t.Fatalf("admin requests=%d want 3", requests.Load())
	}
	assertExternalStageOutputHasNoSecret(t, listOut+createOut+revokeOut, listErrOut+createErrOut+revokeErrOut, nil)
}

func TestExternalStagePrerequisitesSealUsesLiteralRouteAndExactBindings(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/agent-mode/deliveries/issue:4664/external-prerequisite-sets" {
			t.Errorf("request=%s %s", r.Method, r.URL.Path)
		}
		if r.Header.Get(idempotencyHeader) == "" || r.Header.Get("Content-Type") != externalStageAdminMediaType ||
			r.Header.Get(externalstage.HandoffSecretHeader) != "" {
			t.Errorf("seal headers=%v", r.Header)
		}
		var request externalStagePrerequisiteSetRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
		}
		if request.StageKey != "deployment" || request.ExecutionNumber != 2 || request.ExpectedPlanRevision != 4 ||
			request.ExpectedAuthorityEpoch != 3 || len(request.Prerequisites) != 2 ||
			request.Prerequisites[0] != (externalStagePrerequisite{DependencyKey: "authorization", ReporterRegistrationID: 11}) ||
			request.Prerequisites[1] != (externalStagePrerequisite{DependencyKey: "credential-handoff", ReporterRegistrationID: 12}) {
			t.Errorf("seal request=%+v", request)
		}
		w.Header().Set("Content-Type", externalStageAdminMediaType)
		_ = json.NewEncoder(w).Encode(externalStagePrerequisiteSet{
			DeliveryKey: "issue:4664", StageKey: "deployment", ExecutionNumber: 2,
			PlanRevision: 4, AuthorityEpoch: 3, DeclaredCount: 2, SealedAt: "2026-08-21T09:00:00Z",
		})
	}))
	defer server.Close()
	setExternalStageTestInstance(t, server.URL)
	out, errOut, err := executeCLIForTest(t,
		"--json", "external-stage", "prerequisites", "seal", "issue:4664",
		"--stage", "deployment", "--execution", "2", "--plan-revision", "4", "--authority-epoch", "3",
		"--prerequisite", "authorization=11", "--prerequisite", "credential-handoff=12")
	if err != nil {
		t.Fatalf("seal: %v stderr=%s", err, errOut)
	}
	var response externalStagePrerequisiteSet
	if err := json.Unmarshal([]byte(out), &response); err != nil || response.DeclaredCount != 2 {
		t.Fatalf("seal response=%s error=%v", out, err)
	}
	assertExternalStageOutputHasNoSecret(t, out, errOut, nil)
}

func TestExternalStageAdminValidationRejectsMutationsBeforeNetwork(t *testing.T) {
	validPharos := externalStageRegistrationRequest{
		APIKeyID: 5, ReporterClass: "pharos", ReporterRole: "owner", Workflow: "deploy-production", Environment: "production-eu1",
	}
	for _, test := range []struct {
		name   string
		mutate func(*externalStageRegistrationRequest)
	}{
		{name: "api-key", mutate: func(r *externalStageRegistrationRequest) { r.APIKeyID = 0 }},
		{name: "class", mutate: func(r *externalStageRegistrationRequest) { r.ReporterClass = "custom" }},
		{name: "pharos-role", mutate: func(r *externalStageRegistrationRequest) { r.ReporterRole = "dependency" }},
		{name: "pharos-dependency", mutate: func(r *externalStageRegistrationRequest) { r.DependencyKey = "authorization" }},
		{name: "pharos-workflow", mutate: func(r *externalStageRegistrationRequest) { r.Workflow = "Deploy Production" }},
		{name: "pharos-environment", mutate: func(r *externalStageRegistrationRequest) { r.Environment = "" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			request := validPharos
			test.mutate(&request)
			if err := validateExternalStageRegistration(request); err == nil {
				t.Fatal("mutated registration accepted")
			}
		})
	}
	for _, invalid := range [][]string{
		nil,
		{"Authorization=11"},
		{"authorization=0"},
		{"authorization=11", "authorization=12"},
		{"authorization=11", "credential-handoff=11"},
	} {
		if parsed, err := parseExternalStagePrerequisites(invalid); err == nil {
			t.Fatalf("invalid prerequisite bindings accepted: %+v", parsed)
		}
	}
	validJanus := externalStageRegistrationRequest{
		APIKeyID: 6, ReporterClass: "janus", ReporterRole: "dependency", DependencyKey: "authorization",
	}
	if err := validateExternalStageRegistration(validJanus); err != nil {
		t.Fatalf("valid Janus registration rejected: %v", err)
	}
}

func TestExternalStageMintStreamsExactRawBytesToNew0600File(t *testing.T) {
	originalBufferFactory := externalStageNewCopyBuffer
	var capturedCopyBuffer []byte
	externalStageNewCopyBuffer = func() []byte {
		capturedCopyBuffer = make([]byte, externalstage.OneTimeSecretBytes)
		return capturedCopyBuffer
	}
	t.Cleanup(func() { externalStageNewCopyBuffer = originalBufferFactory })
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.Method != http.MethodPost || r.URL.Path != "/api/agent-mode/external-stage-handoffs/"+externalStageTestHandoffID+"/mint" {
			t.Errorf("request=%s %s", r.Method, r.URL.Path)
		}
		if r.Header.Get("Accept") != externalstage.SecretMediaTypeV1 || r.Header.Get("Content-Type") != externalstage.MediaTypeV1 {
			t.Errorf("media accept=%q content=%q", r.Header.Get("Accept"), r.Header.Get("Content-Type"))
		}
		if got := r.Header.Get(externalstage.HandoffSecretHeader); got != "" {
			t.Errorf("mint unexpectedly sent handoff credential header")
		}
		body, _ := io.ReadAll(r.Body)
		if string(body) != `{"expected_credential_epoch":0}` {
			t.Errorf("body=%s", body)
		}
		w.Header().Set("Content-Type", externalstage.SecretMediaTypeV1)
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write(externalStageTestSecret)
	}))
	defer server.Close()
	setExternalStageTestInstance(t, server.URL)
	outPath := filepath.Join(t.TempDir(), "handoff.bin")
	out, errOut, err := executeCLIForTest(t,
		"external-stage", "mint", externalStageTestHandoffID,
		"--expected-credential-epoch", "0", "--secret-output", outPath,
	)
	if err != nil {
		t.Fatalf("mint: %v stderr=%s", err, errOut)
	}
	if requests.Load() != 1 {
		t.Fatalf("requests=%d", requests.Load())
	}
	raw, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(raw, externalStageTestSecret) {
		t.Fatal("output did not contain the exact raw response bytes")
	}
	info, err := os.Stat(outPath)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("output mode=%o want 600", info.Mode().Perm())
	}
	if len(capturedCopyBuffer) != externalstage.OneTimeSecretBytes ||
		!bytes.Equal(capturedCopyBuffer, make([]byte, len(capturedCopyBuffer))) {
		t.Fatal("mint response copy buffer was not zeroed after durable capture")
	}
	assertExternalStageOutputHasNoSecret(t, out, errOut, nil)
}

func TestExternalStageMintRejectsExistingTargetBeforeRequest(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { requests.Add(1) }))
	defer server.Close()
	setExternalStageTestInstance(t, server.URL)
	outPath := filepath.Join(t.TempDir(), "existing.bin")
	if err := os.WriteFile(outPath, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	out, errOut, err := executeCLIForTest(t,
		"external-stage", "mint", externalStageTestHandoffID, "--secret-output", outPath)
	if err == nil {
		t.Fatal("existing target accepted")
	}
	if requests.Load() != 0 {
		t.Fatalf("request sent before O_EXCL failure: %d", requests.Load())
	}
	if raw, _ := os.ReadFile(outPath); string(raw) != "keep" {
		t.Fatal("existing target changed")
	}
	assertExternalStageOutputHasNoSecret(t, out, errOut, err)
}

func TestExternalStageMintRemovesIncompleteOutputAndPrintsNoResponseBytes(t *testing.T) {
	overlong := append(append([]byte(nil), externalStageTestSecret...), 'x')
	originalBufferFactory := externalStageNewCopyBuffer
	var capturedCopyBuffer []byte
	externalStageNewCopyBuffer = func() []byte {
		capturedCopyBuffer = make([]byte, externalstage.OneTimeSecretBytes)
		return capturedCopyBuffer
	}
	t.Cleanup(func() { externalStageNewCopyBuffer = originalBufferFactory })
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", externalstage.SecretMediaTypeV1)
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write(overlong)
	}))
	defer server.Close()
	setExternalStageTestInstance(t, server.URL)
	outPath := filepath.Join(t.TempDir(), "partial.bin")
	out, errOut, err := executeCLIForTest(t,
		"external-stage", "mint", externalStageTestHandoffID, "--secret-output", outPath)
	if err == nil || !strings.Contains(err.Error(), "rotate before use") {
		t.Fatalf("overlong response error=%v", err)
	}
	if _, statErr := os.Stat(outPath); !os.IsNotExist(statErr) {
		t.Fatalf("incomplete output remains: %v", statErr)
	}
	if len(capturedCopyBuffer) != externalstage.OneTimeSecretBytes ||
		!bytes.Equal(capturedCopyBuffer, make([]byte, len(capturedCopyBuffer))) {
		t.Fatal("rejected mint response copy buffer was not zeroed")
	}
	assertExternalStageOutputHasNoSecret(t, out, errOut, err)
	for _, output := range []string{out, errOut, err.Error()} {
		if strings.Contains(output, string(overlong)) {
			t.Fatal("overlong response entered CLI output")
		}
	}
}

func TestExternalStageMintRemovesOutputWhenParentDirectorySyncFails(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", externalstage.SecretMediaTypeV1)
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write(externalStageTestSecret)
	}))
	defer server.Close()
	setExternalStageTestInstance(t, server.URL)
	originalSync := externalStageSyncDirectory
	externalStageSyncDirectory = func(string) error { return fmt.Errorf("injected directory sync failure") }
	t.Cleanup(func() { externalStageSyncDirectory = originalSync })
	outPath := filepath.Join(t.TempDir(), "unsynced.bin")
	out, errOut, err := executeCLIForTest(t,
		"external-stage", "mint", externalStageTestHandoffID, "--secret-output", outPath)
	if err == nil || !strings.Contains(err.Error(), "rotate before use") {
		t.Fatalf("directory sync error=%v", err)
	}
	if _, statErr := os.Stat(outPath); !os.IsNotExist(statErr) {
		t.Fatalf("unsynced credential output remains: %v", statErr)
	}
	assertExternalStageOutputHasNoSecret(t, out, errOut, err)
}

func TestExternalStagePullUsesProtectedRawFileAndHeaderOnly(t *testing.T) {
	wantHeader := base64.RawURLEncoding.EncodeToString(externalStageTestSecret)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/external-stage/handoffs/"+externalStageTestHandoffID {
			t.Errorf("request=%s %s", r.Method, r.URL.Path)
		}
		if r.URL.RawQuery != "" || r.Header.Get("Cookie") != "" {
			t.Errorf("unsafe credential channel query=%q cookie=%q", r.URL.RawQuery, r.Header.Get("Cookie"))
		}
		if got := r.Header.Get(externalstage.HandoffSecretHeader); got != wantHeader {
			t.Errorf("handoff header=%q want canonical base64url", got)
		}
		body, _ := io.ReadAll(r.Body)
		if len(body) != 0 {
			t.Errorf("GET body=%q", body)
		}
		w.Header().Set("Content-Type", externalstage.MediaTypeV1)
		_ = json.NewEncoder(w).Encode(externalStagePullFixture())
	}))
	defer server.Close()
	setExternalStageTestInstance(t, server.URL)
	secretPath := writeExternalStageSecretFile(t, externalStageTestSecret, 0o600)
	out, errOut, err := executeCLIForTest(t,
		"--json", "external-stage", "pull", externalStageTestHandoffID, "--secret-file", secretPath)
	if err != nil {
		t.Fatalf("pull: %v stderr=%s", err, errOut)
	}
	assertExternalStageOutputHasNoSecret(t, out, errOut, nil)
}

func TestExternalStagePullReadsRawSecretFromStdin(t *testing.T) {
	wantHeader := base64.RawURLEncoding.EncodeToString(externalStageTestSecret)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get(externalstage.HandoffSecretHeader) != wantHeader {
			t.Error("stdin credential did not reach the canonical header")
		}
		w.Header().Set("Content-Type", externalstage.MediaTypeV1)
		_ = json.NewEncoder(w).Encode(externalStagePullFixture())
	}))
	defer server.Close()
	setExternalStageTestInstance(t, server.URL)
	withExternalStageStdin(t, externalStageTestSecret)
	out, errOut, err := executeCLIForTest(t,
		"external-stage", "pull", externalStageTestHandoffID, "--secret-stdin")
	if err != nil {
		t.Fatal(err)
	}
	assertExternalStageOutputHasNoSecret(t, out, errOut, nil)
}

func TestExternalStageSecretFileMutationsFailBeforeNetwork(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix file ownership/link/permission contract")
	}
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { requests.Add(1) }))
	defer server.Close()
	setExternalStageTestInstance(t, server.URL)
	directory := t.TempDir()
	goodPath := filepath.Join(directory, "good.bin")
	if err := os.WriteFile(goodPath, externalStageTestSecret, 0o600); err != nil {
		t.Fatal(err)
	}
	permissivePath := filepath.Join(directory, "permissive.bin")
	if err := os.WriteFile(permissivePath, externalStageTestSecret, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(permissivePath, 0o640); err != nil {
		t.Fatal(err)
	}
	symlinkPath := filepath.Join(directory, "symlink.bin")
	if err := os.Symlink(goodPath, symlinkPath); err != nil {
		t.Fatal(err)
	}
	hardlinkPath := filepath.Join(directory, "hardlink.bin")
	if err := os.Link(goodPath, hardlinkPath); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name string
		path string
	}{
		{name: "group-readable", path: permissivePath},
		{name: "symlink", path: symlinkPath},
		{name: "multiple-links", path: goodPath},
	} {
		t.Run(test.name, func(t *testing.T) {
			out, errOut, err := executeCLIForTest(t,
				"external-stage", "pull", externalStageTestHandoffID, "--secret-file", test.path)
			if err == nil {
				t.Fatal("unsafe credential file accepted")
			}
			assertExternalStageOutputHasNoSecret(t, out, errOut, err)
		})
	}
	if requests.Load() != 0 {
		t.Fatalf("unsafe file reached network %d times", requests.Load())
	}
}

func TestExternalStageSecretOwnershipMetadataRejectsDifferentUID(t *testing.T) {
	if err := validateExternalStageSecretMetadata(0o600, 1001, 1002, 1); err == nil {
		t.Fatal("different owner uid accepted")
	}
	if err := validateExternalStageSecretMetadata(0o600, 1001, 1001, 1); err != nil {
		t.Fatalf("valid metadata rejected: %v", err)
	}
}

func TestExternalStageSecretInputRequiresExact32BytesAndClearsRejectedBuffer(t *testing.T) {
	for _, size := range []int{31, 33} {
		t.Run(fmt.Sprintf("bytes-%d", size), func(t *testing.T) {
			raw := bytes.Repeat([]byte{'q'}, size)
			path := writeExternalStageSecretFile(t, raw, 0o600)
			_, err := readExternalStageSecret(externalStageSecretInput{file: path})
			if err == nil || !strings.Contains(err.Error(), "exactly 32") {
				t.Fatalf("size %d error=%v", size, err)
			}
			if strings.Contains(err.Error(), string(raw)) {
				t.Fatal("rejected raw credential entered error")
			}
		})
	}
}

func TestExternalStageRawSecretZeroizationHelpers(t *testing.T) {
	rejected := bytes.Repeat([]byte{'r'}, externalstage.OneTimeSecretBytes+1)
	rejectExternalStageSecretBytes(rejected)
	if !bytes.Equal(rejected, make([]byte, len(rejected))) {
		t.Fatal("rejected raw credential buffer was not zeroed")
	}
	returned := append([]byte(nil), externalStageTestSecret...)
	clearExternalStageSecret(returned)
	if !bytes.Equal(returned, make([]byte, len(returned))) {
		t.Fatal("post-use raw credential buffer was not zeroed")
	}
	source, err := os.ReadFile("cmd_external_stage.go")
	if err != nil {
		t.Fatal(err)
	}
	if reads, clears := bytes.Count(source, []byte("rawSecret, err := readExternalStageSecret(secret)")), bytes.Count(source, []byte("defer clearExternalStageSecret(rawSecret)")); reads != 3 || clears != reads {
		t.Fatalf("raw credential read/defer-clear sites reads=%d clears=%d", reads, clears)
	}
}

func TestExternalStagePartialReadErrorZeroesAllocatedRawBuffer(t *testing.T) {
	reader := &externalStageBytesAndErrorReader{raw: append([]byte(nil), externalStageTestSecret...)}
	if _, err := readExternalStageSecretBytes(reader); err == nil {
		t.Fatal("injected partial read error returned success")
	}
	if len(reader.captured) != len(externalStageTestSecret) || !bytes.Equal(reader.captured, make([]byte, len(reader.captured))) {
		t.Fatalf("partial raw buffer was not zeroed: %x", reader.captured)
	}
}

func TestExternalStageSecretBearingHTTPErrorConcealsBodyAndOpaquePath(t *testing.T) {
	encoded := base64.RawURLEncoding.EncodeToString(externalStageTestSecret)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"` + encoded + `"}`))
	}))
	defer server.Close()
	setExternalStageTestInstance(t, server.URL)
	secretPath := writeExternalStageSecretFile(t, externalStageTestSecret, 0o600)
	out, errOut, err := executeCLIForTest(t,
		"external-stage", "pull", externalStageTestHandoffID, "--secret-file", secretPath)
	if err == nil {
		t.Fatal("HTTP rejection returned success")
	}
	assertExternalStageOutputHasNoSecret(t, out, errOut, err)
	for _, value := range []string{out, errOut, err.Error()} {
		if strings.Contains(value, externalStageTestHandoffID) || strings.Contains(value, encoded) {
			t.Fatalf("secret-bearing error leaked opaque path or response body: %q", value)
		}
	}
}

func TestExternalStageSecretBearingRequestNeverFollowsRedirect(t *testing.T) {
	var redirectedRequests atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		redirectedRequests.Add(1)
		if r.Header.Get(externalstage.HandoffSecretHeader) != "" {
			t.Error("handoff credential was forwarded across a redirect")
		}
	}))
	defer target.Close()
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Location", target.URL+"/capture")
		w.WriteHeader(http.StatusTemporaryRedirect)
	}))
	defer origin.Close()
	setExternalStageTestInstance(t, origin.URL)
	secretPath := writeExternalStageSecretFile(t, externalStageTestSecret, 0o600)
	out, errOut, err := executeCLIForTest(t,
		"external-stage", "pull", externalStageTestHandoffID, "--secret-file", secretPath)
	if err == nil {
		t.Fatal("redirect unexpectedly returned success")
	}
	if redirectedRequests.Load() != 0 {
		t.Fatalf("secret-bearing redirect was followed %d times", redirectedRequests.Load())
	}
	assertExternalStageOutputHasNoSecret(t, out, errOut, err)
}

func TestExternalStageSecretBearingSuccessSchemaErrorConcealsReflectedCredential(t *testing.T) {
	encoded := base64.RawURLEncoding.EncodeToString(externalStageTestSecret)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", externalstage.MediaTypeV1)
		_, _ = w.Write([]byte(`{"` + encoded + `":true}`))
	}))
	defer server.Close()
	setExternalStageTestInstance(t, server.URL)
	secretPath := writeExternalStageSecretFile(t, externalStageTestSecret, 0o600)
	out, errOut, err := executeCLIForTest(t,
		"external-stage", "pull", externalStageTestHandoffID, "--secret-file", secretPath)
	if err == nil {
		t.Fatal("schema-mutated success returned success")
	}
	assertExternalStageOutputHasNoSecret(t, out, errOut, err)
}

func TestExternalStageReportRejectsUnknownJSONBeforeSecretOrNetwork(t *testing.T) {
	reportPath := filepath.Join(t.TempDir(), "report.json")
	if err := os.WriteFile(reportPath, []byte(`{"sequence":2,"state":"active","observed_at":"2026-08-20T10:00:00Z","heartbeat":true,"unknown":"x"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	out, errOut, err := executeCLIForTest(t,
		"external-stage", "report", externalStageTestHandoffID,
		"--report-file", reportPath, "--secret-file", filepath.Join(t.TempDir(), "missing"))
	if err == nil || !strings.Contains(err.Error(), "unknown") {
		t.Fatalf("unknown report field error=%v", err)
	}
	assertExternalStageOutputHasNoSecret(t, out, errOut, err)
}

func TestExternalStageReportValidationRejectsFrozenContractMutationsBeforeSecretOrNetwork(t *testing.T) {
	falseValue := false
	trueValue := true
	tests := []struct {
		name   string
		mutate func(*externalstage.ReportRequest)
	}{
		{name: "too-many-blockers", mutate: func(r *externalstage.ReportRequest) {
			r.BlockerCodes = []externalstage.BlockerCode{"dependency_pending", "dependency_failed", "reporter_stale", "external_waiting", "dependency_pending", "dependency_failed", "reporter_stale", "external_waiting", "dependency_pending"}
		}},
		{name: "duplicate-blocker", mutate: func(r *externalstage.ReportRequest) {
			r.BlockerCodes = []externalstage.BlockerCode{"dependency_pending", "dependency_pending"}
		}},
		{name: "unknown-blocker", mutate: func(r *externalstage.ReportRequest) { r.BlockerCodes = []externalstage.BlockerCode{"custom"} }},
		{name: "heartbeat-evidence", mutate: func(r *externalstage.ReportRequest) { r.Heartbeat = true }},
		{name: "heartbeat-terminal", mutate: func(r *externalstage.ReportRequest) { r.Heartbeat = true; r.PharosEvidence = nil }},
		{name: "pharos-kind", mutate: func(r *externalstage.ReportRequest) { r.PharosEvidence.Kind = externalstage.EvidenceKindAuthorization }},
		{name: "pharos-result", mutate: func(r *externalstage.ReportRequest) { r.PharosEvidence.Result = externalstage.EvidenceResultSatisfied }},
		{name: "pharos-workflow", mutate: func(r *externalstage.ReportRequest) { r.PharosEvidence.Workflow = "Deploy Production" }},
		{name: "pharos-environment", mutate: func(r *externalstage.ReportRequest) { r.PharosEvidence.Environment = "https://example.test" }},
		{name: "artifact-version", mutate: func(r *externalstage.ReportRequest) { r.PharosEvidence.Artifact.Version = "bad version" }},
		{name: "artifact-digest", mutate: func(r *externalstage.ReportRequest) {
			r.PharosEvidence.Artifact.Digest = "sha256:" + strings.Repeat("A", 64)
		}},
		{name: "commit-uppercase", mutate: func(r *externalstage.ReportRequest) { r.PharosEvidence.Artifact.CommitDigest = strings.Repeat("A", 40) }},
		{name: "commit-length", mutate: func(r *externalstage.ReportRequest) { r.PharosEvidence.Artifact.CommitDigest = strings.Repeat("a", 41) }},
		{name: "pharos-observed-at", mutate: func(r *externalstage.ReportRequest) { r.PharosEvidence.ObservedAt = "2026-08-20T10:01:01Z" }},
		{name: "pharos-state", mutate: func(r *externalstage.ReportRequest) { r.State = externalstage.HandoffStateFailed }},
		{name: "janus-kind", mutate: func(r *externalstage.ReportRequest) {
			*r = validExternalStageJanusReport()
			r.JanusEvidence.Kind = externalstage.EvidenceKindDeployment
		}},
		{name: "janus-result", mutate: func(r *externalstage.ReportRequest) {
			*r = validExternalStageJanusReport()
			r.JanusEvidence.Result = externalstage.EvidenceResultSucceeded
		}},
		{name: "janus-both-booleans", mutate: func(r *externalstage.ReportRequest) {
			*r = validExternalStageJanusReport()
			r.JanusEvidence.CredentialReady = &trueValue
		}},
		{name: "janus-missing-boolean", mutate: func(r *externalstage.ReportRequest) {
			*r = validExternalStageJanusReport()
			r.JanusEvidence.Authorized = nil
		}},
		{name: "janus-boolean-coherence", mutate: func(r *externalstage.ReportRequest) {
			*r = validExternalStageJanusReport()
			r.JanusEvidence.Authorized = &falseValue
		}},
		{name: "janus-observed-at", mutate: func(r *externalstage.ReportRequest) {
			*r = validExternalStageJanusReport()
			r.JanusEvidence.ObservedAt = "2026-08-20T10:01:01Z"
		}},
		{name: "janus-state", mutate: func(r *externalstage.ReportRequest) {
			*r = validExternalStageJanusReport()
			r.State = externalstage.HandoffStateBlocked
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			report := validExternalStagePharosReport(strings.Repeat("a", 40))
			test.mutate(&report)
			if err := validateExternalStageReport(report); err == nil {
				t.Fatal("mutated report passed local validation")
			}
			reportPath := filepath.Join(t.TempDir(), "report.json")
			raw, err := json.Marshal(report)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(reportPath, raw, 0o600); err != nil {
				t.Fatal(err)
			}
			_, _, err = executeCLIForTest(t,
				"external-stage", "report", externalStageTestHandoffID,
				"--report-file", reportPath, "--secret-file", filepath.Join(t.TempDir(), "missing"))
			if err == nil || strings.Contains(err.Error(), "credential file") {
				t.Fatalf("mutation was not rejected before secret input: %v", err)
			}
		})
	}
}

func TestExternalStageReportAcceptsOnlyLowercase40Or64HexCommitDigests(t *testing.T) {
	for _, width := range []int{40, 64} {
		report := validExternalStagePharosReport(strings.Repeat("a", width))
		if err := validateExternalStageReport(report); err != nil {
			t.Fatalf("lowercase %d-hex commit rejected: %v", width, err)
		}
	}
	for _, invalid := range []string{strings.Repeat("a", 39), strings.Repeat("a", 41), strings.Repeat("a", 63), strings.Repeat("a", 65), strings.Repeat("A", 40)} {
		report := validExternalStagePharosReport(invalid)
		if err := validateExternalStageReport(report); err == nil {
			t.Fatalf("invalid commit digest accepted: len=%d", len(invalid))
		}
	}
}

func TestExternalStageCLIHasNoRawSecretArgumentOrOutputMode(t *testing.T) {
	c := externalStageCmd()
	for _, child := range c.Commands() {
		if child.Flags().Lookup("secret") != nil || child.Flags().Lookup("unsafe-secret-stdout") != nil {
			t.Fatalf("%s exposes a raw credential argv/output flag", child.Name())
		}
	}
}

func setExternalStageTestInstance(t *testing.T, url string) {
	t.Helper()
	t.Setenv(envURL, url)
	t.Setenv(envAPIKey, "separate-test-api-key")
}

func writeExternalStageSecretFile(t *testing.T, raw []byte, mode os.FileMode) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "handoff.bin")
	if err := os.WriteFile(path, raw, mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
	return path
}

func withExternalStageStdin(t *testing.T, raw []byte) {
	t.Helper()
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	original := os.Stdin
	os.Stdin = reader
	t.Cleanup(func() {
		os.Stdin = original
		_ = reader.Close()
	})
	go func() {
		_, _ = writer.Write(raw)
		_ = writer.Close()
	}()
}

func assertExternalStageOutputHasNoSecret(t *testing.T, out, errOut string, err error) {
	t.Helper()
	encoded := base64.RawURLEncoding.EncodeToString(externalStageTestSecret)
	values := []string{out, errOut}
	if err != nil {
		values = append(values, err.Error())
	}
	for _, value := range values {
		if strings.Contains(value, string(externalStageTestSecret)) || strings.Contains(value, encoded) {
			t.Fatalf("raw or encoded handoff credential entered CLI output: %q", value)
		}
	}
}

func externalStagePullFixture() externalstage.PullResponse {
	return externalstage.PullResponse{
		HandoffID: externalStageTestHandoffID, ContractMajor: externalstage.ContractMajor,
		FixtureDigest: "sha256:" + strings.Repeat("1", 64), CredentialEpoch: 1,
		ExpiresAt: "2026-08-21T12:00:00Z", State: externalstage.HandoffStateIssued,
		ReporterClass: externalstage.ReporterClassJanus, ReporterRole: externalstage.ReporterRoleDependency,
		DependencyKey: "privileged-handoff", EvidenceCeiling: []externalstage.EvidenceKind{externalstage.EvidenceKindAuthorization},
		StageKey: "deployment", ExecutionNumber: 1,
		PlanDigest: "sha256:" + strings.Repeat("2", 64), PredecessorDigest: "sha256:" + strings.Repeat("3", 64),
		AuthorityEpoch: 1, ContextDigest: "sha256:" + strings.Repeat("4", 64),
	}
}

func validExternalStagePharosReport(commitDigest string) externalstage.ReportRequest {
	return externalstage.ReportRequest{
		Sequence: 2, State: externalstage.HandoffStateSucceeded,
		ObservedAt: "2026-08-20T10:01:00Z", Heartbeat: false,
		PharosEvidence: &externalstage.PharosEvidence{
			Kind: externalstage.EvidenceKindDeployment, Workflow: "deploy-production", Environment: "production-eu1",
			Artifact: externalstage.ArtifactEvidence{
				Version: "1.2.3", Digest: "sha256:" + strings.Repeat("1", 64), CommitDigest: commitDigest,
			},
			Result: externalstage.EvidenceResultSucceeded, ObservedAt: "2026-08-20T10:01:00Z",
		},
	}
}

func validExternalStageJanusReport() externalstage.ReportRequest {
	authorized := true
	return externalstage.ReportRequest{
		Sequence: 2, State: externalstage.HandoffStateSucceeded,
		ObservedAt: "2026-08-20T10:01:00Z", Heartbeat: false,
		JanusEvidence: &externalstage.JanusEvidence{
			Kind: externalstage.EvidenceKindAuthorization, Result: externalstage.EvidenceResultSatisfied,
			Authorized: &authorized, ObservedAt: "2026-08-20T10:01:00Z",
		},
	}
}

type externalStageBytesAndErrorReader struct {
	raw      []byte
	captured []byte
}

func (reader *externalStageBytesAndErrorReader) Read(buffer []byte) (int, error) {
	count := copy(buffer, reader.raw)
	reader.captured = buffer[:count]
	reader.raw = nil
	return count, errors.New("injected read failure")
}
