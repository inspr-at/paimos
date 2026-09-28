// SPDX-License-Identifier: AGPL-3.0-only

package cli

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

const (
	heartbeatSchema       = "aeon.harness-heartbeat.v1"
	heartbeatInterval     = 50
	heartbeatMaxCommits   = 20
	heartbeatMaxRemember  = 100
	heartbeatMaxTokens    = 1_000_000_000_000
	heartbeatIndexMax     = 65536
	heartbeatTitleLineMax = 4096
	heartbeatUsageLineMax = 1 << 20
	heartbeatNoteMax      = 120
)

var (
	errOwnerExited   = errors.New("owner exited")
	heartbeatModelRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/-]*$`)
	heartbeatPhases  = map[string]bool{"starting": true, "working": true, "yielded": true, "stopping": true}
	heartbeatActs    = map[string]bool{"busy": true, "idle": true, "throttled": true}
)

// heartbeatDeps replaces clocks, liveness and name lookup in tests.
type heartbeatDeps struct {
	alive   func(pid int) bool
	wait    func(ctx context.Context, pid int, interval time.Duration) error
	commits func(worktree, since string) ([]heartbeatCommit, error)
	label   func() (string, bool)
}

type heartbeatOptions struct {
	OwnerPID       int
	Interval       int
	StateDir       string
	Project        string
	Agent          string
	Harness        string
	Host           string
	Label          string
	Model          string
	Effort         string
	AccountLabel   string
	Brief          string
	Worktree       string
	Branch         string
	Note           string
	Phase          string
	Activity       string
	Parent         string
	Ticket         string
	Shape          string
	Management     string
	Role           string
	SourceSession  string
	CodexIndex     string
	ClaudeProjects string
	Transcript     string
	PrintControls  bool
}

type heartbeatCommit struct {
	SHA     string
	Subject string
}

type heartbeatDisk struct {
	Schema      string               `json:"schema"`
	SessionID   string               `json:"session_id"`
	Sequence    int64                `json:"sequence"`
	LabelSent   bool                 `json:"label_sent"`
	SentLabel   string               `json:"sent_label"`
	SentCommits []string             `json:"sent_commits"`
	StartRev    string               `json:"start_rev"`
	Usage       []heartbeatUsageDisk `json:"usage,omitempty"`
}

type heartbeatUsageDisk struct {
	Model    string `json:"model"`
	Sequence int64  `json:"sequence"`
	Input    int64  `json:"input"`
	Output   int64  `json:"output"`
	Cached   int64  `json:"cached"`
}

type heartbeatSession struct {
	id    string
	lease string
	disk  heartbeatDisk
}

func (rt *runtime) harnessRunHeartbeat() *Command {
	var o heartbeatOptions
	o.Interval = heartbeatInterval
	o.Phase = "working"
	o.Activity = "busy"
	return &Command{
		Name:  "run-heartbeat",
		Short: "Heartbeat while an owner process lives, then mark the session stopped",
		Use:   "harness run-heartbeat --owner-pid PID --state-dir DIR --project KEY --agent NAME --harness KIND",
		addFlags: func(fs *flagSet) {
			fs.int(&o.OwnerPID, "owner-pid", "process id whose exit stops the session")
			fs.int(&o.Interval, "interval", "seconds between beats (default 50)")
			fs.string(&o.StateDir, "state-dir", 0, "private directory for registration and resume")
			fs.string(&o.Project, "project", 'p', "project key")
			fs.string(&o.Agent, "agent", 0, "authenticated agent name")
			fs.string(&o.Harness, "harness", 0, "adapter family")
			fs.string(&o.Host, "host", 0, "non-secret host label")
			fs.string(&o.Label, "label", 0, "session display label used until a name source has one")
			fs.string(&o.Model, "model", 0, "model name, or AEON_MODEL when omitted")
			fs.string(&o.Effort, "effort", 0, "reasoning effort, or AEON_EFFORT when omitted")
			fs.string(&o.AccountLabel, "account-label", 0, "subscription or account display name (never a credential)")
			fs.string(&o.Brief, "brief", 0, "short prompt file name or ticket key")
			fs.string(&o.Worktree, "worktree", 0, "worktree path")
			fs.string(&o.Branch, "branch", 0, "branch name")
			fs.string(&o.Note, "note", 0, "current step, at most 120 characters")
			fs.string(&o.Phase, "phase", 0, "starting, working, yielded or stopping")
			fs.string(&o.Activity, "activity", 0, "busy, idle or throttled")
			fs.string(&o.Parent, "parent-session", 0, "parent public session UUID")
			fs.string(&o.Ticket, "ticket", 0, "ticket node key")
			fs.string(&o.Shape, "work-shape", 0, "ship or scout")
			fs.string(&o.Management, "management", 0, "managed or unmanaged")
			fs.string(&o.Role, "role", 0, "worker or coordinator")
			fs.string(&o.SourceSession, "source-session", 0, "harness session UUID for the name source")
			fs.string(&o.CodexIndex, "codex-index", 0, "Codex session_index.jsonl (default ~/.codex/session_index.jsonl)")
			fs.string(&o.ClaudeProjects, "claude-projects", 0, "Claude Code projects directory (default ~/.claude/projects)")
			fs.string(&o.Transcript, "transcript", 0, "Claude Code session transcript JSONL for usage and its title")
			fs.bool(&o.PrintControls, "print-controls", 0, "print one line per pending control or inbox message")
		},
		run: func([]string) error {
			if err := o.prepare(); err != nil {
				return err
			}
			ctx, stop := signalContext()
			defer stop()
			return rt.runHeartbeat(ctx, o, heartbeatDeps{})
		},
	}
}

func (o *heartbeatOptions) prepare() error {
	if o.OwnerPID <= 0 {
		return usagef("--owner-pid must be a positive process id")
	}
	if strings.TrimSpace(o.StateDir) == "" {
		return usagef("--state-dir is required")
	}
	if strings.TrimSpace(o.Project) == "" || !agentNameRE.MatchString(o.Agent) || !modelHarnesses[o.Harness] {
		return usagef("--project, --agent and --harness are required")
	}
	if o.Interval < 1 || o.Interval > 3600 {
		return usagef("--interval must be 1-3600 seconds")
	}
	if o.Phase == "" {
		o.Phase = "working"
	}
	if !heartbeatPhases[o.Phase] {
		return usagef("invalid --phase")
	}
	if o.Activity == "" {
		o.Activity = "busy"
	}
	if heartbeatText(o.Activity, 40) == "" {
		o.Activity = ""
	} else if !heartbeatActs[o.Activity] {
		return usagef("invalid --activity")
	}
	if o.SourceSession != "" && !validUUID(o.SourceSession) {
		return usagef("--source-session must be a UUID")
	}
	if o.Parent != "" && !validUUID(o.Parent) {
		return usagef("invalid parent session")
	}
	if o.Management == "" {
		o.Management = "unmanaged"
	}
	if o.Role == "" {
		o.Role = "worker"
	}
	if o.Management != "managed" && o.Management != "unmanaged" || o.Role != "worker" && o.Role != "coordinator" {
		return usagef("invalid management or role")
	}
	if o.Host == "" {
		host, err := os.Hostname()
		if err != nil || heartbeatText(host, 200) == "" {
			return usagef("--host is required")
		}
		if i := strings.IndexByte(host, '.'); i > 0 {
			host = host[:i]
		}
		o.Host = host
	}
	if heartbeatText(o.Host, 200) == "" {
		return usagef("--host is required")
	}
	if o.CodexIndex == "" {
		if home, err := os.UserHomeDir(); err == nil {
			o.CodexIndex = filepath.Join(home, ".codex", "session_index.jsonl")
		}
	}
	if o.ClaudeProjects == "" {
		if home, err := os.UserHomeDir(); err == nil {
			o.ClaudeProjects = filepath.Join(home, ".claude", "projects")
		}
	}
	if o.Model == "" {
		o.Model = os.Getenv("AEON_MODEL")
	}
	if o.Effort == "" {
		o.Effort = os.Getenv("AEON_EFFORT")
	}
	return nil
}

func (rt *runtime) runHeartbeat(ctx context.Context, o heartbeatOptions, dep heartbeatDeps) error {
	if o.Management == "" {
		o.Management = "unmanaged"
	}
	if o.Role == "" {
		o.Role = "worker"
	}
	if o.Phase == "" {
		o.Phase = "working"
	}
	if o.Interval <= 0 {
		o.Interval = heartbeatInterval
	}
	if strings.TrimSpace(o.Model) == "" {
		o.Model = os.Getenv("AEON_MODEL")
	}
	if strings.TrimSpace(o.Effort) == "" {
		o.Effort = os.Getenv("AEON_EFFORT")
	}
	if dep.alive == nil {
		dep.alive = processAlive
	}
	if dep.wait == nil {
		dep.wait = func(ctx context.Context, pid int, interval time.Duration) error {
			return waitHeartbeat(ctx, pid, dep.alive, interval)
		}
	}
	session, created, err := rt.openHeartbeatSession(o, dep)
	if err != nil {
		return err
	}
	if created {
		if err := saveHeartbeatSession(o.StateDir, session); err != nil {
			if _, statErr := os.Stat(filepath.Join(o.StateDir, "session.id")); statErr != nil {
				_ = rt.stopHeartbeat(o.Project, session)
			}
			return err
		}
	}
	interval := time.Duration(o.Interval) * time.Second
	for {
		if ctx.Err() != nil || !dep.alive(o.OwnerPID) {
			return rt.stopHeartbeat(o.Project, session)
		}
		if err := rt.heartbeatBeat(o, dep, &session); err != nil {
			fmt.Fprintf(rt.stderr, "heartbeat: beat failed: %s\n", err.Error())
		} else if err := saveHeartbeatSession(o.StateDir, session); err != nil {
			fmt.Fprintf(rt.stderr, "heartbeat: state save failed\n")
		}
		err := dep.wait(ctx, o.OwnerPID, interval)
		if err != nil {
			return rt.stopHeartbeat(o.Project, session)
		}
	}
}

func waitHeartbeat(ctx context.Context, pid int, alive func(int) bool, interval time.Duration) error {
	timer := time.NewTimer(interval)
	defer timer.Stop()
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
			if !alive(pid) {
				return errOwnerExited
			}
			return nil
		case <-ticker.C:
			if !alive(pid) {
				return errOwnerExited
			}
		}
	}
}

func (rt *runtime) openHeartbeatSession(o heartbeatOptions, dep heartbeatDeps) (heartbeatSession, bool, error) {
	if err := os.MkdirAll(o.StateDir, 0o700); err != nil {
		return heartbeatSession{}, false, err
	}
	existing, ok, err := loadHeartbeatSession(o.StateDir)
	if err != nil {
		return heartbeatSession{}, false, err
	}
	if ok {
		if existing.disk.StartRev == "" {
			existing.disk.StartRev, _ = gitHEAD(o.Worktree)
		}
		return existing, false, nil
	}
	projectID, err := rt.harnessProject(o.Project)
	if err != nil {
		return heartbeatSession{}, false, err
	}
	me, err := rt.caller()
	if err != nil {
		return heartbeatSession{}, false, err
	}
	if me.Principal.Name != o.Agent {
		return heartbeatSession{}, false, usagef("--agent must name the authenticated agent")
	}
	lease, err := readOrCreateSecret(filepath.Join(o.StateDir, "lease.key"), 32)
	if err != nil {
		return heartbeatSession{}, false, err
	}
	ref, err := readOrCreateSecret(filepath.Join(o.StateDir, "session.ref"), 24)
	if err != nil {
		return heartbeatSession{}, false, err
	}
	label, haveLabel := resolveHeartbeatLabel(o, dep, true)
	body := map[string]any{
		"agent_principal_id":      me.Principal.ID,
		"harness":                 o.Harness,
		"host":                    heartbeatText(o.Host, 200),
		"harness_session_ref":     ref,
		"worker_lease":            lease,
		"management_mode":         o.Management,
		"role":                    o.Role,
		"advertised_capabilities": []string{"status"},
	}
	putText(body, "display_label", label, haveLabel)
	putText(body, "model", heartbeatText(o.Model, 128), true)
	putText(body, "reasoning_effort", heartbeatText(o.Effort, 40), true)
	putText(body, "account_label", heartbeatText(o.AccountLabel, 128), true)
	putText(body, "brief", heartbeatText(o.Brief, 240), true)
	putText(body, "worktree", heartbeatText(o.Worktree, 512), true)
	putText(body, "branch", heartbeatText(o.Branch, 200), true)
	if o.Parent != "" {
		parent := strings.ToLower(o.Parent)
		body["parent_harness_session_id"] = parent
	}
	if o.Ticket != "" {
		ticketID, err := rt.harnessTicket(projectID, o.Ticket, 0)
		if err != nil {
			return heartbeatSession{}, false, err
		}
		if ticketID == nil {
			return heartbeatSession{}, false, usagef("--ticket was not found")
		}
		if o.Shape != "ship" && o.Shape != "scout" {
			return heartbeatSession{}, false, usagef("--work-shape must be ship or scout")
		}
		body["ticket_node_id"] = *ticketID
		body["work_shape"] = o.Shape
	}
	var out struct {
		ID string `json:"id"`
	}
	if err := rt.harnessDo(http.MethodPost, harnessPath(projectID, ""), "", body, &out); err != nil {
		return heartbeatSession{}, false, err
	}
	if !validUUID(out.ID) {
		return heartbeatSession{}, false, errors.New("registration did not return a session id")
	}
	start, _ := gitHEAD(o.Worktree)
	disk := heartbeatDisk{Schema: heartbeatSchema, SessionID: strings.ToLower(out.ID), StartRev: start}
	if haveLabel {
		disk.LabelSent = true
		disk.SentLabel = label
	}
	return heartbeatSession{id: disk.SessionID, lease: lease, disk: disk}, true, nil
}

func (rt *runtime) heartbeatBeat(o heartbeatOptions, dep heartbeatDeps, session *heartbeatSession) error {
	projectID, err := rt.harnessProject(o.Project)
	if err != nil {
		return err
	}
	session.disk.Sequence++
	body := map[string]any{
		"phase":             o.Phase,
		"activity_sequence": session.disk.Sequence,
	}
	activity := o.Activity
	if activity == "" {
		activity = "busy"
	}
	body["activity"] = activity
	if label, ok := resolveHeartbeatLabel(o, dep, false); ok && (!session.disk.LabelSent || label != session.disk.SentLabel) {
		body["display_label"] = label
	}
	putText(body, "model", heartbeatText(o.Model, 128), true)
	putText(body, "reasoning_effort", heartbeatText(o.Effort, 40), true)
	putText(body, "account_label", heartbeatText(o.AccountLabel, 128), true)
	putText(body, "brief", heartbeatText(o.Brief, 240), true)
	putText(body, "worktree", heartbeatText(o.Worktree, 512), true)
	putText(body, "branch", heartbeatText(o.Branch, 200), true)
	note := heartbeatNote(o.Note)
	if note == "" {
		note = agentStatusNote(o.Worktree)
	}
	if note != "" {
		body["activity_note"] = note
	}
	commits := heartbeatCommits(o, dep, session.disk)
	if len(commits) > 0 {
		items := make([]map[string]string, 0, len(commits))
		for _, c := range commits {
			items = append(items, map[string]string{"sha": c.SHA, "subject": c.Subject})
		}
		body["commits"] = items
	}
	path := harnessPath(projectID, session.id) + "/heartbeat"
	err = rt.harnessDo(http.MethodPost, path, session.lease, body, new(any))
	if err != nil && strings.Contains(err.Error(), "api 409") {
		var status struct {
			ActivitySequence int64 `json:"activity_sequence"`
		}
		if readErr := rt.harnessDo(http.MethodGet, harnessPath(projectID, session.id), "", nil, &status); readErr == nil && status.ActivitySequence >= session.disk.Sequence {
			session.disk.Sequence = status.ActivitySequence + 1
			body["activity_sequence"] = session.disk.Sequence
			err = rt.harnessDo(http.MethodPost, path, session.lease, body, new(any))
		}
	}
	if err != nil {
		session.disk.Sequence--
		return err
	}
	if label, ok := body["display_label"].(string); ok {
		session.disk.LabelSent = true
		session.disk.SentLabel = label
	}
	rememberCommits(&session.disk, commits)
	if o.Transcript != "" {
		if uerr := rt.reportHeartbeatUsage(projectID, o, session); uerr != nil {
			fmt.Fprintf(rt.stderr, "heartbeat: usage report failed\n")
		}
	}
	if o.PrintControls {
		rt.printHeartbeatControls(projectID, session.id)
	}
	return nil
}

func (rt *runtime) stopHeartbeat(project string, session heartbeatSession) error {
	if session.id == "" || session.lease == "" {
		return nil
	}
	projectID, err := rt.harnessProject(project)
	if err != nil {
		return err
	}
	return rt.harnessDo(http.MethodPost, harnessPath(projectID, session.id)+"/stop", session.lease, map[string]string{"reason": "stopped"}, new(any))
}

func (rt *runtime) reportHeartbeatUsage(projectID string, o heartbeatOptions, session *heartbeatSession) error {
	sums, err := claudeUsage(o.Transcript, heartbeatText(o.Model, 128))
	if err != nil || len(sums) == 0 {
		return err
	}
	models := make([]string, 0, len(sums))
	for model := range sums {
		models = append(models, model)
	}
	slices.Sort(models)
	for _, model := range models {
		sum := sums[model]
		prev := usageByModel(session.disk.Usage, model)
		if prev != nil && prev.Input == sum.input && prev.Output == sum.output && prev.Cached == sum.cached {
			continue
		}
		if prev != nil && (sum.input < prev.Input || sum.output < prev.Output || sum.cached < prev.Cached) {
			continue
		}
		seq := int64(1)
		if prev != nil {
			seq = prev.Sequence + 1
		}
		provisional := true
		report := map[string]any{
			"report_id":           usageReportID(session.id, model, seq, sum.input, sum.output, sum.cached),
			"model":               model,
			"sequence":            seq,
			"input_tokens":        sum.input,
			"output_tokens":       sum.output,
			"cached_input_tokens": sum.cached,
			"provisional":         provisional,
			// billing_mode is the usage schema's required enum, not a display label.
			"billing_mode": "unknown",
		}
		if err := rt.harnessDo(http.MethodPost, harnessPath(projectID, session.id)+"/usage", session.lease, report, new(any)); err != nil {
			return err
		}
		next := heartbeatUsageDisk{Model: model, Sequence: seq, Input: sum.input, Output: sum.output, Cached: sum.cached}
		if prev == nil {
			session.disk.Usage = append(session.disk.Usage, next)
		} else {
			*prev = next
		}
	}
	return nil
}

func (rt *runtime) printHeartbeatControls(projectID, sessionID string) {
	var status struct {
		Controls []struct {
			ID    string `json:"id"`
			Kind  string `json:"kind"`
			State string `json:"state"`
		} `json:"controls"`
	}
	if err := rt.harnessDo(http.MethodGet, harnessPath(projectID, sessionID), "", nil, &status); err == nil {
		for _, c := range status.Controls {
			if c.State == "" || c.State == "completed" || !validUUID(c.ID) {
				continue
			}
			kind := heartbeatText(c.Kind, 40)
			state := heartbeatText(c.State, 40)
			if kind == "" || state == "" {
				continue
			}
			fmt.Fprintf(rt.stdout, "control %s %s %s\n", strings.ToLower(c.ID), kind, state)
		}
	}
	var page struct {
		Items []struct {
			ID        string  `json:"id"`
			AckedAt   *string `json:"acked_at"`
			SessionID string  `json:"recipient_session_id"`
		} `json:"items"`
	}
	if err := rt.do(http.MethodGet, "/api/inbox/messages?wait_ms=0", nil, &page); err != nil {
		return
	}
	for _, item := range page.Items {
		if item.AckedAt != nil && *item.AckedAt != "" {
			continue
		}
		if item.SessionID != "" && !strings.EqualFold(item.SessionID, sessionID) {
			continue
		}
		if !validUUID(item.ID) {
			continue
		}
		fmt.Fprintf(rt.stdout, "message %s\n", strings.ToLower(item.ID))
	}
}

func resolveHeartbeatLabel(o heartbeatOptions, dep heartbeatDeps, includeFlag bool) (string, bool) {
	if dep.label != nil {
		label, ok := dep.label()
		if !ok {
			return "", false
		}
		label = heartbeatText(label, 128)
		return label, label != ""
	}
	if label, ok := readHeartbeatNames(o); ok {
		return label, true
	}
	if includeFlag {
		if label := heartbeatText(o.Label, 128); label != "" {
			return label, true
		}
	}
	return "", false
}

func readHeartbeatNames(o heartbeatOptions) (string, bool) {
	id := o.sourceID()
	codex := func() (string, bool) { return codexSessionLabel(o.CodexIndex, id) }
	claude := func() (string, bool) { return claudeSessionLabel(o, id) }
	switch o.Harness {
	case "codex":
		if label, ok := codex(); ok {
			return label, true
		}
		return claude()
	case "claude":
		if label, ok := claude(); ok {
			return label, true
		}
		return codex()
	default:
		if label, ok := codex(); ok {
			return label, true
		}
		return claude()
	}
}

func (o heartbeatOptions) sourceID() string {
	if validUUID(o.SourceSession) {
		return strings.ToLower(o.SourceSession)
	}
	base := strings.TrimSuffix(filepath.Base(o.Transcript), ".jsonl")
	if validUUID(base) {
		return strings.ToLower(base)
	}
	return ""
}

func codexSessionLabel(path, sessionID string) (string, bool) {
	if path == "" || !validUUID(sessionID) || unsafeHeartbeatPath(path) {
		return "", false
	}
	f, err := openNoFollow(path)
	if err != nil {
		return "", false
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil || st.Size() > heartbeatIndexMax {
		return "", false
	}
	raw, err := io.ReadAll(io.LimitReader(f, heartbeatIndexMax+1))
	if err != nil || len(raw) > heartbeatIndexMax {
		return "", false
	}
	var found string
	var matched bool
	for _, line := range bytes.Split(raw, []byte("\n")) {
		line = bytes.TrimSpace(line)
		if len(line) == 0 || len(line) > heartbeatTitleLineMax {
			continue
		}
		var rec struct {
			ID         string `json:"id"`
			ThreadName string `json:"thread_name"`
		}
		if json.Unmarshal(line, &rec) != nil || !strings.EqualFold(rec.ID, sessionID) {
			continue
		}
		matched = true
		found = heartbeatText(rec.ThreadName, 128)
	}
	if !matched || found == "" {
		return "", false
	}
	return found, true
}

func claudeSessionLabel(o heartbeatOptions, sessionID string) (string, bool) {
	if label, ok := claudeTitleFile(o.Transcript); ok {
		return label, true
	}
	path := findClaudeTranscript(o.ClaudeProjects, sessionID)
	return claudeTitleFile(path)
}

func claudeTitleFile(path string) (string, bool) {
	if path == "" || unsafeHeartbeatPath(path) {
		return "", false
	}
	var custom, ai string
	err := scanJSONL(path, heartbeatTitleLineMax, func(line []byte) error {
		if !bytes.Contains(line, []byte("Title")) && !bytes.Contains(line, []byte("title")) {
			return nil
		}
		var rec struct {
			Type        string `json:"type"`
			CustomTitle string `json:"customTitle"`
			AITitle     string `json:"aiTitle"`
		}
		if json.Unmarshal(line, &rec) != nil {
			return nil
		}
		switch rec.Type {
		case "custom-title":
			if label := heartbeatText(rec.CustomTitle, 128); label != "" {
				custom = label
			}
		case "ai-title":
			if label := heartbeatText(rec.AITitle, 128); label != "" {
				ai = label
			}
		}
		return nil
	})
	if err != nil {
		return "", false
	}
	if custom != "" {
		return custom, true
	}
	if ai != "" {
		return ai, true
	}
	return "", false
}

func findClaudeTranscript(root, sessionID string) string {
	if root == "" || !validUUID(sessionID) || unsafeHeartbeatPath(root) {
		return ""
	}
	name := strings.ToLower(sessionID) + ".jsonl"
	entries, err := os.ReadDir(root)
	if err != nil {
		return ""
	}
	for _, entry := range entries {
		info, err := entry.Info()
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			continue
		}
		candidate := filepath.Join(root, entry.Name(), name)
		st, err := os.Lstat(candidate)
		if err != nil || !st.Mode().IsRegular() || st.Mode()&os.ModeSymlink != 0 {
			continue
		}
		return candidate
	}
	return ""
}

type usageSum struct {
	input, output, cached int64
}

func claudeUsage(path, fallbackModel string) (map[string]usageSum, error) {
	if path == "" || unsafeHeartbeatPath(path) {
		return nil, nil
	}
	sums := map[string]usageSum{}
	poisoned := map[string]bool{}
	seen := map[string]bool{}
	err := scanJSONL(path, heartbeatUsageLineMax, func(line []byte) error {
		if !bytes.Contains(line, []byte(`"usage"`)) {
			return nil
		}
		var rec struct {
			UUID    string `json:"uuid"`
			Type    string `json:"type"`
			Message struct {
				Model string                     `json:"model"`
				Usage map[string]json.RawMessage `json:"usage"`
			} `json:"message"`
		}
		if json.Unmarshal(line, &rec) != nil || rec.Type != "assistant" || len(rec.Message.Usage) == 0 {
			return nil
		}
		if rec.UUID != "" {
			if seen[rec.UUID] {
				return nil
			}
			seen[rec.UUID] = true
		}
		input, okIn := jsonToken(rec.Message.Usage["input_tokens"])
		output, okOut := jsonToken(rec.Message.Usage["output_tokens"])
		if !okIn || !okOut {
			return nil
		}
		cached := int64(0)
		if raw, ok := rec.Message.Usage["cache_read_input_tokens"]; ok && len(bytes.TrimSpace(raw)) > 0 && string(raw) != "null" {
			var okCached bool
			cached, okCached = jsonToken(raw)
			if !okCached {
				return nil
			}
		}
		// The usage route treats input as inclusive of cached input.
		inclusive, ok := addTokens(input, cached)
		if !ok {
			return errUsageOverflow
		}
		model := heartbeatText(rec.Message.Model, 128)
		if model == "" {
			model = fallbackModel
		}
		if model == "" || !heartbeatModelRE.MatchString(model) || poisoned[model] {
			return nil
		}
		cur := sums[model]
		nextIn, ok1 := addTokens(cur.input, inclusive)
		nextOut, ok2 := addTokens(cur.output, output)
		nextCached, ok3 := addTokens(cur.cached, cached)
		if !ok1 || !ok2 || !ok3 {
			poisoned[model] = true
			delete(sums, model)
			return nil
		}
		sums[model] = usageSum{input: nextIn, output: nextOut, cached: nextCached}
		return nil
	})
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	return sums, nil
}

var errUsageOverflow = errors.New("usage overflow")

func jsonToken(raw json.RawMessage) (int64, bool) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || string(raw) == "null" {
		return 0, false
	}
	var n int64
	if err := json.Unmarshal(raw, &n); err != nil || n < 0 || n > heartbeatMaxTokens {
		return 0, false
	}
	return n, true
}

func addTokens(a, b int64) (int64, bool) {
	if a < 0 || b < 0 || a > heartbeatMaxTokens-b {
		return 0, false
	}
	return a + b, true
}

func scanJSONL(path string, maxLine int, fn func([]byte) error) error {
	f, err := openNoFollow(path)
	if err != nil {
		return err
	}
	defer f.Close()
	r := bufio.NewReader(f)
	for {
		line, overflow, err := readLimitedLine(r, maxLine)
		if overflow {
			if errors.Is(err, io.EOF) {
				return nil
			}
			if err != nil {
				return err
			}
			continue
		}
		if len(line) > 0 {
			if ferr := fn(line); ferr != nil {
				return ferr
			}
		}
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
	}
}

func readLimitedLine(r *bufio.Reader, maxLine int) ([]byte, bool, error) {
	var line []byte
	overflow := false
	for {
		chunk, err := r.ReadSlice('\n')
		if len(chunk) > 0 && !overflow {
			if len(line)+len(chunk) > maxLine+1 {
				overflow = true
				line = nil
			} else {
				line = append(line, chunk...)
			}
		}
		if errors.Is(err, bufio.ErrBufferFull) {
			continue
		}
		if overflow {
			return nil, true, err
		}
		return bytes.TrimRight(line, "\r\n"), false, err
	}
}

func heartbeatCommits(o heartbeatOptions, dep heartbeatDeps, disk heartbeatDisk) []heartbeatCommit {
	var found []heartbeatCommit
	var err error
	if dep.commits != nil {
		found, err = dep.commits(o.Worktree, disk.StartRev)
	} else {
		found, err = gitCommitsSince(o.Worktree, disk.StartRev)
	}
	if err != nil {
		return nil
	}
	sent := map[string]bool{}
	for _, sha := range disk.SentCommits {
		sent[strings.ToLower(sha)] = true
	}
	return selectHeartbeatCommits(found, sent)
}

func selectHeartbeatCommits(found []heartbeatCommit, sent map[string]bool) []heartbeatCommit {
	out := make([]heartbeatCommit, 0, len(found))
	seen := map[string]bool{}
	for _, c := range found {
		sha := strings.ToLower(c.SHA)
		subject := heartbeatText(c.Subject, 200)
		if subject == "" || !validCommitSHA(sha) || sent[sha] || seen[sha] {
			continue
		}
		seen[sha] = true
		out = append(out, heartbeatCommit{SHA: sha, Subject: subject})
		if len(out) == heartbeatMaxCommits {
			break
		}
	}
	return out
}

func rememberCommits(disk *heartbeatDisk, commits []heartbeatCommit) {
	for _, c := range commits {
		disk.SentCommits = append(disk.SentCommits, c.SHA)
	}
	if len(disk.SentCommits) > heartbeatMaxRemember {
		disk.SentCommits = disk.SentCommits[len(disk.SentCommits)-heartbeatMaxRemember:]
	}
}

func gitHEAD(worktree string) (string, error) {
	if strings.TrimSpace(worktree) == "" {
		return "", nil
	}
	out, err := heartbeatGit(worktree, "rev-parse", "HEAD")
	if err != nil {
		return "", err
	}
	sha := strings.ToLower(strings.TrimSpace(out))
	if !validCommitSHA(sha) {
		return "", errors.New("invalid HEAD")
	}
	return sha, nil
}

func gitCommitsSince(worktree, since string) ([]heartbeatCommit, error) {
	if strings.TrimSpace(worktree) == "" || !validCommitSHA(since) {
		return nil, nil
	}
	out, err := heartbeatGit(worktree, "log", "--reverse", since+"..HEAD", "--format=%H%x1f%s")
	if err != nil {
		return nil, err
	}
	var commits []heartbeatCommit
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		sha, subject, ok := strings.Cut(line, "\x1f")
		if !ok {
			continue
		}
		commits = append(commits, heartbeatCommit{SHA: sha, Subject: subject})
	}
	return commits, nil
}

func heartbeatGit(worktree string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = worktree
	cmd.Stdin = nil
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

func validCommitSHA(sha string) bool {
	if len(sha) < 7 || len(sha) > 40 {
		return false
	}
	for _, r := range sha {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			return false
		}
	}
	return true
}

func loadHeartbeatSession(dir string) (heartbeatSession, bool, error) {
	raw, err := os.ReadFile(filepath.Join(dir, "session.id"))
	if errors.Is(err, os.ErrNotExist) {
		return heartbeatSession{}, false, nil
	}
	if err != nil {
		return heartbeatSession{}, false, err
	}
	id := strings.ToLower(strings.TrimSpace(string(raw)))
	if !validUUID(id) {
		return heartbeatSession{}, false, usagef("state directory has an invalid session id")
	}
	lease, err := readPrivateSecret(filepath.Join(dir, "lease.key"))
	if err != nil {
		return heartbeatSession{}, false, err
	}
	disk := heartbeatDisk{Schema: heartbeatSchema, SessionID: id}
	stateRaw, err := os.ReadFile(filepath.Join(dir, "state.json"))
	if err == nil {
		if len(stateRaw) > 1<<20 {
			return heartbeatSession{}, false, usagef("heartbeat state is too large")
		}
		if json.Unmarshal(stateRaw, &disk) != nil || disk.Schema != heartbeatSchema || !strings.EqualFold(disk.SessionID, id) {
			return heartbeatSession{}, false, usagef("heartbeat state does not match the session id")
		}
		disk.SessionID = id
	} else if !errors.Is(err, os.ErrNotExist) {
		return heartbeatSession{}, false, err
	}
	return heartbeatSession{id: id, lease: lease, disk: disk}, true, nil
}

func saveHeartbeatSession(dir string, session heartbeatSession) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	session.disk.Schema = heartbeatSchema
	session.disk.SessionID = session.id
	raw, err := json.Marshal(session.disk)
	if err != nil {
		return err
	}
	if err := writePrivate(filepath.Join(dir, "session.id"), []byte(session.id+"\n")); err != nil {
		return err
	}
	return writePrivate(filepath.Join(dir, "state.json"), append(raw, '\n'))
}

func readOrCreateSecret(path string, n int) (string, error) {
	if _, err := os.Lstat(path); err == nil {
		return readPrivateSecret(path)
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	value := hex.EncodeToString(buf)
	if err := writePrivate(path, []byte(value+"\n")); err != nil {
		return "", err
	}
	return value, nil
}

func readPrivateSecret(path string) (string, error) {
	// Reuse the harness lease checks: private regular file, one line, long enough.
	st, err := os.Lstat(path)
	if err != nil {
		return "", err
	}
	if !st.Mode().IsRegular() || st.Mode().Perm()&0o077 != 0 || st.Size() > 8192 || st.Mode()&os.ModeSymlink != 0 {
		return "", usagef("%s must be a private regular file", filepath.Base(path))
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	value := strings.TrimSpace(string(raw))
	if len(value) < 32 || strings.ContainsAny(value, "\r\n") {
		return "", usagef("%s must contain one value", filepath.Base(path))
	}
	return value, nil
}

func putText(body map[string]any, key, value string, include bool) {
	if include && value != "" {
		body[key] = value
	}
}

func heartbeatText(raw string, max int) string {
	if !utf8.ValidString(raw) {
		return ""
	}
	clean := strings.TrimSpace(strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, raw))
	if clean == "" || strings.EqualFold(clean, "unknown") {
		return ""
	}
	if utf8.RuneCountInString(clean) > max {
		return ""
	}
	return clean
}

func heartbeatNote(raw string) string {
	clean := heartbeatText(raw, 1<<20)
	if clean == "" {
		return ""
	}
	if utf8.RuneCountInString(clean) <= heartbeatNoteMax {
		return clean
	}
	return string([]rune(clean)[:heartbeatNoteMax])
}

func agentStatusNote(worktree string) string {
	if strings.TrimSpace(worktree) == "" {
		return ""
	}
	path := filepath.Join(worktree, ".agent-status.json")
	st, err := os.Lstat(path)
	if err != nil || !st.Mode().IsRegular() || st.Mode()&os.ModeSymlink != 0 || st.Size() > 65536 {
		return ""
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	var doc struct {
		Note string `json:"note"`
	}
	if json.Unmarshal(raw, &doc) != nil {
		return ""
	}
	return heartbeatNote(doc.Note)
}

func usageByModel(items []heartbeatUsageDisk, model string) *heartbeatUsageDisk {
	for i := range items {
		if items[i].Model == model {
			return &items[i]
		}
	}
	return nil
}

func usageReportID(session, model string, seq, input, output, cached int64) string {
	sum := sha256.Sum256([]byte(fmt.Sprintf("aeon-heartbeat-usage-v1\x00%s\x00%s\x00%d\x00%d\x00%d\x00%d", session, model, seq, input, output, cached)))
	sum[6] = sum[6]&0x0f | 0x50
	sum[8] = sum[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", sum[0:4], sum[4:6], sum[6:8], sum[8:10], sum[10:16])
}

func unsafeHeartbeatPath(path string) bool {
	base := strings.ToLower(filepath.Base(path))
	return base == "" || strings.HasPrefix(base, ".env") || strings.HasSuffix(base, ".key") || strings.Contains(base, "credential")
}
