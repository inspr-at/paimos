// SPDX-License-Identifier: AGPL-3.0-only

package host

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"regexp"
	"strings"

	"github.com/inspr-at/paimos/internal/aithema/tokens"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/events"
	"github.com/inspr-at/paimos/internal/linkvault"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

var uuidRE = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[1-8][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)
var digestRE = regexp.MustCompile(`^[0-9a-f]{64}$`)

// Settings contains no cleartext credentials. The public key pin is the
// service's preview signer, independent of Aeon's delegated-token keyset.
type Settings struct {
	ServiceURL      string      `json:"service_url"`
	Location        string      `json:"location"`
	PluginPrincipal string      `json:"plugin_principal_id"`
	PreviewKeys     tokens.JWKS `json:"preview_keys"`
	PickerSHA256    string      `json:"picker_sha256"`
	Currency        string      `json:"currency"`
	SessionCap      int64       `json:"session_cap_micro"`
	PrincipalDayCap int64       `json:"principal_day_cap_micro"`
	TenantDayCap    int64       `json:"tenant_day_cap_micro"`
	// Defaults false until host-qualified processor evidence is approved.
	EvidenceVerified bool `json:"evidence_verified"`
}

type secret string

func (secret) MarshalJSON() ([]byte, error) { return []byte(`"********"`), nil }
func (secret) String() string               { return "********" }
func (secret) GoString() string             { return "********" }

type settingsWrite struct {
	Settings
	ServiceJWT *secret `json:"service_jwt,omitempty"`
}
type settingsRead struct {
	Settings
	ServiceJWT    string `json:"service_jwt"`
	ServiceJWTSet bool   `json:"service_jwt_set"`
}

func masked(s Settings, configured bool) settingsRead {
	value := ""
	if configured {
		value = "********"
	}
	return settingsRead{s, value, configured}
}
func (s Settings) validate() error {
	if _, _, err := serviceEndpoint(s); err != nil {
		return fail(400, "invalid_settings")
	}
	if !uuidRE.MatchString(s.PluginPrincipal) || !regexp.MustCompile(`^[A-Z]{3}$`).MatchString(s.Currency) {
		return fail(400, "invalid_settings")
	}
	for _, n := range []int64{s.SessionCap, s.PrincipalDayCap, s.TenantDayCap} {
		if n < 0 || n > tokens.MaxSafeInteger {
			return fail(400, "invalid_settings")
		}
	}
	if len(s.PreviewKeys.Keys) < 1 || len(s.PreviewKeys.Keys) > 8 {
		return fail(400, "invalid_settings")
	}
	seen := map[string]bool{}
	for _, k := range s.PreviewKeys.Keys {
		pub, err := base64.RawURLEncoding.Strict().DecodeString(k.X)
		if err != nil || len(pub) != ed25519.PublicKeySize || k.Type != "OKP" || k.Curve != "Ed25519" || k.Algorithm != "EdDSA" || k.Use != "sig" || k.ID == "" || len(k.ID) > 128 || seen[k.ID] {
			return fail(400, "invalid_settings")
		}
		seen[k.ID] = true
	}
	hash, err := base64.StdEncoding.Strict().DecodeString(s.PickerSHA256)
	if err != nil || len(hash) != 32 {
		return fail(400, "invalid_settings")
	}
	return nil
}

func loadSettings(ctx context.Context, tx pgx.Tx, tid string, lock bool) (Settings, []byte, error) {
	q := `SELECT settings,service_credential FROM aithema_host_settings WHERE tenant_id=$1`
	if lock {
		q += " FOR UPDATE"
	}
	var raw, encrypted []byte
	err := tx.QueryRow(ctx, q, tid).Scan(&raw, &encrypted)
	if errors.Is(err, pgx.ErrNoRows) {
		return Settings{}, nil, fail(503, "unconfigured")
	}
	var s Settings
	if err != nil || json.Unmarshal(raw, &s) != nil || s.validate() != nil {
		return Settings{}, nil, fail(503, "unavailable")
	}
	return s, encrypted, nil
}

