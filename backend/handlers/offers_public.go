// SPDX-License-Identifier: AGPL-3.0-only
package handlers

import (
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"mime"
	"net"
	"net/http"
	"net/netip"
	"os"
	"strings"
	"sync"
	"time"
	_ "time/tzdata"

	"github.com/go-chi/chi/v5"
	"github.com/inspr-at/paimos/backend/auth"
	"github.com/inspr-at/paimos/backend/db"
	"github.com/inspr-at/paimos/backend/publicbase"
)

var offerLocation = func() *time.Location {
	loc, err := time.LoadLocation("Europe/Vienna")
	if err != nil {
		panic("Europe/Vienna timezone is required for offers")
	}
	return loc
}()

func offerToday() string { return time.Now().In(offerLocation).Format("2006-01-02") }
func offerDateValid(day string) bool {
	parsed, err := time.Parse("2006-01-02", day)
	return err == nil && parsed.Format("2006-01-02") == day
}

// Forwarded addresses are evidence only when supplied by an explicitly trusted
// immediate proxy. Direct/unconfigured deployments ignore all forwarding headers.
func offerClientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	peer, err := netip.ParseAddr(host)
	if err != nil {
		return "unknown"
	}
	peer = peer.Unmap()
	trusted := false
	for _, raw := range strings.Split(os.Getenv("OFFER_TRUSTED_PROXY_CIDRS"), ",") {
		prefix, e := netip.ParsePrefix(strings.TrimSpace(raw))
		if e == nil && prefix.Contains(peer) {
			trusted = true
			break
		}
	}
	if trusted {
		// Walk from the nearest peer toward the client; ignore attacker-supplied
		// entries to the left of the first untrusted hop.
		chain := strings.Split(r.Header.Get("X-Forwarded-For"), ",")
		for i := len(chain) - 1; i >= 0; i-- {
			ip, e := netip.ParseAddr(strings.TrimSpace(chain[i]))
			if e != nil {
				return peer.String()
			}
			ip = ip.Unmap()
			hopTrusted := false
			for _, raw := range strings.Split(os.Getenv("OFFER_TRUSTED_PROXY_CIDRS"), ",") {
				prefix, e := netip.ParsePrefix(strings.TrimSpace(raw))
				if e == nil && prefix.Contains(ip) {
					hopTrusted = true
					break
				}
			}
			if !hopTrusted || i == 0 {
				return ip.String()
			}
		}
	}
	return peer.String()
}
func newOfferToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}
func validOfferToken(token string) bool {
	b, e := base64.RawURLEncoding.DecodeString(token)
	return e == nil && len(b) == 32 && base64.RawURLEncoding.EncodeToString(b) == token
}

// Both SPA and API capability paths must stay out of ordinary URL logs,
// referrers and caches, including malformed tokens and wrong methods.
func isPublicOfferRequest(r *http.Request) bool {
	path := r.URL.Path
	if stripped, ok := publicbase.Current().Strip(path); ok {
		path = stripped
	}
	return path == "/offers" || strings.HasPrefix(path, "/offers/") || path == "/api/public/offers" || strings.HasPrefix(path, "/api/public/offers/")
}
func OfferPrivacyMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if isPublicOfferRequest(r) {
			w.Header().Set("Cache-Control", "no-store")
			w.Header().Set("Referrer-Policy", "no-referrer")
			w.Header().Set("X-Robots-Tag", "noindex, nofollow, noarchive")
		}
		next.ServeHTTP(w, r)
	})
}

type offerLimitEntry struct {
	count int
	until time.Time
}
type offerRateLimiter struct {
	sync.Mutex
	entries map[string]offerLimitEntry
}

