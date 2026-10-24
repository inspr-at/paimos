// SPDX-License-Identifier: AGPL-3.0-only

// Package devicesessions owns person device-session families and the native
// login handoff. Agentd's owned sessions live in internal/nativesessions.
package devicesessions

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/tenant"
)

const (
	RedirectURI = "paimos-app://auth/callback"
	LoginTTL    = 10 * time.Minute
	CodeTTL     = time.Minute
	AccessTTL   = 15 * time.Minute
	IdleTTL     = 30 * 24 * time.Hour
	AbsoluteTTL = 180 * 24 * time.Hour
)

var (
	ErrRefused     = errors.New("native login refused")
	ErrRateLimited = errors.New("native login attempt limit reached")
)

// Service has no bearer middleware or refresh policy. Those consumers share
// the family row and its hash/expiry fields in later packages.
type Service struct {
	pool *pgxpool.Pool
	now  func() time.Time
}

func New(pool *pgxpool.Pool, clock ...func() time.Time) *Service {
	s := &Service{pool: pool, now: time.Now}
	if len(clock) > 0 && clock[0] != nil {
		s.now = clock[0]
	}
	return s
}

type Start struct {
	TenantID, TenantSlug, Challenge, Origin, State string
}

// Start retains the raw app state only until completion or expiry cleanup.
func (s *Service) Start(ctx context.Context, in Start) (string, error) {
	ctx = db.NoProjects(ctx, "native login start")
	stateHash := sha256.Sum256([]byte(in.State))
	var id string
	err := db.InTenant(ctx, s.pool, in.TenantID, func(tx pgx.Tx) error {
		if err := db.LockTenant(ctx, tx, in.TenantID); err != nil {
			return err
		}
		now := s.now()
		var count int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM
			(SELECT 1 FROM native_login_transactions WHERE tenant_id=$1::uuid AND created_at>$2 LIMIT 120) recent`, in.TenantID, now.Add(-time.Minute)).Scan(&count); err != nil {
			return err
		}
		if count >= 120 {
			return ErrRateLimited
		}
		return tx.QueryRow(ctx, `INSERT INTO native_login_transactions
			(tenant_id,tenant_slug,challenge_app,redirect_uri,issuing_origin,state_app,state_hash,created_at,expires_at)
			VALUES($1::uuid,$2,$3,$4,$5,$6,$7,$8,$9) RETURNING id::text`,
			in.TenantID, in.TenantSlug, in.Challenge, RedirectURI, in.Origin, in.State, stateHash[:], now, now.Add(LoginTTL)).Scan(&id)
	})
	return id, err
}

type Completion struct {
	TenantID, TenantSlug, TransactionID, Origin, PersonID, IdentityID string
}
type Handoff struct{ Code, State string }

// Complete captures state under FOR UPDATE, clears it with the code binding,
// then returns only after commit. Repeated/concurrent callbacks have one winner.
func (s *Service) Complete(ctx context.Context, in Completion) (Handoff, error) {
	p := tenant.Principal{TenantID: in.TenantID, ID: in.PersonID, Kind: tenant.Person}
	ctx = db.NoProjects(tenant.WithPrincipal(ctx, p), "native login callback")
	code, err := credential("pnl_", in.TenantID)
	if err != nil {
		return Handoff{}, err
	}
	codeHash := sha256.Sum256([]byte(code))
	out := Handoff{Code: code}
	err = db.InTenant(ctx, s.pool, in.TenantID, func(tx pgx.Tx) error {
		if err := db.LockTenant(ctx, tx, in.TenantID); err != nil {
			return err
		}
		if err := member(ctx, tx, p, in.IdentityID); err != nil {
			return err
		}
		now := s.now()
		if err := tx.QueryRow(ctx, `SELECT state_app FROM native_login_transactions
			WHERE tenant_id=$1::uuid AND id=$2::uuid AND tenant_slug=$3 AND issuing_origin=$4
			AND callback_at IS NULL AND state_app IS NOT NULL AND expires_at>$5 FOR UPDATE`,
			in.TenantID, in.TransactionID, in.TenantSlug, in.Origin, now).Scan(&out.State); err != nil {
			return refused(err)
		}
		tag, err := tx.Exec(ctx, `UPDATE native_login_transactions
			SET person_id=$3::uuid,identity_id=$4::uuid,code_hash=$5,code_expires_at=$6,callback_at=$7,state_app=NULL
			WHERE tenant_id=$1::uuid AND id=$2::uuid AND callback_at IS NULL AND expires_at>$7`,
			in.TenantID, in.TransactionID, in.PersonID, in.IdentityID, codeHash[:], now.Add(CodeTTL), now)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return ErrRefused
		}
		return nil
	})
	if err != nil {
		return Handoff{}, err
	}
	return out, nil
}

type Exchange struct {
	Code, Verifier, State, RedirectURI, Origin, TenantSlug, DeviceLabel string
}
type Tokens struct {
	AccessToken     string `json:"access_token"`
	RefreshToken    string `json:"refresh_token"`
	ExpiresIn       int    `json:"expires_in"`
	DeviceSessionID string `json:"device_session_id"`
	TenantID        string `json:"tenant_id"`
	PersonID        string `json:"person_id"`
}

// Exchange consumes the code and creates the family in one tenant transaction.
// Wrong bindings and membership loss leave the code unconsumed and mint nothing.
func (s *Service) Exchange(ctx context.Context, in Exchange) (Tokens, error) {
	tid, ok := credentialTenant(in.Code, "pnl_")
	if !ok {
		return Tokens{}, ErrRefused
	}
	ctx = db.NoProjects(ctx, "native login exchange")
	hash := sha256.Sum256([]byte(in.Code))
	stateHash := sha256.Sum256([]byte(in.State))
	verifierHash := sha256.Sum256([]byte(in.Verifier))
	challenge := base64.RawURLEncoding.EncodeToString(verifierHash[:])
	out := Tokens{TenantID: tid, ExpiresIn: int(AccessTTL.Seconds())}
	err := db.InTenant(ctx, s.pool, tid, func(tx pgx.Tx) error {
		// Refuse unknown codes before taking the tenant-wide fence.
		var known bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM native_login_transactions WHERE tenant_id=$1::uuid AND code_hash=$2)`, tid, hash[:]).Scan(&known); err != nil {
			return err
		}
		if !known {
			return ErrRefused
		}
		if err := db.LockTenant(ctx, tx, tid); err != nil {
			return err
		}
		var id, slug, origin, redirect, expectedChallenge, identityID string
		var expectedCode, expectedState []byte
		now := s.now()
		if err := tx.QueryRow(ctx, `SELECT id::text,tenant_slug,issuing_origin,redirect_uri,challenge_app,
			code_hash,state_hash,person_id::text,identity_id::text FROM native_login_transactions
			WHERE tenant_id=$1::uuid AND code_hash=$2 AND callback_at IS NOT NULL AND used_at IS NULL
			AND expires_at>$3 AND code_expires_at>$3 FOR UPDATE`, tid, hash[:], now).
			Scan(&id, &slug, &origin, &redirect, &expectedChallenge, &expectedCode, &expectedState, &out.PersonID, &identityID); err != nil {
			return refused(err)
		}
		if subtle.ConstantTimeCompare(expectedCode, hash[:]) != 1 || subtle.ConstantTimeCompare(expectedState, stateHash[:]) != 1 ||
			subtle.ConstantTimeCompare([]byte(expectedChallenge), []byte(challenge)) != 1 ||
			origin != in.Origin || redirect != in.RedirectURI || (in.TenantSlug != "" && slug != in.TenantSlug) {
			return ErrRefused
		}
		p := tenant.Principal{TenantID: tid, ID: out.PersonID, Kind: tenant.Person}
		if err := member(ctx, tx, p, identityID); err != nil {
			return err
		}
		var err error
		out.AccessToken, err = credential("pdv_", tid)
		if err != nil {
			return err
		}
		out.RefreshToken, err = credential("pdr_", tid)
		if err != nil {
			return err
		}
		accessHash := sha256.Sum256([]byte(out.AccessToken))
		refreshHash := sha256.Sum256([]byte(out.RefreshToken))
		label := in.DeviceLabel
		if label == "" {
			label = "PAIMOS App"
		}
		tag, err := tx.Exec(ctx, `UPDATE native_login_transactions SET used_at=$3
			WHERE tenant_id=$1::uuid AND id=$2::uuid AND used_at IS NULL AND expires_at>$3 AND code_expires_at>$3`, tid, id, now)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return ErrRefused
		}
		return tx.QueryRow(ctx, `INSERT INTO device_sessions
			(tenant_id,person_id,device_label,access_token_hash,refresh_token_hash,access_expires_at,created_at,last_used_at,idle_expires_at,absolute_expires_at)
			VALUES($1::uuid,$2::uuid,$3,$4,$5,$6,$7,$7,$8,$9) RETURNING id::text`,
			tid, out.PersonID, label, accessHash[:], refreshHash[:], now.Add(AccessTTL), now, now.Add(IdleTTL), now.Add(AbsoluteTTL)).Scan(&out.DeviceSessionID)
	})
	if err != nil {
		return Tokens{}, err
	}
	return out, nil
}

