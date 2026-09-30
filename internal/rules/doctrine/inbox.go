// SPDX-License-Identifier: AGPL-3.0-only

package doctrine

// The doctrine inbox (AEON-444). An agent proposes a rule change from its
// loop; the proposal waits until a person sends it to git as a PR (the
// AEON-319 path), edits it first, or dismisses it with a reason. Nothing
// reaches GitHub before a person acts. The waiting text is the one piece of
// doctrine prose held outside the pinned cache: doctrine_proposal_drafts keeps
// it, bounded, until the proposal leaves the inbox. The transaction that
// records the PR, the dismissal or the promotion deletes it, and a sweep on
// every inbox read and each hour deletes any text left behind and expires
// proposals that waited longer than inboxDraftTTL. Git remains the source of
// truth; the proposal row keeps references only.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"

	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/events"
	"github.com/inspr-at/paimos/internal/systemactor"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/inspr-at/paimos/internal/workorders"
)

const (
	maxInboxPending       = 50
	maxInboxPerProposer   = 10
	maxDismissReasonBytes = 500
	// A proposal no person acted on for this long is dismissed as expired.
	inboxDraftTTL = 30 * 24 * time.Hour
	inboxExpired  = "expired"
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
	notified     bool
}

// InboxHeadline is what the dot and the toast need, without the text.
// Notified is true once this person claimed the proposal's toast.
type InboxHeadline struct {
	ID        string    `json:"id"`
	Label     string    `json:"label"`
	CreatedAt time.Time `json:"created_at"`
	Notified  bool      `json:"notified"`
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

// retireDraft deletes the text of an inbox proposal that left the inbox, in
// the transaction that records why it left.
func retireDraft(ctx context.Context, tx pgx.Tx, p Proposal) error {
	if !p.Inbox || p.State == "pending" && p.PRNumber == 0 {
		return nil
	}
	return deleteDraft(ctx, tx, p.ID)
}

// tldrDigest names a proposed TL;DR without keeping its text, so promotion
// can check that the sidecar landed too.
func tldrDigest(en, de string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(en) + "\x00" + strings.TrimSpace(de)))
	return hex.EncodeToString(sum[:])
}

// sweepInbox bounds draft retention. It deletes the text and the
// notification claims of every proposal that left the inbox, and dismisses as expired, through the system actor,
// any proposal that waited longer than inboxDraftTTL.
func sweepInbox(ctx context.Context, tx pgx.Tx, tenantID string, now time.Time) error {
	for _, table := range []string{"doctrine_proposal_drafts", "doctrine_inbox_notified"} {
		if _, err := tx.Exec(ctx, `DELETE FROM `+table+` d USING doctrine_proposals p
			WHERE p.tenant_id=d.tenant_id AND p.id=d.proposal_id
			AND (COALESCE(p.data->>'state','proposed')<>'pending' OR COALESCE((p.data->>'pr_number')::int,0)>0)`); err != nil {
			return err
		}
	}
	rows, err := tx.Query(ctx, `SELECT `+proposalColumns+` FROM doctrine_proposals
		WHERE data->>'inbox'='true' AND data->>'state'='pending' AND created_at<$1
		ORDER BY id LIMIT 50 FOR UPDATE SKIP LOCKED`, now.Add(-inboxDraftTTL))
	if err != nil {
		return err
	}
	var stale []Proposal
	for rows.Next() {
		p, err := scanProposal(rows)
		if err != nil {
			rows.Close()
			return err
		}
		stale = append(stale, p)
	}
	rows.Close()
	if err := rows.Err(); err != nil || len(stale) == 0 {
		return err
	}
	system, err := systemactor.Ensure(ctx, tx, tenantID)
	if err != nil {
		return err
	}
	for _, p := range stale {
		if p.PRNumber > 0 || p.OperationID != "" && now.Before(p.OperationUntil) {
			continue
		}
		p.State, p.DismissedBy, p.DismissReason = "dismissed", system.ID, inboxExpired
		if err := saveProposal(ctx, tx, system, &p, "doctrine.inbox_dismissed"); err != nil {
			return err
		}
		if err := deleteDraft(ctx, tx, p.ID); err != nil {
			return err
		}
		body := fmt.Sprintf("Doctrine proposal expired: no person acted on it within %d days.\n\nRule `%s` in %s/%s.", int(inboxDraftTTL.Hours()/24), p.RuleKey, p.Repository, p.Path)
		if err := ticketComment(ctx, tx, system, p.TicketID, body); err != nil {
			return err
		}
	}
	return nil
}

