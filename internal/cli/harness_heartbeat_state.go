// SPDX-License-Identifier: AGPL-3.0-only

package cli

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"strings"
)

// heartbeatHold is the exclusive owner of one private state directory.
// The lock is held until release, which is process exit for a live helper.
type heartbeatHold struct {
	dir  *os.File
	lock *os.File
}

type heartbeatDisk struct {
	Schema          string                  `json:"schema"`
	SessionID       string                  `json:"session_id"`
	ProjectID       string                  `json:"project_id,omitempty"`
	Sequence        int64                   `json:"sequence"`
	LabelSent       bool                    `json:"label_sent"`
	SentLabel       string                  `json:"sent_label,omitempty"`
	SentCommits     []string                `json:"sent_commits,omitempty"`
	StartRev        string                  `json:"start_rev,omitempty"`
	Usage           []heartbeatUsageDisk    `json:"usage,omitempty"`
	PendingUsage    []heartbeatPendingUsage `json:"pending_usage,omitempty"`
	UsageOffset     int64                   `json:"usage_offset,omitempty"`
	UsageRecent     []string                `json:"usage_recent,omitempty"`
	UsageDiscard    bool                    `json:"usage_discard,omitempty"`
	OwnerPID        int                     `json:"owner_pid,omitempty"`
	OwnerStart      string                  `json:"owner_start,omitempty"`
	BoundWorktree   string                  `json:"bound_worktree,omitempty"`
	BoundBranch     string                  `json:"bound_branch,omitempty"`
	StartedUnix     int64                   `json:"started_unix,omitempty"`
	CommitCursor    string                  `json:"commit_cursor,omitempty"`
	Terminal        bool                    `json:"terminal,omitempty"`
	TerminalReason  string                  `json:"terminal_reason,omitempty"`
	Closed          bool                    `json:"closed,omitempty"`
	SourcesRecorded bool                    `json:"sources_recorded,omitempty"`
}

type heartbeatUsageDisk struct {
	Model    string `json:"model"`
	Sequence int64  `json:"sequence"`
	Input    int64  `json:"input"`
	Output   int64  `json:"output"`
	Cached   int64  `json:"cached"`
}

// heartbeatPendingUsage is the exact report persisted before it is posted.
// A lost response replays these bytes; the transcript is not reread into a new id.
type heartbeatPendingUsage struct {
	Model    string   `json:"model"`
	Sequence int64    `json:"sequence"`
	Input    int64    `json:"input"`
	Output   int64    `json:"output"`
	Cached   int64    `json:"cached"`
	ReportID string   `json:"report_id"`
	Offset   int64    `json:"offset"`
	Recent   []string `json:"recent,omitempty"`
	Discard  bool     `json:"discard,omitempty"`
}

type heartbeatSession struct {
	id    string
	lease string
	disk  heartbeatDisk
	hold  heartbeatHold
}

func validStateName(name string) bool {
	switch name {
	case "session.id", "state.json", "lease.key", "session.ref", "stop.intent", "settle.intent", "heartbeat.lock":
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
