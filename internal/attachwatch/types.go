// SPDX-License-Identifier: AGPL-3.0-only

// Package attachwatch carries the content-free immutable approval protocol.
// Conversation text is never part of a snapshot, journal, event or heartbeat.
package attachwatch

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"path"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

const MaxText = 16 << 10
const Lease = 60 * time.Second

type Process struct {
	PID        int    `json:"pid"`
	UID        int    `json:"uid"`
	Started    string `json:"started"`
	Executable string `json:"executable"`
	CWD        string `json:"cwd"`
}
type Snapshot struct {
	ComputerID string  `json:"computer_id"`
	ProjectID  string  `json:"project_id"`
	TicketID   string  `json:"ticket_id"`
	Host       string  `json:"host"`
	Harness    string  `json:"harness"`
	Process    Process `json:"process"`
	Transcript string  `json:"transcript"`
	FileID     string  `json:"file_id"`
}

func (s Snapshot) Digest() string {
	b, _ := json.Marshal(s)
	h := sha256.Sum256(append([]byte("aeon.attach.v1\x00"), b...))
	return hex.EncodeToString(h[:])
}
func Text(s string, max int) bool {
	return s != "" && len(s) <= max && utf8.ValidString(s) && !strings.ContainsFunc(s, unicode.IsControl)
}
func PhysicalPath(s string) bool {
	return Text(s, 1024) && path.IsAbs(s) && path.Clean(s) == s && s != "/"
}
func Within(root, cwd string) bool {
	return PhysicalPath(root) && PhysicalPath(cwd) && (cwd == root || strings.HasPrefix(cwd, root+"/"))
}
func (s Snapshot) Valid() bool {
	return Text(s.Host, 128) && Text(s.FileID, 128) && s.Process.PID > 0 && s.Process.UID >= 0 && Text(s.Process.Started, 128) && PhysicalPath(s.Process.Executable) && PhysicalPath(s.Process.CWD) && PhysicalPath(s.Transcript) && (s.Harness == "codex" || s.Harness == "claude" || s.Harness == "cursor" || s.Harness == "grok")
}

type DeviceRequest struct {
	Operation   string   `json:"operation"`
	RequestID   string   `json:"request_id"`
	ComputerID  string   `json:"computer_id"`
	DeviceProof string   `json:"device_proof"`
	Snapshot    Snapshot `json:"snapshot"`
	Digest      string   `json:"request_digest"`
	Sequence    int64    `json:"sequence"`
	Text        string   `json:"text,omitempty"`
}
type View struct {
	RequestID  string     `json:"request_id"`
	Digest     string     `json:"request_digest"`
	Snapshot   Snapshot   `json:"snapshot"`
	State      string     `json:"state"`
	ExpiresAt  time.Time  `json:"expires_at"`
	LeaseUntil *time.Time `json:"lease_until"`
	SessionID  *string    `json:"session_id"`
	UserCode   string     `json:"user_code,omitempty"`
}
