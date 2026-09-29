// SPDX-License-Identifier: AGPL-3.0-only

package harness

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/inspr-at/paimos/internal/releasehistory"
	"github.com/inspr-at/paimos/internal/rules"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/inspr-at/paimos/internal/workorders"
	"github.com/jackc/pgx/v5"
)

// RulesReceiptWrite reports receipt of bytes, not installation or execution.
// Pointers require explicit zero for the initial CAS and an explicit byte count.
type RulesReceiptWrite struct {
	RequestID        string        `json:"request_id"`
	ExpectedRevision *int64        `json:"expected_revision"`
	Context          rules.Context `json:"context"`
	BodySHA256       string        `json:"body_sha256"`
	Version          string        `json:"version"`
	ByteSize         *int          `json:"byte_size"`
	Source           string        `json:"source"`
}

type RulesReceipt struct {
	Request             RulesReceiptWrite `json:"request"`
	ID                  string            `json:"id"`
	SessionID           string            `json:"session_id"`
	Revision            int64             `json:"revision"`
	RecordedBy          string            `json:"recorded_by_principal_id"`
	RecordedAt          time.Time         `json:"recorded_at"`
	Evidence            string            `json:"evidence"`
	PublicationVerified bool              `json:"publication_verified"`
	LoadVerified        bool              `json:"load_verified"`
	ExecutionVerified   bool              `json:"execution_verified"`
	AuthorityGranted    bool              `json:"authority_granted"`
}

type rulesReceiptRecorded struct {
	Receipt  RulesReceipt `json:"receipt"`
	Replayed bool         `json:"replayed"`
}

const rulesReceiptColumns = `id::text,session_id::text,revision,request,recorded_by::text,created_at`

func scanRulesReceipt(row pgx.Row) (RulesReceipt, error) {
	out := RulesReceipt{Evidence: "worker_reported_received"}
	var raw []byte
	err := row.Scan(&out.ID, &out.SessionID, &out.Revision, &raw, &out.RecordedBy, &out.RecordedAt)
	if err == nil {
		err = json.Unmarshal(raw, &out.Request)
	}
	return out, err
}

