// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package main

import (
	"encoding/json"
	"testing"
)

func TestRunReportDryRunClaudeCodeProofMapping(t *testing.T) {
	out, _, err := executeCLIForTest(t,
		"--json", "run", "report", "799",
		"--sequence", "3",
		"--correlation-id", "claude-session-abc",
		"--provider", "anthropic",
		"--adapter", "claude-code",
		"--reported-at", "2026-08-20T10:00:00Z",
		"--kind", "progress", "--heartbeat",
		"--phase", "testing",
		"--activity", "Running the documented backend test gate",
		"--estimate-revision", "2",
		"--progress-percent", "75",
		"--eta-seconds", "300", "--eta-min-seconds", "180", "--eta-max-seconds", "480",
		"--estimate-source", "adapter", "--confidence", "0.8",
		"--basis", "Three of four named verification checkpoints completed",
		"--dry-run",
	)
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Method string             `json:"method"`
		Path   string             `json:"path"`
		Body   runTelemetryReport `json:"body"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("decode dry run: %v\n%s", err, out)
	}
	if got.Method != "POST" || got.Path != "/api/runs/799/telemetry" {
		t.Fatalf("request=%s %s", got.Method, got.Path)
	}
	if got.Body.Provider != "anthropic" || got.Body.Adapter != "claude-code" || got.Body.Sequence != 3 {
		t.Fatalf("Claude Code mapping=%+v", got.Body)
	}
	if got.Body.ProgressPercent == nil || *got.Body.ProgressPercent != 75 || got.Body.EstimateConfidence == nil || *got.Body.EstimateConfidence != .8 {
		t.Fatalf("evidence mapping=%+v", got.Body)
	}
}

func TestRunReportUsesRunnerEnvironmentSeam(t *testing.T) {
	t.Setenv("PAIMOS_RUN_ID", "88")
	t.Setenv("PAIMOS_RUN_CORRELATION_ID", "run-88")
	t.Setenv("PAIMOS_RUN_PROVIDER", "anthropic")
	t.Setenv("PAIMOS_RUN_ADAPTER", "claude-code")
	out, _, err := executeCLIForTest(t,
		"--json", "run", "report", "--sequence", "1", "--kind", "heartbeat", "--heartbeat", "--dry-run")
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Path string             `json:"path"`
		Body runTelemetryReport `json:"body"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatal(err)
	}
	if got.Path != "/api/runs/88/telemetry" || got.Body.CorrelationID != "run-88" {
		t.Fatalf("runner env seam=%+v", got)
	}
}
