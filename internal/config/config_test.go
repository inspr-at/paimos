// SPDX-License-Identifier: AGPL-3.0-only

package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDoctrineBinaryAllowlist(t *testing.T) {
	hash := strings.Repeat("a", 64)
	good := `{"assets/logo.png":"` + hash + `"}`
	policy, err := doctrineBinaryAllowlist(good)
	if err != nil || policy["assets/logo.png"] != hash {
		t.Fatal("valid exact exception rejected")
	}
	for _, raw := range []string{"", "{}"} {
		policy, err := doctrineBinaryAllowlist(raw)
		if err != nil || len(policy) != 0 {
			t.Fatal("default must allow nothing")
		}
	}
	for _, raw := range []string{"null", "[]", `{`, good + good, `{"x":12}`, `{"x":"short"}`, strings.Replace(good, "assets/logo.png", "../logo.png", 1), strings.Replace(good, "assets/logo.png", "/logo.png", 1), strings.Replace(good, "assets/logo.png", "assets/*.png", 1), `{"x":"` + hash + `","x":"` + hash + `"}`} {
		if _, err := doctrineBinaryAllowlist(raw); err == nil {
			t.Fatal("invalid exception config accepted")
		}
	}
	t.Setenv("AEON_DATABASE_URL", "postgres://example")
	t.Setenv("AEON_ENV", "prod")
	t.Setenv("AEON_DOCTRINE_GUARD_KEY_FILE", "")
	t.Setenv("AEON_MESSAGING_KEY_FILE", "")
	t.Setenv("AEON_DOCTRINE_BINARY_ALLOWLIST", good)
	config, err := FromEnv()
	if err != nil || config.DoctrineBinaryAllowlist["assets/logo.png"] != hash {
		t.Fatal("host exception was not loaded")
	}
	t.Setenv("AEON_DOCTRINE_BINARY_ALLOWLIST", "invalid")
	if _, err := FromEnv(); err == nil {
		t.Fatal("invalid host exception config started")
	}
}

func TestFromEnvDefaults(t *testing.T) {
	t.Setenv("AEON_DATABASE_URL", "postgres://example")
	t.Setenv("AEON_DOCTRINE_GUARD_KEY_FILE", "")
	t.Setenv("AEON_ADDR", "")
	t.Setenv("AEON_ENV", "")
	t.Setenv("AEON_PUBLIC_URL", "")
	t.Setenv("AEON_WEB_DIR", "")
	t.Setenv("AEON_AITHEMA_OPERATOR_LOCAL_SERVICES", "")
	t.Setenv("AEON_BOOTSTRAP_TENANT_SLUG", "")
	t.Setenv("AEON_BOOTSTRAP_TENANT_NAME", "")

	cfg, err := FromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Addr != ":8080" || cfg.Env != "dev" {
		t.Fatalf("addr/env = %s %s", cfg.Addr, cfg.Env)
	}
	if len(cfg.AithemaOperatorLocalServices) != 0 {
		t.Fatal("operator-local service exceptions must default off")
	}
	if cfg.BootstrapTenantSlug != "inspr" || cfg.BootstrapTenantName != "INSPR" {
		t.Fatalf("bootstrap = %s %s", cfg.BootstrapTenantSlug, cfg.BootstrapTenantName)
	}
	if cfg.DatabaseURL != "postgres://example" || cfg.PublicURL != "" || cfg.WebDir != "" {
		t.Fatalf("unexpected cfg %+v", cfg)
	}
}

func TestFromEnvOverrides(t *testing.T) {
	t.Setenv("AEON_DATABASE_URL", "postgres://example")
	t.Setenv("AEON_DOCTRINE_GUARD_KEY_FILE", "")
	t.Setenv("AEON_ADDR", ":9090")
	t.Setenv("AEON_ENV", "prod")
	t.Setenv("AEON_PUBLIC_URL", "https://aeon.example")
	t.Setenv("AEON_WEB_DIR", "/var/aeon/web")
	t.Setenv("AEON_AITHEMA_OPERATOR_LOCAL_SERVICES", "127.0.0.1:8910,[::1]:8910")
	t.Setenv("AEON_BOOTSTRAP_TENANT_SLUG", "studio")
	t.Setenv("AEON_BOOTSTRAP_TENANT_NAME", "Studio")

	cfg, err := FromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Addr != ":9090" || cfg.Env != "prod" || cfg.PublicURL != "https://aeon.example" {
		t.Fatalf("%+v", cfg)
	}
	if cfg.WebDir != "/var/aeon/web" || cfg.BootstrapTenantSlug != "studio" || cfg.BootstrapTenantName != "Studio" {
		t.Fatalf("%+v", cfg)
	}
	if len(cfg.AithemaOperatorLocalServices) != 2 || cfg.AithemaOperatorLocalServices[0] != "127.0.0.1:8910" || cfg.AithemaOperatorLocalServices[1] != "[::1]:8910" {
		t.Fatal("deployment service exceptions were not loaded")
	}
}

func TestFromEnvRejects(t *testing.T) {
	t.Setenv("AEON_DOCTRINE_GUARD_KEY_FILE", "")
	t.Setenv("AEON_DATABASE_URL", "")
	t.Setenv("AEON_ENV", "dev")
	if _, err := FromEnv(); err == nil {
		t.Fatal("expected missing database url to fail")
	}

	t.Setenv("AEON_DATABASE_URL", "postgres://example")
	t.Setenv("AEON_ENV", "staging")
	if _, err := FromEnv(); err == nil {
		t.Fatal("expected invalid env to fail")
	}
}

