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
	"reflect"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/inspr-at/paimos/backend/contracts"
	"github.com/inspr-at/paimos/backend/externalstage"
	"github.com/spf13/cobra"
)

const externalStageTestHandoffID = "01ARZ3NDEKTSV4RRFFQ69G5FAV"

const externalStageTestIdempotencyKey = "9f1c2d3e-4a5b-4c6d-8e7f-0a1b2c3d4e5f"

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

func TestExternalStageReusableIdempotencyKeyAcceptsOnlyCanonicalForms(t *testing.T) {
	for _, key := range []string{"", externalStageTestIdempotencyKey, externalStageTestHandoffID, "00000000000000000000000000", "7ZZZZZZZZZZZZZZZZZZZZZZZZZ"} {
		if err := (externalStageMutationOptions{idempotencyKey: key}).validate(); err != nil {
			t.Fatalf("canonical key %q rejected: %v", key, err)
		}
	}
	for _, key := range []string{
		"9F1C2D3E-4A5B-4C6D-8E7F-0A1B2C3D4E5F",
		"9f1c2d3e4a5b4c6d8e7f0a1b2c3d4e5f",
		"9f1c2d3e-4a5b-0c6d-8e7f-0a1b2c3d4e5f",
		"9f1c2d3e-4a5b-4c6d-7e7f-0a1b2c3d4e5f",
		"01arz3ndektsv4rrffq69g5fav",
		"8ZZZZZZZZZZZZZZZZZZZZZZZZZ",
		" " + externalStageTestIdempotencyKey,
		externalStageTestIdempotencyKey + "," + externalStageTestHandoffID,
	} {
		if err := (externalStageMutationOptions{idempotencyKey: key}).validate(); err == nil {
			t.Fatalf("non-canonical key %q accepted", key)
		} else if strings.Contains(err.Error(), key) {
			t.Fatal("invalid key was reflected into its validation error")
		}
	}
}

func TestExternalStageReusableIdempotencyFlagIsLimitedToExactReplayJSONOperations(t *testing.T) {
	replayable := []*cobra.Command{
		externalStageCreateCmd(), externalStageRevokeCmd(), externalStageAcceptCmd(), externalStageReportCmd(),
		externalStageRegistrationsCreateCmd(), externalStageRegistrationsRevokeCmd(), externalStagePrerequisitesSealCmd(),
		externalStageOwnerActivateCmd(),
	}
	for _, command := range replayable {
		if command.Flags().Lookup("idempotency-key") == nil {
			t.Fatalf("%s lacks reusable idempotency", command.CommandPath())
		}
	}
	for _, command := range []*cobra.Command{
		externalStagePullCmd(), externalStageRegistrationsListCmd(), externalStageMintCmd(false), externalStageMintCmd(true),
	} {
		if command.Flags().Lookup("idempotency-key") != nil {
			t.Fatalf("%s incorrectly exposes reusable idempotency", command.CommandPath())
		}
	}
	for _, command := range []*cobra.Command{externalStageMintCmd(false), externalStageMintCmd(true)} {
		if !strings.Contains(command.Long, "credential must be rotated before use") {
			t.Fatalf("%s does not explain raw-once lost-response recovery", command.Name())
		}
	}
}

