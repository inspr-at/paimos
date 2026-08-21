// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU Affero General Public License as
// published by the Free Software Foundation, version 3.

package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/inspr-at/paimos/backend/contracts"
	"github.com/inspr-at/paimos/backend/externalstage"
	"github.com/spf13/cobra"
)

const externalStageMaxJSONBytes = 1 << 20

const (
	externalStageAdminMediaType         = "application/json"
	externalStageRegistrationsPath      = "/api/agent-mode/deliveries/{deliveryKey}/external-reporter-registrations"
	externalStageRegistrationRevokePath = "/api/agent-mode/deliveries/{deliveryKey}/external-reporter-registrations/{registrationID}/revoke"
	externalStagePrerequisiteSetsPath   = "/api/agent-mode/deliveries/{deliveryKey}/external-prerequisite-sets"
)

var (
	externalStageHandoffIDPattern = regexp.MustCompile(`^[0-9A-HJKMNP-TV-Z]{26}$`)
	externalStageDeliveryPattern  = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/-]{0,127}$`)
	externalStageSymbolPattern    = regexp.MustCompile(`^[a-z][a-z0-9._-]{0,63}$`)
	externalStageVersionPattern   = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._+-]{0,63}$`)
	externalStageDigestPattern    = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
	externalStageCommitPattern    = regexp.MustCompile(`^(?:[0-9a-f]{40}|[0-9a-f]{64})$`)
	externalStageUUIDPattern      = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[1-8][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)
	externalStageSyncDirectory    = syncExternalStageOutputDirectory
	externalStageNewCopyBuffer    = func() []byte { return make([]byte, externalstage.OneTimeSecretBytes) }
)

type externalStageSecretInput struct {
	file  string
	stdin bool
}

type externalStageMutationOptions struct {
	idempotencyKey string
}

type externalStageRegistrationRequest struct {
	APIKeyID      int64  `json:"api_key_id"`
	ReporterClass string `json:"reporter_class"`
	ReporterRole  string `json:"reporter_role"`
	DependencyKey string `json:"dependency_key,omitempty"`
	Workflow      string `json:"workflow,omitempty"`
	Environment   string `json:"environment,omitempty"`
}

type externalStageRegistration struct {
	RegistrationID  int64                        `json:"registration_id"`
	ReporterID      int64                        `json:"reporter_id"`
	APIKeyID        int64                        `json:"api_key_id"`
	ReporterClass   externalstage.ReporterClass  `json:"reporter_class"`
	ReporterRole    externalstage.ReporterRole   `json:"reporter_role"`
	DependencyKey   string                       `json:"dependency_key,omitempty"`
	Workflow        string                       `json:"workflow,omitempty"`
	Environment     string                       `json:"environment,omitempty"`
	EvidenceCeiling []externalstage.EvidenceKind `json:"evidence_ceiling"`
	CreatedAt       string                       `json:"created_at"`
	RevokedAt       string                       `json:"revoked_at,omitempty"`
}

type externalStageRegistrationList struct {
	Registrations []externalStageRegistration `json:"registrations"`
}

type externalStagePrerequisite struct {
	DependencyKey          string `json:"dependency_key"`
	ReporterRegistrationID int64  `json:"reporter_registration_id"`
	Requirement            string `json:"requirement"`
}

type externalStagePrerequisiteSetRequest struct {
	StageKey               string                      `json:"stage_key"`
	ExecutionNumber        int64                       `json:"execution_number"`
	ExpectedPlanRevision   int64                       `json:"expected_plan_revision"`
	ExpectedAuthorityEpoch int64                       `json:"expected_authority_epoch"`
	Prerequisites          []externalStagePrerequisite `json:"prerequisites"`
}

type externalStagePrerequisiteSet struct {
	DeliveryKey     string `json:"delivery_key"`
	StageKey        string `json:"stage_key"`
	ExecutionNumber int64  `json:"execution_number"`
	PlanRevision    int64  `json:"plan_revision"`
	AuthorityEpoch  int64  `json:"authority_epoch"`
	DeclaredCount   int    `json:"declared_count"`
	SealedAt        string `json:"sealed_at"`
}

func externalStageCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "external-stage",
		Short: "Create and report pinned v1 external delivery-stage handoffs",
		Long: `Create internal handoffs and drive the pinned external-stage v1 protocol.

The independent 32-byte handoff credential is never accepted as an argument,
environment variable, URL, query value, cookie, JSON value, or output. Mint and
rotate require a new output file and create it with owner-only permissions before
the request is sent. Pull, accept, and report read raw credential bytes only from
an owner-only file or stdin and send their base64url form only in the
X-PAIMOS-Handoff-Secret request header.`,
	}
	c.AddCommand(externalStageCreateCmd())
	c.AddCommand(externalStageRegistrationsCmd())
	c.AddCommand(externalStagePrerequisitesCmd())
	c.AddCommand(externalStageMintCmd(false))
	c.AddCommand(externalStageMintCmd(true))
	c.AddCommand(externalStageRevokeCmd())
	c.AddCommand(externalStagePullCmd())
	c.AddCommand(externalStageAcceptCmd())
	c.AddCommand(externalStageReportCmd())
	return c
}

func externalStageCreateCmd() *cobra.Command {
	var request externalstage.CreateHandoffRequest
	var mutation externalStageMutationOptions
	var dryRun bool
	c := &cobra.Command{
		Use:   "create <delivery-key>",
		Short: "Create safe handoff metadata without minting a credential",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			deliveryKey, err := validateExternalStageDeliveryKey(args[0])
			if err != nil {
				return err
			}
			if err := validateExternalStageCreate(request); err != nil {
				return err
			}
			if err := mutation.validate(); err != nil {
				return err
			}
			path := strings.Replace(externalstage.InternalCreatePath, "{deliveryKey}", url.PathEscape(deliveryKey), 1)
			if dryRun {
				return emitJSON(externalStageDryRunPlan(http.MethodPost, path, request, mutation))
			}
			client, err := instanceClient()
			if err != nil {
				return err
			}
			var metadata externalstage.HandoffMetadata
			if err := externalStageJSONRoundTrip(client, http.MethodPost, path, request, nil, mutation.idempotencyKey, &metadata); err != nil {
				return reportError(err)
			}
			return emitExternalStageResult(metadata,
				fmt.Sprintf("created external-stage handoff %s for %s", metadata.HandoffID, metadata.DeliveryKey))
		},
	}
	c.Flags().StringVar(&request.StageKey, "stage", "", "bound delivery stage key")
	c.Flags().Int64Var(&request.ExecutionNumber, "execution", 0, "expected current stage execution number")
	c.Flags().Int64Var(&request.ExpectedPlanRevision, "plan-revision", 0, "expected immutable attempt plan revision")
	c.Flags().Int64Var(&request.ExpectedAuthorityEpoch, "authority-epoch", 0, "expected current delivery authority epoch")
	c.Flags().Int64Var(&request.ReporterRegistrationID, "reporter-registration-id", 0, "server-owned reporter registration id")
	c.Flags().StringVar(&request.ExpiresAt, "expires-at", "", "handoff expiry in RFC3339 form")
	addExternalStageReusableIdempotencyFlag(c, &mutation)
	c.Flags().BoolVar(&dryRun, "dry-run", false, "print the safe request without sending it")
	return c
}

func externalStageRegistrationsCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "registrations",
		Short: "Discover and administer audited external reporter registrations",
	}
	c.AddCommand(externalStageRegistrationsListCmd())
	c.AddCommand(externalStageRegistrationsCreateCmd())
	c.AddCommand(externalStageRegistrationsRevokeCmd())
	return c
}

func externalStageRegistrationsListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list <delivery-key>",
		Short: "List current non-revoked reporter registrations for one delivery",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			deliveryKey, err := validateExternalStageDeliveryKey(args[0])
			if err != nil {
				return err
			}
			client, err := instanceClient()
			if err != nil {
				return err
			}
			path := externalStageDeliveryAdminPath(externalStageRegistrationsPath, deliveryKey)
			var response externalStageRegistrationList
			if err := externalStageAdminJSONRoundTrip(client, http.MethodGet, path, nil, "", &response); err != nil {
				return reportError(err)
			}
			return emitExternalStageResult(response,
				fmt.Sprintf("found %d external reporter registrations for %s", len(response.Registrations), deliveryKey))
		},
	}
}

func externalStageRegistrationsCreateCmd() *cobra.Command {
	var request externalStageRegistrationRequest
	var mutation externalStageMutationOptions
	var dryRun bool
	c := &cobra.Command{
		Use:   "create <delivery-key>",
		Short: "Create one audited server-owned reporter registration",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			deliveryKey, err := validateExternalStageDeliveryKey(args[0])
			if err != nil {
				return err
			}
			if err := validateExternalStageRegistration(request); err != nil {
				return err
			}
			if err := mutation.validate(); err != nil {
				return err
			}
			path := externalStageDeliveryAdminPath(externalStageRegistrationsPath, deliveryKey)
			if dryRun {
				return emitJSON(externalStageDryRunPlan(http.MethodPost, path, request, mutation))
			}
			client, err := instanceClient()
			if err != nil {
				return err
			}
			var response externalStageRegistration
			if err := externalStageAdminJSONRoundTrip(client, http.MethodPost, path, request, mutation.idempotencyKey, &response); err != nil {
				return reportError(err)
			}
			return emitExternalStageResult(response,
				fmt.Sprintf("created external reporter registration %d for %s", response.RegistrationID, deliveryKey))
		},
	}
	c.Flags().Int64Var(&request.APIKeyID, "api-key-id", 0, "exact server-owned API key id bound to the reporter")
	c.Flags().StringVar(&request.ReporterClass, "class", "", "reporter class: pharos or janus")
	c.Flags().StringVar(&request.ReporterRole, "role", "", "reporter role: owner or dependency")
	c.Flags().StringVar(&request.DependencyKey, "dependency", "", "Janus dependency key")
	c.Flags().StringVar(&request.Workflow, "workflow", "", "Pharos workflow symbol")
	c.Flags().StringVar(&request.Environment, "environment", "", "Pharos environment symbol")
	addExternalStageReusableIdempotencyFlag(c, &mutation)
	c.Flags().BoolVar(&dryRun, "dry-run", false, "print the safe request without sending it")
	return c
}

func externalStageRegistrationsRevokeCmd() *cobra.Command {
	var mutation externalStageMutationOptions
	var dryRun bool
	c := &cobra.Command{
		Use:   "revoke <delivery-key> <registration-id>",
		Short: "Revoke one exact reporter registration with mandatory audit",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			deliveryKey, err := validateExternalStageDeliveryKey(args[0])
			if err != nil {
				return err
			}
			registrationID, err := positiveExternalStageID("registration-id", args[1])
			if err != nil {
				return err
			}
			if err := mutation.validate(); err != nil {
				return err
			}
			path := externalStageDeliveryAdminPath(externalStageRegistrationRevokePath, deliveryKey)
			path = strings.Replace(path, "{registrationID}", strconv.FormatInt(registrationID, 10), 1)
			request := struct{}{}
			if dryRun {
				return emitJSON(externalStageDryRunPlan(http.MethodPost, path, request, mutation))
			}
			client, err := instanceClient()
			if err != nil {
				return err
			}
			var response externalStageRegistration
			if err := externalStageAdminJSONRoundTrip(client, http.MethodPost, path, request, mutation.idempotencyKey, &response); err != nil {
				return reportError(err)
			}
			return emitExternalStageResult(response,
				fmt.Sprintf("revoked external reporter registration %d for %s", response.RegistrationID, deliveryKey))
		},
	}
	addExternalStageReusableIdempotencyFlag(c, &mutation)
	c.Flags().BoolVar(&dryRun, "dry-run", false, "print the safe request without sending it")
	return c
}

func externalStagePrerequisitesCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "prerequisites",
		Short: "Seal exact external dependency prerequisites",
	}
	c.AddCommand(externalStagePrerequisitesSealCmd())
	return c
}

