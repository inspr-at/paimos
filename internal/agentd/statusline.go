// SPDX-License-Identifier: AGPL-3.0-only
package agentd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/inspr-at/paimos/internal/agentsetup"
	"github.com/inspr-at/paimos/internal/capacity"
)

type StatuslineRequest struct {
	AccountID string             `json:"account_id"`
	Readings  []capacity.Reading `json:"readings"`
}
type StatuslineResponse struct {
	Plan     string `json:"plan"`
	Reported bool   `json:"reported"`
}
type StatuslineConsent struct {
	Enabled bool   `json:"enabled"`
	Plan    string `json:"plan,omitempty"`
}

func (r *Remote) StatuslineConsent(ctx context.Context, id string) (StatuslineConsent, error) {
	var v StatuslineConsent
	err := r.Client.Do(ctx, "GET", "/api/agent-accounts/"+url.PathEscape(id)+"/statusline", nil, &v)
	return v, err
}

// ReportStatusline authenticates at the owner-only socket and binds the account
// to this daemon's enrollment. Only numeric quota fields cross this boundary.
// Attempts are durable and limited per account, including failures/restarts.
func (s *Supervisor) ReportStatusline(ctx context.Context, req StatuslineRequest, now time.Time) (StatuslineResponse, error) {
	s.statuslineMu.Lock()
	defer s.statuslineMu.Unlock()
	api, ok := s.api.(capacityAPI)
	if !ok {
		return StatuslineResponse{}, ErrUnsupported
	}
	s.mu.Lock()
	found := false
	for _, a := range s.accounts {
		if a.ID == req.AccountID && a.Harness == Claude {
			found = true
			break
		}
	}
	s.mu.Unlock()
	if !found || !s.dispatchAllowed(req.AccountID) || !s.statuslineEnabled[req.AccountID] {
		return StatuslineResponse{}, ErrScope
	}
	if len(req.Readings) > 2 {
		return StatuslineResponse{}, ErrScope
	}
	seen := map[string]bool{}
	for _, r := range req.Readings {
		if r.Validate(now) != nil || r.ReadAt.Before(now.Add(-time.Minute)) || r.ReadAt.After(now.Add(5*time.Second)) ||
			r.Source != "harness" || r.Phase != "update" || r.RunID != "" || r.Plan != "" || r.OrdinaryUsageAllowed != nil ||
			(r.Bucket != "five_hour" || r.WindowKind != "5h") && (r.Bucket != "seven_day" || r.WindowKind != "weekly") || seen[r.Bucket] {
			return StatuslineResponse{}, ErrScope
		}
		seen[r.Bucket] = true
	}
	out := StatuslineResponse{Plan: "Aeon · plan unavailable"}
	if v, ok := s.statuslinePlans[req.AccountID]; ok && now.Sub(v.at) < time.Minute {
		out.Plan = v.plan
	}
	name := "statusline-" + agentsetup.Hash([]byte(req.AccountID)) + ".json"
	var last struct {
		TenantID    string    `json:"tenant_id"`
		PrincipalID string    `json:"principal_id"`
		At          time.Time `json:"at"`
	}
	raw, err := s.state.Read(name, 1024)
	if err == nil {
		if json.Unmarshal(raw, &last) != nil || last.TenantID != s.tenantID || last.PrincipalID != s.principalID {
			return out, ErrScope
		}
		if now.Sub(last.At) < time.Minute {
			return out, nil
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return out, ErrScope
	}
	if len(req.Readings) == 0 {
		return out, nil
	}
	last.TenantID = s.tenantID
	last.PrincipalID = s.principalID
	last.At = now
	raw, _ = json.Marshal(last)
	if s.state.Write(name, raw, false) != nil {
		return out, ErrScope
	}
	op, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	if err = api.ReportCapacity(op, req.AccountID, req.Readings); err != nil {
		return out, errors.New("capacity report unavailable")
	}
	s.rememberCapacityCapture(req.AccountID, req.Readings)
	out.Reported = true
	return out, nil
}

type statuslinePlan struct {
	plan string
	at   time.Time
}

// Consent is read from Aeon, never inferred from a statusline invocation or
// account enrollment. Unknown/user-authored status lines are never overwritten.
func applyClaudeStatusline(home, command string, enabled bool) error {
	store, err := agentsetup.OpenStore(home, false)
	if err != nil {
		return err
	}
	defer store.Close()
	raw, err := store.Read("settings.json", 1<<20)
	if errors.Is(err, os.ErrNotExist) {
		if !enabled {
			return nil
		}
		raw = []byte(`{}`)
	} else if err != nil {
		return err
	}
	var settings map[string]json.RawMessage
	if rejectDuplicateKeys(raw) != nil || json.Unmarshal(raw, &settings) != nil || settings == nil {
		return errors.New("Claude settings unavailable")
	}
	wanted, _ := json.Marshal(struct {
		Type    string `json:"type"`
		Command string `json:"command"`
	}{"command", command})
	old := settings["statusLine"]
	if len(old) > 0 {
		var v struct {
			Type    string `json:"type"`
			Command string `json:"command"`
		}
		if json.Unmarshal(old, &v) != nil || v.Type != "command" || !claudeStatuslineOwned(v.Command, command) {
			return errors.New("Claude status line already configured")
		}
		if enabled {
			if v.Command == command {
				return nil
			}
			settings["statusLine"] = wanted
		} else {
			delete(settings, "statusLine")
		}
	} else if enabled {
		settings["statusLine"] = wanted
	} else {
		return nil
	}
	next, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return errors.New("Claude settings unavailable")
	}
	return store.Write("settings.json", append(next, '\n'), false)
}

