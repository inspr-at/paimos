// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/inspr-at/paimos/backend/managedharness"
	"github.com/inspr-at/paimos/backend/models"
	"github.com/spf13/cobra"
)

func harnessCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "harness", Short: "Manage durable harness-session control-plane state"}
	cmd.AddCommand(harnessRegisterCmd(), harnessListCmd(), harnessStatusCmd(), harnessHeartbeatCmd(), harnessYieldCmd(),
		harnessDrainCmd(), harnessCompleteDeliveryCmd(), harnessDrainSteerCmd(), harnessCompleteSteerCmd(),
		harnessControlCmd("interrupt"), harnessControlCmd("stop"), harnessCompleteControlCmd(), harnessMarkStoppedCmd())
	return cmd
}

func parseHarnessCapabilities(raw []string) (models.HarnessCapabilities, error) {
	var out models.HarnessCapabilities
	for _, group := range raw {
		for _, value := range strings.Split(group, ",") {
			switch strings.ToLower(strings.TrimSpace(value)) {
			case "":
			case "inbox":
				out.Inbox = true
			case "status":
				out.Status = true
			case "steer":
				out.Steer = true
			case "interrupt":
				out.Interrupt = true
			case "stop":
				out.Stop = true
			default:
				return out, &usageError{msg: "--capability must contain only inbox,status,steer,interrupt,stop"}
			}
		}
	}
	return out, nil
}

func harnessRegisterCmd() *cobra.Command {
	var project, agent, harness, host, refFile, targetID, management, role, steerMode string
	var capabilities []string
	cmd := &cobra.Command{Use: "register", Short: "Register a durable managed or unmanaged harness session", RunE: func(cmd *cobra.Command, args []string) error {
		if strings.TrimSpace(project) == "" || strings.TrimSpace(agent) == "" || strings.TrimSpace(harness) == "" || strings.TrimSpace(host) == "" || strings.TrimSpace(refFile) == "" {
			return &usageError{msg: "--project, --agent, --harness, --host, and --harness-session-file are required"}
		}
		rawRef, err := readProtectedSecretInput(refFile, "--harness-session-file")
		if err != nil {
			return err
		}
		ref := strings.TrimSpace(string(rawRef))
		if ref == "" || strings.ContainsAny(ref, "\r\n") {
			return &usageError{msg: "--harness-session-file must contain exactly one opaque identifier line"}
		}
		caps, err := parseHarnessCapabilities(capabilities)
		if err != nil {
			return err
		}
		input := managedharness.RegisterInput{ProjectID: 1, AgentName: agent, Harness: harness, Host: host, SessionRef: ref, MessageTargetID: targetID, ManagementMode: management, Role: role, SteerMode: steerMode, Capabilities: caps}
		if err := managedharness.ValidateRegistration(input); err != nil {
			return &usageError{msg: err.Error()}
		}
		client, err := instanceClient()
		if err != nil {
			return err
		}
		projectID, err := resolveProjectRefToID(client, project)
		if err != nil {
			return reportError(err)
		}
		payload := map[string]any{"agent_name": agent, "harness": harness, "host": host, "harness_session_ref": ref, "message_target_id": targetID, "management_mode": management, "role": role, "steer_mode": steerMode, "advertised_capabilities": caps}
		response, err := client.do(http.MethodPost, fmt.Sprintf("/api/projects/%d/harness-sessions", projectID), payload)
		if err != nil {
			return reportError(err)
		}
		return printHarnessResponse(response)
	}}
	cmd.Flags().StringVarP(&project, "project", "p", "", "project key or numeric id (required)")
	cmd.Flags().StringVar(&agent, "agent", "", "registered project agent (required)")
	cmd.Flags().StringVar(&harness, "harness", "", "harness address prefix (required)")
	cmd.Flags().StringVar(&host, "host", "", "non-secret host attribution (required)")
	cmd.Flags().StringVar(&refFile, "harness-session-file", "", "protected 0600 file containing the private session reference, or - for stdin")
	cmd.Flags().StringVar(&targetID, "message-target-id", "", "existing encrypted target id for an unmanaged inbox")
	cmd.Flags().StringVar(&management, "management", "managed", "managed or unmanaged")
	cmd.Flags().StringVar(&role, "role", "worker", "coordinator or worker")
	cmd.Flags().StringVar(&steerMode, "steer-mode", "none", "none, owned, or codex_external")
	cmd.Flags().StringSliceVar(&capabilities, "capability", nil, "advertised capabilities: inbox,status,steer,interrupt,stop")
	return cmd
}

