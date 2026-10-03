// SPDX-License-Identifier: AGPL-3.0-only

package portal

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"unicode"

	"github.com/jackc/pgx/v5"

	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/events"
	"github.com/inspr-at/paimos/internal/tenant"
)

const portalEditLimit = 12 << 10

type statusError struct {
	status int
	msg    string
}

func (e statusError) Error() string { return e.msg }

type portalEdit struct {
	ID      string            `json:"id"`
	Title   string            `json:"title"`
	Summary string            `json:"summary"`
	State   string            `json:"state"`
	Fields  map[string]string `json:"fields"`
}

type portalRow struct {
	ID     string
	Title  string
	Body   string
	State  string
	Fields json.RawMessage
}

type productWrite struct {
	Title     *string `json:"title"`
	Summary   *string `json:"summary"`
	Published *bool   `json:"published"`
}

type featureWrite struct {
	Title   *string `json:"title"`
	Summary *string `json:"summary"`
	Status  *string `json:"status"`
	Legal   *string `json:"legal_basis"`
	Reason  *string `json:"decline_reason"`
	Live    *string `json:"live_since"`
}

func (m *Module) publishWish(w http.ResponseWriter, r *http.Request) {
	m.moderateWish(w, r, "published", "portal.wish_published")
}

func (m *Module) rejectWish(w http.ResponseWriter, r *http.Request) {
	m.moderateWish(w, r, "rejected", "portal.wish_rejected")
}

func (m *Module) hideWish(w http.ResponseWriter, r *http.Request) {
	m.moderateWish(w, r, "hidden", "portal.wish_hidden")
}

func (m *Module) moderateWish(w http.ResponseWriter, r *http.Request, state, eventType string) {
	m.moderate(w, r, func(ctx context.Context, tx pgx.Tx, p tenant.Principal) (portalEdit, error) {
		if err := emptyWishBody(r); err != nil {
			return portalEdit{}, err
		}
		id := r.PathValue("wishId")
		if !uuidPattern.MatchString(id) {
			return portalEdit{}, statusError{status: http.StatusBadRequest, msg: "invalid wish"}
		}
		return applyWish(ctx, tx, p, id, state, eventType)
	})
}

func (m *Module) editProduct(w http.ResponseWriter, r *http.Request) {
	m.moderate(w, r, func(ctx context.Context, tx pgx.Tx, p tenant.Principal) (portalEdit, error) {
		var in productWrite
		if err := decodeEdit(r, &in); err != nil || (in.Title == nil && in.Summary == nil && in.Published == nil) {
			return portalEdit{}, statusError{status: http.StatusBadRequest, msg: "invalid product"}
		}
		id := r.PathValue("productId")
		if !uuidPattern.MatchString(id) {
			return portalEdit{}, statusError{status: http.StatusBadRequest, msg: "invalid product"}
		}
		return applyProduct(ctx, tx, p, id, in)
	})
}

func (m *Module) editFeature(w http.ResponseWriter, r *http.Request) {
	m.moderate(w, r, func(ctx context.Context, tx pgx.Tx, p tenant.Principal) (portalEdit, error) {
		var in featureWrite
		if err := decodeEdit(r, &in); err != nil || (in.Title == nil && in.Summary == nil && in.Status == nil && in.Legal == nil && in.Reason == nil && in.Live == nil) {
			return portalEdit{}, statusError{status: http.StatusBadRequest, msg: "invalid feature"}
		}
		id := r.PathValue("featureId")
		if !uuidPattern.MatchString(id) {
			return portalEdit{}, statusError{status: http.StatusBadRequest, msg: "invalid feature"}
		}
		return applyFeature(ctx, tx, p, id, in)
	})
}

