// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU Affero General Public License as
// published by the Free Software Foundation, version 3.

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

// runTelemetryReport is the stable client seam consumed by local runner
// adapters. Keep it provider-neutral and in sync with AgentRunTelemetryInput.
type runTelemetryReport struct {
	Sequence           int64    `json:"sequence"`
	CorrelationID      string   `json:"correlation_id"`
	Provider           string   `json:"provider"`
	Adapter            string   `json:"adapter"`
	AgentReportedAt    string   `json:"agent_reported_at"`
	Kind               string   `json:"kind"`
	Heartbeat          bool     `json:"heartbeat"`
	Phase              string   `json:"phase,omitempty"`
	Activity           string   `json:"activity,omitempty"`
	NeedsInput         bool     `json:"needs_input"`
	BlockerState       string   `json:"blocker_state,omitempty"`
	EstimateRevision   *int64   `json:"estimate_revision,omitempty"`
	ProgressPercent    *float64 `json:"progress_percent,omitempty"`
	ETASeconds         *int64   `json:"eta_seconds,omitempty"`
	ETAMinSeconds      *int64   `json:"eta_min_seconds,omitempty"`
	ETAMaxSeconds      *int64   `json:"eta_max_seconds,omitempty"`
	EstimateSource     string   `json:"estimate_source,omitempty"`
	EstimateConfidence *float64 `json:"estimate_confidence,omitempty"`
	EstimateBasis      string   `json:"estimate_basis,omitempty"`
}

func runCmd() *cobra.Command {
	c := &cobra.Command{Use: "run", Short: "Inspect and report one agent run"}
	c.AddCommand(runReportCmd())
	return c
}

