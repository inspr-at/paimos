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
	"github.com/inspr-at/paimos/backend/safetext"
)

const RuntimeLeaseHeader = "X-Paimos-Runtime-Lease"
const HarnessLeaseHeader = "X-Paimos-Harness-Worker-Lease"
const RuntimeTTLSeconds = 120

var (
	ErrInvalid        = errors.New("lifecycle_invalid")
	ErrUnavailable    = errors.New("lifecycle_unavailable")
	ErrConflict       = errors.New("lifecycle_conflict")
	ErrStorage        = errors.New("lifecycle_storage_unavailable")
	stable            = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)
	contentDigestBody = regexp.MustCompile(`^[0-9a-f]{64}$`)
	readinessReason   = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)
	accountKey        = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`)
	identity          = regexp.MustCompile(`^[0-9a-f]{64}$`)
	workspaceLabel    = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9 ._-]{0,47}$`)
)

const (
	RuntimeSchemaV1       = 1
	AccountChoiceSchemaV2 = 2
	maxAdvertisedAccounts = 16
)

type Workspace struct {
	Handle   string `json:"handle"`
	Identity string `json:"identity"`
	Label    string `json:"label,omitempty"`
}
type Profile struct {
	ID      string `json:"id"`
	Version string `json:"version"`
}

// AccountChoice is a non-secret advertised named-account selection. Key and
// operator label are distinct from account class, harness, model, worker id
// and runtime generation. Homes, emails and credentials never appear here.
type AccountChoice struct {
	Key   string `json:"key"`
	Label string `json:"label"`
}
type Registration struct {
	Generation    string          `json:"generation"`
	Host          string          `json:"host"`
	AccountLabel  string          `json:"account_label"`
	Accounts      []AccountChoice `json:"accounts,omitempty"`
	Workspaces    []Workspace     `json:"workspaces"`
	Profiles      []Profile       `json:"profiles"`
	SchemaVersion int             `json:"schema_version,omitempty"`
}
type Runtime struct {
	ID            string              `json:"id"`
	ProjectID     int64               `json:"project_id"`
	Generation    string              `json:"generation"`
	MachineID     string              `json:"machine_id"`
	AccountLabel  string              `json:"account_label"`
	Accounts      []AccountChoice     `json:"accounts,omitempty"`
	Workspaces    []Workspace         `json:"workspaces"`
	Profiles      []Profile           `json:"profiles"`
	ExpiresAt     string              `json:"expires_at"`
	Sessions      []SessionProjection `json:"sessions"`
	SchemaVersion int                 `json:"schema_version,omitempty"`
}

// ReadinessContractVersion is the inspr readiness evidence contract this
// authority accepts from an owned daemon. Reports naming any other version are
// refused; an observation is never inferred from a registration advertisement.
const ReadinessContractVersion = "inspr.readiness.v1"

// ReadinessTTLSeconds bounds how long one owned observation may be reused. The
// stored deadline is additionally clamped to the runtime registration and the
// intent, so evidence can never outlive the ownership that produced it.
const ReadinessTTLSeconds = 300

// RequiredReadinessChecks must all be present and passing before a report may
// call itself ready. They are the required set of the accepted contract
// profile; dispatch_profile stays optional.
var RequiredReadinessChecks = []string{
	"host_kind",
	"activated_generation",
	"doctrine_loader",
	"workspace_isolation",
	"tool_prerequisites",
	"paimos_runtime_doctor",
	"paimos_account",
}

var optionalReadinessChecks = []string{"dispatch_profile"}

// ReadinessCheck is one bounded, closed-vocabulary probe outcome. Reasons are
// closed codes; raw paths, executables, environments and output never appear.
type ReadinessCheck struct {
	ID     string `json:"id"`
	Status string `json:"status"`
	Reason string `json:"reason"`
	Digest string `json:"digest,omitempty"`
}

// ReadinessReport is the daemon-reported observation attached to a completed
// readiness intent. ObservedAt is the daemon's own observation time, not a
// server clock reading, and the server refuses reports it cannot bind to the
// exact runtime, account, profile, workspace and baseline it authorized.
type ReadinessReport struct {
	ContractVersion   string           `json:"contract_version"`
	Status            string           `json:"status"`
	ObservedAt        string           `json:"observed_at"`
	TTLSeconds        int              `json:"ttl_seconds"`
	HostKind          string           `json:"host_kind"`
	NextAction        string           `json:"next_action"`
	WorkspaceIdentity string           `json:"workspace_identity"`
	AccountKey        string           `json:"account_key,omitempty"`
	BaselineDigest    string           `json:"baseline_digest"`
	Checks            []ReadinessCheck `json:"checks"`
}

// ReadinessObservation is the durable projection of one accepted report.
type ReadinessObservation struct {
	IntentID          string           `json:"intent_id"`
	ProjectID         int64            `json:"project_id"`
	RuntimeID         string           `json:"runtime_id"`
	RuntimeGeneration string           `json:"runtime_generation"`
	AccountLabel      string           `json:"account_label"`
	AccountKey        string           `json:"account_key,omitempty"`
	ProfileID         string           `json:"dispatch_profile_id"`
	ProfileVersion    string           `json:"dispatch_profile_version"`
	WorkspaceHandle   string           `json:"workspace_handle"`
	WorkspaceIdentity string           `json:"workspace_identity"`
	BaselineDigest    string           `json:"baseline_digest"`
	HostKind          string           `json:"host_kind"`
	Status            string           `json:"status"`
	NextAction        string           `json:"next_action"`
	ObservedAt        string           `json:"observed_at"`
	ExpiresAt         string           `json:"expires_at"`
	Checks            []ReadinessCheck `json:"checks"`
}

