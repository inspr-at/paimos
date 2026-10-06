// SPDX-License-Identifier: AGPL-3.0-only

package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/inspr-at/paimos/internal/agentd"
	"github.com/inspr-at/paimos/internal/client"
)

type runAgentOptions struct {
	Project, RepoRoot, Exec, ActionKey, TestExec          string
	Accounts, StateRoot, DaemonID                         string
	PermissionMode, AllowedTools                          string
	Yes, AllowDeploy, YesDeploy, AttachLogs, UnsafeBypass bool
	DeployExec                                            string
	ExecutionTimeout, HeartbeatTimeout, HeartbeatInterval string
}

func (rt *runtime) cmdRunAgent() *Command {
	return &Command{Name: "run-agent", Short: "Local runner for Aeon agent runs", Use: "run-agent <watch>", subs: []*Command{rt.cmdRunAgentWatch()}}
}

func (rt *runtime) cmdRunAgentWatch() *Command {
	o := runAgentOptions{Exec: "claude", PermissionMode: "dontAsk", AllowedTools: "Read,Glob,Grep,Edit,Write",
		ExecutionTimeout: "2h", HeartbeatTimeout: "5m", HeartbeatInterval: "15s"}
	return &Command{Name: "watch", Short: "Watch one project's work orders and execute queued Claude runs locally", Use: "run-agent watch --project KEY [--repo-root PATH]", addFlags: func(fs *flagSet) {
		fs.string(&o.Project, "project", 'p', "project key or id (required)")
		fs.string(&o.RepoRoot, "repo-root", 0, "repository the runner operates in (default cwd)")
		fs.string(&o.Exec, "exec", 0, "Claude Code command, or explicit raw command")
		fs.string(&o.ActionKey, "action-key", 0, "provider action (claude_cli.implement)")
		fs.string(&o.TestExec, "test-exec", 0, "optional test command after the agent")
		fs.bool(&o.Yes, "yes", 0, "skip per-job confirmation")
		fs.bool(&o.AllowDeploy, "allow-deploy", 0, "reserved; this runner reports back only")
		fs.string(&o.DeployExec, "deploy-exec", 0, "reserved; this runner reports back only")
		fs.bool(&o.YesDeploy, "yes-deploy", 0, "reserved; this runner reports back only")
		fs.bool(&o.AttachLogs, "attach-logs", 0, "attach bounded child output as work evidence")
		fs.string(&o.ExecutionTimeout, "execution-timeout", 0, "maximum child lifetime")
		fs.string(&o.HeartbeatTimeout, "heartbeat-timeout", 0, "terminate an idle child")
		fs.string(&o.HeartbeatInterval, "heartbeat-interval", 0, "run liveness report interval")
		fs.string(&o.PermissionMode, "claude-permission-mode", 0, "Claude permission mode")
		fs.string(&o.AllowedTools, "claude-allowed-tools", 0, "Claude tool availability and approval allowlist")
		fs.bool(&o.UnsafeBypass, "unsafe-allow-bypass-permissions", 0, "explicit bypassPermissions opt-in")
		fs.string(&o.Accounts, "accounts", 0, "private Aeon agentd account registry")
		fs.string(&o.StateRoot, "state-root", 0, "private local runner state")
		fs.string(&o.DaemonID, "daemon-id", 0, "registered local daemon ID")
	}, run: func([]string) error { return rt.watchAgent(o) }}
}

type localAccount struct {
	Harness   string          `json:"harness"`
	Key       string          `json:"key"`
	AccountID string          `json:"account_id"`
	Home      string          `json:"home,omitempty"`
	Identity  string          `json:"identity,omitempty"`
	Grok      json.RawMessage `json:"grok,omitempty"`
}

func loadLocalAccounts(path string) ([]localAccount, error) {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || info.Size() > 64<<10 {
		return nil, errors.New("private account registry unavailable")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, errors.New("private account registry unavailable")
	}
	var registry struct {
		Accounts []localAccount `json:"accounts"`
	}
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	if dec.Decode(&registry) != nil || dec.Decode(&struct{}{}) != io.EOF {
		return nil, errors.New("account registry invalid")
	}
	return registry.Accounts, nil
}

