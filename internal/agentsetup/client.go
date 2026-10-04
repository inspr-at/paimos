// SPDX-License-Identifier: AGPL-3.0-only

package agentsetup

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/inspr-at/paimos/internal/agentcompat"
)

const apiBase = "/api/agent-pairing"

var uuidPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
var hashPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

type Details struct {
	LocalAuthPublicKey string      `json:"local_auth_public_key,omitempty"`
	ComputerName       string      `json:"computer_name"`
	Platform           string      `json:"platform"`
	Arch               string      `json:"arch"`
	Workspace          string      `json:"workspace_path"`
	Capabilities       []string    `json:"capabilities"`
	Accounts           []Candidate `json:"accounts"`
	ExistingComputerID string      `json:"existing_computer_id,omitempty"`
}
type DeviceRequest struct {
	Details
	RequestID     string `json:"request_id"`
	TenantID      string `json:"tenant_id,omitempty"`
	TenantSlug    string `json:"tenant_slug,omitempty"`
	DeviceHash    string `json:"device_hash"`
	RuntimeHash   string `json:"runtime_hash"`
	LifecycleHash string `json:"lifecycle_hash"`
	ExistingProof secret `json:"existing_lifecycle_secret,omitempty"`
}
type DeviceResponse struct {
	RequestID       string    `json:"request_id"`
	TenantID        string    `json:"tenant_id"`
	UserCode        string    `json:"user_code"`
	State           string    `json:"state"`
	ExpiresAt       time.Time `json:"expires_at"`
	IntervalSeconds int       `json:"interval_seconds"`
	VerificationURI string    `json:"verification_uri"`
	Digest          string    `json:"request_digest"`
}
type ProofRequest struct {
	Progress        *SetupProgress `json:"progress,omitempty"`
	TenantID        string         `json:"tenant_id"`
	RequestID       string         `json:"request_id"`
	DeviceSecret    secret         `json:"device_secret,omitempty"`
	LifecycleSecret secret         `json:"lifecycle_secret,omitempty"`
	Cleaned         []string       `json:"cleanup_confirmed_account_ids,omitempty"`
	ComputerCleaned bool           `json:"computer_cleanup_confirmed,omitempty"`
}
type SetupProgress struct {
	AgentRelease    *agentcompat.Release     `json:"agent_release,omitempty"`
	HarnessDetails  map[string]HarnessDetail `json:"harness_details,omitempty"`
	HarnessStatuses map[string]string        `json:"harness_statuses,omitempty"`
	State           string                   `json:"state"`
	ErrorCode       string                   `json:"error_code,omitempty"`
}

// reportRelease uses the normal lifecycle response as capability negotiation.
// It adds no discovery request, including during cold revocation or cleanup.
func reportRelease(view View, progress *SetupProgress) *SetupProgress {
	if view.AgentCompatibility != nil {
		release := agentcompat.Current()
		progress.AgentRelease = &release
	}
	return progress
}

type Verification struct {
	Policy         string    `json:"policy"`
	Mode           string    `json:"mode"`
	RunsPerAccount int       `json:"runs_per_account"`
	MaxParallel    int       `json:"max_parallel_runs"`
	MaxDuration    int       `json:"max_duration_seconds"`
	Allowance      int       `json:"allowance"`
	Unit           string    `json:"unit"`
	ExpiresAt      time.Time `json:"expires_at"`
	Task           string    `json:"task"`
}
type Enrollment struct {
	AccountingState    string   `json:"accounting_state"`
	LocalProcesses     string   `json:"local_processes"`
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
	VerificationRunID  string   `json:"verification_run_id"`
	ActiveRunIDs       []string `json:"active_run_ids"`
}
type View struct {
	LocalAuthPinned    *bool                    `json:"local_auth_pinned,omitempty"`
	AgentCompatibility *agentcompat.Result      `json:"agent_compatibility,omitempty"`
	HarnessDetails     map[string]HarnessDetail `json:"harness_details,omitempty"`
	HarnessStatuses    map[string]string        `json:"harness_statuses,omitempty"`
	ExistingComputerID string                   `json:"existing_computer_id,omitempty"`
	AccountingState    string                   `json:"accounting_state"`
	SetupState         string                   `json:"setup_state"`
	SetupError         string                   `json:"setup_error"`
	Connectivity       string                   `json:"connectivity"`
	RequestID          string                   `json:"request_id"`
	TenantID           string                   `json:"tenant_id"`
	TenantName         string                   `json:"tenant_name"`
	State              string                   `json:"state"`
	Digest             string                   `json:"request_digest"`
	ExpiresAt          time.Time                `json:"expires_at"`
	ComputerName       string                   `json:"computer_name"`
	Platform           string                   `json:"platform"`
	Arch               string                   `json:"arch"`
	Workspace          string                   `json:"workspace_path"`
	Capabilities       []string                 `json:"capabilities"`
	Requested          []Candidate              `json:"requested_accounts"`
	Verification       Verification             `json:"verification"`
	ComputerID         string                   `json:"computer_id"`
	ComputerState      string                   `json:"computer_state"`
	PrincipalID        string                   `json:"principal_id"`
	DaemonID           string                   `json:"daemon_id"`
	RuntimePrefix      string                   `json:"runtime_prefix,omitempty"`
	Cleanup            string                   `json:"local_cleanup"`
	Processes          string                   `json:"local_processes"`
	Enrollments        []Enrollment             `json:"enrollments"`
	Revision           int64                    `json:"revision"`
}
type Guide struct {
	AgentCompatibility *agentcompat.Policy `json:"agent_compatibility,omitempty"`
	InstanceURL        string              `json:"instance_url"`
	DefaultTenantSlug  string              `json:"default_tenant_slug"`
	Protocol           string              `json:"protocol"`
	Platforms          []string            `json:"platforms"`
	Version            string              `json:"version,omitempty"`
}

