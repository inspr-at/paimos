// SPDX-License-Identifier: AGPL-3.0-only
package recurrences_test

import (
	"net/http"
	"testing"

	"github.com/inspr-at/paimos/internal/nodes"
	"github.com/inspr-at/paimos/internal/recurrences"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Risk: recurring work loses release copy or silently bypasses the Done gate.
// Mount both production modules outside recurrences so its tests remain acyclic
// when nodes imports agentruns, which also depends on recurrences.
func TestRecurrenceReleaseCopySurvivesEditsAndDoneGate(t *testing.T) {
	recurrences.CheckReleaseCopySurvivesEditsAndDoneGate(t, func(mux *http.ServeMux, pool *pgxpool.Pool) {
		nodes.New(pool, nil).Mount(mux)
	})
}
