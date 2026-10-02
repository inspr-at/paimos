// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/inspr-at/paimos/internal/activity"
	"github.com/inspr-at/paimos/internal/agentaccounts"
	"github.com/inspr-at/paimos/internal/agentpairing"
	"github.com/inspr-at/paimos/internal/agentruns"
	"github.com/inspr-at/paimos/internal/aithema/host"
	"github.com/inspr-at/paimos/internal/aithema/journal"
	"github.com/inspr-at/paimos/internal/aithema/tokens"
	"github.com/inspr-at/paimos/internal/approvals"
	"github.com/inspr-at/paimos/internal/attachments"
	"github.com/inspr-at/paimos/internal/auth"
	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/brand"
	"github.com/inspr-at/paimos/internal/business/costunits"
	"github.com/inspr-at/paimos/internal/business/crm"
	"github.com/inspr-at/paimos/internal/business/directory"
	"github.com/inspr-at/paimos/internal/business/hours"
	"github.com/inspr-at/paimos/internal/business/quotes"
	"github.com/inspr-at/paimos/internal/business/quotes/collaboration"
	"github.com/inspr-at/paimos/internal/business/quotes/confirmation"
	publicquotes "github.com/inspr-at/paimos/internal/business/quotes/public"
	"github.com/inspr-at/paimos/internal/config"
	"github.com/inspr-at/paimos/internal/crossreview"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/deliveryvote"
	"github.com/inspr-at/paimos/internal/embedding"
	"github.com/inspr-at/paimos/internal/events"
	"github.com/inspr-at/paimos/internal/features"
	"github.com/inspr-at/paimos/internal/fromclassic"
	"github.com/inspr-at/paimos/internal/greetings"
	"github.com/inspr-at/paimos/internal/harness"
	"github.com/inspr-at/paimos/internal/httpapi"
	"github.com/inspr-at/paimos/internal/identity"
	"github.com/inspr-at/paimos/internal/imports"
	"github.com/inspr-at/paimos/internal/inbox"
	"github.com/inspr-at/paimos/internal/intake"
	"github.com/inspr-at/paimos/internal/journey"
	"github.com/inspr-at/paimos/internal/knowledge"
	"github.com/inspr-at/paimos/internal/modelprovider"
	"github.com/inspr-at/paimos/internal/modelregistry"
	"github.com/inspr-at/paimos/internal/nodes"
	"github.com/inspr-at/paimos/internal/outcomes"
	"github.com/inspr-at/paimos/internal/plugins"
	"github.com/inspr-at/paimos/internal/portal"
	"github.com/inspr-at/paimos/internal/profile"
	"github.com/inspr-at/paimos/internal/projectgroups"
	"github.com/inspr-at/paimos/internal/relations"
	"github.com/inspr-at/paimos/internal/releasehistory"
	"github.com/inspr-at/paimos/internal/releases"
	"github.com/inspr-at/paimos/internal/requirements"
	"github.com/inspr-at/paimos/internal/rules"
	"github.com/inspr-at/paimos/internal/rules/doctrine"
	"github.com/inspr-at/paimos/internal/search"
	"github.com/inspr-at/paimos/internal/stagehandoff"
	"github.com/inspr-at/paimos/internal/statusautopilot"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/inspr-at/paimos/internal/tenantbrand"
	"github.com/inspr-at/paimos/internal/ticketwork"
	"github.com/inspr-at/paimos/internal/usagedashboard"
	"github.com/inspr-at/paimos/internal/views"
	"github.com/inspr-at/paimos/internal/workorders"
	"github.com/inspr-at/paimos/web"
)

// shutdownBudget is the wall clock from SIGTERM to the end of Shutdown.
// The container stop grace must be longer than this. Docker's default 10s
// sends SIGKILL while handlers, including SSE, are still running.
const shutdownBudget = 15 * time.Second

// drainFor is how long readiness stays false while the listener still accepts,
// so a load balancer probing once a second can observe 503 and stop sending
// new requests before Shutdown closes the port. It is included in shutdownBudget.
const drainFor = 2 * time.Second

