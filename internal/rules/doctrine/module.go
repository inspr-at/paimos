// SPDX-License-Identifier: AGPL-3.0-only

// Package doctrine shows the INSPR doctrine as a git-backed, read-only rule
// layer (AEON-318). Git is the source of truth: each workspace names the
// repositories it indexes and the commit each is pinned to. Rules are indexed
// straight from the blobs at that commit with the AEON-250 importer grammar.
// The only doctrine bytes Aeon holds are a cache keyed by that commit, which
// the database empties when the pin moves. Proposals edit the owning git
// repository through a PR (AEON-319), never this cache. TL;DRs live in git
// next to each file and are changed in the same proposal as their rule.
package doctrine

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/events"
	"github.com/inspr-at/paimos/internal/httpapi"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/inspr-at/paimos/internal/workorders"
)

// Options configure the server side. CredentialsDir is
// AEON_DOCTRINE_CREDENTIALS_DIR; Client allows test transport injection.
// GuardKey is the server secret for per-tenant HMAC of the private quotation
// guard. It is copied, never logged, and never written to the database.
type Options struct {
	CredentialsDir string
	Client         *http.Client
	App            AppConfig
	GuardKey       []byte
}

// Module serves the doctrine layer.
type Module struct {
	pool        *pgxpool.Pool
	credentials Credentials
	client      *http.Client
	app         AppConfig
	guardMaster []byte
}

var _ httpapi.Module = (*Module)(nil)

func New(pool *pgxpool.Pool, opts Options) *Module {
	var key []byte
	if len(opts.GuardKey) >= 32 {
		key = append([]byte(nil), opts.GuardKey...)
	}
	return &Module{pool: pool, credentials: Credentials{Dir: opts.CredentialsDir}, client: opts.Client, app: opts.App, guardMaster: key}
}

func (m *Module) guardKey(tenantID string) []byte {
	if m == nil {
		return nil
	}
	return deriveGuardKey(m.guardMaster, tenantID)
}

// fetchTimeout bounds one resolve or index against the repository host.
const fetchTimeout = 45 * time.Second

func (m *Module) Mount(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/rules/doctrine", m.handle(m.layer))
	mux.HandleFunc("GET /api/rules/doctrine/proposals", m.handle(m.listProposals))
	mux.HandleFunc("POST /api/rules/doctrine/proposals", m.handle(m.propose))
	mux.HandleFunc("POST /api/rules/doctrine/proposals/{proposalId}/refresh", m.handle(m.refreshProposal))
	mux.HandleFunc("POST /api/rules/doctrine/proposals/{proposalId}/approve", m.handle(m.approveProposal))
	mux.HandleFunc("POST /api/rules/doctrine/proposals/{proposalId}/pins", m.handle(m.reportPin))
	mux.HandleFunc("POST /api/rules/doctrine/sources", m.handle(m.create))
	mux.HandleFunc("PUT /api/rules/doctrine/sources/{sourceId}", m.handle(m.update))
	mux.HandleFunc("DELETE /api/rules/doctrine/sources/{sourceId}", m.handle(m.remove))
	mux.HandleFunc("POST /api/rules/doctrine/sources/{sourceId}/index", m.handle(m.reindex))
}

// ---------- Errors ----------

type failure struct {
	Status  int    `json:"-"`
	Code    string `json:"code"`
	Message string `json:"error"`
}

func (f *failure) Error() string { return f.Message }

func fail(status int, code, message string) error {
	return &failure{Status: status, Code: code, Message: message}
}

