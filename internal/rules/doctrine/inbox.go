// SPDX-License-Identifier: AGPL-3.0-only

package doctrine

// The doctrine inbox (AEON-444). An agent proposes a rule change from its
// loop; the proposal waits until a person sends it to git as a PR (the
// AEON-319 path), edits it first, or dismisses it with a reason. Nothing
// reaches GitHub before a person acts. The waiting text is the one piece of
// doctrine prose held outside the pinned cache: doctrine_proposal_drafts keeps
// it, bounded, until the proposal leaves the inbox, and then deletes it. Git
// remains the source of truth; the proposal row keeps references only.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"

	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/events"
	"github.com/inspr-at/paimos/internal/systemactor"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/inspr-at/paimos/internal/workorders"
)

const (
	maxInboxPending       = 50
	maxInboxPerProposer   = 10
	maxDismissReasonBytes = 500
)

var ticketKeyPattern = regexp.MustCompile(`^[A-Z][A-Z0-9]{0,15}-[1-9][0-9]{0,8}$`)

// InboxInput is one proposal from an agent loop. The source is named by id or
// by repository ("inspr-modules" or "inspr-at/inspr-modules"). rule_sha256 is
// optional: without it the proposal is against the rule at the current pin.
// Without tldr the rule keeps its current TL;DR.
type InboxInput struct {
	RequestID  string     `json:"request_id"`
	SourceID   string     `json:"source_id,omitempty"`
	Repository string     `json:"repository,omitempty"`
	Path       string     `json:"path"`
	RuleKey    string     `json:"rule_key"`
	RuleSHA    string     `json:"rule_sha256,omitempty"`
	Source     string     `json:"source"`
	TLDR       *inboxTLDR `json:"tldr,omitempty"`
	Why        string     `json:"why"`
	Ticket     string     `json:"ticket,omitempty"`
}

type inboxTLDR struct {
	EN string `json:"en"`
	DE string `json:"de,omitempty"`
}

// InboxSubmit sends a waiting proposal to git. Any of source, tldr and why
// make it "edit then propose"; rule_sha256 then names the pinned rule the
// person edited against.
type InboxSubmit struct {
	Source  *string    `json:"source,omitempty"`
	TLDR    *inboxTLDR `json:"tldr,omitempty"`
	Why     *string    `json:"why,omitempty"`
	RuleSHA string     `json:"rule_sha256,omitempty"`
}

// DiffPart is one run of a word diff: eq, del (only in the base) or ins.
type DiffPart struct {
	Op   string `json:"op"`
	Text string `json:"text"`
}

// InboxItem is a proposal as the inbox shows it: the rule, a word diff
// against the pinned rule, the proposer, the ticket and the why.
type InboxItem struct {
	Proposal
	Heading      string     `json:"heading"`
	Strength     string     `json:"strength,omitempty"`
	Label        string     `json:"label"`
	Base         string     `json:"base"`
	BaseSHA      string     `json:"base_sha256,omitempty"`
	Proposed     string     `json:"proposed,omitempty"`
	TLDR         *TLDR      `json:"tldr,omitempty"`
	Why          string     `json:"why,omitempty"`
	Diff         []DiffPart `json:"diff"`
	Outdated     bool       `json:"outdated,omitempty"`
	Proposer     string     `json:"proposer"`
	ProposerKind string     `json:"proposer_kind"`
	TicketHref   string     `json:"ticket_href,omitempty"`
}

// InboxHeadline is what the dot and the toast need, without the text.
type InboxHeadline struct {
	ID        string    `json:"id"`
	Label     string    `json:"label"`
	CreatedAt time.Time `json:"created_at"`
}

type draft struct {
	Source, EN, DE, Why string
}

func humanActor(p tenant.Principal) bool { return p.Kind == tenant.Person && p.KeyCreatorID == "" }

func loadDraft(ctx context.Context, tx pgx.Tx, id string) (draft, error) {
	var d draft
	err := tx.QueryRow(ctx, `SELECT source,tldr_en,tldr_de,why FROM doctrine_proposal_drafts WHERE proposal_id=$1`, id).Scan(&d.Source, &d.EN, &d.DE, &d.Why)
	if errors.Is(err, pgx.ErrNoRows) {
		return d, fail(409, "not_pending", "This proposal is no longer waiting in the inbox.")
	}
	return d, err
}

func deleteDraft(ctx context.Context, tx pgx.Tx, id string) error {
	_, err := tx.Exec(ctx, `DELETE FROM doctrine_proposal_drafts WHERE proposal_id=$1`, id)
	return err
}

