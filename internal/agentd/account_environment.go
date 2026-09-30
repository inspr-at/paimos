// SPDX-License-Identifier: AGPL-3.0-only
package agentd

import "errors"

// AccountEnvironment is carried only over the authenticated owner-only Unix
// socket. It must never be part of any Aeon API request, report or projection.
type AccountEnvironment struct {
	AccountID string `json:"account_id"`
	DaemonID  string `json:"daemon_id"`
	Harness   string `json:"harness"`
	Variable  string `json:"variable"`
	Home      string `json:"home"`
}

func (s *Supervisor) AccountEnvironment(accountID, daemonID, harness string) (AccountEnvironment, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if daemonID != s.daemonID {
		return AccountEnvironment{}, errors.New("account belongs to another computer")
	}
	for _, a := range s.accounts {
		if a.ID != accountID || a.Harness != harness {
			continue
		}
		var homes map[string]string
		var variable string
		switch adapter := s.adapters[a.Harness].(type) {
		case *CodexAdapter:
			homes, variable = adapter.Homes, "CODEX_HOME"
		case *ClaudeAdapter:
			homes, variable = adapter.Homes, "CLAUDE_CONFIG_DIR"
		case *PiAdapter:
			homes, variable = adapter.Homes, "PI_CODING_AGENT_DIR"
		case *CursorAdapter:
			homes, variable = adapter.Homes, "CURSOR_CONFIG_DIR"
		case *GrokAdapter:
			homes, variable = adapter.Homes, "GROK_HOME"
		default:
			return AccountEnvironment{}, errors.New("account environment unavailable")
		}
		home, err := localHome(homes, a.Key)
		if err != nil {
			return AccountEnvironment{}, errors.New("account environment unavailable")
		}
		return AccountEnvironment{accountID, daemonID, harness, variable, home}, nil
	}
	return AccountEnvironment{}, errors.New("account is not enrolled on this computer")
}
