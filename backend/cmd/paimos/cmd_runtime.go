// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/inspr-at/paimos/backend/agentd"
	"github.com/inspr-at/paimos/backend/runtimehealth"
	"github.com/spf13/cobra"
)

// No service is installed implicitly;
// every mutation is scoped to a verified platform declaration and private root.
func runtimeCmd() *cobra.Command {
	var root, file, service, expected, confirm string
	var projectID int64
	var projectKey string
	cache, _ := os.UserCacheDir()
	cmd := &cobra.Command{Use: "runtime", Short: "Set up, diagnose, repair or reset one local Agent Intercom runtime", Args: cobra.NoArgs}
	cmd.PersistentFlags().StringVar(&root, "state-root", filepath.Join(cache, "paimos", "agentd"), "private agentd state root")
	cmd.PersistentFlags().StringVar(&file, "service-file", "", "reviewed LaunchAgent plist or Linux user unit (default: named-instance service)")
	cmd.PersistentFlags().StringVar(&service, "service-name", "", "platform service label or unit name")
	cmd.PersistentFlags().StringVar(&expected, "expect-deployment-instance", "", "expected authenticated deployment identity (defaults to named instance)")
	cmd.PersistentFlags().StringVar(&projectKey, "project", "", "authorized canonical project key for scoped diagnosis")
	cmd.PersistentFlags().Int64Var(&projectID, "project-id", 0, "authorized project for canonical-agent and target diagnosis")
	for _, operation := range []string{"doctor", "setup", "repair", "reset"} {
		child := &cobra.Command{Use: operation, Short: map[string]string{"doctor": "Report independent readiness layers without changing runtime state", "setup": "Verify the declarative service and reconnect idempotently", "repair": "Repair owned state within a persistent three-attempt budget", "reset": "Preview recoverable reset; --confirm applies that exact preview"}[operation], Args: cobra.NoArgs}
		child.RunE = func(c *cobra.Command, _ []string) error {
			if !orchestratorInstanceNamePattern.MatchString(flagInstance) {
				return &usageError{msg: "runtime requires --instance with a safe configured name"}
			}
			if expected == "" {
				expected = flagInstance
			}
			if !orchestratorInstanceNamePattern.MatchString(expected) || expected == "default" {
				return &usageError{msg: "--expect-deployment-instance must name a non-default deployment"}
			}
			if projectID < 0 {
				return &usageError{msg: "--project-id must be positive when supplied"}
			}
			if projectKey != "" {
				named, err := runtimeNamedClient(flagInstance)
				if err != nil {
					return errors.New("project diagnosis requires named instance authentication")
				}
				project, err := resolveExactOrchestratorProject(named, projectKey)
				if err != nil {
					return errors.New("authorized project key could not be resolved")
				}
				if projectID != 0 && projectID != project.ID {
					return errors.New("--project and --project-id disagree")
				}
				projectID = project.ID
			}
			home, e := os.UserHomeDir()
			if e != nil {
				return errors.New("runtime home unavailable")
			}
			manager, e := runtimehealth.NewPlatformService(runtime.GOOS, home, expected, root, file, service)
			if e != nil {
				return e
			}
			dir, e := agentd.InstanceStateDir(root, expected)
			if e != nil {
				return errors.New("runtime state root invalid")
			}
			daemon, e := agentd.NewClient(filepath.Join(dir, "agentd.sock"))
			if e != nil {
				return errors.New("runtime transport unavailable")
			}
			if named, err := runtimeNamedClient(flagInstance); err == nil {
				manager.ExpectedURL = named.baseURL
			}
			remote := runtimeRemoteProbe(flagInstance, expected, projectID)
			local, e := runtimehealth.New(runtimehealth.Config{Instance: expected, StateRoot: root, Service: manager, Daemon: daemon, Remote: remote})
			if e != nil {
				return e
			}
			ctx, cancel := context.WithTimeout(c.Context(), 45*time.Second)
			defer cancel()
			var report runtimehealth.Report
			switch operation {
			case "doctor":
				report = local.Doctor(ctx)
				if !report.Ready {
					e = runtimehealth.ErrActionRequired
				}
			case "setup":
				report, e = local.Setup(ctx)
			case "repair":
				report, e = local.Repair(ctx)
			case "reset":
				report, e = local.Reset(ctx, confirm)
			}
			prefix := "paimos --instance " + flagInstance
			if flagConfigPath != "" {
				prefix += " --config " + runtimeShellQuote(flagConfigPath)
			}
			report.Bootstrap = prefix + " runtime setup --state-root " + runtimeShellQuote(root) + " --expect-deployment-instance " + expected
			if file != "" {
				report.Bootstrap += " --service-file " + runtimeShellQuote(file)
			}
			if service != "" {
				report.Bootstrap += " --service-name " + runtimeShellQuote(service)
			}
			if projectID > 0 {
				report.Bootstrap += fmt.Sprintf(" --project-id %d", projectID)
			}
			if operation == "reset" && confirm == "" && report.Plan != nil && report.Plan.CanApply {
				for i := range report.Layers {
					if report.Layers[i].Name == "reset" {
						report.Layers[i].Action = strings.Replace(report.Bootstrap, "runtime setup", "runtime reset", 1) + " --confirm " + report.Plan.Token
					}
				}
			}
			if flagJSON {
				if err := json.NewEncoder(stdout).Encode(report); err != nil {
					return err
				}
			} else {
				if err := report.WriteHuman(stdout); err != nil {
					return err
				}
			}
			if e != nil {
				return runtimehealth.ErrActionRequired
			}
			return nil
		}
		if operation == "reset" {
			child.Flags().StringVar(&confirm, "confirm", "", "apply only the current reset preview token")
		}
		cmd.AddCommand(child)
	}
	return cmd
}

func runtimeShellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}