// inboxSource resolves the source an agent names by id or repository.
func inboxSource(ctx context.Context, tx pgx.Tx, in InboxInput) (Source, error) {
	if (in.SourceID == "") == (in.Repository == "") {
		return Source{}, fail(400, "invalid_request", "Name the doctrine source by source_id or repository.")
	}
	if in.SourceID != "" {
		if !workorders.UUID(in.SourceID) || strings.ToLower(in.SourceID) != in.SourceID {
			return Source{}, fail(400, "invalid_request", "invalid source UUID")
		}
		return getSource(ctx, tx, in.SourceID, false)
	}
	sources, err := listSources(ctx, tx)
	if err != nil {
		return Source{}, err
	}
	for _, s := range sources {
		if s.Repository == in.Repository || !strings.Contains(in.Repository, "/") && s.Repository == "inspr-at/"+in.Repository {
			return s, nil
		}
	}
	return Source{}, errNoSource
}

func findRule(views []FileView, path, key string) (FileView, RuleView, bool) {
	file, rule, _, ok := locateRule(views, path, key, "", -1)
	return file, rule, ok
}

// locateRule finds a rule by key. A text-derived key changes with its text, so
// a waiting proposal also finds its rule by section and position, and an
// edit names the pinned rule by its digest.
func locateRule(views []FileView, path, key, set string, index int) (FileView, RuleView, int, bool) {
	for _, v := range views {
		if v.Path != path {
			continue
		}
		for i, r := range v.Rules {
			if r.Key == key {
				return v, r, i, true
			}
		}
		if index >= 0 && index < len(v.Rules) && v.Rules[index].Set == set {
			return v, v.Rules[index], index, true
		}
		return v, RuleView{}, -1, false
	}
	return FileView{}, RuleView{}, -1, false
}

func ruleByDigest(views []FileView, path, digest string) (RuleView, bool) {
	for _, v := range views {
		if v.Path == path {
			for _, r := range v.Rules {
				if r.SHA256 == digest {
					return r, true
				}
			}
		}
	}
	return RuleView{}, false
}

func ruleHeading(file FileView, rule RuleView) string {
	for _, s := range file.Sets {
		if s.Set == rule.Set {
			return setTitle(s.Title)
		}
	}
	return setTitle(rule.HeadingPath)
}

func setTitle(title string) string {
	if cut := strings.LastIndex(title, " / "); cut >= 0 {
		return title[cut+3:]
	}
	return title
}