func (l *offerRateLimiter) allow(key string, limit int, now time.Time) bool {
	l.Lock()
	defer l.Unlock()
	if l.entries == nil {
		l.entries = map[string]offerLimitEntry{}
	}
	if len(l.entries) >= 10000 {
		for k, v := range l.entries {
			if !now.Before(v.until) {
				delete(l.entries, k)
			}
		}
	}
	e, ok := l.entries[key]
	if !ok && len(l.entries) >= 10000 {
		return false
	}
	if !now.Before(e.until) {
		e = offerLimitEntry{until: now.Add(time.Minute)}
	}
	if e.count >= limit {
		return false
	}
	e.count++
	l.entries[key] = e
	return true
}
func PublicOfferMiddleware() func(http.Handler) http.Handler {
	limiter := &offerRateLimiter{}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Public offers are the only CRM API where the door also closes access.
			var enabled string
			err := db.DB.QueryRowContext(r.Context(), "SELECT value FROM app_settings WHERE key='crm_enabled'").Scan(&enabled)
			if err != nil && !errors.Is(err, sql.ErrNoRows) {
				jsonError(w, "Angebot derzeit nicht verfügbar", 503)
				return
			}
			if enabled == "0" {
				jsonError(w, "Angebot nicht verfügbar", 404)
				return
			}
			ip := offerClientIP(r)
			token := chi.URLParam(r, "token")
			hash := sha256.Sum256([]byte(token))
			now := time.Now()
			limit := 60
			kind := "read:"
			if r.Method == http.MethodPost {
				limit = 10
				kind = "accept:"
			}
			if !limiter.allow("all", 3000, now) || !limiter.allow(kind+"ip:"+ip, 120, now) || !limiter.allow(kind+hex.EncodeToString(hash[:]), limit, now) {
				w.Header().Set("Retry-After", "60")
				jsonError(w, "Zu viele Anfragen. Bitte in einer Minute erneut versuchen.", 429)
				return
			}
			if !validOfferToken(token) {
				jsonError(w, "Angebot nicht verfügbar", 404)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
func RegisterPublicOfferRoutes(r chi.Router) {
	r.Route("/public/offers/{token}", func(r chi.Router) {
		r.Use(PublicOfferMiddleware())
		r.Get("/", GetPublicOffer)
		r.Post("/accept", AcceptPublicOffer)
		r.Get("/pdf", GetPublicOfferPDF)
	})
}

type publicOffer struct {
	DocumentSHA256  string             `json:"document_sha256,omitempty"`
	Confirmation    *OfferConfirmation `json:"confirmation,omitempty"`
	OfferNo         string             `json:"offer_no"`
	Status          string             `json:"status"`
	Revision        int64              `json:"revision"`
	Document        OfferDocument      `json:"document"`
	AcceptedAt      *string            `json:"accepted_at,omitempty"`
	AcceptedName    string             `json:"accepted_name,omitempty"`
	AcceptedCompany string             `json:"accepted_company,omitempty"`
	AcceptedNote    string             `json:"accepted_note,omitempty"`
}

func publicOfferView(o Offer) publicOffer {
	return publicOffer{o.DocumentSHA256, o.Confirmation, o.OfferNo, o.Status, o.Revision, o.Document, o.AcceptedAt, o.AcceptedName, o.AcceptedCompany, o.AcceptedNote}
}
func loadPublicOffer(r *http.Request) (Offer, error) {
	return scanOffer(db.DB.QueryRowContext(r.Context(), `SELECT `+offerColumns+` FROM offers WHERE public_token=? AND status IN ('sent','accepted','expired')`, chi.URLParam(r, "token")))
}
func GetPublicOffer(w http.ResponseWriter, r *http.Request) {
	o, err := loadPublicOffer(r)
	if errors.Is(err, sql.ErrNoRows) {
		jsonError(w, "Angebot nicht verfügbar", 404)
		return
	}
	if err != nil {
		jsonError(w, "Angebot derzeit nicht verfügbar", 503)
		return
	}
	loadOfferConfirmation(r.Context(), &o)
	jsonOK(w, publicOfferView(o))
}
func AcceptPublicOffer(w http.ResponseWriter, r *http.Request) {
	// JSON-only and a custom header prevent HTML forms/cross-origin simple requests.
	// No session credentials are used: the unguessable link is the capability.
	mediaType, _, mediaErr := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if mediaErr != nil || mediaType != "application/json" || r.Header.Get("X-Offer-Acceptance") != "1" || r.Header.Get("Sec-Fetch-Site") == "cross-site" {
		jsonError(w, "Ungültige Annahmeanfrage", 403)
		return
	}
	var body struct {
		Name      string `json:"name"`
		Company   string `json:"company"`
		Note      string `json:"note"`
		Confirmed bool   `json:"confirmed"`
		Revision  int64  `json:"revision"`
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10))
	dec.DisallowUnknownFields()
	if dec.Decode(&body) != nil {
		jsonError(w, "Ungültige Annahme", 400)
		return
	}
	body.Name = strings.TrimSpace(body.Name)
	body.Company = strings.TrimSpace(body.Company)
	body.Note = strings.TrimSpace(body.Note)
	if !body.Confirmed || body.Name == "" || len(body.Name) > 200 || body.Company == "" || len(body.Company) > 300 || len(body.Note) > 2000 || body.Revision < 1 {
		jsonError(w, "Name, Firma und ausdrückliche Bestätigung sind erforderlich", 400)
		return
	}
	tx, err := db.DB.BeginTx(r.Context(), nil)
	if err != nil {
		jsonError(w, "Annahme konnte nicht gespeichert werden", 503)
		return
	}
	defer tx.Rollback()
	// A conditional write is the first DB statement, so concurrent attempts
	// serialize before reading. No duplicate acceptance can replace the signer.
	now := time.Now().UTC().Format(time.RFC3339)
	o, err := scanOffer(tx.QueryRowContext(r.Context(), `UPDATE offers SET status='accepted',accepted_at=?,accepted_name=?,accepted_company=?,accepted_note=?,revision=revision+1,updated_at=? WHERE public_token=? AND status='sent' AND revision=? AND date(json_extract(document,'$.valid_until'))=json_extract(document,'$.valid_until') AND json_extract(document,'$.valid_until')>=? RETURNING `+offerColumns, now, body.Name, body.Company, body.Note, now, chi.URLParam(r, "token"), body.Revision, offerToday()))
	if errors.Is(err, sql.ErrNoRows) {
		jsonError(w, "Dieses Angebot ist nicht mehr zur Annahme verfügbar. Bitte neu laden.", 409)
		return
	}
	if err != nil {
		jsonError(w, "Annahme konnte nicht gespeichert werden", 503)
		return
	}
	var raw string
	if err = tx.QueryRowContext(r.Context(), "SELECT document FROM offers WHERE id=?", o.ID).Scan(&raw); err != nil {
		jsonError(w, "Annahme konnte nicht gespeichert werden", 503)
		return
	}
	hash := sha256.Sum256([]byte(raw))
	ua := r.UserAgent()
	if len(ua) > 1000 {
		ua = ua[:1000]
	}
	_, err = tx.ExecContext(r.Context(), `INSERT INTO offer_acceptance_audit(offer_id,accepted_at,accepted_name,accepted_company,accepted_note,ip,user_agent,document_sha256,offer_revision) VALUES(?,?,?,?,?,?,?,?,?)`, o.ID, now, body.Name, body.Company, body.Note, offerClientIP(r), ua, hex.EncodeToString(hash[:]), body.Revision)
	if err != nil {
		jsonError(w, "Annahme konnte nicht dokumentiert werden", 503)
		return
	}
	if validOfferEmail(o.Document.Customer.Email) && validOfferEmail(o.Document.Sender.Email) {
		if _, err = tx.ExecContext(r.Context(), `INSERT INTO offer_confirmations(offer_id,next_attempt_at,updated_at,public_url,message_id) VALUES(?,?,?,?,?)`, o.ID, now, now, offerCustomerURL(o.PublicToken), "offer-"+hex.EncodeToString(hash[:])+"-"+fmt.Sprint(o.ID)); err != nil {
			jsonError(w, "Annahme konnte nicht dokumentiert werden", 503)
			return
		}
	}
	if err = tx.Commit(); err != nil {
		jsonError(w, "Annahme konnte nicht gespeichert werden", 503)
		return
	}
	loadOfferConfirmation(r.Context(), &o)
	jsonOK(w, publicOfferView(o))
}

// Existing finalized offers predate public links. Explicit admin action enables
// their link without changing the frozen document or publishing any draft.
func CreateOfferLink(w http.ResponseWriter, r *http.Request) {
	token, err := newOfferToken()
	if err != nil {
		jsonError(w, "Kundenlink konnte nicht erstellt werden", 503)
		return
	}
	_, err = db.DB.ExecContext(r.Context(), `UPDATE offers SET public_token=? WHERE id=? AND status='sent' AND public_token IS NULL`, token, chi.URLParam(r, "id"))
	if err != nil {
		jsonError(w, "Kundenlink konnte nicht erstellt werden", 503)
		return
	}
	o, err := scanOffer(db.DB.QueryRowContext(r.Context(), `SELECT `+offerColumns+` FROM offers WHERE id=? AND public_token IS NOT NULL`, chi.URLParam(r, "id")))
	if err != nil {
		jsonError(w, "Kein finalisiertes Angebot mit Kundenlink vorhanden", 404)
		return
	}
	jsonOK(w, o)
}

// Durable creator notification: the CRM surfaces accepted offers directly from
// the receipt, so process restarts cannot lose an in-memory notification.
func ListOfferAcceptances(w http.ResponseWriter, r *http.Request) {
	user := auth.GetUser(r)
	if user == nil {
		jsonError(w, "Anmeldung erforderlich", 401)
		return
	}
	rows, err := db.DB.QueryContext(r.Context(), `SELECT `+offerColumns+` FROM offers WHERE created_by=? AND status='accepted' ORDER BY accepted_at DESC LIMIT 20`, user.ID)
	if err != nil {
		jsonError(w, "Annahmen konnten nicht geladen werden", 503)
		return
	}
	defer rows.Close()
	out := []Offer{}
	for rows.Next() {
		o, e := scanOffer(rows)
		if e != nil {
			jsonError(w, "Annahmen konnten nicht geladen werden", 503)
			return
		}
		o.PublicToken = ""
		out = append(out, o)
	}
	if rows.Err() != nil {
		jsonError(w, "Annahmen konnten nicht geladen werden", 503)
		return
	}
	jsonOK(w, out)
}
