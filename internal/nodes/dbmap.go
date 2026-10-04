// SPDX-License-Identifier: AGPL-3.0-only

package nodes

import (
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/inspr-at/paimos/internal/events"
)

func mapDB(err error) *httpError {
	if events.PortalCatalogDenied(err) {
		return &httpError{status: http.StatusForbidden, msg: "permission denied"}
	}
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		return nil
	}
	switch pgErr.Code {
	case "42501":
		if pgErr.Message == "decision desk nodes require the question service" {
			return &httpError{status: http.StatusForbidden, msg: "use the Decision Desk question API"}
		}
		return nil
	case "40P01":
		return conflictCoded("concurrent node update; retry request", "retryable_conflict")
	case "23505":
		switch {
		case strings.Contains(pgErr.ConstraintName, "slug"):
			return conflict("kind slug already exists")
		case strings.Contains(pgErr.ConstraintName, "key"):
			return conflict("node key already exists")
		default:
			return conflict("conflict")
		}
	case "23503":
		if strings.Contains(pgErr.ConstraintName, "kind") {
			return badRequest("kind not found")
		}
		return badRequest("related row does not exist")
	case "23514":
		return badRequest("invalid value")
	case "22P02":
		return badRequest("bad request")
	case "P0001":
		switch pgErr.Message {
		case "parent status follows its children":
			return conflictCoded("parent status follows its children", "parent_status_derived")
		case "node has live children":
			return conflict("node has live children")
		case "child kind is not allowed under parent kind":
			return conflict("child kind is not allowed under parent kind")
		case "node move would create a cycle":
			return conflict("node move would create a cycle")
		case "node cannot parent itself":
			return conflict("node cannot parent itself")
		case "parent node does not exist or is deleted":
			return badRequest("parent node does not exist or is deleted")
		case "cannot restore node under deleted parent":
			return conflict("cannot restore node under deleted parent")
		case "invalid tenant or key prefix":
			return badRequest("invalid key prefix")
		case "node key is reserved by alias", "node key is current":
			return conflict("node key already exists")
		default:
			return nil
		}
	default:
		return nil
	}
}

func dbErr(op string, err error) error {
	if err == nil {
		return nil
	}
	if he := mapDB(err); he != nil {
		return he
	}
	return fmt.Errorf("%s: %w", op, err)
}
