// SPDX-License-Identifier: AGPL-3.0-only

// Package client is the Aeon HTTP API client used by the command line and the MCP server.
package client

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/inspr-at/paimos/internal/stepup"
)

// Me is the GET /api/me response.
type Me struct {
	OwnerWorkstation      bool      `json:"owner_workstation,omitempty"`
	WorkstationComputerID string    `json:"workstation_computer_id,omitempty"`
	Principal             Principal `json:"principal"`
	Tenant                Tenant    `json:"tenant"`
	Identity              *Identity `json:"identity"`
}

// Principal is the acting person or agent.
type Principal struct {
	ID       string   `json:"id"`
	TenantID string   `json:"tenant_id"`
	Kind     string   `json:"kind"`
	Name     string   `json:"name"`
	Roles    []string `json:"roles"`
}

// Tenant is the caller's workspace.
type Tenant struct {
	ID   string `json:"id"`
	Slug string `json:"slug"`
	Name string `json:"name"`
}

// Identity is the OIDC identity behind a person. Agents have none.
type Identity struct {
	ID          string  `json:"id"`
	Issuer      string  `json:"issuer"`
	Subject     string  `json:"subject"`
	Email       *string `json:"email"`
	DisplayName *string `json:"display_name"`
}

// StatusError is an HTTP response that is not a success.
type StatusError struct {
	Status        int
	Message       string
	AttachRefusal string
	ReasonCode    string
}

func (e *StatusError) Error() string {
	if e.Message != "" {
		return fmt.Sprintf("api %d: %s", e.Status, e.Message)
	}
	return fmt.Sprintf("api %d", e.Status)
}

// Client calls one Aeon instance with an agent API key.
type Client struct {
	BaseURL string
	Token   string
	HTTP    *http.Client
	// ConfirmStepUp receives only a challenge ID. Nil fails closed on 428.
	ConfirmStepUp func(context.Context, string) (string, error)
}

// New returns a client for baseURL. token is sent as a Bearer credential and is never logged.
func New(baseURL, token string) *Client {
	return &Client{
		BaseURL: strings.TrimRight(strings.TrimSpace(baseURL), "/"),
		Token:   token,
		HTTP:    &http.Client{Timeout: 30 * time.Second},
	}
}

// Me calls GET /api/me.
func (c *Client) Me(ctx context.Context) (Me, error) {
	var me Me
	if err := c.Do(ctx, http.MethodGet, "/api/me", nil, &me); err != nil {
		return Me{}, err
	}
	return me, nil
}

// Do sends one JSON request. path is absolute from the instance root, for example /api/me.
// A nil dest discards a success body. The token is not included in errors.
func (c *Client) Do(ctx context.Context, method, path string, body, dest any) error {
	return c.DoWithHeaders(ctx, method, path, body, dest, nil)
}

// DoWithHeaders sends one JSON request with additional non-credential protocol
// headers such as a run's daemon fencing identity.
// MaxResponseBytes caps one response body the client reads.
const MaxResponseBytes = 64 << 20