func externalStagePrerequisitesSealCmd() *cobra.Command {
	var request externalStagePrerequisiteSetRequest
	var rawPrerequisites []string
	var mutation externalStageMutationOptions
	var dryRun bool
	c := &cobra.Command{
		Use:   "seal <delivery-key>",
		Short: "Seal 0–16 explicit required or optional Janus bindings",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			deliveryKey, err := validateExternalStageDeliveryKey(args[0])
			if err != nil {
				return err
			}
			request.Prerequisites, err = parseExternalStagePrerequisites(rawPrerequisites)
			if err != nil {
				return err
			}
			if err := validateExternalStagePrerequisiteSet(request); err != nil {
				return err
			}
			if err := mutation.validate(); err != nil {
				return err
			}
			path := externalStageDeliveryAdminPath(externalStagePrerequisiteSetsPath, deliveryKey)
			if dryRun {
				return emitJSON(externalStageDryRunPlan(http.MethodPost, path, request, mutation))
			}
			client, err := instanceClient()
			if err != nil {
				return err
			}
			var response externalStagePrerequisiteSet
			if err := externalStageAdminJSONRoundTrip(client, http.MethodPost, path, request, mutation.idempotencyKey, &response); err != nil {
				return reportError(err)
			}
			return emitExternalStageResult(response,
				fmt.Sprintf("sealed %d external prerequisites for %s", response.DeclaredCount, deliveryKey))
		},
	}
	c.Flags().StringVar(&request.StageKey, "stage", "", "bound delivery stage key")
	c.Flags().Int64Var(&request.ExecutionNumber, "execution", 0, "exact current stage execution number")
	c.Flags().Int64Var(&request.ExpectedPlanRevision, "plan-revision", 0, "expected immutable attempt plan revision")
	c.Flags().Int64Var(&request.ExpectedAuthorityEpoch, "authority-epoch", 0, "expected current delivery authority epoch")
	c.Flags().StringArrayVar(&rawPrerequisites, "prerequisite", nil, "required|optional:dependency=registration-id binding (repeat 0–16 times)")
	addExternalStageReusableIdempotencyFlag(c, &mutation)
	c.Flags().BoolVar(&dryRun, "dry-run", false, "print the safe request without sending it")
	return c
}

func externalStageMintCmd(rotate bool) *cobra.Command {
	var expectedEpoch int64
	var outputPath string
	var dryRun bool
	verb := "mint"
	pastTense := "minted"
	pathTemplate := externalstage.InternalMintPath
	short := "Mint the first credential into a new owner-only file"
	if rotate {
		verb = "rotate"
		pastTense = "rotated"
		pathTemplate = externalstage.InternalRotatePath
		short = "Rotate the credential into a new owner-only file"
	}
	c := &cobra.Command{
		Use:   verb + " <handoff-id>",
		Short: short,
		Long: short + `. The destination must not exist. PAIMOS reserves it with
mode 0600 before sending the request, streams exactly 32 response bytes, then
fsyncs and closes it. Raw credential bytes are never written to CLI output.
If a response is lost or file finalization fails after the request was sent,
the credential must be rotated before use.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			handoffID, err := validateExternalStageHandoffID(args[0])
			if err != nil {
				return err
			}
			if strings.TrimSpace(outputPath) == "" {
				return &usageError{msg: "--secret-output is required; raw credential output is file-only"}
			}
			if expectedEpoch < 0 || (rotate && expectedEpoch < 1) {
				minimum := "zero or greater"
				if rotate {
					minimum = "a positive integer"
				}
				return &usageError{msg: "--expected-credential-epoch must be " + minimum}
			}
			path := strings.Replace(pathTemplate, "{handoffID}", handoffID, 1)
			request := externalstage.CredentialEpochRequest{ExpectedCredentialEpoch: expectedEpoch}
			if dryRun {
				if _, err := os.Lstat(outputPath); err == nil {
					return fmt.Errorf("secret output already exists")
				} else if !os.IsNotExist(err) {
					return fmt.Errorf("check secret output: %w", err)
				}
				return emitJSON(map[string]any{
					"method": http.MethodPost, "path": path, "body": request,
					"secret_output": outputPath, "secret_output_mode": "0600",
				})
			}
			client, err := instanceClient()
			if err != nil {
				return err
			}
			if err := externalStageStreamSecret(client, path, request, outputPath); err != nil {
				return reportError(err)
			}
			if flagJSON {
				return emitJSON(map[string]any{
					"handoff_id": handoffID, "operation": verb, "secret_output": outputPath,
					"secret_output_mode": "0600",
				})
			}
			fmt.Fprintf(stdout, "%s external-stage credential for %s into %s (0600)\n", pastTense, handoffID, outputPath)
			return nil
		},
	}
	c.Flags().Int64Var(&expectedEpoch, "expected-credential-epoch", 0, "current credential epoch (mint: 0; rotate: positive)")
	c.Flags().StringVar(&outputPath, "secret-output", "", "new file that receives exactly 32 raw credential bytes (required)")
	c.Flags().BoolVar(&dryRun, "dry-run", false, "print the safe request and output-file plan without sending it")
	return c
}

func externalStageRevokeCmd() *cobra.Command {
	var expectedEpoch int64
	var mutation externalStageMutationOptions
	var dryRun bool
	c := &cobra.Command{
		Use:   "revoke <handoff-id>",
		Short: "Terminally revoke a handoff",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			handoffID, err := validateExternalStageHandoffID(args[0])
			if err != nil {
				return err
			}
			if expectedEpoch < 1 {
				return &usageError{msg: "--expected-credential-epoch must be a positive integer"}
			}
			if err := mutation.validate(); err != nil {
				return err
			}
			path := strings.Replace(externalstage.InternalRevokePath, "{handoffID}", handoffID, 1)
			request := externalstage.RevokeHandoffRequest{ExpectedCredentialEpoch: expectedEpoch}
			if dryRun {
				return emitJSON(externalStageDryRunPlan(http.MethodPost, path, request, mutation))
			}
			client, err := instanceClient()
			if err != nil {
				return err
			}
			var metadata externalstage.HandoffMetadata
			if err := externalStageJSONRoundTrip(client, http.MethodPost, path, request, nil, mutation.idempotencyKey, &metadata); err != nil {
				return reportError(err)
			}
			return emitExternalStageResult(metadata, fmt.Sprintf("revoked external-stage handoff %s", metadata.HandoffID))
		},
	}
	c.Flags().Int64Var(&expectedEpoch, "expected-credential-epoch", 0, "current credential epoch")
	addExternalStageReusableIdempotencyFlag(c, &mutation)
	c.Flags().BoolVar(&dryRun, "dry-run", false, "print the safe request without sending it")
	return c
}

func externalStagePullCmd() *cobra.Command {
	var secret externalStageSecretInput
	c := &cobra.Command{
		Use:   "pull <handoff-id>",
		Short: "Pull the safe external projection with two credentials",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			handoffID, err := validateExternalStageHandoffID(args[0])
			if err != nil {
				return err
			}
			rawSecret, err := readExternalStageSecret(secret)
			if err != nil {
				return err
			}
			defer clearExternalStageSecret(rawSecret)
			client, err := instanceClient()
			if err != nil {
				return err
			}
			path := strings.Replace(externalstage.ExternalPullPath, "{handoffID}", handoffID, 1)
			var response externalstage.PullResponse
			if err := externalStageJSONRoundTrip(client, http.MethodGet, path, nil, rawSecret, "", &response); err != nil {
				return reportError(err)
			}
			if err := validateExternalStagePullResponse(handoffID, response, rawSecret); err != nil {
				return reportError(err)
			}
			return emitExternalStageResult(response,
				fmt.Sprintf("pulled external-stage handoff %s (%s)", response.HandoffID, response.State))
		},
	}
	addExternalStageSecretInputFlags(c, &secret)
	return c
}

func externalStageAcceptCmd() *cobra.Command {
	var secret externalStageSecretInput
	var mutation externalStageMutationOptions
	var observedAt string
	c := &cobra.Command{
		Use:   "accept <handoff-id>",
		Short: "Accept the issued handoff as sequence one",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			handoffID, err := validateExternalStageHandoffID(args[0])
			if err != nil {
				return err
			}
			if err := mutation.validate(); err != nil {
				return err
			}
			observedAt, err = externalStageTimestamp("--observed-at", observedAt)
			if err != nil {
				return err
			}
			rawSecret, err := readExternalStageSecret(secret)
			if err != nil {
				return err
			}
			defer clearExternalStageSecret(rawSecret)
			client, err := instanceClient()
			if err != nil {
				return err
			}
			path := strings.Replace(externalstage.ExternalAcceptPath, "{handoffID}", handoffID, 1)
			request := externalstage.AcceptRequest{Sequence: 1, ObservedAt: observedAt}
			var receipt externalstage.ReportReceipt
			if err := externalStageJSONRoundTrip(client, http.MethodPost, path, request, rawSecret, mutation.idempotencyKey, &receipt); err != nil {
				return reportError(err)
			}
			if err := validateExternalStageReportReceipt(handoffID, request.Sequence, externalstage.HandoffStateAccepted, receipt, rawSecret); err != nil {
				return reportError(err)
			}
			return emitExternalStageResult(receipt,
				fmt.Sprintf("accepted external-stage handoff %s at sequence %d", receipt.HandoffID, receipt.Sequence))
		},
	}
	c.Flags().StringVar(&observedAt, "observed-at", "", "reporter observation time in RFC3339 form (default now)")
	addExternalStageReusableIdempotencyFlag(c, &mutation)
	addExternalStageSecretInputFlags(c, &secret)
	return c
}

func externalStageReportCmd() *cobra.Command {
	var secret externalStageSecretInput
	var mutation externalStageMutationOptions
	var reportFile string
	c := &cobra.Command{
		Use:   "report <handoff-id>",
		Short: "Append one strict exact-next external-stage report",
		Long: `Append one report from a JSON file. The file must contain exactly one
ExternalStageReportRequest value and unknown fields fail locally. Use
--report-file - only when the credential comes from --secret-file; one stdin
stream cannot carry both the report and the independent credential.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			handoffID, err := validateExternalStageHandoffID(args[0])
			if err != nil {
				return err
			}
			if err := mutation.validate(); err != nil {
				return err
			}
			if strings.TrimSpace(reportFile) == "" {
				return &usageError{msg: "--report-file is required"}
			}
			if reportFile == "-" && secret.stdin {
				return &usageError{msg: "--report-file - cannot be combined with --secret-stdin"}
			}
			request, err := readExternalStageReport(reportFile)
			if err != nil {
				return err
			}
			rawSecret, err := readExternalStageSecret(secret)
			if err != nil {
				return err
			}
			defer clearExternalStageSecret(rawSecret)
			client, err := instanceClient()
			if err != nil {
				return err
			}
			path := strings.Replace(externalstage.ExternalReportPath, "{handoffID}", handoffID, 1)
			var receipt externalstage.ReportReceipt
			if err := externalStageJSONRoundTrip(client, http.MethodPost, path, request, rawSecret, mutation.idempotencyKey, &receipt); err != nil {
				return reportError(err)
			}
			if err := validateExternalStageReportReceipt(handoffID, request.Sequence, request.State, receipt, rawSecret); err != nil {
				return reportError(err)
			}
			return emitExternalStageResult(receipt,
				fmt.Sprintf("reported external-stage handoff %s at sequence %d (%s)", receipt.HandoffID, receipt.Sequence, receipt.State))
		},
	}
	c.Flags().StringVar(&reportFile, "report-file", "", "strict ExternalStageReportRequest JSON file, or - for stdin")
	addExternalStageReusableIdempotencyFlag(c, &mutation)
	addExternalStageSecretInputFlags(c, &secret)
	return c
}