func TestExternalStageReusableIdempotencyDryRunShowsSourceWithoutPrintingKey(t *testing.T) {
	tests := []struct {
		name string
		args []string
	}{
		{name: "handoff-create", args: []string{"external-stage", "create", "issue:4664", "--stage", "deployment", "--execution", "2", "--plan-revision", "4", "--authority-epoch", "3", "--reporter-registration-id", "9", "--expires-at", "2026-08-21T12:00:00Z"}},
		{name: "handoff-revoke", args: []string{"external-stage", "revoke", externalStageTestHandoffID, "--expected-credential-epoch", "1"}},
		{name: "registration-create", args: []string{"external-stage", "registrations", "create", "issue:4664", "--api-key-id", "5", "--class", "pharos", "--role", "owner", "--workflow", "deploy-production", "--environment", "production-eu1"}},
		{name: "registration-revoke", args: []string{"external-stage", "registrations", "revoke", "issue:4664", "9"}},
		{name: "prerequisite-seal", args: []string{"external-stage", "prerequisites", "seal", "issue:4664", "--stage", "deployment", "--execution", "2", "--plan-revision", "4", "--authority-epoch", "3", "--prerequisite", "required:authorization=11"}},
		{name: "owner-activate", args: []string{"external-stage", "owner", "activate", "issue:4664", "--stage", "deployment", "--attempt", "1", "--plan-revision", "4", "--reporter-registration-id", "9"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			args := append([]string{"--json"}, test.args...)
			args = append(args, "--idempotency-key", externalStageTestIdempotencyKey, "--dry-run")
			out, errOut, err := executeCLIForTest(t, args...)
			if err != nil {
				t.Fatalf("dry-run: %v stderr=%s", err, errOut)
			}
			if strings.Contains(out, externalStageTestIdempotencyKey) || strings.Contains(errOut, externalStageTestIdempotencyKey) {
				t.Fatal("dry-run printed the raw idempotency key")
			}
			var plan map[string]any
			if err := json.Unmarshal([]byte(out), &plan); err != nil {
				t.Fatal(err)
			}
			if plan["idempotency_key_source"] != "operator-supplied" {
				t.Fatalf("dry-run idempotency source=%v", plan["idempotency_key_source"])
			}
		})
	}
}

func TestExternalStageReusableIdempotencyKeyOverridesAutoUUIDAcrossExactRetries(t *testing.T) {
	tests := []struct {
		name string
		args func(string, string) []string
	}{
		{name: "handoff-create", args: func(_, _ string) []string {
			return []string{"external-stage", "create", "issue:4664", "--stage", "deployment", "--execution", "2", "--plan-revision", "4", "--authority-epoch", "3", "--reporter-registration-id", "9", "--expires-at", "2026-08-21T12:00:00Z"}
		}},
		{name: "handoff-revoke", args: func(_, _ string) []string {
			return []string{"external-stage", "revoke", externalStageTestHandoffID, "--expected-credential-epoch", "1"}
		}},
		{name: "accept", args: func(secretPath, _ string) []string {
			return []string{"external-stage", "accept", externalStageTestHandoffID, "--observed-at", "2026-08-20T10:00:00Z", "--secret-file", secretPath}
		}},
		{name: "report", args: func(secretPath, reportPath string) []string {
			return []string{"external-stage", "report", externalStageTestHandoffID, "--report-file", reportPath, "--secret-file", secretPath}
		}},
		{name: "registration-create", args: func(_, _ string) []string {
			return []string{"external-stage", "registrations", "create", "issue:4664", "--api-key-id", "5", "--class", "pharos", "--role", "owner", "--workflow", "deploy-production", "--environment", "production-eu1"}
		}},
		{name: "registration-revoke", args: func(_, _ string) []string {
			return []string{"external-stage", "registrations", "revoke", "issue:4664", "9"}
		}},
		{name: "prerequisite-seal", args: func(_, _ string) []string {
			return []string{"external-stage", "prerequisites", "seal", "issue:4664", "--stage", "deployment", "--execution", "2", "--plan-revision", "4", "--authority-epoch", "3", "--prerequisite", "required:authorization=11", "--prerequisite", "optional:credential-handoff=12"}
		}},
		{name: "owner-activate", args: func(_, _ string) []string {
			return []string{"external-stage", "owner", "activate", "issue:4664", "--stage", "deployment", "--attempt", "1", "--plan-revision", "4", "--reporter-registration-id", "9"}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var receivedKeys []string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				values := r.Header.Values(idempotencyHeader)
				if len(values) != 1 {
					t.Errorf("idempotency header cardinality=%d", len(values))
				}
				receivedKeys = append(receivedKeys, r.Header.Get(idempotencyHeader))
				if strings.Contains(r.URL.Path, "external-reporter-registrations") || strings.Contains(r.URL.Path, "external-prerequisite-sets") ||
					strings.Contains(r.URL.Path, "external-owner-activations") {
					w.Header().Set("Content-Type", externalStageAdminMediaType)
					_, _ = w.Write([]byte(`{}`))
					return
				}
				w.Header().Set("Content-Type", externalstage.MediaTypeV1)
				switch {
				case strings.HasSuffix(r.URL.Path, "/accept"):
					_ = json.NewEncoder(w).Encode(externalStageReceiptFixture(1, externalstage.HandoffStateAccepted))
				case strings.HasSuffix(r.URL.Path, "/reports"):
					_ = json.NewEncoder(w).Encode(externalStageReceiptFixture(2, externalstage.HandoffStateSucceeded))
				default:
					_, _ = w.Write([]byte(`{}`))
				}
			}))
			defer server.Close()
			setExternalStageTestInstance(t, server.URL)
			secretPath := writeExternalStageSecretFile(t, externalStageTestSecret, 0o600)
			reportPath := writeExternalStageReportFile(t)
			for attempt := 0; attempt < 2; attempt++ {
				args := append(test.args(secretPath, reportPath), "--idempotency-key", externalStageTestIdempotencyKey)
				out, errOut, err := executeCLIForTest(t, args...)
				if err != nil {
					t.Fatalf("attempt %d: %v stderr=%s", attempt+1, err, errOut)
				}
				for _, output := range []string{out, errOut} {
					if strings.Contains(output, externalStageTestIdempotencyKey) {
						t.Fatal("raw idempotency key entered CLI output")
					}
				}
			}
			if len(receivedKeys) != 2 || receivedKeys[0] != externalStageTestIdempotencyKey || receivedKeys[1] != externalStageTestIdempotencyKey {
				t.Fatalf("retry headers=%v", receivedKeys)
			}
		})
	}
}