// proposeToInbox records a waiting proposal. It runs the editor's checks
// (one rule, same section and marker, sidecar, credential and public-identity
// text, private quotation guard) but writes nothing to GitHub. Locked rules
// change only through a person.
func (m *Module) proposeToInbox(r *http.Request, actor tenant.Principal) (any, error) {
	if err := m.proposalAccess(actor); err != nil {
		return nil, err
	}
	if len(m.guardMaster) < 32 {
		return nil, fail(503, "guard_unavailable", missingGuardReason)
	}
	var in InboxInput
	if err := workorders.Decode(r, &in); err != nil {
		return nil, err
	}
	if !workorders.UUID(in.RequestID) || strings.ToLower(in.RequestID) != in.RequestID {
		return nil, fail(400, "invalid_request", "Name a canonical request UUID.")
	}
	if in.Ticket != "" && !ticketKeyPattern.MatchString(in.Ticket) {
		return nil, fail(400, "invalid_request", "ticket must be a key such as INSPR-491.")
	}
	if in.RuleSHA != "" && !digestPattern.MatchString(in.RuleSHA) {
		return nil, fail(400, "invalid_request", "rule_sha256 must be a SHA-256.")
	}
	ctx, cancel := context.WithTimeout(r.Context(), fetchTimeout)
	defer cancel()
	var source Source
	var files []File
	var guard *guardCorpus
	var replay *Proposal
	err := m.tx(ctx, actor, "rules.write", func(tx pgx.Tx) error {
		p, err := getProposal(ctx, tx, in.RequestID)
		if err == nil {
			replay = &p
			return nil
		}
		var f *failure
		if !errors.As(err, &f) || f.Status != 404 {
			return err
		}
		if source, err = inboxSource(ctx, tx, in); err != nil {
			return err
		}
		if !writableSource(source) {
			return fail(422, "unsupported_repository", "Only the public and private INSPR doctrine repositories accept proposals; their visibility must match.")
		}
		if source.CredentialRef != "" {
			if err := m.credentials.authorize(source.CredentialRef, actor.TenantID, source.Repository); err != nil {
				return err
			}
		}
		if files, err = cachedFiles(ctx, tx, source); err != nil {
			return err
		}
		if source.Repository == publicRepository {
			guard, err = m.privateGuard(ctx, tx, actor)
		}
		return err
	})
	if err != nil {
		return nil, err
	}
	if replay != nil {
		// The same request id replays its proposal; anything else is a conflict.
		if !replay.Inbox || replay.ProposedBy != actor.ID || replay.Path != in.Path || replay.RuleKey != in.RuleKey {
			return nil, fail(409, "request_conflict", "That request UUID belongs to another proposal.")
		}
		return *replay, nil
	}
	file, rule, index, ok := locateRule(Render(source.Repository, source.Commit, source.Visibility == "private", files), in.Path, in.RuleKey, "", -1)
	if !ok || file.Problem != "" {
		return nil, fail(409, "stale_rule", "That rule is not indexed at the pinned commit.")
	}
	if in.RuleSHA != "" && in.RuleSHA != rule.SHA256 {
		return nil, fail(409, "stale_rule", "The rule changed at the pin; reload it before proposing.")
	}
	pin := ProposalInput{RequestID: in.RequestID, SourceID: source.ID, Path: in.Path, RuleKey: in.RuleKey, RuleSHA: rule.SHA256, Source: in.Source, Explanation: in.Why}
	switch {
	case in.TLDR != nil:
		pin.TLDR.EN, pin.TLDR.DE = in.TLDR.EN, in.TLDR.DE
	case rule.TLDR != nil:
		pin.TLDR.EN, pin.TLDR.DE = rule.TLDR.EN, rule.TLDR.DE
	}
	if strings.TrimSpace(pin.TLDR.EN) == "" {
		return nil, fail(400, "invalid_request", "This rule has no TL;DR yet; propose one with the change.")
	}
	if err := pin.validate(); err != nil {
		return nil, err
	}
	_, old, next, err := editRuleViews(source, files, pin)
	if err != nil {
		return nil, err
	}
	if !humanActor(actor) && (old.Strength == "locked" || next.Strength == "locked") {
		return nil, fail(403, "locked_rule", "Locked rules change only through a person. Ask one to edit it under Doctrine.")
	}
	if next.SHA256 == old.SHA256 && (rule.TLDR == nil || rule.TLDR.EN == strings.TrimSpace(pin.TLDR.EN) && rule.TLDR.DE == strings.TrimSpace(pin.TLDR.DE)) {
		return nil, fail(400, "no_change", "The proposal matches the pinned rule.")
	}
	if _, err := m.checkPrivateQuotes(ctx, actor, source, files, guard, pin.Source, pin.TLDR.EN, pin.TLDR.DE, pin.Explanation); err != nil {
		return nil, err
	}
	p := Proposal{
		ID: in.RequestID, SourceID: source.ID, Repository: source.Repository, Path: in.Path, RuleKey: in.RuleKey,
		State: "pending", ProposedBy: actor.ID, Inbox: true, BaseRuleSHA: old.SHA256, ProposedSHA: next.SHA256, Ticket: in.Ticket,
		RuleSet: rule.Set, RuleIndex: index,
	}
	err = m.tx(ctx, actor, "rules.write", func(tx pgx.Tx) error {
		current, err := getSource(ctx, tx, source.ID, true)
		if err != nil {
			return err
		}
		if current.Commit != source.Commit {
			return fail(409, "stale_source", "The doctrine pin moved; reload the rule and propose again.")
		}
		var total, mine int
		var sameID, sameSHA string
		if err := tx.QueryRow(ctx, `SELECT count(*), count(*) FILTER (WHERE proposed_by=$1),
			COALESCE(max(id::text) FILTER (WHERE proposed_by=$1 AND source_id=$2 AND path=$3 AND rule_key=$4),''),
			COALESCE(max(data->>'proposed_rule_sha256') FILTER (WHERE proposed_by=$1 AND source_id=$2 AND path=$3 AND rule_key=$4),'')
			FROM doctrine_proposals WHERE data->>'inbox'='true' AND data->>'state'='pending'`, actor.ID, source.ID, in.Path, in.RuleKey).Scan(&total, &mine, &sameID, &sameSHA); err != nil {
			return err
		}
		if sameID != "" {
			if sameSHA == next.SHA256 {
				existing, err := getProposal(ctx, tx, sameID)
				replay = &existing
				return err
			}
			return fail(409, "rule_proposal_open", "You already proposed a change to this rule. A person has to act on it first.")
		}
		if total >= maxInboxPending || mine >= maxInboxPerProposer {
			return fail(409, "inbox_full", "The doctrine inbox is full. A person has to act on waiting proposals first.")
		}
		if in.Ticket != "" {
			var projectID string
			err := tx.QueryRow(ctx, `SELECT id::text, COALESCE(project_id::text,'') FROM nodes WHERE key=$1 AND deleted_at IS NULL`, in.Ticket).Scan(&p.TicketID, &projectID)
			if errors.Is(err, pgx.ErrNoRows) {
				return fail(404, "ticket_not_found", "That ticket is not in this workspace.")
			}
			if err != nil {
				return err
			}
			if err := authz.RequireTx(ctx, tx, actor, "nodes.read", authz.Scope{ProjectID: projectID}); err != nil {
				return err
			}
		}
		raw, err := jsonData(p)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO doctrine_proposals(tenant_id,id,source_id,repository,path,rule_key,input_digest,base_commit,proposed_by,data) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`,
			actor.TenantID, p.ID, p.SourceID, p.Repository, p.Path, p.RuleKey, inputDigest(pin), source.Commit, actor.ID, raw); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO doctrine_proposal_drafts(tenant_id,proposal_id,source,tldr_en,tldr_de,why) VALUES($1,$2,$3,$4,$5,$6)`,
			actor.TenantID, p.ID, pin.Source, strings.TrimSpace(pin.TLDR.EN), strings.TrimSpace(pin.TLDR.DE), strings.TrimSpace(pin.Explanation)); err != nil {
			return err
		}
		_, err = events.Append(ctx, tx, actor, events.Change{Type: "doctrine.inbox_proposed", After: map[string]any{
			"proposal_id": p.ID, "repository": p.Repository, "path": p.Path, "rule_key": p.RuleKey, "proposed_by": actor.ID, "ticket": p.Ticket,
		}})
		return err
	})
	if err != nil {
		return nil, err
	}
	if replay != nil {
		return *replay, nil
	}
	return scanStored(ctx, m, actor, p.ID)
}