type PairingAPI interface {
	Guide(context.Context) (Guide, error)
	Create(context.Context, DeviceRequest) (DeviceResponse, error)
	Redeem(context.Context, ProofRequest) (View, error)
	Reconcile(context.Context, ProofRequest) (View, error)
	Disconnect(context.Context, secret, string) (View, error)
}
type APIError struct {
	Code       string
	RetryAfter time.Duration
	StatusCode int
}

func (e *APIError) Error() string { return "pairing request failed: " + e.Code }

type HTTPClient struct {
	Origin string
	HTTP   *http.Client
}

func ValidateOrigin(origin string) error {
	u, e := url.Parse(origin)
	if e != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.Path != "" && u.Path != "/" || u.RawQuery != "" || u.Fragment != "" || u.Opaque != "" || strings.ContainsAny(origin, "\x00\r\n") {
		return errors.New("pairing requires an HTTPS instance origin without a path, credentials, query or fragment")
	}
	return nil
}
func (c HTTPClient) call(ctx context.Context, method, path string, token secret, body, out any) error {
	if err := ValidateOrigin(c.Origin); err != nil {
		return err
	}
	var raw []byte
	var err error
	if body != nil {
		raw, err = json.Marshal(body)
		if err != nil {
			return errors.New("invalid pairing request")
		}
	}
	r, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(c.Origin, "/")+apiBase+path, bytes.NewReader(raw))
	if err != nil {
		return errors.New("invalid pairing request")
	}
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Accept", "application/json")
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+string(token))
	}
	hc := http.Client{Timeout: 20 * time.Second}
	if c.HTTP != nil {
		hc = *c.HTTP
	}
	hc.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	res, err := hc.Do(r)
	if err != nil {
		return &APIError{Code: "unreachable", RetryAfter: 5 * time.Second}
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		code := "unavailable"
		switch res.StatusCode {
		case 400:
			code = "invalid_request"
		case 401, 403:
			code = "forbidden"
		case 404:
			code = "not_found"
		case 409:
			code = "conflict"
		case 410:
			code = "pairing_revoked"
		case 429:
			code = "rate_limited"
		}
		retry := 5 * time.Second
		if n, e := strconv.Atoi(res.Header.Get("Retry-After")); e == nil && n >= 5 && n <= 600 {
			retry = time.Duration(n) * time.Second
		}
		// Remote error text is never displayed: it could reflect capabilities.
		return &APIError{Code: code, RetryAfter: retry, StatusCode: res.StatusCode}
	}
	d := json.NewDecoder(io.LimitReader(res.Body, (128<<10)+1))
	if d.Decode(out) != nil || d.Decode(&struct{}{}) != io.EOF {
		return errors.New("pairing response invalid")
	}
	return nil
}
func (c HTTPClient) Guide(ctx context.Context) (Guide, error) {
	var v Guide
	e := c.call(ctx, "GET", "/guide", "", nil, &v)
	return v, e
}
func (c HTTPClient) Create(ctx context.Context, r DeviceRequest) (DeviceResponse, error) {
	var v DeviceResponse
	e := c.call(ctx, "POST", "/device", "", r, &v)
	return v, e
}
func (c HTTPClient) Redeem(ctx context.Context, r ProofRequest) (View, error) {
	var v View
	e := c.call(ctx, "POST", "/redeem", "", r, &v)
	return v, e
}
func (c HTTPClient) Reconcile(ctx context.Context, r ProofRequest) (View, error) {
	var v View
	e := c.call(ctx, "POST", "/reconcile", "", r, &v)
	// A server downgrade may leave a saved advertisement from the newer server.
	// Strict legacy decoding rejects the additive field before any mutation;
	// retry that request once with only the old lifecycle shape.
	var apiErr *APIError
	if r.Progress != nil && r.Progress.AgentRelease != nil && errors.As(e, &apiErr) && apiErr.Code == "invalid_request" {
		progress := *r.Progress
		progress.AgentRelease = nil
		r.Progress = &progress
		e = c.call(ctx, "POST", "/reconcile", "", r, &v)
	}
	return v, e
}
func (c HTTPClient) Disconnect(ctx context.Context, token secret, account string) (View, error) {
	var v View
	e := c.call(ctx, "POST", "/self/disconnect", token, map[string]string{"mode": "drain", "account_id": account}, &v)
	return v, e
}
