// SPDX-License-Identifier: AGPL-3.0-only
package doctrine

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type preparationFenceProbe struct {
	t       *testing.T
	d       *dbtest.DB
	tenant  string
	samples atomic.Int32
}

func (p *preparationFenceProbe) TraceQueryStart(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	if !strings.Contains(data.SQL, "FROM doctrine_cache") && !strings.Contains(data.SQL, "FROM doctrine_private_guard") {
		return ctx
	}
	p.samples.Add(1)
	// The exact expensive read is the barrier. NOWAIT makes a held mutation
	// fence a deterministic failure without scheduler timing or sleeps.
	tx, err := p.d.Admin.Begin(ctx)
	if err != nil {
		p.t.Error(err)
		return ctx
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `SELECT id FROM tenants WHERE id=$1 FOR NO KEY UPDATE NOWAIT`, p.tenant); err != nil {
		p.t.Error("doctrine rendering/guard read held the tenant mutation fence")
	}
	return ctx
}
func (*preparationFenceProbe) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}

func TestFix2InboxPreparationDoesNotHoldMutationFence(t *testing.T) {
	f, _, m, owner, pin := publicProposalFixture(t)
	seedPrivateGuard(t, f, m, owner)
	probe := &preparationFenceProbe{t: t, d: f.d, tenant: owner.TenantID}
	config := f.d.App.Config()
	config.ConnConfig.Tracer = probe
	pool, err := pgxpool.NewWithConfig(t.Context(), config)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	m.pool = pool
	in := InboxInput{RequestID: pin.RequestID, SourceID: pin.SourceID, Path: pin.Path, RuleKey: pin.RuleKey, RuleSHA: pin.RuleSHA, Source: pin.Source, Why: pin.Explanation, TLDR: &inboxTLDR{EN: "Keep credentials and session material out of transcripts."}}
	f.call(owner, "POST", "/api/rules/doctrine/inbox", in, 200)
	if probe.samples.Load() < 2 {
		t.Fatal("fixture did not exercise doctrine rendering and private guard reads")
	}
}
