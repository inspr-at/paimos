// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

// Package lifecycleintents owns browser authorization and durable lifecycle
// outcomes. Local process effects belong exclusively to the authenticated daemon.
package lifecycleintents

import (
	"errors"
	"regexp"
	"strings"

	"github.com/google/uuid"
	"github.com/inspr-at/paimos/backend/dispatchprofile"
)

const RuntimeLeaseHeader = "X-Paimos-Runtime-Lease"
const HarnessLeaseHeader = "X-Paimos-Harness-Worker-Lease"
const RuntimeTTLSeconds = 120

var (
	ErrInvalid     = errors.New("lifecycle_invalid")
	ErrUnavailable = errors.New("lifecycle_unavailable")
	ErrConflict    = errors.New("lifecycle_conflict")
	ErrStorage     = errors.New("lifecycle_storage_unavailable")
	stable         = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)
	identity       = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

type Workspace struct {
	Handle   string `json:"handle"`
	Identity string `json:"identity"`
}
type Profile struct {
	ID      string `json:"id"`
	Version string `json:"version"`
}
type Registration struct {
	Generation   string      `json:"generation"`
	Host         string      `json:"host"`
	AccountLabel string      `json:"account_label"`
	Workspaces   []Workspace `json:"workspaces"`
	Profiles     []Profile   `json:"profiles"`
}
type Runtime struct {
	ID           string                `json:"id"`
	ProjectID    int64                 `json:"project_id"`
	Generation   string                `json:"generation"`
	MachineID    string                `json:"machine_id"`
	AccountLabel string                `json:"account_label"`
	Workspaces   []Workspace           `json:"workspaces"`
	Profiles     []Profile             `json:"profiles"`
	ExpiresAt    string                `json:"expires_at"`
	Sessions     []SessionRegistration `json:"sessions"`
}
type Request struct {
	RequestKey             string  `json:"request_key"`
	Operation              string  `json:"operation"`
	RuntimeID              string  `json:"runtime_id"`
	RuntimeGeneration      string  `json:"runtime_generation"`
	AccountLabel           string  `json:"account_label"`
	TTLSeconds             int     `json:"ttl_seconds"`
	WorkspaceHandle        string  `json:"workspace_handle,omitempty"`
	AgentName              string  `json:"agent_name,omitempty"`
	DispatchProfileID      string  `json:"dispatch_profile_id,omitempty"`
	DispatchProfileVersion string  `json:"dispatch_profile_version,omitempty"`
	TicketID               *int64  `json:"ticket_id,omitempty"`
	WorkShape              string  `json:"work_shape,omitempty"`
	Role                   string  `json:"role,omitempty"`
	ParentSessionID        *string `json:"parent_harness_session_id,omitempty"`
	SessionID              string  `json:"session_id,omitempty"`
	SessionGeneration      string  `json:"session_generation,omitempty"`
	ExpectedRevision       int64   `json:"expected_revision,omitempty"`
	RepairLayer            string  `json:"repair_layer,omitempty"`
}
type Intent struct {
	SchemaVersion   int     `json:"schema_version"`
	ID              string  `json:"id"`
	ProjectID       int64   `json:"project_id"`
	Request         Request `json:"request"`
	State           string  `json:"state"`
	Revision        int64   `json:"revision"`
	CreatedAt       string  `json:"created_at"`
	ExpiresAt       string  `json:"expires_at"`
	UpdatedAt       string  `json:"updated_at"`
	NewGeneration   string  `json:"new_generation,omitempty"`
	ResultSessionID string  `json:"result_session_id,omitempty"`
	Reason          string  `json:"reason"`
}
type Event struct {
	Revision  int64  `json:"revision"`
	State     string `json:"state"`
	Reason    string `json:"reason"`
	CreatedAt string `json:"created_at"`
}
type SessionRegistration struct {
	SessionID  string `json:"session_id"`
	Generation string `json:"generation"`
}
type Transition struct {
	RuntimeID         string `json:"runtime_id"`
	RuntimeGeneration string `json:"runtime_generation"`
	ExpectedRevision  int64  `json:"expected_revision"`
	State             string `json:"state"`
	Reason            string `json:"reason"`
	ResultSessionID   string `json:"result_session_id,omitempty"`
}

func validID(v string) bool {
	return len(v) == 36 && uuid.Validate(v) == nil && v == strings.ToLower(v)
}

func label(v string, max int) bool { return len(v) > 0 && len(v) <= max && stable.MatchString(v) }
func resolveProfile(id, version string) (dispatchprofile.Profile, error) {
	for _, p := range dispatchprofile.List() {
		if p.ID == id && p.Version == version {
			return p, nil
		}
	}
	return dispatchprofile.Profile{}, ErrUnavailable
}

// Match the shipped managedharness/local probe vocabulary, excluding unknown.
func validAccount(value string) bool {
	switch value {
	case "chatgpt", "api_key", "claude_ai_max", "claude_ai_pro", "claude_ai_team", "claude_ai_enterprise", "console":
		return true
	}
	return false
}
func (r Request) validate() error {
	if !validID(r.RequestKey) || !validID(r.RuntimeID) || !validID(r.RuntimeGeneration) || !validAccount(r.AccountLabel) || r.TTLSeconds < 30 || r.TTLSeconds > 600 {
		return ErrInvalid
	}
	if r.Operation == "repair" {
		if (r.RepairLayer != "reporter" && r.RepairLayer != "listeners") || r.WorkspaceHandle != "" || r.AgentName != "" || r.DispatchProfileID != "" || r.DispatchProfileVersion != "" || r.TicketID != nil || r.WorkShape != "" || r.Role != "" || r.ParentSessionID != nil || r.SessionID != "" || r.SessionGeneration != "" || r.ExpectedRevision != 0 {
			return ErrInvalid
		}
		return nil
	}
	switch r.Operation {
	case "start", "attach", "reassign", "restart":
	default:
		return ErrInvalid
	}
	if !validID(r.WorkspaceHandle) || !label(r.AgentName, 64) || !label(r.DispatchProfileID, 128) || !label(r.DispatchProfileVersion, 128) || r.RepairLayer != "" || (r.Role != "worker" && r.Role != "coordinator") {
		return ErrInvalid
	}
	if r.TicketID == nil {
		if r.WorkShape != "unknown" {
			return ErrInvalid
		}
	} else if *r.TicketID <= 0 || (r.WorkShape != "ship" && r.WorkShape != "scout") {
		return ErrInvalid
	}
	if r.ParentSessionID != nil && !validID(*r.ParentSessionID) {
		return ErrInvalid
	}
	if r.Operation == "start" {
		if r.SessionID != "" || r.SessionGeneration != "" || r.ExpectedRevision != 0 {
			return ErrInvalid
		}
	} else if !validID(r.SessionID) || !validID(r.SessionGeneration) || r.ExpectedRevision < 1 {
		return ErrInvalid
	}
	return nil
}
func terminal(state string) bool {
	return state == "completed" || state == "failed" || state == "expired" || state == "cancelled"
}