func (m *Module) moderate(w http.ResponseWriter, r *http.Request, fn func(context.Context, pgx.Tx, tenant.Principal) (portalEdit, error)) {
	publicHeaders(w)
	p, ok := m.person(w, r)
	if !ok {
		return
	}
	if m.pool == nil {
		fail(w, http.StatusServiceUnavailable, "portal unavailable")
		return
	}
	var item portalEdit
	err := db.InTenant(r.Context(), m.pool, p.TenantID, func(tx pgx.Tx) error {
		if err := lockPortalTenant(r.Context(), tx); err != nil {
			return err
		}
		if err := lockPortalTree(r.Context(), tx); err != nil {
			return err
		}
		if err := authz.RequireTx(r.Context(), tx, p, "settings.manage", authz.Scope{}); err != nil {
			return err
		}
		if _, err := tx.Exec(r.Context(), `SELECT set_config('aeon.portal_moderation','on',true)`); err != nil {
			return err
		}
		var applyErr error
		item, applyErr = fn(r.Context(), tx, p)
		return applyErr
	})
	if errors.Is(err, authz.ErrForbidden) {
		fail(w, http.StatusForbidden, "permission denied")
		return
	}
	var se statusError
	if errors.As(err, &se) {
		fail(w, se.status, se.msg)
		return
	}
	if err != nil {
		slog.Error("portal moderation", "err", err)
		fail(w, http.StatusServiceUnavailable, "portal unavailable")
		return
	}
	write(w, http.StatusOK, item)
}

func emptyWishBody(r *http.Request) error {
	buf, err := io.ReadAll(io.LimitReader(r.Body, 64))
	if err != nil || len(buf) >= 64 {
		return statusError{status: http.StatusBadRequest, msg: "invalid wish"}
	}
	trimmed := bytes.TrimSpace(buf)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("{}")) {
		return nil
	}
	return statusError{status: http.StatusBadRequest, msg: "invalid wish"}
}

func decodeEdit(r *http.Request, dest any) error {
	if !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
		return errors.New("JSON required")
	}
	buf, err := io.ReadAll(io.LimitReader(r.Body, portalEditLimit+1))
	if err != nil || len(buf) == 0 || len(buf) > portalEditLimit {
		return errors.New("invalid JSON")
	}
	dec := json.NewDecoder(bytes.NewReader(buf))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dest); err != nil {
		return errors.New("invalid JSON")
	}
	var extra struct{}
	if err := dec.Decode(&extra); err != io.EOF {
		return errors.New("invalid JSON")
	}
	return nil
}

func applyWish(ctx context.Context, tx pgx.Tx, p tenant.Principal, id, state, eventType string) (portalEdit, error) {
	current, err := loadPortalNode(ctx, tx, id, "portal_wish")
	if err != nil {
		return portalEdit{}, err
	}
	if !wishTransition(current.State, state) {
		return portalEdit{}, statusError{status: http.StatusConflict, msg: "wish cannot take that state"}
	}
	next := current
	next.State = state
	return commitPortal(ctx, tx, p, current, next, eventType)
}

func wishTransition(from, to string) bool {
	if from == to {
		return true
	}
	switch to {
	case "published":
		return from == "pending" || from == "hidden"
	case "rejected":
		return from == "pending"
	case "hidden":
		return from == "published"
	default:
		return false
	}
}

func applyProduct(ctx context.Context, tx pgx.Tx, p tenant.Principal, id string, in productWrite) (portalEdit, error) {
	current, err := loadPortalNode(ctx, tx, id, "portal_product")
	if err != nil {
		return portalEdit{}, err
	}
	next := current
	if in.Title != nil {
		title, ok := cleanText(*in.Title, 300, false)
		if !ok || title == "" {
			return portalEdit{}, statusError{status: http.StatusBadRequest, msg: "invalid product"}
		}
		next.Title = title
	}
	if in.Summary != nil {
		summary, ok := cleanText(*in.Summary, 4000, true)
		if !ok {
			return portalEdit{}, statusError{status: http.StatusBadRequest, msg: "invalid product"}
		}
		next.Body = summary
	}
	if in.Published != nil {
		next.State = "unpublished"
		if *in.Published {
			next.State = "published"
		}
	}
	return commitPortal(ctx, tx, p, current, next, "portal.product_updated")
}