func addExternalStageSecretInputFlags(c *cobra.Command, input *externalStageSecretInput) {
	c.Flags().StringVar(&input.file, "secret-file", "", "owner-only file containing exactly 32 raw credential bytes")
	c.Flags().BoolVar(&input.stdin, "secret-stdin", false, "read exactly 32 raw credential bytes from stdin")
}

func addExternalStageReusableIdempotencyFlag(c *cobra.Command, options *externalStageMutationOptions) {
	c.Flags().StringVar(&options.idempotencyKey, "idempotency-key", "",
		"canonical lowercase UUID or uppercase ULID to reuse only for an exact retry (never printed)")
}

func (options externalStageMutationOptions) validate() error {
	if options.idempotencyKey == "" {
		return nil
	}
	if !externalStageUUIDPattern.MatchString(options.idempotencyKey) &&
		!(externalStageHandoffIDPattern.MatchString(options.idempotencyKey) && options.idempotencyKey[0] <= '7') {
		return &usageError{msg: "--idempotency-key must be a canonical lowercase UUID or uppercase ULID"}
	}
	return nil
}

func externalStageDryRunPlan(method, path string, body any, options externalStageMutationOptions) map[string]any {
	source := "automatic"
	if options.idempotencyKey != "" {
		source = "operator-supplied"
	}
	return map[string]any{
		"method": method, "path": path, "body": body, "idempotency_key_source": source,
	}
}

func readExternalStageSecret(input externalStageSecretInput) ([]byte, error) {
	if (strings.TrimSpace(input.file) == "") == !input.stdin {
		return nil, &usageError{msg: "exactly one of --secret-file or --secret-stdin is required"}
	}
	var reader io.Reader
	var file *os.File
	if input.stdin {
		reader = os.Stdin
	} else {
		var err error
		file, err = openExternalStageSecretFile(input.file)
		if err != nil {
			return nil, err
		}
		defer file.Close()
		reader = file
	}
	raw, err := readExternalStageSecretBytes(reader)
	if err != nil {
		return nil, err
	}
	if len(raw) != externalstage.OneTimeSecretBytes {
		rejectExternalStageSecretBytes(raw)
		return nil, fmt.Errorf("handoff credential must contain exactly %d raw bytes", externalstage.OneTimeSecretBytes)
	}
	return raw, nil
}

