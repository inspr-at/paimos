// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU Affero General Public License as
// published by the Free Software Foundation, version 3.

package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/spf13/cobra"
)

var (
	orchestratorInstanceNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)
	orchestratorProjectKeyPattern   = regexp.MustCompile(`^[A-Z][A-Z0-9]{2,9}$`)
	orchestratorAgentKeyPattern     = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,31}$`)
)

type orchestratorHealth struct {
	AgentBusInstance         string `json:"agent_bus_instance"`
	DeploymentInstance       string `json:"deployment_instance"`
	AgentBusIdentityEnforced bool   `json:"agent_bus_identity_enforced"`
}

type orchestratorProject struct {
	ID  int64  `json:"id"`
	Key string `json:"key"`
}

type orchestratorTarget struct {
	ProjectID      int64  `json:"project_id"`
	ProjectKey     string `json:"project_key"`
	ProjectAgentID int64  `json:"project_agent_id"`
	Key            string `json:"key"`
	DisplayLabel   string `json:"display_label"`
}

type orchestratorConfig struct {
	SchemaVersion int                 `json:"schema_version"`
	Revision      int64               `json:"revision"`
	Orchestrator  *orchestratorTarget `json:"orchestrator"`
	UpdatedAt     *string             `json:"updated_at"`
}

func orchestratorCmd() *cobra.Command {
	command := &cobra.Command{
		Use:   "orchestrator",
		Short: "Configure the current instance orchestrator",
		Long: `Configure the explicit orchestrator pin on exactly the selected PAIMOS
