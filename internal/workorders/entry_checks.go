// SPDX-License-Identifier: AGPL-3.0-only
package workorders

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/reviewgate"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/inspr-at/paimos/internal/ticketbenefits"
	"github.com/jackc/pgx/v5"
)

type EntryRule struct {
	ID              string   `json:"id"`
	Label           string   `json:"label,omitempty"`
	LabelDE         string   `json:"label_de,omitempty"`
	Condition       string   `json:"condition,omitempty"`
	Kinds           []string `json:"kinds,omitempty"`
	Labels          []string `json:"labels,omitempty"`
	OwnerID         *string  `json:"owner_id,omitempty"`
	FallbackOwnerID *string  `json:"fallback_owner_id,omitempty"`
}
type EntryDefinition struct {
	ID         string                 `json:"id"`
	Revision   time.Time              `json:"revision"`
	Source     string                 `json:"source"`
	CopiedFrom *time.Time             `json:"copied_from_revision"`
	Checks     map[string][]EntryRule `json:"checks"`
}
type EntryPerson struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}
type EntryResult struct {
	EntryRule
	Applicable bool         `json:"applicable"`
	Passed     bool         `json:"passed"`
	Reason     string       `json:"reason"`
	Owner      *EntryPerson `json:"owner"`
	CheckedBy  *EntryPerson `json:"checked_by"`
	CheckedAt  *time.Time   `json:"checked_at"`
	CanTick    bool         `json:"can_tick"`
}
type EntryBlock struct {
	ToStatus string        `json:"to_status"`
	Passed   int           `json:"passed"`
	Total    int           `json:"total"`
	Checks   []EntryResult `json:"checks"`
}
type EntryRecord struct {
	EntryBlock
	At                 time.Time   `json:"at"`
	By                 EntryPerson `json:"by"`
	OverrideReason     *string     `json:"override_reason"`
	DefinitionID       string      `json:"definition_id"`
	DefinitionRevision time.Time   `json:"definition_revision"`
}
type EntryRefusal struct {
	ToStatus string        `json:"to_status"`
	Failed   []EntryResult `json:"failed"`
	At       time.Time     `json:"at"`
	By       EntryPerson   `json:"by"`
	Count    int           `json:"count"`
}
type EntryChecks struct {
	NodeID        string           `json:"node_id"`
	Revision      time.Time        `json:"revision"`
	Definition    *EntryDefinition `json:"definition"`
	Blocks        []EntryBlock     `json:"blocks"`
	Entries       []EntryRecord    `json:"entries"`
	LatestRefusal *EntryRefusal    `json:"latest_refusal"`
}
type EntryFailure struct {
	ToStatus        string        `json:"to_status"`
	Failed          []EntryResult `json:"failed"`
	OverrideAllowed bool          `json:"override_allowed"`
}

func (e *EntryFailure) Error() string     { return "entry_checks_failed" }
func (e *EntryFailure) HTTPStatus() int   { return 409 }
func (e *EntryFailure) ErrorCode() string { return "entry_checks_failed" }
func (e *EntryFailure) ErrorDetails() map[string]any {
	return map[string]any{"to_status": e.ToStatus, "failed": e.Failed, "override_allowed": e.OverrideAllowed}
}

type entryCatalogItem struct{ en, de, condition string }

var entryCatalog = map[string]entryCatalogItem{
	"estimate_set":           {"Estimate set", "Schätzung gesetzt", "all"},
	"acceptance_present":     {"Acceptance criteria written", "Abnahmekriterien beschrieben", "all"},
	"design_attached":        {"Approved design attached", "Freigegebenes Design angehängt", "ui"},
	"blocker_named":          {"Blocker named", "Blocker benannt", "blocked"},
	"no_active_build":        {"No other build in flight", "Kein anderer Build aktiv", "all"},
	"ci_green_latest_main":   {"Full CI green on latest main", "Volle CI grün auf aktuellem main", "code"},
	"review_ok_cross_family": {"Cross-family review ok", "Review durch andere Modellfamilie ok", "code"},
	"visual_ok":              {"Visual check ok", "Visuelle Prüfung ok", "ui"},
	"pills_benefits_set":     {"Pills and benefits set", "Pills und Nutzen beschrieben", "release_notes"},
	"release_copy_set":       {"Release text set (EN + DE)", "Release-Text gesetzt (EN + DE)", "release_notes"},
	"lesson_recorded":        {"Lesson recorded", "Lehre festgehalten", "all"},
	"merged":                 {"Merged to main", "In main gemergt", "code"},
	"released":               {"Shipped in a release", "In einem Release ausgeliefert", "releases"},
}
var manualEntryID = regexp.MustCompile(`^manual:[a-z][a-z0-9-]{0,71}$`)

