// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package pirpc

import (
	"errors"
	"fmt"
	"strings"
)

// ValidateEffectiveState fails closed when the owned child's live get_state does
// not match the reviewed launch intent.
func ValidateEffectiveState(state StateData, expected ExpectedState) error {
	if expected.Provider == "" && expected.ModelID == "" && expected.ThinkingLevel == "" {
		return errors.New("pi rpc expected state is empty")
	}
	if state.Model == nil {
		return errors.New("pi rpc get_state missing model")
	}
	if expected.Provider != "" && !strings.EqualFold(strings.TrimSpace(state.Model.Provider), strings.TrimSpace(expected.Provider)) {
		return fmt.Errorf("pi rpc provider mismatch")
	}
	if expected.ModelID != "" && strings.TrimSpace(state.Model.ID) != strings.TrimSpace(expected.ModelID) {
		return fmt.Errorf("pi rpc model mismatch")
	}
	if expected.ThinkingLevel != "" && strings.TrimSpace(state.ThinkingLevel) != strings.TrimSpace(expected.ThinkingLevel) {
		return fmt.Errorf("pi rpc thinking mismatch")
	}
	return nil
}

// ExpectedFromLaunch derives the reviewed intent from frozen launch argv.
func ExpectedFromLaunch(cfg LaunchConfig) ExpectedState {
	return ExpectedState{
		Provider:      strings.TrimSpace(cfg.Provider),
		ModelID:       strings.TrimSpace(cfg.Model),
		ThinkingLevel: strings.TrimSpace(cfg.ThinkingLevel),
	}
}