func TestExternalStageReusableIdempotencyKeyNeverEntersReflectedSuccessOrErrorOutput(t *testing.T) {
	tests := []struct {
		name        string
		contentType string
		status      int
		body        any
		args        []string
	}{
		{
			name: "vendor-success", contentType: externalstage.MediaTypeV1, status: http.StatusOK,
			body: externalstage.HandoffMetadata{HandoffID: externalStageTestHandoffID, DeliveryKey: externalStageTestIdempotencyKey},
			args: []string{"external-stage", "create", "issue:4664", "--stage", "deployment", "--execution", "2", "--plan-revision", "4", "--authority-epoch", "3", "--reporter-registration-id", "9", "--expires-at", "2026-08-21T12:00:00Z"},
		},
		{
			name: "admin-success", contentType: externalStageAdminMediaType, status: http.StatusOK,
			body: externalStageRegistration{RegistrationID: 9, Workflow: externalStageTestIdempotencyKey},
			args: []string{"external-stage", "registrations", "create", "issue:4664", "--api-key-id", "5", "--class", "pharos", "--role", "owner", "--workflow", "deploy-production", "--environment", "production-eu1"},
		},
		{
			name: "vendor-error", contentType: externalstage.MediaTypeV1, status: http.StatusConflict,
			body: map[string]string{"error": externalStageTestIdempotencyKey},
			args: []string{"external-stage", "revoke", externalStageTestHandoffID, "--expected-credential-epoch", "1"},
		},
		{
			name: "admin-error", contentType: externalStageAdminMediaType, status: http.StatusConflict,
			body: map[string]string{"error": externalStageTestIdempotencyKey},
			args: []string{"external-stage", "registrations", "revoke", "issue:4664", "9"},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", test.contentType)
				w.WriteHeader(test.status)
				_ = json.NewEncoder(w).Encode(test.body)
			}))
			defer server.Close()
			setExternalStageTestInstance(t, server.URL)
			args := append(test.args, "--idempotency-key", externalStageTestIdempotencyKey)
			out, errOut, err := executeCLIForTest(t, args...)
			if err == nil {
				t.Fatal("reflected idempotency key response was accepted")
			}
			for _, output := range []string{out, errOut, err.Error()} {
				if strings.Contains(output, externalStageTestIdempotencyKey) {
					t.Fatal("raw idempotency key entered CLI output")
				}
			}
		})
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
			request.Prerequisites[0] != (externalStagePrerequisite{DependencyKey: "authorization", ReporterRegistrationID: 11, Requirement: "required"}) ||
			request.Prerequisites[1] != (externalStagePrerequisite{DependencyKey: "credential-handoff", ReporterRegistrationID: 12, Requirement: "optional"}) {
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
		"--prerequisite", "required:authorization=11", "--prerequisite", "optional:credential-handoff=12")
	if err != nil {
		t.Fatalf("seal: %v stderr=%s", err, errOut)
	}
	var response externalStagePrerequisiteSet
	if err := json.Unmarshal([]byte(out), &response); err != nil || response.DeclaredCount != 2 {
		t.Fatalf("seal response=%s error=%v", out, err)
	}
	assertExternalStageOutputHasNoSecret(t, out, errOut, nil)
}