func (m *Module) handle(fn func(*http.Request, tenant.Principal) (any, error)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		p, ok := tenant.PrincipalFrom(r.Context())
		if !ok || !workorders.UUID(p.ID) || !workorders.UUID(p.TenantID) {
			writeFailure(w, fail(401, "unauthorized", "authentication required"))
			return
		}
		if p.Kind != tenant.Person && p.Kind != tenant.Agent {
			writeFailure(w, authz.ErrForbidden)
			return
		}
		if id := r.PathValue("sourceId"); id != "" && (!workorders.UUID(id) || strings.ToLower(id) != id) {
			writeFailure(w, fail(400, "invalid_request", "invalid source UUID"))
			return
		}
		if id := r.PathValue("proposalId"); id != "" && (!workorders.UUID(id) || strings.ToLower(id) != id) {
			writeFailure(w, fail(400, "invalid_request", "invalid proposal UUID"))
			return
		}
		if m.pool == nil {
			writeFailure(w, fail(503, "unavailable", "the doctrine layer is unavailable"))
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
		out, err := fn(r, p)
		if err != nil {
			// A transaction timeout can follow a successful external write.
			// Never promise "nothing changed" for a proposal operation.
			if strings.HasPrefix(r.URL.Path, "/api/rules/doctrine/proposals") {
				var known *failure
				var decode *workorders.Error
				if !errors.As(err, &known) && !errors.As(err, &decode) && !errors.Is(err, authz.ErrForbidden) && !errors.Is(err, errNoSource) && !errors.Is(err, ErrCredential) && !errors.Is(err, ErrGit) {
					err = fail(503, "outcome_unknown", "The proposal outcome was not confirmed. Refresh, or retry the identical request UUID and input; GitHub may have accepted it.")
				}
			}
			writeFailure(w, err)
			return
		}
		httpapi.WriteJSON(w, http.StatusOK, out)
	}
}

func writeFailure(w http.ResponseWriter, err error) {
	var f *failure
	var we *workorders.Error
	var pe *pgconn.PgError
	switch {
	case errors.As(err, &f):
	case errors.Is(err, authz.ErrForbidden):
		f = &failure{Status: 403, Code: "forbidden", Message: "permission denied"}
	case errors.Is(err, errNoSource):
		f = &failure{Status: 404, Code: "not_found", Message: "that doctrine repository is not configured"}
	case errors.As(err, &we):
		f = &failure{Status: we.Status, Code: "invalid_request", Message: we.Message}
	case errors.Is(err, ErrGit), errors.Is(err, ErrCredential):
		f = &failure{Status: 422, Code: "git_unavailable", Message: err.Error()}
	case errors.As(err, &pe) && pe.Code == "23505":
		f = &failure{Status: 409, Code: "conflict", Message: "that repository is already configured"}
	case errors.As(err, &pe) && (pe.Code == "55P03" || pe.Code == "57014"), errors.Is(err, context.DeadlineExceeded):
		f = &failure{Status: 503, Code: "busy", Message: "the doctrine layer is busy; nothing was changed, try again"}
	default:
		slog.Error("doctrine layer", "err", err)
		f = &failure{Status: 500, Code: "internal_error", Message: "doctrine operation failed"}
	}
	httpapi.WriteJSON(w, f.Status, f)
}

// ---------- Views ----------

// Layer is the git-backed doctrine layer: every configured repository at its
// pinned commit.
type Layer struct {
	Sources          []SourceView `json:"sources"`
	ProposalsEnabled bool         `json:"proposals_enabled,omitempty"`
}

// SourceView is one repository at its pin. State is ready (indexed at the
// pinned commit), not_indexed (never fetched at this pin) or failed (the last
// fetch at this pin failed or its credential grant is unavailable; Error says why).
type SourceView struct {
	ID            string     `json:"id"`
	Repository    string     `json:"repository"`
	Visibility    string     `json:"visibility"`
	Ref           string     `json:"ref,omitempty"`
	Commit        string     `json:"commit"`
	CommittedAt   *time.Time `json:"committed_at,omitempty"`
	PinnedAt      time.Time  `json:"pinned_at"`
	Paths         []string   `json:"paths"`
	CredentialRef string     `json:"credential_ref,omitempty"`
	URL           string     `json:"url"`
	State         string     `json:"state"`
	Error         string     `json:"error,omitempty"`
	IndexedAt     *time.Time `json:"indexed_at,omitempty"`
	Files         []FileView `json:"files"`
	Skipped       []Skip     `json:"skipped"`
}

func view(s Source, files []File) SourceView {
	v := SourceView{
		ID: s.ID, Repository: s.Repository, Visibility: s.Visibility, Ref: s.Ref, Commit: s.Commit,
		CommittedAt: s.CommittedAt, PinnedAt: s.PinnedAt, Paths: s.Paths, CredentialRef: s.CredentialRef,
		URL: treeURL(s.Repository, s.Commit), Error: s.IndexError, IndexedAt: s.IndexedAt, Files: []FileView{}, Skipped: s.Skipped,
	}
	if v.Skipped == nil {
		v.Skipped = []Skip{}
	}
	switch {
	case s.IndexedAt != nil && len(files) > 0:
		v.State = "ready"
		v.Files = Render(s.Repository, s.Commit, s.Visibility == "private", files)
	case s.IndexedAt != nil:
		// Indexed, and the paths selected no doctrine file at this commit.
		v.State = "ready"
	case s.IndexError != "":
		v.State = "failed"
	default:
		v.State = "not_indexed"
	}
	return v
}