type Request struct {
	RequestKey             string  `json:"request_key"`
	Operation              string  `json:"operation"`
	BaselineDigest         string  `json:"baseline_digest,omitempty"`
	RuntimeID              string  `json:"runtime_id"`
	RuntimeGeneration      string  `json:"runtime_generation"`
	AccountLabel           string  `json:"account_label"`
	AccountKey             string  `json:"account_key,omitempty"`
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

// WorkspaceHandle is derived from current server-held provenance. It is never
// accepted as evidence in a daemon's session-registration request.
type SessionProjection struct {
	SessionID       string `json:"session_id"`
	Generation      string `json:"generation"`
	WorkspaceHandle string `json:"workspace_handle,omitempty"`
}

func validWorkspaceLabel(v string) bool {
	return v == "" || (v == strings.TrimSpace(v) && workspaceLabel.MatchString(v) && !safetext.ContainsSecretLike(v))
}

type Transition struct {
	RuntimeID         string           `json:"runtime_id"`
	RuntimeGeneration string           `json:"runtime_generation"`
	ExpectedRevision  int64            `json:"expected_revision"`
	State             string           `json:"state"`
	Reason            string           `json:"reason"`
	ResultSessionID   string           `json:"result_session_id,omitempty"`
	Readiness         *ReadinessReport `json:"readiness,omitempty"`
}

// validateReadinessReport applies the closed contract before any observation can
// be stored. Shape, vocabulary and required-check coverage are all server-side:
// a daemon cannot promote itself to ready by asserting a status string.
func validateReadinessReport(in *ReadinessReport) error {
	if in == nil || in.ContractVersion != ReadinessContractVersion {
		return ErrInvalid
	}
	switch in.Status {
	case "ready", "needs_setup", "unavailable":
	default:
		return ErrInvalid
	}
	if !readinessReason.MatchString(in.NextAction) || in.TTLSeconds < 30 || in.TTLSeconds > ReadinessTTLSeconds {
		return ErrInvalid
	}
	if !identity.MatchString(in.WorkspaceIdentity) || !ValidBaselineDigest(in.BaselineDigest) {
		return ErrInvalid
	}
	if in.HostKind != "macos-home-manager" && in.HostKind != "nixos-home-manager" {
		return ErrInvalid
	}
	if in.AccountKey != "" && !validAccountKey(in.AccountKey) {
		return ErrInvalid
	}
	if len(in.Checks) == 0 || len(in.Checks) > len(RequiredReadinessChecks)+len(optionalReadinessChecks) {
		return ErrInvalid
	}
	known := map[string]bool{}
	for _, id := range append(append([]string{}, RequiredReadinessChecks...), optionalReadinessChecks...) {
		known[id] = true
	}
	passed := map[string]bool{}
	seen := map[string]bool{}
	for _, check := range in.Checks {
		if !known[check.ID] || seen[check.ID] || !readinessReason.MatchString(check.Reason) {
			return ErrInvalid
		}
		seen[check.ID] = true
		switch check.Status {
		case "pass", "fail", "warn", "unknown", "unsupported", "stale":
		default:
			return ErrInvalid
		}
		if check.Digest != "" && !ValidBaselineDigest(check.Digest) {
			return ErrInvalid
		}
		passed[check.ID] = check.Status == "pass"
	}
	for _, id := range RequiredReadinessChecks {
		if !seen[id] {
			return ErrInvalid
		}
		if in.Status == "ready" && !passed[id] {
			return ErrInvalid
		}
	}
	return nil
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
	case "chatgpt", "api_key", "claude_ai_max", "claude_ai_pro", "claude_ai_team", "claude_ai_enterprise", "console", "pi_context":
		return true
	}
	return false
}

func validAccountKey(value string) bool {
	if !accountKey.MatchString(value) || strings.ContainsAny(value, "/\\") {
		return false
	}
	return !validAccount(value) && value != "unknown" && value != "local_probe"
}

func intentSchema(req Request) int {
	if req.AccountKey != "" {
		return AccountChoiceSchemaV2
	}
	return RuntimeSchemaV1
}

func (r Runtime) matchAccount(key string) bool {
	if len(r.Accounts) == 0 {
		return key == ""
	}
	if key == "" {
		return false
	}
	for _, choice := range r.Accounts {
		if choice.Key == key {
			return true
		}
	}
	return false
}

// ValidBaselineDigest accepts only the canonical Aithema content digest form.
func ValidBaselineDigest(v string) bool {
	return len(v) == 71 && strings.HasPrefix(v, "sha256:") && contentDigestBody.MatchString(v[7:])
}

func (r Request) validate() error {
	if !validID(r.RequestKey) || !validID(r.RuntimeID) || !validID(r.RuntimeGeneration) || !validAccount(r.AccountLabel) || r.TTLSeconds < 30 || r.TTLSeconds > 600 {
		return ErrInvalid
	}
	if r.AccountKey != "" && !validAccountKey(r.AccountKey) {
		return ErrInvalid
	}
	if r.Operation != "readiness" && r.BaselineDigest != "" {
		return ErrInvalid
	}
	if r.Operation == "readiness" {
		// A readiness probe observes; it never binds a session, ticket, agent or
		// hierarchy, and it must name the exact baseline whose start it gates.
		if !ValidBaselineDigest(r.BaselineDigest) || !validID(r.WorkspaceHandle) ||
			!label(r.DispatchProfileID, 128) || !label(r.DispatchProfileVersion, 128) {
			return ErrInvalid
		}
		if r.AgentName != "" || r.TicketID != nil || r.WorkShape != "" || r.Role != "" || r.RepairLayer != "" ||
			r.ParentSessionID != nil || r.SessionID != "" || r.SessionGeneration != "" || r.ExpectedRevision != 0 {
			return ErrInvalid
		}
		return nil
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
