// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"

	"github.com/inspr-at/paimos/backend/baselinebatch"
	"github.com/spf13/cobra"
)

func baselineBatchCmd() *cobra.Command {
	c := commandGroup(&cobra.Command{
		Use:   "baseline-batch",
		Short: "Report typed baseline-batch built evidence",
		Long: `Report the typed built artifact and scoped QA for a started baseline batch.

This records implementation and QA evidence. It does not start deployment or
mint handoff secrets.`,
	})
	c.AddCommand(baselineBatchReportBuiltCmd())
	return c
}

func baselineBatchReportBuiltCmd() *cobra.Command {
	var (
		projectRef         string
		batchIDRaw         string
		receiptFile        string
		idempotencyKey     string
		expectedAttempt    int64
		expectedPlan       int64
		expectedImplExec   int64
		expectedImplEpoch  int64
		expectedAccountKey string
		expectedRuntimeGen string
		commit             string
		ociConfigDigest    string
		releaseManifest    string
		releaseCoordinate  string
		ociIndexDigest     string
		scheme             string
		channel            string
		sequence           int64
		version            string
		qaDigest           string
		dryRun             bool
	)
	c := &cobra.Command{
		Use:   "report-built",
		Short: "Record typed built-artifact and scoped QA evidence",
		Long: `POST a typed built receipt for one baseline batch.

Supply either --receipt-file (or "-" for stdin) or the explicit identity and
CAS flags. Unknown and duplicate JSON fields fail before any network call. No secret
arguments or outputs.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) != 0 {
				return &usageError{msg: "report-built takes no positional arguments"}
			}
			if strings.TrimSpace(projectRef) == "" {
				return &usageError{msg: "--project is required"}
			}
			batchID, err := parsePositiveInt64Flag("batch-id", batchIDRaw)
			if err != nil {
				return err
			}
			req, err := loadBuiltReceiptRequest(cmd, receiptFile, baselinebatch.BuiltReceiptRequest{
				IdempotencyKey:                       strings.TrimSpace(idempotencyKey),
				ExpectedAttemptID:                    expectedAttempt,
				ExpectedPlanRevision:                 expectedPlan,
				ExpectedImplementationExecution:      expectedImplExec,
				ExpectedImplementationAuthorityEpoch: expectedImplEpoch,
				ExpectedAccountKey:                   strings.TrimSpace(expectedAccountKey),
				ExpectedRuntimeGeneration:            strings.TrimSpace(expectedRuntimeGen),
				Commit:                               strings.TrimSpace(commit),
				OCIConfigDigest:                      strings.TrimSpace(ociConfigDigest),
				ReleaseManifestDigest:                strings.TrimSpace(releaseManifest),
				ReleaseManifestCoordinate:            strings.TrimSpace(releaseCoordinate),
				OCIIndexDigest:                       strings.TrimSpace(ociIndexDigest),
				VersionScheme:                        strings.TrimSpace(scheme),
				ReleaseChannel:                       strings.TrimSpace(channel),
				ReleaseSequence:                      sequence,
				Version:                              strings.TrimSpace(version),
				QADigest:                             strings.TrimSpace(qaDigest),
			})
			if err != nil {
				return err
			}
			if err := baselinebatch.ValidateBuiltReceipt(req); err != nil {
				return &usageError{msg: err.Error()}
			}
			path := fmt.Sprintf("/api/projects/%s/baseline-batches/batches/%d/built-receipt", strings.TrimSpace(projectRef), batchID)
			if dryRun {
				return emitJSON(map[string]any{"method": http.MethodPost, "path": path, "body": req})
			}
			client, err := instanceClient()
			if err != nil {
				return err
			}
			projectID, err := resolveProjectRefToID(client, projectRef)
			if err != nil {
				return reportError(err)
			}
			body, err := client.do(http.MethodPost, fmt.Sprintf("/api/projects/%d/baseline-batches/batches/%d/built-receipt", projectID, batchID), req)
			if err != nil {
				return reportError(err)
			}
			if flagJSON {
				fmt.Fprintln(stdout, string(body))
				return nil
			}
			var batch baselinebatch.Batch
			if err := json.Unmarshal(body, &batch); err != nil {
				return fmt.Errorf("decode built receipt: %w", err)
			}
			fmt.Fprintf(stdout, "batch %d recorded built receipt (next %s)\n", batch.ID, batch.Progress.NextAction)
			return nil
		},
	}
	c.Flags().StringVar(&projectRef, "project", "", "project key or id")
	c.Flags().StringVar(&batchIDRaw, "batch-id", "", "baseline batch id")
	c.Flags().StringVar(&receiptFile, "receipt-file", "", "JSON receipt file, or - for stdin")
	c.Flags().StringVar(&idempotencyKey, "idempotency-key", "", "idempotency key")
	c.Flags().Int64Var(&expectedAttempt, "expected-attempt-id", 0, "expected delivery attempt number")
	c.Flags().Int64Var(&expectedPlan, "expected-plan-revision", 0, "expected plan revision")
	c.Flags().Int64Var(&expectedImplExec, "expected-implementation-execution", 0, "expected implementation execution (0 if none)")
	c.Flags().Int64Var(&expectedImplEpoch, "expected-implementation-authority-epoch", 0, "expected implementation authority epoch (0 if none)")
	c.Flags().StringVar(&expectedAccountKey, "expected-account-key", "", "selected worker account key (automatic API-key receipts)")
	c.Flags().StringVar(&expectedRuntimeGen, "expected-runtime-generation", "", "selected runtime generation (automatic API-key receipts)")
	c.Flags().StringVar(&commit, "commit", "", "source commit")
	c.Flags().StringVar(&ociConfigDigest, "oci-config-digest", "", "OCI config digest")
	c.Flags().StringVar(&releaseManifest, "release-manifest-digest", "", "release-set digest")
	c.Flags().StringVar(&releaseCoordinate, "release-coordinate", "", "release-set coordinate")
	c.Flags().StringVar(&ociIndexDigest, "oci-index-digest", "", "optional OCI index digest")
	c.Flags().StringVar(&scheme, "scheme", "", "version scheme (legacy, inspr-calendar-v1, or inspr-calendar-v2)")
	c.Flags().StringVar(&channel, "channel", "", "release channel")
	c.Flags().Int64Var(&sequence, "sequence", 0, "release sequence")
	c.Flags().StringVar(&version, "version", "", "release version")
	c.Flags().StringVar(&qaDigest, "qa-digest", "", "scoped QA digest")
	c.Flags().BoolVar(&dryRun, "dry-run", false, "print the request and exit")
	return c
}

func loadBuiltReceiptRequest(cmd *cobra.Command, receiptFile string, fromFlags baselinebatch.BuiltReceiptRequest) (baselinebatch.BuiltReceiptRequest, error) {
	file := strings.TrimSpace(receiptFile)
	explicit := builtReceiptFlagsChanged(cmd)
	if file != "" && explicit {
		return baselinebatch.BuiltReceiptRequest{}, &usageError{msg: "use either --receipt-file or explicit identity flags, not both"}
	}
	if file != "" {
		src, err := openBuiltReceiptSource(file)
		if err != nil {
			return baselinebatch.BuiltReceiptRequest{}, err
		}
		defer src.Close()
		req, err := baselinebatch.DecodeBuiltReceiptJSON(src)
		if err != nil {
			return baselinebatch.BuiltReceiptRequest{}, &usageError{msg: "invalid receipt JSON"}
		}
		return req, nil
	}
	return fromFlags, nil
}

func builtReceiptFlagsChanged(cmd *cobra.Command) bool {
	for _, name := range []string{
		"idempotency-key", "expected-attempt-id", "expected-plan-revision",
		"expected-implementation-execution", "expected-implementation-authority-epoch",
		"expected-account-key", "expected-runtime-generation", "commit", "oci-config-digest",
		"release-manifest-digest", "release-coordinate", "oci-index-digest", "scheme",
		"channel", "sequence", "version", "qa-digest",
	} {
		if cmd.Flags().Changed(name) {
			return true
		}
	}
	return false
}

func openBuiltReceiptSource(path string) (io.ReadCloser, error) {
	if path == "-" {
		return io.NopCloser(os.Stdin), nil
	}
	src, err := os.Open(path) // #nosec G304 -- operator-selected receipt file
	if err != nil {
		return nil, &usageError{msg: "could not read --receipt-file"}
	}
	return src, nil
}
