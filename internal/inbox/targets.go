// SPDX-License-Identifier: AGPL-3.0-only

package inbox

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"path/filepath"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/events"
	"github.com/inspr-at/paimos/internal/httpapi"
	"github.com/inspr-at/paimos/internal/plugins"
	"github.com/inspr-at/paimos/internal/tenant"
)

// NewMessaging returns the additional P5.4 httpapi.Module. Mount beside New;
// migrations 0510-0519 and api/openapi-messaging.yaml are its contract.
// The coordinator supplies a persistent, dedicated 32-byte encryption key;
// replacing that key requires an explicit re-encryption migration. Nothing in
// this package reads environment secrets or starts vendor processes. Target
// registration is configuration, never proof of local process ownership.
// The coordinator mounts this module and runs NewRoutineDispatcher per tenant
// for server-owned grok_bot_routine webhook delivery; neither constructor
// starts a background worker implicitly.
func NewMessaging(pool *pgxpool.Pool, key []byte) (httpapi.Module, error) {
	if len(key) != 32 {
		return nil, errors.New("messaging requires a 32-byte encryption key")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, errors.New("messaging encryption unavailable")
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, errors.New("messaging encryption unavailable")
	}
	return &messaging{base: newModule(pool), aead: aead}, nil
}

// MessagingPlugin supplies a sealed registration for plugins.Builtin's extra
// constructors. This is a host HTTP capability; it deliberately declares no
// executable tools or workflow gates. Agent authority remains inbox.send.
func MessagingPlugin() (plugins.Plugin, error) {
	p := plugins.Plugin{Manifest: plugins.Manifest{ID: "inbox_messaging", Version: "1", Owner: "inspr-at"}}
	digest, err := plugins.Digest(p)
	p.Manifest.DigestSHA256 = digest
	return p, err
}

type messaging struct {
	base *module
	aead cipher.AEAD
	// databaseClock replaces clock_timestamp() for lease decisions. Production
	// leaves it nil. Tests set it to simulate an API host ahead of or behind
	// the database.
	databaseClock func(context.Context, pgx.Tx) (time.Time, error)
}

func (m *messaging) Mount(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/projects/{projectId}/message-targets", m.setTarget)
	mux.HandleFunc("GET /api/projects/{projectId}/message-targets", m.getTargets)
	mux.HandleFunc("POST /api/projects/{projectId}/messages", m.sendMessage)
	mux.HandleFunc("GET /api/projects/{projectId}/messages", m.inspectMessages)
	mux.HandleFunc("POST /api/projects/{projectId}/messages/{messageId}/resolution", m.resolveMessage)
	mux.HandleFunc("GET /api/projects/{projectId}/messages/listen", m.listenMessages)
	mux.HandleFunc("POST /api/projects/{projectId}/messages/{messageId}/ack", m.ackCompatMessage)
	mux.HandleFunc("GET /api/projects/{projectId}/message-deliveries", m.getDeliveries)
	mux.HandleFunc("POST /api/projects/{projectId}/messages/delivery-claim", m.claimDelivery)
	mux.HandleFunc("POST /api/projects/{projectId}/messages/delivery-complete", m.completeDelivery)
	mux.HandleFunc("POST /api/projects/{projectId}/messages/delivery-unavailable", m.unavailableDelivery)
}

