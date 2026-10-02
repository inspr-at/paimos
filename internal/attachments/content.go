// SPDX-License-Identifier: AGPL-3.0-only
package attachments

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"mime"
	"net/http"
	"os"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/inspr-at/paimos/internal/db"
)

func (m *Module) content(w http.ResponseWriter, r *http.Request) {
	p, ok := m.principal(w, r, "attachments.read")
	if !ok {
		return
	}
	id := r.PathValue("id")
	if !uuid(id) {
		apierr(w, bad(400, "invalid attachment id"))
		return
	}
	variant := r.URL.Query().Get("variant")
	if variant == "" {
		variant = "original"
	}
	if variant != "original" && variant != "thumb" && variant != "preview" {
		apierr(w, bad(400, "invalid variant"))
		return
	}
	var a Attachment
	err := db.InTenant(r.Context(), m.Pool, p.TenantID, func(tx pgx.Tx) error {
		var err error
		a, err = scan(tx.QueryRow(r.Context(), `SELECT `+columns+` FROM attachments WHERE tenant_id=$1 AND id=$2 AND deleted_at IS NULL`, p.TenantID, id))
		return err
	})
	if err != nil {
		apierr(w, err)
		return
	}
	if variant != "original" && !strings.HasPrefix(a.ContentType, "image/") && !(isHTML(a.ContentType) && variant == "thumb") {
		apierr(w, bad(404, "variant not found"))
		return
	}
	f, err := m.Store.Open(p.TenantID, a.SHA256, variant)
	if err != nil {
		if os.IsNotExist(err) {
			apierr(w, bad(404, "content not found"))
			return
		}
		apierr(w, err)
		return
	}
	defer f.Close()
	etag := a.SHA256
	if variant != "original" {
		h := sha256.New()
		if _, err = io.Copy(h, f); err != nil {
			apierr(w, err)
			return
		}
		etag = hex.EncodeToString(h.Sum(nil))
		if _, err = f.Seek(0, io.SeekStart); err != nil {
			apierr(w, err)
			return
		}
	}
	etag = `"` + etag + `"`
	contentType := a.ContentType
	if variant != "original" {
		contentType = "image/png"
	}
	disposition := "attachment"
	if strings.HasPrefix(contentType, "image/") {
		disposition = "inline"
	}
	w.Header().Set("ETag", etag)
	w.Header().Set("Cache-Control", "private, max-age=31536000, immutable")
	if isHTML(a.ContentType) {
		w.Header().Set("Cache-Control", "no-store, private")
	}
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Content-Disposition", mime.FormatMediaType(disposition, map[string]string{"filename": a.Name}))
	if !strings.HasPrefix(contentType, "image/") {
		w.Header().Set("Content-Security-Policy", "default-src 'none'; sandbox")
	}
	if r.Header.Get("If-None-Match") == etag {
		w.WriteHeader(304)
		return
	}
	fi, err := f.Stat()
	if err != nil {
		apierr(w, err)
		return
	}
	w.Header().Set("Content-Length", fmtInt(fi.Size()))
	if _, err = io.Copy(w, f); err != nil {
		return
	}
}
func fmtInt(n int64) string { return strconv.FormatInt(n, 10) }
