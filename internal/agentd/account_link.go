// SPDX-License-Identifier: AGPL-3.0-only
package agentd

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"time"
	"unicode"

	"github.com/inspr-at/paimos/internal/agentsetup"
)

type AccountLinkRequest struct {
	AccountID string `json:"account_id,omitempty"`
	Harness   string `json:"harness"`
	Home      string `json:"home,omitempty"` // Local socket only; never uploaded.
	Operation string `json:"operation"`
	RequestID string `json:"request_id,omitempty"`
}

type AccountLinkAPI interface {
	AccountLink(context.Context, string, string, string) (agentsetup.AccountLinkView, error)
}

// Installation proof stays in the paired daemon, never in a vendor environment,
// CLI flag, local socket response or log. Pairing pins the HTTP origin.
func (r *Remote) SetAccountLinkProof(proof string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.accountLinkProof = proof
}
func (r *Remote) AccountLink(ctx context.Context, account, operation, id string) (agentsetup.AccountLinkView, error) {
	r.mu.RLock()
	proof := r.accountLinkProof
	r.mu.RUnlock()
	if !regexp.MustCompile(`^[a-f0-9]{64}$`).MatchString(proof) {
		return agentsetup.AccountLinkView{}, errors.New("paired installation proof unavailable")
	}
	var out agentsetup.AccountLinkView
	body := map[string]string{"account_id": account, "device_proof": proof, "operation": operation}
	if id != "" {
		body["request_id"] = id
	}
	err := r.Client.Do(ctx, "POST", "/api/agent-pairing/account-link", body, &out)
	if err != nil {
		return out, err
	}
	if out.AccountID != account || id != "" && out.RequestID != id {
		return agentsetup.AccountLinkView{}, errors.New("account link binding changed")
	}
	if out.ShowPrompt {
		if out.URI != strings.TrimRight(r.Client.BaseURL, "/")+"/link" || out.ExpiresAt == nil || !out.ExpiresAt.After(time.Now()) {
			return agentsetup.AccountLinkView{}, errors.New("account link origin or expiry changed")
		}
		if _, err = AccountLinkPrompt(out); err != nil {
			return agentsetup.AccountLinkView{}, err
		}
	}
	return out, nil
}

// Resolve only this OS user's enrolled adapter and private account home. A
// shared machine or a second login never implies which account a caller means.
func (s *Supervisor) AccountLink(ctx context.Context, in AccountLinkRequest) (agentsetup.AccountLinkView, error) {
	if in.Operation != "offer" && in.Operation != "poll" && in.Operation != "renew" {
		return agentsetup.AccountLinkView{}, errors.New("unsupported account link operation")
	}
	api, ok := s.api.(AccountLinkAPI)
	if !ok {
		return agentsetup.AccountLinkView{}, errors.New("account linking unavailable")
	}
	s.mu.Lock()
	candidates := []EnrolledAccount{}
	for _, a := range s.accounts {
		if a.Harness == in.Harness && (in.AccountID == "" || in.AccountID == a.ID) && !a.DependencyBlocked {
			candidates = append(candidates, a)
		}
	}
	s.mu.Unlock()
	if in.Home != "" {
		kept := []EnrolledAccount{}
		for _, a := range candidates {
			env, err := s.AccountEnvironment(a.ID, s.daemonID, a.Harness)
			if err == nil && env.Home == in.Home {
				kept = append(kept, a)
			}
		}
		candidates = kept
	}
	if len(candidates) != 1 {
		return agentsetup.AccountLinkView{}, errors.New("choose your enrolled account; automatic linking is ambiguous")
	}
	a := candidates[0]
	if in.Operation != "poll" {
		s.mu.Lock()
		adapter := s.adapters[a.Harness]
		s.mu.Unlock()
		prober, ok := adapter.(AccountProber)
		if !ok || !probeAccount(ctx, prober, a.Key).OK {
			return agentsetup.AccountLinkView{}, errors.New("enrolled vendor login could not be verified")
		}
	}
	return api.AccountLink(ctx, a.ID, in.Operation, in.RequestID)
}

func AccountLinkPrompt(v agentsetup.AccountLinkView) (string, error) {
	u, err := url.Parse(v.URI)
	if err != nil || ValidateBaseURL(v.URI) != nil || u.Path != "/link" || u.RawQuery != "" || u.Fragment != "" || !regexp.MustCompile(`^[0-9]{3} [0-9]{3}$`).MatchString(v.Code) || v.State != "pending" || !v.ShowPrompt {
		return "", errors.New("invalid account link prompt")
	}
	return fmt.Sprintf("Link this account to you: %s · code %s", v.URI, v.Code), nil
}
func AccountLinkedLine(v agentsetup.AccountLinkView) (string, error) {
	if v.State != "linked" || strings.TrimSpace(v.PersonName) == "" || len(v.PersonName) > 256 || strings.ContainsFunc(v.PersonName, func(r rune) bool { return unicode.IsControl(r) || unicode.In(r, unicode.Cf) }) {
		return "", errors.New("invalid account link result")
	}
	return "Linked to " + v.PersonName, nil
}