func ValidateEntryRules(checks map[string][]EntryRule) error {
	if checks == nil || len(checks) > 3 {
		return Fail(400, "checks must contain Ready, QA or Done blocks")
	}
	encoded, err := json.Marshal(checks)
	if err != nil || len(encoded) > 65536 {
		return Fail(400, "definition checks exceed 64 KiB")
	}
	seen := map[string]bool{}
	for state, rules := range checks {
		if !slices.Contains([]string{"in_progress", "qa", "done"}, state) || len(rules) > 32 {
			return Fail(400, "invalid entry check block")
		}
		for _, r := range rules {
			_, builtin := entryCatalog[r.ID]
			if !builtin && (!manualEntryID.MatchString(r.ID) || strings.TrimSpace(r.Label) == "") || seen[r.ID] {
				return Fail(400, "unknown or duplicate entry check")
			}
			seen[r.ID] = true
			if len(r.ID) > 80 || !utf8.ValidString(r.Label+r.LabelDE) || utf8.RuneCountInString(r.Label) > 200 || utf8.RuneCountInString(r.LabelDE) > 200 || len(r.Kinds) > 16 || len(r.Labels) > 16 || !slices.Contains([]string{"", "all", "ui", "code", "blocked", "release_notes", "releases"}, r.Condition) || r.OwnerID != nil && (!manualEntryID.MatchString(r.ID) || !UUID(*r.OwnerID)) || r.FallbackOwnerID != nil && (!manualEntryID.MatchString(r.ID) || !UUID(*r.FallbackOwnerID) || r.OwnerID != nil) {
				return Fail(400, "invalid bounded entry check rule")
			}
			for _, s := range append(slices.Clone(r.Kinds), r.Labels...) {
				if strings.TrimSpace(s) == "" || len(s) > 100 || !utf8.ValidString(s) {
					return Fail(400, "invalid check condition")
				}
			}
		}
	}
	return nil
}

// Definitions are ordinary nodes at exactly two scopes. The project copy is
// selected even when empty; malformed/duplicate definitions fail closed.
// Inheriting reads use the built-in workspace policy until a definition is
// saved. Non-inheriting reads return only saved nodes for revision-checked writes.
func EntryDefinitionTx(ctx context.Context, tx pgx.Tx, project *string, inherit bool) (*EntryDefinition, error) {
	load := func(scope *string) (*EntryDefinition, error) {
		rows, err := tx.Query(ctx, `SELECT n.id::text,n.updated_at,n.fields FROM nodes n JOIN node_kinds k ON k.tenant_id=n.tenant_id AND k.id=n.kind_id WHERE k.slug='definition' AND n.deleted_at IS NULL AND n.parent_id IS NOT DISTINCT FROM $1::uuid ORDER BY n.id LIMIT 2`, scope)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		var out *EntryDefinition
		for rows.Next() {
			if out != nil {
				return nil, Fail(409, "multiple definitions at this scope")
			}
			d := EntryDefinition{Source: "workspace"}
			if scope != nil {
				d.Source = "project"
			}
			var raw []byte
			if err = rows.Scan(&d.ID, &d.Revision, &raw); err != nil {
				return nil, err
			}
			if len(raw) > 65536 {
				return nil, Fail(409, "definition exceeds size limit")
			}
			var fields struct {
				Checks     map[string][]EntryRule `json:"checks"`
				CopiedFrom *time.Time             `json:"copied_from_revision"`
			}
			if json.Unmarshal(raw, &fields) != nil {
				return nil, Fail(409, "invalid definition")
			}
			if err = ValidateEntryRules(fields.Checks); err != nil {
				return nil, Fail(409, "invalid definition checks")
			}
			d.Checks, d.CopiedFrom = fields.Checks, fields.CopiedFrom
			out = &d
		}
		return out, rows.Err()
	}
	d, err := load(project)
	if err != nil || d != nil || !inherit {
		return d, err
	}
	if project != nil {
		d, err = load(nil)
		if err != nil || d != nil {
			return d, err
		}
	}
	// A stable synthetic identity/revision makes entry audit records identify
	// this policy without writing a node during reads or weakening existing
	// tenants' guards at rollout. Explicit empty definitions opt out as usual.
	return &EntryDefinition{
		ID:       "00000000-0000-0000-0000-000000000000",
		Revision: time.Date(2026, 10, 15, 0, 0, 0, 0, time.UTC),
		Source:   "workspace",
		Checks: map[string][]EntryRule{
			"in_progress": {{ID: "estimate_set"}, {ID: "acceptance_present"}, {ID: "blocker_named"}},
			"done":        {{ID: "release_copy_set"}, {ID: "lesson_recorded"}},
		},
	}, nil
}