// ---------- Handlers ----------

func (m *Module) tx(ctx context.Context, p tenant.Principal, permission string, fn func(pgx.Tx) error) error {
	if permission == "settings.manage" && p.Kind != tenant.Person {
		return authz.ErrForbidden
	}
	return db.InTenant(ctx, m.pool, p.TenantID, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT set_config('lock_timeout','3s',true), set_config('statement_timeout','10s',true)`); err != nil {
			return err
		}
		// Proposal mutations serialize with workspace access changes. Check
		// authority after taking the same tenant lock used by role writers.
		if permission == "rules.write" || permission == "rules.publish" {
			if _, err := tx.Exec(ctx, `SELECT id FROM tenants WHERE id=$1 FOR NO KEY UPDATE`, p.TenantID); err != nil {
				return err
			}
		}
		scope := authz.Scope{}
		if permission == "rules.read" {
			scope.AnyProject = true
		}
		if err := authz.RequireTx(ctx, tx, p, permission, scope); err != nil {
			return err
		}
		return fn(tx)
	})
}

func (m *Module) load(ctx context.Context, p tenant.Principal, permission string) (Layer, error) {
	type loaded struct {
		source Source
		files  []File
	}
	var all []loaded
	err := m.tx(ctx, p, permission, func(tx pgx.Tx) error {
		sources, err := listSources(ctx, tx)
		if err != nil {
			return err
		}
		for _, s := range sources {
			if s.CredentialRef != "" {
				if err := m.credentials.authorize(s.CredentialRef, p.TenantID, s.Repository); err != nil {
					// A revoked grant also hides cached bytes without preventing
					// the owner from managing the source or reading other sources.
					s.IndexError, s.IndexedAt = ErrCredential.Error(), nil
					s.Skipped = nil
					all = append(all, loaded{source: s})
					continue
				}
			}
			files, err := cachedFiles(ctx, tx, s)
			if err != nil {
				return err
			}
			all = append(all, loaded{s, files})
		}
		return nil
	})
	if err != nil {
		return Layer{}, err
	}
	out := Layer{Sources: []SourceView{}, ProposalsEnabled: m.app.configured() && p.TenantID == m.app.TenantID}
	for _, item := range all {
		out.Sources = append(out.Sources, view(item.source, item.files))
	}
	return out, nil
}

func (m *Module) layer(r *http.Request, p tenant.Principal) (any, error) {
	return m.load(r.Context(), p, "rules.read")
}

// SourceInput configures one repository. Commit is the pin. Ref names the
// release (a tag) and, when Commit is empty, is resolved to the commit it
// names now; when both are given, Ref must name Commit.
type SourceInput struct {
	Repository    string   `json:"repository"`
	Visibility    string   `json:"visibility"`
	Ref           string   `json:"ref"`
	Commit        string   `json:"commit"`
	Paths         []string `json:"paths"`
	CredentialRef string   `json:"credential_ref"`
}

func (in SourceInput) validate() error {
	if !repositoryPattern.MatchString(in.Repository) {
		return fail(400, "invalid_request", "repository must be owner/name on GitHub")
	}
	if in.Visibility != "public" && in.Visibility != "private" {
		return fail(400, "invalid_request", "visibility must be public or private")
	}
	if in.Ref == "" && in.Commit == "" {
		return fail(400, "invalid_request", "name the release tag or the commit to pin")
	}
	if in.Ref != "" && !validRef(in.Ref) {
		return fail(400, "invalid_request", "ref must be a tag or branch name")
	}
	if in.Commit != "" && !shaPattern.MatchString(in.Commit) {
		return fail(400, "invalid_request", "commit must be a full 40-digit lowercase SHA")
	}
	if len(in.Paths) > 0 && !ValidPatterns(in.Paths) {
		return fail(400, "invalid_request", "paths must be 1 to 20 distinct repository-relative patterns such as docs/AGENTS-*.md")
	}
	if in.CredentialRef != "" && !credentialRefPattern.MatchString(in.CredentialRef) {
		return fail(400, "invalid_request", "credential_ref must be a lowercase name such as doctrine-private-read")
	}
	if in.Visibility == "private" && in.CredentialRef == "" {
		return fail(400, "invalid_request", "a private repository needs a credential reference")
	}
	return nil
}

// reader builds the repository reader for one fetch, with the token the
// source's credential reference resolves to (none for a public source
// without a reference).
func (m *Module) reader(tenantID, repository, credentialRef string) (Reader, error) {
	token := ""
	if credentialRef != "" {
		var err error
		if token, err = m.credentials.Token(credentialRef, tenantID, repository); err != nil {
			return nil, err
		}
	}
	return &GitHub{Token: token, Client: m.client}, nil
}

// pin resolves the input to a commit on the repository host.
func (m *Module) pin(ctx context.Context, tenantID string, in SourceInput) (Commit, error) {
	reader, err := m.reader(tenantID, in.Repository, in.CredentialRef)
	if err != nil {
		return Commit{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, fetchTimeout)
	defer cancel()
	target := in.Commit
	if target == "" {
		target = in.Ref
	}
	commit, err := reader.Commit(ctx, in.Repository, target)
	if err != nil {
		return Commit{}, err
	}
	if in.Ref != "" && in.Commit != "" {
		named, err := reader.Commit(ctx, in.Repository, in.Ref)
		if err != nil {
			return Commit{}, err
		}
		if named.SHA != commit.SHA {
			return Commit{}, fail(409, "ref_mismatch", "the ref "+in.Ref+" names commit "+named.SHA[:12]+", not "+in.Commit[:12])
		}
	}
	return commit, nil
}

func (m *Module) create(r *http.Request, p tenant.Principal) (any, error) {
	var in SourceInput
	if err := workorders.Decode(r, &in); err != nil {
		return nil, err
	}
	if err := in.validate(); err != nil {
		return nil, err
	}
	ctx := r.Context()
	// Authorize before any outbound request.
	if err := m.tx(ctx, p, "settings.manage", func(tx pgx.Tx) error { return nil }); err != nil {
		return nil, err
	}
	commit, err := m.pin(ctx, p.TenantID, in)
	if err != nil {
		return nil, err
	}
	paths := in.Paths
	if len(paths) == 0 {
		paths = DefaultPaths
	}
	var created Source
	err = m.tx(ctx, p, "settings.manage", func(tx pgx.Tx) error {
		var count int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM doctrine_sources`).Scan(&count); err != nil {
			return err
		}
		if count >= MaxSources {
			return fail(409, "conflict", "a workspace indexes at most 8 doctrine repositories")
		}
		var err error
		created, err = insertSource(ctx, tx, p.TenantID, Source{
			Repository: in.Repository, Visibility: in.Visibility, Ref: in.Ref, Commit: commit.SHA,
			CommittedAt: commit.CommittedAt, Paths: paths, CredentialRef: in.CredentialRef,
		})
		if err != nil {
			return err
		}
		_, err = events.Append(ctx, tx, p, events.Change{Type: "doctrine.source_added", After: created.audit()})
		return err
	})
	if err != nil {
		return nil, err
	}
	m.index(context.WithoutCancel(ctx), p, created.ID)
	return m.load(ctx, p, "settings.manage")
}

