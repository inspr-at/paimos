// SPDX-License-Identifier: AGPL-3.0-only

// Package agentpairing implements tenant-bound device approval and irreversible
// enrollment fences. Setup credentials never become general API authority.
package agentpairing

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/jackc/pgx/v5/pgconn"
	"io"
	"log/slog"
	"net/http"
	"path"
	"regexp"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/inspr-at/paimos/internal/agentcompat"
	"github.com/inspr-at/paimos/internal/agentsetup"
	"github.com/inspr-at/paimos/internal/attachwatch"
	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/httpapi"
)

// CoordinatorPermissions is the CLI coordinator key ceiling (AEON-327).
// Pairing does not mint it. models.read is safe on the key: the registry
// holds no secrets. rules.read is on the ceiling only; authorization grants
// it on projects the key can already read, not as a workspace permission.
var CoordinatorPermissions = authz.CoordinatorKeyScopes

const VerificationTask = "Reply exactly AEON_VERIFIED. Do not modify files, perform privileged actions, access external networks, or use external/MCP tools. Use the enforced read-only verification mode."
const VerificationSeconds = 60

var RuntimePermissions = []string{"run.read", "run.claim", "run.telemetry", "work_orders.read", "work_orders.write", "nodes.read", "models.read", "models.report", "models.refresh", "account.read", "account.route", "account.probe", "harness.read", "harness.write", "harness.worker"}
var uuidRE = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
var hashRE = regexp.MustCompile(`^[0-9a-f]{64}$`)
var accountRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`)
var providerRE = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,63}$`)

type Choice struct {
	AccountKey string `json:"account_key"`
	Harness    string `json:"harness"`
	Label      string `json:"label"`
	ProfileID  string `json:"model_profile_id,omitempty"`
	Provider   string `json:"provider,omitempty"`
}
type Details struct {
	LocalAuthPublicKey string   `json:"local_auth_public_key,omitempty"`
	ComputerName       string   `json:"computer_name"`
	Platform           string   `json:"platform"`
	Arch               string   `json:"arch"`
	Workspace          string   `json:"workspace_path"`
	Capabilities       []string `json:"capabilities"`
	Accounts           []Choice `json:"accounts"`
	ExistingComputerID string   `json:"existing_computer_id,omitempty"`
}
type deviceRequest struct {
	Details
	RequestID     string `json:"request_id"`
	TenantID      string `json:"tenant_id,omitempty"`
	TenantSlug    string `json:"tenant_slug,omitempty"`
	DeviceHash    string `json:"device_hash"`
	RuntimeHash   string `json:"runtime_hash"`
	LifecycleHash string `json:"lifecycle_hash"`
	ExistingProof string `json:"existing_lifecycle_secret,omitempty"`
}
type SetupProgress struct {
	AgentRelease    *agentcompat.Release                `json:"agent_release,omitempty"`
	HarnessDetails  map[string]agentsetup.HarnessDetail `json:"harness_details,omitempty"`
	HarnessStatuses map[string]string                   `json:"harness_statuses,omitempty"`
	State           string                              `json:"state"`
	ErrorCode       string                              `json:"error_code,omitempty"`
}

