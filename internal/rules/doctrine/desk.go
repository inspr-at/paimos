// SPDX-License-Identifier: AGPL-3.0-only
package doctrine

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/events"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/inspr-at/paimos/internal/workorders"
	"github.com/jackc/pgx/v5"
)

// InboxTLDR shares the existing inbox's bounded sidecar input with the desk.
type InboxTLDR = inboxTLDR

// DeskFailure exposes only the inbox's safe, authored refusal explanation.
func DeskFailure(err error) (string, string) {
	var f *failure
	if errors.As(err, &f) {
		return f.Code, f.Message
	}
	if errors.Is(err, authz.ErrForbidden) {
		return "doctrine_forbidden", "Doctrine proposals require workspace rules.write permission."
	}
	return "doctrine_failed", "The doctrine draft could not be saved; retry after checking the source."
}

// CheckDeskTargetTx checks the mapping and current authority without creating a
// draft or contacting git. The caller holds the tenant access fence.
func (m *Module) CheckDeskTargetTx(ctx context.Context, tx pgx.Tx, actor tenant.Principal, in InboxInput) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if _, err := tx.Exec(ctx, `SELECT set_config('lock_timeout','3s',true),set_config('statement_timeout','10s',true)`); err != nil {
		return err
	}
	if err := m.proposalAccess(actor); err != nil {
		return err
	}
	if err := authz.RequireTx(ctx, tx, actor, "rules.write", authz.Scope{}); err != nil {
		return err
	}
	if len(m.guardMaster) < 32 {
		return fail(503, "guard_unavailable", missingGuardReason)
	}
	if !digestPattern.MatchString(in.RuleSHA) || len(in.Path) > 300 || len(in.RuleKey) > 200 || in.RuleKey == "" {
		return fail(422, "doctrine_mapping_required", "An indexed source, rule and exact base digest are required.")
	}
	source, err := inboxSource(ctx, tx, in)
	if err != nil {
		return err
	}
	if !m.writableSource(source) {
		return fail(422, "unsupported_repository", "This source does not accept doctrine proposals.")
	}
	if source.CredentialRef != "" {
		if err := m.credentials.authorize(source.CredentialRef, actor.TenantID, source.Repository); err != nil {
			return err
		}
	}
	files, err := cachedFiles(ctx, tx, source)
	if err != nil {
		return err
	}
	file, rule, _, ok := locateRule(Render(source.Repository, source.Commit, source.Visibility == "private", files), in.Path, in.RuleKey, "", -1)
	if !ok || file.Problem != "" || rule.SHA256 != in.RuleSHA {
		return fail(409, "stale_rule", "The rule changed at the pin; reload its mapping before deciding.")
	}
	if rule.TLDR == nil && (in.TLDR == nil || strings.TrimSpace(in.TLDR.EN) == "") {
		return fail(422, "doctrine_tldr_required", "This rule needs an English TL;DR with the proposal.")
	}
	if m.repositories.IsPublic(source.Repository) {
		_, err = m.privateGuard(ctx, tx, actor)
	}
	return err
}

// RecordDeskDraftTx uses the AEON-444 writer. No publication action is invoked.
// All returned events must be appended after the caller's other row writes.
func (m *Module) RecordDeskDraftTx(ctx context.Context, tx pgx.Tx, actor tenant.Principal, in InboxInput, question, answer, replaces string) (string, []events.Change, error) {
	if !humanActor(actor) {
		return "", nil, authz.ErrForbidden
	}
	p, changes, err := m.recordInboxTx(ctx, tx, actor, in, question, answer, replaces)
	return p.ID, changes, err
}