func (m *Module) update(r *http.Request, p tenant.Principal) (any, error) {
	var in SourceInput
	if err := workorders.Decode(r, &in); err != nil {
		return nil, err
	}
	ctx := r.Context()
	id := r.PathValue("sourceId")
	var current Source
	if err := m.tx(ctx, p, "settings.manage", func(tx pgx.Tx) error {
		var err error
		current, err = getSource(ctx, tx, id, false)
		return err
	}); err != nil {
		return nil, err
	}
	// The repository is the source's identity; another repository is another source.
	if in.Repository == "" {
		in.Repository = current.Repository
	}
	if in.Repository != current.Repository {
		return nil, fail(400, "invalid_request", "the repository of a source cannot change; add the other repository instead")
	}
	if err := in.validate(); err != nil {
		return nil, err
	}
	commit, err := m.pin(ctx, p.TenantID, in)
	if err != nil {
		return nil, err
	}
	paths := in.Paths
	if len(paths) == 0 {
		paths = DefaultPaths
	}
	var updated Source
	err = m.tx(ctx, p, "settings.manage", func(tx pgx.Tx) error {
		before, err := getSource(ctx, tx, id, true)
		if err != nil {
			return err
		}
		next := before
		next.Visibility, next.Ref, next.Commit, next.CommittedAt, next.Paths, next.CredentialRef = in.Visibility, in.Ref, commit.SHA, commit.CommittedAt, paths, in.CredentialRef
		if updated, err = updateSource(ctx, tx, before, next); err != nil {
			return err
		}
		kind := "doctrine.source_changed"
		if before.Commit != updated.Commit {
			kind = "doctrine.pinned"
		}
		_, err = events.Append(ctx, tx, p, events.Change{Type: kind, Before: before.audit(), After: updated.audit()})
		return err
	})
	if err != nil {
		return nil, err
	}
	if updated.IndexedAt == nil {
		m.index(context.WithoutCancel(ctx), p, updated.ID)
	}
	return m.load(ctx, p, "settings.manage")
}

