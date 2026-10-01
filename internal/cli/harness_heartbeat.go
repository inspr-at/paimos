// SPDX-License-Identifier: AGPL-3.0-only

package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/inspr-at/paimos/internal/eta"
	"github.com/inspr-at/paimos/internal/harness"
	"github.com/inspr-at/paimos/internal/modelreport"
	"github.com/inspr-at/paimos/internal/rules"
	"github.com/inspr-at/paimos/internal/sessionrequest"
	"github.com/inspr-at/paimos/internal/version"
)

const (
	heartbeatSchema        = "aeon.harness-heartbeat.v1"
	heartbeatInterval      = 50
	heartbeatMaxCommits    = 20
	heartbeatMaxTokens     = 1_000_000_000_000
	heartbeatIndexMax      = 65536
	heartbeatTitleLineMax  = 4096
	heartbeatUsageLineMax  = 1 << 20
	heartbeatNoteMax       = 120
	heartbeatProgressStale = 30 * time.Minute
	heartbeatRemainingMax  = 364 * 24 * 60
)

// heartbeatStopTimeout bounds one stop attempt. The final usage flush has its
// own budget, so a stalled usage request cannot consume the stop. Tests may
// shorten either budget; production callers keep these defaults.
//
// heartbeatStopRetryBound is how many failed /stop attempts are kept across
// process starts. A 403 or 409 is not a failure when the server already shows
// this generation stopped. Transient failures, and a 403 or 409 that does not,
// are retried on later starts until the bound. Any other status is not retried.
var (
	heartbeatStopTimeout       = 15 * time.Second
	heartbeatUsageFlushTimeout = 15 * time.Second
	heartbeatStopRetryBound    = 5
)

