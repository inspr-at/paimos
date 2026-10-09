// SPDX-License-Identifier: AGPL-3.0-only

// Package phoneapprovals adds passkey-verified decisions and opt-in Web Push.
// Push is a pointer to an authenticated review, never a bearer approval.
package phoneapprovals

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"

	webpush "github.com/SherClockHolmes/webpush-go"
	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/webauthn"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/inspr-at/paimos/internal/agentpairing"
	"github.com/inspr-at/paimos/internal/approvals"
	"github.com/inspr-at/paimos/internal/attachwatch"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/events"
	"github.com/inspr-at/paimos/internal/httpapi"
	"github.com/inspr-at/paimos/internal/tenant"
)

type Module struct {
	pool    *pgxpool.Pool
	pairing *agentpairing.Module
	wa      *webauthn.WebAuthn
	origin  string
	vault   cipher.AEAD
	vapid   *webpush.Options
	send    func(context.Context, []byte, *webpush.Subscription, *webpush.Options) (*http.Response, error)
	// Native step-up owns target authorization. The adapter is wired at startup
	// without a package cycle; decisions remain on its protected native routes.
	StepUpReviewTx func(context.Context, pgx.Tx, tenant.Principal, string) (Review, error)
}

// New accepts only deployment-owned origin and file-provisioned VAPID keys.
// Missing keys disable push. An absent/invalid origin disables ceremonies.
// The provisioned master key derives a separate phone-push encryption key.
func New(pool *pgxpool.Pool, pairing *agentpairing.Module, publicURL string, masterKey []byte, vapid *webpush.Options) *Module {
	m := &Module{pool: pool, pairing: pairing, send: webpush.SendNotificationWithContext}
	u, err := url.Parse(publicURL)
	if err == nil && u.User == nil && u.Host != "" && (u.Scheme == "https" || u.Scheme == "http" && (u.Hostname() == "localhost" || u.Hostname() == "127.0.0.1")) {
		m.origin = u.Scheme + "://" + u.Host
		m.wa, _ = webauthn.New(&webauthn.Config{RPID: u.Hostname(), RPDisplayName: "Aeon approvals", RPOrigins: []string{m.origin}, AuthenticatorSelection: protocol.AuthenticatorSelection{AuthenticatorAttachment: protocol.Platform, UserVerification: protocol.VerificationRequired}, Timeouts: webauthn.TimeoutsConfig{Login: webauthn.TimeoutConfig{Enforce: true, Timeout: 2 * time.Minute}, Registration: webauthn.TimeoutConfig{Enforce: true, Timeout: 2 * time.Minute}}})
	}
	if len(masterKey) == 32 {
		// Quote capabilities use the master directly. Separate the nonce domains
		// so a push nonce collision cannot compromise either encryption key.
		if key, err := hkdf.Key(sha256.New, masterKey, nil, "aeon.phone.push.encryption.v1", 32); err == nil {
			block, _ := aes.NewCipher(key)
			m.vault, _ = cipher.NewGCM(block)
		}
	}
	if vapid != nil && m.vault != nil && m.wa != nil {
		options := *vapid
		// webpush-go prefixes bare email addresses with mailto: itself.
		options.Subscriber = strings.TrimPrefix(options.Subscriber, "mailto:")
		options.HTTPClient = &http.Client{Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
		options.TTL = 60
		m.vapid = &options
	}
	return m
}

func (m *Module) Mount(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/me/phone-approvals", m.settings)
	mux.HandleFunc("PUT /api/me/phone-approvals/settings", m.preferences)
	mux.HandleFunc("POST /api/me/phone-approvals/passkeys/options", m.registerOptions)
	mux.HandleFunc("POST /api/me/phone-approvals/passkeys", m.register)
	mux.HandleFunc("DELETE /api/me/phone-approvals/passkeys/{credentialId}", m.revokePasskey)
	mux.HandleFunc("POST /api/me/phone-approvals/subscriptions", m.subscribe)
	mux.HandleFunc("DELETE /api/me/phone-approvals/subscriptions/{subscriptionId}", m.unsubscribe)
	mux.HandleFunc("GET /api/phone-approvals/{kind}/{requestId}", m.review)
	mux.HandleFunc("POST /api/phone-approvals/{kind}/{requestId}/options", m.decisionOptions)
	mux.HandleFunc("POST /api/phone-approvals/{kind}/{requestId}/decision", m.decision)
}

type problem struct {
	status  int
	message string
}

func (e *problem) Error() string        { return e.message }
func fail(status int, msg string) error { return &problem{status, msg} }
func respond(w http.ResponseWriter, v any, err error) {
	w.Header().Set("Cache-Control", "no-store")
	if err != nil {
		var p *problem
		if errors.As(err, &p) {
			httpapi.WriteError(w, p.status, p.message)
		} else {
			httpapi.WriteError(w, 500, "phone approvals unavailable")
		}
		return
	}
	httpapi.WriteJSON(w, 200, v)
}
func decode(w http.ResponseWriter, r *http.Request, v any) error {
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10))
	d.DisallowUnknownFields()
	if d.Decode(v) != nil {
		return fail(400, "invalid request")
	}
	var extra any
	if d.Decode(&extra) != io.EOF {
		return fail(400, "invalid request")
	}
	return nil
}
func (m *Module) person(w http.ResponseWriter, r *http.Request, write bool) (tenant.Principal, bool) {
	p, ok := tenant.PrincipalFrom(r.Context())
	if !ok {
		respond(w, nil, fail(401, "sign in required"))
		return p, false
	}
	if p.Kind != tenant.Person {
		respond(w, nil, fail(403, "only a person may use phone approvals"))
		return p, false
	}
	if write {
		if m.wa == nil {
			respond(w, nil, fail(503, "configure the public HTTPS origin first"))
			return p, false
		}
		if r.Header.Get("Origin") != m.origin {
			respond(w, nil, fail(403, "same-origin request required"))
			return p, false
		}
		var n int
		err := db.InTenant(r.Context(), m.pool, p.TenantID, func(tx pgx.Tx) error {
			return tx.QueryRow(r.Context(), `INSERT INTO phone_approval_limits(tenant_id,person_id) VALUES($1,$2) ON CONFLICT(tenant_id,person_id) DO UPDATE SET attempts=CASE WHEN phone_approval_limits.started_at<now()-interval '10 minutes' THEN 1 ELSE phone_approval_limits.attempts+1 END,started_at=CASE WHEN phone_approval_limits.started_at<now()-interval '10 minutes' THEN now() ELSE phone_approval_limits.started_at END RETURNING attempts`, p.TenantID, p.ID).Scan(&n)
		})
		if err != nil {
			respond(w, nil, err)
			return p, false
		}
		if n > 30 {
			if n == 31 {
				_ = db.InTenant(r.Context(), m.pool, p.TenantID, func(tx pgx.Tx) error {
					return audit(r.Context(), tx, p, "phone_approval.rate_limited", map[string]int{"window_seconds": 600})
				})
			}
			w.Header().Set("Retry-After", "600")
			respond(w, nil, fail(429, "too many attempts; try again later"))
			return p, false
		}
	}
	return p, true
}