// Messaging errors must not log pgx errors: they can contain private row
// values. Return only controlled error codes, including on encryption failure.
func messagingFailure(w http.ResponseWriter, err error) {
	var he *httpError
	if errors.As(err, &he) {
		writeError(w, he.status, he.code, he.msg)
		return
	}
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, 404, "not_found", "not found")
		return
	}
	writeError(w, 500, "internal_error", "messaging operation failed")
}
func (m *messaging) messagingPrincipal(w http.ResponseWriter, r *http.Request, admin bool) (tenant.Principal, string, bool) {
	p, ok := principal(w, r)
	if !ok {
		return p, "", false
	}
	project, valid := parseUUID(r.PathValue("projectId"))
	if !valid {
		messagingFailure(w, errNotFound)
		return p, "", false
	}
	if admin && authz.Require(authz.BindPool(r.Context(), m.base.pool), "inbox.manage", authz.Scope{ProjectID: project}) != nil {
		messagingFailure(w, errForbidden)
		return p, "", false
	}
	return p, project, true
}
func messagingProject(ctx context.Context, tx pgx.Tx, project string) error {
	var exists bool
	err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM nodes n JOIN node_kinds k ON k.tenant_id=n.tenant_id AND k.id=n.kind_id WHERE n.id=$1::uuid AND k.slug='project' AND n.deleted_at IS NULL)`, project).Scan(&exists)
	if err != nil {
		return err
	}
	if !exists {
		return errNotFound
	}
	return nil
}

var messageAddressRE = regexp.MustCompile(`^(paimos|codex|claude|pi|cursor|grok|grok_bot):([a-z][a-z0-9_-]{0,63})$`)
var cloudSessionRE = regexp.MustCompile(`^(session|cse)_[A-Za-z0-9_-]{1,128}$`)

// resolveAddress never trusts an attribution header. A registered address is
// bound immutably to its principal; otherwise the name must be one unique agent.
func resolveAddress(ctx context.Context, tx pgx.Tx, project, address string) (string, error) {
	if id, ok := parseUUID(address); ok {
		var found string
		err := tx.QueryRow(ctx, `SELECT id::text FROM principals WHERE id=$1::uuid`, id).Scan(&found)
		return found, err
	}
	match := messageAddressRE.FindStringSubmatch(address)
	if match == nil {
		return "", badRequest("invalid harness address")
	}
	var id string
	err := tx.QueryRow(ctx, `SELECT principal_id::text FROM inbox_message_targets WHERE project_id=$1::uuid AND address=$2 ORDER BY version DESC LIMIT 1`, project, address).Scan(&id)
	if err == nil {
		return id, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return "", err
	}
	ids, err := tx.Query(ctx, `SELECT id::text FROM principals WHERE kind='agent' AND name=$1 ORDER BY id LIMIT 2`, match[2])
	if err != nil {
		return "", err
	}
	defer ids.Close()
	var found []string
	for ids.Next() {
		var v string
		if err := ids.Scan(&v); err != nil {
			return "", err
		}
		found = append(found, v)
	}
	if err := ids.Err(); err != nil {
		return "", err
	}
	if len(found) != 1 {
		return "", errNotFound
	}
	return found[0], nil
}

type targetInput struct {
	Address      string `json:"address"`
	PrincipalID  string `json:"principal_id,omitempty"`
	Adapter      string `json:"adapter"`
	Kind         string `json:"target_kind"`
	Ref          string `json:"target_ref"`
	Secret       string `json:"target_secret,omitempty"`
	MaximumLevel string `json:"maximum_level,omitempty"`
	Role         string `json:"role,omitempty"`
}

// MessageTarget is the complete public projection; private values cannot be
// marshalled by accident because they are absent from this type.
type MessageTarget struct {
	ID           string    `json:"id"`
	PrincipalID  string    `json:"principal_id"`
	Address      string    `json:"address"`
	Adapter      string    `json:"adapter"`
	Kind         string    `json:"target_kind"`
	MaximumLevel string    `json:"maximum_level"`
	Role         string    `json:"role"`
	Version      int       `json:"version"`
	Enabled      bool      `json:"enabled"`
	HasSecret    bool      `json:"has_secret"`
	CreatedAt    time.Time `json:"created_at"`
}

const compatTargetCols = `id::text,principal_id::text,address,adapter,target_kind,maximum_level,role,version,enabled,has_secret,created_at`

func scanCompatTarget(row pgx.Row) (MessageTarget, error) {
	var v MessageTarget
	err := row.Scan(&v.ID, &v.PrincipalID, &v.Address, &v.Adapter, &v.Kind, &v.MaximumLevel, &v.Role, &v.Version, &v.Enabled, &v.HasSecret, &v.CreatedAt)
	return v, err
}

func validateCompatTarget(ctx context.Context, in *targetInput) error {
	match := messageAddressRE.FindStringSubmatch(in.Address)
	if match == nil {
		return badRequest("invalid harness address")
	}
	if in.Role == "" {
		in.Role = "primary"
	}
	if in.MaximumLevel == "" {
		in.MaximumLevel = "simple"
	}
	if in.Role != "primary" && in.Role != "simple_fallback" {
		return badRequest("invalid target role")
	}
	if in.MaximumLevel != "simple" && in.MaximumLevel != "steer" {
		return badRequest("invalid maximum level")
	}
	if in.Role == "simple_fallback" && in.MaximumLevel != "simple" {
		return badRequest("fallback must be simple")
	}
	if !utf8.ValidString(in.Ref) || len(in.Ref) == 0 || len(in.Ref) > 4096 || strings.ContainsRune(in.Ref, 0) {
		return badRequest("invalid target reference")
	}
	kind, harness, steer := "", "", false
	switch in.Adapter {
	case "codex":
		kind, harness, steer = "codex_thread", "codex", true
		if len(in.Ref) > 256 || strings.ContainsAny(in.Ref, "\r\n") {
			return badRequest("invalid target reference")
		}
	case "agentd_codex", "agentd_claude", "agentd_pi", "agentd_cursor":
		kind, harness, steer = "agentd_session", strings.TrimPrefix(in.Adapter, "agentd_"), in.Adapter != "agentd_cursor"
		var ref struct {
			Socket    string `json:"socket"`
			SessionID string `json:"session_id"`
		}
		dec := json.NewDecoder(strings.NewReader(in.Ref))
		dec.DisallowUnknownFields()
		if dec.Decode(&ref) != nil || dec.Decode(new(any)) != io.EOF {
			return badRequest("invalid target reference")
		}
		id, ok := parseUUID(ref.SessionID)
		if !ok || id != ref.SessionID || !filepath.IsAbs(ref.Socket) || strings.ContainsAny(ref.Socket, "\x00\r\n") {
			return badRequest("invalid target reference")
		}
	case "claude_resume", "claude_channel":
		kind, harness = "claude_session", "claude"
		id, local := parseUUID(in.Ref)
		local = local && id == in.Ref
		if !local && (in.Adapter == "claude_channel" || !cloudSessionRE.MatchString(in.Ref)) {
			return badRequest("invalid target reference")
		}
	case "grok_bot_routine":
		kind, harness = "https_webhook", "grok_bot"
		if err := validateWebhookURL(ctx, in.Ref); err != nil {
			return badRequest("target must be a public HTTPS URL")
		}
		if len(in.Secret) < 8 || len(in.Secret) > 512 || strings.HasPrefix(strings.ToLower(in.Secret), "bearer ") {
			return badRequest("invalid routine sender key")
		}
		for _, c := range []byte(in.Secret) {
			if c < '!' || c > '~' {
				return badRequest("invalid routine sender key")
			}
		}
		if strings.ContainsAny(in.Secret, " \t\r\n\x00") {
			return badRequest("invalid routine sender key")
		}
	default:
		return badRequest("unsupported adapter")
	}
	if match[1] != harness || in.Kind != kind {
		return badRequest("adapter does not match address or target kind")
	}
	if in.MaximumLevel == "steer" && !steer {
		return badRequest("adapter does not support steer")
	}
	if in.Adapter != "grok_bot_routine" && in.Secret != "" {
		return badRequest("adapter does not accept a target key")
	}
	return nil
}
func (m *messaging) setTarget(w http.ResponseWriter, r *http.Request) {
	p, project, ok := m.messagingPrincipal(w, r, true)
	if !ok {
		return
	}
	var in targetInput
	if !decodeJSON(w, r, 16<<10, &in) {
		return
	}
	if err := validateCompatTarget(r.Context(), &in); err != nil {
		messagingFailure(w, err)
		return
	}
	out, err := m.storeTarget(r.Context(), p, project, in)
	if err != nil {
		messagingFailure(w, err)
		return
	}
	writeJSON(w, 201, out)
}
func (m *messaging) storeTarget(ctx context.Context, p tenant.Principal, project string, in targetInput) (MessageTarget, error) {
	var out MessageTarget
	err := db.InTenant(tenant.WithPrincipal(ctx, p), m.base.pool, p.TenantID, func(tx pgx.Tx) error {
		if err := messagingProject(ctx, tx, project); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,54))`, p.TenantID+project+in.Address); err != nil {
			return err
		}
		principalID, err := resolveAddress(ctx, tx, project, in.Address)
		if err != nil {
			return err
		}
		if in.PrincipalID != "" && in.PrincipalID != principalID {
			return badRequest("principal does not match address")
		}
		var id string
		var version int
		if err := tx.QueryRow(ctx, `SELECT gen_random_uuid()::text, COALESCE(max(version),0)+1 FROM inbox_message_targets WHERE project_id=$1::uuid AND address=$2 AND role=$3`, project, in.Address, in.Role).Scan(&id, &version); err != nil {
			return err
		}
		private, _ := json.Marshal(struct {
			Ref    string `json:"ref"`
			Secret string `json:"secret"`
		}{in.Ref, in.Secret})
		nonce := make([]byte, m.aead.NonceSize())
		if _, err := rand.Read(nonce); err != nil {
			return errors.New("target encryption failed")
		}
		sealed := m.aead.Seal(nonce, nonce, private, []byte(p.TenantID+"/"+project+"/"+id))
		rows, err := tx.Query(ctx, `UPDATE inbox_message_targets SET enabled=false WHERE project_id=$1::uuid AND address=$2 AND role=$3 AND enabled RETURNING `+compatTargetCols, project, in.Address, in.Role)
		if err != nil {
			return err
		}
		var retired []MessageTarget
		for rows.Next() {
			v, e := scanCompatTarget(rows)
			if e != nil {
				rows.Close()
				return e
			}
			retired = append(retired, v)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		for _, v := range retired {
			if _, err := events.Append(ctx, tx, p, events.Change{Type: "inbox.compat_target_disabled", After: v}); err != nil {
				return err
			}
		}
		out, err = scanCompatTarget(tx.QueryRow(ctx, `INSERT INTO inbox_message_targets(tenant_id,id,project_id,principal_id,address,adapter,target_kind,maximum_level,role,version,sealed_target,has_secret) VALUES($1::uuid,$2::uuid,$3::uuid,$4::uuid,$5,$6,$7,$8,$9,$10,$11,$12) RETURNING `+compatTargetCols, p.TenantID, id, project, principalID, in.Address, in.Adapter, in.Kind, in.MaximumLevel, in.Role, version, sealed, in.Secret != ""))
		if err != nil {
			return err
		}
		_, err = events.Append(ctx, tx, p, events.Change{Type: "inbox.compat_target_created", After: out})
		return err
	})
	return out, err
}
func (m *messaging) getTargets(w http.ResponseWriter, r *http.Request) {
	p, project, ok := m.messagingPrincipal(w, r, true)
	if !ok {
		return
	}
	items := []MessageTarget{}
	err := db.InTenant(r.Context(), m.base.pool, p.TenantID, func(tx pgx.Tx) error {
		if err := messagingProject(r.Context(), tx, project); err != nil {
			return err
		}
		rows, err := tx.Query(r.Context(), `SELECT `+compatTargetCols+` FROM inbox_message_targets WHERE project_id=$1::uuid AND ($2='' OR address=$2) ORDER BY address,role,version DESC LIMIT 200`, project, r.URL.Query().Get("address"))
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			v, err := scanCompatTarget(rows)
			if err != nil {
				return err
			}
			items = append(items, v)
		}
		return rows.Err()
	})
	if err != nil {
		messagingFailure(w, err)
		return
	}
	writeJSON(w, 200, items)
}