func jsonData(p Proposal) ([]byte, error) {
	return json.Marshal(proposalData{Proposal: p})
}

func scanStored(ctx context.Context, m *Module, actor tenant.Principal, id string) (Proposal, error) {
	var p Proposal
	err := m.tx(ctx, actor, "rules.write", func(tx pgx.Tx) error {
		var err error
		p, err = scanProposal(tx.QueryRow(ctx, `SELECT `+proposalColumns+` FROM doctrine_proposals WHERE id=$1`, id))
		return err
	})
	return p, err
}

// listInbox shows a person every waiting proposal, and an agent (or a key)
// its own proposals with their outcome, so a dismissal reason reaches it.
func (m *Module) listInbox(r *http.Request, actor tenant.Principal) (any, error) {
	items, pending, err := m.inboxItems(r.Context(), actor, true)
	out := struct {
		Pending int         `json:"pending"`
		Items   []InboxItem `json:"items"`
	}{Pending: pending, Items: items}
	return out, err
}

// inboxSummary is the count and the headlines, for the dot and the toast.
func (m *Module) inboxSummary(r *http.Request, actor tenant.Principal) (any, error) {
	items, pending, err := m.inboxItems(r.Context(), actor, false)
	out := struct {
		Pending int             `json:"pending"`
		Items   []InboxHeadline `json:"items"`
	}{Pending: pending, Items: []InboxHeadline{}}
	for _, item := range items {
		if item.State == "pending" {
			out.Items = append(out.Items, InboxHeadline{ID: item.ID, Label: item.Label, CreatedAt: item.CreatedAt})
		}
	}
	return out, err
}