// sweepInboxOnce runs sweepInbox in a savepoint; a failure is logged and never
// costs the surrounding read.
func sweepInboxOnce(ctx context.Context, tx pgx.Tx, tenantID string) error {
	sp, err := tx.Begin(ctx)
	if err != nil {
		return err
	}
	if err := sweepInbox(ctx, sp, tenantID, time.Now()); err != nil {
		_ = sp.Rollback(ctx)
		slog.Error("doctrine inbox sweep", "err", err)
		return nil
	}
	return sp.Commit(ctx)
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

// proposeToInbox records a waiting proposal from an agent loop or a person.
func (m *Module) proposeToInbox(r *http.Request, actor tenant.Principal) (any, error) {
	var in InboxInput
	if err := workorders.Decode(r, &in); err != nil {
		return nil, err
	}
	return m.recordInboxProposal(r.Context(), actor, in)
}

// recordInboxProposal records a waiting proposal. It runs the editor's checks
// (one rule, same section and marker, sidecar, credential and public-identity
// text, private quotation guard) but writes nothing to GitHub. Locked rules
// change only through a person. The outcome job proposes here too.
func (m *Module) recordInboxProposal(parent context.Context, actor tenant.Principal, in InboxInput) (Proposal, error) {
	if err := m.proposalAccess(actor); err != nil {
		return Proposal{}, err
	}
	if len(m.guardMaster) < 32 {
		return Proposal{}, fail(503, "guard_unavailable", missingGuardReason)
	}
	if !workorders.UUID(in.RequestID) || strings.ToLower(in.RequestID) != in.RequestID {
		return Proposal{}, fail(400, "invalid_request", "Name a canonical request UUID.")
	}
	if in.Ticket != "" && !ticketKeyPattern.MatchString(in.Ticket) {
		return Proposal{}, fail(400, "invalid_request", "ticket must be a key such as INSPR-491.")
	}
	if in.RuleSHA != "" && !digestPattern.MatchString(in.RuleSHA) {
		return Proposal{}, fail(400, "invalid_request", "rule_sha256 must be a SHA-256.")
	}
	ctx, cancel := context.WithTimeout(parent, fetchTimeout)
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
		return Proposal{}, err
	}
	if replay != nil {
		// The same request id replays its proposal; anything else is a conflict.
		if !replay.Inbox || replay.ProposedBy != actor.ID || replay.Path != in.Path || replay.RuleKey != in.RuleKey {
			return Proposal{}, fail(409, "request_conflict", "That request UUID belongs to another proposal.")
		}
		return *replay, nil
	}
	file, rule, index, ok := locateRule(Render(source.Repository, source.Commit, source.Visibility == "private", files), in.Path, in.RuleKey, "", -1)
	if !ok || file.Problem != "" {
		return Proposal{}, fail(409, "stale_rule", "That rule is not indexed at the pinned commit.")
	}
	if in.RuleSHA != "" && in.RuleSHA != rule.SHA256 {
		return Proposal{}, fail(409, "stale_rule", "The rule changed at the pin; reload it before proposing.")
	}
	pin := ProposalInput{RequestID: in.RequestID, SourceID: source.ID, Path: in.Path, RuleKey: in.RuleKey, RuleSHA: rule.SHA256, Source: in.Source, Explanation: in.Why}
	switch {
	case in.TLDR != nil:
		pin.TLDR.EN, pin.TLDR.DE = in.TLDR.EN, in.TLDR.DE
	case rule.TLDR != nil:
		pin.TLDR.EN, pin.TLDR.DE = rule.TLDR.EN, rule.TLDR.DE
	}
	if strings.TrimSpace(pin.TLDR.EN) == "" {
		return Proposal{}, fail(400, "invalid_request", "This rule has no TL;DR yet; propose one with the change.")
	}
	if err := pin.validate(); err != nil {
		return Proposal{}, err
	}
	_, old, next, err := editRuleViews(source, files, pin)
	if err != nil {
		return Proposal{}, err
	}
	if !humanActor(actor) && (old.Strength == "locked" || next.Strength == "locked") {
		return Proposal{}, fail(403, "locked_rule", "Locked rules change only through a person. Ask one to edit it under Doctrine.")
	}
	// A TL;DR alone is a change; adding one to a rule without one is too.
	if next.SHA256 == old.SHA256 && rule.TLDR != nil && tldrDigest(rule.TLDR.EN, rule.TLDR.DE) == tldrDigest(pin.TLDR.EN, pin.TLDR.DE) {
		return Proposal{}, fail(400, "no_change", "The proposal matches the pinned rule.")
	}
	if _, err := m.checkPrivateQuotes(ctx, actor, source, files, guard, pin.Source, pin.TLDR.EN, pin.TLDR.DE, pin.Explanation); err != nil {
		return Proposal{}, err
	}
	p := Proposal{
		ID: in.RequestID, SourceID: source.ID, Repository: source.Repository, Path: in.Path, RuleKey: in.RuleKey,
		State: "pending", ProposedBy: actor.ID, Inbox: true, BaseRuleSHA: old.SHA256, ProposedSHA: next.SHA256, Ticket: in.Ticket,
		ProposedTLDR: tldrDigest(pin.TLDR.EN, pin.TLDR.DE), RuleSet: rule.Set, RuleIndex: index,
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
		return Proposal{}, err
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
			out.Items = append(out.Items, InboxHeadline{ID: item.ID, Label: item.Label, CreatedAt: item.CreatedAt, Notified: item.notified})
		}
	}
	return out, err
}

