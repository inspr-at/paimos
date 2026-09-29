// SPDX-License-Identifier: AGPL-3.0-only

package harness

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"errors"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"

	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/inspr-at/paimos/internal/workorders"
)

// Instruction provenance is append-only on the session. A worker may still post
// allowlisted file and prompt-template identities. A rules receipt and heartbeat
// registration also append: the merged rules version, published set versions
// when that merge still matches, and hashes of AGENTS.md or CLAUDE.md when
// those files are present. Rows never store file contents, rule text, secrets,
// local paths, or a hash of a version identifier. rules_merged and rules_set
// are server-recorded; the worker POST rejects them.

const maxProvenanceItems = 16
const maxAutomaticProvenanceItems = 64
const maxProvenanceRevisions = 32
const maxProvenanceBytes int64 = 1 << 20
const maxInstructionSourceHits = 200

var (
	provenanceSHA256 = regexp.MustCompile(`^[0-9a-f]{64}$`)
	provenanceSkill  = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}/SKILL\.md$`)
)

// ProvenanceItem is one allowlisted instruction or prompt-template identity.
// HashKind is content when ContentSHA256 is a digest of the file or template
// bytes. It is absent for a prompt template recorded with a version and no
// template digest. A version identifier is never stored as that digest.
type ProvenanceItem struct {
	Kind          string  `json:"kind"`
	LogicalName   string  `json:"logical_name"`
	HashKind      string  `json:"hash_kind"`
	ContentSHA256 *string `json:"content_sha256"`
	Version       *string `json:"version"`
	ByteSize      *int64  `json:"byte_size"`
}

// ProvenanceRevision is one immutable published set.
type ProvenanceRevision struct {
	ID         string           `json:"id"`
	SessionID  string           `json:"session_id"`
	Revision   int64            `json:"revision"`
	SetSHA256  string           `json:"set_sha256"`
	RecordedBy string           `json:"recorded_by_principal_id"`
	RecordedAt time.Time        `json:"recorded_at"`
	Items      []ProvenanceItem `json:"items"`
}

// ProvenancePage is the newest revisions for one visible session.
type ProvenancePage struct {
	SessionID string               `json:"session_id"`
	Revisions []ProvenanceRevision `json:"revisions"`
	Truncated bool                 `json:"truncated"`
}

type provenanceRecorded struct {
	ProvenanceRevision
	Replayed bool `json:"replayed"`
}

func (m *Module) readProvenance(r *http.Request, tx pgx.Tx, _ tenant.Principal) (any, error) {
	ctx := r.Context()
	s, err := load(ctx, tx, r.PathValue("projectId"), r.PathValue("sessionId"), false)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, workorders.Fail(404, "not found")
		}
		return nil, err
	}
	return provenancePage(ctx, tx, s.ID)
}

func (m *Module) recordProvenance(r *http.Request, tx pgx.Tx, p tenant.Principal) (any, error) {
	var in struct {
		Items []ProvenanceItem `json:"items"`
	}
	if err := workorders.Decode(r, &in); err != nil {
		return nil, err
	}
	items, err := normalizeProvenanceItems(in.Items)
	if err != nil {
		return nil, err
	}
	ctx := r.Context()
	s, err := worker(ctx, tx, r, p)
	if err != nil {
		return nil, err
	}
	// File posts replace file and prompt identities and keep server-recorded
	// rule versions, so a later heartbeat cannot drop a receipt's sources.
	return appendInstructionSources(ctx, tx, p, s, items, map[string]bool{
		"agents": true, "claude": true, "skill": true, "prompt_template": true,
	})
}

// insertProvenanceRevision appends one immutable set, or returns the latest
// revision when that set digest is already current. Replay writes no audit row.
func insertProvenanceRevision(ctx context.Context, tx pgx.Tx, p tenant.Principal, s Session, items []ProvenanceItem) (provenanceRecorded, error) {
	digest := provenanceSetDigest(items)
	var latestID string
	var latestRevision int64
	var latestDigest []byte
	err := tx.QueryRow(ctx, `SELECT id::text, revision, set_digest FROM harness_instruction_provenance WHERE session_id=$1 ORDER BY revision DESC LIMIT 1 FOR UPDATE`, s.ID).Scan(&latestID, &latestRevision, &latestDigest)
	if err == nil && subtle.ConstantTimeCompare(latestDigest, digest) == 1 {
		got, loadErr := provenanceRevision(ctx, tx, s.ID, latestID)
		if loadErr != nil {
			return provenanceRecorded{}, loadErr
		}
		return provenanceRecorded{ProvenanceRevision: got, Replayed: true}, nil
	}
	next := int64(1)
	var before any
	if err == nil {
		previous, loadErr := provenanceRevision(ctx, tx, s.ID, latestID)
		if loadErr != nil {
			return provenanceRecorded{}, loadErr
		}
		before = previous
		next = latestRevision + 1
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return provenanceRecorded{}, err
	}
	var out ProvenanceRevision
	err = tx.QueryRow(ctx, `INSERT INTO harness_instruction_provenance(tenant_id, session_id, revision, set_digest, recorded_by) VALUES($1,$2,$3,$4,$5) RETURNING id::text, revision, encode(set_digest, 'hex'), recorded_by::text, created_at`, p.TenantID, s.ID, next, digest, p.ID).Scan(&out.ID, &out.Revision, &out.SetSHA256, &out.RecordedBy, &out.RecordedAt)
	if err != nil {
		return provenanceRecorded{}, err
	}
	out.SessionID = s.ID
	out.Items = items
	for i, item := range items {
		if _, err = tx.Exec(ctx, `INSERT INTO harness_instruction_provenance_items(tenant_id, provenance_id, ordinal, kind, logical_name, hash_kind, content_sha256, version, byte_size) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9)`, p.TenantID, out.ID, i, item.Kind, item.LogicalName, item.HashKind, item.ContentSHA256, item.Version, item.ByteSize); err != nil {
			return provenanceRecorded{}, err
		}
	}
	if err = record(ctx, tx, p, s, "provenance_recorded", before, out); err != nil {
		return provenanceRecorded{}, err
	}
	return provenanceRecorded{ProvenanceRevision: out, Replayed: false}, nil
}

// appendInstructionSources keeps instruction-file items and replaces kinds in
// replaceKinds, then appends one revision when the combined digest changed.
// An empty incoming set records nothing. The stored rows are hashes and
// version identifiers only.
func appendInstructionSources(ctx context.Context, tx pgx.Tx, p tenant.Principal, s Session, incoming []ProvenanceItem, replaceKinds map[string]bool) (provenanceRecorded, error) {
	if len(incoming) == 0 {
		return provenanceRecorded{}, nil
	}
	var latestID string
	err := tx.QueryRow(ctx, `SELECT id::text FROM harness_instruction_provenance WHERE session_id=$1 ORDER BY revision DESC LIMIT 1 FOR UPDATE`, s.ID).Scan(&latestID)
	var latest []ProvenanceItem
	if err == nil {
		rev, loadErr := provenanceRevision(ctx, tx, s.ID, latestID)
		if loadErr != nil {
			return provenanceRecorded{}, loadErr
		}
		latest = rev.Items
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return provenanceRecorded{}, err
	}
	items, err := normalizeAutomaticProvenance(combineProvenanceItems(latest, incoming, replaceKinds))
	if err != nil {
		return provenanceRecorded{}, err
	}
	return insertProvenanceRevision(ctx, tx, p, s, items)
}

func combineProvenanceItems(latest, incoming []ProvenanceItem, replaceKinds map[string]bool) []ProvenanceItem {
	skip := map[string]bool{}
	for _, item := range incoming {
		skip[item.LogicalName] = true
	}
	out := make([]ProvenanceItem, 0, len(latest)+len(incoming))
	for _, item := range latest {
		if replaceKinds[item.Kind] || skip[item.LogicalName] {
			continue
		}
		out = append(out, item)
	}
	return append(out, incoming...)
}

func provenancePage(ctx context.Context, tx pgx.Tx, sessionID string) (ProvenancePage, error) {
	page := ProvenancePage{SessionID: sessionID, Revisions: []ProvenanceRevision{}}
	var n int64
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM harness_instruction_provenance WHERE session_id=$1`, sessionID).Scan(&n); err != nil {
		return page, err
	}
	page.Truncated = n > maxProvenanceRevisions
	revisions, err := queryProvenance(ctx, tx, sessionID, "", maxProvenanceRevisions)
	if err != nil {
		return page, err
	}
	if revisions != nil {
		page.Revisions = revisions
	}
	return page, nil
}

