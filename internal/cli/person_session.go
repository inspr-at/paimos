// SPDX-License-Identifier: AGPL-3.0-only
package cli

import (
	"encoding/hex"
	"io"
	"os"
	"strings"

	"github.com/inspr-at/paimos/internal/client"
	"gopkg.in/yaml.v3"
)

// usePersonSession scopes explicit person authentication to this invocation.
// URL resolution never loads, migrates or falls back to an agent credential.
func (rt *runtime) usePersonSession(path string) error {
	var baseURL string
	if urlVar, _, _, ok := rt.envNames(); ok {
		if rt.instance != "" {
			return usagef("--instance conflicts with %s", urlVar)
		}
		baseURL = os.Getenv(urlVar)
	} else {
		configPath, err := rt.configFile()
		if err != nil {
			return err
		}
		// Decode URL configuration without loadConfig's legacy key migration.
		cfg, err := readPersonConfig(configPath)
		if err != nil {
			return err
		}
		_, inst, err := pickInstance(cfg, rt.instance)
		if err != nil {
			return err
		}
		baseURL = inst.URL
	}
	u, err := normalizeURL(baseURL)
	if err != nil {
		return err
	}
	f, err := os.Open(path)
	if err != nil {
		return usagef("cannot open session cookie file")
	}
	defer f.Close()
	raw, err := io.ReadAll(io.LimitReader(f, 4097))
	if err != nil || len(raw) > 4096 {
		return usagef("cannot read bounded session cookie file")
	}
	token := strings.TrimSpace(string(raw))
	decoded, err := hex.DecodeString(token)
	if err != nil || len(decoded) != 32 {
		return usagef("session cookie file must contain one aeon_session value")
	}
	rt.personClient = client.NewSession(u, token)
	return nil
}

func readPersonConfig(path string) (fileConfig, error) {
	var cfg fileConfig
	f, err := os.Open(path)
	if err != nil {
		return cfg, usagef("cannot open instance configuration")
	}
	defer f.Close()
	raw, err := io.ReadAll(io.LimitReader(f, 1<<20+1))
	if err != nil || len(raw) > 1<<20 {
		return cfg, usagef("cannot read bounded instance configuration")
	}
	if yaml.Unmarshal(raw, &cfg) != nil {
		return cfg, usagef("invalid instance configuration")
	}
	return cfg, nil
}