func readExternalStageSecretBytes(reader io.Reader) ([]byte, error) {
	raw, err := io.ReadAll(io.LimitReader(reader, externalstage.OneTimeSecretBytes+1))
	if err != nil {
		clearExternalStageSecret(raw)
		return nil, errors.New("read handoff credential: failed")
	}
	return raw, nil
}

func clearExternalStageSecret(raw []byte) {
	for index := range raw {
		raw[index] = 0
	}
}

func rejectExternalStageSecretBytes(raw []byte) {
	clearExternalStageSecret(raw)
}

func validateExternalStageSecretMetadata(mode os.FileMode, ownerUID, currentUID, linkCount uint64) error {
	if !mode.IsRegular() || mode.Perm()&0o077 != 0 || ownerUID != currentUID || linkCount != 1 {
		return errors.New("handoff credential file must be a regular, current-user-owned, single-link owner-only file")
	}
	return nil
}

func readExternalStageReport(path string) (externalstage.ReportRequest, error) {
	var reader io.Reader
	var file *os.File
	if path == "-" {
		reader = os.Stdin
	} else {
		var err error
		file, err = os.Open(path) // #nosec G304 -- explicit non-secret report file chosen by the operator.
		if err != nil {
			return externalstage.ReportRequest{}, fmt.Errorf("open report file: %w", err)
		}
		defer file.Close()
		reader = file
	}
	var report externalstage.ReportRequest
	if err := decodeExternalStageJSON(io.LimitReader(reader, externalStageMaxJSONBytes+1), &report); err != nil {
		return report, fmt.Errorf("decode report file: %w", err)
	}
	if err := validateExternalStageReport(report); err != nil {
		return report, err
	}
	return report, nil
}

func externalStageJSONRoundTrip(client *Client, method, path string, body any, rawSecret []byte, idempotencyKey string, target any) error {
	var requestBody io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("encode external-stage request: %w", err)
		}
		requestBody = bytes.NewReader(raw)
	}
	req, err := http.NewRequest(method, client.baseURL+path, requestBody)
	if err != nil {
		return fmt.Errorf("build external-stage request: %w", err)
	}
	client.prepareRequest(req, body != nil, externalstage.MediaTypeV1, externalstage.MediaTypeV1)
	if idempotencyKey != "" {
		req.Header.Set(idempotencyHeader, idempotencyKey)
	}
	if len(rawSecret) > 0 {
		req.Header.Set(externalstage.HandoffSecretHeader, base64.RawURLEncoding.EncodeToString(rawSecret))
		defer req.Header.Del(externalstage.HandoffSecretHeader)
	}
	resp, err := externalStageHTTPDo(client, req, len(rawSecret) > 0)
	if err != nil {
		if idempotencyKey != "" && strings.Contains(err.Error(), idempotencyKey) {
			return errors.New("external-stage request failed")
		}
		return err
	}
	defer resp.Body.Close()
	if strings.TrimSpace(resp.Header.Get("Content-Type")) != externalstage.MediaTypeV1 {
		return errors.New("external-stage response did not use the pinned v1 JSON media type")
	}
	if err := decodeExternalStageJSON(io.LimitReader(resp.Body, externalStageMaxJSONBytes+1), target); err != nil {
		if len(rawSecret) > 0 {
			return errors.New("external-stage response violated the pinned v1 JSON schema")
		}
		if idempotencyKey != "" && strings.Contains(err.Error(), idempotencyKey) {
			return errors.New("external-stage response violated the pinned v1 JSON schema")
		}
		return fmt.Errorf("decode external-stage response: %w", err)
	}
	if externalStageResponseContainsIdempotencyKey(target, idempotencyKey) {
		return errors.New("external-stage response violated the pinned v1 JSON schema")
	}
	return nil
}

func externalStageAdminJSONRoundTrip(client *Client, method, path string, body any, idempotencyKey string, target any) error {
	var requestBody io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("encode external-stage admin request: %w", err)
		}
		requestBody = bytes.NewReader(raw)
	}
	req, err := http.NewRequest(method, client.baseURL+path, requestBody)
	if err != nil {
		return fmt.Errorf("build external-stage admin request: %w", err)
	}
	client.prepareRequest(req, body != nil, externalStageAdminMediaType, externalStageAdminMediaType)
	if idempotencyKey != "" {
		req.Header.Set(idempotencyHeader, idempotencyKey)
	}
	resp, err := externalStageHTTPDo(client, req, false)
	if err != nil {
		if idempotencyKey != "" && strings.Contains(err.Error(), idempotencyKey) {
			return errors.New("external-stage admin request failed")
		}
		return err
	}
	defer resp.Body.Close()
	if strings.TrimSpace(resp.Header.Get("Content-Type")) != externalStageAdminMediaType {
		return errors.New("external-stage admin response did not use application/json")
	}
	if err := decodeExternalStageJSON(io.LimitReader(resp.Body, externalStageMaxJSONBytes+1), target); err != nil {
		if idempotencyKey != "" && strings.Contains(err.Error(), idempotencyKey) {
			return errors.New("external-stage admin response violated its JSON schema")
		}
		return fmt.Errorf("decode external-stage admin response: %w", err)
	}
	if externalStageResponseContainsIdempotencyKey(target, idempotencyKey) {
		return errors.New("external-stage admin response violated its JSON schema")
	}
	return nil
}

