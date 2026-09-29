// SPDX-License-Identifier: AGPL-3.0-only
package agentsetup

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/inspr-at/paimos/internal/openrouter"
	"os"
	"path/filepath"
	"strings"
)

// OpenRouterKeyFile reads only the owner-selected private env file. It never
// sources shell code or consults inherited environment variables.
func OpenRouterKeyFile(path string) (string, error) {
	raw, err := ReadPrivateFile(path, 16<<10)
	if err != nil {
		return "", errors.New("OpenRouter env file must be private, owned and physical")
	}
	key := ""
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimPrefix(line, "export ")
		name, value, ok := strings.Cut(line, "=")
		if !ok || name != "OPENROUTER_API_KEY" || key != "" {
			return "", errors.New("OpenRouter env file must contain only OPENROUTER_API_KEY")
		}
		if len(value) > 1 && (value[0] == '\'' && value[len(value)-1] == '\'' || value[0] == '"' && value[len(value)-1] == '"') {
			value = value[1 : len(value)-1]
		}
		if strings.HasPrefix(value, "!") || strings.ContainsAny(value, "$`\\ \t\r\x00") {
			return "", errors.New("OpenRouter env file must contain a literal key")
		}
		key = value
	}
	if len(key) < 8 || len(key) > 1024 {
		return "", openrouter.ErrKey
	}
	return key, nil
}

// PrepareOpenRouter creates a fresh per-account pi profile. The key is never a
// Candidate field, command argument, environment entry or pairing payload.
func (d Discovery) PrepareOpenRouter(ctx context.Context, root, key string, client openrouter.Client) (Candidate, error) {
	if _, err := client.CheckKey(ctx, key); err != nil {
		return Candidate{}, err
	}
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		return Candidate{}, errors.New("private pi profile unavailable")
	}
	profile := filepath.Join(root, "pi-"+hex.EncodeToString(id[:]))
	store, err := OpenStore(profile, true)
	if err != nil {
		return Candidate{}, err
	}
	defer store.Close()
	auth := map[string]any{"openrouter": map[string]string{"type": "api_key", "key": key}}
	raw, err := json.Marshal(auth)
	if err != nil {
		return Candidate{}, errors.New("private pi credential unavailable")
	}
	if err = store.Write("auth.json", raw, true); err != nil {
		return Candidate{}, err
	}
	if err = store.Write("aeon-openrouter-profile", []byte("aeon.openrouter.v1"), true); err != nil {
		return Candidate{}, err
	}
	d.PiHome = profile
	// The remote GET above checks the credential without launching a prompt;
	// Detect still pins and verifies the actual local executable/interpreter.
	d.PiProvider = func(context.Context, string, string, string, string) (string, error) { return "openrouter", nil }
	return d.Detect(ctx, "pi", "openrouter")
}

// OpenRouterCredits opens only an Aeon-owned API-key profile, never executes
// auth.json command substitutions, and projects only numeric usage fields.
func OpenRouterCredits(ctx context.Context, home string, client openrouter.Client) (*openrouter.Credits, error) {
	raw, err := ReadPrivateFile(filepath.Join(home, "auth.json"), 16<<10)
	if err != nil {
		return nil, openrouter.ErrUnavailable
	}
	var auth struct {
		OpenRouter struct {
			Type string `json:"type"`
			Key  string `json:"key"`
		} `json:"openrouter"`
	}
	if json.Unmarshal(raw, &auth) != nil || auth.OpenRouter.Type != "api_key" || strings.HasPrefix(auth.OpenRouter.Key, "!") {
		return nil, openrouter.ErrKey
	}
	return client.CheckKey(ctx, auth.OpenRouter.Key)
}

// ConfigureOpenRouterModel publishes a custom ID for pi's bundled catalog to
// recognize. Only profiles created by this setup flow are changed; unrelated
// models.json configuration is never adopted. The running pi keeps its in-memory
// model; this file is read by the next process only.
func ConfigureOpenRouterModel(home, model string) error {
	if !openrouter.ValidModel("openrouter", model) {
		return errors.New("invalid OpenRouter model")
	}
	store, err := OpenStore(home, false)
	if err != nil {
		return err
	}
	defer store.Close()
	marker, err := store.Read("aeon-openrouter-profile", 64)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil || string(marker) != "aeon.openrouter.v1" {
		return errors.New("OpenRouter profile ownership unavailable")
	}
	if err = store.Lock(); err != nil {
		return err
	}
	type modelConfig struct {
		ID string `json:"id"`
	}
	type providerConfig struct {
		BaseURL string        `json:"baseUrl"`
		API     string        `json:"api"`
		Models  []modelConfig `json:"models"`
	}
	type config struct {
		Providers map[string]providerConfig `json:"providers"`
	}
	if raw, err := store.Read("models.json", 16<<10); err == nil {
		var old config
		dec := json.NewDecoder(bytes.NewReader(raw))
		dec.DisallowUnknownFields()
		if dec.Decode(&old) != nil || len(old.Providers) != 1 || old.Providers["openrouter"].BaseURL != openrouter.BaseURL || old.Providers["openrouter"].API != "openai-completions" {
			return errors.New("pi model configuration changed locally; review it before starting")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	raw, _ := json.Marshal(config{Providers: map[string]providerConfig{"openrouter": {BaseURL: openrouter.BaseURL, API: "openai-completions", Models: []modelConfig{{ID: model}}}}})
	return store.Write("models.json", raw, false)
}