type proofRequest struct {
	Progress        *SetupProgress `json:"progress,omitempty"`
	TenantID        string         `json:"tenant_id"`
	RequestID       string         `json:"request_id"`
	DeviceSecret    string         `json:"device_secret,omitempty"`
	LifecycleSecret string         `json:"lifecycle_secret,omitempty"`
	Cleaned         []string       `json:"cleanup_confirmed_account_ids,omitempty"`
	ComputerCleaned bool           `json:"computer_cleanup_confirmed,omitempty"`
}
type Verification struct {
	Policy         string    `json:"policy"`
	Mode           *string   `json:"mode"`
	RunsPerAccount int       `json:"runs_per_account"`
	MaxParallel    int       `json:"max_parallel_runs"`
	MaxDuration    int       `json:"max_duration_seconds"`
	Allowance      int       `json:"allowance"`
	Unit           string    `json:"unit"`
	ExpiresAt      time.Time `json:"expires_at"`
	Task           string    `json:"task"`
}
type Enrollment struct {
	LocalProcesses     string   `json:"local_processes"`
	AccountingState    string   `json:"accounting_state"`
	VerificationState  string   `json:"verification_state"`
	VerificationError  string   `json:"verification_error"`
	VerificationReason string   `json:"verification_reason,omitempty"`
	AccountID          string   `json:"account_id"`
	AccountKey         string   `json:"account_key"`
	Harness            string   `json:"harness"`
	Label              string   `json:"label"`
	ProfileID          string   `json:"model_profile_id"`
	State              string   `json:"state"`
	Cleanup            string   `json:"local_cleanup"`
	VerificationRunID  *string  `json:"verification_run_id"`
	ActiveRunIDs       []string `json:"active_run_ids"`
}
type View struct {
	LocalAuthPinned           *bool                               `json:"local_auth_pinned,omitempty"`
	AgentRelease              agentcompat.Release                 `json:"agent_release"`
	AgentCompatibility        agentcompat.Result                  `json:"agent_compatibility"`
	HarnessDetails            map[string]agentsetup.HarnessDetail `json:"harness_details,omitempty"`
	HarnessStatuses           map[string]string                   `json:"harness_statuses,omitempty"`
	VerificationCapabilities  map[string]VerificationCapability   `json:"verification_capabilities"`
	VerificationHelperVersion string                              `json:"verification_helper_version"`
	ExistingComputerID        string                              `json:"existing_computer_id,omitempty"`
	AccountingState           string                              `json:"accounting_state"`
	SetupState                string                              `json:"setup_state"`
	SetupError                string                              `json:"setup_error"`
	LastSeenAt                *time.Time                          `json:"last_seen_at"`
	ArchivedAt                *time.Time                          `json:"archived_at,omitempty"`
	Connectivity              string                              `json:"connectivity"`
	RequestID                 string                              `json:"request_id"`
	TenantID                  string                              `json:"tenant_id"`
	TenantName                string                              `json:"tenant_name"`
	State                     string                              `json:"state"`
	Digest                    string                              `json:"request_digest"`
	ExpiresAt                 time.Time                           `json:"expires_at"`
	ComputerName              string                              `json:"computer_name"`
	Platform                  string                              `json:"platform"`
	Arch                      string                              `json:"arch"`
	Workspace                 string                              `json:"workspace_path"`
	Capabilities              []string                            `json:"capabilities"`
	Requested                 []Choice                            `json:"requested_accounts"`
	Verification              Verification                        `json:"verification"`
	ComputerID                *string                             `json:"computer_id"`
	ComputerState             *string                             `json:"computer_state"`
	PrincipalID               *string                             `json:"principal_id"`
	DaemonID                  *string                             `json:"daemon_id"`
	RuntimePrefix             string                              `json:"runtime_prefix,omitempty"`
	Cleanup                   string                              `json:"local_cleanup"`
	Processes                 string                              `json:"local_processes"`
	Enrollments               []Enrollment                        `json:"enrollments"`
	Revision                  int64                               `json:"revision"`
}
type record struct {
	ID, TenantID, Code, DeviceHash, RuntimeHash, LifecycleHash, Digest, State string
	Details                                                                   Details
	Mode                                                                      *string
	ExpiresAt, VerificationExpiresAt                                          time.Time
	ApprovedBy, ComputerID                                                    *string
	Attempts                                                                  int
}
type Error struct {
	Status        int
	Code, Message string
}

