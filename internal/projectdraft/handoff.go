// SPDX-License-Identifier: AGPL-3.0-only
package projectdraft

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"mime"
	"net/http"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/httpapi"
	"github.com/inspr-at/paimos/internal/portal"
)

// A urlencoded field can triple in size on the wire; the decoded payload
// field is held to MaxPayloadBytes after parsing.
const (
	maxWire      = 3*MaxPayloadBytes + 4096
	readDeadline = 20 * time.Second
	storeBudget  = 10 * time.Second
)

var (
	errTooLarge = errors.New("handoff payload too large")
	errCapacity = errors.New("too many live drafts")
)

// handoff is the public form POST from START. Order: feature switch, content
// type, client rate limit, then the bounded body, the strict schema and the
// live-draft caps. Success is a 303 to the assistant with the capability in
// the fragment, which browsers never send to a server.
func (m *Module) handoff(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	lang := pageLanguage(r.Header.Get("Accept-Language"))
	if m.tenant == "" || m.pool == nil {
		writePage(w, http.StatusServiceUnavailable, lang, pageUnavailable)
		return
	}
	contentType, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if contentType != "application/x-www-form-urlencoded" && contentType != "multipart/form-data" {
		writePage(w, http.StatusUnsupportedMediaType, lang, pageUnreadable)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), storeBudget)
	defer cancel()
	key, err := m.bucketKey(clientIP(r, m.trusted))
	if err != nil {
		httpapi.LogFailure(r, "handoff_limit_key", err)
		writePage(w, http.StatusServiceUnavailable, lang, pageUnavailable)
		return
	}
	allowed, retry, err := portal.AllowPublic(ctx, m.pool, key, m.perMinute)
	if err != nil {
		httpapi.LogFailure(r, "handoff_limit", err)
		writePage(w, http.StatusServiceUnavailable, lang, pageUnavailable)
		return
	}
	if !allowed {
		w.Header().Set("Retry-After", strconv.Itoa(retry))
		writePage(w, http.StatusTooManyRequests, lang, pageBusy)
		return
	}
	// The server has no body read timeout; this request gets its own.
	_ = http.NewResponseController(w).SetReadDeadline(time.Now().Add(readDeadline))
	r.Body = http.MaxBytesReader(w, r.Body, maxWire)
	raw, err := formPayload(r, contentType)
	if errors.Is(err, errTooLarge) {
		writePage(w, http.StatusRequestEntityTooLarge, lang, pageTooLarge)
		return
	}
	if err != nil {
		writePage(w, http.StatusBadRequest, lang, pageUnreadable)
		return
	}
	payload, err := ParsePayload(raw)
	if errors.Is(err, errVersion) {
		writePage(w, http.StatusUnprocessableEntity, lang, pageVersion)
		return
	}
	if err != nil {
		writePage(w, http.StatusBadRequest, lang, pageUnreadable)
		return
	}
	lang = payload.Language
	stored, err := encode(payload)
	if err != nil || len(stored) > MaxPayloadBytes {
		writePage(w, http.StatusRequestEntityTooLarge, lang, pageTooLarge)
		return
	}
	capability, digest, err := newCapability()
	if err == nil {
		err = m.storeDraft(ctx, digest, stored, payload.Language)
	}
	if errors.Is(err, errCapacity) {
		w.Header().Set("Retry-After", "60")
		writePage(w, http.StatusTooManyRequests, lang, pageBusy)
		return
	}
	if err != nil {
		httpapi.LogFailure(r, "handoff_store", err)
		writePage(w, http.StatusServiceUnavailable, lang, pageUnavailable)
		return
	}
	w.Header().Set("Location", "/new-project#draft="+capability)
	w.WriteHeader(http.StatusSeeOther)
}

// encode keeps <, > and & as sent, so a payload at the wire bound still fits.
func encode(v any) ([]byte, error) {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimRight(b.Bytes(), "\n"), nil
}

// formPayload reads exactly one payload field from the bounded body.
func formPayload(r *http.Request, contentType string) ([]byte, error) {
	var values []string
	if contentType == "multipart/form-data" {
		if err := r.ParseMultipartForm(maxWire); err != nil {
			return nil, bodyError(err)
		}
		defer func() { _ = r.MultipartForm.RemoveAll() }()
		values = r.MultipartForm.Value["payload"]
	} else {
		if err := r.ParseForm(); err != nil {
			return nil, bodyError(err)
		}
		values = r.PostForm["payload"]
	}
	if len(values) != 1 {
		return nil, errSchema
	}
	if len(values[0]) > MaxPayloadBytes {
		return nil, errTooLarge
	}
	return []byte(values[0]), nil
}

func bodyError(err error) error {
	var tooLarge *http.MaxBytesError
	if errors.As(err, &tooLarge) {
		return errTooLarge
	}
	return errSchema
}

// storeDraft inserts one unclaimed draft. One writer at a time per tenant
// keeps the live count and byte caps exact across replicas; expired rows no
// longer count even before the sweep deletes them.
func (m *Module) storeDraft(ctx context.Context, digest, payload []byte, language string) error {
	return db.InTenant(db.NoProjects(ctx, "START handoff"), m.pool, m.tenant, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('aeon-import-drafts:'||$1::text, 0))`, m.tenant); err != nil {
			return err
		}
		var live int
		var bytes int64
		if err := tx.QueryRow(ctx, `SELECT count(*), coalesce(sum(payload_bytes),0) FROM import_project_drafts WHERE consumed_at IS NULL AND expires_at > clock_timestamp()`).Scan(&live, &bytes); err != nil {
			return err
		}
		if live >= m.liveDrafts || bytes+int64(len(payload)) > m.liveBytes {
			return errCapacity
		}
		_, err := tx.Exec(ctx, `INSERT INTO import_project_drafts(tenant_id,capability_digest,payload,payload_bytes,language,expires_at)
		  VALUES($1::uuid,$2,$3::jsonb,$4,$5,clock_timestamp()+make_interval(secs => $6))`, m.tenant, digest, string(payload), len(payload), language, int(draftTTL.Seconds()))
		return err
	})
}