func provenanceRevision(ctx context.Context, tx pgx.Tx, sessionID, id string) (ProvenanceRevision, error) {
	revisions, err := queryProvenance(ctx, tx, sessionID, id, 1)
	if err != nil {
		return ProvenanceRevision{}, err
	}
	if len(revisions) != 1 {
		return ProvenanceRevision{}, workorders.Fail(404, "not found")
	}
	return revisions[0], nil
}

func queryProvenance(ctx context.Context, tx pgx.Tx, sessionID, onlyID string, limit int) ([]ProvenanceRevision, error) {
	q := `SELECT p.id::text, p.revision, encode(p.set_digest, 'hex'), p.recorded_by::text, p.created_at, i.kind, i.logical_name, i.hash_kind, i.content_sha256, i.version, i.byte_size
		FROM (SELECT id, tenant_id, revision, set_digest, recorded_by, created_at FROM harness_instruction_provenance WHERE session_id=$1`
	args := []any{sessionID}
	if onlyID != "" {
		args = append(args, onlyID)
		q += ` AND id=$` + strconv.Itoa(len(args))
	}
	args = append(args, limit)
	q += ` ORDER BY revision DESC LIMIT $` + strconv.Itoa(len(args)) + `) p
		JOIN harness_instruction_provenance_items i ON i.tenant_id=p.tenant_id AND i.provenance_id=p.id
		ORDER BY p.revision DESC, i.ordinal`
	rows, err := tx.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ProvenanceRevision
	index := map[string]int{}
	for rows.Next() {
		var rev ProvenanceRevision
		var item ProvenanceItem
		if err = rows.Scan(&rev.ID, &rev.Revision, &rev.SetSHA256, &rev.RecordedBy, &rev.RecordedAt, &item.Kind, &item.LogicalName, &item.HashKind, &item.ContentSHA256, &item.Version, &item.ByteSize); err != nil {
			return nil, err
		}
		rev.SessionID = sessionID
		at, ok := index[rev.ID]
		if !ok {
			rev.Items = []ProvenanceItem{}
			out = append(out, rev)
			at = len(out) - 1
			index[rev.ID] = at
		}
		out[at].Items = append(out[at].Items, item)
	}
	return out, rows.Err()
}