var (
	errOwnerExited             = errors.New("owner exited")
	errOwnerGone               = errors.New("owner process is not alive")
	errHeartbeatTerminal       = errors.New("heartbeat generation is closed")
	errHeartbeatAlreadyStopped = errors.New("heartbeat generation is already stopped")
	errHeartbeatStopBound      = errors.New("heartbeat stop retry bound reached")
	errHeartbeatStopRejected   = errors.New("heartbeat stop was rejected")
	errHeartbeatBusy           = errors.New("heartbeat state is already in use")
	errHeartbeatState          = errors.New("heartbeat state must be an owned private directory of regular files")
	errUsageOverflow           = errors.New("usage overflow")
	heartbeatModelRE           = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/-]*$`)
	heartbeatPhases            = map[string]bool{"starting": true, "working": true, "yielded": true, "stopping": true}
	heartbeatActs              = map[string]bool{"busy": true, "idle": true, "throttled": true}
)

// heartbeatDeps replaces clocks, liveness and name lookup in tests.
type heartbeatDeps struct {
	alive   func(pid int) bool
	wait    func(ctx context.Context, pid int, interval time.Duration) error
	commits func(worktree, since string) ([]heartbeatCommit, error)
	label   func() (string, bool)
}

type heartbeatOptions struct {
	StatusFile        string
	Capacity          heartbeatCapacity
	OwnerPID          int
	Interval          int
	StateDir          string
	Project           string
	Agent             string
	Harness           string
	Host              string
	Label             string
	Model             string
	Effort            string
	AccountLabel      string
	HarnessVersion    string
	Brief             string
	Worktree          string
	Branch            string
	Note              string
	Phase             string
	Activity          string
	Succeeds          string
	Parent            string
	Ticket            string
	Shape             string
	Management        string
	Role              string
	SourceSession     string
	CodexIndex        string
	ClaudeProjects    string
	Transcript        string
	UsageSource       string
	UsageFile         string
	UsageID           string
	UsageStartedAt    time.Time // Registered generation start; never a CLI assertion.
	CodexHome         string
	GrokHome          string
	AccountID         string
	BillingMode       string
	SubscriptionLabel string
	PrintControls     bool
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
			o.Capacity.flags(fs)
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
			fs.string(&o.HarnessVersion, "harness-version", 0, "harness version (defaults to a bounded local --version probe)")
			fs.string(&o.Brief, "brief", 0, "short prompt file name or ticket key")
			fs.string(&o.Worktree, "worktree", 0, "worktree path")
			fs.string(&o.StatusFile, "status-file", 0, "JSON status: pct, remaining_min and note (workers default to WORKTREE/.agent-status.json)")
			fs.string(&o.Branch, "branch", 0, "branch name")
			fs.string(&o.Note, "note", 0, "current step, at most 120 characters")
			fs.string(&o.Phase, "phase", 0, "starting, working, yielded or stopping")
			fs.string(&o.Activity, "activity", 0, "busy, idle or throttled")
			fs.string(&o.Succeeds, "succeeds", 0, "stopped or heartbeat-lost predecessor coordinator UUID")
			fs.string(&o.Parent, "parent-session", 0, "parent public session UUID")
			fs.string(&o.Ticket, "ticket", 0, "ticket node key")
			fs.string(&o.Shape, "work-shape", 0, "required with --ticket; one of: ship, scout")
			fs.string(&o.Management, "management", 0, "managed or unmanaged")
			fs.string(&o.Role, "role", 0, "worker or coordinator")
			fs.string(&o.SourceSession, "source-session", 0, "harness session UUID for the name source and inbox index")
			fs.string(&o.CodexIndex, "codex-index", 0, "Codex session_index.jsonl (default ~/.codex/session_index.jsonl)")
			fs.string(&o.ClaudeProjects, "claude-projects", 0, "Claude Code projects directory (default $CLAUDE_CONFIG_DIR/projects or ~/.claude/projects)")
			fs.string(&o.Transcript, "transcript", 0, "Claude Code session transcript JSONL for usage and its title")
			fs.string(&o.UsageSource, "usage-source", 0, "usage log family: claude, codex, cursor, or grok")
			fs.string(&o.UsageFile, "usage-file", 0, "explicit usage log; credential paths are rejected")
			fs.string(&o.UsageID, "usage-id", 0, "vendor session or thread id used to locate the usage log")
			fs.string(&o.CodexHome, "codex-home", 0, "Codex home (default $CODEX_HOME or ~/.codex)")
			fs.string(&o.GrokHome, "grok-home", 0, "Grok home (default $GROK_HOME or ~/.grok)")
			fs.string(&o.AccountID, "account-id", 0, "Aeon account UUID; saved billing used when billing-mode is unknown")
			fs.string(&o.BillingMode, "billing-mode", 0, "unknown, api, or subscription (default unknown)")
			fs.string(&o.SubscriptionLabel, "subscription-label", 0, "public subscription label; only with --billing-mode subscription")
			fs.bool(&o.PrintControls, "print-controls", 0, "print request JSON records and pending control/message lines; --json emits NDJSON")
		},
		run: func([]string) error {
			if err := o.Capacity.validate(); err != nil {
				return err
			}
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
		if dir := os.Getenv("CLAUDE_CONFIG_DIR"); dir != "" {
			o.ClaudeProjects = filepath.Join(dir, "projects")
		} else if home, err := os.UserHomeDir(); err == nil {
			o.ClaudeProjects = filepath.Join(home, ".claude", "projects")
		}
	}
	if o.CodexHome == "" {
		if home := os.Getenv("CODEX_HOME"); home != "" {
			o.CodexHome = home
		} else if home, err := os.UserHomeDir(); err == nil {
			o.CodexHome = filepath.Join(home, ".codex")
		}
	}
	if o.GrokHome == "" {
		if home := os.Getenv("GROK_HOME"); home != "" {
			o.GrokHome = home
		} else if home, err := os.UserHomeDir(); err == nil {
			o.GrokHome = filepath.Join(home, ".grok")
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
	if o.Succeeds != "" && !validUUID(o.Succeeds) {
		return usagef("invalid --succeeds session")
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
	if o.AccountID != "" && o.Capacity.Account != "" && o.AccountID != o.Capacity.Account {
		return usagef("usage and capacity accounts must match")
	}
	if o.AccountID != "" && !validUUID(o.AccountID) {
		return usagef("invalid --account-id")
	}
	switch o.BillingMode {
	case "", "unknown", "api", "subscription":
	default:
		return usagef("invalid --billing-mode")
	}
	if o.BillingMode == "" {
		o.BillingMode = "unknown"
	}
	if o.SubscriptionLabel != "" && o.BillingMode != "subscription" {
		return usagef("--subscription-label requires subscription billing")
	}
	switch o.UsageSource {
	case "", "claude", "codex", "cursor", "grok":
	default:
		return usagef("invalid --usage-source")
	}
	if o.UsageID != "" && !usageID(o.UsageID) {
		return usagef("invalid --usage-id")
	}
	if o.UsageFile != "" && !allowedUsagePath(usageSourceOf(*o), o.UsageFile) {
		return usagef("--usage-file is not a usage log")
	}
	return nil
}

func (rt *runtime) runHeartbeat(ctx context.Context, o heartbeatOptions, dep heartbeatDeps) error {
	o.applyRuntimeDefaults()
	session, created, err := rt.openHeartbeatSession(ctx, o, dep)
	if err != nil {
		if errors.Is(err, errOwnerExited) || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return nil
		}
		return err
	}
	defer session.hold.release()
	// A completed generation keeps its identity. Retry usage here and do not
	// heartbeat: a beat against an already-stopped session is a 403 that
	// would mark the generation terminal and strand the report.
	if heartbeatSettling(&session) {
		explainClosedHeartbeat(rt, &session)
		rt.settleGeneration(o, &session)
		releaseSessionIndex(&session)
		return nil
	}
	if session.disk.Terminal {
		explainClosedHeartbeat(rt, &session)
		releaseSessionIndex(&session)
		return nil
	}
	if created {
		if err := saveHeartbeatSession(&session); err != nil {
			return rt.abandonHeartbeat(o, &session, err)
		}
	}
	if dep.alive == nil {
		// A registration read can miss the stamp. One more read, then the
		// closure keeps that value: a later read must match it (AEON-343).
		if created && session.disk.OwnerStart == "" {
			if stamp, stampErr := readOwnerStamp(session.disk.OwnerPID); stampErr == nil {
				session.disk.OwnerPID = stamp.PID
				session.disk.OwnerStart = stamp.Start
				if serr := saveHeartbeatSession(&session); serr != nil {
					fmt.Fprintf(rt.stderr, "heartbeat: state save failed\n")
				}
			}
		}
		pid, start := session.disk.OwnerPID, session.disk.OwnerStart
		dep.alive = func(int) bool { return ownerAlive(pid, start) }
	}
	// Dying before the first beat is a start failure. A later exit still
	// finishes cleanly through the loop.
	if !dep.alive(o.OwnerPID) {
		return rt.rejectDeadOwner(o, &session)
	}
	if err := recordSessionIndex(rt, o, &session); err != nil {
		return err
	}
	// Runs before hold.release, so every return after publish withdraws the binding.
	defer releaseSessionIndex(&session)
	return rt.heartbeatLoop(ctx, o, dep, &session)
}

// heartbeatLoop serves both the PID observer and a one-shot child process.
func (rt *runtime) heartbeatLoop(ctx context.Context, o heartbeatOptions, dep heartbeatDeps, session *heartbeatSession) error {
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
		rt.noteHeartbeatSources(ctx, o, session)
		if ctx.Err() != nil || !dep.alive(o.OwnerPID) {
			return rt.finishHeartbeat(o, session)
		}
		err := rt.heartbeatBeat(ctx, o, dep, session)
		switch {
		case errors.Is(err, errHeartbeatTerminal):
			rememberSettlement(session)
			if serr := saveHeartbeatSession(session); serr != nil {
				releaseSessionIndex(session)
				return serr
			}
			releaseSessionIndex(session)
			return nil
		case err != nil && ctx.Err() != nil:
			return rt.finishHeartbeat(o, session)
		case err != nil:
			fmt.Fprintf(rt.stderr, "heartbeat: beat failed: %s\n", err.Error())
		default:
			if serr := saveHeartbeatSession(session); serr != nil {
				fmt.Fprintf(rt.stderr, "heartbeat: state save failed\n")
			}
		}
		if err := dep.wait(ctx, o.OwnerPID, interval); err != nil {
			return rt.finishHeartbeat(o, session)
		}
	}
}

// noteHeartbeatSources records hashes of AGENTS.md and CLAUDE.md in the
// registered worktree. Missing files record nothing. Refused files are skipped.
// The request carries only those root kinds, so the server keeps skills and
// prompt templates from an earlier report. The body is logical names, digests
// and sizes, never paths or contents.
func (rt *runtime) noteHeartbeatSources(ctx context.Context, o heartbeatOptions, session *heartbeatSession) {
	if session == nil || session.disk.SourcesRecorded || strings.TrimSpace(o.Worktree) == "" {
		return
	}
	if !validUUID(session.disk.ProjectID) || !validUUID(session.id) {
		return
	}
	items, settled := heartbeatInstructionItems(o.Worktree)
	if len(items) > 0 {
		pctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		err := rt.harnessDoCtx(pctx, http.MethodPost, harnessPath(session.disk.ProjectID, session.id)+"/provenance", session.lease, map[string]any{"items": items}, new(map[string]any))
		cancel()
		if err != nil {
			fmt.Fprintf(rt.stderr, "heartbeat: instruction provenance was not recorded\n")
			return
		}
	} else if !settled {
		return
	}
	session.disk.SourcesRecorded = true
	if err := saveHeartbeatSession(session); err != nil {
		fmt.Fprintf(rt.stderr, "heartbeat: instruction provenance was not recorded\n")
	}
}

// heartbeatInstructionItems hashes the worktree's AGENTS.md and CLAUDE.md
// through the hardened instruction reader. The presence check lives in the
// harness package too, so no heartbeat source file stats a file outside the
// harness fence (TestHarnessReadersUseTheFence).
func heartbeatInstructionItems(dir string) ([]harness.ProvenanceItem, bool) {
	return harness.CollectPresentInstructionFiles(dir, "AGENTS.md", "CLAUDE.md")
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
	if !session.disk.Terminal {
		usageCtx, cancel := context.WithTimeout(context.Background(), heartbeatUsageFlushTimeout)
		rt.drainHeartbeatUsage(usageCtx, o, session)
		cancel()
		capacityCtx, capacityCancel := context.WithTimeout(context.Background(), heartbeatUsageFlushTimeout)
		finalCapacity := o.Capacity
		if finalCapacity.File != "" {
			finalCapacity.Phase = "end"
		}
		if err := rt.reportHeartbeatCapacity(capacityCtx, finalCapacity); err != nil {
			fmt.Fprintln(rt.stderr, "heartbeat: final capacity flush failed")
		}
		capacityCancel()
	}
	if session.disk.Terminal {
		_ = saveHeartbeatSession(session)
		releaseSessionIndex(session)
		return nil
	}
	return rt.finishStop(o, session)
}

// drainHeartbeatUsage posts every complete usage window that fits in ctx.
// A stalled report ends this budget only; the cursor and any unacknowledged
// report stay on disk for the next start.
func (rt *runtime) drainHeartbeatUsage(ctx context.Context, o heartbeatOptions, session *heartbeatSession) {
	if session == nil {
		return
	}
	// Terminal means "do not heartbeat". Pending usage is still owed, and so
	// is unread transcript: a partial record or an interrupted scan. A
	// stopped generation is allowed to settle those, and a closed one may
	// still have transcript bytes past the cursor.
	owed := len(session.disk.PendingUsage) > 0 || usageOutstanding(o, session)
	if session.disk.Terminal && !owed && !session.disk.Closed {
		return
	}
	target, targetErr := resolveSessionHeartbeatUsage(o, session)
	if targetErr != nil {
		fmt.Fprintf(rt.stderr, "heartbeat: usage source rejected\n")
		_ = saveHeartbeatSession(session)
		return
	}
	if target.Path == "" && len(session.disk.PendingUsage) == 0 {
		return
	}
	projectID := session.disk.ProjectID
	if !validUUID(projectID) {
		var err error
		projectID, err = rt.harnessProjectCtx(ctx, o.Project)
		if err != nil {
			fmt.Fprintf(rt.stderr, "heartbeat: final usage flush failed\n")
			return
		}
		session.disk.ProjectID = projectID
	}
	for {
		if err := ctx.Err(); err != nil {
			_ = saveHeartbeatSession(session)
			return
		}
		beforeOff := session.disk.UsageOffset
		beforePending := len(session.disk.PendingUsage)
		uerr := rt.reportHeartbeatUsage(ctx, projectID, o, session)
		if heartbeatTerminalStatus(uerr) {
			markHeartbeatTerminal(session, terminalReason(uerr))
			if heartbeatStatus(uerr) == http.StatusGone {
				session.disk.PendingUsage = nil
				_ = session.hold.remove("settle.intent")
			} else {
				rememberSettlement(session)
			}
			_ = saveHeartbeatSession(session)
			return
		}
		if uerr != nil {
			fmt.Fprintf(rt.stderr, "heartbeat: final usage flush failed\n")
			_ = saveHeartbeatSession(session)
			return
		}
		if err := saveHeartbeatSession(session); err != nil {
			fmt.Fprintf(rt.stderr, "heartbeat: final usage flush failed\n")
			return
		}
		if len(session.disk.PendingUsage) == 0 && usageCaughtUp(o, session) {
			return
		}
		if session.disk.UsageOffset == beforeOff && len(session.disk.PendingUsage) == beforePending {
			return
		}
	}
}

// heartbeatUsageDue is true when this beat can owe a usage post. A resolved
// Grok, Codex, or Cursor log counts even when --transcript is empty.
func heartbeatUsageDue(o heartbeatOptions, session *heartbeatSession) bool {
	if o.Transcript != "" {
		return true
	}
	target, err := resolveSessionHeartbeatUsage(o, session)
	return err != nil || target.Path != ""
}

func heartbeatTranscriptCaughtUp(source, path string, offset int64) bool {
	kind, ok := harnessKindForSource(source)
	if path == "" || !ok {
		return true
	}
	info, err := statHarnessFile(kind, path)
	if err != nil {
		return true
	}
	return offset >= info.Size()
}

// heartbeatTranscriptOutstanding reports bytes a later scan can still turn
// into usage: a partial record past the cursor, or an interrupted discard
// still waiting for its newline.
func heartbeatTranscriptOutstanding(source, path string, offset int64, discarding bool) bool {
	if !heartbeatTranscriptCaughtUp(source, path, offset) {
		return true
	}
	kind, ok := harnessKindForSource(source)
	if !discarding || path == "" || !ok {
		return false
	}
	info, err := statHarnessFile(kind, path)
	// A cursor past EOF means the transcript shrank.
	return err == nil && offset <= info.Size()
}

// finishStop closes the generation on a budget that the usage flush does not share.
// A failed close leaves stop.intent so the next start retries it. Pending usage
// is recorded as settle.intent whether or not the close itself succeeds.
func (rt *runtime) finishStop(o heartbeatOptions, session *heartbeatSession) error {
	ctx, cancel := context.WithTimeout(context.Background(), heartbeatStopTimeout)
	defer cancel()
	err := rt.stopHeartbeat(ctx, o.Project, *session)
	if errors.Is(err, errHeartbeatAlreadyStopped) {
		err = nil
	}
	if err != nil {
		rememberStopIntent(session, false, 1, heartbeatStatus(err))
		rememberSettlement(session)
		_ = saveHeartbeatSession(session)
		releaseSessionIndex(session)
		return err
	}
	return persistStopSuccess(session)
}

// rememberStopIntent records a close that has not been persisted.
// attempts is how many failed /stop calls have already been made.
// code is the last HTTP status, or 0 when the failure was not an API status.
func rememberStopIntent(session *heartbeatSession, strict bool, attempts, code int) {
	if session == nil || session.id == "" || session.hold.dir == nil {
		return
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s\n", session.id)
	if strict {
		b.WriteString("strict\n")
	} else {
		b.WriteByte('\n')
	}
	fmt.Fprintf(&b, "%d\n%d\n", attempts, code)
	// How the job ended survives with the intent, so a replayed stop still says so (AEON-437).
	fmt.Fprintf(&b, "%s\n", persistedStopReason(session.stopReason))
	_ = session.hold.writeFile("stop.intent", []byte(b.String()))
}

func rememberSettleIntent(session *heartbeatSession) {
	if session == nil || session.id == "" || session.hold.dir == nil {
		return
	}
	_ = session.hold.writeFile("settle.intent", []byte(session.id+"\n"))
}

func rememberSettlement(session *heartbeatSession) {
	if session == nil || len(session.disk.PendingUsage) == 0 {
		return
	}
	rememberSettleIntent(session)
}

func settleIntent(session *heartbeatSession) bool {
	if session == nil || session.hold.dir == nil {
		return false
	}
	_, err := session.hold.readFile("settle.intent", 256)
	return err == nil
}

func heartbeatSettling(session *heartbeatSession) bool {
	if session == nil {
		return false
	}
	if session.disk.Closed || settleIntent(session) {
		return true
	}
	return session.disk.Terminal && len(session.disk.PendingUsage) > 0
}

// settleGeneration posts usage owed by a generation that must not heartbeat.
// Acknowledged reports stay eligible for another settle while unread
// transcript remains, including a partial record or an interrupted scan.
func (rt *runtime) settleGeneration(o heartbeatOptions, session *heartbeatSession) {
	ctx, cancel := context.WithTimeout(context.Background(), heartbeatUsageFlushTimeout)
	defer cancel()
	rt.drainHeartbeatUsage(ctx, o, session)
	if session.disk.TerminalReason == "archived" {
		session.disk.PendingUsage = nil
		_ = session.hold.remove("settle.intent")
	} else if len(session.disk.PendingUsage) > 0 || usageOutstanding(o, session) {
		rememberSettleIntent(session)
	} else {
		_ = session.hold.remove("settle.intent")
	}
	_ = saveHeartbeatSession(session)
}

// persistStopSuccess keeps the generation id and transcript cursor. The next
// start settles any leftover usage and does not register another generation.
func persistStopSuccess(session *heartbeatSession) error {
	if session == nil {
		return errHeartbeatState
	}
	session.disk.Closed = true
	if len(session.disk.PendingUsage) > 0 {
		rememberSettleIntent(session)
	} else if session.hold.dir != nil {
		_ = session.hold.remove("settle.intent")
	}
	if session.hold.dir != nil {
		_ = session.hold.remove("stop.intent")
	}
	err := saveHeartbeatSession(session)
	releaseSessionIndex(session)
	return err
}

// abandonHeartbeat closes a generation whose state could not be saved, and
// keeps a stop intent when the close itself does not succeed.
func (rt *runtime) abandonHeartbeat(o heartbeatOptions, session *heartbeatSession, cause error) error {
	ctx, cancel := context.WithTimeout(context.Background(), heartbeatStopTimeout)
	defer cancel()
	if err := rt.stopHeartbeat(ctx, o.Project, *session); err != nil && !errors.Is(err, errHeartbeatAlreadyStopped) {
		fmt.Fprintf(rt.stderr, "heartbeat: could not close the new session\n")
		_ = session.hold.writeFile("session.id", []byte(session.id+"\n"))
		rememberStopIntent(session, false, 1, heartbeatStatus(err))
		releaseSessionIndex(session)
		return cause
	}
	_ = clearHeartbeatIdentity(&session.hold)
	releaseSessionIndex(session)
	return cause
}

func (rt *runtime) openHeartbeatSession(ctx context.Context, o heartbeatOptions, dep heartbeatDeps) (session heartbeatSession, created bool, err error) {
	ownerCtx := ctx
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
			// The state lock is already held. A busy lock returns above, before
			// this defer, and leaves the other helper's binding in place.
			// Use the local hold: a named return replaces session before defers run.
			releaseSessionIndex(&heartbeatSession{hold: hold})
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
		// State written before BoundTicket has no local binding. A restart
		// without --ticket would then omit progress and ETA. The server's
		// session binding is copied in and persisted.
		rt.backfillBoundTicket(ctx, o, &existing)
		return existing, false, nil
	}
	// Prove the owner before registration. A dead owner must not create a
	// session that stays "starting" when its stop is rejected.
	proved, err := proveOwnerAtStart(o.OwnerPID, dep)
	if err != nil {
		fmt.Fprintf(rt.stderr, "heartbeat: owner %d failed the start check\n", o.OwnerPID)
		return heartbeatSession{}, false, errOwnerGone
	}
	projectID, err := rt.harnessProjectCtx(ctx, o.Project)
	if err != nil {
		return heartbeatSession{}, false, err
	}
	me, err := rt.caller()
	if err != nil {
		return heartbeatSession{}, false, err
	}
	if me.Principal.Name != o.Agent {
		return heartbeatSession{}, false, harnessAgentMismatch(me.Principal.Name)
	}
	lease, err := readOrCreateStateSecret(&session.hold, "lease.key", 32)
	if err != nil {
		return heartbeatSession{}, false, err
	}
	ref, err := readOrCreateStateSecret(&session.hold, "session.ref", 24)
	if err != nil {
		return heartbeatSession{}, false, err
	}
	// A native coordinator session survives a helper process restart.
	if o.Role == "coordinator" && o.SourceSession != "" {
		ref = o.Harness + ":" + strings.ToLower(o.SourceSession)
	}
	label, haveLabel := resolveHeartbeatLabel(ctx, o, dep, true)
	body := map[string]any{
		"max_session_file_bytes": rules.SessionFileLimit(o.Harness), "rules_client_version": version.Version,
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
	putText(body, "harness_version", harnessVersionOrProbe(ctx, o.Harness, o.HarnessVersion), true)
	putText(body, "brief", heartbeatText(o.Brief, 240), true)
	putText(body, "worktree", heartbeatText(o.Worktree, 512), true)
	putText(body, "branch", heartbeatText(o.Branch, 200), true)
	if o.Succeeds != "" {
		body["succeeds_session_id"] = strings.ToLower(o.Succeeds)
	}
	if o.Parent != "" {
		body["parent_harness_session_id"] = strings.ToLower(o.Parent)
	}
	attachVendorSessionRef(body, o.Harness, ref, lease, o.Parent, o.SourceSession)
	var boundTicket string
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
		boundTicket = strings.TrimSpace(o.Ticket)
	}
	var out struct {
		ID string `json:"id"`
	}
	// Capture the owner before registration: a long predecessor timeout must
	// not attach the new generation to a process that reused the owner's PID.
	disk := heartbeatDisk{Schema: heartbeatSchema, OwnerPID: o.OwnerPID, ProjectID: projectID, BoundTicket: boundTicket}
	if proved.Start != "" {
		disk.OwnerPID = proved.PID
		disk.OwnerStart = proved.Start
	} else if stamp, stampErr := readOwnerStamp(o.OwnerPID); stampErr == nil {
		disk.OwnerPID = stamp.PID
		disk.OwnerStart = stamp.Start
	}
	if dep.alive == nil {
		dep.alive = func(int) bool { return ownerAlive(disk.OwnerPID, disk.OwnerStart) }
	}
	if dep.wait == nil {
		dep.wait = func(ctx context.Context, pid int, interval time.Duration) error {
			return waitHeartbeat(ctx, pid, dep.alive, interval)
		}
	}
	for {
		err = rt.harnessDoCtx(ctx, http.MethodPost, harnessPath(projectID, ""), "", body, &out)
		if err == nil {
			break
		}
		// A crashed coordinator can still count as healthy until its last
		// heartbeat expires. Retry only that registration conflict, keeping
		// the same body and state lock until the server can adopt its children.
		if o.Role != "coordinator" || o.SourceSession == "" || (err.Error() != "api 409: active generation conflicts with registration" && err.Error() != "api 409: "+harness.RegistrationLeaseConflict) {
			return heartbeatSession{}, false, err
		}
		if err = ownerCtx.Err(); err != nil {
			return heartbeatSession{}, false, err
		}
		if !dep.alive(o.OwnerPID) {
			return heartbeatSession{}, false, errOwnerExited
		}
		interval := time.Duration(o.Interval) * time.Second
		fmt.Fprintf(rt.stderr, "heartbeat: predecessor generation is still active; retrying registration in %s\n", interval)
		if err = dep.wait(ownerCtx, o.OwnerPID, interval); err != nil {
			return heartbeatSession{}, false, err
		}
		if err = ownerCtx.Err(); err != nil {
			return heartbeatSession{}, false, err
		}
		if !dep.alive(o.OwnerPID) {
			return heartbeatSession{}, false, errOwnerExited
		}
	}
	if !validUUID(out.ID) {
		return heartbeatSession{}, false, errors.New("registration did not return a session id")
	}
	disk.SessionID = strings.ToLower(out.ID)
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
	id, strict, attempts, code, reason := heartbeatStopIntent(raw)
	lease, err := readStateSecret(&session.hold, "lease.key")
	if err != nil || !validUUID(id) {
		return errHeartbeatState
	}
	stopping := heartbeatSession{id: id, lease: lease, stopReason: reason}
	// A previous flush may have saved a report and then failed to stop.
	// Post that work on the usage budget, then stop on a fresh one.
	// The generation id and transcript cursor stay so the next start cannot
	// register again and reread the same bytes.
	var kept *heartbeatSession
	if existing, ok, loadErr := loadHeartbeatSession(&session.hold); loadErr == nil && ok && strings.EqualFold(existing.id, id) {
		existing.hold = session.hold
		existing.lease = lease
		usageCtx, cancel := context.WithTimeout(context.Background(), heartbeatUsageFlushTimeout)
		rt.drainHeartbeatUsage(usageCtx, o, &existing)
		cancel()
		stopping.disk = existing.disk
		existing.stopReason = reason
		kept = &existing
	}
	ctx, cancel := context.WithTimeout(context.Background(), heartbeatStopTimeout)
	defer cancel()
	if attempts >= heartbeatStopRetryBound || (attempts > 0 && !stopRetryable(code)) {
		return rt.finishBoundedStop(ctx, o, stopping, kept, id, lease, session.hold, strict, attempts)
	}
	var stopErr error
	if strict {
		stopErr = rt.stopHeartbeatStrict(ctx, o.Project, stopping)
	} else {
		stopErr = rt.stopHeartbeat(ctx, o.Project, stopping)
	}
	if errors.Is(stopErr, errHeartbeatAlreadyStopped) {
		stopErr = nil
	}
	if stopErr != nil {
		if strict {
			fmt.Fprintf(rt.stderr, "heartbeat: owner %d failed the start check\n", o.OwnerPID)
		}
		holder := kept
		if holder == nil {
			holder = &heartbeatSession{id: id, hold: session.hold, stopReason: reason}
		}
		rememberStopIntent(holder, strict, attempts+1, heartbeatStatus(stopErr))
		if kept != nil {
			rememberSettlement(kept)
			_ = saveHeartbeatSession(kept)
		}
		releaseSessionIndex(holder)
		return stopErr
	}
	if kept != nil {
		return persistStopSuccess(kept)
	}
	return session.hold.remove("stop.intent")
}

// finishBoundedStop persists a generation the server already stopped.
// Otherwise it refuses another /stop: the bound is spent, or the last status
// is not one this recovery retries.
func (rt *runtime) finishBoundedStop(ctx context.Context, o heartbeatOptions, stopping heartbeatSession, kept *heartbeatSession, id, lease string, hold heartbeatHold, strict bool, attempts int) error {
	if stopped, readErr := rt.generationAlreadyStopped(ctx, o.Project, stopping); readErr == nil && stopped {
		target := kept
		if target == nil {
			disk := stopping.disk
			disk.SessionID = id
			target = &heartbeatSession{id: id, lease: lease, disk: disk, hold: hold}
		}
		return persistStopSuccess(target)
	}
	if attempts >= heartbeatStopRetryBound {
		fmt.Fprintf(rt.stderr, "heartbeat: stop retries are exhausted\n")
		if strict {
			fmt.Fprintf(rt.stderr, "heartbeat: owner %d failed the start check\n", o.OwnerPID)
		}
		releaseSessionIndex(&heartbeatSession{hold: hold})
		return errHeartbeatStopBound
	}
	fmt.Fprintf(rt.stderr, "heartbeat: stop will not be retried\n")
	releaseSessionIndex(&heartbeatSession{hold: hold})
	return errHeartbeatStopRejected
}

// backfillBoundTicket copies the server's ticket binding into state that
// predates BoundTicket. A missing or unbound session is left unchanged so
// resume still heartbeats. A closed generation is skipped: it does not
// report estimates, and a status read would be a new request on every open.
func (rt *runtime) backfillBoundTicket(ctx context.Context, o heartbeatOptions, session *heartbeatSession) {
	if session == nil || session.disk.Terminal || session.disk.Closed || strings.TrimSpace(session.disk.BoundTicket) != "" {
		return
	}
	projectID := session.disk.ProjectID
	if !validUUID(projectID) {
		var err error
		projectID, err = rt.harnessProjectCtx(ctx, o.Project)
		if err != nil || !validUUID(projectID) {
			return
		}
	}
	var status struct {
		ID           string  `json:"id"`
		TicketNodeID *string `json:"ticket_node_id"`
	}
	if err := rt.harnessDoCtx(ctx, http.MethodGet, harnessPath(projectID, session.id), "", nil, &status); err != nil {
		return
	}
	if status.ID != "" && !strings.EqualFold(status.ID, session.id) {
		return
	}
	if status.TicketNodeID == nil || !validUUID(*status.TicketNodeID) {
		return
	}
	var node apiNode
	if err := rt.doCtx(ctx, http.MethodGet, "/api/nodes/"+url.PathEscape(*status.TicketNodeID), nil, &node); err != nil {
		return
	}
	if !strings.EqualFold(node.ID, *status.TicketNodeID) {
		return
	}
	key := heartbeatText(node.Key, 200)
	if key == "" {
		return
	}
	session.disk.BoundTicket = key
	if err := saveHeartbeatSession(session); err != nil {
		fmt.Fprintf(rt.stderr, "heartbeat: state save failed\n")
	}
}

func bindHeartbeatWorktree(ctx context.Context, o heartbeatOptions, disk *heartbeatDisk) {
	if strings.TrimSpace(o.Worktree) == "" {
		return
	}
	disk.BoundWorktree = filepath.Clean(o.Worktree)
	disk.RegisteredAt = time.Now().UTC()
	disk.StartedUnix = disk.RegisteredAt.Unix()
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
	// Resolve on the beat's context every time. A cached id would hide a
	// cancelled lookup, and the shutdown path reads ProjectID instead.
	projectID, err := rt.harnessProjectCtx(ctx, o.Project)
	if err != nil {
		return err
	}
	var controls []heartbeatControl
	if o.PrintControls {
		controls = rt.readHeartbeatControls(ctx, projectID, session.id)
		applyHeartbeatRequests(o, dep, session, controls, ctx)
	}
	session.disk.ProjectID = projectID
	session.disk.Sequence++
	body := map[string]any{
		"max_session_file_bytes": rules.SessionFileLimit(o.Harness), "rules_client_version": version.Version,
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
	if session.disk.RequestedLabel != "" {
		label, ok := resolveHeartbeatLabel(ctx, o, dep, false)
		if ok && label != session.disk.RequestedLabelSource && label != session.disk.RequestedLabel {
			session.disk.RequestedLabel = ""
		} else if !session.disk.LabelSent || session.disk.SentLabel != session.disk.RequestedLabel {
			body["display_label"] = session.disk.RequestedLabel
		} else {
			delete(body, "display_label")
		}
	}
	putText(body, "model", heartbeatText(o.Model, 128), true)
	putText(body, "reasoning_effort", heartbeatText(o.Effort, 40), true)
	if session.disk.RequestedModel != "" && o.Model == session.disk.ModelFlagAtRequest && o.Effort == session.disk.EffortFlagAtRequest {
		body["model"] = session.disk.RequestedModel
		body["reasoning_effort"] = session.disk.RequestedEffort
	} else {
		session.disk.RequestedModel = ""
		session.disk.RequestedEffort = ""
	}

	if model, ok := body["model"].(string); ok && model != "" {
		if effort, ok := body["reasoning_effort"].(string); ok && modelreport.ValidTuple(model, effort) {
			body["model_reports"] = []modelreport.Observation{{ReportID: modelreport.EvidenceID(session.id + "/advertised/" + model + "/" + effort), Harness: o.Harness, Model: model, Effort: effort, Status: "advertised"}}
		}
	}
	putText(body, "account_label", heartbeatText(o.AccountLabel, 128), true)
	putText(body, "brief", heartbeatText(o.Brief, 240), true)
	putText(body, "worktree", heartbeatText(o.Worktree, 512), true)
	putText(body, "branch", heartbeatText(o.Branch, 200), true)
	status, statusOK := readAgentStatus(o)
	now := time.Now().UTC()
	if !statusOK {
		rt.printEstimateWarnings([]harness.EstimateWarning{{Code: "status_file_unavailable", Hint: "Status file is missing, malformed or refused; keep pct (0–100) and remaining_min (0–524160) current in --status-file."}}, &session.disk.WarningAt, now)
	}
	if !status.ModifiedAt.IsZero() && now.Sub(status.ModifiedAt) > heartbeatProgressStale {
		rt.printEstimateWarnings([]harness.EstimateWarning{{Code: "stale_progress", Hint: "Status file has not been updated for over 30 minutes; refresh pct, remaining_min and note."}}, &session.disk.WarningAt, now)
	}
	// Registration persists the ticket. A restarted helper for that same
	// generation often has an empty --ticket flag and would otherwise drop
	// progress_pct and eta_ready_at, leaving the server on missing_progress
	// and missing_eta. An explicit flag still wins.
	ticket := strings.TrimSpace(o.Ticket)
	if ticket == "" && session != nil {
		ticket = strings.TrimSpace(session.disk.BoundTicket)
	}
	if o.Role == "worker" && ticket != "" {
		if status.Progress != nil {
			body["progress_pct"] = *status.Progress
		}
		if status.Remaining != nil {
			ready := status.ModifiedAt.Add(time.Duration(*status.Remaining * float64(time.Minute)))
			// Keep a margin inside the server window for client/server clock skew.
			if !ready.Before(now.Add(-eta.MaxPast+eta.ClockSkew)) && !ready.After(now.Add(eta.MaxFuture-eta.ClockSkew)) {
				body["eta_ready_at"] = ready.Format(time.RFC3339Nano)
			}
		}
	}
	note := heartbeatNote(o.Note)
	if note == "" {
		note = status.Note
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
	var response struct {
		Warnings []harness.EstimateWarning `json:"warnings"`
	}
	postBeat := func() error {
		err := rt.harnessDoCtx(ctx, http.MethodPost, path, session.lease, body, &response)
		// A ticket can be unbound while this helper retains its original flags.
		// Optional status-file estimates must never prevent a liveness report.
		if heartbeatStatus(err) == http.StatusBadRequest && (body["progress_pct"] != nil || body["eta_ready_at"] != nil) {
			delete(body, "progress_pct")
			delete(body, "eta_ready_at")
			rt.printEstimateWarnings([]harness.EstimateWarning{{Code: "status_file_unavailable", Hint: "Status-file progress or ETA was rejected; continuing the heartbeat without those fields."}}, &session.disk.WarningAt, time.Now())
			response.Warnings = nil
			err = rt.harnessDoCtx(ctx, http.MethodPost, path, session.lease, body, &response)
		}
		return err
	}
	err = postBeat()
	if heartbeatTerminalStatus(err) {
		session.disk.Sequence--
		markHeartbeatTerminal(session, terminalReason(err))
		rememberSettlement(session)
		return errHeartbeatTerminal
	}
	if err != nil && heartbeatStatus(err) == http.StatusConflict {
		var status struct {
			ActivitySequence int64 `json:"activity_sequence"`
		}
		if readErr := rt.harnessDoCtx(ctx, http.MethodGet, harnessPath(projectID, session.id), "", nil, &status); readErr == nil && status.ActivitySequence >= session.disk.Sequence {
			session.disk.Sequence = status.ActivitySequence + 1
			body["activity_sequence"] = session.disk.Sequence
			err = postBeat()
		}
	}
	if heartbeatTerminalStatus(err) {
		session.disk.Sequence--
		markHeartbeatTerminal(session, terminalReason(err))
		rememberSettlement(session)
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
	rt.printEstimateWarnings(response.Warnings, &session.disk.WarningAt, time.Now())
	if len(commits) > 0 {
		session.disk.CommitCursor = commits[len(commits)-1].SHA
	}
	if heartbeatUsageDue(o, session) {
		if uerr := rt.reportHeartbeatUsage(ctx, projectID, o, session); uerr != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if heartbeatTerminalStatus(uerr) {
				markHeartbeatTerminal(session, terminalReason(uerr))
				rememberSettlement(session)
				return errHeartbeatTerminal
			}
			fmt.Fprintf(rt.stderr, "heartbeat: usage report failed\n")
		}
	}
	capacityOptions := o.Capacity
	if capacityOptions.File != "" && capacityOptions.Phase == "" && !session.disk.CapacityStarted {
		capacityOptions.Phase = "start"
	}
	if reported, err := rt.reportCapacityReading(ctx, capacityOptions); err != nil {
		fmt.Fprintln(rt.stderr, "heartbeat: capacity report failed")
	} else if reported {
		session.disk.CapacityStarted = true
	}
	if o.PrintControls {
		rt.printHeartbeatControls(ctx, session.id, o.Harness, controls)
	}
	return nil
}

func proveOwnerAtStart(pid int, dep heartbeatDeps) (ownerStamp, error) {
	if dep.alive != nil {
		if !dep.alive(pid) {
			return ownerStamp{}, errOwnerGone
		}
		return ownerStamp{}, nil
	}
	return readOwnerStamp(pid)
}

func explainClosedHeartbeat(rt *runtime, session *heartbeatSession) {
	if rt == nil || session == nil || session.id == "" {
		return
	}
	reason := "closed"
	switch session.disk.TerminalReason {
	case "stopped", "archived":
		reason = session.disk.TerminalReason
	}
	fmt.Fprintf(rt.stderr, "heartbeat: session %s is %s and will not resume\n", session.id, reason)
}

func heartbeatStopIntent(raw []byte) (id string, strict bool, attempts, code int, reason string) {
	line, rest, _ := strings.Cut(string(raw), "\n")
	id = strings.ToLower(strings.TrimSpace(line))
	next, rest, _ := strings.Cut(rest, "\n")
	strict = strings.TrimSpace(next) == "strict"
	attemptLine, rest, _ := strings.Cut(rest, "\n")
	codeLine, rest, _ := strings.Cut(rest, "\n")
	reasonLine, _, _ := strings.Cut(rest, "\n")
	return id, strict, heartbeatAttemptCount(attemptLine), heartbeatAttemptCount(codeLine), persistedStopReason(reasonLine)
}

func heartbeatAttemptCount(raw string) int {
	raw = strings.TrimSpace(raw)
	n := 0
	for _, c := range raw {
		if c < '0' || c > '9' {
			return 0
		}
		n = n*10 + int(c-'0')
		if n > 1000 {
			return 1000
		}
	}
	return n
}

// stopRetryable is a failure a later start may try again.
// 403 and 409 stay retryable until a status read shows the generation
// stopped, because a rejected stop can still land on the next attempt.
// A network error has no status and is retryable.
func stopRetryable(code int) bool {
	switch code {
	case 0, http.StatusRequestTimeout, http.StatusTooManyRequests,
		http.StatusInternalServerError, http.StatusBadGateway,
		http.StatusServiceUnavailable, http.StatusGatewayTimeout,
		http.StatusForbidden, http.StatusConflict:
		return true
	default:
		return false
	}
}

// rejectDeadOwner stops a generation whose owner failed before the first beat.
// A 403 or 409 is success only when the server already shows this generation
// stopped: that rerun persists closed and exits 0. An unconfirmed rejection
// stays strict and is retried on later starts until the bound.
func (rt *runtime) rejectDeadOwner(o heartbeatOptions, session *heartbeatSession) error {
	fmt.Fprintf(rt.stderr, "heartbeat: owner %d failed the start check\n", o.OwnerPID)
	if session == nil || session.id == "" {
		return errOwnerGone
	}
	ctx, cancel := context.WithTimeout(context.Background(), heartbeatStopTimeout)
	defer cancel()
	err := rt.stopHeartbeatStrict(ctx, o.Project, *session)
	if errors.Is(err, errHeartbeatAlreadyStopped) {
		if perr := persistStopSuccess(session); perr != nil {
			return perr
		}
		explainClosedHeartbeat(rt, session)
		return nil
	}
	if err != nil {
		rememberStopIntent(session, true, 1, heartbeatStatus(err))
		_ = saveHeartbeatSession(session)
		releaseSessionIndex(session)
		return err
	}
	if err := persistStopSuccess(session); err != nil {
		return err
	}
	return errOwnerGone
}

func (rt *runtime) stopHeartbeat(ctx context.Context, project string, session heartbeatSession) error {
	return rt.stopHeartbeatMode(ctx, project, session, false)
}

func (rt *runtime) stopHeartbeatStrict(ctx context.Context, project string, session heartbeatSession) error {
	return rt.stopHeartbeatMode(ctx, project, session, true)
}

func (rt *runtime) stopHeartbeatMode(ctx context.Context, project string, session heartbeatSession, strict bool) error {
	if session.id == "" || session.lease == "" {
		return nil
	}
	projectID := session.disk.ProjectID
	if !validUUID(projectID) {
		var err error
		projectID, err = rt.harnessProjectCtx(ctx, project)
		if err != nil {
			return err
		}
	}
	reason := session.stopReason
	if reason == "" {
		reason = "stopped"
	}
	err := rt.harnessDoCtx(ctx, http.MethodPost, harnessPath(projectID, session.id)+"/stop", session.lease, map[string]string{"reason": reason}, new(any))
	if err == nil || heartbeatStatus(err) == http.StatusGone {
		return nil
	}
	code := heartbeatStatus(err)
	if code == http.StatusForbidden || code == http.StatusConflict {
		// The same 403 is "already stopped" and "proof rejected". Only a
		// status read for this generation tells them apart. A lost read
		// keeps the failure so a genuine rejection is not stored as closed.
		stopped, readErr := rt.generationStopped(ctx, projectID, session.id)
		if readErr == nil && stopped {
			return errHeartbeatAlreadyStopped
		}
		if !strict && code == http.StatusForbidden {
			return nil
		}
	}
	return err
}

// generationAlreadyStopped resolves the project, then reads the generation.
func (rt *runtime) generationAlreadyStopped(ctx context.Context, project string, session heartbeatSession) (bool, error) {
	projectID := session.disk.ProjectID
	if !validUUID(projectID) {
		var err error
		projectID, err = rt.harnessProjectCtx(ctx, project)
		if err != nil {
			return false, err
		}
	}
	return rt.generationStopped(ctx, projectID, session.id)
}

// generationStopped reports whether this generation is already closed on the
// server. The body id must match: a status without it, or for another
// generation, is not confirmation.
func (rt *runtime) generationStopped(ctx context.Context, projectID, sessionID string) (bool, error) {
	var status struct {
		ID         string     `json:"id"`
		Phase      string     `json:"phase"`
		StoppedAt  *time.Time `json:"stopped_at"`
		ArchivedAt *time.Time `json:"archived_at"`
	}
	if err := rt.harnessDoCtx(ctx, http.MethodGet, harnessPath(projectID, sessionID), "", nil, &status); err != nil {
		return false, err
	}
	if !strings.EqualFold(status.ID, sessionID) {
		return false, nil
	}
	if status.ArchivedAt != nil || status.StoppedAt != nil || strings.EqualFold(status.Phase, "stopped") {
		return true, nil
	}
	return false, nil
}

type heartbeatControl struct {
	ID                 string     `json:"id"`
	SessionID          string     `json:"session_id"`
	ExpectedGeneration string     `json:"expected_generation,omitempty"`
	Kind               string     `json:"kind"`
	State              string     `json:"state"`
	Sequence           int64      `json:"sequence"`
	Outcome            string     `json:"outcome,omitempty"`
	ExpiresAt          *time.Time `json:"expires_at,omitempty"`
	Payload            struct {
		DisplayLabel    string `json:"display_label,omitempty"`
		Model           string `json:"model,omitempty"`
		ReasoningEffort string `json:"reasoning_effort,omitempty"`
		AccountID       string `json:"account_id,omitempty"`
		ModelProfileID  string `json:"model_profile_id,omitempty"`
	} `json:"request_payload"`
}

func (rt *runtime) readHeartbeatControls(ctx context.Context, projectID, sessionID string) []heartbeatControl {
	var status struct {
		Controls []heartbeatControl `json:"controls"`
	}
	if err := rt.harnessDoCtx(ctx, http.MethodGet, harnessPath(projectID, sessionID), "", nil, &status); err != nil {
		return nil
	}
	return status.Controls
}

func heartbeatSessionRequest(c heartbeatControl, sessionID string) bool {
	return (c.Kind == "rename_request" || c.Kind == "model_request") && validUUID(c.ID) &&
		strings.EqualFold(c.SessionID, sessionID) && strings.EqualFold(c.ExpectedGeneration, sessionID) && c.Sequence > 0 && c.ExpiresAt != nil
}

// Completion is a report by the owning harness, never an instruction to mutate
// the local harness. Pending requests only get printed; nothing executes them.
func applyHeartbeatRequests(o heartbeatOptions, dep heartbeatDeps, session *heartbeatSession, controls []heartbeatControl, ctx context.Context) {
	sort.Slice(controls, func(i, j int) bool { return controls[i].Sequence < controls[j].Sequence })
	for _, c := range controls {
		if !heartbeatSessionRequest(c, session.id) || c.State != "completed" || c.Outcome != "applied" {
			continue
		}
		if c.Kind == "rename_request" {
			if c.Sequence <= session.disk.AppliedRenameSequence {
				continue
			}
			session.disk.AppliedRenameSequence = c.Sequence
			label := c.Payload.DisplayLabel
			if !sessionrequest.ValidLabel(label) {
				continue
			}
			session.disk.RequestedLabel = label
			session.disk.RequestedLabelSource, _ = resolveHeartbeatLabel(ctx, o, dep, false)
		} else {
			if c.Sequence <= session.disk.AppliedModelSequence {
				continue
			}
			session.disk.AppliedModelSequence = c.Sequence
			model, effort := c.Payload.Model, c.Payload.ReasoningEffort
			if !sessionrequest.ValidModel(o.Harness, model, effort) {
				continue
			}
			session.disk.RequestedModel, session.disk.RequestedEffort = model, effort
			session.disk.ModelFlagAtRequest, session.disk.EffortFlagAtRequest = o.Model, o.Effort
		}
	}
}

func (rt *runtime) printHeartbeatControls(ctx context.Context, sessionID, harness string, controls []heartbeatControl) {
	var profiles []struct {
		ID      string `json:"id"`
		Harness string `json:"harness"`
		Model   string `json:"model"`
		Effort  string `json:"effort"`
		Enabled bool   `json:"enabled"`
	}
	catalogRead := false
	for _, c := range controls {
		if c.State != "pending" && c.State != "claimed" || !validUUID(c.ID) {
			continue
		}
		if c.Kind == "rename_request" || c.Kind == "model_request" {
			if !heartbeatSessionRequest(c, sessionID) || !c.ExpiresAt.After(time.Now()) {
				continue
			}
			if c.Kind == "rename_request" {
				if !sessionrequest.ValidLabel(c.Payload.DisplayLabel) || c.Payload.Model != "" || c.Payload.ReasoningEffort != "" || c.Payload.AccountID != "" || c.Payload.ModelProfileID != "" {
					continue
				}
			} else {
				if c.Payload.DisplayLabel != "" || !validUUID(c.Payload.AccountID) || !validUUID(c.Payload.ModelProfileID) || !sessionrequest.ValidModel(harness, c.Payload.Model, c.Payload.ReasoningEffort) {
					continue
				}
				if !catalogRead {
					catalogRead = true
					if err := rt.doCtx(ctx, http.MethodGet, "/api/models", nil, &profiles); err != nil {
						profiles = nil // Fail closed; a later beat retries the catalog.
					}
				}
				matched := false
				for _, p := range profiles {
					if strings.EqualFold(p.ID, c.Payload.ModelProfileID) && p.Enabled && p.Harness == harness && p.Model == c.Payload.Model && p.Effort == c.Payload.ReasoningEffort {
						matched = true
						break
					}
				}
				if !matched {
					continue
				}
			}
			record := struct {
				Type   string `json:"type"`
				Schema string `json:"schema"`
				heartbeatControl
			}{"request", "aeon.session-request.v1", c}
			_ = json.NewEncoder(rt.stdout).Encode(record)
		} else {
			kind, state := heartbeatText(c.Kind, 40), heartbeatText(c.State, 40)
			if kind == "" || state == "" {
				continue
			}
			if rt.jsonOut {
				_ = json.NewEncoder(rt.stdout).Encode(map[string]string{"type": "control", "id": strings.ToLower(c.ID), "kind": kind, "state": state})
			} else {
				fmt.Fprintf(rt.stdout, "control %s %s %s\n", strings.ToLower(c.ID), kind, state)
			}
		}
	}
	var page struct {
		Items []struct {
			ID        string  `json:"id"`
			AckedAt   *string `json:"acked_at"`
			SessionID string  `json:"recipient_session_id"`
		} `json:"items"`
	}
	// ?session= returns this generation's bound messages too and records it as
	// listening; without it session-bound messages never showed (AEON-280).
	if err := rt.doCtx(ctx, http.MethodGet, "/api/inbox/messages?wait_ms=0&session="+url.QueryEscape(sessionID), nil, &page); err != nil {
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
		if rt.jsonOut {
			_ = json.NewEncoder(rt.stdout).Encode(map[string]string{"type": "message", "id": strings.ToLower(item.ID)})
		} else {
			fmt.Fprintf(rt.stdout, "message %s\n", strings.ToLower(item.ID))
		}
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

type agentStatus struct {
	Note       string
	Progress   *int
	Remaining  *float64
	ModifiedAt time.Time
}

func readAgentStatus(o heartbeatOptions) (agentStatus, bool) {
	path, kind := o.StatusFile, harnessExplicitStatus
	if path == "" {
		if o.Role != "worker" || strings.TrimSpace(o.Worktree) == "" {
			return agentStatus{}, true
		}
		path, kind = filepath.Join(o.Worktree, ".agent-status.json"), harnessAgentStatus
	}
	f, err := openHarnessFile(kind, path)
	if err != nil {
		return agentStatus{}, false
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil || st.Size() > 65536 {
		return agentStatus{}, false
	}
	raw := make([]byte, st.Size())
	if _, err := io.ReadFull(f, raw); err != nil {
		return agentStatus{}, false
	}
	var doc struct {
		Note      string          `json:"note"`
		Pct       json.RawMessage `json:"pct"`
		Remaining json.RawMessage `json:"remaining_min"`
	}
	if json.Unmarshal(raw, &doc) != nil || string(bytesTrim(raw)) == "null" {
		return agentStatus{}, false
	}
	status := agentStatus{Note: heartbeatNote(doc.Note), ModifiedAt: st.ModTime().UTC()}
	valid := true
	if len(doc.Pct) > 0 && string(doc.Pct) != "null" {
		var n int
		if json.Unmarshal(doc.Pct, &n) != nil || n < 0 || n > 100 {
			valid = false
		} else {
			status.Progress = &n
		}
	}
	if len(doc.Remaining) > 0 && string(doc.Remaining) != "null" {
		var minutes float64
		if json.Unmarshal(doc.Remaining, &minutes) != nil || math.IsNaN(minutes) || math.IsInf(minutes, 0) || minutes < 0 || minutes > heartbeatRemainingMax {
			valid = false
		} else {
			status.Remaining = &minutes
		}
	}
	return status, valid
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