// RetireDeskDraftTx supersedes only an untouched pending draft from this desk
// question. A publication attempt, person edit, PR or landed rule needs review.
func (m *Module) RetireDeskDraftTx(ctx context.Context, tx pgx.Tx, actor tenant.Principal, id, question, answer string) ([]events.Change, error) {
	if err := authz.RequireTx(ctx, tx, actor, "rules.write", authz.Scope{}); err != nil {
		return nil, err
	}
	p, err := getProposal(ctx, tx, id)
	if err != nil {
		return nil, err
	}
	if !p.Inbox || p.DeskQuestionID != question {
		return nil, fail(409, "effect_ownership_conflict", "That draft belongs to another effect.")
	}
	// A person's dismissal (including TTL expiry) already retired this draft.
	// Preserve their attribution, reason and history; the desk records lineage
	// on its own new effect instead of reopening or rewriting the proposal.
	if p.State == "dismissed" {
		return nil, nil
	}
	if p.State != "pending" || p.PRNumber > 0 || p.Branch != "" || p.SubmittedBy != "" || p.EditedBy != "" || p.MergeCommit != "" || p.PromotedCommit != "" || p.OperationID != "" && time.Now().Before(p.OperationUntil) {
		return nil, fail(409, "doctrine_review_required", "The earlier doctrine proposal was edited or published. Person review is required through the existing Doctrine path; its history remains unchanged.")
	}
	p.State, p.DismissedBy, p.DismissReason, p.SupersededBy = "dismissed", actor.ID, "Superseded by Decision Desk answer "+answer, answer
	if err := saveProposal(ctx, tx, actor, &p, ""); err != nil {
		return nil, err
	}
	if err := deleteDraft(ctx, tx, p.ID); err != nil {
		return nil, err
	}
	return []events.Change{{Type: "doctrine.inbox_dismissed", After: map[string]any{"proposal_id": p.ID, "question_id": question, "superseded_by": answer, "state": "dismissed"}}}, nil
}

