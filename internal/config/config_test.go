// SPDX-License-Identifier: AGPL-3.0-only

package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReviewWebhookSecretFile(t *testing.T) {
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(dir, "webhook-fixture")
	for _, tc := range []struct {
		name, raw string
		mode      os.FileMode
		valid     bool
	}{
		{"valid", strings.Repeat("x", 32) + "\n", 0600, true},
		{"short", "fixture", 0600, false},
		{"oversized", strings.Repeat("x", 4097), 0600, false},
		{"readable", strings.Repeat("x", 32), 0644, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := os.WriteFile(file, []byte(tc.raw), 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(file, tc.mode); err != nil {
				t.Fatal(err)
			}
			_, err := reviewWebhookSecret(file)
			if (err == nil) != tc.valid {
				t.Fatal("unexpected host key validation result")
			}
		})
	}
	link := filepath.Join(dir, "symlink")
	if err := os.Symlink(file, link); err != nil {
		t.Fatal(err)
	}
	if _, err := reviewWebhookSecret(link); err == nil {
		t.Fatal("symlink accepted")
	}
}

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
	t.Setenv("AEON_STATUS_AUTOPILOT", "")
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
	if cfg.StatusAutopilot != "on" {
		t.Fatal("status autopilot must default on")
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

func TestStatusAutopilotServerMode(t *testing.T) {
	t.Setenv("AEON_DATABASE_URL", "postgres://example")
	t.Setenv("AEON_ENV", "prod")
	t.Setenv("AEON_MESSAGING_KEY_FILE", "")
	t.Setenv("AEON_LINK_KEY_FILE", "")
	t.Setenv("AEON_DOCTRINE_GUARD_KEY_FILE", "")
	for _, mode := range []string{"off", "suggest", "on"} {
		t.Setenv("AEON_STATUS_AUTOPILOT", mode)
		cfg, err := FromEnv()
		if err != nil || cfg.StatusAutopilot != mode {
			t.Fatalf("mode %s: %v", mode, err)
		}
	}
	for _, mode := range []string{"false", "apply", "ON", "unknown"} {
		t.Setenv("AEON_STATUS_AUTOPILOT", mode)
		if _, err := FromEnv(); err == nil {
			t.Fatal("invalid server mode started")
		}
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

// R11/R14: bad host policy must not start or silently restore public writes.
func TestDoctrineRepositoriesFromEnv(t *testing.T) {
	t.Setenv("AEON_DATABASE_URL", "postgres://example")
	t.Setenv("AEON_ENV", "prod")
	for _, name := range []string{"AEON_DOCTRINE_GUARD_KEY_FILE", "AEON_MESSAGING_KEY_FILE", "AEON_LINK_KEY_FILE", "AEON_REVIEW_WEBHOOK_SECRET_FILE", "AEON_PHONE_PUSH_VAPID_FILE", "AEON_DATABASE_PASSWORD_FILE"} {
		t.Setenv(name, "")
	}
	for _, tc := range []struct {
		name, public, private, wantPublic, wantPrivate, refusal string
		unset                                                   bool
	}{
		{name: "unchanged defaults", unset: true, wantPublic: "inspr-at/inspr-modules", wantPrivate: "inspr-at/inspr-doctrine-private"},
		{name: "custom pair", public: "team/shared-doctrine", private: "team/private-doctrine", wantPublic: "team/shared-doctrine", wantPrivate: "team/private-doctrine"},
		{name: "private only", private: "augmentoring-team/agm-doctrine", wantPrivate: "augmentoring-team/agm-doctrine"},
		{name: "canonical case", public: "TEAM/Shared", private: "TEAM/Private", wantPublic: "team/shared", wantPrivate: "team/private"},
		{name: "same repository", public: "team/doctrine", private: "TEAM/Doctrine", refusal: "must be different"},
		{name: "no private guard", public: "team/shared", refusal: "PRIVATE_REPOSITORY"},
		{name: "both disabled", refusal: "PRIVATE_REPOSITORY"},
		{name: "URL", public: "https://github.com/team/shared", private: "team/private", refusal: "PUBLIC_REPOSITORY"},
		{name: "path escape", public: "team/..", private: "team/private", refusal: "PUBLIC_REPOSITORY"},
		{name: "whitespace", private: " team/private", refusal: "PRIVATE_REPOSITORY"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("AEON_DOCTRINE_PUBLIC_REPOSITORY", tc.public)
			t.Setenv("AEON_DOCTRINE_PRIVATE_REPOSITORY", tc.private)
			if tc.unset {
				if err := os.Unsetenv("AEON_DOCTRINE_PUBLIC_REPOSITORY"); err != nil {
					t.Fatal(err)
				}
				if err := os.Unsetenv("AEON_DOCTRINE_PRIVATE_REPOSITORY"); err != nil {
					t.Fatal(err)
				}
			}
			cfg, err := FromEnv()
			if tc.refusal != "" {
				if err == nil || !strings.Contains(err.Error(), tc.refusal) {
					t.Fatal("bad repository policy did not fail startup for the expected reason")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if cfg.DoctrineRepositories == nil || cfg.DoctrineRepositories.Public() != tc.wantPublic || cfg.DoctrineRepositories.Private() != tc.wantPrivate {
				t.Fatal("deployment proposal repositories not loaded")
			}
		})
	}
}
