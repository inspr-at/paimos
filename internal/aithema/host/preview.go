// SPDX-License-Identifier: AGPL-3.0-only

package host

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/inspr-at/paimos/internal/aithema/tokens"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/linkvault"
	"github.com/jackc/pgx/v5"
)

type previewClaims struct {
	Issuer    string      `json:"iss"`
	Audience  string      `json:"aud"`
	Tenant    string      `json:"tid"`
	Session   string      `json:"sid"`
	Design    string      `json:"design_rev"`
	IssuedAt  json.Number `json:"iat"`
	ExpiresAt json.Number `json:"exp"`
}

func previewParts(token string) ([]string, []byte, []byte, []byte, error) {
	parts := strings.Split(token, ".")
	if len(token) > 16384 || len(parts) != 3 {
		return nil, nil, nil, nil, fail(404, "not_found")
	}
	decoded := make([][]byte, 3)
	for i, p := range parts {
		if strings.ContainsAny(p, "\r\n=") {
			return nil, nil, nil, nil, fail(404, "not_found")
		}
		b, err := base64.RawURLEncoding.Strict().DecodeString(p)
		if err != nil {
			return nil, nil, nil, nil, fail(404, "not_found")
		}
		decoded[i] = b
	}
	return parts, decoded[0], decoded[1], decoded[2], nil
}
func decodeCap(raw []byte) (previewClaims, error) {
	var c previewClaims
	if _, err := tokens.CanonicalJSON(raw); err != nil {
		return c, fail(404, "not_found")
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	d.UseNumber()
	if d.Decode(&c) != nil || !uuidRE.MatchString(c.Tenant) || !uuidRE.MatchString(c.Session) || !digestRE.MatchString(c.Design) {
		return c, fail(404, "not_found")
	}
	// encoding/json accepts quoted numeric strings into json.Number fields.
	// The capability contract requires actual JSON numbers, before conversion.
	var fields map[string]any
	d = json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	if d.Decode(&fields) != nil || len(fields) != 7 {
		return c, fail(404, "not_found")
	}
	if _, ok := fields["iat"].(json.Number); !ok {
		return c, fail(404, "not_found")
	}
	if _, ok := fields["exp"].(json.Number); !ok {
		return c, fail(404, "not_found")
	}
	return c, nil
}
func verifyCap(token string, s Settings, issuer, design string, now time.Time) (previewClaims, error) {
	parts, header, payload, signature, err := previewParts(token)
	if err != nil {
		return previewClaims{}, err
	}
	if _, err := tokens.CanonicalJSON(header); err != nil {
		return previewClaims{}, fail(404, "not_found")
	}
	var h struct {
		Alg  string `json:"alg"`
		Kid  string `json:"kid"`
		Type string `json:"typ"`
	}
	d := json.NewDecoder(bytes.NewReader(header))
	d.DisallowUnknownFields()
	if d.Decode(&h) != nil || h.Alg != "EdDSA" || h.Kid == "" || h.Type != "JWT" {
		return previewClaims{}, fail(404, "not_found")
	}
	var pub []byte
	for _, k := range s.PreviewKeys.Keys {
		if k.ID == h.Kid {
			pub, _ = base64.RawURLEncoding.Strict().DecodeString(k.X)
		}
	}
	if len(pub) != ed25519.PublicKeySize || !ed25519.Verify(pub, []byte(parts[0]+"."+parts[1]), signature) {
		return previewClaims{}, fail(404, "not_found")
	}
	c, err := decodeCap(payload)
	if err != nil {
		return c, err
	}
	iat, e1 := c.IssuedAt.Int64()
	exp, e2 := c.ExpiresAt.Int64()
	if e1 != nil || e2 != nil || iat < 1 || exp > tokens.MaxSafeInteger || exp <= iat || exp-iat > 300 || iat > now.Unix() || exp <= now.Unix() || c.Issuer != s.ServiceURL || c.Audience != issuer || c.Design != design {
		return c, fail(404, "not_found")
	}
	return c, nil
}
func previewHeaders(w http.ResponseWriter, s Settings, css bool) {
	w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'self'; img-src data:; font-src data:; script-src 'sha256-"+s.PickerSHA256+"'; form-action 'none'; base-uri 'none'; frame-ancestors 'self'")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("X-Frame-Options", "SAMEORIGIN")
	w.Header().Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if css {
		w.Header().Set("Content-Type", "text/css; charset=utf-8")
	}
}
func (m *Module) preview(w http.ResponseWriter, r *http.Request) {
	// Every rejection is identical, including disabled/foreign sessions and
	// upstream failures. Neither a cookie nor an agent key is consulted.
	notFound := func() { w.Header().Set("Cache-Control", "private, no-store"); http.NotFound(w, r) }
	if m.Pool == nil || m.Journal == nil || m.Keys == nil {
		notFound()
		return
	}
	q, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil || len(q["cap"]) != 1 || q.Get("cap") == "" || !digestRE.MatchString(r.PathValue("design_rev")) {
		notFound()
		return
	}
	for k, v := range q {
		if (k != "cap" && k != "resource") || len(v) != 1 {
			notFound()
			return
		}
	}
	resource := q.Get("resource")
	if resource != "" && resource != "base.css" && resource != "tokens.css" {
		notFound()
		return
	}
	_, _, payload, _, err := previewParts(q.Get("cap"))
	if err != nil {
		notFound()
		return
	}
	untrusted, err := decodeCap(payload)
	if err != nil {
		notFound()
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	var s Settings
	var c previewClaims
	var credential secret
	err = db.InTenant(ctx, m.Pool, untrusted.Tenant, func(tx pgx.Tx) error {
		// Unsigned tid is only a key-selection hint. Do not even read the
		// credential column until the pinned signature and all claims verify.
		var raw []byte
		if err := tx.QueryRow(ctx, `SELECT settings FROM aithema_host_settings WHERE tenant_id=$1`, untrusted.Tenant).Scan(&raw); err != nil {
			return err
		}
		if json.Unmarshal(raw, &s) != nil || s.validate() != nil {
			return fail(404, "not_found")
		}
		var err error
		c, err = verifyCap(q.Get("cap"), s, m.Issuer, r.PathValue("design_rev"), m.clock())
		if err != nil || c.Tenant != untrusted.Tenant {
			return fail(404, "not_found")
		}
		var encrypted []byte
		if err := tx.QueryRow(ctx, `SELECT service_credential FROM aithema_host_settings WHERE tenant_id=$1`, c.Tenant).Scan(&encrypted); err != nil || len(encrypted) == 0 {
			return fail(404, "not_found")
		}
		plain, err := linkvault.Decrypt(m.vaultKey, c.Tenant, "aithema-service-jwt", encrypted)
		if err != nil || plain == "" {
			return fail(404, "not_found")
		}
		credential = secret(plain)
		return nil
	})
	if err != nil {
		notFound()
		return
	}
	err = db.InTenant(ctx, m.Pool, c.Tenant, func(tx pgx.Tx) error {
		state, err := m.Journal.LockAuthority(ctx, tx, c.Tenant, c.Session)
		if err != nil {
			return err
		}
		if state.Tombstone || state.Suspended {
			return fail(409, "revoked")
		}
		_, _, err = m.live(ctx, tx, c.Tenant, state, "intake.read")
		return err
	})
	if err != nil {
		notFound()
		return
	}
	u := s.ServiceURL + "/aithema/preview/" + c.Design + "?" + q.Encode()
	request, _ := http.NewRequestWithContext(ctx, "GET", u, nil)
	request.Header.Set("Authorization", "Bearer "+string(credential))
	resp, err := serviceClient(s, m.servicePolicy).Do(request)
	if err != nil {
		notFound()
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		notFound()
		return
	}
	contentType := "text/html"
	if resource != "" {
		contentType = "text/css"
	}
	if !strings.HasPrefix(resp.Header.Get("Content-Type"), contentType) {
		notFound()
		return
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, (1<<20)+1))
	if err != nil || len(body) > 1<<20 || bytes.Contains(body, []byte(string(credential))) {
		notFound()
		return
	}
	// Recheck expiry after network delay; fetched responses cannot extend cap life.
	if _, err := verifyCap(q.Get("cap"), s, m.Issuer, c.Design, m.clock()); err != nil {
		notFound()
		return
	}
	previewHeaders(w, s, resource != "")
	w.WriteHeader(200)
	if r.Method != http.MethodHead {
		_, _ = w.Write(body)
	}
}