func (m *Module) recordInboxTx(ctx context.Context, tx pgx.Tx, actor tenant.Principal, in InboxInput, deskQuestion, deskAnswer, replaces string) (Proposal, []events.Change, error) {
	if err := m.proposalAccess(actor); err != nil {
		return Proposal{}, nil, err
	}
	if len(m.guardMaster) < 32 {
		return Proposal{}, nil, fail(503, "guard_unavailable", missingGuardReason)
	}
	if !workorders.UUID(in.RequestID) || strings.ToLower(in.RequestID) != in.RequestID {
		return Proposal{}, nil, fail(400, "invalid_request", "Name a canonical request UUID.")
	}
	if in.Ticket != "" && !ticketKeyPattern.MatchString(in.Ticket) {
		return Proposal{}, nil, fail(400, "invalid_request", "ticket must be a key such as INSPR-491.")
	}
	if in.RuleSHA != "" && !digestPattern.MatchString(in.RuleSHA) {
		return Proposal{}, nil, fail(400, "invalid_request", "rule_sha256 must be a SHA-256.")
	}
	rawInput, _ := json.Marshal(in)
	sum := sha256.Sum256(rawInput)
	requestDigest := hex.EncodeToString(sum[:])
	// Read-only preparation precedes the tenant mutation fence. A prepared
	// capability is bound to this actor and exact input, and revalidated below.
	if preparedInboxFrom(ctx) == nil {
		prepared, err := m.prepareInboxTx(ctx, tx, actor, in)
		if err != nil {
			return Proposal{}, nil, err
		}
		ctx = context.WithValue(ctx, preparedInboxKey{}, prepared)
	}
	// This writer is also used by the pre-existing unforgeable analysis service
	// capability. It receives the same access fence, never human authority.
	if _, err := tx.Exec(ctx, `SELECT id FROM tenants WHERE id=$1 FOR NO KEY UPDATE`, actor.TenantID); err != nil {
		return Proposal{}, nil, err
	}
	if !m.analysisAuthorized(ctx, actor) {
		if err := authz.RequireTx(ctx, tx, actor, "rules.write", authz.Scope{}); err != nil {
			return Proposal{}, nil, err
		}
	}
	existing, err := getProposal(ctx, tx, in.RequestID)
	if err == nil {
		if !existing.Inbox || existing.ProposedBy != actor.ID || existing.InboxRequestDigest != inboxRequestDigest(in) || existing.DeskQuestionID != deskQuestion || existing.DeskAnswerID != deskAnswer {
			return Proposal{}, nil, fail(409, "request_conflict", "That request UUID belongs to another proposal.")
		}
		return existing, nil, nil
	}
	var refusal *failure
	if !errors.As(err, &refusal) || refusal.Status != 404 {
		return Proposal{}, nil, err
	}
	prepared := preparedInboxFrom(ctx)
	if err := m.verifyPreparedTx(ctx, tx, actor, in, prepared); err != nil {
		return Proposal{}, nil, err
	}
	source, pin, old, next, rule, index := prepared.source, prepared.pin, prepared.old, prepared.next, prepared.rule, prepared.index
	changes := []events.Change{}
	if replaces != "" {
		changes, err = m.RetireDeskDraftTx(ctx, tx, actor, replaces, deskQuestion, deskAnswer)
		if err != nil {
			return Proposal{}, nil, err
		}
	}
	p := Proposal{ID: in.RequestID, SourceID: source.ID, Repository: source.Repository, Path: in.Path, RuleKey: in.RuleKey,
		State: "pending", ProposedBy: actor.ID, Inbox: true, BaseRuleSHA: old.SHA256, ProposedSHA: next.SHA256, Ticket: in.Ticket,
		ProposedTLDR: tldrDigest(pin.TLDR.EN, pin.TLDR.DE), RuleSet: rule.Set, RuleIndex: index, DeskQuestionID: deskQuestion, DeskAnswerID: deskAnswer, InboxDigest: requestDigest, InboxRequestDigest: inboxRequestDigest(in)}
	current, err := getSource(ctx, tx, source.ID, false)
	if err != nil {
		return Proposal{}, nil, err
	}
	if current.Commit != source.Commit {
		return Proposal{}, nil, fail(409, "stale_source", "The doctrine pin moved; reload the rule and propose again.")
	}
	var total, mine int
	var sameID, sameSHA string
	if err := tx.QueryRow(ctx, `SELECT count(*), count(*) FILTER (WHERE proposed_by=$1),
		COALESCE(max(id::text) FILTER (WHERE proposed_by=$1 AND source_id=$2 AND path=$3 AND rule_key=$4),''),
		COALESCE(max(data->>'proposed_rule_sha256') FILTER (WHERE proposed_by=$1 AND source_id=$2 AND path=$3 AND rule_key=$4),'')
		FROM doctrine_proposals WHERE data->>'inbox'='true' AND data->>'state'='pending'`, actor.ID, source.ID, in.Path, in.RuleKey).Scan(&total, &mine, &sameID, &sameSHA); err != nil {
		return Proposal{}, nil, err
	}
	if sameID != "" {
		if deskQuestion == "" && sameSHA == next.SHA256 {
			existing, err := getProposal(ctx, tx, sameID)
			if err != nil {
				return Proposal{}, nil, err
			}
			if existing.InboxRequestDigest == p.InboxRequestDigest && existing.DeskQuestionID == "" && existing.DeskAnswerID == "" {
				return existing, nil, nil
			}
		}
		return Proposal{}, nil, fail(409, "rule_proposal_open", "You already proposed a change to this rule. A person has to act on it first.")
	}
	if total >= maxInboxPending || mine >= maxInboxPerProposer {
		return Proposal{}, nil, fail(409, "inbox_full", "The doctrine inbox is full. A person has to act on waiting proposals first.")
	}
	if in.Ticket != "" {
		var projectID string
		err := tx.QueryRow(ctx, `SELECT id::text, COALESCE(project_id::text,'') FROM nodes WHERE key=$1 AND deleted_at IS NULL`, in.Ticket).Scan(&p.TicketID, &projectID)
		if errors.Is(err, pgx.ErrNoRows) {
			return Proposal{}, nil, fail(404, "ticket_not_found", "That ticket is not in this workspace.")
		}
		if err != nil {
			return Proposal{}, nil, err
		}
		if err := authz.RequireTx(ctx, tx, actor, "nodes.read", authz.Scope{ProjectID: projectID}); err != nil {
			return Proposal{}, nil, err
		}
	}
	raw, err := jsonData(p)
	if err != nil {
		return Proposal{}, nil, err
	}
	if err := tx.QueryRow(ctx, `INSERT INTO doctrine_proposals(tenant_id,id,source_id,repository,path,rule_key,input_digest,base_commit,proposed_by,data) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10) RETURNING created_at`,
		actor.TenantID, p.ID, p.SourceID, p.Repository, p.Path, p.RuleKey, inputDigest(pin), source.Commit, actor.ID, raw).Scan(&p.CreatedAt); err != nil {
		return Proposal{}, nil, err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO doctrine_proposal_drafts(tenant_id,proposal_id,source,tldr_en,tldr_de,why) VALUES($1,$2,$3,$4,$5,$6)`,
		actor.TenantID, p.ID, pin.Source, strings.TrimSpace(pin.TLDR.EN), strings.TrimSpace(pin.TLDR.DE), strings.TrimSpace(pin.Explanation)); err != nil {
		return Proposal{}, nil, err
	}
	p.BaseCommit, p.InputDigest = source.Commit, inputDigest(pin)
	changes = append(changes, events.Change{Type: "doctrine.inbox_proposed", After: map[string]any{"proposal_id": p.ID, "repository": p.Repository, "path": p.Path, "rule_key": p.RuleKey, "proposed_by": actor.ID, "ticket": p.Ticket, "question_id": deskQuestion, "answer_id": deskAnswer}})
	return p, changes, nil
}

// PreparedInbox carries validated editor output only inside the server. It is
// never request authority: the writer still fences access and checks freshness.
type PreparedInbox struct {
	exemptMain                 string
	owner                      *Module
	actor, tenant, inputDigest string
	fingerprint                string
	source                     Source
	pin                        ProposalInput
	old, next, rule            RuleView
	index                      int
}
type preparedInboxKey struct{}

func preparedInboxFrom(ctx context.Context) *PreparedInbox {
	p, _ := ctx.Value(preparedInboxKey{}).(*PreparedInbox)
	return p
}
func WithPreparedInbox(ctx context.Context, p *PreparedInbox) context.Context {
	return context.WithValue(ctx, preparedInboxKey{}, p)
}
func inboxDigest(in InboxInput) string {
	raw, _ := json.Marshal(in)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

// Xmin identifies bounded cache/source versions without rendering or decrypting
// under the tenant lock. Preparation verifies the same snapshot on both sides.
func inboxFingerprint(ctx context.Context, tx pgx.Tx) (string, error) {
	var count int
	var stamp string
	err := tx.QueryRow(ctx, `SELECT count(*),coalesce(jsonb_agg(jsonb_build_array(s.id,s.xmin::text,g.xmin::text,c.xmin::text) ORDER BY s.id)::text,'[]')
 FROM (SELECT *,xmin FROM doctrine_sources ORDER BY id LIMIT 101) s
 LEFT JOIN doctrine_private_guard g ON g.tenant_id=s.tenant_id AND g.source_id=s.id
 LEFT JOIN doctrine_public_main_cache c ON c.tenant_id=s.tenant_id AND c.source_id=s.id`).Scan(&count, &stamp)
	if err != nil {
		return "", err
	}
	if count > 100 {
		return "", fail(422, "source_limit", "Review the workspace source limit before proposing.")
	}
	return stamp, nil
}
func (m *Module) PrepareDeskDraft(ctx context.Context, actor tenant.Principal, in InboxInput) (*PreparedInbox, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	var prepared *PreparedInbox
	readCtx := ctx
	if m.analysisAuthorized(ctx, actor) {
		readCtx = db.AllProjects(ctx, "doctrine outcome preparation")
	}
	err := db.InTenant(readCtx, m.pool, actor.TenantID, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT set_config('lock_timeout','3s',true),set_config('statement_timeout','10s',true)`); err != nil {
			return err
		}
		if !m.analysisAuthorized(ctx, actor) {
			if err := authz.RequireTx(ctx, tx, actor, "rules.write", authz.Scope{}); err != nil {
				return err
			}
		}
		var err error
		prepared, err = m.prepareInboxTx(ctx, tx, actor, in)
		return err
	})
	return prepared, err
}
func (m *Module) prepareInboxTx(ctx context.Context, tx pgx.Tx, actor tenant.Principal, in InboxInput) (*PreparedInbox, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := m.proposalAccess(actor); err != nil {
		return nil, err
	}
	if len(m.guardMaster) < 32 {
		return nil, fail(503, "guard_unavailable", missingGuardReason)
	}
	stamp, err := inboxFingerprint(ctx, tx)
	if err != nil {
		return nil, err
	}
	source, err := inboxSource(ctx, tx, in)
	if err != nil {
		return nil, err
	}
	if !m.writableSource(source) {
		return nil, fail(422, "unsupported_repository", "Only the deployment-configured doctrine repositories accept proposals; their visibility must match.")
	}
	if source.CredentialRef != "" {
		if err := m.credentials.authorize(source.CredentialRef, actor.TenantID, source.Repository); err != nil {
			return nil, err
		}
	}
	files, err := cachedFiles(ctx, tx, source)
	if err != nil {
		return nil, err
	}
	var guard *guardCorpus
	if m.repositories.IsPublic(source.Repository) {
		guard, err = m.privateGuard(ctx, tx, actor)
		if err != nil {
			return nil, err
		}
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
	_, old, next, err := m.editRuleViews(source, files, pin)
	if err != nil {
		return nil, err
	}
	if !humanActor(actor) && (old.Strength == "locked" || next.Strength == "locked") {
		return nil, fail(403, "locked_rule", "Locked rules change only through a person. Ask one to edit it under Doctrine.")
	}
	// A TL;DR alone is a change; adding one to a rule without one is too.
	if next.SHA256 == old.SHA256 && rule.TLDR != nil && tldrDigest(rule.TLDR.EN, rule.TLDR.DE) == tldrDigest(pin.TLDR.EN, pin.TLDR.DE) {
		return nil, fail(400, "no_change", "The proposal matches the pinned rule.")
	}
	exemptMain, err := m.checkPrivateQuotesTx(ctx, tx, actor, source, files, guard, pin.Source, pin.TLDR.EN, pin.TLDR.DE, pin.Explanation)
	if err != nil {
		return nil, err
	}

	after, err := inboxFingerprint(ctx, tx)
	if err != nil {
		return nil, err
	}
	if after != stamp {
		return nil, fail(409, "stale_source", "Doctrine changed during preparation; reload and decide again.")
	}
	return &PreparedInbox{exemptMain: exemptMain, owner: m, actor: actor.ID, tenant: actor.TenantID, inputDigest: inboxDigest(in), fingerprint: stamp, source: source, pin: pin, old: old, next: next, rule: rule, index: index}, nil
}
func (m *Module) verifyPreparedTx(ctx context.Context, tx pgx.Tx, actor tenant.Principal, in InboxInput, p *PreparedInbox) error {
	if p.owner != m || p.actor != actor.ID || p.tenant != actor.TenantID || p.inputDigest != inboxDigest(in) {
		return fail(409, "request_conflict", "Prepared doctrine belongs to another actor or input.")
	}
	// Protect the exact source/cache snapshot against pin and index changes.
	// Sorted source fences precede any proposal rows created by this writer.
	rows, err := tx.Query(ctx, `SELECT id FROM doctrine_sources ORDER BY id LIMIT 101 FOR NO KEY UPDATE`)
	if err != nil {
		return err
	}
	for rows.Next() {
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	current, err := getSource(ctx, tx, p.source.ID, false)
	if err != nil {
		return err
	}
	stamp, err := inboxFingerprint(ctx, tx)
	if err != nil {
		return err
	}
	if stamp != p.fingerprint || current.Commit != p.source.Commit {
		return fail(409, "stale_source", "Doctrine changed after preparation; reload and decide again.")
	}
	if p.exemptMain != "" {
		var valid bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM doctrine_public_main_cache WHERE source_id=$1 AND main_commit=$2 AND observed_at<=clock_timestamp() AND observed_at>clock_timestamp()-interval '5 minutes')`, current.ID, p.exemptMain).Scan(&valid); err != nil {
			return err
		}
		if !valid {
			return fail(422, "public_main_unavailable", "The public main exception expired after preparation; reindex before deciding again.")
		}
	}
	if current.CredentialRef != "" {
		if err := m.credentials.authorize(current.CredentialRef, actor.TenantID, current.Repository); err != nil {
			return err
		}
	}
	if m.repositories.IsPublic(current.Repository) {
		sources, err := listSources(ctx, tx)
		if err != nil {
			return err
		}
		for _, source := range sources {
			if m.repositories.IsPrivate(source.Repository) {
				if err := m.credentials.authorize(source.CredentialRef, actor.TenantID, source.Repository); err != nil {
					return err
				}
			}
		}
	}
	return nil
}