func externalStageStreamSecret(client *Client, path string, body externalstage.CredentialEpochRequest, outputPath string) (err error) {
	output, err := os.OpenFile(outputPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600) // #nosec G304 -- explicit output selected by the operator; O_EXCL prevents overwrite.
	if err != nil {
		return errors.New("create new handoff credential output: failed (destination must not exist)")
	}
	complete := false
	defer func() {
		if !complete {
			_ = output.Close()
			_ = os.Remove(outputPath)
			_ = externalStageSyncDirectory(outputPath)
		}
	}()
	rawBody, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("encode credential-epoch request: %w", err)
	}
	req, err := http.NewRequest(http.MethodPost, client.baseURL+path, bytes.NewReader(rawBody))
	if err != nil {
		return fmt.Errorf("build credential request: %w", err)
	}
	client.prepareRequest(req, true, externalstage.MediaTypeV1, externalstage.SecretMediaTypeV1)
	resp, err := externalStageHTTPDo(client, req, true)
	if err != nil {
		return fmt.Errorf("credential response was not safely captured; rotate before use: %w", err)
	}
	defer resp.Body.Close()
	if strings.TrimSpace(resp.Header.Get("Content-Type")) != externalstage.SecretMediaTypeV1 {
		return errors.New("credential response was not safely captured; rotate before use: response media type mismatch")
	}
	copyBuffer := externalStageNewCopyBuffer()
	defer clearExternalStageSecret(copyBuffer)
	// Suppress os.File.ReadFrom so the only caller-owned raw response buffer is
	// this explicitly cleared slice. The 33-byte limit detects trailing data.
	written, err := io.CopyBuffer(struct{ io.Writer }{output},
		io.LimitReader(resp.Body, externalstage.OneTimeSecretBytes+1), copyBuffer)
	if err != nil || written != externalstage.OneTimeSecretBytes {
		return errors.New("credential response was not safely captured; rotate before use: response was not exactly 32 bytes")
	}
	if err := output.Sync(); err != nil {
		return errors.New("credential output could not be finalized; rotate before use")
	}
	if err := output.Close(); err != nil {
		return errors.New("credential output could not be finalized; rotate before use")
	}
	if err := externalStageSyncDirectory(outputPath); err != nil {
		return errors.New("credential output directory could not be finalized; rotate before use")
	}
	complete = true
	return nil
}

func externalStageHTTPDo(client *Client, req *http.Request, concealResponseBody bool) (*http.Response, error) {
	httpClient := *client.http
	httpClient.CheckRedirect = func(_ *http.Request, _ []*http.Request) error {
		return http.ErrUseLastResponse
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, errors.New("external-stage HTTP request failed")
	}
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return resp, nil
	}
	defer resp.Body.Close()
	if concealResponseBody {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, externalStageMaxJSONBytes))
		return nil, &httpError{
			Code: resp.StatusCode, Method: req.Method, Path: "/api/external-stage/handoffs/<redacted>",
			Body: []byte(`{"error":"external-stage request rejected"}`),
		}
	}
	raw, readErr := io.ReadAll(io.LimitReader(resp.Body, externalStageMaxJSONBytes+1))
	if readErr != nil {
		return nil, fmt.Errorf("read external-stage error response: %w", readErr)
	}
	return nil, &httpError{Code: resp.StatusCode, Method: req.Method, Path: req.URL.Path, Body: raw}
}

func decodeExternalStageJSON(reader io.Reader, target any) error {
	decoder := json.NewDecoder(reader)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("expected exactly one JSON value")
		}
		return err
	}
	return nil
}

func validateExternalStagePullResponse(expectedHandoffID string, response externalstage.PullResponse, rawSecret []byte) error {
	stringsToCheck := []string{
		response.HandoffID, response.FixtureDigest, response.ExpiresAt, string(response.State),
		string(response.ReporterClass), string(response.ReporterRole), response.DependencyKey,
		response.StageKey, response.PlanDigest, response.PredecessorDigest, response.ContextDigest,
	}
	for _, kind := range response.EvidenceCeiling {
		stringsToCheck = append(stringsToCheck, string(kind))
	}
	if externalStageResponseReflectsSecret(rawSecret, stringsToCheck...) {
		return errors.New("external-stage response violated the pinned v1 JSON schema")
	}
	if response.HandoffID != expectedHandoffID || response.ContractMajor != externalstage.ContractMajor ||
		response.FixtureDigest != "sha256:"+contracts.ExternalStageV1FixtureDigestHex || response.CredentialEpoch < 1 ||
		response.ExecutionNumber < 1 || response.AuthorityEpoch < 1 || !validExternalStageState(response.State) ||
		!validExternalStageStage(response.StageKey) || !externalStageDigestPattern.MatchString(response.PlanDigest) ||
		!externalStageDigestPattern.MatchString(response.PredecessorDigest) || !externalStageDigestPattern.MatchString(response.ContextDigest) {
		return errors.New("external-stage response violated the pinned v1 JSON schema")
	}
	if _, err := time.Parse(time.RFC3339Nano, response.ExpiresAt); err != nil {
		return errors.New("external-stage response violated the pinned v1 JSON schema")
	}
	if !validExternalStageReporterProjection(response) {
		return errors.New("external-stage response violated the pinned v1 JSON schema")
	}
	return nil
}

func validExternalStageReporterProjection(response externalstage.PullResponse) bool {
	if len(response.EvidenceCeiling) < 1 || len(response.EvidenceCeiling) > 2 {
		return false
	}
	seen := make(map[externalstage.EvidenceKind]bool, len(response.EvidenceCeiling))
	for _, kind := range response.EvidenceCeiling {
		if seen[kind] {
			return false
		}
		seen[kind] = true
		switch response.ReporterClass {
		case externalstage.ReporterClassPharos:
			if kind != externalstage.EvidenceKindDeployment && kind != externalstage.EvidenceKindVerification {
				return false
			}
		case externalstage.ReporterClassJanus:
			if kind != externalstage.EvidenceKindAuthorization && kind != externalstage.EvidenceKindCredentialHandoff {
				return false
			}
		default:
			return false
		}
	}
	switch response.ReporterClass {
	case externalstage.ReporterClassPharos:
		return response.ReporterRole == externalstage.ReporterRoleOwner && response.DependencyKey == ""
	case externalstage.ReporterClassJanus:
		return response.ReporterRole == externalstage.ReporterRoleDependency &&
			externalStageSymbolPattern.MatchString(response.DependencyKey)
	default:
		return false
	}
}

func validateExternalStageReportReceipt(expectedHandoffID string, expectedSequence int64,
	expectedState externalstage.HandoffState, receipt externalstage.ReportReceipt, rawSecret []byte,
) error {
	if externalStageResponseReflectsSecret(rawSecret, receipt.HandoffID, string(receipt.State), receipt.ServerReceivedAt) ||
		receipt.HandoffID != expectedHandoffID || receipt.Sequence != expectedSequence || receipt.Sequence < 1 ||
		receipt.State != expectedState || !validExternalStageState(receipt.State) || receipt.CredentialEpoch < 1 {
		return errors.New("external-stage response violated the pinned v1 JSON schema")
	}
	if _, err := time.Parse(time.RFC3339Nano, receipt.ServerReceivedAt); err != nil {
		return errors.New("external-stage response violated the pinned v1 JSON schema")
	}
	return nil
}