func applyFeature(ctx context.Context, tx pgx.Tx, p tenant.Principal, id string, in featureWrite) (portalEdit, error) {
	current, err := loadPortalNode(ctx, tx, id, "portal_feature")
	if err != nil {
		return portalEdit{}, err
	}
	next := current
	if in.Title != nil {
		title, ok := cleanText(*in.Title, 300, false)
		if !ok || title == "" {
			return portalEdit{}, statusError{status: http.StatusBadRequest, msg: "invalid feature"}
		}
		next.Title = title
	}
	if in.Summary != nil {
		summary, ok := cleanText(*in.Summary, 4000, true)
		if !ok {
			return portalEdit{}, statusError{status: http.StatusBadRequest, msg: "invalid feature"}
		}
		next.Body = summary
	}
	status := current.State
	if in.Status != nil {
		if !catalogState(*in.Status) {
			return portalEdit{}, statusError{status: http.StatusBadRequest, msg: "invalid feature"}
		}
		status = *in.Status
	}
	fields, err := featureFields(current.Fields, status, in)
	if err != nil {
		return portalEdit{}, err
	}
	next.State = status
	next.Fields = fields
	return commitPortal(ctx, tx, p, current, next, "portal.feature_updated")
}

func featureFields(current json.RawMessage, status string, in featureWrite) (json.RawMessage, error) {
	obj := map[string]json.RawMessage{}
	if len(bytes.TrimSpace(current)) > 0 && string(current) != "null" {
		if json.Unmarshal(current, &obj) != nil || obj == nil {
			return nil, statusError{status: http.StatusBadRequest, msg: "invalid feature"}
		}
	}
	if in.Legal != nil {
		legal, ok := cleanText(*in.Legal, 240, false)
		if !ok {
			return nil, statusError{status: http.StatusBadRequest, msg: "invalid feature"}
		}
		setTextField(obj, "legal_basis", legal)
	}
	if status != "declined" {
		delete(obj, "decline_reason")
	} else if in.Reason != nil || textField(obj, "decline_reason") == "" {
		reason := ""
		if in.Reason != nil {
			reason = *in.Reason
		} else {
			reason = textField(obj, "decline_reason")
		}
		cleaned, ok := cleanText(reason, 500, true)
		if !ok || cleaned == "" {
			return nil, statusError{status: http.StatusBadRequest, msg: "A declined feature needs a public reason."}
		}
		setTextField(obj, "decline_reason", cleaned)
	}
	if status != "live" {
		delete(obj, "live_since")
	} else if in.Live != nil {
		live := strings.TrimSpace(*in.Live)
		if !liveSincePattern.MatchString(live) {
			return nil, statusError{status: http.StatusBadRequest, msg: "invalid feature"}
		}
		setTextField(obj, "live_since", live)
	} else if live := textField(obj, "live_since"); live != "" && !liveSincePattern.MatchString(live) {
		delete(obj, "live_since")
	}
	if obj == nil {
		obj = map[string]json.RawMessage{}
	}
	raw, err := json.Marshal(obj)
	if err != nil {
		return nil, err
	}
	return raw, nil
}

func setTextField(obj map[string]json.RawMessage, key, value string) {
	if value == "" {
		delete(obj, key)
		return
	}
	raw, _ := json.Marshal(value)
	obj[key] = raw
}

func textField(obj map[string]json.RawMessage, key string) string {
	var value string
	if json.Unmarshal(obj[key], &value) != nil {
		return ""
	}
	return value
}

