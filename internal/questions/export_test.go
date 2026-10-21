// SPDX-License-Identifier: AGPL-3.0-only

package questions

import (
	"net/http"

	"github.com/jackc/pgx/v5/pgxpool"
)

// NodeRouteMount serves the real /api/nodes and /api/kinds routes during tests.
// Package questions_test installs it. An in-package test cannot import nodes:
// nodes imports harness, harness imports native interaction, and interaction
// imports questions.
var NodeRouteMount func(pool *pgxpool.Pool, mux *http.ServeMux)