type user struct {
	p     tenant.Principal
	creds []webauthn.Credential
}

func (u user) WebAuthnID() []byte {
	sum := sha256.Sum256([]byte("aeon.phone.person.v1\x00" + u.p.TenantID + "\x00" + u.p.ID))
	return sum[:]
}
func (u user) WebAuthnName() string                       { return u.p.ID }
func (u user) WebAuthnDisplayName() string                { return "Aeon approver" }
func (u user) WebAuthnCredentials() []webauthn.Credential { return u.creds }
func loadUser(ctx context.Context, tx pgx.Tx, p tenant.Principal) (user, error) {
	return loadUserCredentials(ctx, tx, p, true)
}

func loadUserCredentials(ctx context.Context, tx pgx.Tx, p tenant.Principal, lock bool) (user, error) {
	u := user{p: p}
	var kind string
	if err := tx.QueryRow(ctx, `SELECT kind FROM principals WHERE id=$1`, p.ID).Scan(&kind); err != nil {
		return u, err
	}
	if kind != "person" {
		return u, fail(403, "person required")
	}
	query := `SELECT credential FROM phone_passkeys WHERE person_id=$1 AND revoked_at IS NULL ORDER BY id`
	if lock {
		query += " FOR UPDATE"
	}
	rows, err := tx.Query(ctx, query, p.ID)
	if err != nil {
		return u, err
	}
	defer rows.Close()
	for rows.Next() {
		var b []byte
		var c webauthn.Credential
		if err = rows.Scan(&b); err != nil {
			return u, err
		}
		if err = json.Unmarshal(b, &c); err != nil {
			return u, err
		}
		u.creds = append(u.creds, c)
	}
	return u, rows.Err()
}
func audit(ctx context.Context, tx pgx.Tx, p tenant.Principal, typ string, data any) error {
	_, err := events.Append(ctx, tx, p, events.Change{Type: typ, After: data})
	return err
}
func (m *Module) rejected(ctx context.Context, p tenant.Principal, kind, id string) {
	_ = db.InTenant(ctx, m.pool, p.TenantID, func(tx pgx.Tx) error {
		return audit(ctx, tx, p, "phone_approval.rejected", map[string]string{"kind": kind, "request_id": id})
	})
}
func digest(v any) string {
	b, _ := json.Marshal(v)
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// The binding is part of the authenticator challenge, not merely an HTTP field.
func binding(p tenant.Principal, kind, id, hash, decision, reason string) string {
	return digest([]string{"aeon.phone.decision.v1", p.TenantID, p.ID, kind, id, hash, decision, reason})
}
func boundChallenge(id, hash, bind string) ([]byte, error) {
	nonce := make([]byte, 32)
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	return []byte("aeon.phone.v1:" + id + ":" + hash + ":" + bind + ":" + base64.RawURLEncoding.EncodeToString(nonce)), nil
}
func storeSession(ctx context.Context, tx pgx.Tx, p tenant.Principal, kind, id, bind string, session *webauthn.SessionData) (string, error) {
	b, err := json.Marshal(session)
	if err != nil {
		return "", err
	}
	var requestID *string
	if id != "" {
		requestID = &id
	}
	var challengeID string
	// Retain at most one ceremony per person; a new ceremony invalidates old proofs.
	if _, err = tx.Exec(ctx, `DELETE FROM phone_approval_challenges WHERE person_id=$1`, p.ID); err != nil {
		return "", err
	}
	err = tx.QueryRow(ctx, `INSERT INTO phone_approval_challenges(tenant_id,person_id,kind,request_id,binding,session_data) VALUES($1,$2,$3,$4,$5,$6) RETURNING id::text`, p.TenantID, p.ID, kind, requestID, bind, b).Scan(&challengeID)
	return challengeID, err
}
func consume(ctx context.Context, tx pgx.Tx, p tenant.Principal, challengeID, kind, id, bind string) (webauthn.SessionData, error) {
	var s webauthn.SessionData
	var b []byte
	if !validID(challengeID) {
		return s, fail(403, "fresh passkey verification required")
	}
	err := tx.QueryRow(ctx, `UPDATE phone_approval_challenges SET consumed_at=now() WHERE id=$1 AND person_id=$2 AND kind=$3 AND request_id IS NOT DISTINCT FROM $4::uuid AND binding=$5 AND consumed_at IS NULL AND expires_at>now() RETURNING session_data`, challengeID, p.ID, kind, nullableID(id), bind).Scan(&b)
	if errors.Is(err, pgx.ErrNoRows) {
		return s, fail(403, "verification expired, changed or already used")
	}
	if err != nil {
		return s, err
	}
	err = json.Unmarshal(b, &s)
	return s, err
}
func nullableID(id string) any {
	if id == "" {
		return nil
	}
	return id
}
func validID(id string) bool {
	if len(id) != 36 {
		return false
	}
	for i, c := range id {
		if i == 8 || i == 13 || i == 18 || i == 23 {
			if c != '-' {
				return false
			}
		} else if !strings.ContainsRune("0123456789abcdefABCDEF", c) {
			return false
		}
	}
	return true
}

type Review struct {
	Kind     string              `json:"kind"`
	ID       string              `json:"request_id"`
	Hash     string              `json:"request_hash"`
	Pending  bool                `json:"pending"`
	Approval *approvals.Approval `json:"approval,omitempty"`
	Attach   *attachwatch.View   `json:"attach,omitempty"`
	StepUp   json.RawMessage     `json:"stepup,omitempty"`
}

func approvalReview(a approvals.Approval) Review {
	bound := a
	bound.AgentName = nil
	return Review{Kind: "approval", ID: a.ID, Hash: digest(bound), Pending: a.Decision == nil && a.ExpiresAt.After(time.Now()), Approval: &a}
}
func attachReview(a attachwatch.View) Review {
	a.UserCode = ""
	a.LocalAuthNonce = ""
	return Review{Kind: "attach", ID: a.RequestID, Hash: digest(a), Pending: a.State == "pending" && a.ExpiresAt.After(time.Now()), Attach: &a}
}
func loadReview(ctx context.Context, tx pgx.Tx, p tenant.Principal, kind, id string) (Review, error) {
	if !validID(id) {
		return Review{}, fail(404, "request unavailable")
	}
	switch kind {
	case "approval":
		a, err := approvals.Review(ctx, tx, p, id)
		if err != nil {
			return Review{}, fail(403, "approval unavailable")
		}
		if err := approvals.CanDecide(ctx, tx, p, a); err != nil {
			return Review{}, fail(403, "decision permission required")
		}
		return approvalReview(a), nil
	case "attach":
		a, err := agentpairing.ReviewAttach(ctx, tx, p, id)
		if err != nil {
			return Review{}, fail(403, "attach unavailable")
		}
		return attachReview(a), nil
	}
	return Review{}, fail(404, "request unavailable")
}
func (m *Module) loadReview(ctx context.Context, tx pgx.Tx, p tenant.Principal, kind, id string) (Review, error) {
	if kind != "stepup" {
		return loadReview(ctx, tx, p, kind, id)
	}
	if !validID(id) || m.StepUpReviewTx == nil {
		return Review{}, fail(404, "request unavailable")
	}
	if p.Kind != tenant.Person || p.KeyCreatorID != "" {
		return Review{}, fail(403, "person required")
	}
	v, err := m.StepUpReviewTx(ctx, tx, p, id)
	if err != nil {
		return Review{}, fail(403, "step-up unavailable")
	}
	return v, nil
}
func (m *Module) review(w http.ResponseWriter, r *http.Request) {
	p, ok := m.person(w, r, false)
	if !ok {
		return
	}
	var out Review
	err := db.InTenant(r.Context(), m.pool, p.TenantID, func(tx pgx.Tx) error {
		var err error
		out, err = m.loadReview(r.Context(), tx, p, r.PathValue("kind"), r.PathValue("requestId"))
		return err
	})
	respond(w, out, err)
}

type Decision struct {
	Decision string `json:"decision"`
	Reason   string `json:"reason"`
	Hash     string `json:"request_hash"`
}
type Proof struct {
	ChallengeID string          `json:"challenge_id"`
	Credential  json.RawMessage `json:"credential"`
}
type DecisionProof struct {
	Decision
	Proof
}

func validateDecision(d Decision) error {
	if (d.Decision != "approved" && d.Decision != "denied") || utf8.RuneCountInString(d.Reason) > 4000 || strings.ContainsRune(d.Reason, 0) || len(d.Hash) != 64 || strings.Trim(d.Hash, "0123456789abcdef") != "" {
		return fail(400, "invalid decision")
	}
	return nil
}
func (m *Module) decisionOptions(w http.ResponseWriter, r *http.Request) {
	p, ok := m.person(w, r, true)
	if !ok {
		return
	}
	var d Decision
	if err := decode(w, r, &d); err != nil {
		respond(w, nil, err)
		return
	}
	if err := validateDecision(d); err != nil {
		respond(w, nil, err)
		return
	}
	var out any
	ctx := r.Context()
	kind, id := r.PathValue("kind"), r.PathValue("requestId")
	err := db.InTenant(ctx, m.pool, p.TenantID, func(tx pgx.Tx) error {
		v, err := loadReview(ctx, tx, p, kind, id)
		if err != nil {
			return err
		}
		if !v.Pending || v.Hash != d.Hash {
			return fail(409, "request changed or ended; review it again")
		}
		u, err := loadUser(ctx, tx, p)
		if err != nil {
			return err
		}
		if len(u.creds) == 0 {
			return fail(409, "add a passkey in Personal settings first")
		}
		bind := binding(p, kind, id, d.Hash, d.Decision, d.Reason)
		challenge, err := boundChallenge(id, d.Hash, bind)
		if err != nil {
			return err
		}
		options, session, err := m.wa.BeginLogin(u, webauthn.WithChallenge(challenge), webauthn.WithUserVerification(protocol.VerificationRequired))
		if err != nil {
			return fail(403, "passkey options unavailable")
		}
		sid, err := storeSession(ctx, tx, p, kind, id, bind, session)
		out = map[string]any{"challenge_id": sid, "publicKey": options.Response}
		return err
	})
	respond(w, out, err)
}
func (m *Module) verify(ctx context.Context, tx pgx.Tx, p tenant.Principal, kind, id string, d DecisionProof, current Review) error {
	if !current.Pending || current.Hash != d.Hash {
		return fail(409, "request changed or ended; review it again")
	}
	u, err := loadUser(ctx, tx, p)
	if err != nil {
		return err
	}
	bind := binding(p, kind, id, d.Hash, d.Decision.Decision, d.Reason)
	session, err := consume(ctx, tx, p, d.ChallengeID, kind, id, bind)
	if err != nil {
		return err
	}
	parsed, err := protocol.ParseCredentialRequestResponseBytes(d.Credential)
	if err != nil {
		return fail(403, "passkey verification rejected")
	}
	c, err := m.wa.ValidateLogin(u, session, parsed)
	if err != nil || c == nil || c.Authenticator.CloneWarning {
		return fail(403, "passkey verification rejected")
	}
	b, err := json.Marshal(c)
	if err != nil {
		return err
	}
	tag, err := tx.Exec(ctx, `UPDATE phone_passkeys SET credential=$3 WHERE person_id=$1 AND credential_id=$2 AND revoked_at IS NULL`, p.ID, base64.RawURLEncoding.EncodeToString(c.ID), b)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return fail(403, "passkey revoked")
	}
	return audit(ctx, tx, p, "phone_approval.verified", map[string]string{"kind": kind, "request_id": id, "request_hash": d.Hash, "decision": d.Decision.Decision})
}
func (m *Module) decision(w http.ResponseWriter, r *http.Request) {
	p, ok := m.person(w, r, true)
	if !ok {
		return
	}
	var d DecisionProof
	if err := decode(w, r, &d); err != nil {
		respond(w, nil, err)
		return
	}
	if err := validateDecision(d.Decision); err != nil {
		respond(w, nil, err)
		return
	}
	ctx := r.Context()
	kind, id := r.PathValue("kind"), r.PathValue("requestId")
	if !validID(id) {
		respond(w, nil, fail(404, "request unavailable"))
		return
	}
	switch kind {
	case "approval":
		a, err := approvals.DecideVerified(ctx, m.pool, p, id, d.Decision.Decision, d.Reason, func(tx pgx.Tx, a approvals.Approval) error {
			return m.verify(ctx, tx, p, kind, id, d, approvalReview(a))
		})
		if err != nil {
			m.rejected(ctx, p, kind, id)
			var own *problem
			if errors.As(err, &own) {
				respond(w, nil, err)
			} else {
				respond(w, nil, fail(404, "request unavailable"))
			}
			return
		}
		respond(w, approvalReview(a), nil)
	case "attach":
		if m.pairing == nil {
			respond(w, nil, fail(503, "attach unavailable"))
			return
		}
		a, err := m.pairing.DecideAttachVerified(ctx, p, id, "", "", d.Decision.Decision == "denied", func(tx pgx.Tx, a attachwatch.View) error { return m.verify(ctx, tx, p, kind, id, d, attachReview(a)) })
		if err != nil {
			m.rejected(ctx, p, kind, id)
			var own *problem
			if errors.As(err, &own) {
				respond(w, nil, err)
			} else {
				respond(w, nil, fail(404, "request unavailable"))
			}
			return
		}
		respond(w, attachReview(a), nil)
	default:
		respond(w, nil, fail(404, "request unavailable"))
	}
}

func subscriptionAAD(tenantID, personID string) []byte {
	return []byte("aeon.phone.push.v1\x00" + tenantID + "\x00" + personID)
}
func seal(a cipher.AEAD, b, aad []byte) ([]byte, error) {
	n := make([]byte, a.NonceSize())
	if _, err := rand.Read(n); err != nil {
		return nil, err
	}
	return a.Seal(n, n, b, aad), nil
}
func open(a cipher.AEAD, b, aad []byte) ([]byte, error) {
	if len(b) < a.NonceSize() {
		return nil, errors.New("invalid ciphertext")
	}
	return a.Open(nil, b[:a.NonceSize()], b[a.NonceSize():], aad)
}