func (m *Module) recordRulesReceipt(r *http.Request, tx pgx.Tx, p tenant.Principal) (any, error) {
	var in RulesReceiptWrite
	// All strings are UUIDs, digests, fixed enums or calendar versions. Unknown
	// fields (including bodies, paths and caller-supplied evidence) are rejected.
	r.Body = http.MaxBytesReader(nil, r.Body, 8192)
	if err := workorders.Decode(r, &in); err != nil {
		return nil, err
	}
	if !workorders.UUID(in.RequestID) || strings.ToLower(in.RequestID) != in.RequestID ||
		in.ExpectedRevision == nil || *in.ExpectedRevision < 0 || *in.ExpectedRevision == math.MaxInt64 ||
		!provenanceSHA256.MatchString(in.BodySHA256) || in.ByteSize == nil || *in.ByteSize < 1 || *in.ByteSize > rules.MaxBytes ||
		(in.Version != "floor-only" && !releasehistory.ValidVersion(in.Version)) ||
		(in.Source != "online" && in.Source != "cache" && in.Source != "floor-only") ||
		(in.Source == "floor-only") != (in.Version == "floor-only") || rules.ValidateContext(in.Context) != nil {
		return nil, workorders.Fail(400, "invalid rules receipt metadata")
	}
	ctx := r.Context()
	// The exact existing lease/owner/project checks run before even an identical
	// replay. worker locks the generation, serializing CAS with stop/archive/bind.
	s, err := worker(ctx, tx, r, p)
	if err != nil {
		return nil, err
	}
	c := in.Context
	h := s.Harness
	if h == "claude" {
		h = "claude-code"
	}
	if c.TenantID != p.TenantID || c.ProjectID != s.ProjectID || c.AgentID != s.AgentPrincipalID || c.Harness != h {
		return nil, workorders.Fail(403, "rules receipt context rejected")
	}
	// Same canonical active creator resolution as rules.actorOwner. Never accept
	// a caller-selected person identity as evidence of ownership.
	var owner string
	if !workorders.UUID(p.KeyCreatorID) {
		return nil, workorders.Fail(403, "rules receipt owner rejected")
	}
	err = tx.QueryRow(ctx, `SELECT coalesce(linked_to,id)::text FROM principals WHERE tenant_id=$1 AND id=$2 AND kind='person' AND status='active'`, p.TenantID, p.KeyCreatorID).Scan(&owner)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && owner != c.PersonID) {
		return nil, workorders.Fail(403, "rules receipt owner rejected")
	}
	if err != nil {
		return nil, err
	}
	if c.TaskID != "" {
		if s.TicketNodeID == nil || *s.TicketNodeID != c.TaskID {
			return nil, workorders.Fail(403, "rules receipt task binding rejected")
		}
		var visible bool
		err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM nodes WHERE id=$1 AND project_id=$2 AND deleted_at IS NULL)`, c.TaskID, s.ProjectID).Scan(&visible)
		if err != nil {
			return nil, err
		}
		if !visible {
			return nil, workorders.Fail(403, "rules receipt task binding rejected")
		}
	}
	raw, err := json.Marshal(in)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(append([]byte("aeon.harness.rules-receipt.v1\x00"), raw...))
	var oldDigest []byte
	err = tx.QueryRow(ctx, `SELECT request_digest FROM harness_rules_receipts WHERE session_id=$1 AND request_id=$2`, s.ID, in.RequestID).Scan(&oldDigest)
	if err == nil {
		if subtle.ConstantTimeCompare(oldDigest, sum[:]) != 1 {
			return nil, workorders.Fail(409, "rules receipt request_id already used with different metadata")
		}
		out, err := scanRulesReceipt(tx.QueryRow(ctx, `SELECT `+rulesReceiptColumns+` FROM harness_rules_receipts WHERE session_id=$1 AND request_id=$2`, s.ID, in.RequestID))
		return rulesReceiptRecorded{Receipt: out, Replayed: true}, err
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}
	var revision int64
	if err = tx.QueryRow(ctx, `SELECT coalesce(max(revision),0) FROM harness_rules_receipts WHERE session_id=$1`, s.ID).Scan(&revision); err != nil {
		return nil, err
	}
	if revision != *in.ExpectedRevision {
		return nil, workorders.Fail(409, "rules receipt expected_revision conflict; read receipt history")
	}
	out, err := scanRulesReceipt(tx.QueryRow(ctx, `INSERT INTO harness_rules_receipts(tenant_id,session_id,revision,request_id,request_digest,request,recorded_by) VALUES($1,$2,$3,$4,$5,$6,$7) RETURNING `+rulesReceiptColumns, p.TenantID, s.ID, revision+1, in.RequestID, sum[:], raw, p.ID))
	if err != nil {
		return nil, err
	}
	if err = record(ctx, tx, p, s, "rules_received", nil, out); err != nil {
		return nil, err
	}
	if err = recordReceiptProvenance(ctx, tx, p, s, in); err != nil {
		return nil, err
	}
	return rulesReceiptRecorded{Receipt: out}, nil
}

func (m *Module) readRulesReceipts(r *http.Request, tx pgx.Tx, _ tenant.Principal) (any, error) {
	var before *int64
	for key, values := range r.URL.Query() {
		if key != "before_revision" || len(values) != 1 {
			return nil, workorders.Fail(400, "invalid receipt history cursor")
		}
		value, err := strconv.ParseInt(values[0], 10, 64)
		if err != nil || value < 1 {
			return nil, workorders.Fail(400, "invalid receipt history cursor")
		}
		before = &value
	}
	s, err := load(r.Context(), tx, r.PathValue("projectId"), r.PathValue("sessionId"), false)
	if err != nil {
		return nil, err
	}
	rows, err := tx.Query(r.Context(), `SELECT `+rulesReceiptColumns+` FROM harness_rules_receipts WHERE session_id=$1 AND ($2::bigint IS NULL OR revision<$2) ORDER BY revision DESC LIMIT 33`, s.ID, before)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []RulesReceipt{}
	for rows.Next() {
		item, err := scanRulesReceipt(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	var next *int64
	if len(items) > 32 {
		next = &items[31].Revision
		items = items[:32]
	}
	return struct {
		SessionID string         `json:"session_id"`
		Receipts  []RulesReceipt `json:"receipts"`
		Next      *int64         `json:"next_before_revision"`
	}{s.ID, items, next}, rows.Err()
}
