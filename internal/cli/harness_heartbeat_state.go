// SPDX-License-Identifier: AGPL-3.0-only

package cli

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"time"
)

// heartbeatHold is the exclusive owner of one private state directory.
// The lock is held until release, which is process exit for a live helper.
type heartbeatHold struct {
	dir  *os.File
	lock *os.File
}

type heartbeatDisk struct {
	ModelSent             bool                    `json:"model_sent,omitempty"`
	SentModel             string                  `json:"sent_model,omitempty"`
	SentEffort            string                  `json:"sent_effort,omitempty"`
	ActivityMode          string                  `json:"agent_activity_mode,omitempty"`
	WarningAt             map[string]time.Time    `json:"warning_at,omitempty"`
	CapacityStarted       bool                    `json:"capacity_started,omitempty"`
	AppliedModelSequence  int64                   `json:"applied_model_sequence,omitempty"`
	AppliedRenameSequence int64                   `json:"applied_rename_sequence,omitempty"`
	RequestedLabel        string                  `json:"requested_label,omitempty"`
	RequestedLabelSource  string                  `json:"requested_label_source,omitempty"`
	RequestedModel        string                  `json:"requested_model,omitempty"`
	RequestedEffort       string                  `json:"requested_effort,omitempty"`
	ModelFlagAtRequest    string                  `json:"model_flag_at_request,omitempty"`
	EffortFlagAtRequest   string                  `json:"effort_flag_at_request,omitempty"`
	Schema                string                  `json:"schema"`
	SessionID             string                  `json:"session_id"`
	ProjectID             string                  `json:"project_id,omitempty"`
	Sequence              int64                   `json:"sequence"`
	LabelSent             bool                    `json:"label_sent"`
	SentLabel             string                  `json:"sent_label,omitempty"`
	SentCommits           []string                `json:"sent_commits,omitempty"`
	StartRev              string                  `json:"start_rev,omitempty"`
	Usage                 []heartbeatUsageDisk    `json:"usage,omitempty"`
	UsagePath             string                  `json:"usage_path,omitempty"`
	UsageSource           string                  `json:"usage_source,omitempty"`
	PendingUsage          []heartbeatPendingUsage `json:"pending_usage,omitempty"`
	UsageOffset           int64                   `json:"usage_offset,omitempty"`
	UsageRecent           []string                `json:"usage_recent,omitempty"`
	UsageDiscard          bool                    `json:"usage_discard,omitempty"`
	UsageCodex            *heartbeatCodexCursor   `json:"usage_codex,omitempty"`
	OwnerPID              int                     `json:"owner_pid,omitempty"`
	OwnerStart            string                  `json:"owner_start,omitempty"`
	BoundWorktree         string                  `json:"bound_worktree,omitempty"`
	BoundBranch           string                  `json:"bound_branch,omitempty"`
	BoundTicket           string                  `json:"bound_ticket,omitempty"`
	RegisteredAt          time.Time               `json:"registered_at,omitempty"`
	StartedUnix           int64                   `json:"started_unix,omitempty"`
	CommitCursor          string                  `json:"commit_cursor,omitempty"`
	Terminal              bool                    `json:"terminal,omitempty"`
	TerminalReason        string                  `json:"terminal_reason,omitempty"`
	Closed                bool                    `json:"closed,omitempty"`
	SourcesRecorded       bool                    `json:"sources_recorded,omitempty"`

	// The observation fence survives clearing RequestedModel and helper
	// restarts. Keep the existing JSON key for compatibility with saved state.
	RequestedModelBaseline *heartbeatModelBaseline `json:"requested_model_baseline,omitempty"`
}

// heartbeatCodexCursor is the Codex scan context that must survive between
// beats, committed together with the usage offset: the model named by the
// latest turn_context and the session-wide cumulative totals already
// attributed to some model. A token record is attributed as its increase
// over these totals, so a model switch never re-counts earlier tokens.
type heartbeatCodexCursor struct {
	Model     string `json:"model,omitempty"`
	Input     int64  `json:"input"`
	Output    int64  `json:"output"`
	Cached    int64  `json:"cached"`
	Reasoning int64  `json:"reasoning"`
	// ReasoningKnown is false once a record omitted reasoning: the next
	// reported total re-establishes the baseline without attributing it.
	ReasoningKnown bool `json:"reasoning_known,omitempty"`
}