func (m *Module) remove(r *http.Request, p tenant.Principal) (any, error) {
	ctx := r.Context()
	id := r.PathValue("sourceId")
	err := m.tx(ctx, p, "settings.manage", func(tx pgx.Tx) error {
		before, err := getSource(ctx, tx, id, true)
		if err != nil {
			return err
		}
		if err := deleteSource(ctx, tx, id); err != nil {
			return err
		}
		_, err = events.Append(ctx, tx, p, events.Change{Type: "doctrine.source_removed", Before: before.audit()})
		return err
	})
	if err != nil {
		return nil, err
	}
	return m.load(ctx, p, "settings.manage")
}

func (m *Module) reindex(r *http.Request, p tenant.Principal) (any, error) {
	ctx := r.Context()
	id := r.PathValue("sourceId")
	if err := m.tx(ctx, p, "settings.manage", func(tx pgx.Tx) error {
		_, err := getSource(ctx, tx, id, false)
		return err
	}); err != nil {
		return nil, err
	}
	m.index(context.WithoutCancel(ctx), p, id)
	return m.load(ctx, p, "settings.manage")
}

// index fetches the source at its pinned commit and replaces its cache. The
// fetch runs outside any transaction; the write checks that the pin did not
// move meanwhile. A failure is recorded on the source, never returned: the
// pin stays as configured and the layer shows why it could not be read.
func (m *Module) index(ctx context.Context, p tenant.Principal, id string) {
	var s Source
	if err := m.tx(ctx, p, "settings.manage", func(tx pgx.Tx) error {
		var err error
		s, err = getSource(ctx, tx, id, false)
		return err
	}); err != nil {
		return
	}
	files, skipped, corpus, fetchErr := m.fetch(ctx, p.TenantID, s)
	err := m.tx(ctx, p, "settings.manage", func(tx pgx.Tx) error {
		current, err := getSource(ctx, tx, id, true)
		if err != nil {
			return err
		}
		if current.Commit != s.Commit || !slices.Equal(current.Paths, s.Paths) {
			return nil // the pin moved meanwhile; that change indexes itself
		}
		if fetchErr != nil {
			return recordIndexError(ctx, tx, id, safeMessage(fetchErr))
		}
		if err := storeIndex(ctx, tx, p.TenantID, s, files, skipped); err != nil {
			return err
		}
		if err := storeGuardCorpus(ctx, tx, p.TenantID, s, corpus); err != nil {
			return err
		}
		_, err = events.Append(ctx, tx, p, events.Change{Type: "doctrine.indexed", After: map[string]any{
			"source_id": s.ID, "repository": s.Repository, "commit": s.Commit, "files": len(files), "skipped": len(skipped),
		}})
		return err
	})
	if err != nil {
		slog.Error("doctrine index", "source", id, "err", err)
	}
}

func (m *Module) fetch(ctx context.Context, tenantID string, s Source) ([]File, []Skip, []byte, error) {
	reader, err := m.reader(tenantID, s.Repository, s.CredentialRef)
	if err != nil {
		return nil, nil, nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, fetchTimeout)
	defer cancel()
	files, skipped, err := Fetch(ctx, reader, s.Repository, s.Commit, s.Paths)
	if err != nil {
		return nil, nil, nil, err
	}
	// The visible index follows the configured paths. The quotation guard does
	// not: a public proposal is checked against every file at the pin.
	if s.Repository != privateRepository || s.Visibility != "private" {
		return files, skipped, nil, nil
	}
	corpus, err := readPrivateCorpus(ctx, reader, s.Repository, s.Commit, m.guardKey(tenantID))
	if err != nil {
		return nil, nil, nil, err
	}
	return files, skipped, corpus, nil
}