// claimInboxNotice records that a person was told about a waiting proposal.
// Only the first claim per person and proposal is true, across tabs, devices
// and reloads: one toast per proposal. A proposal that left the inbox claims
// nothing; the claim waits for a dismissal or a submission in flight.
func (m *Module) claimInboxNotice(r *http.Request, actor tenant.Principal) (any, error) {
	if !humanActor(actor) {
		return nil, fail(403, "forbidden", "Only a person is notified of doctrine proposals.")
	}
	id := r.PathValue("proposalId")
	if !workorders.UUID(id) || strings.ToLower(id) != id {
		return nil, fail(400, "invalid_request", "invalid proposal UUID")
	}
	out := struct {
		Claimed bool `json:"claimed"`
	}{}
	err := m.tx(r.Context(), actor, "rules.read", func(tx pgx.Tx) error {
		result, err := tx.Exec(r.Context(), `INSERT INTO doctrine_inbox_notified(tenant_id,principal_id,proposal_id)
			SELECT p.tenant_id,$2,p.id FROM doctrine_proposals p
			WHERE p.id=$1 AND p.data->>'inbox'='true' AND p.data->>'state'='pending' AND COALESCE((p.data->>'pr_number')::int,0)=0
			FOR SHARE OF p
			ON CONFLICT DO NOTHING`, id, actor.ID)
		out.Claimed = err == nil && result.RowsAffected() == 1
		return err
	})
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
		if err := sweepInboxOnce(ctx, tx, actor.TenantID); err != nil {
			return err
		}
		query := `SELECT ` + prefixed("p", proposalColumns) + `,d.source,d.tldr_en,d.tldr_de,d.why,COALESCE(pr.name,''),COALESCE(pr.kind,''),n.proposal_id IS NOT NULL
			FROM doctrine_proposals p
			LEFT JOIN doctrine_proposal_drafts d ON d.tenant_id=p.tenant_id AND d.proposal_id=p.id
			LEFT JOIN principals pr ON pr.tenant_id=p.tenant_id AND pr.id=p.proposed_by
			LEFT JOIN doctrine_inbox_notified n ON n.tenant_id=p.tenant_id AND n.proposal_id=p.id AND n.principal_id=$1
			WHERE p.data->>'inbox'='true' AND `
		args := []any{actor.ID}
		if humanActor(actor) {
			query += `p.data->>'state'='pending' ORDER BY p.created_at DESC LIMIT 100`
		} else {
			query += `p.proposed_by=$1 ORDER BY p.created_at DESC LIMIT 50`
		}
		rows, err := tx.Query(ctx, query, args...)
		if err != nil {
			return err
		}
		drafts := map[string]draft{}
		for rows.Next() {
			var item InboxItem
			var src, en, de, why *string
			p, err := scanProposal(rows, &src, &en, &de, &why, &item.Proposer, &item.ProposerKind, &item.notified)
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
				case !full:
					// The summary polls: headlines need no rendered doctrine.
					loaded.source = s
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
		current.ProposedTLDR = tldrDigest(in.TLDR.EN, in.TLDR.DE)
		current.ApprovedFileSHA = hashText(changed[in.Path])
		return saveProposal(ctx, tx, actor, &current, "doctrine.inbox_submitted")
	})
	if err != nil {
		return nil, err
	}
	if m.beforeInboxLease != nil {
		m.beforeInboxLease(id)
	}
	// runProposal records the PR and deletes the draft in one transaction.
	return m.runProposal(ctx, actor, "rules.write", id, func(ctx context.Context, p *Proposal) (string, error) {
		if p.PRNumber > 0 {
			return "", nil
		}
		// A dismissal, expiry or promotion that won the race before the lease
		// stands; nothing reaches GitHub.
		if p.State != "pending" {
			return "", fail(409, "not_pending", "This proposal is no longer waiting in the inbox.")
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
// any route: the proposed rule's exact bytes and its proposed TL;DR are both
// indexed at this commit. The linked ticket gets a comment. It runs inside the
// index transaction.
func promoteLanded(ctx context.Context, tx pgx.Tx, actor tenant.Principal, s Source, files []File) error {
	rows, err := tx.Query(ctx, `SELECT `+proposalColumns+` FROM doctrine_proposals
		WHERE source_id=$1 AND data->>'inbox'='true' AND COALESCE(data->>'state','proposed') NOT IN ('dismissed','promoted')
		AND COALESCE(data->>'proposed_rule_sha256','')<>'' AND COALESCE(data->>'proposed_tldr_sha256','')<>'' ORDER BY created_at LIMIT 200`, s.ID)
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
			if r.TLDR != nil {
				landed[v.Path+"\x00"+r.SHA256+"\x00"+tldrDigest(r.TLDR.EN, r.TLDR.DE)] = true
			}
		}
	}
	var system *tenant.Principal
	for _, p := range open {
		if !landed[p.Path+"\x00"+p.ProposedSHA+"\x00"+p.ProposedTLDR] || p.OperationID != "" && time.Now().Before(p.OperationUntil) {
			continue
		}
		p.State, p.PromotedCommit = "promoted", s.Commit
		// Agents report the file the pin holds, with any other change in it.
		for _, f := range files {
			if f.Path == p.Path {
				p.ApprovedFileSHA = hashText(string(f.Content))
			}
		}
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

// reconcilePromotions re-checks open inbox proposals against the pinned
// index of their source, one source or (with "") every source. Indexing skips
// a proposal under a lease; this runs when a lease ends and in the hourly
// sweep, so a pin that landed meanwhile still promotes it.
func reconcilePromotions(ctx context.Context, tx pgx.Tx, tenantID, sourceID string) error {
	rows, err := tx.Query(ctx, `SELECT DISTINCT source_id::text FROM doctrine_proposals
		WHERE data->>'inbox'='true' AND COALESCE(data->>'state','proposed') NOT IN ('dismissed','promoted')
		AND ($1='' OR source_id::text=$1)`, sourceID)
	if err != nil {
		return err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil || len(ids) == 0 {
		return err
	}
	system, err := systemactor.Ensure(ctx, tx, tenantID)
	if err != nil {
		return err
	}
	for _, id := range ids {
		s, err := getSource(ctx, tx, id, false)
		if errors.Is(err, errNoSource) {
			continue
		}
		if err != nil {
			return err
		}
		files, err := cachedFiles(ctx, tx, s)
		if err != nil {
			return err
		}
		if len(files) == 0 {
			continue
		}
		if err := promoteLanded(ctx, tx, system, s, files); err != nil {
			return err
		}
	}
	return nil
}

// reconcileInbox runs reconcilePromotions outside a request. A failure is
// logged; the hourly sweep retries.
func (m *Module) reconcileInbox(parent context.Context, tenantID, sourceID string) {
	ctx, cancel := context.WithTimeout(db.AllProjects(context.WithoutCancel(parent), "doctrine inbox promotion"), 30*time.Second)
	defer cancel()
	err := db.InTenant(ctx, m.pool, tenantID, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT set_config('lock_timeout','3s',true)`); err != nil {
			return err
		}
		return reconcilePromotions(ctx, tx, tenantID, sourceID)
	})
	if err != nil {
		slog.Error("doctrine inbox promotion check incomplete; the hourly sweep retries")
	}
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