func loadPortalNode(ctx context.Context, tx pgx.Tx, id, kind string) (portalRow, error) {
	var row portalRow
	var fields string
	err := tx.QueryRow(ctx, `
		SELECT n.id::text, n.title, n.body, n.state, n.fields::text
		FROM nodes n
		JOIN node_kinds k ON k.tenant_id = n.tenant_id AND k.id = n.kind_id
		WHERE n.id = $1::uuid AND k.slug = $2 AND n.deleted_at IS NULL
		FOR UPDATE OF n`, id, kind).Scan(&row.ID, &row.Title, &row.Body, &row.State, &fields)
	if errors.Is(err, pgx.ErrNoRows) {
		return portalRow{}, statusError{status: http.StatusNotFound, msg: "not found"}
	}
	if err != nil {
		return portalRow{}, err
	}
	if fields == "" {
		fields = "{}"
	}
	row.Fields = json.RawMessage(fields)
	return row, nil
}

func commitPortal(ctx context.Context, tx pgx.Tx, p tenant.Principal, before, after portalRow, eventType string) (portalEdit, error) {
	if portalSame(before, after) {
		return presentPortal(after), nil
	}
	var fields string
	err := tx.QueryRow(ctx, `
		UPDATE nodes
		SET title = $2, body = $3, state = $4, fields = $5::jsonb,
		    updated_at = greatest(clock_timestamp(), updated_at + interval '1 microsecond')
		WHERE id = $1::uuid AND deleted_at IS NULL
		RETURNING title, body, state, fields::text`,
		after.ID, after.Title, after.Body, after.State, string(after.Fields)).Scan(&after.Title, &after.Body, &after.State, &fields)
	if err != nil {
		return portalEdit{}, err
	}
	if fields == "" {
		fields = "{}"
	}
	after.Fields = json.RawMessage(fields)
	if _, err := events.Append(ctx, tx, p, events.Change{
		NodeID: &after.ID,
		Type:   eventType,
		Before: portalSnap(before),
		After:  portalSnap(after),
	}); err != nil {
		return portalEdit{}, err
	}
	return presentPortal(after), nil
}

func portalSame(a, b portalRow) bool {
	return a.Title == b.Title && a.Body == b.Body && a.State == b.State && bytes.Equal(canonicalJSON(a.Fields), canonicalJSON(b.Fields))
}

func canonicalJSON(raw json.RawMessage) []byte {
	var value any
	if json.Unmarshal(raw, &value) != nil {
		return raw
	}
	out, err := json.Marshal(value)
	if err != nil {
		return raw
	}
	return out
}

func portalSnap(row portalRow) map[string]any {
	fields := row.Fields
	if len(fields) == 0 {
		fields = json.RawMessage(`{}`)
	}
	return map[string]any{
		"id": row.ID, "title": row.Title, "body": row.Body, "state": row.State, "fields": fields,
	}
}

func presentPortal(row portalRow) portalEdit {
	return portalEdit{
		ID: row.ID, Title: row.Title, Summary: row.Body, State: row.State, Fields: publicFieldMap(row.Fields),
	}
}

func publicFieldMap(raw json.RawMessage) map[string]string {
	out := map[string]string{}
	var obj map[string]any
	if json.Unmarshal(raw, &obj) != nil {
		return out
	}
	for _, key := range []string{"live_since", "legal_basis", "decline_reason"} {
		if text, ok := obj[key].(string); ok && text != "" {
			out[key] = text
		}
	}
	return out
}

func cleanText(raw string, max int, allowBreaks bool) (string, bool) {
	raw = strings.TrimSpace(strings.ReplaceAll(raw, "\x00", ""))
	if strings.ContainsRune(raw, '\x00') {
		return "", false
	}
	for _, r := range raw {
		switch r {
		case '\n', '\r':
			if !allowBreaks {
				return "", false
			}
		case '\t':
		default:
			if unicode.IsControl(r) {
				return "", false
			}
		}
	}
	if len([]rune(raw)) > max {
		return "", false
	}
	return raw, true
}