func TestMessagingKey(t *testing.T) {
	t.Setenv("AEON_DATABASE_URL", "postgres://aeon@localhost/aeon")
	t.Setenv("AEON_DOCTRINE_GUARD_KEY_FILE", "")
	dir := t.TempDir()
	file := filepath.Join(dir, "messaging-key")
	if err := os.WriteFile(file, []byte("0123456789abcdefghijklmnopqrstuvwxyzABCD\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AEON_ENV", "prod")
	t.Setenv("AEON_MESSAGING_KEY_FILE", file)
	cfg, err := FromEnv()
	if err != nil || len(cfg.MessagingKey) != 32 {
		t.Fatalf("key from file: %v %d", err, len(cfg.MessagingKey))
	}
	again, _ := FromEnv()
	if string(again.MessagingKey) != string(cfg.MessagingKey) {
		t.Fatal("key from the same file must be stable")
	}
	if err := os.WriteFile(file, []byte("short"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := FromEnv(); err == nil {
		t.Fatal("expected a short key file to fail")
	}
	t.Setenv("AEON_MESSAGING_KEY_FILE", "")
	if cfg, err := FromEnv(); err != nil || cfg.MessagingKey != nil {
		t.Fatalf("prod without a file must disable messaging: %v %v", err, cfg.MessagingKey)
	}
	t.Setenv("AEON_ENV", "dev")
	if cfg, err := FromEnv(); err != nil || len(cfg.MessagingKey) != 32 {
		t.Fatalf("dev without a file gets an in-memory key: %v", err)
	}
}

func TestDoctrineGuardKey(t *testing.T) {
	t.Setenv("AEON_DATABASE_URL", "postgres://aeon@localhost/aeon")
	t.Setenv("AEON_MESSAGING_KEY_FILE", "")
	file := filepath.Join(t.TempDir(), "guard-key")
	secret := "synthetic-doctrine-guard-key-0123456789"
	if err := os.WriteFile(file, []byte(secret+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AEON_ENV", "prod")
	t.Setenv("AEON_DOCTRINE_GUARD_KEY_FILE", file)
	cfg, err := FromEnv()
	if err != nil || string(cfg.DoctrineGuardKey) != secret {
		t.Fatal("guard key was not read")
	}
	again, err := FromEnv()
	if err != nil || string(again.DoctrineGuardKey) != string(cfg.DoctrineGuardKey) {
		t.Fatal("guard key from the same file must be stable")
	}
	if err := os.WriteFile(file, []byte("short"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := FromEnv(); err == nil || strings.Contains(err.Error(), secret) {
		t.Fatal("short guard key accepted or reflected")
	}
	t.Setenv("AEON_DOCTRINE_GUARD_KEY_FILE", "")
	if cfg, err := FromEnv(); err != nil || cfg.DoctrineGuardKey != nil {
		t.Fatal("prod without a file must leave the guard key unset")
	}
	t.Setenv("AEON_ENV", "dev")
	dev, err := FromEnv()
	if err != nil || len(dev.DoctrineGuardKey) != 32 {
		t.Fatal("dev without a file gets an in-memory key")
	}
	other, err := FromEnv()
	if err != nil || string(other.DoctrineGuardKey) == string(dev.DoctrineGuardKey) {
		t.Fatal("dev guard key must not repeat")
	}
}

func TestLinkKeyRequiresPersistentFile(t *testing.T) {
	t.Setenv("AEON_DATABASE_URL", "postgres://example")
	t.Setenv("AEON_DOCTRINE_GUARD_KEY_FILE", "")
	t.Setenv("AEON_LINK_KEY_FILE", "")
	t.Setenv("AEON_MESSAGING_KEY_FILE", "")
	cfg, err := FromEnv()
	if err != nil || cfg.LinkKey != nil {
		t.Fatalf("no file must disable retention: %v", err)
	}
	file := filepath.Join(t.TempDir(), "link-key")
	if err := os.WriteFile(file, []byte("synthetic-host-generated-link-key-0123456789\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AEON_LINK_KEY_FILE", file)
	cfg, err = FromEnv()
	if err != nil || len(cfg.LinkKey) != 32 {
		t.Fatalf("link key not derived: %v", err)
	}
	again, err := LinkKeyFromEnv()
	if err != nil || string(again) != string(cfg.LinkKey) {
		t.Fatalf("link key is not stable: %v", err)
	}
	if err := os.WriteFile(file, []byte("short"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := FromEnv(); err == nil {
		t.Fatal("short link key accepted")
	}
	t.Setenv("AEON_LINK_KEY_FILE", "")
	if err := os.WriteFile(file, []byte("synthetic-host-generated-link-key-0123456789\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AEON_MESSAGING_KEY_FILE", file)
	cfg, err = FromEnv()
	if err != nil || len(cfg.LinkKey) != 32 || string(cfg.LinkKey) == string(cfg.MessagingKey) {
		t.Fatalf("messaging file fallback must be domain-separated: %v", err)
	}
}

func TestMissingDoctrineGuardFileDoesNotPreventStartup(t *testing.T) {
	t.Setenv("AEON_DATABASE_URL", "postgres://example")
	t.Setenv("AEON_MESSAGING_KEY_FILE", "")
	t.Setenv("AEON_LINK_KEY_FILE", "")
	t.Setenv("AEON_DOCTRINE_GUARD_KEY_FILE", filepath.Join(t.TempDir(), "not-provisioned"))
	for _, mode := range []string{"prod", "dev"} {
		t.Run(mode, func(t *testing.T) {
			t.Setenv("AEON_ENV", mode)
			cfg, err := FromEnv()
			if err != nil || cfg.DoctrineGuardKey != nil {
				t.Fatal("missing guard must allow startup without inventing a key")
			}
			if cfg.DatabaseURL != "postgres://example" {
				t.Fatal("unrelated configuration changed")
			}
		})
	}
}
