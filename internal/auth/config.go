// SPDX-License-Identifier: AGPL-3.0-only

package auth

import (
	"crypto/rand"
	"errors"
	"fmt"
	"os"
	"strings"
	"unicode"
)

const (
	envDev             = "dev"
	envOIDCIssuer      = "AEON_OIDC_ISSUER"
	envOIDCClientID    = "AEON_OIDC_CLIENT_ID"
	envOIDCDisplayName = "AEON_OIDC_DISPLAY_NAME"
	envPublicURL       = "AEON_PUBLIC_URL"
	envSessionKeyFile  = "AEON_SESSION_KEY_FILE"
	envAppEnv          = "AEON_ENV"
	envTenantSlug      = "AEON_BOOTSTRAP_TENANT_SLUG"
	envAdminEmail      = "AEON_BOOTSTRAP_ADMIN_EMAIL"
	defaultTenantSlug  = "inspr"
	minSessionKey      = 32
	maxOIDCDisplayName = 48
)

// Config is the process configuration for people (OIDC) and agents (API keys).
type Config struct {
	Env                 string
	PublicURL           string
	OIDCIssuer          string
	OIDCClientID        string
	OIDCDisplayName     string
	SessionKey          []byte
	BootstrapTenantSlug string
	BootstrapAdminEmail string
}

// Dev reports whether the dev-only login route and non-Secure cookies are on.
func (c Config) Dev() bool { return c.Env == envDev }

// FromEnv reads configuration. AEON_SESSION_KEY_FILE must contain at least 32
// bytes, except when AEON_ENV=dev and the file is unset: then the key is a
// random 32 bytes kept only in memory. An unset AEON_ENV does not enable dev login.
func FromEnv() (Config, error) {
	env := strings.TrimSpace(os.Getenv(envAppEnv))
	key, err := sessionKey(env)
	if err != nil {
		return Config{}, err
	}
	slug := strings.TrimSpace(os.Getenv(envTenantSlug))
	if slug == "" {
		slug = defaultTenantSlug
	}
	return Config{
		Env:                 env,
		PublicURL:           strings.TrimRight(strings.TrimSpace(os.Getenv(envPublicURL)), "/"),
		OIDCIssuer:          strings.TrimRight(strings.TrimSpace(os.Getenv(envOIDCIssuer)), "/"),
		OIDCClientID:        strings.TrimSpace(os.Getenv(envOIDCClientID)),
		OIDCDisplayName:     oidcDisplayName(os.Getenv(envOIDCDisplayName)),
		SessionKey:          key,
		BootstrapTenantSlug: slug,
		BootstrapAdminEmail: strings.TrimSpace(os.Getenv(envAdminEmail)),
	}, nil
}

// oidcDisplayName keeps the public label short and free of invisible controls.
// Whitespace controls become spaces so words remain separated.
func oidcDisplayName(value string) string {
	value = strings.Map(func(r rune) rune {
		if unicode.IsSpace(r) {
			return ' '
		}
		if unicode.IsControl(r) || unicode.Is(unicode.Bidi_Control, r) {
			return -1
		}
		return r
	}, strings.TrimSpace(value))
	runes := []rune(strings.Join(strings.Fields(value), " "))
	if len(runes) > maxOIDCDisplayName {
		runes = runes[:maxOIDCDisplayName]
	}
	return strings.TrimSpace(string(runes))
}

func sessionKey(env string) ([]byte, error) {
	path := os.Getenv(envSessionKeyFile)
	if path == "" {
		if env != envDev {
			return nil, errors.New("AEON_SESSION_KEY_FILE is required")
		}
		key := make([]byte, minSessionKey)
		if _, err := rand.Read(key); err != nil {
			return nil, fmt.Errorf("session key: %w", err)
		}
		return key, nil
	}
	key, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read AEON_SESSION_KEY_FILE: %w", err)
	}
	if len(key) < minSessionKey {
		return nil, errors.New("AEON_SESSION_KEY_FILE must contain at least 32 bytes")
	}
	return key, nil
}