func serve() error {
	cfg, err := config.FromEnv()
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()
	// Bind after init. A port open during migrations accepts TCP that nothing
	// reads until Serve, which is a hung backend rather than "not ready".
	return serveListener(ctx, cfg, nil)
}

func serveListener(ctx context.Context, cfg config.Config, ln net.Listener) error {
	closeListener := func() {
		if ln != nil {
			_ = ln.Close()
		}
	}
	setupLogger(cfg.Env)
	pdfConcurrency, err := pdfRenderConcurrency(os.Getenv("AEON_PDF_CONCURRENCY"))
	if err != nil {
		closeListener()
		return err
	}

	pool, err := db.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		closeListener()
		return err
	}
	defer pool.Close()

	if err := db.EnsureTenant(ctx, pool, cfg.BootstrapTenantSlug, cfg.BootstrapTenantName); err != nil {
		closeListener()
		return err
	}

	webFS, err := resolveWeb(cfg)
	if err != nil {
		closeListener()
		return err
	}

	authCfg, err := auth.FromEnv()
	if err != nil {
		closeListener()
		return err
	}
	authMod, err := auth.New(authCfg, pool)
	if err != nil {
		closeListener()
		return err
	}
	// The host-wide signer is owned by the bootstrap tenant for forced RLS.
	// Reuse the existing host session-key file with a separate vault derivation.
	// HTTP-only dev hosts publish 503 until an HTTPS issuer is configured.
	tokenMod := &tokens.Module{}
	if strings.HasPrefix(cfg.PublicURL, "https://") && (authCfg.Env != "dev" || os.Getenv("AEON_SESSION_KEY_FILE") != "") {
		var signingOwner string
		if err := pool.QueryRow(ctx, `SELECT id::text FROM tenants WHERE slug=$1`, cfg.BootstrapTenantSlug).Scan(&signingOwner); err != nil {
			closeListener()
			return fmt.Errorf("signing key owner: %w", err)
		}
		tokenMod.Keys, err = tokens.New(ctx, &tokens.PostgresStore{Pool: pool, TenantID: signingOwner}, authCfg.SessionKey, tokens.Config{Issuer: cfg.PublicURL, Audience: cfg.PublicURL})
		if err != nil {
			closeListener()
			return fmt.Errorf("aithema signing keys: %w", err)
		}
	}
	// In-app models are workspace opt-ins. There is no global vendor fallback.
	workspaceModels := modelprovider.New(pool, authCfg.SessionKey)
	workspaceNotes := crm.WorkspaceNotes{Provider: workspaceModels}
	extraPlugins := []func() (plugins.Plugin, error){costunits.Plugin, func() (plugins.Plugin, error) { return crm.PluginWithNoteGenerator(workspaceNotes) }, quotes.ManifestPlugin, hours.Plugin, greetings.ManifestPlugin, profile.Plugin, host.Plugin}
	journalStore, err := journal.NewStore(pool)
	if err != nil {
		closeListener()
		return fmt.Errorf("aithema journal contracts: %w", err)
	}
	journalMod := &journal.Module{Store: journalStore, Keys: tokenMod.Keys}
	aithemaHost, err := host.New(pool, journalStore, tokenMod.Keys, authCfg.SessionKey, cfg.PublicURL, cfg.AithemaOperatorLocalServices...)
	if err != nil {
		closeListener()
		return fmt.Errorf("aithema host: %w", err)
	}
	authMod.Delegated = aithemaHost.DelegatedIntake
	journalStore.HostAuthorization = aithemaHost.CheckJournal
	journalStore.AuthorityProjection = aithemaHost.CheckAuthority
	if tokenMod.Keys != nil {
		go aithemaHost.Run(ctx)
	}

	var messagingMod httpapi.Module
	if cfg.MessagingKey != nil {
		m, err := inbox.NewMessaging(pool, cfg.MessagingKey)
		if err != nil {
			closeListener()
			return fmt.Errorf("messaging: %w", err)
		}
		messagingMod = m
		extraPlugins = append(extraPlugins, inbox.MessagingPlugin)
		// CP1: receiver-owned grok_bot_routine webhook wakes, one runner per tenant.
		dispatcher, err := inbox.NewRoutineDispatcher(pool, cfg.MessagingKey)
		if err != nil {
			closeListener()
			return fmt.Errorf("routine dispatcher: %w", err)
		}
		go runRoutineDispatchers(ctx, pool, dispatcher)
	} else {
		slog.Warn("messaging disabled: AEON_MESSAGING_KEY_FILE is not set")
	}
	greetingsMod, err := greetings.New(pool)
	if err != nil {
		return fmt.Errorf("greetings: %w", err)
	}
	fileStore := attachments.Store{FilesDir: cfg.FilesDir}
	attachmentsMod := attachments.New(pool, fileStore)
	attachmentsMod.Sandbox = attachments.NewSandbox(cfg.PublicURL, cfg.HTMLSandboxOrigin, authMod.PreviewSessionLease)
	var confirmationMod *confirmation.Module
	pluginRegistry, err := plugins.BuiltinWithRegistration(func(reg *plugins.Registry) error {
		var err error
		confirmationMod, err = confirmation.New(pool, reg, webFS, fileStore)
		if err != nil {
			return err
		}
		if err := confirmationMod.SetRenderConcurrency(pdfConcurrency); err != nil {
			return err
		}
		plugin, err := confirmation.ManifestPlugin(confirmationMod)
		if err != nil {
			return err
		}
		return reg.Register(plugin)
	}, extraPlugins...)
	if err != nil {
		closeListener()
		return fmt.Errorf("plugins: %w", err)
	}
	quotesMod, err := quotes.New(pool, pluginRegistry)
	if err != nil {
		closeListener()
		return fmt.Errorf("quotes: %w", err)
	}
	collaborationMod, err := collaboration.New(pool, pluginRegistry)
	if err != nil {
		closeListener()
		return fmt.Errorf("quote collaboration: %w", err)
	}
	publicQuotesMod, err := publicquotes.NewWithStore(pool, pluginRegistry, webFS, fileStore, cfg.PublicURL)
	if err != nil {
		closeListener()
		return fmt.Errorf("public quotes: %w", err)
	}
	go embedding.NewWorker(pool, nil, embedding.Options{Resolve: workspaceModels.Embeddings}).Run(ctx)
	go runConfirmationJobs(ctx, pool, confirmationMod, pdfConcurrency)
	// A bad AEON_BRAND_FILE must stop startup, never fall back silently.
	productBrand, err := brand.Load()
	if err != nil {
		return fmt.Errorf("brand: %w", err)
	}
	historyMod, err := releasehistory.New()
	if err != nil {
		return fmt.Errorf("release history: %w", err)
	}
	historyMod.UseTickets(releasehistory.DBTickets(pool))
	historyMod.WithBackfills(pool, "AEON")
	portalMod := portal.New(pool, cfg.Env != "dev", authCfg.SessionKey)
	go portalMod.RunLimitSweep(ctx)
	// AEON-178: invites can create the sign-in account through a configured identity
	// provisioner (none by default). A misconfigured provisioner stops startup.
	provisioner, err := identity.FromEnv()
	if err != nil {
		return fmt.Errorf("identity provisioner: %w", err)
	}
	// R2: webhook wake for inbox deliveries.
	go inbox.NewWorker(pool, inbox.WorkerOptions{}).Run(ctx)
	// AEON-291: silent unmanaged sessions become "Lost contact" (one runner per tenant).
	go harness.RunLostContactSweeper(ctx, pool)
	// AEON-280: delivery deadlines and the attempt cap; one runner across
	// processes through an advisory lock.
	go inbox.NewSweeper(pool).Run(ctx)
	// AEON-288: one daily pass nominates method learnings. It never accepts them.
	go knowledge.NewTagger(pool).Run(ctx)
	statusAuto := statusautopilot.New(pool)
	go statusAuto.Run(ctx)
	pairingMod := agentpairing.New(pool, cfg.PublicURL, cfg.BootstrapTenantSlug, cfg.PairingNixGuide)
	if err := pairingMod.ConfigureAccountLink(authCfg.SessionKey); err != nil {
		return fmt.Errorf("account linking: %w", err)
	}
	doctrineMod := doctrine.New(pool, doctrine.Options{
		CredentialsDir:    cfg.DoctrineCredentialsDir,
		GuardKey:          cfg.DoctrineGuardKey,
		BinaryAllowlist:   cfg.DoctrineBinaryAllowlist,
		AnalysisLearnings: knowledge.AnalysisLearnings,
		App: doctrine.AppConfig{
			ID: cfg.DoctrineAppID, InstallationID: cfg.DoctrineInstallationID, KeyRef: cfg.DoctrineAppKeyRef,
			TenantID: cfg.DoctrineAppTenantID, GateLogin: cfg.DoctrineGateLogin, DCOAcknowledged: cfg.DoctrineDCOAcknowledged,
		},
	})
	go doctrineMod.EnsurePrivateGuards(ctx)
	go doctrineMod.RunOutcomeAnalysis(ctx)
	reviewApp := &crossreview.GitHubApp{Config: crossreview.AppConfig{
		ID: cfg.ReviewAppID, InstallationID: cfg.ReviewInstallationID, KeyFile: cfg.ReviewAppKeyFile,
		TenantID: cfg.ReviewAppTenantID, Repository: cfg.ReviewAppRepository,
	}}
	var reviewPublisher crossreview.StatusPublisher
	if reviewApp.Configured(cfg.ReviewAppTenantID, cfg.ReviewAppRepository) {
		reviewPublisher = reviewApp
	}
	reviewMod := crossreview.New(pool, reviewPublisher)
	go reviewMod.RunStatusReporter(ctx)
	api := &httpapi.Server{
		Pool:                    pool,
		Brand:                   &productBrand,
		PublicURL:               cfg.PublicURL,
		AttachmentSandboxOrigin: attachmentsMod.Sandbox.Origin(),
		// AEON-430: the footer names the running release from /api/version.
		Codename: historyMod.CodenameOf,
		Web:      webFS,
		Modules: []httpapi.Module{
			authMod,
			tokenMod,
			journalMod,
			aithemaHost,
			// ADR-003: permissions, roles, members, project members, invites and
			// access audit. P1 shipped with it unmounted, so /api/me/permissions answered 403.
			authz.NewWithProvisioner(pool, provisioner),
			nodes.New(pool, nodes.SQLWriter{}),
			fromclassic.New(pool),
			tenantbrand.New(pool),
			features.New(pool),
			workspaceModels,
			relations.New(pool),
			events.New(pool, events.WithUndoHandlers(nodes.UndoHandlers()), relations.UndoOption(), events.WithUndoHandlers(views.UndoHandlers()), events.WithUndoHandlers(knowledge.UndoHandlers()), events.WithUndoHandlers(projectgroups.UndoHandlers()), events.WithUndoHandlers(attachments.UndoHandlers()), events.WithUndoHandlers(hours.UndoHandlers(pluginRegistry)), events.WithUndoHandlers(profile.UndoHandlers()), events.WithUndoHandlers(crm.UndoHandlers(pluginRegistry)), events.WithUndoHandlers(publicquotes.UndoHandlers()), events.WithUndoHandlers(quotes.UndoHandlers(pluginRegistry)), events.WithUndoHandlers(statusautopilot.UndoHandlers()), events.WithUndoHandlers(releases.UndoHandlers()), events.WithUndoHandlers(harness.UndoHandlers())),
			search.NewWithResolver(pool, workspaceModels.Embeddings),
			views.New(pool),
			activity.New(pool),
			attachmentsMod,
			greetingsMod,
			knowledge.New(pool),
			projectgroups.New(pool),
			historyMod,
			profile.New(pool, attachments.Store{FilesDir: cfg.FilesDir}),
			imports.New(pool),
			// R2: agents
			inbox.New(pool),
			harness.New(pool, nodes.CapturePlanningStart),
			rules.New(pool),
			doctrineMod,
			ticketwork.New(pool),
			outcomes.New(pool),
			deliveryvote.New(pool),
			usagedashboard.New(pool),
			workorders.New(pool),
			reviewMod,
			agentruns.NewWithReviews(pool, settleUsage, reviewMod.RequestForRun),
			approvals.New(pool),
			modelregistry.New(pool),
			agentaccounts.New(pool),
			pairingMod,
			// R3: journey
			journey.New(pool),
			requirements.New(pool),
			releases.New(pool),
			statusAuto,
			intake.NewDelegated(pool, tokenMod.Keys, aithemaHost),
			plugins.NewWithRegistry(pool, pluginRegistry),
			// EvidenceLaunchChecks admits only from the recorded candidate artifact
			// and a fresh launch_readiness row. A missing record stays refused.
			stagehandoff.New(pool, pluginRegistry, stagehandoff.EvidenceLaunchChecks{Pool: pool}),
			// R4: business plugins
			costunits.New(pool, pluginRegistry),
			crm.NewWithNoteGenerator(pool, pluginRegistry, workspaceNotes),
			quotesMod,
			collaborationMod,
			publicQuotesMod,
			portalMod,
			confirmationMod,
			hours.New(pool, pluginRegistry),
			directory.New(pool, pluginRegistry),
		},
		Middleware: []func(http.Handler) http.Handler{authMod.Middleware, (doctrine.Credentials{Dir: cfg.DoctrineCredentialsDir}).CatalogMiddleware, events.PositionMiddleware(pool)},
	}
	if tokenMod.Keys != nil {
		api.AithemaOrigin = cfg.PublicURL
	}
	if messagingMod != nil {
		api.Modules = append(api.Modules, messagingMod)
	}
	if ln == nil {
		listened, lerr := net.Listen("tcp", cfg.Addr)
		if lerr != nil {
			return fmt.Errorf("listen: %w", lerr)
		}
		ln = listened
	}
	srv := &http.Server{
		Handler:           attachmentsMod.WrapSandbox(agentpairing.GuidePage(api.Handler(), webFS, cfg.PublicURL, cfg.PairingNixGuide)),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       time.Minute,
	}

	errCh := make(chan error, 1)
	go func() {
		api.SetServing(true)
		err := srv.Serve(ln)
		api.SetServing(false)
		if errors.Is(err, http.ErrServerClosed) {
			errCh <- nil
			return
		}
		errCh <- err
	}()

	slog.Info("aeon listening", "addr", ln.Addr().String(), "env", cfg.Env, "public_url", cfg.PublicURL)

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		slog.Info("draining")
		if err := stopServing(srv, api.Drain, shutdownBudget, drainFor); err != nil {
			return err
		}
		return <-errCh
	}
}