// The tenant fence serializes membership changes. Recheck the exact OIDC
// binding and current role grants in the final write, not just at callback.
func member(ctx context.Context, tx pgx.Tx, p tenant.Principal, identityID string) error {
	var id string
	if err := tx.QueryRow(ctx, `SELECT id::text FROM principals WHERE tenant_id=$1::uuid AND id=$2::uuid
		AND identity_id=$3::uuid AND kind='person' AND status='active' FOR SHARE`, p.TenantID, p.ID, identityID).Scan(&id); err != nil {
		return refused(err)
	}
	// Match the existing profile self-service gate, including project-only
	// people and portal customers. No native permission or key scope is added.
	for _, permission := range []string{"profile.read", "profile.portal_read"} {
		err := authz.RequireTx(ctx, tx, p, permission, authz.Scope{AnyProject: true})
		if err == nil {
			return nil
		}
		if !errors.Is(err, authz.ErrForbidden) {
			return err
		}
	}
	return ErrRefused
}

func refused(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrRefused
	}
	return err
}

func credential(prefix, tenantID string) (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return prefix + strings.ReplaceAll(tenantID, "-", "") + "_" + base64.RawURLEncoding.EncodeToString(b), nil
}

func credentialTenant(value, prefix string) (string, bool) {
	raw, ok := strings.CutPrefix(value, prefix)
	if !ok || len(raw) != 32+1+43 || raw[32] != '_' {
		return "", false
	}
	u, err := hex.DecodeString(raw[:32])
	if err != nil || len(u) != 16 {
		return "", false
	}
	b, err := base64.RawURLEncoding.DecodeString(raw[33:])
	if err != nil || len(b) != 32 {
		return "", false
	}
	h := raw[:32]
	return h[:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:], true
}
