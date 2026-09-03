// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

// Command paimos-agentd is the operator-local owner of managed harness
// children. It inherits the operator's authenticated CLI environment and
// never accepts provider credentials of its own.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/inspr-at/paimos/backend/agentd"
)

var Version = "dev"

type commonFlags struct{ instance, stateRoot, socket string }

func main() {
	if err := run(os.Args[1:], os.Stdin, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "paimos-agentd:", err)
		os.Exit(1)
	}
}

func run(args []string, stdin io.Reader, stdout io.Writer) error {
	if len(args) == 1 && args[0] == "--version" {
		_, err := fmt.Fprintln(stdout, Version)
		return err
	}
	command := "serve"
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		command, args = args[0], args[1:]
	}
	if command == "version" || command == "--version" {
		_, err := fmt.Fprintln(stdout, Version)
		return err
	}
	flags := flag.NewFlagSet(command, flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	common := addCommonFlags(flags)
	adapter, workspace, identity, role, parentSessionID := "codex", "", "", "worker", ""
	var projectID, ticketID int64
	sessionID, correlationID, codexPath := "", "", ""
	claudePath, nodePath, claudeSDKPath := "", "", ""
	reportHost, reportURL, reportAPIKeyFile, paimosPath := "", "", "", ""
	if command == "serve" {
		flags.StringVar(&codexPath, "codex-path", "", "absolute Codex CLI path")
		flags.StringVar(&claudePath, "claude-path", "", "absolute operator-authenticated Claude CLI path")
		flags.StringVar(&nodePath, "node-path", "", "absolute Node.js >=18 runtime path")
		flags.StringVar(&claudeSDKPath, "claude-sdk-path", "", "absolute operator-installed @anthropic-ai/claude-agent-sdk@0.3.251 sdk.mjs path")
		flags.StringVar(&reportHost, "report-host", "", "non-secret stable host identity for authenticated M161 reporting")
		flags.StringVar(&reportURL, "report-url", "", "exact M161 instance URL for non-interactive reporting")
		flags.StringVar(&reportAPIKeyFile, "report-api-key-file", "", "protected owner-only file containing the M161 API key")
		flags.StringVar(&paimosPath, "paimos-path", "", "paimos CLI used for authenticated M161 reporting")
	}
	if command == "start" {
		flags.StringVar(&adapter, "adapter", "codex", "harness adapter")
		flags.StringVar(&workspace, "workspace", "", "absolute child workspace")
		flags.StringVar(&identity, "identity", "", "attributed harness identity")
		flags.Int64Var(&projectID, "project-id", 0, "owning PPM project numeric ID")
		flags.StringVar(&role, "role", "worker", "durable hierarchy role: coordinator or worker")
		flags.StringVar(&parentSessionID, "parent-session", "", "active parent public harness-session UUID")
		flags.Int64Var(&ticketID, "ticket-id", 0, "active project ticket numeric ID")
	}
	if command == "steer" || command == "interrupt" || command == "stop" {
		flags.StringVar(&sessionID, "session", "", "managed agentd session UUID")
		flags.StringVar(&correlationID, "correlation-id", "", "durable message/delivery/control ID")
		flags.StringVar(&identity, "identity", "", "expected attributed harness identity")
		flags.Int64Var(&projectID, "project-id", 0, "expected PPM project numeric ID")
	}
	if err := flags.Parse(args); err != nil {
		return err
	}
	if common.instance == "" {
		return errors.New("--instance is required")
	}
	if command == "start" || command == "steer" || command == "interrupt" || command == "stop" {
		if projectID <= 0 {
			return errors.New("--project-id is required")
		}
		if strings.TrimSpace(identity) == "" {
			return errors.New("--identity is required")
		}
	}
	root, socket, err := resolvePaths(common)
	if err != nil {
		return err
	}
	if command == "serve" {
		reportConfigured := reportHost != "" || reportURL != "" || reportAPIKeyFile != "" || paimosPath != ""
		if reportConfigured && (reportHost == "" || reportURL == "" || reportAPIKeyFile == "") {
			return errors.New("reporting requires --report-host, --report-url, and --report-api-key-file together")
		}
		lock, err := agentd.AcquireInstanceLock(root, common.instance)
		if err != nil {
			return err
		}
		defer lock.Close()
		var reporter agentd.Reporter
		if reportConfigured {
			reporter, err = newCLIReporter(common.instance, root, reportHost, paimosPath, reportURL, reportAPIKeyFile)
			if err != nil {
				return err
			}
		}
		supervisor, err := agentd.NewSupervisor(agentd.SupervisorConfig{Instance: common.instance, StateRoot: root,
			Adapters: serveAdapters(codexPath, claudePath, nodePath, claudeSDKPath), Reporter: reporter})
		if err != nil {
			return err
		}
		ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer cancel()
		defer supervisor.Close(context.Background())
		return agentd.Serve(ctx, socket, supervisor)
	}
	client, err := agentd.NewClient(socket)
	if err != nil {
		return err
	}
	ctx := context.Background()
	var output any
	switch command {
	case "status":
		output, err = client.Status(ctx)
	case "start":
		prompt, readErr := io.ReadAll(io.LimitReader(stdin, (256<<10)+1))
		if readErr != nil || len(prompt) == 0 || len(prompt) > 256<<10 {
			return errors.New("start prompt on stdin is invalid")
		}
		output, err = client.Start(ctx, agentd.StartRequest{
			Adapter: adapter, Workspace: workspace, Identity: identity, ProjectID: projectID, Prompt: string(prompt),
			Role: role, ParentSessionID: parentSessionID, TicketID: ticketID,
		})
	case "steer":
		body, readErr := io.ReadAll(io.LimitReader(stdin, (64<<10)+1))
		if readErr != nil || len(body) == 0 || len(body) > 64<<10 {
			return errors.New("steer text on stdin is invalid")
		}
		output, err = client.Steer(ctx, sessionID, agentd.ControlRequest{
			Instance: common.instance, ProjectID: projectID, Identity: identity, CorrelationID: correlationID, Text: string(body),
		})
	case "interrupt":
		output, err = client.Interrupt(ctx, sessionID, agentd.ControlRequest{
			Instance: common.instance, ProjectID: projectID, Identity: identity, CorrelationID: correlationID,
		})
	case "stop":
		output, err = client.Stop(ctx, sessionID, agentd.ControlRequest{
			Instance: common.instance, ProjectID: projectID, Identity: identity, CorrelationID: correlationID,
		})
	default:
		return errors.New("command must be serve, start, status, steer, interrupt, stop, or version")
	}
	if err != nil {
		return err
	}
	return json.NewEncoder(stdout).Encode(output)
}

