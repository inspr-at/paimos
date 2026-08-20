// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package main

import (
	"encoding/json"
	"strings"
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

func TestRunReportEnforcesUTF8ByteBoundsBeforeDryRunOrNetwork(t *testing.T) {
	base := []string{
		"run", "report", "799", "--sequence", "1", "--correlation-id", "run-799",
		"--provider", "paimos", "--adapter", "custom-runner", "--kind", "progress", "--dry-run",
	}
	for _, tt := range []struct {
		name, flag string
		exact      string
		over       string
		want       string
	}{
		{name: "activity", flag: "--activity", exact: strings.Repeat("é", 140), over: strings.Repeat("é", 141), want: "280 UTF-8 bytes"},
		{name: "basis", flag: "--basis", exact: strings.Repeat("é", 120), over: strings.Repeat("é", 121), want: "240 UTF-8 bytes"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			args := append(append([]string(nil), base...), tt.flag, tt.exact)
			if _, _, err := executeCLIForTest(t, args...); err != nil {
				t.Fatalf("exact byte boundary rejected: %v", err)
			}
			args = append(append([]string(nil), base...), tt.flag, tt.over)
			if _, _, err := executeCLIForTest(t, args...); err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("over-bound error=%v, want %q", err, tt.want)
			}
		})
	}
}