type heartbeatUsageDisk struct {
	Model     string `json:"model"`
	Sequence  int64  `json:"sequence"`
	Input     int64  `json:"input"`
	Output    int64  `json:"output"`
	Cached    int64  `json:"cached"`
	Reasoning *int64 `json:"reasoning,omitempty"`
}

// heartbeatPendingUsage is the exact report persisted before it is posted.
// A lost response replays these bytes; the transcript is not reread into a new id.
type heartbeatPendingUsage struct {
	Model             string                `json:"model"`
	Sequence          int64                 `json:"sequence"`
	Input             int64                 `json:"input"`
	Output            int64                 `json:"output"`
	Cached            int64                 `json:"cached"`
	Reasoning         *int64                `json:"reasoning,omitempty"`
	ReportID          string                `json:"report_id"`
	Offset            int64                 `json:"offset"`
	Recent            []string              `json:"recent,omitempty"`
	Discard           bool                  `json:"discard,omitempty"`
	Codex             *heartbeatCodexCursor `json:"codex,omitempty"`
	AccountID         string                `json:"account_id,omitempty"`
	BillingMode       string                `json:"billing_mode,omitempty"`
	SubscriptionLabel string                `json:"subscription_label,omitempty"`
}

type heartbeatSession struct {
	// pauseWakeAt is derived from a DB-clock hint and never persisted or used as signal authority.
	pauseWakeAt time.Time
	id          string
	lease       string
	disk        heartbeatDisk
	hold        heartbeatHold
	// stopReason is how the wrapped job ended. A stop that did not land keeps it in
	// stop.intent, so the replay names the same ending (AEON-437). Empty is a plain stop.
	stopReason    string
	subagentScan  *os.File
	subagentNames []string
}

func validStateName(name string) bool {
	switch name {
	case "session.id", "state.json", "lease.key", "session.ref", "stop.intent", "settle.intent", "heartbeat.lock", "activity.json", "activity-mode.json", "subagent.json", "subagent.stop":
		return true
	default:
		return false
	}
}

func loadHeartbeatSession(hold *heartbeatHold) (heartbeatSession, bool, error) {
	raw, err := hold.readFile("session.id", 256)
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
	lease, err := readStateSecret(hold, "lease.key")
	if err != nil {
		return heartbeatSession{}, false, err
	}
	disk := heartbeatDisk{Schema: heartbeatSchema, SessionID: id}
	stateRaw, err := hold.readFile("state.json", 1<<20)
	if err == nil {
		if json.Unmarshal(stateRaw, &disk) != nil || disk.Schema != heartbeatSchema || !strings.EqualFold(disk.SessionID, id) {
			return heartbeatSession{}, false, usagef("heartbeat state does not match the session id")
		}
		disk.SessionID = id
	} else if !errors.Is(err, os.ErrNotExist) {
		return heartbeatSession{}, false, err
	}
	return heartbeatSession{id: id, lease: lease, disk: disk, hold: *hold}, true, nil
}

func saveHeartbeatSession(session *heartbeatSession) error {
	if session == nil || session.hold.dir == nil {
		return errHeartbeatState
	}
	session.disk.Schema = heartbeatSchema
	session.disk.SessionID = session.id
	raw, err := json.Marshal(session.disk)
	if err != nil {
		return err
	}
	if err := session.hold.writeFile("session.id", []byte(session.id+"\n")); err != nil {
		return err
	}
	return session.hold.writeFile("state.json", append(raw, '\n'))
}

func readStateSecret(hold *heartbeatHold, name string) (string, error) {
	raw, err := hold.readFile(name, 8192)
	if err != nil {
		return "", err
	}
	value := strings.TrimSpace(string(raw))
	if len(value) < 32 || strings.ContainsAny(value, "\r\n") {
		return "", usagef("%s must contain one value", name)
	}
	return value, nil
}

func readOrCreateStateSecret(hold *heartbeatHold, name string, n int) (string, error) {
	value, err := readStateSecret(hold, name)
	if err == nil {
		return value, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	value = hex.EncodeToString(buf)
	if err := hold.writeFile(name, []byte(value+"\n")); err != nil {
		return "", err
	}
	return value, nil
}

func clearHeartbeatIdentity(hold *heartbeatHold) error {
	var first error
	for _, name := range []string{"settle.intent", "stop.intent", "session.id", "state.json", "session.ref", "lease.key"} {
		if err := hold.remove(name); err != nil && first == nil {
			first = err
		}
	}
	return first
}