type EntryTicket struct {
	ID, Kind, State, Title, Body string
	ProjectID                    *string
	Fields                       map[string]any
	Revision                     time.Time
	DefinitionID                 string
	DefinitionRevision           time.Time
	NamedBlocker                 bool
}

func EntryTicketTx(ctx context.Context, tx pgx.Tx, id string) (EntryTicket, error) {
	var t EntryTicket
	var raw []byte
	err := tx.QueryRow(ctx, `SELECT n.id::text,k.slug,n.state,n.title,n.body,n.project_id::text,n.fields,n.updated_at,EXISTS(SELECT 1 FROM node_relations r JOIN nodes b ON b.tenant_id=r.tenant_id AND b.id=r.source_node_id WHERE r.target_node_id=n.id AND r.type='blocks' AND b.deleted_at IS NULL) FROM nodes n JOIN node_kinds k ON k.tenant_id=n.tenant_id AND k.id=n.kind_id WHERE n.id=$1 AND n.deleted_at IS NULL`, id).Scan(&t.ID, &t.Kind, &t.State, &t.Title, &t.Body, &t.ProjectID, &raw, &t.Revision, &t.NamedBlocker)
	t.Fields = EntryFields(raw)
	return t, err
}
func EntryStatusTx(ctx context.Context, tx pgx.Tx, kindID, state string) (string, error) {
	var category string
	err := tx.QueryRow(ctx, `SELECT aeon_work_status_category($2,field_schema) FROM node_kinds WHERE id=$1`, kindID, state).Scan(&category)
	if err != nil {
		return "", err
	}
	// Parent rollup keeps QA inside in progress. Entry checks use the QA gate.
	if EntryState(state) == "qa" {
		return "qa", nil
	}
	if category == "delivered" || category == "accepted" {
		category = "done"
	}
	if EntryProgress(category) {
		category = "in_progress"
	}
	return category, nil
}

func entryLabels(fields map[string]any) []string {
	out := []string{}
	for _, key := range []string{"tags", "labels"} {
		switch v := fields[key].(type) {
		case string:
			out = append(out, strings.ToLower(v))
		case []any:
			for _, x := range v {
				switch y := x.(type) {
				case string:
					out = append(out, strings.ToLower(y))
				case map[string]any:
					if s, ok := y["name"].(string); ok {
						out = append(out, strings.ToLower(s))
					}
				}
			}
		}
	}
	return out
}
func entryApplies(r EntryRule, t EntryTicket, usesReleases bool) bool {
	labels := entryLabels(t.Fields)
	if len(r.Kinds) > 0 && !slices.Contains(r.Kinds, t.Kind) {
		return false
	}
	for _, s := range r.Labels {
		if !slices.Contains(labels, strings.ToLower(s)) {
			return false
		}
	}
	condition := r.Condition
	if condition == "" {
		condition = entryCatalog[r.ID].condition
	}
	switch condition {
	case "ui":
		return slices.Contains(labels, "ui") || t.Fields["area"] == "frontend"
	case "code":
		return t.Fields["no_release_needed"] != true && !slices.Contains(labels, "docs") && !slices.Contains(labels, "content")
	case "blocked":
		return EntryState(t.State) == "blocked"
	case "release_notes":
		return !ticketbenefits.OmitFromReleaseNotes(mustEntryJSON(t.Fields))
	case "releases":
		return usesReleases && t.Fields["no_release_needed"] != true
	}
	return true
}
func mustEntryJSON(v any) json.RawMessage { raw, _ := json.Marshal(v); return raw }