func normalizeProvenanceItems(in []ProvenanceItem) ([]ProvenanceItem, error) {
	return normalizeProvenance(in, maxProvenanceItems, false)
}

func normalizeAutomaticProvenance(in []ProvenanceItem) ([]ProvenanceItem, error) {
	items, err := normalizeProvenance(in, maxAutomaticProvenanceItems, true)
	if err != nil {
		return nil, errors.New("instruction provenance could not be recorded")
	}
	return items, nil
}

func normalizeProvenance(in []ProvenanceItem, limit int, serverKinds bool) ([]ProvenanceItem, error) {
	if len(in) < 1 || len(in) > limit {
		return nil, workorders.Fail(400, "invalid instruction provenance")
	}
	items := append([]ProvenanceItem(nil), in...)
	sort.Slice(items, func(i, j int) bool {
		if items[i].LogicalName != items[j].LogicalName {
			return items[i].LogicalName < items[j].LogicalName
		}
		return items[i].Kind < items[j].Kind
	})
	seen := map[string]bool{}
	for i := range items {
		item := &items[i]
		if !validProvenanceShape(item, serverKinds) || seen[item.LogicalName] {
			return nil, workorders.Fail(400, "invalid instruction provenance")
		}
		seen[item.LogicalName] = true
	}
	return items, nil
}

func validProvenanceItem(item *ProvenanceItem) bool {
	return validProvenanceShape(item, false)
}

func validProvenanceShape(item *ProvenanceItem, serverKinds bool) bool {
	versionRequired := item.Kind == "prompt_template" || item.Kind == "rules_merged" || item.Kind == "rules_set"
	if !validProvenanceVersion(item.Version, versionRequired) || !validProvenanceDigest(item) {
		return false
	}
	switch item.Kind {
	case "agents":
		return item.LogicalName == "AGENTS.md" && validProvenanceSize(item.ByteSize)
	case "claude":
		return item.LogicalName == "CLAUDE.md" && validProvenanceSize(item.ByteSize)
	case "skill":
		return provenanceSkill.MatchString(item.LogicalName) && validProvenanceSize(item.ByteSize)
	case "prompt_template":
		return item.LogicalName == "prompt-template" && item.ByteSize == nil
	case "rules_merged":
		return serverKinds && item.LogicalName == "merged-rules" && validProvenanceSize(item.ByteSize)
	case "rules_set":
		return serverKinds && workorders.UUID(item.LogicalName) && item.LogicalName == strings.ToLower(item.LogicalName) && item.ByteSize == nil
	default:
		return false
	}
}

func validProvenanceDigest(item *ProvenanceItem) bool {
	switch item.HashKind {
	case "content":
		return item.ContentSHA256 != nil && provenanceSHA256.MatchString(*item.ContentSHA256)
	case "absent":
		return item.Kind == "prompt_template" && item.ContentSHA256 == nil
	default:
		return false
	}
}

func validProvenanceSize(size *int64) bool {
	return size != nil && *size >= 0 && *size <= maxProvenanceBytes
}

func validProvenanceVersion(version *string, required bool) bool {
	if version == nil {
		return !required
	}
	v := *version
	if v != strings.TrimSpace(v) || !utf8.ValidString(v) {
		return false
	}
	n := utf8.RuneCountInString(v)
	if n < 1 || n > 80 || strings.Contains(v, "..") || strings.ContainsAny(v, "/\\:") {
		return false
	}
	for _, r := range v {
		if unicode.IsControl(r) || unicode.IsSpace(r) {
			return false
		}
	}
	return true
}

func provenanceSetDigest(items []ProvenanceItem) []byte {
	h := sha256.New()
	_, _ = h.Write([]byte("aeon.harness.provenance.v1\x00"))
	for i, item := range items {
		if i > 0 {
			_, _ = h.Write([]byte{0x1e})
		}
		version := ""
		if item.Version != nil {
			version = *item.Version
		}
		size := ""
		if item.ByteSize != nil {
			size = strconv.FormatInt(*item.ByteSize, 10)
		}
		digest := ""
		if item.ContentSHA256 != nil {
			digest = *item.ContentSHA256
		}
		_, _ = h.Write([]byte(item.Kind + "\x1f" + item.LogicalName + "\x1f" + item.HashKind + "\x1f" + digest + "\x1f" + version + "\x1f" + size))
	}
	return h.Sum(nil)
}
