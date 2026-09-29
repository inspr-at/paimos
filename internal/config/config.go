// SPDX-License-Identifier: AGPL-3.0-only

// Package config reads the paimos serve process configuration from the environment.
package config

import (
	"crypto/rand"
	"crypto/sha256"
	"fmt"
	"net/url"
	"os"
	"strings"
)

// Config is the process configuration for `paimos serve`.
type Config struct {
	Addr                string
	DatabaseURL         string
	Env                 string // "dev" or "prod"
	PublicURL           string
	WebDir              string
	BootstrapTenantSlug string
	BootstrapTenantName string
	// MessagingKey encrypts inbox receiver targets. It is SHA-256 of the
	// AEON_MESSAGING_KEY_FILE contents; in dev without a file it is random and
	// lives only in memory; in prod without a file it is nil and messaging is off.
	MessagingKey []byte
	// LinkKey encrypts retained public quote capabilities. It is derived from
	// AEON_LINK_KEY_FILE or the existing host messaging-key file. Without a
	// persistent host key, links can only be shown once.
	LinkKey []byte
	// FilesDir is the attachment store root (AEON_FILES_DIR, default data/files).
	FilesDir string
	// DoctrineCredentialsDir holds host-provisioned read-only tokens for private
	// doctrine repositories, one token file and <ref>.allowlist.json per reference
	// (AEON_DOCTRINE_CREDENTIALS_DIR, AEON-318). Aeon stores only the names.
	DoctrineCredentialsDir string
	// PairingNixGuide is deployment-admin-owned public guidance. There is no
	// tenant or pairing-peer write path; absent configuration hides the block.
	PairingNixGuide *PairingNixGuide
}

// FromEnv reads AEON_* variables. Empty optional values take their defaults.
// AEON_DATABASE_URL is required. AEON_ENV must be dev or prod.
// AEON_DATABASE_PASSWORD_FILE, when set, supplies the database password from a
// file (host-generated secret) so it never appears in the environment.
func FromEnv() (Config, error) {
	cfg := Config{
		Addr:                getenv("AEON_ADDR", ":8080"),
		DatabaseURL:         os.Getenv("AEON_DATABASE_URL"),
		Env:                 getenv("AEON_ENV", "dev"),
		PublicURL:           os.Getenv("AEON_PUBLIC_URL"),
		WebDir:              os.Getenv("AEON_WEB_DIR"),
		BootstrapTenantSlug: getenv("AEON_BOOTSTRAP_TENANT_SLUG", "inspr"),
		BootstrapTenantName: getenv("AEON_BOOTSTRAP_TENANT_NAME", "INSPR"),
		FilesDir:            getenv("AEON_FILES_DIR", "data/files"),
		// Only the directory path is read here; a token is read at fetch time.
		DoctrineCredentialsDir: os.Getenv("AEON_DOCTRINE_CREDENTIALS_DIR"),
	}
	switch cfg.Env {
	case "dev", "prod":
	default:
		return Config{}, fmt.Errorf("AEON_ENV must be dev or prod, got %q", cfg.Env)
	}
	if cfg.DatabaseURL == "" {
		return Config{}, fmt.Errorf("AEON_DATABASE_URL is required")
	}
	if f := os.Getenv("AEON_MESSAGING_KEY_FILE"); f != "" {
		key, err := messagingKey(f)
		if err != nil {
			return Config{}, err
		}
		cfg.MessagingKey = key
	} else if cfg.Env == "dev" {
		cfg.MessagingKey = make([]byte, 32)
		if _, err := rand.Read(cfg.MessagingKey); err != nil {
			return Config{}, fmt.Errorf("dev messaging key: %w", err)
		}
	}
	var err error
	cfg.PairingNixGuide, err = parsePairingNixGuide(os.Getenv("AEON_PAIRING_NIX_GUIDE_JSON"))
	if err != nil {
		return Config{}, err
	}
	cfg.LinkKey, err = LinkKeyFromEnv()
	if err != nil {
		return Config{}, err
	}
	if f := os.Getenv("AEON_DATABASE_PASSWORD_FILE"); f != "" {
		u, err := withPasswordFile(cfg.DatabaseURL, f)
		if err != nil {
			return Config{}, err
		}
		cfg.DatabaseURL = u
	}
	return cfg, nil
}

// LinkKeyFromEnv uses an explicit link file or the existing host messaging
// key file, with domain separation from messaging encryption. An absent host
// file has no ephemeral fallback: links must survive a server restart.
func LinkKeyFromEnv() ([]byte, error) {
	file := os.Getenv("AEON_LINK_KEY_FILE")
	name := "AEON_LINK_KEY_FILE"
	if file == "" {
		file = os.Getenv("AEON_MESSAGING_KEY_FILE")
		name = "AEON_MESSAGING_KEY_FILE"
		if file == "" {
			return nil, nil
		}
	}
	b, err := os.ReadFile(file)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", name, err)
	}
	secret := strings.TrimSpace(string(b))
	if len(secret) < 32 {
		return nil, fmt.Errorf("%s must hold at least 32 characters", name)
	}
	sum := sha256.Sum256([]byte("aeon/link-vault/v1\x00" + secret))
	return sum[:], nil
}

// messagingKey reads a host-generated secret file (at least 32 characters)
// and derives the 32-byte AES key from it.
func messagingKey(file string) ([]byte, error) {
	b, err := os.ReadFile(file)
	if err != nil {
		return nil, fmt.Errorf("AEON_MESSAGING_KEY_FILE: %w", err)
	}
	secret := strings.TrimSpace(string(b))
	if len(secret) < 32 {
		return nil, fmt.Errorf("AEON_MESSAGING_KEY_FILE must hold at least 32 characters")
	}
	sum := sha256.Sum256([]byte(secret))
	return sum[:], nil
}

func getenv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// withPasswordFile sets the password of a postgres:// URL from the first line of file.
func withPasswordFile(dsn, file string) (string, error) {
	b, err := os.ReadFile(file)
	if err != nil {
		return "", fmt.Errorf("AEON_DATABASE_PASSWORD_FILE: %w", err)
	}
	pw := strings.TrimSpace(strings.SplitN(string(b), "\n", 2)[0])
	if pw == "" {
		return "", fmt.Errorf("AEON_DATABASE_PASSWORD_FILE is empty")
	}
	u, err := url.Parse(dsn)
	if err != nil || u.User == nil {
		return "", fmt.Errorf("AEON_DATABASE_URL must be a postgres:// URL with a user")
	}
	u.User = url.UserPassword(u.User.Username(), pw)
	return u.String(), nil
}
