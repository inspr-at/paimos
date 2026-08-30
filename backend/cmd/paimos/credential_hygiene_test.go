// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU Affero General Public License as
// published by the Free Software Foundation, version 3.

package main

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zalando/go-keyring"
)

// PAI-685 credential hygiene. Two retired leak paths: the --api-key
// argv flag (visible in process lists and shell history) and the
// env-short-circuit in migrateAPIKeysToKeyring that could leave a
// legacy plaintext api_key in config.yaml forever.

func writeLegacyConfig(t *testing.T, path, secret string) {
	t.Helper()
	legacy := []byte("default_instance: ppm\ninstances:\n  ppm:\n    url: https://pm.barta.cm\n    api_key: " + secret + "\n")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, legacy, 0o600); err != nil {
		t.Fatalf("write legacy config: %v", err)
	}
}

// TestAuthLogin_APIKeyFlagRejected: the argv credential path fails
// closed with guidance, never uses or persists the value, and never
// echoes it.
func TestAuthLogin_APIKeyFlagRejected(t *testing.T) {
	const planted = "argv-credential-value"
	out, errOut, err := executeCLIForTest(t,
		"auth", "login", "--url", "https://example.invalid", "--api-key", planted)
	if err == nil {
		t.Fatal("login with --api-key succeeded, want rejection")
	}
	if !containsFold(err.Error(), "removed") || !strings.Contains(err.Error(), envAPIKey) {
		t.Fatalf("rejection lacks guidance: %q", err.Error())
	}
	for _, s := range []string{out, errOut, err.Error()} {
		if strings.Contains(s, planted) {
			t.Fatalf("credential value echoed: %q", s)
		}
	}
	if _, found, _ := keyringGet("default"); found {
		t.Fatal("rejected credential was persisted to the keyring")
	}
}

// TestAuthLogin_HelpSurface: the retired flag is hidden and the long
// help recommends only the hidden prompt (workstations) and the env
// override (headless) — no scripting-via-argv suggestion.
func TestAuthLogin_HelpSurface(t *testing.T) {
	c := authLoginCmd()
	f := c.Flags().Lookup("api-key")
	if f == nil {
		t.Fatal("api-key flag missing — legacy invocations would get an unhelpful parse error")
	}
	if !f.Hidden {
		t.Fatal("retired api-key flag still advertised in help")
	}
	if containsFold(c.Long, "scripting") {
		t.Fatalf("help still recommends argv credentials:\n%s", c.Long)
	}
	if !containsFold(c.Long, "hidden interactive prompt") {
		t.Fatalf("help does not recommend the hidden prompt:\n%s", c.Long)
	}
	for _, envName := range []string{envURL, envAPIKey} {
		if !strings.Contains(c.Long, envName) {
			t.Fatalf("help does not name the complete headless target pair %s:\n%s", envName, c.Long)
		}
		if !strings.Contains(f.Usage, envName) {
			t.Fatalf("retired flag guidance does not name the complete headless target pair %s: %s", envName, f.Usage)
		}
	}
}

// TestLoadConfig_MigratesEvenWithEnvSet: a set PAIMOS_API_KEY no longer
// skips the migration — the legacy YAML credential moves into the
// keyring and the field is scrubbed from disk.
func TestLoadConfig_MigratesEvenWithEnvSet(t *testing.T) {
	t.Setenv(envAPIKey, "env-runtime-override")
	withConfigDir(t, func(path string) {
		writeLegacyConfig(t, path, "legacy_env_secret")
		_ = keyringDelete("ppm")

		if _, err := loadConfig(); err != nil {
			t.Fatalf("loadConfig: %v", err)
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("re-read config: %v", err)
		}
		if strings.Contains(string(raw), "api_key") {
			t.Fatalf("legacy api_key still on disk despite working keyring: %q", raw)
		}
		got, found, err := keyringGet("ppm")
		if err != nil || !found || got != "legacy_env_secret" {
			t.Fatalf("keyring after migration = (%q, %v, %v), want legacy value", got, found, err)
		}
	})
}

// TestLoadConfig_KeyringDownWithEnv_KeepsFieldAndWarns: without a
// usable keyring, deleting the only stored copy would destroy the
// credential — the field stays, a warning names the cleanup, and the
// in-memory copy is scrubbed. Never a hard failure (env keeps the
// headless box working).
func TestLoadConfig_KeyringDownWithEnv_KeepsFieldAndWarns(t *testing.T) {
	t.Setenv(envAPIKey, "env-runtime-override")
	keyring.MockInitWithError(errors.New("no session bus"))
	t.Cleanup(keyring.MockInit)

	withConfigDir(t, func(path string) {
		writeLegacyConfig(t, path, "legacy_headless_secret")

		var errBuf bytes.Buffer
		oldErr := stderr
		stderr = &errBuf
		t.Cleanup(func() { stderr = oldErr })

		cfg, err := loadConfig()
		if err != nil {
			t.Fatalf("loadConfig must not fail on headless+env: %v", err)
		}
		if cfg.Instances["ppm"].APIKey != "" {
			t.Fatal("in-memory credential not scrubbed")
		}
		raw, _ := os.ReadFile(path)
		if !strings.Contains(string(raw), "legacy_headless_secret") {
			t.Fatal("field removed although the keyring was down — credential destroyed")
		}
		warning := errBuf.String()
		if !containsFold(warning, "warning") || !containsFold(warning, "remove it manually") {
			t.Fatalf("missing cleanup warning, got: %q", warning)
		}
		if strings.Contains(warning, "legacy_headless_secret") {
			t.Fatal("warning echoed the credential value")
		}
	})
}

// TestLoadConfig_KeyringDownWithoutEnv_Errors: with no env override the
// keyring failure stays a hard error pointing at the bypass — the
// pre-existing contract.
func TestLoadConfig_KeyringDownWithoutEnv_Errors(t *testing.T) {
	if os.Getenv(envAPIKey) != "" {
		t.Setenv(envAPIKey, "")
	}
	keyring.MockInitWithError(errors.New("no session bus"))
	t.Cleanup(keyring.MockInit)

	withConfigDir(t, func(path string) {
		writeLegacyConfig(t, path, "legacy_secret_nofallback")
		_, err := loadConfig()
		if err == nil {
			t.Fatal("loadConfig succeeded despite failed migration and no env override")
		}
		if !strings.Contains(err.Error(), envAPIKey) {
			t.Fatalf("error lacks the bypass hint: %v", err)
		}
	})
}
