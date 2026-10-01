// SPDX-License-Identifier: AGPL-3.0-only

package harnesslaunch

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
)

// GeminiBudget accepts explicit token budgets. Dynamic (-1) and named efforts
// have no fixed 0–5 mapping, so they cannot silently replace a registry pin.
func GeminiBudget(effort string) (int, error) {
	if effort == "off" {
		return 0, nil
	}
	n, err := strconv.Atoi(effort)
	if err != nil || n < 0 || n > 1_000_000 || strconv.Itoa(n) != effort {
		return 0, errors.New("Gemini effort requires an explicit thinking token budget")
	}
	return n, nil
}

// GeminiSettings creates only a fresh, credential-free system override. Both
// the CLI wrapper and ACP use it; persistent user and repository settings stay
// untouched. The private directory is retained as local launch evidence.
func GeminiSettings(model, effort string) (string, error) {
	budget, err := GeminiBudget(effort)
	if err != nil {
		return "", err
	}
	dir, err := os.MkdirTemp("", "aeon-gemini-settings-")
	if err != nil {
		return "", err
	}
	data, err := json.Marshal(map[string]any{
		"modelConfigs": map[string]any{"overrides": []any{map[string]any{
			"match": map[string]string{"model": model},
			"modelConfig": map[string]any{"generateContentConfig": map[string]any{
				"thinkingConfig": map[string]int{"thinkingBudget": budget},
			}},
		}}},
	})
	if err != nil {
		return "", err
	}
	path := filepath.Join(dir, "settings.json")
	return path, os.WriteFile(path, data, 0o600)
}
