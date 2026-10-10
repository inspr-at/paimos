// SPDX-License-Identifier: AGPL-3.0-only
package routineactions

import (
	"context"
	"github.com/inspr-at/paimos/internal/events"
	"github.com/inspr-at/paimos/internal/knowledge"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

func writeKnowledge(ctx context.Context, tx pgx.Tx, p tenant.Principal, in Request) (string, int64, []events.Change, error) {
	v := in.KnowledgeWrite
	return knowledge.WriteRoutineTx(ctx, tx, p, in.ProjectID, in.TargetID, in.ExpectedRevision, v.Type, v.Slug, v.Title, v.Body)
}