instance. This command never guesses an agent: set requires an exact project key,
canonical project-agent key, and display label. The current revision is read just
before the compare-and-swap write, so a concurrent change fails closed.`,
		Args: cobra.NoArgs,
	}
	command.AddCommand(orchestratorSetCmd())
	return command
}

func orchestratorSetCmd() *cobra.Command {
	var projectKey, agentKey, displayLabel, expectedDeployment string
	command := &cobra.Command{
		Use:   "set",
		Short: "Set the explicit instance orchestrator",
		Args:  cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			projectKey = strings.TrimSpace(projectKey)
			agentKey = strings.TrimSpace(agentKey)
			expectedDeployment = strings.TrimSpace(expectedDeployment)
			if !orchestratorInstanceNamePattern.MatchString(expectedDeployment) || expectedDeployment == "default" {
				return &usageError{msg: "--expect-deployment-instance is required and must be a safe non-default deployment identity"}
			}
			if !orchestratorProjectKeyPattern.MatchString(projectKey) {
				return &usageError{msg: "--project is required and must be an exact canonical project key"}
			}
			if !orchestratorAgentKeyPattern.MatchString(agentKey) || agentKey == "web-ui" {
				return &usageError{msg: "--agent must be a canonical project-agent key ([a-z][a-z0-9_-]*, max 32; web-ui is reserved)"}
			}
			if reason := validateOrchestratorDisplayLabel(displayLabel); reason != "" {
				return &usageError{msg: "--display-label " + reason}
			}

			client, err := instanceClient()
			if err != nil {
				return err
			}
			if err := verifyOrchestratorDeployment(client, expectedDeployment); err != nil {
				return reportError(err)
			}
			project, err := resolveExactOrchestratorProject(client, projectKey)
			if err != nil {
				return reportError(err)
			}
			if err := resolveExactOrchestratorAgent(client, project.ID, agentKey); err != nil {
				return reportError(err)
			}

			current, err := readOrchestratorConfig(client)
			if err != nil {
				return reportError(err)
			}
			if current.Orchestrator != nil && current.Orchestrator.ProjectID == project.ID &&
				current.Orchestrator.ProjectKey == project.Key && current.Orchestrator.Key == agentKey &&
				current.Orchestrator.DisplayLabel == displayLabel {
				return renderOrchestratorSet(current, true)
			}

			payload := map[string]any{
				"expected_revision": current.Revision,
				"orchestrator": map[string]any{
					"project_id":    project.ID,
					"key":           agentKey,
					"display_label": displayLabel,
				},
			}
			raw, err := client.do(http.MethodPut, "/api/orchestrator/v1/config", payload)
			if err != nil {
				return reportError(err)
			}
			var configured orchestratorConfig
			if err := json.Unmarshal(raw, &configured); err != nil {
				return fmt.Errorf("decode orchestrator config: %w", err)
			}
			if err := validateOrchestratorConfig(configured); err != nil {
				return err
			}
			if configured.Revision != current.Revision+1 || configured.Orchestrator == nil ||
				configured.Orchestrator.ProjectID != project.ID || configured.Orchestrator.ProjectKey != project.Key ||
				configured.Orchestrator.Key != agentKey || configured.Orchestrator.DisplayLabel != displayLabel {
				return fmt.Errorf("server returned an unexpected orchestrator configuration")
			}
			return renderOrchestratorSet(configured, false)
		},
	}
	command.Flags().StringVar(&projectKey, "project", "", "exact project key (required)")
	command.Flags().StringVar(&agentKey, "agent", "", "exact canonical project-agent key (required)")
	command.Flags().StringVar(&displayLabel, "display-label", "", "display label, 1 to 64 UTF-8 bytes (required)")
	command.Flags().StringVar(&expectedDeployment, "expect-deployment-instance", "", "expected server deployment identity (required safety check)")
	return command
}

func verifyOrchestratorDeployment(client *Client, expected string) error {
	raw, err := client.do(http.MethodGet, "/api/health", nil)
	if err != nil {
		return err
	}
	var health orchestratorHealth
	if err := json.Unmarshal(raw, &health); err != nil {
		return fmt.Errorf("decode server health: %w", err)
	}
	if !health.AgentBusIdentityEnforced ||
		health.DeploymentInstance != expected || health.AgentBusInstance != expected {
		return fmt.Errorf("server deployment identity mismatch: expected %q with enforced matching deployment and agent-bus identities", expected)
	}
	return nil
}

func resolveExactOrchestratorProject(client *Client, want string) (orchestratorProject, error) {
	raw, err := client.do(http.MethodGet, "/api/projects?status=all", nil)
	if err != nil {
		return orchestratorProject{}, err
	}
	var projects []orchestratorProject
	if err := json.Unmarshal(raw, &projects); err != nil {
		return orchestratorProject{}, fmt.Errorf("decode projects: %w", err)
	}
	var match orchestratorProject
	matches := 0
	for _, project := range projects {
		if project.Key == want {
			match = project
			matches++
		}
	}
	if matches == 0 {
		return orchestratorProject{}, fmt.Errorf("project key %q not found on the selected instance", want)
	}
	if matches != 1 || match.ID <= 0 {
		return orchestratorProject{}, fmt.Errorf("project key %q is ambiguous or invalid on the selected instance", want)
	}
	return match, nil
}

func resolveExactOrchestratorAgent(client *Client, projectID int64, want string) error {
	raw, err := client.do(http.MethodGet, fmt.Sprintf("/api/projects/%d/agents", projectID), nil)
	if err != nil {
		return err
	}
	var agents []agentSummary
	if err := json.Unmarshal(raw, &agents); err != nil {
		return fmt.Errorf("decode agents: %w", err)
	}
	matches := 0
	for _, agent := range agents {
		if agent.Name == want {
			matches++
		}
	}
	if matches == 0 {
		return fmt.Errorf("agent %q is not a declared canonical agent on project id %d", want, projectID)
	}
	if matches != 1 {
		return fmt.Errorf("agent %q is ambiguous on project id %d", want, projectID)
	}
	return nil
}

func readOrchestratorConfig(client *Client) (orchestratorConfig, error) {
	raw, err := client.do(http.MethodGet, "/api/orchestrator/v1/config", nil)
	if err != nil {
		return orchestratorConfig{}, err
	}
	var config orchestratorConfig
	if err := json.Unmarshal(raw, &config); err != nil {
		return orchestratorConfig{}, fmt.Errorf("decode orchestrator config: %w", err)
	}
	if err := validateOrchestratorConfig(config); err != nil {
		return orchestratorConfig{}, err
	}
	return config, nil
}

func validateOrchestratorConfig(config orchestratorConfig) error {
	if config.SchemaVersion != 1 || config.Revision < 0 {
		return fmt.Errorf("invalid orchestrator config returned by server")
	}
	if config.Revision == 0 && (config.Orchestrator != nil || config.UpdatedAt != nil) {
		return fmt.Errorf("invalid pristine orchestrator config returned by server")
	}
	if config.Revision > 0 && (config.UpdatedAt == nil || strings.TrimSpace(*config.UpdatedAt) == "") {
		return fmt.Errorf("invalid updated orchestrator config returned by server")
	}
	if target := config.Orchestrator; target != nil {
		if target.ProjectID <= 0 || target.ProjectAgentID <= 0 ||
			!orchestratorProjectKeyPattern.MatchString(target.ProjectKey) ||
			!orchestratorAgentKeyPattern.MatchString(target.Key) || target.Key == "web-ui" ||
			validateOrchestratorDisplayLabel(target.DisplayLabel) != "" {
			return fmt.Errorf("invalid orchestrator target returned by server")
		}
	}
	return nil
}

func validateOrchestratorDisplayLabel(label string) string {
	if label == "" || !utf8.ValidString(label) {
		return "is required"
	}
	if strings.TrimSpace(label) != label {
		return "must not have leading or trailing whitespace"
	}
	if len([]byte(label)) > 64 {
		return "must be at most 64 UTF-8 bytes"
	}
	for _, r := range label {
		if r < 0x20 || r == 0x7f {
			return "must not contain control characters"
		}
	}
	return ""
}

func renderOrchestratorSet(config orchestratorConfig, unchanged bool) error {
	if flagJSON {
		return emitJSON(config)
	}
	if config.Orchestrator == nil {
		return fmt.Errorf("server returned an unset orchestrator after configuration")
	}
	verb := "configured"
	if unchanged {
		verb = "already configured"
	}
	fmt.Fprintf(stdout, "✓ orchestrator %s as %s (project %s, agent %s, revision %d)\n",
		verb, config.Orchestrator.DisplayLabel, config.Orchestrator.ProjectKey,
		config.Orchestrator.Key, config.Revision)
	return nil
}