func TestExternalStageOwnerActivateUsesLiteralRouteAndExactCAS(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/agent-mode/deliveries/issue:4664/external-owner-activations" {
			t.Errorf("request=%s %s", r.Method, r.URL.Path)
		}
		if r.Header.Get(idempotencyHeader) == "" || r.Header.Get("Content-Type") != externalStageAdminMediaType ||
			r.Header.Get("Accept") != externalStageAdminMediaType || r.Header.Get(externalstage.HandoffSecretHeader) != "" {
			t.Errorf("activation headers=%v", r.Header)
		}
		var request externalStageOwnerActivationRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
		}
		want := externalStageOwnerActivationRequest{ReporterRegistrationID: 9, StageKey: "verification",
			ExpectedAttemptNumber: 3, ExpectedPlanRevision: 4,
			ExpectedCurrentExecution: 2, ExpectedCurrentAuthorityEpoch: 5}
		if request != want {
			t.Errorf("activation request=%+v want %+v", request, want)
		}
		w.Header().Set("Content-Type", externalStageAdminMediaType)
		_ = json.NewEncoder(w).Encode(externalStageOwnerActivation{DeliveryKey: "issue:4664",
			ReporterRegistrationID: 9, StageKey: "verification", AttemptNumber: 3, PlanRevision: 4,
			ExecutionNumber: 3, AuthorityEpoch: 2, ReporterID: 7})
	}))
	defer server.Close()
	setExternalStageTestInstance(t, server.URL)
	out, errOut, err := executeCLIForTest(t, "--json", "external-stage", "owner", "activate", "issue:4664",
		"--stage", "verification", "--attempt", "3", "--plan-revision", "4", "--reporter-registration-id", "9",
		"--current-execution", "2", "--current-authority-epoch", "5")
	if err != nil {
		t.Fatalf("activate: %v stderr=%s", err, errOut)
	}
	var response externalStageOwnerActivation
	if err := json.Unmarshal([]byte(out), &response); err != nil || response.ExecutionNumber != 3 || response.AuthorityEpoch != 2 {
		t.Fatalf("activation response=%s err=%v", out, err)
	}
	assertExternalStageOutputHasNoSecret(t, out, errOut, nil)
}

func TestExternalStagePrerequisitesModelEmptyOptionalAndMixedSets(t *testing.T) {
	tests := []struct {
		name string
		raw  []string
		want []externalStagePrerequisite
	}{
		{name: "empty", raw: nil, want: []externalStagePrerequisite{}},
		{name: "optional-only", raw: []string{"optional:authorization=11"}, want: []externalStagePrerequisite{
			{DependencyKey: "authorization", ReporterRegistrationID: 11, Requirement: "optional"},
		}},
		{name: "mixed", raw: []string{"required:authorization=11", "optional:credential-handoff=12"}, want: []externalStagePrerequisite{
			{DependencyKey: "authorization", ReporterRegistrationID: 11, Requirement: "required"},
			{DependencyKey: "credential-handoff", ReporterRegistrationID: 12, Requirement: "optional"},
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			parsed, err := parseExternalStagePrerequisites(test.raw)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(parsed, test.want) {
				t.Fatalf("parsed=%+v want %+v", parsed, test.want)
			}
			request := externalStagePrerequisiteSetRequest{
				StageKey: "deployment", ExecutionNumber: 2, ExpectedPlanRevision: 4,
				ExpectedAuthorityEpoch: 3, Prerequisites: parsed,
			}
			if err := validateExternalStagePrerequisiteSet(request); err != nil {
				t.Fatal(err)
			}
			args := []string{
				"--json", "external-stage", "prerequisites", "seal", "issue:4664",
				"--stage", "deployment", "--execution", "2", "--plan-revision", "4",
				"--authority-epoch", "3", "--dry-run",
			}
			for _, prerequisite := range test.raw {
				args = append(args, "--prerequisite", prerequisite)
			}
			out, errOut, err := executeCLIForTest(t, args...)
			if err != nil {
				t.Fatalf("dry-run: %v stderr=%s", err, errOut)
			}
			var plan struct {
				Body externalStagePrerequisiteSetRequest `json:"body"`
			}
			if err := json.Unmarshal([]byte(out), &plan); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(plan.Body.Prerequisites, test.want) {
				t.Fatalf("dry-run prerequisites=%+v want %+v", plan.Body.Prerequisites, test.want)
			}
			if test.name == "empty" && plan.Body.Prerequisites == nil {
				t.Fatalf("empty seal did not emit an explicit JSON array: %s", out)
			}
		})
	}
}