// stopServing fails readiness, keeps the listener open for drainFor so a load
// balancer can observe 503, then shuts down within the rest of budget.
// Shutdown does not cancel in-flight handler contexts, so SSE streams continue
// until the client leaves or the budget expires and the process exits.
func stopServing(srv *http.Server, markUnready func(), budget, drainFor time.Duration) error {
	if markUnready != nil {
		markUnready()
	}
	if drainFor > 0 {
		timer := time.NewTimer(drainFor)
		<-timer.C
	}
	remain := budget - drainFor
	if remain < 0 {
		remain = 0
	}
	sctx, cancel := context.WithTimeout(context.Background(), remain)
	defer cancel()
	return srv.Shutdown(sctx)
}

func pdfRenderConcurrency(raw string) (int, error) {
	if raw == "" {
		return 1, nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 1 || n > 4 {
		return 0, fmt.Errorf("AEON_PDF_CONCURRENCY must be between 1 and 4")
	}
	return n, nil
}

// runConfirmationJobs scans tenant queues inside tenant transactions. A cycle
// waits for every render before scheduling more, bounding concurrent browsers.
func runConfirmationJobs(ctx context.Context, pool *pgxpool.Pool, worker *confirmation.Module, concurrency int) {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		confirmationCycle(ctx, pool, worker, concurrency)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func confirmationCycle(ctx context.Context, pool *pgxpool.Pool, worker *confirmation.Module, concurrency int) {
	var tenantIDs []string
	// tenants is the one global table; the transaction still carries an explicit
	// tenant setting so no query in the worker bypasses db.InTenant.
	err := db.InTenant(ctx, pool, "00000000-0000-0000-0000-000000000000", func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT id::text FROM tenants ORDER BY id`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				return err
			}
			tenantIDs = append(tenantIDs, id)
		}
		return rows.Err()
	})
	if err != nil {
		if ctx.Err() == nil {
			slog.Error("quote confirmation tenant scan failed", "error", err)
		}
		return
	}
	limit := make(chan struct{}, concurrency)
	var group sync.WaitGroup
	for _, id := range tenantIDs {
		var pending bool
		err := db.InTenant(ctx, pool, id, func(tx pgx.Tx) error {
			return tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM quote_confirmation_jobs WHERE (state IN ('pending','failed') AND next_attempt_at<=clock_timestamp()) OR (state='rendering' AND lease_until<clock_timestamp()))`).Scan(&pending)
		})
		if err != nil {
			if ctx.Err() == nil {
				slog.Error("quote confirmation queue scan failed", "tenant_id", id, "error", err)
			}
			continue
		}
		if !pending {
			continue
		}
		select {
		case limit <- struct{}{}:
		case <-ctx.Done():
			group.Wait()
			return
		}
		group.Add(1)
		go func(tenantID string) {
			defer group.Done()
			defer func() { <-limit }()
			if _, err := worker.ProcessNext(ctx, tenantID); err != nil && ctx.Err() == nil {
				slog.Error("quote confirmation failed", "tenant_id", tenantID, "error", err)
			}
		}(id)
	}
	group.Wait()
}