func serveAdapters(codexPath, claudePath, nodePath, claudeSDKPath string) []agentd.Adapter {
	return []agentd.Adapter{
		agentd.NewCodexAdapter(codexPath, Version),
		agentd.NewClaudeAdapter(claudePath, nodePath, claudeSDKPath),
	}
}

func addCommonFlags(flags *flag.FlagSet) *commonFlags {
	cache, _ := os.UserCacheDir()
	common := &commonFlags{}
	flags.StringVar(&common.instance, "instance", "", "PPM instance name or canonical URL (required)")
	flags.StringVar(&common.stateRoot, "state-root", filepath.Join(cache, "paimos", "agentd"), "private agentd state root")
	flags.StringVar(&common.socket, "socket", "", "private Unix socket override")
	return common
}

func resolvePaths(common *commonFlags) (string, string, error) {
	root, err := filepath.Abs(common.stateRoot)
	if err != nil {
		return "", "", err
	}
	dir, err := agentd.InstanceStateDir(root, common.instance)
	if err != nil {
		return "", "", err
	}
	socket := common.socket
	if socket == "" {
		socket = filepath.Join(dir, "agentd.sock")
	}
	if !filepath.IsAbs(socket) {
		return "", "", errors.New("--socket must be absolute")
	}
	return root, socket, nil
}