func TestExternalStagePrerequisiteSealSendsEmptyAndOptionalOnlyArrays(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want []externalStagePrerequisite
	}{
		{name: "empty", want: []externalStagePrerequisite{}},
		{name: "optional-only", args: []string{"--prerequisite", "optional:authorization=11"}, want: []externalStagePrerequisite{
			{DependencyKey: "authorization", ReporterRegistrationID: 11, Requirement: "optional"},
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				raw, err := io.ReadAll(r.Body)
				if err != nil {
					t.Error(err)
				}
				if test.name == "empty" && !bytes.Contains(raw, []byte(`"prerequisites":[]`)) {
					t.Errorf("empty request body=%s", raw)
				}
				var request externalStagePrerequisiteSetRequest
				if err := json.Unmarshal(raw, &request); err != nil {
					t.Error(err)
				}
				if !reflect.DeepEqual(request.Prerequisites, test.want) {
					t.Errorf("request prerequisites=%+v want %+v", request.Prerequisites, test.want)
				}
				w.Header().Set("Content-Type", externalStageAdminMediaType)
				_ = json.NewEncoder(w).Encode(externalStagePrerequisiteSet{
					DeliveryKey: "issue:4664", StageKey: "deployment", ExecutionNumber: 2,
					PlanRevision: 4, AuthorityEpoch: 3, DeclaredCount: len(test.want), SealedAt: "2026-08-21T09:00:00Z",
				})
			}))
			defer server.Close()
			setExternalStageTestInstance(t, server.URL)
			args := []string{
				"--json", "external-stage", "prerequisites", "seal", "issue:4664",
				"--stage", "deployment", "--execution", "2", "--plan-revision", "4", "--authority-epoch", "3",
			}
			args = append(args, test.args...)
			if _, errOut, err := executeCLIForTest(t, args...); err != nil {
				t.Fatalf("seal: %v stderr=%s", err, errOut)
			}
		})
	}
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
		{"authorization=11"},
		{"mandatory:authorization=11"},
		{"Required:authorization=11"},
		{"required:Authorization=11"},
		{"required:authorization=0"},
		{"required:authorization=11", "optional:authorization=12"},
		{"required:authorization=11", "optional:credential-handoff=11"},
	} {
		if parsed, err := parseExternalStagePrerequisites(invalid); err == nil {
			t.Fatalf("invalid prerequisite bindings accepted: %+v", parsed)
		}
	}
	tooMany := make([]string, 17)
	for index := range tooMany {
		tooMany[index] = fmt.Sprintf("required:dependency-%d=%d", index, index+1)
	}
	if _, err := parseExternalStagePrerequisites(tooMany); err == nil {
		t.Fatal("more than 16 prerequisite bindings accepted")
	}
	validJanus := externalStageRegistrationRequest{
		APIKeyID: 6, ReporterClass: "janus", ReporterRole: "dependency", DependencyKey: "authorization",
	}
	if err := validateExternalStageRegistration(validJanus); err != nil {
		t.Fatalf("valid Janus registration rejected: %v", err)
	}
	validOwner := externalStageOwnerActivationRequest{ReporterRegistrationID: 9, StageKey: "deployment",
		ExpectedAttemptNumber: 1, ExpectedPlanRevision: 4}
	for _, test := range []struct {
		name   string
		mutate func(*externalStageOwnerActivationRequest)
	}{
		{name: "owner-registration", mutate: func(r *externalStageOwnerActivationRequest) { r.ReporterRegistrationID = 0 }},
		{name: "owner-stage", mutate: func(r *externalStageOwnerActivationRequest) { r.StageKey = "qa" }},
		{name: "owner-attempt", mutate: func(r *externalStageOwnerActivationRequest) { r.ExpectedAttemptNumber = 0 }},
		{name: "owner-plan", mutate: func(r *externalStageOwnerActivationRequest) { r.ExpectedPlanRevision = 0 }},
		{name: "owner-negative-execution", mutate: func(r *externalStageOwnerActivationRequest) { r.ExpectedCurrentExecution = -1 }},
		{name: "owner-negative-epoch", mutate: func(r *externalStageOwnerActivationRequest) { r.ExpectedCurrentAuthorityEpoch = -1 }},
		{name: "owner-half-zero", mutate: func(r *externalStageOwnerActivationRequest) { r.ExpectedCurrentExecution = 2 }},
	} {
		t.Run(test.name, func(t *testing.T) {
			request := validOwner
			test.mutate(&request)
			if err := validateExternalStageOwnerActivation(request); err == nil {
				t.Fatal("mutated owner activation accepted")
			}
		})
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
		if r.Header.Get(idempotencyHeader) == "" {
			t.Error("mint omitted its mandatory one-shot idempotency header")
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

func TestExternalStagePullResponseValidatesEveryKnownStringFieldAndCorrelation(t *testing.T) {
	valid := externalStagePullFixture()
	if err := validateExternalStagePullResponse(externalStageTestHandoffID, valid, nil); err != nil {
		t.Fatalf("valid pull response rejected: %v", err)
	}
	tests := []struct {
		name   string
		mutate func(*externalstage.PullResponse)
	}{
		{name: "handoff_id", mutate: func(r *externalstage.PullResponse) { r.HandoffID = "01ARZ3NDEKTSV4RRFFQ69G5FA0" }},
		{name: "fixture_digest", mutate: func(r *externalstage.PullResponse) { r.FixtureDigest = "sha256:" + strings.Repeat("0", 64) }},
		{name: "expires_at", mutate: func(r *externalstage.PullResponse) { r.ExpiresAt = "not-a-time" }},
		{name: "state", mutate: func(r *externalstage.PullResponse) { r.State = externalstage.HandoffState("custom") }},
		{name: "reporter_class", mutate: func(r *externalstage.PullResponse) { r.ReporterClass = externalstage.ReporterClassPharos }},
		{name: "reporter_role", mutate: func(r *externalstage.PullResponse) { r.ReporterRole = externalstage.ReporterRoleOwner }},
		{name: "dependency_key", mutate: func(r *externalstage.PullResponse) { r.DependencyKey = "" }},
		{name: "evidence_ceiling", mutate: func(r *externalstage.PullResponse) {
			r.EvidenceCeiling = []externalstage.EvidenceKind{externalstage.EvidenceKindDeployment}
		}},
		{name: "stage_key", mutate: func(r *externalstage.PullResponse) { r.StageKey = "custom" }},
		{name: "plan_digest", mutate: func(r *externalstage.PullResponse) { r.PlanDigest = "sha256:" + strings.Repeat("A", 64) }},
		{name: "predecessor_digest", mutate: func(r *externalstage.PullResponse) { r.PredecessorDigest = "invalid" }},
		{name: "context_digest", mutate: func(r *externalstage.PullResponse) { r.ContextDigest = "invalid" }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			response := valid
			response.EvidenceCeiling = append([]externalstage.EvidenceKind(nil), valid.EvidenceCeiling...)
			test.mutate(&response)
			if err := validateExternalStagePullResponse(externalStageTestHandoffID, response, nil); err == nil {
				t.Fatal("semantic mutation was accepted")
			}
		})
	}
}

func TestExternalStageReportReceiptValidatesEveryKnownStringFieldAndRequestBinding(t *testing.T) {
	valid := externalStageReceiptFixture(2, externalstage.HandoffStateSucceeded)
	if err := validateExternalStageReportReceipt(externalStageTestHandoffID, 2, externalstage.HandoffStateSucceeded, valid, nil); err != nil {
		t.Fatalf("valid receipt rejected: %v", err)
	}
	for _, test := range []struct {
		name   string
		mutate func(*externalstage.ReportReceipt)
	}{
		{name: "handoff_id", mutate: func(r *externalstage.ReportReceipt) { r.HandoffID = "01ARZ3NDEKTSV4RRFFQ69G5FA0" }},
		{name: "state", mutate: func(r *externalstage.ReportReceipt) { r.State = externalstage.HandoffStateActive }},
		{name: "server_received_at", mutate: func(r *externalstage.ReportReceipt) { r.ServerReceivedAt = "not-a-time" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			receipt := valid
			test.mutate(&receipt)
			if err := validateExternalStageReportReceipt(externalStageTestHandoffID, 2, externalstage.HandoffStateSucceeded, receipt, nil); err == nil {
				t.Fatal("semantic mutation was accepted")
			}
		})
	}
}

func TestExternalStagePullRejectsRawAndBase64SecretReflectionFromEveryStringField(t *testing.T) {
	fields := []struct {
		name   string
		mutate func(*externalstage.PullResponse, string)
	}{
		{name: "handoff_id", mutate: func(r *externalstage.PullResponse, v string) { r.HandoffID = v }},
		{name: "fixture_digest", mutate: func(r *externalstage.PullResponse, v string) { r.FixtureDigest = v }},
		{name: "expires_at", mutate: func(r *externalstage.PullResponse, v string) { r.ExpiresAt = v }},
		{name: "state", mutate: func(r *externalstage.PullResponse, v string) { r.State = externalstage.HandoffState(v) }},
		{name: "reporter_class", mutate: func(r *externalstage.PullResponse, v string) { r.ReporterClass = externalstage.ReporterClass(v) }},
		{name: "reporter_role", mutate: func(r *externalstage.PullResponse, v string) { r.ReporterRole = externalstage.ReporterRole(v) }},
		{name: "dependency_key", mutate: func(r *externalstage.PullResponse, v string) { r.DependencyKey = v }},
		{name: "evidence_ceiling", mutate: func(r *externalstage.PullResponse, v string) {
			r.EvidenceCeiling = []externalstage.EvidenceKind{externalstage.EvidenceKind(v)}
		}},
		{name: "stage_key", mutate: func(r *externalstage.PullResponse, v string) { r.StageKey = v }},
		{name: "plan_digest", mutate: func(r *externalstage.PullResponse, v string) { r.PlanDigest = v }},
		{name: "predecessor_digest", mutate: func(r *externalstage.PullResponse, v string) { r.PredecessorDigest = v }},
		{name: "context_digest", mutate: func(r *externalstage.PullResponse, v string) { r.ContextDigest = v }},
	}
	representations := []struct {
		name  string
		value string
	}{
		{name: "raw", value: string(externalStageTestSecret)},
		{name: "base64url", value: base64.RawURLEncoding.EncodeToString(externalStageTestSecret)},
	}
	for _, field := range fields {
		for _, representation := range representations {
			t.Run(field.name+"/"+representation.name, func(t *testing.T) {
				response := externalStagePullFixture()
				field.mutate(&response, representation.value)
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					w.Header().Set("Content-Type", externalstage.MediaTypeV1)
					_ = json.NewEncoder(w).Encode(response)
				}))
				defer server.Close()
				setExternalStageTestInstance(t, server.URL)
				secretPath := writeExternalStageSecretFile(t, externalStageTestSecret, 0o600)
				out, errOut, err := executeCLIForTest(t, "--json", "external-stage", "pull", externalStageTestHandoffID, "--secret-file", secretPath)
				if err == nil {
					t.Fatal("reflected credential was accepted")
				}
				assertExternalStageOutputHasNoSecret(t, out, errOut, err)
			})
		}
	}
}