func (s *Supervisor) syncStatuslines(ctx context.Context, now time.Time) {
	api, ok := s.api.(interface {
		StatuslineConsent(context.Context, string) (StatuslineConsent, error)
	})
	if !ok {
		return
	}
	s.mu.Lock()
	accounts := append([]EnrolledAccount(nil), s.accounts...)
	adapter, _ := s.adapters[Claude].(*ClaudeAdapter)
	s.mu.Unlock()
	if adapter == nil {
		return
	}
	for _, a := range accounts {
		if a.Harness != Claude || !s.dispatchAllowed(a.ID) {
			continue
		}
		op, cancel := context.WithTimeout(ctx, 3*time.Second)
		consent, err := api.StatuslineConsent(op, a.ID)
		cancel()
		if err != nil {
			continue
		}
		// Consent gates the relay even when the settings write cannot run yet.
		s.statuslineMu.Lock()
		if s.statuslinePlans == nil {
			s.statuslinePlans = map[string]statuslinePlan{}
		}
		if s.statuslineEnabled == nil {
			s.statuslineEnabled = map[string]bool{}
		}
		s.statuslineEnabled[a.ID] = consent.Enabled
		if validPlanLine(consent.Plan) {
			s.statuslinePlans[a.ID] = statuslinePlan{consent.Plan, now}
		}
		s.statuslineMu.Unlock()
		home, err := localHome(adapter.Homes, a.Key)
		if err != nil {
			continue
		}
		path, err := exec.LookPath("aeon")
		if err != nil {
			continue
		}
		path, err = filepath.EvalSymlinks(path)
		if err != nil {
			continue
		}
		if _, err = pinnedExecutable(path); err != nil {
			continue
		}
		_ = applyClaudeStatusline(home, aeonStatuslineCommand(path, s.state.Path(), a.ID), consent.Enabled)
	}
}

// Aeon's entry is the statusline subcommand for this state dir and account.
// The binary path is not part of that signature: a nix store path change must
// still update or remove the entry Aeon installed.
func aeonStatuslineCommand(binary, stateDir, accountID string) string {
	quote := func(v string) string { return "'" + strings.ReplaceAll(v, "'", "'\\''") + "'" }
	return fmt.Sprintf("%s statusline --state-dir %s --account-id %s", quote(binary), quote(stateDir), quote(accountID))
}

func claudeStatuslineOwned(existing, wanted string) bool {
	if existing == wanted {
		return true
	}
	signature := func(command string) string {
		const marker = " statusline --state-dir "
		i := strings.Index(command, marker)
		if i < 2 || !singleQuotedWord(command[:i]) {
			return ""
		}
		return command[i:]
	}
	owned := signature(existing)
	return owned != "" && owned == signature(wanted)
}

func singleQuotedWord(s string) bool {
	if len(s) < 2 || s[0] != '\'' || s[len(s)-1] != '\'' {
		return false
	}
	body := s[1 : len(s)-1]
	for i := 0; i < len(body); {
		if body[i] == '\'' {
			if !strings.HasPrefix(body[i:], `'\''`) {
				return false
			}
			i += 4
			continue
		}
		if body[i] == ' ' || body[i] == '\t' || body[i] == '\n' {
			return false
		}
		i++
	}
	return true
}

func validPlanLine(v string) bool {
	return v != "" && len(v) <= 240 && !strings.ContainsAny(v, "\r\n\x1b")
}
