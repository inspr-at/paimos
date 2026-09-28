// SPDX-License-Identifier: AGPL-3.0-only

package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

const (
	heartbeatSchema       = "aeon.harness-heartbeat.v1"
	heartbeatInterval     = 50
	heartbeatMaxCommits   = 20
	heartbeatMaxTokens    = 1_000_000_000_000
	heartbeatIndexMax     = 65536
	heartbeatTitleLineMax = 4096
	heartbeatUsageLineMax = 1 << 20
	heartbeatNoteMax      = 120
	heartbeatStopTimeout  = 15 * time.Second
)

var (
	errOwnerExited       = errors.New("owner exited")
	errOwnerGone         = errors.New("owner process is not alive")
	errHeartbeatTerminal = errors.New("heartbeat generation is closed")
	errHeartbeatBusy     = errors.New("heartbeat state is already in use")
	errHeartbeatState    = errors.New("heartbeat state must be an owned private directory of regular files")
	errUsageOverflow     = errors.New("usage overflow")
	heartbeatModelRE     = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/-]*$`)
	heartbeatPhases      = map[string]bool{"starting": true, "working": true, "yielded": true, "stopping": true}
	heartbeatActs        = map[string]bool{"busy": true, "idle": true, "throttled": true}
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

// normalize fills defaults and environment once. prepare validates the CLI;
// runHeartbeat applies the same defaults for in-process callers.
func (o *heartbeatOptions) normalize() {
	if o.Management == "" {
		o.Management = "unmanaged"
	}
	if o.Role == "" {
		o.Role = "worker"
	}
	if o.Phase == "" {
		o.Phase = "working"
	}
	if o.Activity == "" {
		o.Activity = "busy"
	}
	if strings.TrimSpace(o.Model) == "" {
		o.Model = os.Getenv("AEON_MODEL")
	}
	if strings.TrimSpace(o.Effort) == "" {
		o.Effort = os.Getenv("AEON_EFFORT")
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
}

func (o *heartbeatOptions) applyRuntimeDefaults() {
	o.normalize()
	if o.Interval <= 0 {
		o.Interval = heartbeatInterval
	}
}

func (o *heartbeatOptions) prepare() error {
	o.normalize()
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
	if !heartbeatPhases[o.Phase] {
		return usagef("invalid --phase")
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
	return nil
}

func (rt *runtime) runHeartbeat(ctx context.Context, o heartbeatOptions, dep heartbeatDeps) error {
	o.applyRuntimeDefaults()
	session, created, err := rt.openHeartbeatSession(ctx, o, dep)
	if err != nil {
		return err
	}
	defer session.hold.release()
	if session.disk.Terminal {
		return nil
	}
	if created {
		if err := saveHeartbeatSession(&session); err != nil {
			return rt.abandonHeartbeat(o, &session, err)
		}
	}
	if dep.alive == nil {
		pid, start := session.disk.OwnerPID, session.disk.OwnerStart
		dep.alive = func(int) bool { return ownerAlive(pid, start) }
	}
	if dep.wait == nil {
		dep.wait = func(ctx context.Context, pid int, interval time.Duration) error {
			return waitHeartbeat(ctx, pid, dep.alive, interval)
		}
	}
	interval := time.Duration(o.Interval) * time.Second
	for {
		if ctx.Err() != nil || !dep.alive(o.OwnerPID) {
			return rt.finishHeartbeat(o, &session)
		}
		err := rt.heartbeatBeat(ctx, o, dep, &session)
		switch {
		case errors.Is(err, errHeartbeatTerminal):
			if serr := saveHeartbeatSession(&session); serr != nil {
				return serr
			}
			return nil
		case err != nil && ctx.Err() != nil:
			return rt.finishHeartbeat(o, &session)
		case err != nil:
			fmt.Fprintf(rt.stderr, "heartbeat: beat failed: %s\n", err.Error())
		default:
			if serr := saveHeartbeatSession(&session); serr != nil {
				fmt.Fprintf(rt.stderr, "heartbeat: state save failed\n")
			}
		}
		if err := dep.wait(ctx, o.OwnerPID, interval); err != nil {
			return rt.finishHeartbeat(o, &session)
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

func (rt *runtime) finishHeartbeat(o heartbeatOptions, session *heartbeatSession) error {
	ctx, cancel := context.WithTimeout(context.Background(), heartbeatStopTimeout)
	defer cancel()
	if o.Transcript != "" && !session.disk.Terminal {
		projectID, err := rt.harnessProject(o.Project)
		if err == nil {
			if uerr := rt.reportHeartbeatUsage(ctx, projectID, o, session); uerr != nil {
				if heartbeatTerminalStatus(uerr) {
					markHeartbeatTerminal(session, terminalReason(uerr))
					_ = saveHeartbeatSession(session)
					return nil
				}
				fmt.Fprintf(rt.stderr, "heartbeat: final usage flush failed\n")
			}
			// Success clears a pending report in memory. Persist that either way so a
			// lost acknowledgement stays replayable and an accepted one is not sent again.
			_ = saveHeartbeatSession(session)
		}
	}
	if session.disk.Terminal {
		_ = saveHeartbeatSession(session)
		return nil
	}
	return rt.stopHeartbeat(ctx, o.Project, *session)
}

// abandonHeartbeat closes a generation whose state could not be saved, and
// keeps a stop intent when the close itself does not succeed.
func (rt *runtime) abandonHeartbeat(o heartbeatOptions, session *heartbeatSession, cause error) error {
	ctx, cancel := context.WithTimeout(context.Background(), heartbeatStopTimeout)
	defer cancel()
	if err := rt.stopHeartbeat(ctx, o.Project, *session); err != nil {
		fmt.Fprintf(rt.stderr, "heartbeat: could not close the new session\n")
		_ = session.hold.writeFile("session.id", []byte(session.id+"\n"))
		_ = session.hold.writeFile("stop.intent", []byte(session.id+"\n"))
		return cause
	}
	_ = clearHeartbeatIdentity(&session.hold)
	return cause
}

func (rt *runtime) openHeartbeatSession(ctx context.Context, o heartbeatOptions, dep heartbeatDeps) (session heartbeatSession, created bool, err error) {
	// A signal that arrives before registration still has to record the generation
	// and then stop it. Cancellation applies to beats, scans and later requests.
	if ctx.Err() != nil {
		ctx = context.WithoutCancel(ctx)
	}
	hold, err := openHeartbeatHold(o.StateDir)
	if err != nil {
		return heartbeatSession{}, false, err
	}
	session.hold = hold
	defer func() {
		if err != nil {
			hold.release()
		}
	}()
	if err = rt.recoverStopIntent(o, &session); err != nil {
		return heartbeatSession{}, false, err
	}
	existing, ok, err := loadHeartbeatSession(&session.hold)
	if err != nil {
		return heartbeatSession{}, false, err
	}
	if ok {
		if existing.disk.StartRev == "" {
			existing.disk.StartRev, _ = gitHEAD(ctx, o.Worktree)
		}
		existing.hold = session.hold
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
	lease, err := readOrCreateStateSecret(&session.hold, "lease.key", 32)
	if err != nil {
		return heartbeatSession{}, false, err
	}
	ref, err := readOrCreateStateSecret(&session.hold, "session.ref", 24)
	if err != nil {
		return heartbeatSession{}, false, err
	}
	label, haveLabel := resolveHeartbeatLabel(ctx, o, dep, true)
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
		body["parent_harness_session_id"] = strings.ToLower(o.Parent)
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
	if err = rt.harnessDoCtx(ctx, http.MethodPost, harnessPath(projectID, ""), "", body, &out); err != nil {
		return heartbeatSession{}, false, err
	}
	if !validUUID(out.ID) {
		return heartbeatSession{}, false, errors.New("registration did not return a session id")
	}
	disk := heartbeatDisk{Schema: heartbeatSchema, SessionID: strings.ToLower(out.ID), OwnerPID: o.OwnerPID}
	if stamp, stampErr := readOwnerStamp(o.OwnerPID); stampErr == nil {
		disk.OwnerPID = stamp.PID
		disk.OwnerStart = stamp.Start
	}
	bindHeartbeatWorktree(ctx, o, &disk)
	if haveLabel {
		disk.LabelSent = true
		disk.SentLabel = label
	}
	return heartbeatSession{id: disk.SessionID, lease: lease, disk: disk, hold: session.hold}, true, nil
}

func (rt *runtime) recoverStopIntent(o heartbeatOptions, session *heartbeatSession) error {
	raw, err := session.hold.readFile("stop.intent", 256)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	id := strings.ToLower(strings.TrimSpace(string(raw)))
	lease, err := readStateSecret(&session.hold, "lease.key")
	if err != nil || !validUUID(id) {
		return errHeartbeatState
	}
	ctx, cancel := context.WithTimeout(context.Background(), heartbeatStopTimeout)
	defer cancel()
	stopErr := rt.stopHeartbeat(ctx, o.Project, heartbeatSession{id: id, lease: lease})
	if stopErr != nil {
		return stopErr
	}
	return clearHeartbeatIdentity(&session.hold)
}

func bindHeartbeatWorktree(ctx context.Context, o heartbeatOptions, disk *heartbeatDisk) {
	if strings.TrimSpace(o.Worktree) == "" {
		return
	}
	disk.BoundWorktree = filepath.Clean(o.Worktree)
	disk.StartedUnix = time.Now().Unix()
	if head, err := gitHEAD(ctx, o.Worktree); err == nil {
		disk.StartRev = head
		disk.CommitCursor = head
	}
	if branch, err := gitLine(ctx, o.Worktree, "rev-parse", "--abbrev-ref", "HEAD"); err == nil && validHeartbeatBranch(branch) {
		disk.BoundBranch = branch
	}
}

func (rt *runtime) heartbeatBeat(ctx context.Context, o heartbeatOptions, dep heartbeatDeps, session *heartbeatSession) error {
	if err := ctx.Err(); err != nil {
		return err
	}
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
	if label, ok := resolveHeartbeatLabel(ctx, o, dep, false); ok && (!session.disk.LabelSent || label != session.disk.SentLabel) {
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
	commits := heartbeatCommits(ctx, o, dep, &session.disk)
	if len(commits) > 0 {
		items := make([]map[string]string, 0, len(commits))
		for _, c := range commits {
			items = append(items, map[string]string{"sha": c.SHA, "subject": c.Subject})
		}
		body["commits"] = items
	}
	path := harnessPath(projectID, session.id) + "/heartbeat"
	err = rt.harnessDoCtx(ctx, http.MethodPost, path, session.lease, body, new(any))
	if heartbeatTerminalStatus(err) {
		session.disk.Sequence--
		markHeartbeatTerminal(session, terminalReason(err))
		return errHeartbeatTerminal
	}
	if err != nil && heartbeatStatus(err) == http.StatusConflict {
		var status struct {
			ActivitySequence int64 `json:"activity_sequence"`
		}
		if readErr := rt.harnessDoCtx(ctx, http.MethodGet, harnessPath(projectID, session.id), "", nil, &status); readErr == nil && status.ActivitySequence >= session.disk.Sequence {
			session.disk.Sequence = status.ActivitySequence + 1
			body["activity_sequence"] = session.disk.Sequence
			err = rt.harnessDoCtx(ctx, http.MethodPost, path, session.lease, body, new(any))
		}
	}
	if heartbeatTerminalStatus(err) {
		session.disk.Sequence--
		markHeartbeatTerminal(session, terminalReason(err))
		return errHeartbeatTerminal
	}
	if err != nil {
		session.disk.Sequence--
		return err
	}
	if label, ok := body["display_label"].(string); ok {
		session.disk.LabelSent = true
		session.disk.SentLabel = label
	}
	if len(commits) > 0 {
		session.disk.CommitCursor = commits[len(commits)-1].SHA
	}
	if o.Transcript != "" {
		if uerr := rt.reportHeartbeatUsage(ctx, projectID, o, session); uerr != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if heartbeatTerminalStatus(uerr) {
				markHeartbeatTerminal(session, terminalReason(uerr))
				return errHeartbeatTerminal
			}
			fmt.Fprintf(rt.stderr, "heartbeat: usage report failed\n")
		}
	}
	if o.PrintControls {
		rt.printHeartbeatControls(ctx, projectID, session.id)
	}
	return nil
}

func (rt *runtime) stopHeartbeat(ctx context.Context, project string, session heartbeatSession) error {
	if session.id == "" || session.lease == "" {
		return nil
	}
	projectID, err := rt.harnessProject(project)
	if err != nil {
		return err
	}
	err = rt.harnessDoCtx(ctx, http.MethodPost, harnessPath(projectID, session.id)+"/stop", session.lease, map[string]string{"reason": "stopped"}, new(any))
	if heartbeatTerminalStatus(err) {
		return nil
	}
	return err
}

func (rt *runtime) printHeartbeatControls(ctx context.Context, projectID, sessionID string) {
	var status struct {
		Controls []struct {
			ID    string `json:"id"`
			Kind  string `json:"kind"`
			State string `json:"state"`
		} `json:"controls"`
	}
	if err := rt.harnessDoCtx(ctx, http.MethodGet, harnessPath(projectID, sessionID), "", nil, &status); err == nil {
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
	if err := rt.doCtx(ctx, http.MethodGet, "/api/inbox/messages?wait_ms=0", nil, &page); err != nil {
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

func resolveHeartbeatLabel(ctx context.Context, o heartbeatOptions, dep heartbeatDeps, includeFlag bool) (string, bool) {
	if dep.label != nil {
		label, ok := dep.label()
		if !ok {
			return "", false
		}
		label = heartbeatText(label, 128)
		return label, label != ""
	}
	if label, ok := readHeartbeatNames(ctx, o); ok {
		return label, true
	}
	if includeFlag {
		if label := heartbeatText(o.Label, 128); label != "" {
			return label, true
		}
	}
	return "", false
}

func readHeartbeatNames(ctx context.Context, o heartbeatOptions) (string, bool) {
	id := o.sourceID()
	codex := func() (string, bool) { return readCodexSessionLabel(ctx, o.CodexIndex, id) }
	claude := func() (string, bool) { return readClaudeSessionLabel(ctx, o, id) }
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
	f, err := openNoFollow(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil || st.Size() > 65536 {
		return ""
	}
	raw := make([]byte, st.Size())
	if _, err := io.ReadFull(f, raw); err != nil {
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

func heartbeatStatus(err error) int {
	if err == nil {
		return 0
	}
	msg := err.Error()
	if !strings.HasPrefix(msg, "api ") {
		return 0
	}
	n := 0
	for i := 4; i < len(msg); i++ {
		if msg[i] < '0' || msg[i] > '9' {
			return n
		}
		n = n*10 + int(msg[i]-'0')
	}
	return n
}

func heartbeatTerminalStatus(err error) bool {
	switch heartbeatStatus(err) {
	case http.StatusForbidden, http.StatusGone:
		return true
	default:
		return false
	}
}

func terminalReason(err error) string {
	if heartbeatStatus(err) == http.StatusGone {
		return "archived"
	}
	return "stopped"
}

func markHeartbeatTerminal(session *heartbeatSession, reason string) {
	session.disk.Terminal = true
	if session.disk.TerminalReason == "" {
		session.disk.TerminalReason = reason
	}
}

func jsonToken(raw json.RawMessage) (int64, bool) {
	trimmed := bytesTrim(raw)
	if len(trimmed) == 0 || string(trimmed) == "null" {
		return 0, false
	}
	var n int64
	if err := json.Unmarshal(trimmed, &n); err != nil || n < 0 || n > heartbeatMaxTokens {
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
