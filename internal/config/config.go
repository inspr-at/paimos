// SPDX-License-Identifier: AGPL-3.0-only

// Package config reads the paimos serve process configuration from the environment.
package config

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path"
	"regexp"
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
	// DoctrineCredentialsDir holds host-provisioned doctrine read tokens and App
	// keys, one credential file and <ref>.allowlist.json per reference
	// (AEON_DOCTRINE_CREDENTIALS_DIR, AEON-318). Aeon stores only the names.
	DoctrineCredentialsDir string
	// PairingNixGuide is deployment-admin-owned public guidance. There is no
	// tenant or pairing-peer write path; absent configuration hides the block.
	PairingNixGuide *PairingNixGuide
	// DoctrineGuardKey is the server secret for per-tenant HMAC of the private
	// doctrine quotation guard (AEON_DOCTRINE_GUARD_KEY_FILE). In dev without a
	// file it is random and lives only in memory; in prod without a file it is
	// nil and public proposals stay refused. It is never logged.
	DoctrineGuardKey []byte
	// Reviewed host configuration only: exact private-tree path -> SHA-256.
	// Empty by default. Never supplied by a proposal or a source's HTTP API.
	DoctrineBinaryAllowlist map[string]string
	DoctrineAppID           string
	DoctrineInstallationID  string
	DoctrineAppKeyRef       string
	DoctrineAppTenantID     string
	DoctrineGateLogin       string
	DoctrineDCOAcknowledged bool
	ReviewAppID             string
	ReviewInstallationID    string
	ReviewAppKeyFile        string
	ReviewAppTenantID       string
	ReviewAppRepository     string
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
		DoctrineCredentialsDir:  os.Getenv("AEON_DOCTRINE_CREDENTIALS_DIR"),
		DoctrineAppID:           os.Getenv("AEON_DOCTRINE_APP_ID"),
		DoctrineInstallationID:  os.Getenv("AEON_DOCTRINE_INSTALLATION_ID"),
		DoctrineAppKeyRef:       os.Getenv("AEON_DOCTRINE_APP_KEY_REF"),
		DoctrineAppTenantID:     os.Getenv("AEON_DOCTRINE_APP_TENANT_ID"),
		DoctrineGateLogin:       os.Getenv("AEON_DOCTRINE_GATE_LOGIN"),
		DoctrineDCOAcknowledged: os.Getenv("AEON_DOCTRINE_DCO_ACKNOWLEDGED") == "true",
		ReviewAppID:             os.Getenv("AEON_REVIEW_APP_ID"),
		ReviewInstallationID:    os.Getenv("AEON_REVIEW_INSTALLATION_ID"),
		ReviewAppKeyFile:        os.Getenv("AEON_REVIEW_APP_KEY_FILE"),
		ReviewAppTenantID:       os.Getenv("AEON_REVIEW_APP_TENANT_ID"),
		ReviewAppRepository:     os.Getenv("AEON_REVIEW_APP_REPOSITORY"),
	}
	switch cfg.Env {
	case "dev", "prod":
	default:
		return Config{}, fmt.Errorf("AEON_ENV must be dev or prod, got %q", cfg.Env)
	}
	if cfg.DatabaseURL == "" {
		return Config{}, fmt.Errorf("AEON_DATABASE_URL is required")
	}
	var policyErr error
	cfg.DoctrineBinaryAllowlist, policyErr = doctrineBinaryAllowlist(os.Getenv("AEON_DOCTRINE_BINARY_ALLOWLIST"))
	if policyErr != nil {
		return Config{}, policyErr
	}
	if f := os.Getenv("AEON_DOCTRINE_GUARD_KEY_FILE"); f != "" {
		key, err := doctrineGuardKey(f)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return Config{}, err
		}
		// A not-yet-provisioned guard disables proposals, not server startup.
		cfg.DoctrineGuardKey = key
	} else if cfg.Env == "dev" {
		cfg.DoctrineGuardKey = make([]byte, 32)
		if _, err := rand.Read(cfg.DoctrineGuardKey); err != nil {
			return Config{}, fmt.Errorf("dev doctrine guard key: %w", err)
		}
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

func doctrineBinaryAllowlist(raw string) (map[string]string, error) {
	policy := map[string]string{}
	if raw == "" {
		return policy, nil
	}
	bad := func() (map[string]string, error) {
		return nil, fmt.Errorf("AEON_DOCTRINE_BINARY_ALLOWLIST must be a JSON object of exact relative paths and lowercase SHA-256 digests")
	}
	if len(raw) > 64<<10 {
		return bad()
	}
	d := json.NewDecoder(strings.NewReader(raw))
	// Read entries explicitly so duplicate keys cannot silently replace an
	// exception that an operator thought they reviewed.
	if token, err := d.Token(); err != nil || token != json.Delim('{') {
		return bad()
	}
	digest := regexp.MustCompile(`^[0-9a-f]{64}$`)
	for d.More() {
		token, err := d.Token()
		name, ok := token.(string)
		var hash string
		if err != nil || !ok || d.Decode(&hash) != nil || name == "." || len(name) > 300 || path.IsAbs(name) || path.Clean(name) != name || strings.HasPrefix(name, "../") || strings.ContainsAny(name, "\\\x00\r\n*?[") || !digest.MatchString(hash) || policy[name] != "" {
			return bad()
		}
		policy[name] = hash
	}
	if _, err := d.Token(); err != nil {
		return bad()
	}
	if _, err := d.Token(); err != io.EOF {
		return bad()
	}
	return policy, nil
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

// doctrineGuardKey reads a host-generated secret file of at least 32
// characters. The bytes are the HMAC master; callers derive a per-tenant key.
// The file contents are never logged.
func doctrineGuardKey(file string) ([]byte, error) {
	b, err := os.ReadFile(file)
	if err != nil {
		return nil, fmt.Errorf("AEON_DOCTRINE_GUARD_KEY_FILE: %w", err)
	}
	if len(b) > 4096 {
		return nil, fmt.Errorf("AEON_DOCTRINE_GUARD_KEY_FILE is too long")
	}
	secret := strings.TrimSpace(string(b))
	if len(secret) < 32 {
		return nil, fmt.Errorf("AEON_DOCTRINE_GUARD_KEY_FILE must hold at least 32 characters")
	}
	return []byte(secret), nil
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