func externalStageResponseReflectsSecret(rawSecret []byte, values ...string) bool {
	if len(rawSecret) == 0 {
		return false
	}
	candidates := []string{
		string(rawSecret),
		base64.RawURLEncoding.EncodeToString(rawSecret),
		base64.URLEncoding.EncodeToString(rawSecret),
	}
	for _, value := range values {
		for _, candidate := range candidates {
			if candidate != "" && strings.Contains(value, candidate) {
				return true
			}
		}
	}
	return false
}

func externalStageResponseContainsIdempotencyKey(value any, idempotencyKey string) bool {
	if idempotencyKey == "" {
		return false
	}
	raw, err := json.Marshal(value)
	return err == nil && bytes.Contains(raw, []byte(idempotencyKey))
}

func validExternalStageState(state externalstage.HandoffState) bool {
	switch state {
	case externalstage.HandoffStateIssued, externalstage.HandoffStateAccepted, externalstage.HandoffStateActive,
		externalstage.HandoffStateWaiting, externalstage.HandoffStateBlocked,
		externalstage.HandoffStateSucceeded, externalstage.HandoffStateFailed:
		return true
	default:
		return false
	}
}

func validateExternalStageDeliveryKey(raw string) (string, error) {
	value := strings.TrimSpace(raw)
	if !externalStageDeliveryPattern.MatchString(value) {
		return "", &usageError{msg: "delivery-key does not match the pinned v1 path contract"}
	}
	return value, nil
}

func externalStageDeliveryAdminPath(template, deliveryKey string) string {
	return strings.Replace(template, "{deliveryKey}", url.PathEscape(deliveryKey), 1)
}

func positiveExternalStageID(name, raw string) (int64, error) {
	if raw == "" || raw[0] == '0' {
		return 0, &usageError{msg: name + " must be a positive decimal integer"}
	}
	for _, character := range raw {
		if character < '0' || character > '9' {
			return 0, &usageError{msg: name + " must be a positive decimal integer"}
		}
	}
	value, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || value < 1 {
		return 0, &usageError{msg: name + " must be a positive decimal integer"}
	}
	return value, nil
}

func validateExternalStageRegistration(request externalStageRegistrationRequest) error {
	if request.APIKeyID < 1 {
		return &usageError{msg: "--api-key-id must be positive"}
	}
	switch externalstage.ReporterClass(request.ReporterClass) {
	case externalstage.ReporterClassPharos:
		if externalstage.ReporterRole(request.ReporterRole) != externalstage.ReporterRoleOwner || request.DependencyKey != "" ||
			!externalStageSymbolPattern.MatchString(request.Workflow) || !externalStageSymbolPattern.MatchString(request.Environment) {
			return &usageError{msg: "Pharos requires role owner, exact workflow/environment symbols, and no dependency"}
		}
	case externalstage.ReporterClassJanus:
		if externalstage.ReporterRole(request.ReporterRole) != externalstage.ReporterRoleDependency ||
			!externalStageSymbolPattern.MatchString(request.DependencyKey) || request.Workflow != "" || request.Environment != "" {
			return &usageError{msg: "Janus requires role dependency, one exact dependency symbol, and no workflow/environment"}
		}
	default:
		return &usageError{msg: "--class must be pharos or janus"}
	}
	return nil
}

func parseExternalStagePrerequisites(raw []string) ([]externalStagePrerequisite, error) {
	if len(raw) > 16 {
		return nil, &usageError{msg: "--prerequisite may be repeated at most 16 times"}
	}
	result := make([]externalStagePrerequisite, 0, len(raw))
	seenDependencies := make(map[string]bool, len(raw))
	seenRegistrations := make(map[int64]bool, len(raw))
	for _, binding := range raw {
		requirement, dependencyBinding, ok := strings.Cut(binding, ":")
		if !ok || (requirement != "required" && requirement != "optional") {
			return nil, &usageError{msg: "each --prerequisite must explicitly be required|optional:dependency-symbol=positive-registration-id"}
		}
		dependencyKey, registrationText, ok := strings.Cut(dependencyBinding, "=")
		if !ok || !externalStageSymbolPattern.MatchString(dependencyKey) {
			return nil, &usageError{msg: "each --prerequisite must explicitly be required|optional:dependency-symbol=positive-registration-id"}
		}
		registrationID, err := positiveExternalStageID("prerequisite registration-id", registrationText)
		if err != nil {
			return nil, err
		}
		if seenDependencies[dependencyKey] || seenRegistrations[registrationID] {
			return nil, &usageError{msg: "prerequisite dependency keys and registration ids must each be unique"}
		}
		seenDependencies[dependencyKey] = true
		seenRegistrations[registrationID] = true
		result = append(result, externalStagePrerequisite{
			DependencyKey: dependencyKey, ReporterRegistrationID: registrationID, Requirement: requirement,
		})
	}
	return result, nil
}

func validateExternalStagePrerequisiteSet(request externalStagePrerequisiteSetRequest) error {
	if !validExternalStageStage(request.StageKey) {
		return &usageError{msg: "--stage must be specification, implementation, qa, deployment, or verification"}
	}
	if request.ExecutionNumber < 1 || request.ExpectedPlanRevision < 1 || request.ExpectedAuthorityEpoch < 1 {
		return &usageError{msg: "--execution, --plan-revision, and --authority-epoch must be positive"}
	}
	if len(request.Prerequisites) > 16 {
		return &usageError{msg: "prerequisites must contain 0–16 exact bindings"}
	}
	for _, prerequisite := range request.Prerequisites {
		if prerequisite.Requirement != "required" && prerequisite.Requirement != "optional" {
			return &usageError{msg: "prerequisite requirement must be required or optional"}
		}
	}
	return nil
}

func validExternalStageStage(stage string) bool {
	return map[string]bool{
		"specification": true, "implementation": true, "qa": true, "deployment": true, "verification": true,
	}[stage]
}

func validateExternalStageHandoffID(raw string) (string, error) {
	value := strings.TrimSpace(raw)
	if !externalStageHandoffIDPattern.MatchString(value) {
		return "", &usageError{msg: "handoff-id must be a 26-character uppercase canonical ULID"}
	}
	return value, nil
}

func validateExternalStageCreate(request externalstage.CreateHandoffRequest) error {
	if !validExternalStageStage(request.StageKey) {
		return &usageError{msg: "--stage must be specification, implementation, qa, deployment, or verification"}
	}
	if request.ExecutionNumber < 1 || request.ExpectedPlanRevision < 1 || request.ExpectedAuthorityEpoch < 1 || request.ReporterRegistrationID < 1 {
		return &usageError{msg: "--execution, --plan-revision, --authority-epoch, and --reporter-registration-id must be positive"}
	}
	if _, err := time.Parse(time.RFC3339Nano, request.ExpiresAt); err != nil {
		return &usageError{msg: "--expires-at must be RFC3339"}
	}
	return nil
}