func (m *Module) inboxItems(ctx context.Context, actor tenant.Principal, full bool) ([]InboxItem, int, error) {
	items := []InboxItem{}
	pending := 0
	type sourceFiles struct {
		source Source
		views  []FileView
		denied bool
	}
	err := m.tx(ctx, actor, "rules.read", func(tx pgx.Tx) error {
		query := `SELECT ` + prefixed("p", proposalColumns) + `,d.source,d.tldr_en,d.tldr_de,d.why,COALESCE(pr.name,''),COALESCE(pr.kind,'')
			FROM doctrine_proposals p
			LEFT JOIN doctrine_proposal_drafts d ON d.tenant_id=p.tenant_id AND d.proposal_id=p.id
			LEFT JOIN principals pr ON pr.tenant_id=p.tenant_id AND pr.id=p.proposed_by
			WHERE p.data->>'inbox'='true' AND `
		args := []any{}
		if humanActor(actor) {
			query += `p.data->>'state'='pending' ORDER BY p.created_at DESC LIMIT 100`
		} else {
			query += `p.proposed_by=$1 ORDER BY p.created_at DESC LIMIT 50`
			args = append(args, actor.ID)
		}
		rows, err := tx.Query(ctx, query, args...)
		if err != nil {
			return err
		}
		drafts := map[string]draft{}
		for rows.Next() {
			var item InboxItem
			var src, en, de, why *string
			p, err := scanProposal(rows, &src, &en, &de, &why, &item.Proposer, &item.ProposerKind)
			if err != nil {
				rows.Close()
				return err
			}
			item.Proposal = p
			if src != nil {
				drafts[p.ID] = draft{Source: *src, EN: *en, DE: *de, Why: *why}
			}
			items = append(items, item)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		sources := map[string]*sourceFiles{}
		for i := range items {
			item := &items[i]
			if item.State == "pending" {
				pending++
			}
			loaded, ok := sources[item.SourceID]
			if !ok {
				loaded = &sourceFiles{}
				sources[item.SourceID] = loaded
				s, err := getSource(ctx, tx, item.SourceID, false)
				switch {
				case errors.Is(err, errNoSource):
					loaded.denied = true
				case err != nil:
					return err
				case s.CredentialRef != "" && m.credentials.authorize(s.CredentialRef, actor.TenantID, s.Repository) != nil:
					// A revoked grant hides the private text, as in the layer.
					loaded.denied = true
				default:
					files, err := cachedFiles(ctx, tx, s)
					if err != nil {
						return err
					}
					loaded.source, loaded.views = s, Render(s.Repository, s.Commit, s.Visibility == "private", files)
				}
			}
			index := -1
			if item.RuleSet != "" {
				index = item.RuleIndex
			}
			file, rule, _, found := locateRule(loaded.views, item.Path, item.RuleKey, item.RuleSet, index)
			item.Heading = item.RuleKey
			if found {
				item.Heading, item.Strength = ruleHeading(file, rule), rule.Strength
			}
			d, hasDraft := drafts[item.ID]
			item.Label = item.Heading
			if hasDraft && !loaded.denied && d.EN != "" {
				item.Label = d.EN
			}
			if !full || loaded.denied {
				item.Diff = []DiffPart{}
				continue
			}
			if found {
				item.Base, item.BaseSHA = sourceBody(rule.Source), rule.SHA256
			}
			item.Outdated = item.State == "pending" && (!found || rule.SHA256 != item.BaseRuleSHA)
			if hasDraft {
				item.Proposed, item.Why = sourceBody(d.Source), d.Why
				item.TLDR = &TLDR{EN: d.EN, DE: d.DE}
				item.Diff = wordDiff(item.Base, item.Proposed)
			} else {
				item.Diff = []DiffPart{}
			}
			if item.TicketID != "" {
				var project string
				if tx.QueryRow(ctx, `SELECT p.key FROM nodes n JOIN nodes p ON p.tenant_id=n.tenant_id AND p.id=n.project_id WHERE n.id=$1 AND n.deleted_at IS NULL`, item.TicketID).Scan(&project) == nil && project != "" {
					item.TicketHref = "/p/" + url.PathEscape(project) + "/" + url.PathEscape(item.Ticket)
				}
			}
		}
		return nil
	})
	return items, pending, err
}

func prefixed(alias, columns string) string {
	parts := strings.Split(columns, ",")
	for i, part := range parts {
		parts[i] = alias + "." + part
	}
	return strings.Join(parts, ",")
}

// sourceBody drops the final line break, which a diff would show as a change.
func sourceBody(s string) string { return strings.TrimRight(s, "\r\n") }

// submitInbox sends a waiting proposal to git: the AEON-319 PR path with the
// same checks, run by the person who submits it. With edits it is "edit then
// propose": the person's text replaces the draft before publication.
func (m *Module) submitInbox(r *http.Request, actor tenant.Principal) (any, error) {
	if !humanActor(actor) {
		return nil, fail(403, "forbidden", "A person sends a doctrine proposal to git.")
	}
	if err := m.proposalAccess(actor); err != nil {
		return nil, err
	}
	if len(m.guardMaster) < 32 {
		return nil, fail(503, "guard_unavailable", missingGuardReason)
	}
	var body InboxSubmit
	if err := workorders.Decode(r, &body); err != nil {
		return nil, err
	}
	if body.RuleSHA != "" && !digestPattern.MatchString(body.RuleSHA) {
		return nil, fail(400, "invalid_request", "rule_sha256 must be a SHA-256.")
	}
	edited := body.Source != nil || body.TLDR != nil || body.Why != nil
	id := r.PathValue("proposalId")
	ctx, cancel := context.WithTimeout(r.Context(), fetchTimeout)
	defer cancel()
	var p Proposal
	var in ProposalInput
	var source Source
	var files []File
	var guard *guardCorpus
	err := m.tx(ctx, actor, "rules.write", func(tx pgx.Tx) error {
		var err error
		if p, err = getProposal(ctx, tx, id); err != nil {
			return err
		}
		if !p.Inbox {
			return fail(404, "not_found", "That proposal is not in the doctrine inbox.")
		}
		if p.PRNumber > 0 {
			return deleteDraft(ctx, tx, id)
		}
		if p.State != "pending" {
			return fail(409, "not_pending", "This proposal is no longer waiting in the inbox.")
		}
		d, err := loadDraft(ctx, tx, id)
		if err != nil {
			return err
		}
		in = ProposalInput{RequestID: id, SourceID: p.SourceID, Path: p.Path, RuleKey: p.RuleKey, RuleSHA: p.BaseRuleSHA, Source: d.Source, Explanation: d.Why}
		in.TLDR.EN, in.TLDR.DE = d.EN, d.DE
		if body.Source != nil {
			in.Source = *body.Source
		}
		if body.TLDR != nil {
			in.TLDR.EN, in.TLDR.DE = body.TLDR.EN, body.TLDR.DE
		}
		if body.Why != nil {
			in.Explanation = *body.Why
		}
		if source, err = getSource(ctx, tx, p.SourceID, false); err != nil {
			return err
		}
		if !writableSource(source) {
			return fail(422, "unsupported_repository", "Only the public and private INSPR doctrine repositories accept proposals; their visibility must match.")
		}
		if source.CredentialRef != "" {
			if err := m.credentials.authorize(source.CredentialRef, actor.TenantID, source.Repository); err != nil {
				return err
			}
		}
		if files, err = cachedFiles(ctx, tx, source); err != nil {
			return err
		}
		if source.Repository == publicRepository {
			guard, err = m.privateGuard(ctx, tx, actor)
		}
		return err
	})
	if err != nil {
		return nil, err
	}
	if p.PRNumber > 0 {
		return p, nil
	}
	if edited && body.RuleSHA != "" {
		// The person edited against the rule at the current pin.
		rule, ok := ruleByDigest(Render(source.Repository, source.Commit, source.Visibility == "private", files), in.Path, body.RuleSHA)
		if !ok {
			return nil, fail(409, "stale_rule", "The rule changed at the pin; reload it before proposing.")
		}
		in.RuleKey, in.RuleSHA = rule.Key, rule.SHA256
	}
	if err := in.validate(); err != nil {
		return nil, err
	}
	_, old, next, err := editRuleViews(source, files, in)
	if err != nil {
		if f := (*failure)(nil); errors.As(err, &f) && f.Code == "stale_rule" && !edited {
			return nil, fail(409, "stale_rule", "The rule changed since this proposal. Edit it against the current rule, then propose.")
		}
		return nil, err
	}
	changed, main, g, err := m.preparePublication(ctx, actor, source, files, guard, in)
	if err != nil {
		return nil, err
	}
	defer g.revoke()
	digest := inputDigest(in)
	err = m.tx(ctx, actor, "rules.write", func(tx pgx.Tx) error {
		current, err := getProposal(ctx, tx, id)
		if err != nil {
			return err
		}
		if current.PRNumber > 0 {
			return nil
		}
		if current.State != "pending" {
			return fail(409, "not_pending", "This proposal is no longer waiting in the inbox.")
		}
		if current.OperationID != "" && time.Now().Before(current.OperationUntil) {
			return fail(409, "proposal_busy", "This proposal is being sent. Refresh shortly.")
		}
		// A branch may already hold an earlier attempt. Only that exact text
		// and base continue it; the deterministic commit recovers the PR.
		if current.Branch != "" && (current.InputDigest != digest || current.BaseCommit != main) {
			return fail(409, "request_conflict", "An earlier attempt already left a branch with other text or another base. Submit the same text, or dismiss this proposal.")
		}
		if _, err := tx.Exec(ctx, `UPDATE doctrine_proposals SET input_digest=$2, base_commit=$3, rule_key=$4 WHERE id=$1`, id, digest, main, in.RuleKey); err != nil {
			return err
		}
		if edited {
			if _, err := tx.Exec(ctx, `UPDATE doctrine_proposal_drafts SET source=$2,tldr_en=$3,tldr_de=$4,why=$5 WHERE proposal_id=$1`,
				id, in.Source, strings.TrimSpace(in.TLDR.EN), strings.TrimSpace(in.TLDR.DE), strings.TrimSpace(in.Explanation)); err != nil {
				return err
			}
			current.EditedBy = actor.ID
		}
		current.SubmittedBy, current.BaseRuleSHA, current.ProposedSHA = actor.ID, old.SHA256, next.SHA256
		return saveProposal(ctx, tx, actor, &current, "doctrine.inbox_submitted")
	})
	if err != nil {
		return nil, err
	}
	result, err := m.runProposal(ctx, actor, "rules.write", id, func(ctx context.Context, p *Proposal) (string, error) {
		if p.PRNumber > 0 {
			return "", nil
		}
		if p.InputDigest != digest || p.BaseCommit != main {
			return "", fail(409, "request_conflict", "This proposal changed during the request. Reload it.")
		}
		if err := g.preparePR(ctx, p, changed, in.Explanation); err != nil {
			return "", err
		}
		p.State = "proposed"
		return "doctrine.proposed", nil
	})
	if err != nil {
		return nil, err
	}
	// Git holds the text now. A failed delete is retried by the next submit.
	_ = m.tx(ctx, actor, "rules.write", func(tx pgx.Tx) error { return deleteDraft(ctx, tx, id) })
	return result, nil
}

// dismissInbox closes a waiting proposal with a reason. The reason goes back
// to the proposer: it is on the proposal they list, and on the linked ticket.
func (m *Module) dismissInbox(r *http.Request, actor tenant.Principal) (any, error) {
	if !humanActor(actor) {
		return nil, fail(403, "forbidden", "A person dismisses a doctrine proposal.")
	}
	var body struct {
		Reason string `json:"reason"`
	}
	if err := workorders.Decode(r, &body); err != nil {
		return nil, err
	}
	reason := strings.TrimSpace(body.Reason)
	if reason == "" || len(reason) > maxDismissReasonBytes || !utf8.ValidString(reason) || strings.ContainsRune(reason, 0) {
		return nil, fail(400, "invalid_request", "Give a reason of at most 500 bytes; it goes back to the proposer.")
	}
	if credentialLeaks.MatchString(normalizeProposalText(reason)) {
		return nil, fail(422, "credential_text", "The reason contains credential-shaped text. Remove it.")
	}
	var p Proposal
	err := m.tx(r.Context(), actor, "rules.write", func(tx pgx.Tx) error {
		var err error
		if p, err = getProposal(r.Context(), tx, r.PathValue("proposalId")); err != nil {
			return err
		}
		if !p.Inbox {
			return fail(404, "not_found", "That proposal is not in the doctrine inbox.")
		}
		if p.State == "dismissed" {
			return nil
		}
		if p.State != "pending" || p.PRNumber > 0 {
			return fail(409, "not_pending", "This proposal is no longer waiting in the inbox.")
		}
		if p.OperationID != "" && time.Now().Before(p.OperationUntil) {
			return fail(409, "proposal_busy", "This proposal is being sent. Refresh shortly.")
		}
		p.State, p.DismissedBy, p.DismissReason = "dismissed", actor.ID, reason
		if err := saveProposal(r.Context(), tx, actor, &p, "doctrine.inbox_dismissed"); err != nil {
			return err
		}
		if err := deleteDraft(r.Context(), tx, p.ID); err != nil {
			return err
		}
		body := fmt.Sprintf("Doctrine proposal dismissed by %s: %s\n\nRule `%s` in %s/%s.", actor.Name, reason, p.RuleKey, p.Repository, p.Path)
		return ticketComment(r.Context(), tx, actor, p.TicketID, body)
	})
	return p, err
}

// ticketComment comments on the linked ticket when it still exists. A ticket
// the actor cannot comment on does not undo the decision it reports.
func ticketComment(ctx context.Context, tx pgx.Tx, actor tenant.Principal, ticketID, body string) error {
	if ticketID == "" {
		return nil
	}
	sp, err := tx.Begin(ctx)
	if err != nil {
		return err
	}
	var exists bool
	if err := sp.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM nodes WHERE id=$1 AND deleted_at IS NULL)`, ticketID).Scan(&exists); err != nil || !exists {
		_ = sp.Rollback(ctx)
		return nil
	}
	if _, err := events.Append(ctx, sp, actor, events.Change{NodeID: &ticketID, Type: "comment.created", After: map[string]any{"body_markdown": body}, Metadata: []byte(`{"job":"doctrine-inbox","reason":"proposal outcome"}`)}); err != nil {
		_ = sp.Rollback(ctx)
		return nil
	}
	return sp.Commit(ctx)
}

// promoteLanded closes inbox proposals whose change the new pin contains, by
// any route: the proposed rule's exact bytes are indexed at this commit. The
// linked ticket gets a comment. It runs inside the index transaction.
func promoteLanded(ctx context.Context, tx pgx.Tx, actor tenant.Principal, s Source, files []File) error {
	rows, err := tx.Query(ctx, `SELECT `+proposalColumns+` FROM doctrine_proposals
		WHERE source_id=$1 AND data->>'inbox'='true' AND COALESCE(data->>'state','proposed') NOT IN ('dismissed','promoted')
		AND COALESCE(data->>'proposed_rule_sha256','')<>'' ORDER BY created_at LIMIT 200`, s.ID)
	if err != nil {
		return err
	}
	var open []Proposal
	for rows.Next() {
		p, err := scanProposal(rows)
		if err != nil {
			rows.Close()
			return err
		}
		open = append(open, p)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	if len(open) == 0 {
		return nil
	}
	landed := map[string]bool{}
	for _, v := range Render(s.Repository, s.Commit, s.Visibility == "private", files) {
		for _, r := range v.Rules {
			landed[v.Path+"\x00"+r.SHA256] = true
		}
	}
	var system *tenant.Principal
	for _, p := range open {
		if !landed[p.Path+"\x00"+p.ProposedSHA] || p.OperationID != "" && time.Now().Before(p.OperationUntil) {
			continue
		}
		p.State, p.PromotedCommit = "promoted", s.Commit
		if err := saveProposal(ctx, tx, actor, &p, "doctrine.inbox_promoted"); err != nil {
			var f *failure
			if errors.As(err, &f) && f.Code == "proposal_changed" {
				continue
			}
			return err
		}
		if err := deleteDraft(ctx, tx, p.ID); err != nil {
			return err
		}
		if p.TicketID == "" {
			continue
		}
		if system == nil {
			actor, err := systemactor.Ensure(ctx, tx, actor.TenantID)
			if err != nil {
				return err
			}
			system = &actor
		}
		release := shortCommit(s.Commit)
		if s.Ref != "" {
			release = s.Ref + " (" + release + ")"
		}
		body := fmt.Sprintf("Doctrine change promoted: rule `%s` in %s/%s is in the pinned doctrine at %s.", p.RuleKey, p.Repository, p.Path, release)
		if p.PRURL != "" {
			body += " PR: " + p.PRURL
		}
		if err := ticketComment(ctx, tx, *system, p.TicketID, body); err != nil {
			return err
		}
	}
	return nil
}

func shortCommit(sha string) string {
	if len(sha) > 12 {
		return sha[:12]
	}
	return sha
}

// ---------- Word diff ----------

// maxDiffCells bounds the LCS table; larger inputs show a whole replacement.
const maxDiffCells = 4 << 20

func diffTokens(s string) []string {
	var out []string
	start := 0
	space := false
	for i, r := range s {
		isSpace := r == ' ' || r == '\t' || r == '\n' || r == '\r'
		if i > start && isSpace != space {
			out = append(out, s[start:i])
			start = i
		}
		space = isSpace
	}
	if start < len(s) {
		out = append(out, s[start:])
	}
	return out
}

// wordDiff is a word-level diff of two texts: runs of eq, del and ins whose
// concatenations restore both sides exactly.
func wordDiff(a, b string) []DiffPart {
	x, y := diffTokens(a), diffTokens(b)
	out := []DiffPart{}
	add := func(op, text string) {
		if text == "" {
			return
		}
		if n := len(out); n > 0 && out[n-1].Op == op {
			out[n-1].Text += text
			return
		}
		out = append(out, DiffPart{Op: op, Text: text})
	}
	if (len(x)+1)*(len(y)+1) > maxDiffCells {
		add("del", a)
		add("ins", b)
		return out
	}
	n, m := len(x), len(y)
	table := make([]int32, (n+1)*(m+1))
	at := func(i, j int) int { return i*(m+1) + j }
	for i := n - 1; i >= 0; i-- {
		for j := m - 1; j >= 0; j-- {
			if x[i] == y[j] {
				table[at(i, j)] = table[at(i+1, j+1)] + 1
			} else {
				table[at(i, j)] = max(table[at(i+1, j)], table[at(i, j+1)])
			}
		}
	}
	i, j := 0, 0
	for i < n && j < m {
		switch {
		case x[i] == y[j]:
			add("eq", x[i])
			i++
			j++
		case table[at(i+1, j)] >= table[at(i, j+1)]:
			add("del", x[i])
			i++
		default:
			add("ins", y[j])
			j++
		}
	}
	for ; i < n; i++ {
		add("del", x[i])
	}
	for ; j < m; j++ {
		add("ins", y[j])
	}
	return out
}