func (c *Client) DoWithHeaders(ctx context.Context, method, path string, body, dest any, headers map[string]string) error {
	if c.BaseURL == "" {
		return fmt.Errorf("instance URL is empty")
	}
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	var rdr io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("encode request: %w", err)
		}
		rdr = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.BaseURL+path, rdr)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}
	for name, value := range headers {
		req.Header.Set(name, value)
	}
	hc := c.HTTP
	if hc == nil {
		hc = http.DefaultClient
	}
	if req.Header.Get(stepup.Header) != "" {
		proofHTTP := *hc
		proofHTTP.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
		hc = &proofHTTP
	}
	for attempt := 0; attempt < 2; attempt++ {
		request := req.Clone(ctx)
		if req.GetBody != nil {
			request.Body, err = req.GetBody()
			if err != nil {
				return err
			}
		}
		res, err := hc.Do(request)
		if err != nil {
			return err
		}

		// A page of 200 imported tickets carries their full bodies and fields and
		// passes 1 MiB on production data (AEON-140); a cut-off read then failed as
		// "unexpected end of JSON input". Read up to MaxResponseBytes and say so
		// plainly when a response is larger.
		limit := int64(MaxResponseBytes)
		if res.StatusCode == http.StatusPreconditionRequired {
			limit = 4096
		}
		payload, err := io.ReadAll(io.LimitReader(res.Body, limit+1))
		_ = res.Body.Close()
		if err != nil {
			return err
		}
		if int64(len(payload)) > limit {
			if limit == MaxResponseBytes {
				return fmt.Errorf("%s %s: response is larger than %d MiB", method, path, MaxResponseBytes>>20)
			}
			return fmt.Errorf("%s %s: Touch ID challenge exceeds %d bytes", method, path, limit)
		}
		if res.StatusCode == http.StatusPreconditionRequired {
			var required stepup.Required
			if json.Unmarshal(payload, &required) == nil && required.Code == "step_up_required" {
				if attempt != 0 || req.Header.Get(stepup.Header) != "" {
					return fmt.Errorf("Touch ID proof refused; action was not retried again")
				}
				if !stepup.ValidID(required.ChallengeID) || !stepup.ValidExpiry(required.ExpiresAt, time.Now()) {
					return fmt.Errorf("invalid or expired Touch ID challenge")
				}
				if res.Request == nil || res.Request.URL.String() != req.URL.String() || res.Request.Method != req.Method {
					return fmt.Errorf("Touch ID challenge from a redirected request refused")
				}
				if c.ConfirmStepUp == nil {
					return fmt.Errorf("Touch ID confirmation required; configure the local paired agentd")
				}
				deadline := required.ExpiresAt
				if cap := time.Now().Add(stepup.ConfirmTimeout); cap.Before(deadline) {
					deadline = cap
				}
				confirmCtx, cancel := context.WithDeadline(ctx, deadline)
				signature, confirmErr := c.ConfirmStepUp(confirmCtx, required.ChallengeID)
				if confirmErr == nil {
					confirmErr = confirmCtx.Err()
				}
				cancel()
				if confirmErr != nil {
					return fmt.Errorf("Touch ID confirmation failed: %w", confirmErr)
				}
				if !time.Now().Before(required.ExpiresAt) || !stepup.ValidSignature(signature) {
					return fmt.Errorf("invalid or expired Touch ID proof")
				}
				req.Header.Set(stepup.Header, required.ChallengeID+"."+signature)
				// A proof must never follow redirects, including same-origin 307/308.
				retryHTTP := *hc
				retryHTTP.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
				hc = &retryHTTP
				continue
			}
		}
		if res.StatusCode < 200 || res.StatusCode >= 300 {
			var detail struct {
				AttachRefusal string `json:"attach_refusal"`
				ReasonCode    string `json:"reason_code"`
			}
			_ = json.Unmarshal(payload, &detail)
			return &StatusError{Status: res.StatusCode, Message: errorMessage(payload), AttachRefusal: detail.AttachRefusal, ReasonCode: detail.ReasonCode}
		}
		if dest == nil || len(bytes.TrimSpace(payload)) == 0 {
			return nil
		}
		if err := json.Unmarshal(payload, dest); err != nil {
			return fmt.Errorf("decode response: %w", err)
		}
		return nil
	}
	return fmt.Errorf("Touch ID retry exhausted")
}

func errorMessage(payload []byte) string {
	// Modules answer {"error": ...} or {"code": ..., "message": ...}; show the words.
	var wrap struct {
		Error   string `json:"error"`
		Message string `json:"message"`
	}
	if json.Unmarshal(payload, &wrap) == nil && wrap.Error != "" {
		return wrap.Error
	}
	if wrap.Message != "" {
		return wrap.Message
	}
	s := strings.TrimSpace(string(payload))
	if len(s) > 240 {
		s = s[:240]
	}
	return s
}
