// SPDX-License-Identifier: AGPL-3.0-only
// Package attachments exposes tenant-scoped node files. The coordinator mounts
// New and registers UndoHandlers with events.WithUndoHandlers; it also wires
// Verify and GC to the operator's paimos files commands.
package attachments

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"path"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/events"
	"github.com/inspr-at/paimos/internal/httpapi"
	"github.com/inspr-at/paimos/internal/tenant"
)

type Attachment struct {
	ThumbnailKind string     `json:"thumbnail_kind,omitempty"`
	ID            string     `json:"id"`
	NodeID        string     `json:"node_id"`
	SHA256        string     `json:"sha256"`
	Name          string     `json:"name"`
	ContentType   string     `json:"content_type"`
	Size          int64      `json:"size"`
	Width         *int       `json:"width"`
	Height        *int       `json:"height"`
	Caption       string     `json:"caption"`
	Position      string     `json:"position"`
	CreatedBy     string     `json:"created_by"`
	CreatedAt     time.Time  `json:"created_at"`
	UpdatedAt     time.Time  `json:"updated_at"`
	DeletedAt     *time.Time `json:"deleted_at"`
}

const columns = `id::text,node_id::text,sha256,name,content_type,size,width,height,caption,position::text,created_by::text,created_at,updated_at,deleted_at`

func scan(row pgx.Row) (Attachment, error) {
	var a Attachment
	err := row.Scan(&a.ID, &a.NodeID, &a.SHA256, &a.Name, &a.ContentType, &a.Size, &a.Width, &a.Height, &a.Caption, &a.Position, &a.CreatedBy, &a.CreatedAt, &a.UpdatedAt, &a.DeletedAt)
	if isHTML(a.ContentType) {
		a.ThumbnailKind = "html-text"
	}
	return a, err
}

type Module struct {
	Pool    *pgxpool.Pool
	Store   Store
	Sandbox *Sandbox
}

var _ httpapi.Module = (*Module)(nil)

func New(pool *pgxpool.Pool, store Store) *Module { return &Module{Pool: pool, Store: store} }
func (m *Module) Mount(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/nodes/{nodeId}/attachments", m.upload)
	mux.HandleFunc("GET /api/nodes/{nodeId}/attachments", m.list)
	mux.HandleFunc("PATCH /api/attachments/{id}", m.patch)
	mux.HandleFunc("DELETE /api/attachments/{id}", m.remove)
	mux.HandleFunc("GET /api/attachments/{id}/content", m.content)
	mux.HandleFunc("POST /api/attachments/{id}/preview", m.preview)
}
func uuid(s string) bool { var v pgtype.UUID; return len(s) == 36 && v.Scan(s) == nil && v.Valid }
func (m *Module) principal(w http.ResponseWriter, r *http.Request, permission string) (tenant.Principal, bool) {
	p, ok := tenant.PrincipalFrom(r.Context())
	if !ok || !uuid(p.ID) || !uuid(p.TenantID) {
		httpapi.WriteError(w, 401, "unauthorized")
		return p, false
	}
	if authz.Require(authz.BindPool(r.Context(), m.Pool), permission, authz.RouteScope(r.Context())) != nil {
		httpapi.WriteError(w, 403, "permission denied")
		return p, false
	}
	return p, true
}
func apierr(w http.ResponseWriter, err error) {
	if errors.Is(err, pgx.ErrNoRows) {
		httpapi.WriteError(w, 404, "not found")
		return
	}
	if e, ok := err.(*failure); ok {
		httpapi.WriteError(w, e.code, e.Error())
		return
	}
	slog.Error("attachments", "error", err)
	httpapi.WriteError(w, 500, "internal error")
}

type failure struct {
	code    int
	message string
}

