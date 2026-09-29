// SPDX-License-Identifier: AGPL-3.0-only

package rules

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/inspr-at/paimos/internal/workorders"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

const maxComparisonBody = 256 << 10
const maxComparisonRules = 500

var (
	comparisonHex    = regexp.MustCompile(`^[0-9a-f]{64}$`)
	comparisonName   = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)
	comparisonID     = regexp.MustCompile(`^[a-z][a-z0-9._-]{0,95}$`)
	comparisonChange = map[string]bool{"text": true, "why": true, "strength": true, "enabled": true, "local_conflict": true}
)

type comparisonUpload struct {
	Harness    string           `json:"harness"`
	Role       string           `json:"role"`
	ProjectID  string           `json:"project_id"`
	AgentID    string           `json:"agent_id,omitempty"`
	RepoSHA256 string           `json:"repo_sha256"`
	RepoName   string           `json:"repo_name,omitempty"`
	Merged     comparisonMerged `json:"merged"`
	Local      comparisonLocal  `json:"local"`
	Rules      []comparisonRule `json:"rules"`
	Counts     comparisonCounts `json:"counts"`
}

type comparisonMerged struct {
	Version   string `json:"version"`
	SHA256    string `json:"sha256"`
	RuleCount int    `json:"rule_count"`
}

type comparisonLocal struct {
	SetSHA256 string `json:"set_sha256"`
	RuleCount int    `json:"rule_count"`
}

type comparisonRule struct {
	Identity         string   `json:"identity"`
	Status           string   `json:"status"`
	LocalTextSHA256  string   `json:"local_text_sha256,omitempty"`
	MergedTextSHA256 string   `json:"merged_text_sha256,omitempty"`
	Changed          []string `json:"changed,omitempty"`
}

type comparisonCounts struct {
	Both       int `json:"both"`
	OnlyLocal  int `json:"only_local"`
	OnlyMerged int `json:"only_merged"`
	Differs    int `json:"differs"`
	Files      int `json:"files"`
}

type comparisonView struct {
	ID             string           `json:"id"`
	Harness        string           `json:"harness"`
	Role           string           `json:"role"`
	ProjectID      string           `json:"project_id"`
	RepoName       string           `json:"repo_name,omitempty"`
	RepoSHA256     string           `json:"repo_sha256"`
	MergeSHA256    string           `json:"merge_sha256"`
	MergeVersion   string           `json:"merge_version"`
	LocalSetSHA256 string           `json:"local_set_sha256"`
	Counts         comparisonCounts `json:"counts"`
	Rules          []comparisonRule `json:"rules"`
	CreatedAt      time.Time        `json:"created_at"`
}

func (m *Module) listComparisons(r *http.Request, tx pgx.Tx, p tenant.Principal) (any, error) {
	q := r.URL.Query()
	if len(q) != 1 || len(q["project_id"]) != 1 {
		return nil, fail(400, "invalid_request", "project_id is required")
	}
	projectID := q.Get("project_id")
	if !workorders.UUID(projectID) || projectID != strings.ToLower(projectID) {
		return nil, fail(400, "invalid_request", "project_id must be a lowercase UUID")
	}
	if err := authz.RequireTx(r.Context(), tx, p, "rules.read", authz.Scope{ProjectID: projectID}); err != nil {
		return nil, err
	}
	if err := comparisonProject(r, tx, projectID); err != nil {
		return nil, err
	}
	rows, err := tx.Query(r.Context(), `
		SELECT DISTINCT ON (harness) id::text, harness, role, project_id::text, repo_name, repo_sha256,
			merge_sha256, merge_version, local_set_sha256, counts, rules, created_at
		FROM rules_comparisons WHERE project_id=$1::uuid
		ORDER BY harness, created_at DESC, id DESC`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]comparisonView, 0)
	for rows.Next() {
		item, err := scanComparison(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	return map[string]any{"comparisons": out}, nil
}

func (m *Module) createComparison(r *http.Request, tx pgx.Tx, p tenant.Principal) (any, error) {
	if len(r.URL.Query()) != 0 {
		return nil, fail(400, "invalid_request", "comparison rejected")
	}
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		return nil, fail(400, "invalid_request", "comparison rejected")
	}
	if len(raw) > maxComparisonBody {
		return nil, fail(413, "invalid_request", "comparison is larger than 256 KiB")
	}
	upload, err := parseComparison(raw)
	if err != nil {
		return nil, err
	}
	if err = authz.RequireTx(r.Context(), tx, p, "rules.write", authz.Scope{ProjectID: upload.ProjectID}); err != nil {
		return nil, err
	}
	owner, err := actorOwner(r.Context(), tx, p)
	if err != nil {
		return nil, err
	}
	agentID, err := comparisonAgent(r, tx, p, owner, upload.AgentID)
	if err != nil {
		return nil, err
	}
	if err = comparisonProject(r, tx, upload.ProjectID); err != nil {
		return nil, err
	}
	counts, err := json.Marshal(upload.Counts)
	if err != nil {
		return nil, err
	}
	rulesJSON, err := json.Marshal(upload.Rules)
	if err != nil {
		return nil, err
	}
	var agent any
	if agentID != "" {
		agent = agentID
	}
	var view comparisonView
	var countsRaw, rulesRaw []byte
	err = tx.QueryRow(r.Context(), `
		INSERT INTO rules_comparisons (
			tenant_id, project_id, person_id, agent_id, harness, role, repo_sha256, repo_name,
			merge_sha256, merge_version, local_set_sha256, counts, rules)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12::jsonb,$13::jsonb)
		RETURNING id::text, harness, role, project_id::text, repo_name, repo_sha256,
			merge_sha256, merge_version, local_set_sha256, counts, rules, created_at`,
		p.TenantID, upload.ProjectID, owner, agent, upload.Harness, upload.Role, upload.RepoSHA256, upload.RepoName,
		upload.Merged.SHA256, upload.Merged.Version, upload.Local.SetSHA256, string(counts), string(rulesJSON),
	).Scan(&view.ID, &view.Harness, &view.Role, &view.ProjectID, &view.RepoName, &view.RepoSHA256,
		&view.MergeSHA256, &view.MergeVersion, &view.LocalSetSHA256, &countsRaw, &rulesRaw, &view.CreatedAt)
	if err != nil {
		return nil, comparisonWriteErr(err)
	}
	if err = json.Unmarshal(countsRaw, &view.Counts); err != nil {
		return nil, err
	}
	if err = json.Unmarshal(rulesRaw, &view.Rules); err != nil {
		return nil, err
	}
	if view.Rules == nil {
		view.Rules = []comparisonRule{}
	}
	return view, nil
}