func harnessProjectCommand(use, short, method, suffix string, body func() any, attributed bool) *cobra.Command {
	var project, sessionID, controlID, agent string
	cmd := &cobra.Command{Use: use, Short: short, RunE: func(cmd *cobra.Command, args []string) error {
		if project == "" {
			return &usageError{msg: "--project is required"}
		}
		if strings.Contains(suffix, "{session}") && sessionID == "" {
			return &usageError{msg: "--session is required"}
		}
		if strings.Contains(suffix, "{control}") && controlID == "" {
			return &usageError{msg: "--control-id is required"}
		}
		client, err := instanceClient()
		if err != nil {
			return err
		}
		id, err := resolveProjectRefToID(client, project)
		if err != nil {
			return reportError(err)
		}
		path := fmt.Sprintf("/api/projects/%d/harness-sessions", id) + strings.ReplaceAll(strings.ReplaceAll(suffix, "{session}", sessionID), "{control}", controlID)
		var raw []byte
		if attributed {
			if agent == "" {
				return &usageError{msg: "--agent is required for worker operations"}
			}
			raw, err = client.doForAgentContext(cmd.Context(), method, path, body(), agent)
		} else {
			raw, err = client.do(method, path, body())
		}
		if err != nil {
			return reportError(err)
		}
		return printHarnessResponse(raw)
	}}
	cmd.Flags().StringVarP(&project, "project", "p", "", "project key or numeric id (required)")
	if strings.Contains(suffix, "{session}") {
		cmd.Flags().StringVar(&sessionID, "session", "", "public harness session UUID (required)")
	}
	if strings.Contains(suffix, "{control}") {
		cmd.Flags().StringVar(&controlID, "control-id", "", "claimed control UUID (required)")
	}
	if attributed {
		cmd.Flags().StringVar(&agent, "agent", "", "registered project agent attribution (required)")
	}
	return cmd
}

func harnessListCmd() *cobra.Command {
	return harnessProjectCommand("list", "List non-secret harness sessions", http.MethodGet, "", func() any { return nil }, false)
}
func harnessStatusCmd() *cobra.Command {
	return harnessProjectCommand("status", "Show one non-secret harness session", http.MethodGet, "/{session}", func() any { return nil }, false)
}
func harnessHeartbeatCmd() *cobra.Command {
	var phase string
	cmd := harnessProjectCommand("heartbeat", "Report harness status", http.MethodPost, "/{session}/heartbeat", func() any { return map[string]string{"phase": phase} }, true)
	cmd.Flags().StringVar(&phase, "phase", "working", "starting, working, yielded, or stopping")
	return cmd
}
func harnessYieldCmd() *cobra.Command {
	return harnessProjectCommand("yield", "Yield and claim typed owned controls", http.MethodPost, "/{session}/yield", func() any { return map[string]any{} }, true)
}
func harnessDrainCmd() *cobra.Command {
	return harnessProjectCommand("drain", "Lease managed inbox work in canonical FIFO order", http.MethodPost, "/{session}/drain", func() any { return map[string]any{} }, true)
}
func harnessDrainSteerCmd() *cobra.Command {
	return harnessProjectCommand("drain-steer", "Lease full FIFO work for a steer-capable managed worker", http.MethodPost, "/{session}/drain-steer", func() any { return map[string]any{} }, true)
}
func harnessCompleteDeliveryCmd() *cobra.Command {
	return harnessDeliveryCompletionCmd("complete-delivery", "Complete a leased managed delivery canonically", "/{session}/complete-delivery", "simple")
}
func harnessCompleteSteerCmd() *cobra.Command {
	return harnessDeliveryCompletionCmd("complete-steer", "Complete a leased delivery for a steer-capable worker", "/{session}/complete-steer", "steer")
}
func harnessDeliveryCompletionCmd(use, short, suffix, defaultLevel string) *cobra.Command {
	var cursor int64
	var delivery, level, reason string
	cmd := harnessProjectCommand(use, short, http.MethodPost, suffix, func() any {
		return map[string]any{"cursor": cursor, "delivery_id": delivery, "effective_level": level, "fallback_reason": reason}
	}, true)
	cmd.Flags().Int64Var(&cursor, "cursor", 0, "leased message cursor")
	cmd.Flags().StringVar(&delivery, "delivery-id", "", "leased delivery UUID")
	cmd.Flags().StringVar(&level, "effective-level", defaultLevel, "simple or steer")
	cmd.Flags().StringVar(&reason, "fallback-reason", "", "canonical fallback reason when effective level is simple")
	return cmd
}
func harnessControlCmd(kind string) *cobra.Command {
	return harnessProjectCommand(kind, "Request typed owned "+kind, http.MethodPost, "/{session}/controls/"+kind, func() any { return map[string]any{} }, false)
}
func harnessCompleteControlCmd() *cobra.Command {
	var outcome, reason string
	cmd := harnessProjectCommand("complete-control", "Complete a claimed typed owned control", http.MethodPost, "/{session}/controls/{control}/complete", func() any { return map[string]string{"outcome": outcome, "reason": reason} }, true)
	cmd.Flags().StringVar(&outcome, "outcome", "applied", "applied or rejected")
	cmd.Flags().StringVar(&reason, "reason", "applied", "closed completion reason")
	return cmd
}

func harnessMarkStoppedCmd() *cobra.Command {
	return harnessProjectCommand("mark-stopped", "Mark the attributed session stopped after owned cleanup", http.MethodPost, "/{session}/stop", func() any { return map[string]any{} }, true)
}

func printHarnessResponse(raw []byte) error {
	if flagJSON {
		fmt.Fprintln(stdout, strings.TrimSpace(string(raw)))
		return nil
	}
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return err
	}
	pretty, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	fmt.Fprintln(stdout, string(pretty))
	return nil
}
