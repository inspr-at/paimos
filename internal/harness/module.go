// SPDX-License-Identifier: AGPL-3.0-only

// Package harness exposes classic-compatible public harness generations on top
// of Aeon's tenant-scoped agent runs, work orders and principal inbox. New
// returns an httpapi.Module; the coordinator mounts it behind auth middleware.
// B7 adds GET /api/harness-sessions with state, harness, agent, project and
// ticket filters, limit 1..200 and a tenant/principal/filter-bound cursor.
// It returns items/next_cursor in descending (created_at,id) order. Each item
// extends Session with project and nullable ticket {id,key,title} summaries.
// State is stopped after closure, otherwise phase. Historical node bindings
// retain their summaries after soft deletion. Existing Plugin() supplies the
// compiled manifest; no new manifest registration or cmd wiring is needed.
// AC4 adds nullable display_label (migration 0864), supplied by the CLI's
// harness register --label. It is public metadata, never principal identity.
// Exact replay includes the normalized label. Existing harness.registered,
// harness.bound and harness.stopped events are emitted transactionally through
// events.Append and the aeon_events notification trigger; /api/events/stream
// replays them with tenant/project visibility. Clients treat them as read hints.
// AEON-184 adds GET /api/harness-sessions/live: the agents actively working
// in each visible project right now, for the Projects page (live.go).
// SC1/AEON-221 accepts heartbeat activity=throttled with the existing event and
// lease/sequence fencing. New(pool) and Plugin() remain the module and manifest
// constructors; no new coordinator wiring is required. Migration 0882 extends
// the activity constraint. Read endpoints attach content-free StateEvidence.
//
// AEON-192 adds activity_note on heartbeat. New(pool) remains the coordinator's
// httpapi.Module constructor and Plugin() remains its compiled manifest.
// TM1 adds optional --model, --effort, --account-label, --harness-version,
// --brief, --worktree and --branch to paimos harness register|heartbeat,
// plus repeated --commit SHA:subject on heartbeat. The coordinator can pass
// these flags from worker scripts without changing server wiring.
// PV1/AEON-219 records instruction provenance on its own route. A rules receipt
// appends merged-rule and rule-set identities in that same transaction.
// Heartbeat registration may post AGENTS.md and CLAUDE.md hashes. Registration
// and heartbeat request schemas are unchanged. Writes use the existing worker lease.
package harness

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/httpapi"
	"github.com/inspr-at/paimos/internal/inbox"
	"github.com/inspr-at/paimos/internal/ownedprocess"
	"github.com/inspr-at/paimos/internal/plugins"
	"github.com/inspr-at/paimos/internal/reportercontract"
	"github.com/inspr-at/paimos/internal/rules"
	"github.com/inspr-at/paimos/internal/runkind"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/inspr-at/paimos/internal/workorders"
)

type Module struct {
	planningStart  func(context.Context, pgx.Tx, string, string) error
	pool           *pgxpool.Pool
	controlText    controlRelay
	ownershipClock func(context.Context, pgx.Tx) (time.Time, error)
}

var _ httpapi.Module = (*Module)(nil)

// The server supplies its planning writer without coupling harness to node APIs.
func New(pool *pgxpool.Pool, planningStart ...func(context.Context, pgx.Tx, string, string) error) httpapi.Module {
	m := &Module{pool: pool}
	if len(planningStart) > 0 {
		m.planningStart = planningStart[0]
	}
	return m
}

func (m *Module) Mount(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/inbox/session-binding", m.resolveSessionBinding)
	for _, route := range []struct {
		pattern, scope string
		agent          bool
		status         int
		fn             func(*http.Request, pgx.Tx, tenant.Principal) (any, error)
	}{
		{"POST /api/projects/{projectId}/harness-sessions", "harness.write", false, 201, m.register},
		{"GET /api/me/host-labels", "harness.read", false, 200, m.listHostLabels},
		{"PUT /api/me/host-labels", "harness.read", false, 200, m.putHostLabel},
		{"GET /api/harness-sessions", "harness.read", false, 200, m.listAll},
		{"GET /api/harness-sessions/live", "harness.read", false, 200, m.live},
		{"POST /api/harness-sessions/pause", "harness.control", false, 200, m.pauseAll},
		{"POST /api/harness-sessions/resume", "harness.control", false, 200, m.resumeAll},
		{"GET /api/projects/{projectId}/harness-sessions", "harness.read", false, 200, m.list},
		{"GET /api/projects/{projectId}/harness-sessions/orchestrator", "harness.read", false, 200, m.orchestrator},
		{"GET /api/projects/{projectId}/harness-sessions/{sessionId}", "harness.read", false, 200, m.status},
		{"GET /api/projects/{projectId}/harness-sessions/{sessionId}/lookup", "harness.read", false, 200, m.lookup},
		{"GET /api/projects/{projectId}/harness-sessions/{sessionId}/read-marker", "harness.read", false, 200, m.getReadMarker},
		{"PUT /api/projects/{projectId}/harness-sessions/{sessionId}/read-marker", "harness.read", false, 200, m.putReadMarker},
		{"POST /api/projects/{projectId}/harness-sessions/{sessionId}/reparent", "harness.write", false, 200, m.reparent},
		{"PATCH /api/projects/{projectId}/harness-sessions/{sessionId}/binding", "harness.write", false, 200, m.bind},
		{"POST /api/projects/{projectId}/harness-sessions/{sessionId}/usage", "harness.worker", true, 200, m.reportUsage},
		{"GET /api/projects/{projectId}/harness-sessions/{sessionId}/usage", "harness.read", false, 200, m.sessionUsage},
		{"POST /api/model-prices", "models.manage", false, 201, m.createUsagePrice},
		{"GET /api/model-prices", "harness.read", false, 200, m.listUsagePrices},
		{"POST /api/projects/{projectId}/harness-sessions/{sessionId}/heartbeat", "harness.worker", true, 200, m.heartbeat},
		{"GET /api/settings/eta-interval", "settings.manage", false, 200, m.getEtaInterval},
		{"PUT /api/settings/eta-interval", "settings.manage", false, 200, m.putEtaInterval},
		{"GET /api/settings/heartbeat-lost", "settings.manage", false, 200, m.getHeartbeatLost},
		{"PUT /api/settings/heartbeat-lost", "settings.manage", false, 200, m.putHeartbeatLost},
		{"PUT /api/nodes/{nodeId}/live-eta", "harness.worker", true, 200, m.setLiveEta},
		{"GET /api/projects/{projectId}/harness-sessions/{sessionId}/provenance", "harness.read", false, 200, m.readProvenance},
		{"POST /api/projects/{projectId}/harness-sessions/{sessionId}/provenance", "harness.worker", true, 200, m.recordProvenance},
		{"GET /api/projects/{projectId}/instruction-provenance", "harness.read", false, 200, m.queryInstructionSources},
		{"GET /api/projects/{projectId}/harness-sessions/{sessionId}/rules-receipts", "harness.read", false, 200, m.readRulesReceipts},
		{"POST /api/projects/{projectId}/harness-sessions/{sessionId}/rules-receipts", "harness.worker", true, 200, m.recordRulesReceipt},
		{"POST /api/projects/{projectId}/harness-sessions/{sessionId}/yield", "harness.worker", true, 200, m.yield},
		{"POST /api/projects/{projectId}/harness-sessions/{sessionId}/pause", "harness.control", false, 200, m.requestPause},
		{"POST /api/projects/{projectId}/harness-sessions/{sessionId}/pause-plan", "harness.worker", true, 200, m.planPause},
		{"POST /api/projects/{projectId}/harness-sessions/{sessionId}/resume", "harness.control", false, 200, m.resumePause},
		{"POST /api/projects/{projectId}/harness-sessions/pause", "harness.control", false, 200, m.pauseBatch},
		{"POST /api/projects/{projectId}/harness-sessions/resume", "harness.control", false, 200, m.resumeBatch},
		{"POST /api/projects/{projectId}/harness-sessions/{sessionId}/drain", "harness.worker", true, 200, m.drain},
		{"POST /api/projects/{projectId}/harness-sessions/{sessionId}/complete-delivery", "harness.worker", true, 200, m.completeDelivery},
		{"GET /api/projects/{projectId}/harness-sessions/{sessionId}/managed-settings", "harness.control", false, 200, m.managedSettings},
		{"POST /api/projects/{projectId}/harness-sessions/{sessionId}/managed-controls", "harness.control", false, 201, m.managedControl},
		{"POST /api/projects/{projectId}/harness-sessions/{sessionId}/managed-context", "harness.worker", true, 200, m.managedContext},
		{"POST /api/projects/{projectId}/harness-sessions/{sessionId}/requests", "harness.control", false, 201, m.requestSessionChange},
		{"POST /api/projects/{projectId}/harness-sessions/{sessionId}/controls/interrupt", "harness.control", false, 201, m.interrupt},
		{"POST /api/projects/{projectId}/harness-sessions/{sessionId}/controls/stop", "harness.control", false, 201, m.stop},
		{"GET /api/projects/{projectId}/harness-sessions/{sessionId}/controls/{controlId}", "harness.read", false, 200, m.control},
		{"POST /api/projects/{projectId}/harness-sessions/{sessionId}/controls/{controlId}/complete", "harness.worker", true, 200, m.completeControl},
		{"POST /api/projects/{projectId}/harness-sessions/{sessionId}/stop", "harness.worker", true, 200, m.markStopped},
		{"GET /api/projects/{projectId}/harness-sessions/{sessionId}/recovery", "harness.read", false, 200, m.recovery},
		{"POST /api/projects/{projectId}/harness-sessions/{sessionId}/remove", "harness.read", false, 200, m.remove},
		{"POST /api/projects/{projectId}/harness-sessions/remove-stale", "harness.read", false, 200, m.removeStale},
		{"POST /api/projects/{projectId}/harness-sessions/{sessionId}/archive", "harness.recover", false, 200, m.archive},
		{"POST /api/projects/{projectId}/harness-sessions/{sessionId}/controls/force-stop", "harness.force_stop", false, 201, m.forceStop},
	} {
		handler := workorders.Endpoint(m.pool, route.scope, route.agent, route.status, route.fn)
		if route.pattern == "GET /api/projects/{projectId}/harness-sessions/{sessionId}" || route.pattern == "POST /api/projects/{projectId}/harness-sessions/{sessionId}/heartbeat" {
			handler = reportercontract.WithHeader(reportercontract.HarnessSession, handler)
		}
		mux.HandleFunc(route.pattern, handler)
	}
}

