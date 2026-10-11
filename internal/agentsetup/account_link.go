// SPDX-License-Identifier: AGPL-3.0-only
package agentsetup

import (
	"context"
	"errors"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// SetupRequest is local-socket input, never an Aeon API payload. LocalCandidate
// retains the private bindings that Candidate deliberately omits from the API.
type SetupRequest struct {
	Operation     string           `json:"operation"`
	RequestID     string           `json:"request_id,omitempty"`
	Candidates    []LocalCandidate `json:"candidates,omitempty"`
	NodePath      string           `json:"node_path,omitempty"`
	ClaudeSDKPath string           `json:"claude_sdk_path,omitempty"`
}

// AccountLogin is diagnostic local identity, never transmitted to Aeon.
type AccountLogin struct {
	ActiveIdentity string `json:"active_identity"`
	Approved       bool   `json:"approved"`
}

func ValidLoginLabel(label string) bool {
	return label != "" && len(label) <= 128 && utf8.ValidString(label) && strings.TrimSpace(label) == label &&
		!strings.ContainsFunc(label, func(r rune) bool { return unicode.IsControl(r) || unicode.Is(unicode.Cf, r) })
}

// SetupExisting cannot create a computer, grant approval, or clean up a service.
// The daemon serializes this operation with SyncFences before invoking it.
func (e *Engine) SetupExisting(ctx context.Context, req SetupRequest) (Progress, error) {
	if !e.RunningService || (req.Operation != "add_harness" && req.Operation != "step") || len(req.Candidates) > 7 {
		return Progress{}, errors.New("unsupported running-daemon setup operation")
	}
	if err := e.Store.Lock(); err != nil {
		return Progress{}, err
	}
	s, err := e.load()
	if err != nil {
		return Progress{}, err
	}
	if s.DisconnectAll || s.ComputerCleaned || s.View.ComputerState != "connected" {
		return e.progress(s), errors.New("computer is disconnecting or revoked; enrollment cannot resume")
	}
	if req.Operation == "step" {
		if req.RequestID != s.Request.RequestID || len(req.Candidates) != 0 || req.NodePath != "" || req.ClaudeSDKPath != "" {
			return e.progress(s), errors.New("pairing request changed; resume the current enrollment")
		}
		return e.Step(ctx)
	}
	if req.RequestID != "" || len(req.Candidates) == 0 {
		return e.progress(s), errors.New("select a harness login to enroll")
	}
	candidates := make([]Candidate, 0, len(req.Candidates))
	for _, local := range req.Candidates {
		c := local.Candidate
		c.Path, c.Home, c.Identity, c.Version = local.Path, local.Home, local.Identity, local.Version
		c.Login = "signed_in"
		if c.Harness == "gemini" || c.Harness == "opencode" {
			c.Login = "unverified"
		}
		candidates = append(candidates, c)
	}
	e.ClaudeDependencies = ClaudeDependencies{NodePath: req.NodePath, SDKPath: req.ClaudeSDKPath}
	return e.AddHarness(ctx, candidates)
}

// AccountLinkView carries only the user-facing code and result, never proof.
type AccountLinkView struct {
	AccountID  string     `json:"account_id"`
	RequestID  string     `json:"request_id,omitempty"`
	State      string     `json:"state"`
	ShowPrompt bool       `json:"show_prompt"`
	ShowResult bool       `json:"show_result"`
	Code       string     `json:"user_code,omitempty"`
	URI        string     `json:"verification_uri,omitempty"`
	ExpiresAt  *time.Time `json:"expires_at,omitempty"`
	PersonName string     `json:"person_name,omitempty"`
}