func (m *Module) settings(ctx context.Context, tid string) (Settings, secret, error) {
	var s Settings
	var value secret
	err := db.InTenant(ctx, m.Pool, tid, func(tx pgx.Tx) error {
		var ciphertext []byte
		var err error
		s, ciphertext, err = loadSettings(ctx, tx, tid, false)
		if err != nil {
			return err
		}
		if len(ciphertext) > 0 {
			plain, err := linkvault.Decrypt(m.vaultKey, tid, "aithema-service-jwt", ciphertext)
			if err != nil {
				return fail(503, "unavailable")
			}
			value = secret(plain)
		}
		return nil
	})
	return s, value, err
}

func (m *Module) saveSettings(ctx context.Context, p tenant.Principal, in settingsWrite) (settingsRead, error) {
	if err := in.Settings.validate(); err != nil {
		return settingsRead{}, err
	}
	if _, err := m.servicePolicy.addresses(ctx, in.Settings); err != nil {
		return settingsRead{}, fail(400, "invalid_settings")
	}
	if in.ServiceJWT != nil && (*in.ServiceJWT == "********" || len(*in.ServiceJWT) > 16384 || strings.ContainsAny(string(*in.ServiceJWT), "\r\n\t ")) {
		return settingsRead{}, fail(400, "invalid_settings")
	}
	var out settingsRead
	err := db.InTenant(ctx, m.Pool, p.TenantID, func(tx pgx.Tx) error {
		// Also serializes first writes, before a settings row exists.
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, "aithema/settings/"+p.TenantID); err != nil {
			return err
		}
		prior, encrypted, err := loadSettings(ctx, tx, p.TenantID, true)
		var f *Fault
		if err != nil && !(errors.As(err, &f) && f.Code == "unconfigured") {
			return err
		}
		// Processing policy is frozen while sessions or deliveries remain.
		// Public preview verification pins and the service credential may rotate.
		beforePolicy, afterPolicy := prior, in.Settings
		beforePolicy.PreviewKeys, afterPolicy.PreviewKeys = tokens.JWKS{}, tokens.JWKS{}
		beforePolicy.PickerSHA256, afterPolicy.PickerSHA256 = "", ""
		beforeJSON, _ := json.Marshal(beforePolicy)
		afterJSON, _ := json.Marshal(afterPolicy)
		if prior.ServiceURL != "" && string(beforeJSON) != string(afterJSON) {
			var busy bool
			if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM aithema_callbacks WHERE tenant_id=$1 AND state='pending') OR EXISTS(SELECT 1 FROM aithema_host_sessions h JOIN aithema_sessions s USING(tenant_id,sid) WHERE h.tenant_id=$1 AND NOT s.tombstone)`, p.TenantID).Scan(&busy); err != nil {
				return err
			}
			if busy {
				return fail(409, "active_sessions")
			}
		}
		if _, err := tx.Exec(ctx, `UPDATE aithema_budget_policy SET principal_day_cap=$3,tenant_day_cap=$4 WHERE tenant_id=$1 AND currency=$2`, p.TenantID, in.Currency, in.PrincipalDayCap, in.TenantDayCap); err != nil {
			return err
		}
		if in.ServiceJWT != nil {
			encrypted = nil
			if *in.ServiceJWT != "" {
				encrypted, err = linkvault.Encrypt(m.vaultKey, p.TenantID, "aithema-service-jwt", string(*in.ServiceJWT))
				if err != nil {
					return fail(503, "unavailable")
				}
			}
		}
		raw, _ := json.Marshal(in.Settings)
		if _, err := tx.Exec(ctx, `INSERT INTO aithema_host_settings(tenant_id,settings,service_credential) VALUES($1,$2,$3) ON CONFLICT(tenant_id) DO UPDATE SET settings=EXCLUDED.settings,service_credential=EXCLUDED.service_credential,updated_at=now()`, p.TenantID, raw, encrypted); err != nil {
			return err
		}
		_, err = events.Append(ctx, tx, p, events.Change{Type: "aithema.settings_changed", After: map[string]any{"fields": []string{"settings"}, "credential_changed": in.ServiceJWT != nil}})
		out = masked(in.Settings, len(encrypted) > 0)
		return err
	})
	return out, err
}