type Session struct {
	Pause                *Pause        `json:"pause,omitempty"`
	Continuation         *Continuation `json:"continuation,omitempty"`
	CanReparent          *bool         `json:"can_reparent,omitempty"`
	Generator            *string       `json:"generator,omitempty"`
	Command              *string       `json:"command,omitempty"`
	ownerID              *string
	HandedOverToID       *string                `json:"handed_over_to_id,omitempty"`
	AdoptedFromID        *string                `json:"adopted_from_id,omitempty"`
	Controls             []Control              `json:"controls,omitempty"`
	Watch                *AttachStatus          `json:"watch,omitempty"`
	ProcessOwnership     *ownedprocess.Identity `json:"process_ownership,omitempty"`
	ProcessObservedAt    *time.Time             `json:"process_observed_at,omitempty"`
	ArchivedAt           *time.Time             `json:"archived_at"`
	RecoveryProcessState *string                `json:"recovery_process_state"`
	StateEvidence
	ID                                                              string            `json:"id"`
	ProjectID                                                       string            `json:"project_id"`
	AgentPrincipalID                                                string            `json:"agent_principal_id"`
	RunID                                                           *string           `json:"run_id"`
	TicketNodeID                                                    *string           `json:"ticket_node_id"`
	WorkOrderID                                                     *string           `json:"work_order_id"`
	ParentID                                                        *string           `json:"parent_harness_session_id"`
	Harness                                                         string            `json:"harness"`
	Host                                                            string            `json:"host"`
	DisplayLabel                                                    *string           `json:"display_label"`
	Model                                                           *string           `json:"model"`
	ModelRaw                                                        *string           `json:"model_raw,omitempty"`
	ModelProfileID                                                  *string           `json:"model_profile_id,omitempty"`
	ReasoningEffort                                                 *string           `json:"reasoning_effort"`
	AccountLabel                                                    *string           `json:"account_label"`
	HarnessVersion                                                  *string           `json:"harness_version"`
	Brief                                                           *string           `json:"brief"`
	Worktree                                                        *string           `json:"worktree"`
	Branch                                                          *string           `json:"branch"`
	Commits                                                         []Commit          `json:"commits"`
	ActivityNote                                                    *string           `json:"activity_note"`
	ActivityHistory                                                 []ActivityNote    `json:"activity_history,omitempty"`
	MetadataHistory                                                 *[]MetadataChange `json:"metadata_history,omitempty"`
	Management                                                      string            `json:"management_mode"`
	Role                                                            string            `json:"role"`
	WorkShape                                                       string            `json:"work_shape"`
	Capabilities                                                    []string          `json:"advertised_capabilities"`
	Phase                                                           string            `json:"phase"`
	Activity                                                        string            `json:"activity"`
	ActivitySequence                                                int64             `json:"activity_sequence"`
	Revision                                                        int64             `json:"revision"`
	RowVersion                                                      int64             `json:"row_version,omitempty"`
	HeartbeatAt                                                     *time.Time        `json:"heartbeat_at"`
	StoppedAt                                                       *time.Time        `json:"stopped_at"`
	StopReason                                                      *string           `json:"stop_reason"`
	CreatedAt                                                       time.Time         `json:"created_at"`
	EtaReadyAt                                                      *time.Time        `json:"eta_ready_at,omitempty"`
	EtaLiveAt                                                       *time.Time        `json:"eta_live_at,omitempty"`
	ProgressPct                                                     *int              `json:"progress_pct,omitempty"`
	EtaReportedAt                                                   *time.Time        `json:"eta_reported_at,omitempty"`
	EtaStale                                                        bool              `json:"eta_stale,omitempty"`
	refDigest, leaseDigest, registrationMetaDigest, vendorRefDigest []byte

	// AEON-280: when and how this generation last pulled its inbox. Omitted
	// until it pulls once; clients derive Listening from the age.
	InboxSeenAt  *time.Time `json:"inbox_seen_at,omitempty"`
	InboxSeenVia string     `json:"inbox_seen_via,omitempty"`
	// AEON-369: true when a vendor session reference is stored. The reference
	// itself is never returned. Omitted when absent.
	HasVendorSessionRef bool `json:"has_vendor_session_ref,omitempty"`
	// AEON-437: required in every session payload (detail, list, mutation and event
	// snapshot), never omitted: the session reported 100% and stopped with a recorded
	// clean exit. Read in the same statement as the row by aeon_session_finished, the
	// one authority, so a screen never derives Done itself.
	Finished bool `json:"finished"`
}

type ActivityNote struct {
	Note string    `json:"note"`
	At   time.Time `json:"at"`
}

type MetadataChange struct {
	Field         string    `json:"field"`
	PreviousValue *string   `json:"previous_value"`
	Value         *string   `json:"value"`
	At            time.Time `json:"at"`
}

type Commit struct {
	SHA     string `json:"sha"`
	Subject string `json:"subject"`
}

type sessionText struct {
	Model           *string `json:"model"`
	ReasoningEffort *string `json:"reasoning_effort"`
	AccountLabel    *string `json:"account_label"`
	HarnessVersion  *string `json:"harness_version"`
	Brief           *string `json:"brief"`
	Worktree        *string `json:"worktree"`
	Branch          *string `json:"branch"`
}

func cleanText(raw string, max int, name string) (string, error) {
	if !utf8.ValidString(raw) {
		return "", workorders.Fail(400, "invalid "+name)
	}
	clean := strings.TrimSpace(strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, raw))
	if utf8.RuneCountInString(clean) > max {
		return "", workorders.Fail(400, name+" is too long")
	}
	return clean, nil
}

func (m *sessionText) normalize() error {
	for _, field := range []struct {
		value **string
		max   int
		name  string
	}{
		{&m.Model, 128, "model"}, {&m.ReasoningEffort, 40, "reasoning effort"},
		{&m.AccountLabel, 128, "account label"}, {&m.HarnessVersion, 80, "harness version"},
		{&m.Brief, 240, "brief"}, {&m.Worktree, 512, "worktree"}, {&m.Branch, 200, "branch"},
	} {
		if *field.value == nil {
			continue
		}
		clean, err := cleanText(**field.value, field.max, field.name)
		if err != nil {
			return err
		}
		*field.value = nil
		if clean != "" {
			*field.value = &clean
		}
	}
	return nil
}