func setupLogger(env string) {
	slog.SetDefault(slog.New(loggerHandler(env, os.Stdout)))
}

func loggerHandler(env string, w io.Writer) slog.Handler {
	if env == "prod" {
		return slog.NewJSONHandler(w, nil)
	}
	return slog.NewTextHandler(w, nil)
}

func resolveWeb(cfg config.Config) (fs.FS, error) {
	if cfg.WebDir != "" {
		info, err := os.Stat(cfg.WebDir)
		if err != nil {
			return nil, fmt.Errorf("AEON_WEB_DIR: %w", err)
		}
		if !info.IsDir() {
			return nil, fmt.Errorf("AEON_WEB_DIR %s is not a directory", cfg.WebDir)
		}
		return os.DirFS(cfg.WebDir), nil
	}
	if fsys, ok := web.Static(); ok {
		return fsys, nil
	}
	return nil, nil
}

// settleUsage lets finished runs settle their account allowance projections (R2).
func settleUsage(ctx context.Context, tx pgx.Tx, p tenant.Principal, run agentruns.Run, _ agentruns.Telemetry) error {
	return agentaccounts.Settle(ctx, tx, p, run.ID)
}

// runRoutineDispatchers starts one routine dispatcher per tenant that exists at
// startup; a tenant created later is picked up on the next restart.
func runRoutineDispatchers(ctx context.Context, pool *pgxpool.Pool, d *inbox.RoutineDispatcher) {
	rows, err := pool.Query(ctx, `SELECT id::text FROM tenants ORDER BY id`)
	if err != nil {
		slog.Error("routine dispatcher: list tenants", "err", err)
		return
	}
	var ids []string
	for rows.Next() {
		var id string
		if rows.Scan(&id) == nil {
			ids = append(ids, id)
		}
	}
	rows.Close()
	for _, id := range ids {
		go func(tenantID string) {
			if err := d.Run(ctx, tenantID); err != nil && ctx.Err() == nil {
				slog.Error("routine dispatcher stopped", "tenant", tenantID, "err", err)
			}
		}(id)
	}
}