func EntryOwnerTx(ctx context.Context, tx pgx.Tx, p tenant.Principal, t EntryTicket, r EntryRule) (*EntryPerson, error) {
	preferred, fallback := r.OwnerID, r.FallbackOwnerID
	if preferred == nil {
		if assignee, ok := t.Fields["assignee"].(string); ok && UUID(assignee) {
			preferred = &assignee
		}
	}
	if t.ID != "" && t.DefinitionID != "" {
		var owner string
		err := tx.QueryRow(ctx, `SELECT after->>'owner_id' FROM events WHERE node_id=$1 AND type='node.person_step_reassigned' AND after->>'check_id'=$2 AND after->>'definition_id'=$3 AND (after->>'definition_revision')::timestamptz=$4 ORDER BY id DESC LIMIT 1`, t.ID, r.ID, t.DefinitionID, t.DefinitionRevision).Scan(&owner)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return nil, err
		}
		if err == nil {
			preferred = &owner
			fallback = nil
		}
	}
	var person EntryPerson
	err := tx.QueryRow(ctx, `SELECT id::text,name FROM principals WHERE id=aeon_person_step_owner($1,$2,$3)`, t.ProjectID, preferred, fallback).Scan(&person.ID, &person.Name)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &person, nil
}

// EvaluateEntryTx runs under the same tenant/tree fence as status and queue
// writers. No client boolean is delivery, review, visual or manual evidence.
func EvaluateEntryTx(ctx context.Context, tx pgx.Tx, p tenant.Principal, t EntryTicket, d *EntryDefinition, status string) (EntryBlock, error) {
	out := EntryBlock{ToStatus: status, Checks: []EntryResult{}}
	if d == nil || !slices.Contains([]string{"work", "ticket", "task"}, t.Kind) {
		return out, nil
	}
	var usesReleases bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM nodes n JOIN node_kinds k ON k.tenant_id=n.tenant_id AND k.id=n.kind_id WHERE n.project_id=$1 AND k.slug='release' AND n.deleted_at IS NULL)`, t.ProjectID).Scan(&usesReleases); err != nil {
		return out, err
	}
	t.DefinitionID, t.DefinitionRevision = d.ID, d.Revision
	for _, rule := range d.Checks[status] {
		r := EntryResult{EntryRule: rule, Reason: ""}
		if c, ok := entryCatalog[rule.ID]; ok {
			r.Label, r.LabelDE = c.en, c.de
		}
		r.Applicable = entryApplies(rule, t, usesReleases)
		if rule.ID == "lesson_recorded" {
			labels := entryLabels(t.Fields)
			r.Applicable = r.Applicable && status == "done" && (t.Fields["non_obvious_fix"] == true || slices.ContainsFunc(labels, func(label string) bool {
				return slices.Contains([]string{"incident", "regression", "non_obvious_fix"}, EntryState(label))
			}))
		}
		if !r.Applicable {
			r.Reason = "Not applicable"
			out.Checks = append(out.Checks, r)
			continue
		}
		out.Total++
		var err error
		switch rule.ID {
		case "estimate_set":
			r.Passed = EntryHours(t.Fields) > 0
			r.Reason = "Set an estimate between 0 and 200 hours"
		case "acceptance_present":
			r.Passed = len(EntryCriteria(t.Fields)) > 0
			r.Reason = "Write what must be true when the work is done"
		case "blocker_named":
			r.Passed = t.NamedBlocker
			for _, key := range []string{"blocker", "blocked_by"} {
				if v, ok := t.Fields[key].(string); ok && strings.TrimSpace(v) != "" && !strings.EqualFold(strings.TrimSpace(v), "unnamed") {
					r.Passed = true
				}
			}
			r.Reason = "Name the ticket or reason blocking the work"
		case "release_copy_set", "pills_benefits_set":
			r.Passed = len(ticketbenefits.Issues(mustEntryJSON(t.Fields))) == 0
			r.Reason = strings.Join(ticketbenefits.Issues(mustEntryJSON(t.Fields)), "; ")
		case "lesson_recorded":
			r.Passed, err = entryLessonTx(ctx, tx, t)
			r.Reason = "Record the lesson in project Knowledge or add a No lesson: <reason> note"
		case "no_active_build":
			err = RequireBuildAdmissionTx(ctx, tx, p, t.ID, "", nil)
			var conflict *BuildConflict
			if errors.As(err, &conflict) {
				r.Reason = conflict.Error()
				err = nil
			} else if err == nil {
				r.Passed = true
			}
		case "merged":
			err = tx.QueryRow(ctx, `SELECT coalesce((SELECT d.state='merged' OR d.observation->>'merged'='true' FROM delivery_items d JOIN delivery_metric_sources s ON s.tenant_id=d.tenant_id AND s.project_id=d.project_id AND s.repository=d.repository WHERE d.ticket_node_id=$1 ORDER BY d.updated_at DESC,d.id DESC LIMIT 1),false)`, t.ID).Scan(&r.Passed)
			r.Reason = "Merge the current build to main"
		case "released":
			err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM journey_tickets t JOIN journey_releases r ON r.tenant_id=t.tenant_id AND r.release_node_id=t.release_node_id WHERE t.ticket_node_id=$1 AND r.project_node_id=$2 AND r.state IN ('released','superseded'))`, t.ID, t.ProjectID).Scan(&r.Passed)
			r.Reason = "Publish the release that includes this work"
		case "ci_green_latest_main":
			r.Passed, err = entryCITx(ctx, tx, t)
			r.Reason = "Run the required CI checks on the current build against latest main"
		case "review_ok_cross_family":
			var order string
			err = tx.QueryRow(ctx, `SELECT work_order_id::text FROM work_order_reviews WHERE ticket_node_id=$1 ORDER BY created_at DESC,work_order_id DESC LIMIT 1`, t.ID).Scan(&order)
			if errors.Is(err, pgx.ErrNoRows) {
				err = nil
			} else if err == nil {
				r.Passed, err = reviewgate.VerifiedLatestTx(ctx, tx, order, t.ID)
			}
			if err == nil && !r.Passed {
				r.Passed, err = reviewgate.CoordinatorOKTx(ctx, tx, t.ID)
			}
			r.Reason = "Record a passing review from another model family for the current build"
		case "visual_ok":
			r.Passed, err = reviewgate.AuthenticatedOKTx(ctx, tx, t.ID, "visual")
			r.Reason = "Record an authenticated visual verdict with the separate visual approval grant"
		case "design_attached":
			r.Passed, err = entryEvidenceTx(ctx, tx, t, rule.ID)
			r.Reason = "A project admin records approval for the attached design"
		default:
			r.Owner, err = EntryOwnerTx(ctx, tx, p, t, rule)
			if err != nil {
				return out, err
			}
			r.Reason = "A person must tick this"
			if r.Owner != nil {
				r.Reason += " · waiting for " + r.Owner.Name
			}
			project := ""
			if t.ProjectID != nil {
				project = *t.ProjectID
			}
			r.CanTick = p.Kind == tenant.Person && r.Owner != nil && (r.Owner.ID == p.ID || authz.RequireProjectAdminTx(ctx, tx, p, project) == nil)
			var raw []byte
			err = tx.QueryRow(ctx, `SELECT after FROM events WHERE node_id=$1 AND type IN ('entry_check.ticked','node.entry_check_ticked') AND after->>'check_id'=$2 AND after->>'definition_id'=$3 AND (after->>'definition_revision')::timestamptz=$4 ORDER BY id DESC LIMIT 1`, t.ID, rule.ID, d.ID, d.Revision).Scan(&raw)
			if errors.Is(err, pgx.ErrNoRows) {
				err = nil
			} else if err == nil {
				var mark struct {
					Checked bool         `json:"checked"`
					By      *EntryPerson `json:"by"`
					At      *time.Time   `json:"at"`
				}
				err = json.Unmarshal(raw, &mark)
				r.Passed, r.CheckedBy, r.CheckedAt = mark.Checked, mark.By, mark.At
			}
		}
		if err != nil {
			return out, err
		}
		if err == nil && !r.Passed && (rule.ID == "review_ok_cross_family" || rule.ID == "visual_ok") {
			var provenance reviewgate.EvidenceProvenance
			provenance, err = reviewgate.EvidenceProvenanceTx(ctx, tx, t.ID)
			if rule.ID == "review_ok_cross_family" && provenance.ReviewLegacy || rule.ID == "visual_ok" && provenance.VisualLegacy {
				r.Reason += "; legacy_unverified: existing evidence has no authenticated run provenance"
			}
			if err != nil {
				return out, err
			}
		}
		if r.Passed {
			out.Passed++
			r.Reason = ""
		}
		out.Checks = append(out.Checks, r)
	}
	return out, nil
}