func runReportCmd() *cobra.Command {
	var report runTelemetryReport
	var reportedAt string
	var estimateRevision, etaSeconds, etaMinSeconds, etaMaxSeconds int64
	var progressPercent, estimateConfidence float64
	var dryRun bool
	c := &cobra.Command{
		Use:   "report [run-id]",
		Short: "Append one provider-neutral progress report to a run",
		Long: `Append one allowlisted telemetry fact to a PAIMOS agent run.

Reports are immutable and sequence numbers must increase. Percentage and ETA
are optional evidence-backed declarations; PAIMOS never derives them from
elapsed wall-clock time. The built-in supervisor owns its sequence space;
external adapters must serialize reports and reuse the exact body/sequence when
retrying an ambiguous result. A new append returns 201 and an exact duplicate
returns 200. If run-id is omitted, PAIMOS_RUN_ID is used.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			runIDRaw := strings.TrimSpace(os.Getenv("PAIMOS_RUN_ID"))
			if len(args) == 1 {
				runIDRaw = args[0]
			}
			runID, err := strconv.ParseInt(runIDRaw, 10, 64)
			if err != nil || runID <= 0 {
				return &usageError{msg: "run-id must be a positive integer (argument or PAIMOS_RUN_ID)"}
			}
			report.CorrelationID = firstRunReportValue(report.CorrelationID, "PAIMOS_RUN_CORRELATION_ID")
			report.Provider = firstRunReportValue(report.Provider, "PAIMOS_RUN_PROVIDER")
			report.Adapter = firstRunReportValue(report.Adapter, "PAIMOS_RUN_ADAPTER")
			if report.Sequence <= 0 || report.CorrelationID == "" || report.Provider == "" || report.Adapter == "" || report.Kind == "" {
				return &usageError{msg: "--sequence, --correlation-id, --provider, --adapter, and --kind are required"}
			}
			if strings.TrimSpace(reportedAt) == "" {
				reportedAt = time.Now().UTC().Format(time.RFC3339Nano)
			} else if _, err := time.Parse(time.RFC3339Nano, reportedAt); err != nil {
				return &usageError{msg: "--reported-at must be RFC3339"}
			}
			report.AgentReportedAt = reportedAt
			if cmd.Flags().Changed("estimate-revision") {
				report.EstimateRevision = &estimateRevision
			}
			if cmd.Flags().Changed("progress-percent") {
				report.ProgressPercent = &progressPercent
			}
			if cmd.Flags().Changed("eta-seconds") {
				report.ETASeconds = &etaSeconds
			}
			if cmd.Flags().Changed("eta-min-seconds") {
				report.ETAMinSeconds = &etaMinSeconds
			}
			if cmd.Flags().Changed("eta-max-seconds") {
				report.ETAMaxSeconds = &etaMaxSeconds
			}
			if cmd.Flags().Changed("confidence") {
				report.EstimateConfidence = &estimateConfidence
			}
			if dryRun {
				return emitJSON(map[string]any{
					"method": http.MethodPost,
					"path":   fmt.Sprintf("/api/runs/%d/telemetry", runID),
					"body":   report,
				})
			}
			client, err := instanceClient()
			if err != nil {
				return err
			}
			raw, err := reportRunTelemetry(client, runID, report)
			if err != nil {
				return reportError(err)
			}
			if flagJSON {
				var result any
				if err := json.Unmarshal(raw, &result); err != nil {
					return fmt.Errorf("decode telemetry response: %w", err)
				}
				return emitJSON(result)
			}
			fmt.Fprintf(stdout, "reported run %d sequence %d (%s)\n", runID, report.Sequence, report.Kind)
			return nil
		},
	}
	c.Flags().Int64Var(&report.Sequence, "sequence", 0, "monotonic report sequence (required)")
	c.Flags().StringVar(&report.CorrelationID, "correlation-id", "", "stable run correlation id (or PAIMOS_RUN_CORRELATION_ID)")
	c.Flags().StringVar(&report.Provider, "provider", "", "provider identifier (or PAIMOS_RUN_PROVIDER)")
	c.Flags().StringVar(&report.Adapter, "adapter", "", "adapter identifier (or PAIMOS_RUN_ADAPTER)")
	c.Flags().StringVar(&reportedAt, "reported-at", "", "agent clock time (RFC3339; default now)")
	c.Flags().StringVar(&report.Kind, "kind", "", "heartbeat|progress|phase|needs_input|blocker|estimate")
	c.Flags().BoolVar(&report.Heartbeat, "heartbeat", false, "mark this report as a liveness heartbeat")
	c.Flags().StringVar(&report.Phase, "phase", "unknown", "bounded phase hint")
	c.Flags().StringVar(&report.Activity, "activity", "", "one-line human activity summary (max 280 bytes)")
	c.Flags().BoolVar(&report.NeedsInput, "needs-input", false, "run requires human input")
	c.Flags().StringVar(&report.BlockerState, "blocker-state", "none", "none|input|dependency|permission|environment|external|unknown")
	c.Flags().Int64Var(&estimateRevision, "estimate-revision", 0, "monotonic estimate revision")
	c.Flags().Float64Var(&progressPercent, "progress-percent", 0, "evidence-backed progress from 0 through 100")
	c.Flags().Int64Var(&etaSeconds, "eta-seconds", 0, "evidence-backed ETA in seconds")
	c.Flags().Int64Var(&etaMinSeconds, "eta-min-seconds", 0, "ETA lower bound in seconds")
	c.Flags().Int64Var(&etaMaxSeconds, "eta-max-seconds", 0, "ETA upper bound in seconds")
	c.Flags().StringVar(&report.EstimateSource, "estimate-source", "", "agent|adapter|provider|tool")
	c.Flags().Float64Var(&estimateConfidence, "confidence", 0, "estimate confidence from 0 through 1")
	c.Flags().StringVar(&report.EstimateBasis, "basis", "", "one-line evidence basis (max 240 bytes)")
	c.Flags().BoolVar(&dryRun, "dry-run", false, "print the resolved request without sending it")
	return c
}

func firstRunReportValue(flagValue, envName string) string {
	if value := strings.TrimSpace(flagValue); value != "" {
		return value
	}
	return strings.TrimSpace(os.Getenv(envName))
}

func reportRunTelemetry(client *Client, runID int64, report runTelemetryReport) ([]byte, error) {
	return client.do(http.MethodPost, fmt.Sprintf("/api/runs/%d/telemetry", runID), report)
}

func reportRunTelemetryContext(ctx context.Context, client *Client, runID int64, report runTelemetryReport) error {
	body, err := json.Marshal(report)
	if err != nil {
		return fmt.Errorf("encode telemetry: %w", err)
	}
	return postRunTelemetryBody(ctx, client, runID, body)
}

func postRunTelemetryBody(ctx context.Context, client *Client, runID int64, body []byte) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		client.baseURL+fmt.Sprintf("/api/runs/%d/telemetry", runID), bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("build telemetry request: %w", err)
	}
	client.prepareRequest(req, true, "application/json", "application/json")
	raw, err := client.doRequest(req)
	if err != nil {
		return err
	}
	var accepted struct {
		Accepted  bool `json:"accepted"`
		Duplicate bool `json:"duplicate"`
	}
	if err := json.Unmarshal(raw, &accepted); err != nil || !accepted.Accepted {
		return errors.New("telemetry response did not authoritatively confirm acceptance")
	}
	return nil
}

func fetchRunStatusContext(ctx context.Context, client *Client, runID int64) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		client.baseURL+fmt.Sprintf("/api/runs/%d", runID), nil)
	if err != nil {
		return "", fmt.Errorf("build run status request: %w", err)
	}
	client.prepareRequest(req, false, "", "application/json")
	raw, err := client.doRequest(req)
	if err != nil {
		return "", err
	}
	var run struct {
		Status string `json:"status"`
	}
	if err := json.Unmarshal(raw, &run); err != nil {
		return "", fmt.Errorf("decode run status: %w", err)
	}
	status := strings.TrimSpace(run.Status)
	if status == "" {
		return "", errors.New("run status response omitted status")
	}
	return status, nil
}