func comparisonWriteErr(err error) error {
	var pe *pgconn.PgError
	if !errors.As(err, &pe) {
		return err
	}
	switch pe.Code {
	case "23503":
		return pgx.ErrNoRows
	case "23514":
		return fail(400, "invalid_request", "comparison rejected")
	default:
		return err
	}
}

func comparisonProject(r *http.Request, tx pgx.Tx, projectID string) error {
	var found bool
	err := tx.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM nodes n JOIN node_kinds k ON k.tenant_id=n.tenant_id AND k.id=n.kind_id WHERE n.id=$1 AND k.slug='project' AND n.deleted_at IS NULL)`, projectID).Scan(&found)
	if err != nil {
		return err
	}
	if !found {
		return pgx.ErrNoRows
	}
	return nil
}

func comparisonAgent(r *http.Request, tx pgx.Tx, p tenant.Principal, owner, requested string) (string, error) {
	if p.Kind == tenant.Agent {
		if requested != "" && requested != p.ID {
			return "", authz.ErrForbidden
		}
		return p.ID, nil
	}
	if requested == "" {
		return "", nil
	}
	if !workorders.UUID(requested) || requested != strings.ToLower(requested) {
		return "", fail(400, "invalid_request", "comparison rejected")
	}
	var controlled bool
	err := tx.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM agent_keys k JOIN principals a ON a.tenant_id=k.tenant_id AND a.id=k.principal_id WHERE k.tenant_id=$1 AND k.principal_id=$2 AND k.created_by_principal_id=$3 AND k.revoked_at IS NULL AND (k.expires_at IS NULL OR k.expires_at>clock_timestamp()) AND a.kind='agent' AND a.status='active')`, p.TenantID, requested, owner).Scan(&controlled)
	if err != nil {
		return "", err
	}
	if !controlled {
		return "", authz.ErrForbidden
	}
	return requested, nil
}

func scanComparison(rows pgx.Rows) (comparisonView, error) {
	var view comparisonView
	var countsRaw, rulesRaw []byte
	err := rows.Scan(&view.ID, &view.Harness, &view.Role, &view.ProjectID, &view.RepoName, &view.RepoSHA256,
		&view.MergeSHA256, &view.MergeVersion, &view.LocalSetSHA256, &countsRaw, &rulesRaw, &view.CreatedAt)
	if err != nil {
		return comparisonView{}, err
	}
	if err = json.Unmarshal(countsRaw, &view.Counts); err != nil {
		return comparisonView{}, err
	}
	if err = json.Unmarshal(rulesRaw, &view.Rules); err != nil {
		return comparisonView{}, err
	}
	if view.Rules == nil {
		view.Rules = []comparisonRule{}
	}
	return view, nil
}

func parseComparison(raw []byte) (comparisonUpload, error) {
	if err := rejectComparisonKeys(raw); err != nil {
		return comparisonUpload{}, fail(400, "invalid_request", "comparison rejected")
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var upload comparisonUpload
	if err := dec.Decode(&upload); err != nil {
		return comparisonUpload{}, fail(400, "invalid_request", "comparison rejected")
	}
	if err := dec.Decode(&struct{}{}); err != io.EOF {
		return comparisonUpload{}, fail(400, "invalid_request", "comparison rejected")
	}
	if err := validateComparison(upload); err != nil {
		return comparisonUpload{}, err
	}
	return upload, nil
}

func rejectComparisonKeys(raw []byte) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if err := walkComparison(dec); err != nil {
		return err
	}
	if err := dec.Decode(&struct{}{}); err != io.EOF {
		return err
	}
	return nil
}