func validCommits(commits []Commit) error {
	if len(commits) > 20 {
		return workorders.Fail(400, "too many commits")
	}
	for i := range commits {
		c := &commits[i]
		if len(c.SHA) < 7 || len(c.SHA) > 40 || strings.Trim(c.SHA, "0123456789abcdefABCDEF") != "" {
			return workorders.Fail(400, "invalid commit SHA")
		}
		c.SHA = strings.ToLower(c.SHA)
		var err error
		c.Subject, err = cleanText(c.Subject, 200, "commit subject")
		if err != nil {
			return err
		}
		if c.Subject == "" {
			return workorders.Fail(400, "commit subject required")
		}
	}
	return nil
}

func appendCommits(existing, incoming []Commit) []Commit {
	out := append([]Commit{}, existing...)
	for _, c := range incoming {
		found := false
		for _, old := range out {
			if old.SHA == c.SHA {
				found = true
				break
			}
		}
		if !found {
			out = append(out, c)
		}
	}
	if len(out) > 20 {
		out = out[len(out)-20:]
	}
	return out
}

func normalizeActivityNote(raw string) (string, bool) {
	if !utf8.ValidString(raw) {
		return "", false
	}
	clean := strings.TrimSpace(strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, raw))
	return clean, clean != "" && utf8.RuneCountInString(clean) <= 120
}

// row_version is the row's own revision (AEON-449): a trigger bumps it inside every
// statement that changes the row, so of two copies the larger is the newer one.
// revision, by contrast, is an optimistic-lock token only some writers advance.
const sessionColumns = `id::text,project_id::text,agent_principal_id::text,run_id::text,ticket_node_id::text,work_order_id::text,parent_id::text,harness,host,management,role,work_shape,capabilities,phase,activity,activity_sequence,revision,heartbeat_at,stopped_at,stop_reason,created_at,ref_digest,lease_digest,display_label,activity_note,model,reasoning_effort,account_label,harness_version,brief,worktree,branch,commits,registration_metadata_digest,archived_at,recovery_process_state,process_ownership,process_observed_at,eta_ready_at,eta_live_at,progress_pct,eta_reported_at,inbox_seen_at,coalesce(inbox_seen_via,''),vendor_ref_digest,handed_over_to_id::text,adopted_from_id::text,owner_principal_id::text,row_version,aeon_session_finished(stopped_at,stop_reason,progress_pct),generator,command,model_raw,model_profile_id::text,pause_record,continuation_handover`

func scanSession(row pgx.Row) (Session, error) {
	var s Session
	var progress *int16
	err := row.Scan(&s.ID, &s.ProjectID, &s.AgentPrincipalID, &s.RunID, &s.TicketNodeID, &s.WorkOrderID, &s.ParentID, &s.Harness, &s.Host, &s.Management, &s.Role, &s.WorkShape, &s.Capabilities, &s.Phase, &s.Activity, &s.ActivitySequence, &s.Revision, &s.HeartbeatAt, &s.StoppedAt, &s.StopReason, &s.CreatedAt, &s.refDigest, &s.leaseDigest, &s.DisplayLabel, &s.ActivityNote, &s.Model, &s.ReasoningEffort, &s.AccountLabel, &s.HarnessVersion, &s.Brief, &s.Worktree, &s.Branch, &s.Commits, &s.registrationMetaDigest, &s.ArchivedAt, &s.RecoveryProcessState, &s.ProcessOwnership, &s.ProcessObservedAt, &s.EtaReadyAt, &s.EtaLiveAt, &progress, &s.EtaReportedAt, &s.InboxSeenAt, &s.InboxSeenVia, &s.vendorRefDigest, &s.HandedOverToID, &s.AdoptedFromID, &s.ownerID, &s.RowVersion, &s.Finished, &s.Generator, &s.Command, &s.ModelRaw, &s.ModelProfileID, &s.Pause, &s.Continuation)
	if err != nil {
		return s, err
	}
	if progress != nil {
		value := int(*progress)
		s.ProgressPct = &value
	}
	s.HasVendorSessionRef = len(s.vendorRefDigest) > 0
	return s, nil
}
func project(ctx context.Context, tx pgx.Tx, id string) error {
	if !workorders.UUID(id) {
		return workorders.Fail(400, "invalid project id")
	}
	var exists bool
	err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM nodes n JOIN node_kinds k ON k.tenant_id=n.tenant_id AND k.id=n.kind_id WHERE n.id=$1 AND n.deleted_at IS NULL AND k.slug='project')`, id).Scan(&exists)
	if err != nil {
		return err
	}
	if !exists {
		return workorders.Fail(404, "project not found")
	}
	return nil
}
func load(ctx context.Context, tx pgx.Tx, projectID, id string, lock bool) (Session, error) {
	if !workorders.UUID(id) {
		return Session{}, workorders.Fail(400, "invalid session id")
	}
	q := `SELECT ` + sessionColumns + ` FROM harness_sessions WHERE project_id=$1 AND id=$2`
	if lock {
		q += ` FOR UPDATE`
	}
	return scanSession(tx.QueryRow(ctx, q, projectID, id))
}
func digest(domain, value string) []byte {
	sum := sha256.Sum256([]byte("aeon.harness." + domain + "\x00" + value))
	return sum[:]
}

// RegistrationLeaseConflict identifies a live reference held by another lease.
// Native coordinator helpers may retry it until their predecessor expires.
const RegistrationLeaseConflict = "active generation conflicts with registration: harness_session_ref is already active with a different worker lease"

// vendorRefDigest hashes a harness-native session id with the same domain as
// harness_session_ref. The same value is not stored twice; lookup hits ref_digest.
// A nil raw value means the client omitted the field and must not clear a stored one.
func vendorRefDigest(sessionRef, lease string, raw *string) ([]byte, error) {
	if raw == nil || *raw == sessionRef {
		return nil, nil
	}
	if len(*raw) < 16 || len(*raw) > 4096 || strings.ContainsAny(*raw, "\r\n") || *raw == lease {
		return nil, workorders.Fail(400, "invalid harness registration")
	}
	return digest("ref", *raw), nil
}

func fillVendorRef(ctx context.Context, tx pgx.Tx, existing *Session, vendor []byte) error {
	if vendor == nil {
		return nil
	}
	if existing.vendorRefDigest == nil {
		// The body names the row it was made from: keep its version current.
		if err := tx.QueryRow(ctx, `UPDATE harness_sessions SET vendor_ref_digest=$2 WHERE id=$1 AND vendor_ref_digest IS NULL RETURNING row_version`, existing.ID, vendor).Scan(&existing.RowVersion); err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return registrationConflict(err)
		}
		existing.vendorRefDigest = vendor
	} else if subtle.ConstantTimeCompare(existing.vendorRefDigest, vendor) != 1 {
		return workorders.Fail(409, "vendor_session_ref differs from this active generation's existing binding")
	}
	existing.HasVendorSessionRef = true
	return nil
}

func registrationConflict(err error) error {
	var pg *pgconn.PgError
	if errors.As(err, &pg) && pg.Code == "23505" {
		switch pg.ConstraintName {
		case "harness_one_active_vendor_ref":
			return workorders.Fail(409, "vendor_session_ref is already bound to an active generation for this agent")
		case "harness_one_active_ref":
			return workorders.Fail(409, "harness_session_ref is already bound to an active generation in this project")
		default:
			return workorders.Fail(409, "harness registration conflicts with an existing generation")
		}
	}
	return err
}
func proof(s Session, r *http.Request, p tenant.Principal) error {
	// Authenticate the worker before revealing that its generation was archived.
	// All write paths using worker(), including provenance, fence archived
	// registrations before checking idempotent receipts or appending revisions.
	if !leaseProof(s, r, p) {
		return workorders.Fail(403, "harness worker proof rejected")
	}
	if s.ArchivedAt != nil {
		return workorders.Fail(410, "harness generation archived")
	}
	if s.StoppedAt != nil {
		return workorders.Fail(403, "harness worker proof rejected")
	}
	return nil
}

// leaseProof authenticates the generation's own worker, whatever its state.
func leaseProof(s Session, r *http.Request, p tenant.Principal) bool {
	lease := r.Header.Get("X-Aeon-Worker-Lease")
	return s.ID != "" && p.Kind == tenant.Agent && p.ID == s.AgentPrincipalID && len(lease) >= 32 && subtle.ConstantTimeCompare(digest("lease", lease), s.leaseDigest) == 1
}
func worker(ctx context.Context, tx pgx.Tx, r *http.Request, p tenant.Principal) (Session, error) {
	s, err := load(ctx, tx, r.PathValue("projectId"), r.PathValue("sessionId"), true)
	if err != nil { // No existence oracle on a worker path.
		if errors.Is(err, pgx.ErrNoRows) {
			return s, workorders.Fail(403, "harness worker proof rejected")
		}
		return s, err
	}
	return s, proof(s, r, p)
}
func record(ctx context.Context, tx pgx.Tx, p tenant.Principal, s Session, kind string, before, after any) error {
	return workorders.Record(ctx, tx, p, s.ProjectID, "harness."+kind, before, after)
}
func validHarness(v string) bool { return runkind.Valid(v) }
func validShape(v string) bool   { return v == "ship" || v == "scout" }
func validPhase(v string) bool {
	switch v {
	case "starting", "working", "yielded", "stopping":
		return true
	}
	return false
}
func has(s Session, cap string) bool {
	for _, v := range s.Capabilities {
		if v == cap {
			return true
		}
	}
	return false
}
func normalizeCaps(in []string, management string) ([]string, error) {
	seen := map[string]bool{}
	out := []string{}
	for _, group := range in {
		for _, v := range strings.Split(group, ",") {
			v = strings.TrimSpace(v)
			if v == "" {
				continue
			}
			switch v {
			case "inbox", "status", "steer", "interrupt", "stop", "rename", "model", "effort", managedControlCapability:
			default:
				return nil, workorders.Fail(400, "invalid capability")
			}
			if seen[v] {
				continue
			}
			seen[v] = true
			out = append(out, v)
		}
	}
	if management == "unmanaged" && (seen["interrupt"] || seen["stop"] || seen["rename"] || seen["model"] || seen["effort"] || seen[managedControlCapability]) {
		return nil, workorders.Fail(400, "unmanaged session cannot own controls")
	}
	sort.Strings(out)
	return out, nil
}
func validateTicket(ctx context.Context, tx pgx.Tx, projectID, ticketID string) error {
	if !workorders.UUID(ticketID) {
		return workorders.Fail(400, "invalid ticket id")
	}
	var ok bool
	err := tx.QueryRow(ctx, `WITH RECURSIVE chain AS (SELECT id,parent_id,kind_id,deleted_at FROM nodes WHERE id=$1 UNION ALL SELECT n.id,n.parent_id,n.kind_id,n.deleted_at FROM nodes n JOIN chain c ON n.id=c.parent_id) SELECT EXISTS(SELECT 1 FROM chain c JOIN node_kinds k ON k.id=c.kind_id WHERE c.id=$1 AND c.deleted_at IS NULL AND k.slug IN ('ticket','task','work_order')) AND EXISTS(SELECT 1 FROM chain WHERE id=$2 AND deleted_at IS NULL)`, ticketID, projectID).Scan(&ok)
	if err != nil {
		return err
	}
	if !ok {
		return workorders.Fail(400, "ticket must be a live node under project")
	}
	return nil
}
func validateParent(ctx context.Context, tx pgx.Tx, projectID, parentID, childID string) error {
	if parentID == "" {
		return nil
	}
	if !workorders.UUID(parentID) || parentID == childID {
		return workorders.Fail(400, "invalid parent session")
	}
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1::text,0))`, projectID); err != nil {
		return err
	}
	var active bool
	err := tx.QueryRow(ctx, `SELECT stopped_at IS NULL FROM harness_sessions WHERE project_id=$1 AND id=$2`, projectID, parentID).Scan(&active)
	if errors.Is(err, pgx.ErrNoRows) || !active {
		return workorders.Fail(409, "active same-project parent required")
	}
	if err != nil {
		return err
	}
	var depth int
	err = tx.QueryRow(ctx, `WITH RECURSIVE ancestors(id,parent_id,depth) AS (SELECT id,parent_id,1 FROM harness_sessions WHERE id=$1 UNION ALL SELECT p.id,p.parent_id,a.depth+1 FROM harness_sessions p JOIN ancestors a ON p.id=a.parent_id WHERE a.depth<17) SELECT coalesce(max(depth),0) FROM ancestors`, parentID).Scan(&depth)
	if err != nil {
		return err
	}
	if depth >= 16 {
		return workorders.Fail(409, "harness hierarchy exceeds 16 ancestors")
	}
	if childID != "" {
		var cycle bool
		var descendantDepth int
		err = tx.QueryRow(ctx, `WITH RECURSIVE descendants(id,depth) AS (SELECT id,1 FROM harness_sessions WHERE id=$1 UNION ALL SELECT c.id,d.depth+1 FROM harness_sessions c JOIN descendants d ON c.parent_id=d.id WHERE d.depth<17) SELECT coalesce(max(depth),0),coalesce(bool_or(id=$2),false) FROM descendants`, childID, parentID).Scan(&descendantDepth, &cycle)
		if err != nil {
			return err
		}
		if cycle {
			return workorders.Fail(409, "harness hierarchy cycle")
		}
		if depth+descendantDepth > 16 {
			return workorders.Fail(409, "harness hierarchy exceeds 16 ancestors")
		}
	}
	return nil
}