func (e *failure) Error() string         { return e.message }
func bad(code int, message string) error { return &failure{code, message} }
func liveNode(ctx context.Context, tx pgx.Tx, tenantID, nodeID string) error {
	var ok bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM nodes WHERE tenant_id=$1 AND id=$2 AND deleted_at IS NULL)`, tenantID, nodeID).Scan(&ok); err != nil {
		return err
	}
	if !ok {
		return pgx.ErrNoRows
	}
	return nil
}
func (m *Module) list(w http.ResponseWriter, r *http.Request) {
	p, ok := m.principal(w, r, "attachments.read")
	if !ok {
		return
	}
	id := r.PathValue("nodeId")
	if !uuid(id) {
		httpapi.WriteError(w, 400, "invalid node id")
		return
	}
	out := []Attachment{}
	err := db.InTenant(r.Context(), m.Pool, p.TenantID, func(tx pgx.Tx) error {
		if err := liveNode(r.Context(), tx, p.TenantID, id); err != nil {
			return err
		}
		rows, err := tx.Query(r.Context(), `SELECT `+columns+` FROM attachments WHERE tenant_id=$1 AND node_id=$2 AND deleted_at IS NULL ORDER BY position,id`, p.TenantID, id)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			a, err := scan(rows)
			if err != nil {
				return err
			}
			out = append(out, a)
		}
		return rows.Err()
	})
	if err != nil {
		apierr(w, err)
		return
	}
	httpapi.WriteJSON(w, 200, out)
}

type pending struct {
	name string
	blob *Staged
}

func cleanName(s string) string {
	s = path.Base(strings.ReplaceAll(s, "\\", "/"))
	s = strings.TrimSpace(s)
	if s == "." || s == "/" || s == "" {
		s = "attachment"
	}
	s = strings.Map(func(r rune) rune {
		if r < 32 || r == 127 {
			return -1
		}
		return r
	}, s)
	if s == "" {
		s = "attachment"
	}
	if len(s) > 255 {
		s = s[:255]
		for !utf8Valid(s) {
			s = s[:len(s)-1]
		}
	}
	return s
}
func utf8Valid(s string) bool { return strings.ToValidUTF8(s, "") == s }
func (m *Module) upload(w http.ResponseWriter, r *http.Request) {
	p, ok := m.principal(w, r, "attachments.write")
	if !ok {
		return
	}
	nodeID := r.PathValue("nodeId")
	if !uuid(nodeID) {
		httpapi.WriteError(w, 400, "invalid node id")
		return
	}
	var files []pending
	defer func() {
		for _, f := range files {
			_ = f.blob.Close()
		}
	}()
	caption := ""
	ct, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if ct == "multipart/form-data" {
		r.Body = http.MaxBytesReader(w, r.Body, m.Store.limit()*20+(1<<20))
		mr, err := r.MultipartReader()
		if err != nil {
			httpapi.WriteError(w, 400, "invalid multipart body")
			return
		}
		for {
			part, err := mr.NextPart()
			if errors.Is(err, io.EOF) {
				break
			}
			if err != nil {
				apierr(w, bad(400, "invalid multipart body"))
				return
			}
			if part.FormName() == "caption" {
				b, err := io.ReadAll(io.LimitReader(part, 4097))
				if err != nil || len(b) > 4096 {
					apierr(w, bad(400, "caption too long"))
					return
				}
				caption = string(b)
				continue
			}
			if part.FormName() != "file" {
				continue
			}
			if len(files) >= 20 {
				apierr(w, bad(400, "too many files"))
				return
			}
			blob, err := m.Store.StageNamed(r.Context(), p.TenantID, part, part.FileName())
			if err != nil {
				apierr(w, uploadError(err))
				return
			}
			files = append(files, pending{cleanName(part.FileName()), blob})
		}
	} else {
		if !strings.HasPrefix(ct, "image/") {
			apierr(w, bad(415, "raw upload must be an image"))
			return
		}
		blob, err := m.Store.Stage(r.Context(), p.TenantID, r.Body)
		if err != nil {
			apierr(w, uploadError(err))
			return
		}
		if !blob.Image {
			_ = blob.Close()
			apierr(w, bad(415, "raw upload must be an image"))
			return
		}
		files = []pending{{cleanName(r.Header.Get("X-Filename")), blob}}
		caption = r.URL.Query().Get("caption")
	}
	if len(files) == 0 {
		apierr(w, bad(400, "no files"))
		return
	}
	if len(caption) > 4096 {
		apierr(w, bad(400, "caption too long"))
		return
	}
	out := make([]Attachment, 0, len(files))
	err := db.InTenant(r.Context(), m.Pool, p.TenantID, func(tx pgx.Tx) error {
		if err := liveNode(r.Context(), tx, p.TenantID, nodeID); err != nil {
			return err
		}
		staged := make([]*Staged, 0, len(files))
		for _, f := range files {
			staged = append(staged, f.blob)
		}
		if err := Publish(r.Context(), tx, OwnerAttachment, staged...); err != nil {
			return err
		}
		var pos int64
		if err := tx.QueryRow(r.Context(), `SELECT coalesce(ceil(max(position)),0)::bigint FROM attachments WHERE tenant_id=$1 AND node_id=$2`, p.TenantID, nodeID).Scan(&pos); err != nil {
			return err
		}
		for _, f := range files {
			pos++
			a, err := scan(tx.QueryRow(r.Context(), `INSERT INTO attachments(tenant_id,node_id,sha256,name,content_type,size,width,height,caption,position,created_by) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11) RETURNING `+columns, p.TenantID, nodeID, f.blob.SHA256, f.name, f.blob.ContentType, f.blob.Size, optionalDimension(f.blob.Width), optionalDimension(f.blob.Height), caption, pos, p.ID))
			if err != nil {
				return err
			}
			if _, err := events.Append(r.Context(), tx, p, events.Change{NodeID: &nodeID, Type: "attachment.added", After: a}); err != nil {
				return err
			}
			out = append(out, a)
		}
		return nil
	})
	if err != nil {
		apierr(w, err)
		return
	}
	httpapi.WriteJSON(w, 201, out)
}
func optionalDimension(n int) any {
	if n == 0 {
		return nil
	}
	return n
}
func uploadError(err error) error {
	if strings.Contains(err.Error(), "exceeds") {
		return bad(413, "file too large")
	}
	if strings.Contains(err.Error(), "unsupported") || strings.Contains(err.Error(), "image") || strings.Contains(err.Error(), "UTF-8") {
		return bad(415, "unsupported file")
	}
	return err
}

var rankRE = regexp.MustCompile(`^-?[0-9]{1,14}(\.[0-9]{1,15})?$`)

func precondition(r *http.Request) (*time.Time, error) {
	values, ok := r.Header["If-Unmodified-Since"]
	if !ok {
		return nil, nil
	}
	if len(values) != 1 {
		return nil, bad(400, "invalid If-Unmodified-Since")
	}
	t, err := time.Parse(time.RFC3339Nano, values[0])
	if err != nil {
		t, err = http.ParseTime(values[0])
	}
	if err != nil {
		return nil, bad(400, "invalid If-Unmodified-Since")
	}
	return &t, nil
}
func (m *Module) patch(w http.ResponseWriter, r *http.Request) {
	p, ok := m.principal(w, r, "attachments.write")
	if !ok {
		return
	}
	id := r.PathValue("id")
	if !uuid(id) {
		apierr(w, bad(400, "invalid attachment id"))
		return
	}
	cond, err := precondition(r)
	if err != nil {
		apierr(w, err)
		return
	}
	b, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 8192))
	if err != nil {
		apierr(w, bad(400, "invalid body"))
		return
	}
	var payload struct {
		Caption  *string `json:"caption"`
		Position *string `json:"position"`
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if dec.Decode(&payload) != nil || dec.Decode(new(any)) != io.EOF || payload.Caption == nil && payload.Position == nil {
		apierr(w, bad(400, "invalid patch"))
		return
	}
	if payload.Caption != nil && len(*payload.Caption) > 4096 {
		apierr(w, bad(400, "caption too long"))
		return
	}
	if payload.Position != nil && !rankRE.MatchString(*payload.Position) {
		apierr(w, bad(400, "invalid position"))
		return
	}
	var out Attachment
	err = db.InTenant(r.Context(), m.Pool, p.TenantID, func(tx pgx.Tx) error {
		before, err := scan(tx.QueryRow(r.Context(), `SELECT `+columns+` FROM attachments WHERE tenant_id=$1 AND id=$2 AND deleted_at IS NULL FOR UPDATE`, p.TenantID, id))
		if err != nil {
			return err
		}
		if cond != nil && !cond.Equal(before.UpdatedAt) {
			return bad(412, "stale attachment")
		}
		caption, position := before.Caption, before.Position
		if payload.Caption != nil {
			caption = *payload.Caption
		}
		if payload.Position != nil {
			position = *payload.Position
		}
		if caption == before.Caption && position == before.Position {
			out = before
			return nil
		}
		out, err = scan(tx.QueryRow(r.Context(), `UPDATE attachments SET caption=$3,position=$4,updated_at=clock_timestamp() WHERE tenant_id=$1 AND id=$2 RETURNING `+columns, p.TenantID, id, caption, position))
		if err != nil {
			return err
		}
		_, err = events.Append(r.Context(), tx, p, events.Change{NodeID: &out.NodeID, Type: "attachment.updated", Before: before, After: out})
		return err
	})
	if err != nil {
		apierr(w, err)
		return
	}
	httpapi.WriteJSON(w, 200, out)
}
func (m *Module) remove(w http.ResponseWriter, r *http.Request) {
	p, ok := m.principal(w, r, "attachments.delete")
	if !ok {
		return
	}
	id := r.PathValue("id")
	if !uuid(id) {
		apierr(w, bad(400, "invalid attachment id"))
		return
	}
	err := db.InTenant(r.Context(), m.Pool, p.TenantID, func(tx pgx.Tx) error {
		before, err := scan(tx.QueryRow(r.Context(), `SELECT `+columns+` FROM attachments WHERE tenant_id=$1 AND id=$2 AND deleted_at IS NULL FOR UPDATE`, p.TenantID, id))
		if err != nil {
			return err
		}
		after, err := scan(tx.QueryRow(r.Context(), `UPDATE attachments SET deleted_at=clock_timestamp(),updated_at=clock_timestamp() WHERE tenant_id=$1 AND id=$2 RETURNING `+columns, p.TenantID, id))
		if err != nil {
			return err
		}
		_, err = events.Append(r.Context(), tx, p, events.Change{NodeID: &before.NodeID, Type: "attachment.removed", Before: before, After: after})
		return err
	})
	if err != nil {
		apierr(w, err)
		return
	}
	w.WriteHeader(204)
}