func walkComparison(dec *json.Decoder) error {
	tok, err := dec.Token()
	if err != nil {
		return err
	}
	delim, ok := tok.(json.Delim)
	if !ok {
		return nil
	}
	switch delim {
	case '{':
		for dec.More() {
			keyTok, err := dec.Token()
			if err != nil {
				return err
			}
			key, ok := keyTok.(string)
			if !ok || forbiddenComparisonKey(key) {
				return io.ErrUnexpectedEOF
			}
			if err = walkComparison(dec); err != nil {
				return err
			}
		}
		end, err := dec.Token()
		if err != nil || end != json.Delim('}') {
			return io.ErrUnexpectedEOF
		}
	case '[':
		for dec.More() {
			if err = walkComparison(dec); err != nil {
				return err
			}
		}
		end, err := dec.Token()
		if err != nil || end != json.Delim(']') {
			return io.ErrUnexpectedEOF
		}
	default:
		return io.ErrUnexpectedEOF
	}
	return nil
}

func forbiddenComparisonKey(key string) bool {
	switch key {
	case "text", "body", "why", "details", "path", "content":
		return true
	default:
		return false
	}
}

func validateComparison(upload comparisonUpload) error {
	reject := fail(400, "invalid_request", "comparison rejected")
	if upload.Harness != "claude-code" && upload.Harness != "codex" {
		return reject
	}
	if !slices.Contains(Roles, upload.Role) {
		return reject
	}
	if !workorders.UUID(upload.ProjectID) || upload.ProjectID != strings.ToLower(upload.ProjectID) {
		return reject
	}
	if upload.AgentID != "" && (!workorders.UUID(upload.AgentID) || upload.AgentID != strings.ToLower(upload.AgentID)) {
		return reject
	}
	if !comparisonHex.MatchString(upload.RepoSHA256) || !comparisonHex.MatchString(upload.Merged.SHA256) || !comparisonHex.MatchString(upload.Local.SetSHA256) {
		return reject
	}
	if upload.RepoName != "" && !comparisonName.MatchString(upload.RepoName) {
		return reject
	}
	if !comparisonVersion(upload.Merged.Version) {
		return reject
	}
	if len(upload.Rules) > maxComparisonRules || upload.Counts.Files < 0 || upload.Counts.Files > 64 {
		return reject
	}
	if upload.Local.RuleCount < 0 || upload.Local.RuleCount > maxComparisonRules || upload.Merged.RuleCount < 0 || upload.Merged.RuleCount > maxComparisonRules {
		return reject
	}
	var both, onlyLocal, onlyMerged, differs int
	seen := map[string]bool{}
	for _, rule := range upload.Rules {
		if !comparisonID.MatchString(rule.Identity) || seen[rule.Identity] {
			return reject
		}
		seen[rule.Identity] = true
		switch rule.Status {
		case "both":
			both++
			if !comparisonPair(rule, true, true) || len(rule.Changed) != 0 {
				return reject
			}
		case "only_local":
			onlyLocal++
			if !comparisonPair(rule, true, false) || len(rule.Changed) != 0 {
				return reject
			}
		case "only_merged":
			onlyMerged++
			if !comparisonPair(rule, false, true) || len(rule.Changed) != 0 {
				return reject
			}
		case "differs":
			differs++
			if !comparisonChanged(rule) {
				return reject
			}
		default:
			return reject
		}
	}
	localSide := both + onlyLocal + differs
	mergedSide := both + onlyMerged + differs
	if both != upload.Counts.Both || onlyLocal != upload.Counts.OnlyLocal || onlyMerged != upload.Counts.OnlyMerged || differs != upload.Counts.Differs {
		return reject
	}
	if upload.Merged.RuleCount != mergedSide || upload.Local.RuleCount < localSide {
		return reject
	}
	return nil
}

func comparisonPair(rule comparisonRule, local, merged bool) bool {
	if local != comparisonHex.MatchString(rule.LocalTextSHA256) {
		return false
	}
	if merged != comparisonHex.MatchString(rule.MergedTextSHA256) {
		return false
	}
	if !local && rule.LocalTextSHA256 != "" {
		return false
	}
	if !merged && rule.MergedTextSHA256 != "" {
		return false
	}
	return true
}

func comparisonChanged(rule comparisonRule) bool {
	if len(rule.Changed) == 0 || len(rule.Changed) > 4 {
		return false
	}
	seen := map[string]bool{}
	conflict := false
	for _, field := range rule.Changed {
		if seen[field] || !comparisonChange[field] {
			return false
		}
		seen[field] = true
		if field == "local_conflict" {
			conflict = true
		}
	}
	if conflict {
		return len(rule.Changed) == 1 && rule.LocalTextSHA256 == "" && rule.MergedTextSHA256 == ""
	}
	return comparisonPair(rule, true, true)
}

func comparisonVersion(s string) bool {
	if s == "" || len(s) > 128 {
		return false
	}
	for _, r := range s {
		if r < 0x20 || r == 0x7f || r == '\u2028' || r == '\u2029' {
			return false
		}
	}
	return true
}