type registration struct {
	Generator  *string `json:"generator"`
	Command    *string `json:"command"`
	SucceedsID *string `json:"succeeds_session_id"`
	rules.ClientReport
	AgentPrincipalID string  `json:"agent_principal_id"`
	RunID            *string `json:"run_id"`
	TicketNodeID     *string `json:"ticket_node_id"`
	WorkOrderID      *string `json:"work_order_id"`
	ParentID         *string `json:"parent_harness_session_id"`
	Harness          string  `json:"harness"`
	Host             string  `json:"host"`
	DisplayLabel     *string `json:"display_label"`
	sessionText
	Management       string   `json:"management_mode"`
	Role             string   `json:"role"`
	WorkShape        string   `json:"work_shape"`
	Capabilities     []string `json:"advertised_capabilities"`
	SessionRef       string   `json:"harness_session_ref"`
	VendorSessionRef *string  `json:"vendor_session_ref"`
	WorkerLease      string   `json:"worker_lease"`
}

func (m *Module) register(r *http.Request, tx pgx.Tx, p tenant.Principal) (any, error) {
	ctx := r.Context()
	projectID := r.PathValue("projectId")
	if p.Kind == tenant.Agent {
		// A registered generation must be able to heartbeat and stop itself.
		if err := authz.RequireTx(ctx, tx, p, "harness.worker", authz.Scope{ProjectID: projectID}); err != nil {
			return nil, err
		}
	}
	if err := project(ctx, tx, projectID); err != nil {
		return nil, err
	}
	var in registration
	if err := workorders.Decode(r, &in); err != nil {
		return nil, err
	}
	if in.SucceedsID != nil {
		if !workorders.UUID(*in.SucceedsID) {
			return nil, workorders.Fail(400, "valid predecessor required")
		}
		if err := lockHierarchy(ctx, tx, projectID); err != nil {
			return nil, err
		}
		if err := inheritPauseRegistration(ctx, tx, projectID, &in); err != nil {
			return nil, err
		}
	}
	if !workorders.UUID(in.AgentPrincipalID) || !validHarness(in.Harness) || len(in.Host) < 1 || len(in.Host) > 128 || strings.TrimSpace(in.Host) != in.Host || len(in.SessionRef) < 16 || len(in.SessionRef) > 4096 || len(in.WorkerLease) < 32 || len(in.WorkerLease) > 256 || strings.ContainsAny(in.SessionRef+in.WorkerLease, "\r\n") || in.SessionRef == in.WorkerLease {
		return nil, workorders.Fail(400, "invalid harness registration")
	}
	var generator, command string
	if in.Generator != nil {
		generator = *in.Generator
	}
	if in.Command != nil {
		command = *in.Command
	}
	if err := runkind.Validate(in.Harness, generator, command); err != nil {
		return nil, workorders.Fail(400, err.Error())
	}
	if generator == "" {
		in.Generator = nil
	}
	if command == "" {
		in.Command = nil
	}
	if runkind.Process(in.Harness) {
		if in.Role != "worker" || in.ParentID == nil || in.TicketNodeID == nil {
			return nil, workorders.Fail(400, "media and terminal runs require a worker role, parent coordinator and bound ticket")
		}
		if in.Model != nil && *in.Model != "" || in.ReasoningEffort != nil && *in.ReasoningEffort != "" {
			return nil, workorders.Fail(400, "media and terminal runs use generator or command labels, not model or reasoning_effort")
		}
	}
	if in.DisplayLabel != nil {
		label := strings.TrimSpace(*in.DisplayLabel)
		if !utf8.ValidString(label) || utf8.RuneCountInString(label) > 128 || strings.ContainsFunc(*in.DisplayLabel, unicode.IsControl) {
			return nil, workorders.Fail(400, "display label must be at most 128 characters without control characters")
		}
		in.DisplayLabel = nil
		if label != "" {
			in.DisplayLabel = &label
		}
	}
	if err := in.ClientReport.Validate(); err != nil {
		return nil, workorders.Fail(400, err.Error())
	}
	if err := in.sessionText.normalize(); err != nil {
		return nil, err
	}
	if p.Kind == tenant.Agent && p.ID != in.AgentPrincipalID {
		return nil, workorders.Fail(403, "agent may register only itself")
	}
	if in.Management != "managed" && in.Management != "unmanaged" {
		return nil, workorders.Fail(400, "invalid management mode")
	}
	if in.Role != "worker" && in.Role != "coordinator" {
		return nil, workorders.Fail(400, "invalid hierarchy role")
	}
	if in.SucceedsID != nil && !workorders.UUID(*in.SucceedsID) {
		return nil, workorders.Fail(400, "valid predecessor required")
	}
	caps, err := normalizeCaps(in.Capabilities, in.Management)
	if err != nil {
		return nil, err
	}
	if (in.TicketNodeID == nil) != (in.WorkShape == "" || in.WorkShape == "unknown") {
		return nil, workorders.Fail(400, "ticket and work shape must be bound together")
	}
	if in.TicketNodeID == nil {
		in.WorkShape = "unknown"
	} else {
		if !validShape(in.WorkShape) {
			return nil, workorders.Fail(400, "invalid work shape")
		}
		if err = validateTicket(ctx, tx, projectID, *in.TicketNodeID); err != nil {
			return nil, err
		}
	}
	var agentKind string
	err = tx.QueryRow(ctx, `SELECT kind FROM principals WHERE id=$1`, in.AgentPrincipalID).Scan(&agentKind)
	if err != nil {
		return nil, err
	}
	if agentKind != "agent" {
		return nil, workorders.Fail(400, "agent principal required")
	}
	if in.RunID != nil {
		var agentID, orderID string
		err = tx.QueryRow(ctx, `SELECT agent_principal_id::text,work_order_id::text FROM agent_runs WHERE id=$1`, *in.RunID).Scan(&agentID, &orderID)
		if err != nil {
			return nil, err
		}
		if agentID != in.AgentPrincipalID || in.WorkOrderID == nil || *in.WorkOrderID != orderID {
			return nil, workorders.Fail(409, "run and work order binding conflict")
		}
	}
	if in.WorkOrderID != nil {
		var orderNode string
		err = tx.QueryRow(ctx, `SELECT node_id::text FROM work_orders WHERE node_id=$1`, *in.WorkOrderID).Scan(&orderNode)
		if err != nil {
			return nil, err
		}
		if err = validateTicket(ctx, tx, projectID, orderNode); err != nil {
			return nil, err
		}
	}
	metaDigest, err := registrationMetadataDigest(in.sessionText)
	if err != nil {
		return nil, err
	}
	if err = lockHierarchy(ctx, tx, projectID); err != nil {
		return nil, err
	}
	ref, lease := digest("ref", in.SessionRef), digest("lease", in.WorkerLease)
	vendor, err := vendorRefDigest(in.SessionRef, in.WorkerLease, in.VendorSessionRef)
	if err != nil {
		return nil, err
	}
	existing, err := scanSession(tx.QueryRow(ctx, `SELECT `+sessionColumns+` FROM harness_sessions WHERE project_id=$1 AND ref_digest=$2 AND stopped_at IS NULL FOR UPDATE`, projectID, ref))
	if err == nil && subtle.ConstantTimeCompare(existing.leaseDigest, lease) != 1 && in.Role == "coordinator" && existing.Role == "coordinator" && existing.AgentPrincipalID == in.AgentPrincipalID && existing.Harness == in.Harness {
		stale, e := heartbeatExpired(ctx, tx, existing)
		if e != nil {
			return nil, e
		}
		if stale {
			if _, e = closeGeneration(ctx, tx, p, existing, StopReasonHeartbeatLost); e != nil {
				return nil, e
			}
			err = pgx.ErrNoRows
		}
	}
	if err == nil {
		if in.SucceedsID != nil {
			var replay bool
			if e := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM harness_sessions WHERE project_id=$1 AND id=$2 AND handed_over_to_id=$3)`, projectID, *in.SucceedsID, existing.ID).Scan(&replay); e != nil {
				return nil, e
			}
			if !replay {
				return nil, workorders.Fail(409, "successor registration conflicts")
			}
		}
		if subtle.ConstantTimeCompare(existing.leaseDigest, lease) != 1 {
			return nil, workorders.Fail(409, RegistrationLeaseConflict)
		}
		if existing.AgentPrincipalID != in.AgentPrincipalID || existing.Harness != in.Harness || !same(existing.Generator, in.Generator) || !same(existing.Command, in.Command) || existing.Host != in.Host || existing.Management != in.Management || existing.Role != in.Role || existing.WorkShape != in.WorkShape || !same(existing.DisplayLabel, in.DisplayLabel) || !sameRegistrationMetadata(existing, in.sessionText, metaDigest) || !same(existing.ParentID, in.ParentID) || !same(existing.TicketNodeID, in.TicketNodeID) || !same(existing.RunID, in.RunID) || !same(existing.WorkOrderID, in.WorkOrderID) || !sameCaps(existing.Capabilities, caps) {
			return nil, workorders.Fail(409, "active generation conflicts with registration: harness_session_ref is already active with different registration metadata")
		}
		if err = fillVendorRef(ctx, tx, &existing, vendor); err != nil {
			return nil, err
		}
		return existing, rules.RecordClientReport(ctx, tx, existing.ID, in.ClientReport)
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}
	predecessor, err := handoverPredecessor(ctx, tx, projectID, in, ref)
	if err != nil {
		return nil, err
	}
	if predecessor != nil && predecessor.Pause != nil && predecessor.Pause.State == "resume_requested" {
		if subtle.ConstantTimeCompare(predecessor.refDigest, ref) == 1 || subtle.ConstantTimeCompare(predecessor.leaseDigest, lease) == 1 {
			return nil, workorders.Fail(409, "continuation requires fresh session reference and worker lease")
		}
		if p.Kind == tenant.Person {
			owner, admin, parent, e := pauseController(r, tx, p, "")
			if e != nil {
				return nil, e
			}
			if !pauseAllowed(*predecessor, owner, admin, parent) {
				return nil, workorders.Fail(403, "paused registration owner required")
			}
		}
	}
	// An archived worker generation cannot resurrect by replaying registration.
	var revoked bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM harness_sessions WHERE project_id=$1 AND agent_principal_id=$2 AND archived_at IS NOT NULL AND (ref_digest=$3 OR lease_digest=$4))`, projectID, in.AgentPrincipalID, ref, lease).Scan(&revoked); err != nil {
		return nil, err
	}
	if revoked {
		return nil, workorders.Fail(409, "archived generation revoked; use a new session reference and worker lease")
	}
	if in.ParentID != nil {
		if runkind.Process(in.Harness) {
			var coordinator bool
			if err = tx.QueryRow(ctx, `SELECT role='coordinator' FROM harness_sessions WHERE project_id=$1 AND id=$2`, projectID, *in.ParentID).Scan(&coordinator); err != nil || !coordinator {
				return nil, workorders.Fail(400, "--parent-session must name a visible coordinator")
			}
		}
		if err = validateParent(ctx, tx, projectID, *in.ParentID, ""); err != nil {
			return nil, err
		}
	}
	identity, err := resolveModelIdentity(ctx, tx, in.Harness, in.Model, in.ReasoningEffort)
	if err != nil {
		return nil, err
	}
	s, err := scanSession(tx.QueryRow(ctx, `INSERT INTO harness_sessions(tenant_id,project_id,agent_principal_id,run_id,ticket_node_id,work_order_id,parent_id,harness,host,management,role,work_shape,capabilities,ref_digest,lease_digest,display_label,model,reasoning_effort,account_label,harness_version,brief,worktree,branch,registration_metadata_digest,vendor_ref_digest,model_raw,model_profile_id,generator,command) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21,$22,$23,$24,$25,$26,$27,$28,$29) RETURNING `+sessionColumns, p.TenantID, projectID, in.AgentPrincipalID, in.RunID, in.TicketNodeID, in.WorkOrderID, in.ParentID, in.Harness, in.Host, in.Management, in.Role, in.WorkShape, caps, ref, lease, in.DisplayLabel, identity.Model, identity.Effort, in.AccountLabel, in.HarnessVersion, in.Brief, in.Worktree, in.Branch, metaDigest, vendor, in.Model, identity.ProfileID, in.Generator, in.Command))
	if err != nil {
		return nil, registrationConflict(err)
	}
	owner := p.KeyCreatorID
	if p.Kind == tenant.Person {
		owner = p.ID
	}
	if owner != "" {
		if err = tx.QueryRow(ctx, `UPDATE harness_sessions SET owner_principal_id=(SELECT coalesce(linked_to,id) FROM principals WHERE id=$2 AND kind='person') WHERE id=$1 RETURNING owner_principal_id::text,row_version`, s.ID, owner).Scan(&s.ownerID, &s.RowVersion); err != nil {
			return nil, err
		}
	}
	if s.TicketNodeID != nil && m.planningStart != nil {
		if err = m.planningStart(ctx, tx, *s.TicketNodeID, "session"); err != nil {
			return nil, err
		}
	}
	if err = record(ctx, tx, p, s, "registered", nil, s); err != nil {
		return nil, err
	}
	if err = rules.RecordClientReport(ctx, tx, s.ID, in.ClientReport); err != nil {
		return nil, err
	}
	if predecessor != nil {
		if predecessor.Pause != nil && predecessor.Pause.State == "resume_requested" {
			s, err = completeResume(ctx, tx, p, *predecessor, s)
		} else {
			err = adoptChildren(ctx, tx, p, *predecessor, s)
		}
		if err != nil {
			return nil, err
		}
	}
	return s, nil
}
func same(a, b *string) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}
func sameCaps(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
func registrationMetadataDigest(in sessionText) ([]byte, error) {
	raw, err := json.Marshal(in)
	if err != nil {
		return nil, err
	}
	return digest("registration-metadata", string(raw)), nil
}
func sameRegistrationMetadata(existing Session, in sessionText, expectedDigest []byte) bool {
	if existing.registrationMetaDigest != nil {
		return subtle.ConstantTimeCompare(existing.registrationMetaDigest, expectedDigest) == 1
	}
	// Rows created before 0881 registered without these fields. A later
	// heartbeat can fill them, so only an empty registration is their replay.
	return in.Model == nil && in.ReasoningEffort == nil && in.AccountLabel == nil && in.HarnessVersion == nil && in.Brief == nil && in.Worktree == nil && in.Branch == nil
}
func (m *Module) list(r *http.Request, tx pgx.Tx, p tenant.Principal) (any, error) {
	id := r.PathValue("projectId")
	if err := project(r.Context(), tx, id); err != nil {
		return nil, err
	}
	rows, err := tx.Query(r.Context(), `SELECT `+sessionColumns+` FROM harness_sessions WHERE project_id=$1 ORDER BY created_at DESC,id LIMIT 200`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Session{}
	for rows.Next() {
		s, e := scanSession(rows)
		if e != nil {
			return nil, e
		}
		out = append(out, s)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	ids := make([]string, len(out))
	for i := range out {
		ids[i] = out[i].ID
	}
	evidence, err := readStateEvidence(r.Context(), tx, ids)
	if err != nil {
		return nil, err
	}
	ptrs := make([]*Session, len(out))
	for i := range out {
		out[i].StateEvidence = evidence[out[i].ID]
		ptrs[i] = &out[i]
	}
	if err = m.stampSessions(r.Context(), tx, ptrs); err != nil {
		return nil, err
	}
	return out, nil
}
func (m *Module) status(r *http.Request, tx pgx.Tx, p tenant.Principal) (any, error) {
	s, err := load(r.Context(), tx, r.PathValue("projectId"), r.PathValue("sessionId"), true)
	if err != nil {
		return nil, err
	}
	if s, err = expirePause(r.Context(), tx, p, s); err != nil {
		return nil, err
	}
	s.Watch, err = readAttachStatus(r.Context(), tx, s.ID)
	if err != nil {
		return nil, err
	}

	rows, err := tx.Query(r.Context(), `SELECT note,created_at FROM harness_activity_notes WHERE session_id=$1 ORDER BY id DESC LIMIT 20`, s.ID)
	if err != nil {
		return nil, err
	}
	s.ActivityHistory = []ActivityNote{}
	for rows.Next() {
		var item ActivityNote
		if err = rows.Scan(&item.Note, &item.At); err != nil {
			rows.Close()
			return nil, err
		}
		s.ActivityHistory = append(s.ActivityHistory, item)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	changeRows, err := tx.Query(r.Context(), `SELECT field,previous_value,value,created_at FROM harness_metadata_changes WHERE session_id=$1 ORDER BY id DESC LIMIT 20`, s.ID)
	if err != nil {
		return nil, err
	}
	history := []MetadataChange{}
	for changeRows.Next() {
		var change MetadataChange
		if err = changeRows.Scan(&change.Field, &change.PreviousValue, &change.Value, &change.At); err != nil {
			changeRows.Close()
			return nil, err
		}
		history = append(history, change)
	}
	err = changeRows.Err()
	changeRows.Close()
	if err != nil {
		return nil, err
	}
	s.MetadataHistory = &history
	evidence, err := readStateEvidence(r.Context(), tx, []string{s.ID})
	if err != nil {
		return nil, err
	}
	s.StateEvidence = evidence[s.ID]
	if err = m.stampSessions(r.Context(), tx, []*Session{&s}); err != nil {
		return nil, err
	}
	controls, err := readSessionRequests(r.Context(), tx, p, s)
	if err != nil {
		return nil, err
	}
	s.Controls = controls
	return reporterSession(s), nil
}

// lookup answers the ambient tell check for the caller's own generation.
// Another principal's row is the same not-found as a missing id: one query,
// with no second read that could differ in timing or body. It does not lock
// the row or load activity, history, or controls.
func (m *Module) lookup(r *http.Request, tx pgx.Tx, p tenant.Principal) (any, error) {
	projectID, id := r.PathValue("projectId"), r.PathValue("sessionId")
	if !workorders.UUID(projectID) {
		return nil, workorders.Fail(400, "invalid project id")
	}
	if !workorders.UUID(id) {
		return nil, workorders.Fail(400, "invalid session id")
	}
	var out sessionLookup
	err := tx.QueryRow(r.Context(), `SELECT id::text, project_id::text, agent_principal_id::text, stopped_at, archived_at FROM harness_sessions WHERE project_id=$1 AND id=$2 AND agent_principal_id=$3`, projectID, id, p.ID).Scan(&out.ID, &out.ProjectID, &out.AgentPrincipalID, &out.StoppedAt, &out.ArchivedAt)
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (m *Module) orchestrator(r *http.Request, tx pgx.Tx, p tenant.Principal) (any, error) {
	id := r.PathValue("projectId")
	if err := project(r.Context(), tx, id); err != nil {
		return nil, err
	}
	rows, err := tx.Query(r.Context(), `SELECT `+sessionColumns+` FROM harness_sessions WHERE project_id=$1 AND role='coordinator' AND management='managed' AND phase IN ('working','yielded') AND activity IN ('busy','idle','throttled') AND heartbeat_at>clock_timestamp()-interval '2 minutes' AND stopped_at IS NULL`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	found := []Session{}
	for rows.Next() {
		s, e := scanSession(rows)
		if e != nil {
			return nil, e
		}
		found = append(found, s)
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	if len(found) == 1 {
		evidence, err := readStateEvidence(r.Context(), tx, []string{found[0].ID})
		if err != nil {
			return nil, err
		}
		found[0].StateEvidence = evidence[found[0].ID]
		return map[string]any{"state": "resolved", "session": found[0]}, nil
	}
	if len(found) > 1 {
		return map[string]any{"state": "ambiguous"}, nil
	}
	return map[string]any{"state": "unset"}, nil
}
func (m *Module) bind(r *http.Request, tx pgx.Tx, p tenant.Principal) (any, error) {
	var in struct {
		ExpectedRevision int64   `json:"expected_revision"`
		ParentID         *string `json:"parent_harness_session_id"`
		TicketNodeID     *string `json:"ticket_node_id"`
		WorkShape        string  `json:"work_shape"`
	}
	if err := workorders.Decode(r, &in); err != nil {
		return nil, err
	}
	if in.ExpectedRevision < 1 {
		return nil, workorders.Fail(400, "expected revision required")
	}
	ctx := r.Context()
	if err := lockHierarchy(ctx, tx, r.PathValue("projectId")); err != nil {
		return nil, err
	}
	s, err := load(ctx, tx, r.PathValue("projectId"), r.PathValue("sessionId"), true)
	if err != nil {
		return nil, err
	}
	if p.Kind == tenant.Agent && p.ID != s.AgentPrincipalID {
		return nil, workorders.Fail(403, "agent may bind only its own session")
	}
	if s.StoppedAt != nil || s.Revision != in.ExpectedRevision {
		return nil, workorders.Fail(409, "session revision conflict")
	}
	if in.TicketNodeID == nil {
		if in.WorkShape != "unknown" {
			return nil, workorders.Fail(400, "detached ticket needs unknown shape")
		}
	} else {
		if !validShape(in.WorkShape) {
			return nil, workorders.Fail(400, "invalid work shape")
		}
		if err = validateTicket(ctx, tx, s.ProjectID, *in.TicketNodeID); err != nil {
			return nil, err
		}
	}
	if in.ParentID != nil {
		if err = validateParent(ctx, tx, s.ProjectID, *in.ParentID, s.ID); err != nil {
			return nil, err
		}
	}
	if p.Kind == tenant.Person && !same(s.ParentID, in.ParentID) {
		if err = authorizeMove(ctx, tx, p, s, in.ParentID); err != nil {
			return nil, err
		}
	}
	before := s
	s, err = scanSession(tx.QueryRow(ctx, `UPDATE harness_sessions SET parent_id=$2,ticket_node_id=$3,work_shape=$4,
		eta_ready_at=CASE WHEN ticket_node_id IS DISTINCT FROM $3::uuid THEN NULL ELSE eta_ready_at END,
		eta_live_at=CASE WHEN ticket_node_id IS DISTINCT FROM $3::uuid THEN NULL ELSE eta_live_at END,
		progress_pct=CASE WHEN ticket_node_id IS DISTINCT FROM $3::uuid THEN NULL ELSE progress_pct END,
		missing_progress_beats=CASE WHEN ticket_node_id IS DISTINCT FROM $3::uuid THEN 0 ELSE missing_progress_beats END,
		eta_reported_at=CASE WHEN ticket_node_id IS DISTINCT FROM $3::uuid THEN NULL ELSE eta_reported_at END,
		revision=revision+1 WHERE id=$1 RETURNING `+sessionColumns, s.ID, in.ParentID, in.TicketNodeID, in.WorkShape))
	if err != nil {
		return nil, err
	}
	if s.TicketNodeID != nil && m.planningStart != nil && !same(before.TicketNodeID, s.TicketNodeID) {
		if err = m.planningStart(ctx, tx, *s.TicketNodeID, "session"); err != nil {
			return nil, err
		}
	}
	return s, record(ctx, tx, p, s, "bound", before, s)
}
func (m *Module) heartbeat(r *http.Request, tx pgx.Tx, p tenant.Principal) (any, error) {
	var in struct {
		rules.ClientReport
		ProcessOwnership *ownedprocess.Identity `json:"process_ownership"`
		Phase            string                 `json:"phase"`
		Activity         string                 `json:"activity"`
		ActivitySequence int64                  `json:"activity_sequence"`
		ActivityNote     *string                `json:"activity_note"`
		DisplayLabel     json.RawMessage        `json:"display_label"`
		EtaReadyAt       json.RawMessage        `json:"eta_ready_at"`
		EtaLiveAt        json.RawMessage        `json:"eta_live_at"`
		ProgressPct      json.RawMessage        `json:"progress_pct"`
		sessionText
		Commits []Commit `json:"commits"`
	}
	if err := workorders.Decode(r, &in); err != nil {
		return nil, err
	}
	if !validPhase(in.Phase) || in.ActivitySequence < 0 {
		return nil, workorders.Fail(400, "invalid heartbeat")
	}
	ctx := r.Context()
	s, err := worker(ctx, tx, r, p)
	if err != nil && lostContact(s) && leaseProof(s, r, p) {
		// AEON-291: the sweeper closed a silent generation; its own worker is back.
		s, err = revive(ctx, tx, p, s, in.Phase)
	}
	if err != nil {
		return nil, err
	}
	if in.ProcessOwnership != nil {
		if err := m.reportOwnership(ctx, tx, s, *in.ProcessOwnership); err != nil {
			return nil, err
		}
	}
	if in.Activity == "" {
		in.Activity = s.Activity
	}
	if in.Activity != "unknown" && in.Activity != "busy" && in.Activity != "idle" && in.Activity != "throttled" {
		return nil, workorders.Fail(400, "invalid activity")
	}
	if in.ActivitySequence < s.ActivitySequence {
		return nil, workorders.Fail(409, "stale activity sequence")
	}
	if in.ActivitySequence == s.ActivitySequence && in.Activity != s.Activity {
		return nil, workorders.Fail(409, "divergent activity replay")
	}
	if in.ActivityNote != nil {
		note, valid := normalizeActivityNote(*in.ActivityNote)
		if !valid {
			return nil, workorders.Fail(400, "activity note must be at most 120 characters")
		}
		in.ActivityNote = &note
	}
	if err := in.ClientReport.Validate(); err != nil {
		return nil, workorders.Fail(400, err.Error())
	}
	if runkind.Process(s.Harness) && (in.Model != nil && *in.Model != "" || in.ReasoningEffort != nil && *in.ReasoningEffort != "") {
		return nil, workorders.Fail(400, "media and terminal runs use generator or command labels, not model or reasoning_effort")
	}
	if err := in.sessionText.normalize(); err != nil {
		return nil, err
	}
	label := s.DisplayLabel
	if in.DisplayLabel != nil {
		if string(in.DisplayLabel) == "null" {
			label = nil
		} else {
			var raw string
			if err := json.Unmarshal(in.DisplayLabel, &raw); err != nil {
				return nil, workorders.Fail(400, "invalid display label")
			}
			clean := strings.TrimSpace(raw)
			if !utf8.ValidString(raw) || utf8.RuneCountInString(clean) > 128 || strings.ContainsFunc(raw, unicode.IsControl) {
				return nil, workorders.Fail(400, "display label must be at most 128 characters without control characters")
			}
			label = nil
			if clean != "" {
				label = &clean
			}
		}
	}
	if err := validCommits(in.Commits); err != nil {
		return nil, err
	}
	note := s.ActivityNote
	changed := in.ActivityNote != nil && (note == nil || *note != *in.ActivityNote)
	if in.ActivityNote != nil {
		note = in.ActivityNote
	}
	before := s
	commits, err := json.Marshal(appendCommits(s.Commits, in.Commits))
	if err != nil {
		return nil, err
	}
	modelRaw, identity := s.ModelRaw, modelIdentity{s.Model, s.ReasoningEffort, s.ModelProfileID}
	if in.Model != nil || in.ReasoningEffort != nil {
		model, effort := s.Model, s.ReasoningEffort
		if in.Model != nil {
			model, modelRaw = in.Model, in.Model
		} else if modelRaw == nil {
			// Unresolved legacy rows may still hold an effort suffix in model.
			// Preserve that original string before normalizing the identity.
			modelRaw = s.Model
		}
		if in.ReasoningEffort != nil {
			effort = in.ReasoningEffort
		} else if in.Model != nil {
			// Prefer a newly reported suffix, otherwise keep the existing effort
			// as previous clients expect from a model-only heartbeat.
			derived, e := resolveModelIdentity(ctx, tx, s.Harness, model, nil)
			if e != nil {
				return nil, e
			}
			if derived.Effort != nil {
				effort = derived.Effort
			}
		}
		identity, err = resolveModelIdentity(ctx, tx, s.Harness, model, effort)
		if err != nil {
			return nil, err
		}
	}
	s, err = scanSession(tx.QueryRow(ctx, `UPDATE harness_sessions SET phase=$2,activity=$3,activity_sequence=$4,heartbeat_at=clock_timestamp(),activity_note=$5,model=$6,reasoning_effort=$7,account_label=coalesce($8,account_label),harness_version=coalesce($9,harness_version),brief=coalesce($10,brief),worktree=coalesce($11,worktree),branch=coalesce($12,branch),commits=$13::jsonb,display_label=$14,model_raw=$15,model_profile_id=$16 WHERE id=$1 RETURNING `+sessionColumns, s.ID, in.Phase, in.Activity, in.ActivitySequence, note, identity.Model, identity.Effort, in.AccountLabel, in.HarnessVersion, in.Brief, in.Worktree, in.Branch, string(commits), label, modelRaw, identity.ProfileID))
	if err != nil {
		return nil, err
	}
	if s, err = expirePause(ctx, tx, p, s); err != nil {
		return nil, err
	}
	if err := recordMetadataChanges(ctx, tx, p.TenantID, before, s); err != nil {
		return nil, err
	}
	if changed && note != nil {
		if _, err = tx.Exec(ctx, `INSERT INTO harness_activity_notes(tenant_id,session_id,note) VALUES($1,$2,$3)`, p.TenantID, s.ID, *note); err != nil {
			return nil, err
		}
		if _, err = tx.Exec(ctx, `DELETE FROM harness_activity_notes WHERE tenant_id=$1 AND session_id=$2 AND id NOT IN (SELECT id FROM harness_activity_notes WHERE tenant_id=$1 AND session_id=$2 ORDER BY id DESC LIMIT 20)`, p.TenantID, s.ID); err != nil {
			return nil, err
		}
	}
	s, err = applyEstimate(ctx, tx, p, s, in.EtaReadyAt, in.EtaLiveAt, in.ProgressPct)
	if err != nil {
		return nil, err
	}
	if err = rules.RecordClientReport(ctx, tx, s.ID, in.ClientReport); err != nil {
		return nil, err
	}
	if err = record(ctx, tx, p, s, "heartbeat", before, s); err != nil {
		return nil, err
	}
	if err = m.stampSessions(ctx, tx, []*Session{&s}); err != nil {
		return nil, err
	}
	warnings, err := heartbeatEstimateWarnings(ctx, tx, s, in.ProgressPct)
	if err != nil {
		slog.Warn("harness heartbeat guidance failed", "session_id", s.ID, "error", err)
		warnings = []EstimateWarning{}
	}
	return heartbeatResponse{reporterSession(s), warnings}, nil
}
func (m *Module) markStopped(r *http.Request, tx pgx.Tx, p tenant.Principal) (any, error) {
	var in struct {
		Reason   string    `json:"reason"`
		Handover *Handover `json:"handover"`
	}
	if err := workorders.Decode(r, &in); err != nil {
		return nil, err
	}
	switch in.Reason {
	case "stopped", "process_exited", "process_failed", "ownership_lost", "force_stopped", "token_budget_exhausted", "turn_budget_exhausted", "paused":
	default:
		return nil, workorders.Fail(400, "invalid stop reason")
	}
	ctx := r.Context()
	s, err := worker(ctx, tx, r, p)
	if in.Reason == "paused" {
		if err != nil && s.ArchivedAt == nil && s.Pause != nil && s.StopReason != nil && *s.StopReason == "paused" && leaseProof(s, r, p) {
			if in.Handover == nil {
				return s, nil
			}
			raw, _ := json.Marshal(in.Handover)
			previous, _ := json.Marshal(s.Pause.Handover)
			if string(raw) == string(previous) {
				return s, nil
			}
			return nil, workorders.Fail(409, "divergent paused stop replay")
		}
		if err != nil {
			return nil, err
		}
		return finishPause(ctx, tx, p, s, in.Handover)
	}
	if in.Handover != nil {
		return nil, workorders.Fail(400, "handover requires stop reason paused")
	}
	if err != nil && lostContact(s) && leaseProof(s, r, p) {
		// The server already closed this silent generation; the worker's own
		// reason replaces "lost contact". Its obligations and leases are closed.
		before := s
		if s, err = scanSession(tx.QueryRow(ctx, `UPDATE harness_sessions SET stop_reason=$2,revision=revision+1 WHERE id=$1 RETURNING `+sessionColumns, s.ID, in.Reason)); err != nil {
			return nil, err
		}
		return s, record(ctx, tx, p, s, "stop_confirmed", before, s)
	}
	if err != nil {
		return nil, err
	}
	return closeGeneration(ctx, tx, p, s, in.Reason)
}

func closeGeneration(ctx context.Context, tx pgx.Tx, p tenant.Principal, s Session, reason string) (Session, error) {
	if reason != "paused" && reason != StopReasonHeartbeatLost && s.Pause != nil && (s.Pause.State == "requested" || s.Pause.State == "planned") {
		next := *s.Pause
		next.State = "cancelled"
		var err error
		s, err = savePause(ctx, tx, p, s, next, "pause_cancelled")
		if err != nil {
			return s, err
		}
	}
	before := s
	var preservedPause *string
	if reason == StopReasonHeartbeatLost && s.Pause != nil && (s.Pause.State == "requested" || s.Pause.State == "planned") {
		preservedPause = &s.Pause.ControlID
	}
	rows, err := tx.Query(ctx, `SELECT id::text FROM harness_controls WHERE session_id=$1 AND state<>'completed' AND ($2::uuid IS NULL OR id<>$2::uuid) ORDER BY sequence FOR UPDATE`, s.ID, preservedPause)
	if err != nil {
		return Session{}, err
	}
	ids := []string{}
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return Session{}, err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return Session{}, err
	}
	for _, id := range ids {
		beforeControl, e := scanControl(tx.QueryRow(ctx, `SELECT `+controlColumns+` FROM harness_controls WHERE id=$1`, id))
		if e != nil {
			return Session{}, e
		}
		afterControl, e := scanControl(tx.QueryRow(ctx, `UPDATE harness_controls SET state='completed',outcome='rejected',reason='ownership_lost',claimed_at=coalesce(claimed_at,clock_timestamp()),completed_at=clock_timestamp() WHERE id=$1 RETURNING `+controlColumns, id))
		if e != nil {
			return Session{}, e
		}
		if e = record(ctx, tx, p, s, "control_completed", beforeControl, afterControl); e != nil {
			return Session{}, e
		}
	}
	// Undelivered messages bound to this generation fail now, loudly (AEON-280).
	// This runs before the lease release below: message rows are locked before
	// delivery rows on every inbox path.
	if err = inbox.FailSessionMessages(ctx, tx, p.TenantID, s.ID); err != nil {
		return Session{}, err
	}
	leaseRows, err := tx.Query(ctx, `SELECT id::text,message_id::text,cursor FROM harness_deliveries WHERE session_id=$1 AND completed_at IS NULL AND released_at IS NULL FOR UPDATE`, s.ID)
	if err != nil {
		return Session{}, err
	}
	type leaseRef struct {
		id, message string
		cursor      int64
	}
	leases := []leaseRef{}
	for leaseRows.Next() {
		var v leaseRef
		if err = leaseRows.Scan(&v.id, &v.message, &v.cursor); err != nil {
			leaseRows.Close()
			return Session{}, err
		}
		leases = append(leases, v)
	}
	err = leaseRows.Err()
	leaseRows.Close()
	if err != nil {
		return Session{}, err
	}
	for _, v := range leases {
		if _, err = tx.Exec(ctx, `UPDATE harness_deliveries SET released_at=clock_timestamp() WHERE id=$1`, v.id); err != nil {
			return Session{}, err
		}
		if err = record(ctx, tx, p, s, "delivery_released", map[string]any{"delivery_id": v.id, "message_id": v.message, "cursor": v.cursor}, map[string]any{"delivery_id": v.id, "message_id": v.message, "cursor": v.cursor, "released": true}); err != nil {
			return Session{}, err
		}
	}
	s, err = scanSession(tx.QueryRow(ctx, `UPDATE harness_sessions SET phase='stopped',stopped_at=coalesce(stopped_at,clock_timestamp()),stop_reason=coalesce(stop_reason,$2) WHERE id=$1 RETURNING `+sessionColumns, s.ID, reason))
	if err != nil {
		return Session{}, err
	}
	return s, record(ctx, tx, p, s, "stopped", before, s)
}

// Plugin is the compiled registry declaration. Harness HTTP scopes are enforced
// by the R2 key system, independently of plugin execution permissions.
func Plugin() (plugins.Plugin, error) {
	p := plugins.Plugin{Manifest: plugins.Manifest{ID: "harness", Version: "1", Owner: "aeon"}}
	digest, err := plugins.Digest(p)
	if err != nil {
		return p, err
	}
	p.Manifest.DigestSHA256 = digest
	return p, nil
}