func validateExternalStageReport(report externalstage.ReportRequest) error {
	if report.Sequence < 2 {
		return &usageError{msg: "report sequence must be at least 2"}
	}
	validState := map[externalstage.HandoffState]bool{
		externalstage.HandoffStateActive: true, externalstage.HandoffStateWaiting: true,
		externalstage.HandoffStateBlocked: true, externalstage.HandoffStateSucceeded: true,
		externalstage.HandoffStateFailed: true,
	}
	if !validState[report.State] {
		return &usageError{msg: "report state is outside the pinned v1 report lifecycle"}
	}
	if _, err := time.Parse(time.RFC3339Nano, report.ObservedAt); err != nil {
		return &usageError{msg: "report observed_at must be RFC3339"}
	}
	if len(report.BlockerCodes) > 8 {
		return &usageError{msg: "report blocker_codes must contain at most 8 values"}
	}
	validBlocker := map[externalstage.BlockerCode]bool{
		externalstage.BlockerDependencyPending: true, externalstage.BlockerDependencyFailed: true,
		externalstage.BlockerReporterStale: true, externalstage.BlockerExternalWaiting: true,
	}
	seenBlocker := make(map[externalstage.BlockerCode]bool, len(report.BlockerCodes))
	for _, blocker := range report.BlockerCodes {
		if !validBlocker[blocker] || seenBlocker[blocker] {
			return &usageError{msg: "report blocker_codes must be unique pinned v1 values"}
		}
		seenBlocker[blocker] = true
	}
	if report.PharosEvidence != nil && report.JanusEvidence != nil {
		return &usageError{msg: "a report cannot contain both pharos_evidence and janus_evidence"}
	}
	if report.Heartbeat {
		if len(report.BlockerCodes) != 0 || report.PharosEvidence != nil || report.JanusEvidence != nil ||
			report.State == externalstage.HandoffStateSucceeded || report.State == externalstage.HandoffStateFailed {
			return &usageError{msg: "heartbeat reports cannot carry blockers, evidence, or terminal state"}
		}
		return nil
	}
	if report.PharosEvidence != nil {
		if err := validateExternalStagePharosEvidence(report, *report.PharosEvidence); err != nil {
			return &usageError{msg: err.Error()}
		}
	}
	if report.JanusEvidence != nil {
		if err := validateExternalStageJanusEvidence(report, *report.JanusEvidence); err != nil {
			return &usageError{msg: err.Error()}
		}
	}
	return nil
}

func validateExternalStagePharosEvidence(report externalstage.ReportRequest, evidence externalstage.PharosEvidence) error {
	if evidence.Kind != externalstage.EvidenceKindDeployment && evidence.Kind != externalstage.EvidenceKindVerification {
		return errors.New("pharos evidence kind must be deployment or verification")
	}
	if evidence.Result != externalstage.EvidenceResultSucceeded && evidence.Result != externalstage.EvidenceResultFailed {
		return errors.New("pharos evidence result must be succeeded or failed")
	}
	if !externalStageSymbolPattern.MatchString(evidence.Workflow) || !externalStageSymbolPattern.MatchString(evidence.Environment) {
		return errors.New("pharos workflow and environment must be symbolic pinned v1 names")
	}
	if !externalStageVersionPattern.MatchString(evidence.Artifact.Version) ||
		!externalStageDigestPattern.MatchString(evidence.Artifact.Digest) ||
		!externalStageCommitPattern.MatchString(evidence.Artifact.CommitDigest) {
		return errors.New("pharos artifact version, sha256 digest, or 40/64-hex commit digest is invalid")
	}
	if _, err := time.Parse(time.RFC3339Nano, evidence.ObservedAt); err != nil || evidence.ObservedAt != report.ObservedAt {
		return errors.New("pharos evidence observed_at must be RFC3339 and equal report observed_at")
	}
	wantState := externalstage.HandoffStateSucceeded
	if evidence.Result == externalstage.EvidenceResultFailed {
		wantState = externalstage.HandoffStateFailed
	}
	if report.State != wantState {
		return errors.New("pharos evidence result is incompatible with report state")
	}
	return nil
}

func validateExternalStageJanusEvidence(report externalstage.ReportRequest, evidence externalstage.JanusEvidence) error {
	if evidence.Kind != externalstage.EvidenceKindAuthorization && evidence.Kind != externalstage.EvidenceKindCredentialHandoff {
		return errors.New("janus evidence kind must be authorization or credential_handoff")
	}
	if evidence.Result != externalstage.EvidenceResultSatisfied && evidence.Result != externalstage.EvidenceResultBlocked {
		return errors.New("janus evidence result must be satisfied or blocked")
	}
	var fact *bool
	if evidence.Kind == externalstage.EvidenceKindAuthorization {
		if evidence.Authorized == nil || evidence.CredentialReady != nil {
			return errors.New("janus authorization evidence requires only authorized")
		}
		fact = evidence.Authorized
	} else {
		if evidence.CredentialReady == nil || evidence.Authorized != nil {
			return errors.New("janus credential_handoff evidence requires only credential_ready")
		}
		fact = evidence.CredentialReady
	}
	wantFact := evidence.Result == externalstage.EvidenceResultSatisfied
	if *fact != wantFact {
		return errors.New("janus evidence result is incompatible with its boolean fact")
	}
	if _, err := time.Parse(time.RFC3339Nano, evidence.ObservedAt); err != nil || evidence.ObservedAt != report.ObservedAt {
		return errors.New("janus evidence observed_at must be RFC3339 and equal report observed_at")
	}
	wantState := externalstage.HandoffStateSucceeded
	if evidence.Result == externalstage.EvidenceResultBlocked {
		wantState = externalstage.HandoffStateBlocked
	}
	if report.State != wantState {
		return errors.New("janus evidence result is incompatible with report state")
	}
	return nil
}

func externalStageTimestamp(flagName, raw string) (string, error) {
	value := strings.TrimSpace(raw)
	if value == "" {
		return time.Now().UTC().Format(time.RFC3339Nano), nil
	}
	if _, err := time.Parse(time.RFC3339Nano, value); err != nil {
		return "", &usageError{msg: flagName + " must be RFC3339"}
	}
	return value, nil
}

func emitExternalStageResult(value any, message string) error {
	if flagJSON {
		return emitJSON(value)
	}
	fmt.Fprintln(stdout, message)
	return nil
}
