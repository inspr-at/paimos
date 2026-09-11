// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/inspr-at/paimos/backend/agentd"
	"github.com/spf13/cobra"
)

func runtimeAccountCmd(root, expected *string, projectID *int64, projectKey *string) *cobra.Command {
	cmd := commandGroup(&cobra.Command{Use: "account", Short: "Connect or disconnect an already enrolled opaque account on the owned runtime"})
	cmd.AddCommand(runtimeAccountMutationCmd("connect", root, expected, projectID, projectKey))
	cmd.AddCommand(runtimeAccountMutationCmd("disconnect", root, expected, projectID, projectKey))
	cmd.AddCommand(runtimeAccountStatusCmd(root, expected, projectID, projectKey))
	return cmd
}

func runtimeAccountMutationCmd(operation string, root, expected *string, projectID *int64, projectKey *string) *cobra.Command {
	var adapter, accountKey, generation, requestKey string
	var revision int64
	cmd := &cobra.Command{
		Use:   operation,
		Short: map[string]string{"connect": "Attach an already enrolled opaque account to this runtime", "disconnect": "Detach an enrolled account when it owns no unsettled work"}[operation],
		Args:  cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			client, id, err := runtimeAccountClient(*root, *expected, *projectID, *projectKey)
			if err != nil {
				return err
			}
			if strings.TrimSpace(accountKey) == "" {
				return &usageError{msg: "account " + operation + " requires --account-key"}
			}
			if strings.TrimSpace(requestKey) == "" {
				return &usageError{msg: "account " + operation + " requires --request-key"}
			}
			ctx, cancel := context.WithTimeout(c.Context(), 15*time.Second)
			defer cancel()
			if generation == "" {
				status, e := client.Status(ctx)
				if e != nil {
					return errors.New("owned runtime unavailable")
				}
				generation = status.DaemonID
			}
			if uuid.Validate(generation) != nil {
				return errors.New("runtime generation is stale or malformed")
			}
			kind := agentd.AccountLifecycleConnect
			if operation == "disconnect" {
				kind = agentd.AccountLifecycleDisconnect
			}
			result, e := client.ApplyAccountLifecycle(ctx, agentd.AccountLifecycleRequest{
				IdempotencyKey: requestKey, Operation: kind, ProjectID: id, RuntimeGeneration: generation,
				AccountKey: accountKey, Adapter: adapter, ExpectedRevision: revision,
			})
			if e != nil {
				return e
			}
			if flagJSON {
				return json.NewEncoder(stdout).Encode(result)
			}
			if _, err := stdout.Write([]byte(runtimeAccountHuman(result))); err != nil {
				return err
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&adapter, "adapter", "codex", "named-account adapter: codex, pi, or cursor")
	cmd.Flags().StringVar(&accountKey, "account-key", "", "opaque operator registry account key; never a path, env, label, or credential")
	cmd.Flags().StringVar(&generation, "runtime-generation", "", "current daemon generation UUID; omit to use the live generation")
	cmd.Flags().StringVar(&requestKey, "request-key", "", "idempotency key for this reviewed mutation")
	cmd.Flags().Int64Var(&revision, "expected-revision", 0, "expected committed attachment revision")
	return cmd
}

func runtimeAccountStatusCmd(root, expected *string, projectID *int64, projectKey *string) *cobra.Command {
	return &cobra.Command{Use: "status", Short: "Discover attached opaque accounts from committed runtime state", Args: cobra.NoArgs, RunE: func(c *cobra.Command, _ []string) error {
		client, id, err := runtimeAccountClient(*root, *expected, *projectID, *projectKey)
		if err != nil && *projectKey == "" && *projectID == 0 {
			client, err = runtimeAccountDaemon(*root, *expected)
			id = 0
		}
		if err != nil {
			return err
		}
		ctx, cancel := context.WithTimeout(c.Context(), 15*time.Second)
		defer cancel()
		states, e := client.AccountLifecycleStatus(ctx)
		if e != nil {
			return e
		}
		if id > 0 {
			filtered := make([]agentd.RuntimeAccountState, 0, len(states))
			for _, state := range states {
				if state.ProjectID == id {
					filtered = append(filtered, state)
				}
			}
			states = filtered
		}
		if flagJSON {
			return json.NewEncoder(stdout).Encode(states)
		}
		if len(states) == 0 {
			_, err := stdout.Write([]byte("no attached named accounts discovered for this runtime\n"))
			return err
		}
		return json.NewEncoder(stdout).Encode(states)
	}}
}

func runtimeAccountClient(root, expected string, projectID int64, projectKey string) (*agentd.Client, int64, error) {
	if !orchestratorInstanceNamePattern.MatchString(flagInstance) {
		return nil, 0, &usageError{msg: "runtime requires --instance with a safe configured name"}
	}
	if expected == "" {
		expected = flagInstance
	}
	if projectKey != "" {
		named, err := runtimeNamedClient(flagInstance)
		if err != nil {
			return nil, 0, errors.New("project diagnosis requires named instance authentication")
		}
		project, err := resolveExactOrchestratorProject(named, projectKey)
		if err != nil {
			return nil, 0, errors.New("authorized project key could not be resolved")
		}
		if projectID != 0 && projectID != project.ID {
			return nil, 0, errors.New("--project and --project-id disagree")
		}
		projectID = project.ID
	}
	if projectID <= 0 {
		return nil, 0, &usageError{msg: "account lifecycle requires --project or --project-id"}
	}
	client, err := runtimeAccountDaemon(root, expected)
	if err != nil {
		return nil, 0, err
	}
	return client, projectID, nil
}

func runtimeAccountDaemon(root, expected string) (*agentd.Client, error) {
	if expected == "" {
		expected = flagInstance
	}
	dir, err := agentd.InstanceStateDir(root, expected)
	if err != nil {
		return nil, errors.New("runtime state root invalid")
	}
	client, err := agentd.NewClient(filepath.Join(dir, "agentd.sock"))
	if err != nil {
		return nil, errors.New("runtime transport unavailable")
	}
	return client, nil
}

func runtimeAccountHuman(result agentd.AccountLifecycleResult) string {
	out := fmt.Sprintf("%s account %s is %s (revision %d)\n", result.Operation, result.AccountKey, result.State, result.Revision)
	if result.Replayed {
		out += "exact request replayed the original outcome\n"
	}
	if result.Advertisement == agentd.AccountAdvertisementRestartRequired {
		out += "public advertisement updates on the next owned runtime generation; local start already uses committed attachments\n"
	}
	return out
}