func entryLessonTx(ctx context.Context, tx pgx.Tx, t EntryTicket) (bool, error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	// Explicit notes can live in the ticket body/description or a current
	// comment. A bare prefix or a mention inside prose is not a reason.
	hasNote := func(text string) bool {
		for line := range strings.SplitSeq(text, "\n") {
			if reason, ok := strings.CutPrefix(strings.TrimSpace(line), "No lesson:"); ok && strings.TrimSpace(reason) != "" {
				return true
			}
		}
		return false
	}
	description, _ := t.Fields["description"].(string)
	if hasNote(t.Body) || hasNote(description) {
		return true, nil
	}
	var passed bool
	err := tx.QueryRow(ctx, `SELECT EXISTS (
	    SELECT 1 FROM method_learning_decisions d
	    JOIN nodes k ON k.tenant_id=d.tenant_id AND k.id=d.knowledge_id
	    WHERE d.project_id=$2 AND k.project_id=$2 AND k.deleted_at IS NULL
	      AND d.decision='accepted' AND btrim(d.line)<>''
	      AND strpos(E'\n'||k.body||E'\n', E'\n'||d.line||E'\n')>0
	      AND (d.source_key='n-'||$1::uuid::text OR EXISTS (
	          SELECT 1 FROM events c WHERE c.node_id=$1::uuid AND c.type='comment.created'
	            AND d.source_key='c-'||$1::uuid::text||'-'||c.id::text
	      ))
	) OR EXISTS (
	    SELECT 1 FROM events c
	    LEFT JOIN LATERAL (
	        SELECT e.after FROM events e WHERE e.node_id=c.node_id
	          AND e.type IN ('comment.updated','comment.deleted')
	          AND e.after->>'comment_id'=c.id::text ORDER BY e.id DESC LIMIT 1
	    ) latest ON true
	    WHERE c.node_id=$1::uuid AND c.type='comment.created'
	      AND coalesce(latest.after->>'deleted','false')<>'true'
	      AND coalesce(latest.after->>'body_markdown',c.after->>'body_markdown','')
	          ~ E'(^|\n)[ \\t]*No lesson:[ \\t]*[^ \\t\\r\\n]'
	)`, t.ID, t.ProjectID).Scan(&passed)
	return passed, err
}