// EnsurePrivateGuards rebuilds a private quotation guard that is missing or
// was keyed with a different server secret. A failed rebuild leaves the
// doctrine index readable and public proposals refused until a later success.
// It does not log the key or any doctrine text.
func (m *Module) EnsurePrivateGuards(ctx context.Context) {
	if m == nil || m.pool == nil || len(m.guardMaster) < 32 {
		slog.Info("doctrine guard rebuild skipped", "reason", "no guard key")
		return
	}
	rows, err := m.pool.Query(ctx, `SELECT id::text FROM tenants ORDER BY id`)
	if err != nil {
		slog.Error("doctrine guard rebuild", "err", err)
		return
	}
	var tenants []string
	for rows.Next() {
		var id string
		if rows.Scan(&id) == nil {
			tenants = append(tenants, id)
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		slog.Error("doctrine guard rebuild", "err", err)
		return
	}
	for _, tenantID := range tenants {
		if ctx.Err() != nil {
			return
		}
		if err := m.ensureTenantGuard(ctx, tenantID); err != nil && ctx.Err() == nil {
			slog.Error("doctrine guard rebuild", "tenant", tenantID, "err", err)
		}
	}
}

func (m *Module) ensureTenantGuard(ctx context.Context, tenantID string) error {
	var ids []string
	err := db.InTenant(ctx, m.pool, tenantID, func(tx pgx.Tx) error {
		sources, err := listSources(ctx, tx)
		if err != nil {
			return err
		}
		key := m.guardKey(tenantID)
		for _, s := range sources {
			if s.Repository != privateRepository || s.Visibility != "private" || s.IndexedAt == nil || s.CredentialRef == "" {
				continue
			}
			raw, err := loadGuardCorpus(ctx, tx, s)
			if err != nil {
				return err
			}
			if corpus, err := unmarshalGuard(raw, key); err == nil && !corpus.empty() {
				continue
			}
			ids = append(ids, s.ID)
		}
		return nil
	})
	if err != nil {
		return err
	}
	for _, id := range ids {
		if err := m.rebuildGuard(ctx, tenantID, id); err != nil && ctx.Err() == nil {
			slog.Error("doctrine guard rebuild", "tenant", tenantID, "source", id, "err", err)
		}
	}
	return nil
}

func (m *Module) rebuildGuard(ctx context.Context, tenantID, id string) error {
	var s Source
	if err := db.InTenant(ctx, m.pool, tenantID, func(tx pgx.Tx) error {
		var err error
		s, err = getSource(ctx, tx, id, false)
		return err
	}); err != nil {
		return err
	}
	if s.Repository != privateRepository || s.Visibility != "private" || s.IndexedAt == nil || s.CredentialRef == "" {
		return nil
	}
	reader, err := m.reader(tenantID, s.Repository, s.CredentialRef)
	if err != nil {
		return err
	}
	fetchCtx, cancel := context.WithTimeout(ctx, fetchTimeout)
	corpus, err := readPrivateCorpus(fetchCtx, reader, s.Repository, s.Commit, m.guardKey(tenantID))
	cancel()
	if err != nil {
		return err
	}
	return db.InTenant(ctx, m.pool, tenantID, func(tx pgx.Tx) error {
		current, err := getSource(ctx, tx, id, true)
		if err != nil {
			return err
		}
		if current.Commit != s.Commit || current.IndexedAt == nil {
			return nil
		}
		return storeGuardCorpus(ctx, tx, tenantID, current, corpus)
	})
}

// safeMessage is what a failed fetch records: the repository or credential
// message (which never holds a secret), or a generic line.
func safeMessage(err error) string {
	if errors.Is(err, ErrGit) || errors.Is(err, ErrCredential) {
		msg := err.Error()
		if len(msg) > 300 {
			msg = msg[:300]
		}
		return msg
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "the repository did not answer in time"
	}
	return "the repository could not be read"
}