func TestExternalStageAcceptAndReportRejectSecretReflectionFromEveryReceiptStringField(t *testing.T) {
	fields := []struct {
		name   string
		mutate func(*externalstage.ReportReceipt, string)
	}{
		{name: "handoff_id", mutate: func(r *externalstage.ReportReceipt, v string) { r.HandoffID = v }},
		{name: "state", mutate: func(r *externalstage.ReportReceipt, v string) { r.State = externalstage.HandoffState(v) }},
		{name: "server_received_at", mutate: func(r *externalstage.ReportReceipt, v string) { r.ServerReceivedAt = v }},
	}
	representations := []struct {
		name  string
		value string
	}{
		{name: "raw", value: string(externalStageTestSecret)},
		{name: "base64url", value: base64.RawURLEncoding.EncodeToString(externalStageTestSecret)},
	}
	commands := []struct {
		name  string
		state externalstage.HandoffState
		seq   int64
		args  func(string, string) []string
	}{
		{name: "accept", state: externalstage.HandoffStateAccepted, seq: 1, args: func(secretPath, _ string) []string {
			return []string{"--json", "external-stage", "accept", externalStageTestHandoffID, "--observed-at", "2026-08-20T10:00:00Z", "--secret-file", secretPath}
		}},
		{name: "report", state: externalstage.HandoffStateSucceeded, seq: 2, args: func(secretPath, reportPath string) []string {
			return []string{"--json", "external-stage", "report", externalStageTestHandoffID, "--report-file", reportPath, "--secret-file", secretPath}
		}},
	}
	for _, command := range commands {
		for _, field := range fields {
			for _, representation := range representations {
				t.Run(command.name+"/"+field.name+"/"+representation.name, func(t *testing.T) {
					receipt := externalStageReceiptFixture(command.seq, command.state)
					field.mutate(&receipt, representation.value)
					server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
						w.Header().Set("Content-Type", externalstage.MediaTypeV1)
						_ = json.NewEncoder(w).Encode(receipt)
					}))
					defer server.Close()
					setExternalStageTestInstance(t, server.URL)
					secretPath := writeExternalStageSecretFile(t, externalStageTestSecret, 0o600)
					reportPath := writeExternalStageReportFile(t)
					out, errOut, err := executeCLIForTest(t, command.args(secretPath, reportPath)...)
					if err == nil {
						t.Fatal("reflected credential was accepted")
					}
					assertExternalStageOutputHasNoSecret(t, out, errOut, err)
				})
			}
		}
	}
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
		FixtureDigest: "sha256:" + contracts.ExternalStageV1FixtureDigestHex, CredentialEpoch: 1,
		ExpiresAt: "2026-08-21T12:00:00Z", State: externalstage.HandoffStateIssued,
		ReporterClass: externalstage.ReporterClassJanus, ReporterRole: externalstage.ReporterRoleDependency,
		DependencyKey: "privileged-handoff", EvidenceCeiling: []externalstage.EvidenceKind{externalstage.EvidenceKindAuthorization},
		StageKey: "deployment", ExecutionNumber: 1,
		PlanDigest: "sha256:" + strings.Repeat("2", 64), PredecessorDigest: "sha256:" + strings.Repeat("3", 64),
		AuthorityEpoch: 1, ContextDigest: "sha256:" + strings.Repeat("4", 64),
	}
}

func externalStageReceiptFixture(sequence int64, state externalstage.HandoffState) externalstage.ReportReceipt {
	return externalstage.ReportReceipt{
		HandoffID: externalStageTestHandoffID, Sequence: sequence, State: state,
		CredentialEpoch: 1, ServerReceivedAt: "2026-08-20T10:01:01Z",
	}
}

func writeExternalStageReportFile(t *testing.T) string {
	t.Helper()
	reportPath := filepath.Join(t.TempDir(), "report.json")
	raw, err := json.Marshal(validExternalStagePharosReport(strings.Repeat("a", 40)))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(reportPath, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	return reportPath
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
