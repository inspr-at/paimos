// SPDX-License-Identifier: AGPL-3.0-only

package questions_test

import (
	"net/http"
	"testing"

	"github.com/inspr-at/paimos/internal/nodes"
	"github.com/inspr-at/paimos/internal/questions"
	"github.com/jackc/pgx/v5/pgxpool"
)

func init() {
	questions.NodeRouteMount = func(pool *pgxpool.Pool, mux *http.ServeMux) {
		nodes.New(pool, nodes.SQLWriter{}).Mount(mux)
	}
}

func TestQuestionNodeRoutesStayOutsideTheQuestionsPackage(t *testing.T) {
	if questions.NodeRouteMount == nil {
		t.Fatal("node routes were not installed")
	}
}