func (e *Error) Error() string                    { return e.Message }
func fail(status int, code, message string) error { return &Error{status, code, message} }
func WriteError(w http.ResponseWriter, err error) {
	var e *Error
	if !errors.As(err, &e) {
		var pe *pgconn.PgError
		if errors.As(err, &pe) {
			slog.Error("pairing database error", "sqlstate", pe.Code, "routine", pe.Routine, "position", pe.Position)
		}
		e = &Error{500, "internal_error", "pairing operation failed"}
	}
	if e.Status == 429 {
		w.Header().Set("Retry-After", "5")
	}
	w.Header().Set("Cache-Control", "no-store")
	body := map[string]string{"error": e.Message, "code": e.Code}
	var detail *attachDiagnostic
	if errors.As(err, &detail) {
		body["attach_refusal"] = detail.cause
	}
	httpapi.WriteJSON(w, e.Status, body)
}
func decode(w http.ResponseWriter, r *http.Request, v any) error {
	r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	if d.Decode(v) != nil {
		return fail(400, "invalid_request", "invalid JSON request")
	}
	if d.Decode(new(any)) != io.EOF {
		return fail(400, "invalid_request", "one JSON value required")
	}
	return nil
}
func digest(s string) string { h := sha256.Sum256([]byte(s)); return hex.EncodeToString(h[:]) }
func randomHex(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}
func safeText(s string, max int) bool {
	return len(s) > 0 && len(s) <= max && strings.TrimSpace(s) == s && utf8.ValidString(s) && !strings.ContainsFunc(s, unicode.IsControl)
}
func validateDevice(in deviceRequest) error {
	if in.LocalAuthPublicKey != "" && (in.Platform != "darwin" || attachwatch.LocalAuthPublicKey(in.LocalAuthPublicKey) == nil) {
		return fail(400, "invalid_request", "valid Mac P-256 public key required")
	}
	if !uuidRE.MatchString(in.RequestID) || (in.TenantID == "") == (in.TenantSlug == "") || in.TenantID != "" && !uuidRE.MatchString(in.TenantID) || !hashRE.MatchString(in.DeviceHash) || !hashRE.MatchString(in.RuntimeHash) || !hashRE.MatchString(in.LifecycleHash) || in.DeviceHash == in.RuntimeHash || in.DeviceHash == in.LifecycleHash || in.RuntimeHash == in.LifecycleHash {
		return fail(400, "invalid_request", "distinct commitments, request UUID and exactly one tenant selector required")
	}
	if !safeText(in.ComputerName, 128) || !safeText(in.Workspace, 1024) || !path.IsAbs(in.Workspace) || path.Clean(in.Workspace) != in.Workspace || in.Workspace == "/" || (in.Platform != "darwin" && in.Platform != "linux") || (in.Arch != "arm64" && in.Arch != "amd64") || len(in.Capabilities) != 1 || in.Capabilities[0] != "managed_runs" || len(in.Accounts) < 1 || len(in.Accounts) > 7 {
		return fail(400, "invalid_request", "invalid computer, folder, capabilities or account selection")
	}
	seen := map[string]bool{}
	seenAccounts := map[string]bool{}
	for _, a := range in.Accounts {
		if !accountRE.MatchString(a.AccountKey) || !safeText(a.Label, 128) || a.ProfileID != "" && !uuidRE.MatchString(a.ProfileID) || seen[a.Harness] || seenAccounts[a.AccountKey] {
			return fail(400, "invalid_request", "choose exactly one identified account per harness")
		}
		switch a.Harness {
		case "claude", "codex", "cursor", "grok", "pi", "gemini", "opencode":
		default:
			return fail(400, "invalid_request", "unsupported harness")
		}
		seen[a.Harness] = true
		if a.Provider != "" && (a.Harness != "pi" || !providerRE.MatchString(a.Provider)) {
			return fail(400, "invalid_request", "invalid pi provider binding")
		}
		seenAccounts[a.AccountKey] = true
	}
	if in.ExistingComputerID != "" && (!uuidRE.MatchString(in.ExistingComputerID) || !hashRE.MatchString(in.ExistingProof)) || in.ExistingComputerID == "" && in.ExistingProof != "" {
		return fail(400, "invalid_request", "existing computer proof required")
	}
	return nil
}
