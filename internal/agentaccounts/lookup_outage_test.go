// SPDX-License-Identifier: AGPL-3.0-only

package agentaccounts

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
)

func TestAccountLookupOutageIsServerError(t *testing.T) {
	reset(t)
	admin := makePrincipal(t, "lookup-outage", "person", "Ada", []string{"admin"})
	mod := accountsMod()
	id := "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
	previous := loadAccount
	t.Cleanup(func() { loadAccount = previous })

	schedule := encoded(t, scheduleOverride{Scope: "account", AccountID: id})
	loadAccount = func(context.Context, pgx.Tx, string) (Account, error) {
		return Account{}, errors.New("injected database outage")
	}
	for _, req := range []struct {
		method, path, body string
	}{
		{http.MethodGet, "/api/agent-accounts/" + id + "/readings", ""},
		{http.MethodPut, "/api/agent-accounts/capacity/schedule", schedule},
	} {
		status, body := call(t, mod, &admin, "", req.method, req.path, req.body)
		if status < 500 || strings.Contains(string(body), "account not found") {
			t.Fatalf("%s %s status %d body %s", req.method, req.path, status, body)
		}
	}

	loadAccount = func(context.Context, pgx.Tx, string) (Account, error) {
		return Account{}, pgx.ErrNoRows
	}
	for _, req := range []struct {
		method, path, body string
	}{
		{http.MethodGet, "/api/agent-accounts/" + id + "/readings", ""},
		{http.MethodPut, "/api/agent-accounts/capacity/schedule", schedule},
	} {
		status, body := call(t, mod, &admin, "", req.method, req.path, req.body)
		if status != http.StatusNotFound || !strings.Contains(string(body), "account not found") {
			t.Fatalf("%s %s missing account status %d body %s", req.method, req.path, status, body)
		}
	}
}
