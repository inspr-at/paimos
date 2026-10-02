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

// GeminiBudgetForModel rejects unsupported settings rather than letting the
// vendor silently use its default. These are the qualified 2.5 budget ranges.
func GeminiBudgetForModel(model, effort string) (int, error) {
	budget, err := GeminiBudget(effort)
	if err != nil {
		return 0, err
	}
	switch model {
	case "gemini-2.5-flash":
		if budget <= 24576 {
			return budget, nil
		}
	case "gemini-2.5-pro":
		if budget >= 128 && budget <= 32768 {
			return budget, nil
		}
	}
	return 0, errors.New("Gemini model/thinking budget is not qualified")
}

// GeminiEffortLevel is AEON-511C's common 0–5 budget scale. Named or dynamic
// vendor settings remain unknown. This never changes the vendor's budget.
func GeminiEffortLevel(effort string) *int {
	budget, err := GeminiBudget(effort)
	if err != nil {
		return nil
	}
	level := 5
	for i, maximum := range []int{0, 1024, 4096, 16384, 32768} {
		if budget <= maximum {
			level = i
			break
		}
	}
	return &level
}

// GeminiSettings creates only a fresh, credential-free system override. Both
// the CLI wrapper and ACP use it; persistent user and repository settings stay
// untouched. The private directory is retained as local launch evidence.
func GeminiSettings(model, effort string) (string, error) {
	budget, err := GeminiBudgetForModel(model, effort)
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