func entryCITx(ctx context.Context, tx pgx.Tx, t EntryTicket) (bool, error) {
	var raw []byte
	var latestMain string
	err := tx.QueryRow(ctx, `SELECT d.observation,coalesce((SELECT head_sha FROM delivery_metric_runs r WHERE r.repository=d.repository AND r.head_branch='main' ORDER BY r.created_at DESC,r.run_id DESC,r.attempt DESC LIMIT 1),'') FROM delivery_items d JOIN delivery_metric_sources s ON s.tenant_id=d.tenant_id AND s.project_id=d.project_id AND s.repository=d.repository WHERE d.ticket_node_id=$1 ORDER BY d.updated_at DESC,d.id DESC LIMIT 1`, t.ID).Scan(&raw, &latestMain)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if len(raw) > 65536 {
		return false, nil
	}
	var observation struct {
		Head     string `json:"head_sha"`
		Base     string `json:"base_sha"`
		Checks   []struct{ Name, Status, Conclusion string }
		Settings struct {
			RequiredChecks []string `json:"required_checks"`
		} `json:"settings"`
	}
	if json.Unmarshal(raw, &observation) != nil || !reviewgate.ValidSHA(latestMain) || observation.Base != latestMain || !reviewgate.ValidSHA(observation.Head) || len(observation.Settings.RequiredChecks) == 0 || len(observation.Checks) > 1000 {
		return false, nil
	}
	for _, name := range observation.Settings.RequiredChecks {
		found := false
		for _, c := range observation.Checks {
			if c.Name == name {
				found = c.Status == "completed" && c.Conclusion == "success"
				break
			}
		}
		if !found {
			return false, nil
		}
	}
	return true, nil
}
func entryEvidenceTx(ctx context.Context, tx pgx.Tx, t EntryTicket, check string) (bool, error) {
	var ok bool
	err := tx.QueryRow(ctx, `SELECT coalesce((SELECT (e.after->>'ok')::boolean FROM events e WHERE e.node_id=$1 AND e.type='entry_check.evidence' AND e.after->>'check_id'=$2 AND CASE WHEN $2='design_attached' THEN EXISTS(SELECT 1 FROM attachments a WHERE a.node_id=$1 AND a.id::text=e.after->>'attachment_id' AND a.deleted_at IS NULL) ELSE e.after->>'head_sha'=(SELECT head_sha FROM delivery_items d WHERE d.ticket_node_id=$1 ORDER BY d.updated_at DESC,d.id DESC LIMIT 1) END ORDER BY e.id DESC LIMIT 1),false)`, t.ID, check).Scan(&ok)
	return ok, err
}