func (rt *runtime) watchAgent(o runAgentOptions) error {
	if strings.TrimSpace(o.Project) == "" {
		return usagef("--project is required")
	}
	if o.AllowDeploy || o.YesDeploy || o.DeployExec != "" {
		return usagef("run-agent watch is report-back only")
	}
	if o.ActionKey != "" && o.ActionKey != "claude_cli.implement" {
		return usagef("unsupported --action-key %s", o.ActionKey)
	}
	maxRun, err := time.ParseDuration(o.ExecutionTimeout)
	if err != nil || maxRun < time.Second || maxRun > 24*time.Hour {
		return usagef("invalid --execution-timeout")
	}
	idle, err := time.ParseDuration(o.HeartbeatTimeout)
	if err != nil || idle < time.Second {
		return usagef("invalid --heartbeat-timeout")
	}
	heartbeat, err := time.ParseDuration(o.HeartbeatInterval)
	if err != nil || heartbeat < time.Second || heartbeat > time.Minute {
		return usagef("invalid --heartbeat-interval")
	}
	if err := validateLocalClaudeConfig(o); err != nil {
		return err
	}
	if o.RepoRoot == "" {
		o.RepoRoot, err = os.Getwd()
		if err != nil {
			return rt.fail(err, "")
		}
	}
	o.RepoRoot, err = filepath.Abs(o.RepoRoot)
	if err != nil {
		return rt.fail(err, "")
	}
	o.RepoRoot, err = filepath.EvalSymlinks(o.RepoRoot)
	if err != nil {
		return rt.fail(err, "")
	}
	info, err := os.Stat(o.RepoRoot)
	if err != nil || !info.IsDir() {
		return usagef("--repo-root must be a directory")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return rt.fail(err, "")
	}
	if o.Accounts == "" {
		o.Accounts = filepath.Join(home, ".aeon", "agentd", "accounts.json")
	}
	if o.StateRoot == "" {
		o.StateRoot = filepath.Join(home, ".aeon", "run-agent")
	}
	accounts, err := loadLocalAccounts(o.Accounts)
	if err != nil {
		return rt.fail(err, "")
	}
	local := []agentd.EnrolledAccount{}
	homes := map[string]string{}
	for _, account := range accounts {
		if account.Harness != agentd.Claude {
			continue
		}
		if account.Key == "" || !validUUID(account.AccountID) {
			return usagef("invalid Claude account enrollment")
		}
		local = append(local, agentd.EnrolledAccount{ID: account.AccountID, Key: account.Key, Harness: agentd.Claude})
		homes[account.Key] = account.Home
	}
	if len(local) == 0 {
		return rt.fail(errors.New("no Claude account enrolled in local registry"), "")
	}
	if o.DaemonID == "" {
		o.DaemonID = "paimos-run-agent"
	}
	if err := os.MkdirAll(o.StateRoot, 0700); err != nil {
		return rt.fail(err, "")
	}
	stateInfo, err := os.Lstat(o.StateRoot)
	if err != nil || !stateInfo.IsDir() || stateInfo.Mode().Perm() != 0700 {
		return rt.fail(errors.New("state root must be a private directory"), "")
	}
	inst, err := rt.resolve()
	if err != nil {
		return err
	}
	if err := agentd.ValidateBaseURL(inst.URL); err != nil {
		return usagef("invalid Aeon instance URL")
	}
	project, err := rt.projectNode(o.Project)
	if err != nil {
		return err
	}
	remote := agentd.NewRemote(inst.URL, inst.APIKey)
	scoped := &projectRunAPI{API: remote, client: remote.Client, projectID: project.ID}
	adapter := &localClaudeAdapter{opts: o, homes: homes, stdout: rt.stdout, stderr: rt.stderr, stdin: rt.stdin, projectID: project.ID, idle: idle, maxRun: maxRun}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	supervisor, err := agentd.NewSupervisor(ctx, agentd.Config{API: scoped, StateRoot: o.StateRoot, DaemonID: o.DaemonID,
		Workspace: o.RepoRoot, Adapters: []agentd.Adapter{adapter}, Accounts: local,
		EstimatedUnits: map[string]int64{"requests": 1}, HeartbeatInterval: heartbeat, MaxRunDuration: maxRun})
	if err != nil {
		return rt.fail(err, inst.APIKey)
	}
	defer supervisor.Close(context.Background())
	fmt.Fprintf(rt.stdout, "watching %s for Aeon work orders\n", o.Project)
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		pollCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
		err := supervisor.PollOnce(pollCtx)
		cancel()
		if err != nil && ctx.Err() == nil {
			fmt.Fprintln(rt.stderr, "run-agent: poll failed; retrying")
		}
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}

type projectRunAPI struct {
	agentd.API
	client    *client.Client
	projectID string
}

// ClaimHandoff preserves the optional assignment capability through the
// project queue wrapper. Never fall back to a claim without pickup evidence.
func (api *projectRunAPI) ClaimHandoff(ctx context.Context, runID, daemonID, generation string, reservations []string, pickup agentd.WorkerPickup) error {
	claim, ok := api.API.(interface {
		ClaimHandoff(context.Context, string, string, string, []string, agentd.WorkerPickup) error
	})
	if !ok {
		return agentd.ErrUnsupported
	}
	return claim.ClaimHandoff(ctx, runID, daemonID, generation, reservations, pickup)
}

func (api *projectRunAPI) Queued(ctx context.Context) ([]agentd.Run, error) {
	runs, err := api.API.Queued(ctx)
	if err != nil {
		return nil, err
	}
	profiles, err := api.API.Profiles(ctx)
	if err != nil {
		return nil, err
	}
	claude := map[string]bool{}
	for _, profile := range profiles {
		if profile.Harness == agentd.Claude {
			claude[profile.ID] = true
		}
	}
	selected := make([]agentd.Run, 0, len(runs))
	for _, run := range runs {
		if !claude[run.ModelProfileID] {
			continue
		}
		inside, err := api.withinProject(ctx, run.WorkOrderID)
		if err != nil {
			return nil, err
		}
		if inside {
			selected = append(selected, run)
		}
	}
	return selected, nil
}

func (api *projectRunAPI) withinProject(ctx context.Context, id string) (bool, error) {
	seen := map[string]bool{}
	for i := 0; i < 32 && id != ""; i++ {
		if id == api.projectID {
			return true, nil
		}
		if !validUUID(id) || seen[id] {
			return false, errors.New("invalid work-order ancestry")
		}
		seen[id] = true
		var node struct {
			ParentID *string `json:"parent_id"`
		}
		if err := api.client.Do(ctx, http.MethodGet, "/api/nodes/"+url.PathEscape(id), nil, &node); err != nil {
			return false, err
		}
		if node.ParentID == nil {
			return false, nil
		}
		id = *node.ParentID
	}
	return false, errors.New("work-order ancestry exceeds depth")
}
