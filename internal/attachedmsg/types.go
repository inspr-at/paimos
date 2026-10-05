// SPDX-License-Identifier: AGPL-3.0-only

// Package attachedmsg is the shared owner authority and volatile payload policy
// for the existing inbox. It is not a transport, queue, or harness controller.
package attachedmsg

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/url"
	"time"

	"github.com/inspr-at/paimos/internal/attachwatch"
	"github.com/inspr-at/paimos/internal/hooknote"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

const Protocol = "attached_messages_v1"
const Placeholder = "Attached-session note; text not retained"
const Volatile = "attached_volatile"
const Notification = "attached_notification"
const MaxBody = hooknote.MaxBodyBytes
const MaxFrame = hooknote.MaxContextRunes
const MaxWait = 5 * time.Minute

// Binding is also the nonce-binding tuple prefix for S2-3. No client-supplied
// owner, tenant or session-principal attribution is ever used to construct it.
type Binding struct {
	TenantID          string `json:"tenant_id"`
	ProjectID         string `json:"project_id"`
	AttachRequestID   string `json:"attach_request_id"`
	SessionID         string `json:"session_id"`
	ComputerID        string `json:"computer_id"`
	OwnerID           string `json:"owner_id"`
	Generation        string `json:"message_generation"`
	DaemonEpoch       string `json:"daemon_epoch"`
	HookReleaseDigest string `json:"hook_release_digest"`
	HookConfigDigest  string `json:"hook_config_digest"`
	HarnessVersion    string `json:"qualified_harness_version"`
	Scope             string `json:"scope"`
	ConsentPolicy     string `json:"consent_policy"`
	SnapshotDigest    string `json:"snapshot_digest"`
	ServiceEpoch      string `json:"service_epoch"`
}

func (b Binding) Digest() string {
	raw, _ := json.Marshal(b)
	sum := sha256.Sum256(append([]byte("aeon.owner-messages.consent.v1\x00"), raw...))
	return hex.EncodeToString(sum[:])
}

type Grant struct {
	Snapshot       attachwatch.Snapshot `json:"snapshot"`
	Notice         string               `json:"notice"`
	ID             string               `json:"id"`
	Binding        Binding              `json:"binding"`
	State          string               `json:"state"`
	Digest         string               `json:"consent_digest"`
	ExpiresAt      time.Time            `json:"expires_at"`
	LocalAuthNonce string               `json:"message_local_auth_nonce,omitempty"`
	ObservedAt     *time.Time           `json:"-"`
}
type Capability struct {
	Ready   bool   `json:"ready"`
	Blocker string `json:"blocker"`
	Grant   *Grant `json:"grant,omitempty"`
}

// Offer is the S2-3/S2-4 handoff contract. The future broker must commit its
// one attempt before using Take, and bind its nonce digest to this entire tuple.
type Offer struct {
	Epoch      string    `json:"epoch"`
	Owner      string    `json:"owner"`
	CreatedAt  time.Time `json:"created_at"`
	Binding    Binding   `json:"binding"`
	GrantID    string    `json:"grant_id"`
	DeliveryID string    `json:"delivery_id"`
	MessageID  string    `json:"message_id"`
	Deadline   time.Time `json:"deadline"`
	Nonce      string    `json:"nonce"`
	Body       string    `json:"body"`
}

type Error struct {
	Status int
	Code   string
}

func (e *Error) Error() string           { return e.Code }
func Fail(status int, code string) error { return &Error{status, code} }
func WriteError(w http.ResponseWriter, err error) bool {
	e, ok := err.(*Error)
	if !ok {
		return false
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/json")
	if e.Status == 429 {
		w.Header().Set("Retry-After", "10")
	}
	w.WriteHeader(e.Status)
	_ = json.NewEncoder(w).Encode(map[string]string{"code": e.Code, "message": e.Code})
	return true
}

// Auth middleware alone sets BrowserSession after validating the cookie. A
// principal's kind/name or possession of a cookie-shaped header is insufficient.
func Interactive(r *http.Request, origin string, p tenant.Principal) bool {
	return p.Kind == tenant.Person && p.BrowserSession && origin != "" && r.Header.Get("Authorization") == "" && r.Header.Get("Origin") == origin && r.Header.Get("Sec-Fetch-Site") != "cross-site"
}

type interactiveKey struct{}

func BrowserContext(r *http.Request, origin string, p tenant.Principal) context.Context {
	return context.WithValue(r.Context(), interactiveKey{}, Interactive(r, origin, p))
}
func InteractiveContext(ctx context.Context) bool { return interactive(ctx) }
func interactive(ctx context.Context) bool        { v, _ := ctx.Value(interactiveKey{}).(bool); return v }
func Origin(raw string) string {
	u, e := url.Parse(raw)
	if e != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") {
		return ""
	}
	return u.Scheme + "://" + u.Host
}
func UUID() string {
	var b [16]byte
	if _, e := rand.Read(b[:]); e != nil {
		panic(e)
	}
	b[6] = (b[6] & 15) | 64
	b[8] = (b[8] & 63) | 128
	h := hex.EncodeToString(b[:])
	return h[:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:]
}

// Lock follows current main's tenant → pairing → tree → resource rows order.
// NO KEY UPDATE permits FK readers; the event counter is always acquired last.
func Lock(ctx context.Context, tx pgx.Tx) error {
	if _, err := tx.Exec(ctx, `SELECT id FROM tenants WHERE id=current_setting('aeon.tenant_id')::uuid FOR NO KEY UPDATE`); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('aeon-pairing:'||current_setting('aeon.tenant_id'),0))`); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended(current_setting('aeon.tenant_id'),0))`)
	return err
}