func RequireEntryTx(ctx context.Context, tx pgx.Tx, p tenant.Principal, t EntryTicket, status string, reason *string) (*EntryRecord, error) {
	if reason != nil && (strings.TrimSpace(*reason) == "" || len(*reason) > 2000 || utf8.RuneCountInString(*reason) > 500 || !utf8.ValidString(*reason)) {
		return nil, Fail(400, "a person override requires a reason of 1–500 characters")
	}
	d, err := EntryDefinitionTx(ctx, tx, t.ProjectID, true)
	if err != nil {
		return nil, err
	}
	b, err := EvaluateEntryTx(ctx, tx, p, t, d, status)
	if err != nil || d == nil || b.Total == 0 {
		return nil, err
	}
	failed := []EntryResult{}
	for _, r := range b.Checks {
		if r.Applicable && !r.Passed {
			failed = append(failed, r)
		}
	}
	if len(failed) > 0 && (p.Kind != tenant.Person || reason == nil) {
		return nil, &EntryFailure{ToStatus: status, Failed: failed, OverrideAllowed: p.Kind == tenant.Person}
	}
	var at time.Time
	if err = tx.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&at); err != nil {
		return nil, err
	}
	if len(failed) == 0 {
		reason = nil
	} else {
		s := strings.TrimSpace(*reason)
		reason = &s
	}
	return &EntryRecord{EntryBlock: b, At: at, By: EntryPerson{p.ID, p.Name}, OverrideReason: reason, DefinitionID: d.ID, DefinitionRevision: d.Revision}, nil
}

func EntrySummary(b EntryBlock) string { return fmt.Sprintf("%s %d/%d", b.ToStatus, b.Passed, b.Total) }

func EntryState(s string) string {
	return strings.ReplaceAll(strings.ReplaceAll(strings.ToLower(strings.TrimSpace(s)), " ", "_"), "-", "_")
}
func EntryProgress(s string) bool {
	return slices.Contains([]string{"in_progress", "progress", "active"}, EntryState(s))
}
func EntryHours(fields map[string]any) float64 {
	h, ok := fields["estimate_hours"].(float64)
	if !ok || math.IsNaN(h) || math.IsInf(h, 0) || h <= 0 || h > 200 {
		return 0
	}
	return h
}
func EntryFields(raw []byte) map[string]any {
	out := map[string]any{}
	_ = json.Unmarshal(raw, &out)
	return out
}
func EntryCriteria(fields map[string]any) []string {
	out := []string{}
	switch v := fields["acceptance_criteria"].(type) {
	case string:
		for _, line := range strings.Split(v, "\n") {
			if line = strings.TrimSpace(line); strings.Trim(line, "- *#[]xX\t") != "" {
				out = append(out, line)
			}
		}
	case []any:
		for _, value := range v {
			if text, ok := value.(string); ok && strings.TrimSpace(text) != "" {
				out = append(out, strings.TrimSpace(text))
			}
		}
	}
	return out
}
