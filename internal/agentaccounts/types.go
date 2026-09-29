// SPDX-License-Identifier: AGPL-3.0-only

package agentaccounts

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/inspr-at/paimos/internal/agentpairing"
	"github.com/inspr-at/paimos/internal/httpapi"
	"github.com/inspr-at/paimos/internal/tenant"
)

// Account is an opaque local enrollment. AccountKey is not a vendor credential.
type Account struct {
	ID                string     `json:"id"`
	AccountKey        string     `json:"account_key"`
	Harness           string     `json:"harness"`
	DaemonID          string     `json:"daemon_id"`
	Label             string     `json:"label"`
	Plan              string     `json:"plan"`
	HostLabel         string     `json:"host_label"`
	AllowedProfileIDs []string   `json:"allowed_model_profile_ids"`
	MaxParallel       int        `json:"max_parallel_runs"`
	RegisteredBy      string     `json:"registered_by_principal_id"`
	State             string     `json:"state"`
	LastProbeAt       *time.Time `json:"last_probe_at"`
	LastProbeOK       *bool      `json:"last_probe_ok"`
	CreatedAt         time.Time  `json:"created_at"`
	Windows           []Window   `json:"windows"`
	daemonGeneration  *string
}

// Window is one allowance bound for a single unit.
type Window struct {
	// Internal routing metadata; never accepted from or serialized to user APIs.
	pairingVerification bool
	capacityReadAt      *time.Time
	capacityAllowed     bool
	capacityKind        string
	capacityBucket      string
	capacityRetired     bool
	capacityRefreshRun  *string
	capacityBudget      *float64

	ID          string    `json:"id"`
	AccountID   string    `json:"account_id"`
	StartsAt    time.Time `json:"starts_at"`
	EndsAt      time.Time `json:"ends_at"`
	Unit        string    `json:"unit"`
	Allowance   int64     `json:"allowance"`
	PaceModel   string    `json:"pace_model"`
	BurstRatio  float64   `json:"burst_ratio"`
	Used        int64     `json:"used"`
	Reserved    int64     `json:"reserved"`
	Provisional bool      `json:"provisional"`
}

// Reservation is one held estimate against a window.
type Reservation struct {
	ReservationID string `json:"reservation_id"`
	WindowID      string `json:"window_id"`
	Unit          string `json:"unit"`
}

// RouteResult is the account chosen for a queued run.
type RouteResult struct {
	AccountID    string        `json:"account_id"`
	AccountKey   string        `json:"account_key"`
	AccountLabel string        `json:"account_label"`
	DaemonID     string        `json:"daemon_id"`
	Reservations []Reservation `json:"reservations"`
}

// HarnessHealth summarises whether a harness can take new work.
// Accounts counts every enrolled row. Available counts rows that are
// available, recently probed, and under their parallel cap. Dispatchable
// also requires pace headroom on every active window.
type HarnessHealth struct {
	Accounts     int
	Available    int
	Dispatchable int
}

type httpError struct {
	status int
	msg    string
}

func (e *httpError) Error() string { return e.msg }

func fail(status int, msg string) error { return &httpError{status: status, msg: msg} }

var uuidRE = regexp.MustCompile(`(?i)^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
var accountKeyRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]*$`)

func principal(w http.ResponseWriter, r *http.Request) (tenant.Principal, bool) {
	p, ok := tenant.PrincipalFrom(r.Context())
	if !ok || p.ID == "" || p.TenantID == "" {
		httpapi.WriteError(w, http.StatusUnauthorized, "authentication required")
		return tenant.Principal{}, false
	}
	return p, true
}

func requireAgent(p tenant.Principal) error {
	if p.Kind != tenant.Agent {
		return fail(http.StatusForbidden, "agent key required")
	}
	return nil
}

func decodeJSON(w http.ResponseWriter, r *http.Request, dst any) error {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return fail(http.StatusBadRequest, "invalid JSON request body")
	}
	var extra any
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		return fail(http.StatusBadRequest, "request body must contain one JSON value")
	}
	return nil
}

func writeErr(w http.ResponseWriter, err error) {
	var pairingErr *agentpairing.Error
	if errors.As(err, &pairingErr) {
		agentpairing.WriteError(w, err)
		return
	}
	var he *httpError
	if errors.As(err, &he) {
		httpapi.WriteError(w, he.status, he.msg)
		return
	}
	var pe *pgconn.PgError
	if errors.As(err, &pe) {
		switch pe.Code {
		case "23505":
			httpapi.WriteError(w, http.StatusConflict, "account already exists")
			return
		case "23514", "23503":
			httpapi.WriteError(w, http.StatusBadRequest, "invalid account")
			return
		}
	}
	httpapi.WriteError(w, http.StatusInternalServerError, "database operation failed")
}

func validHarness(s string) bool {
	switch s {
	case "codex", "claude", "pi", "cursor", "grok":
		return true
	default:
		return false
	}
}

func validUnit(s string) bool {
	switch s {
	case "requests", "tokens", "cost_micros":
		return true
	default:
		return false
	}
}

func validPace(s string) bool {
	switch s {
	case "steady", "frontload", "unrestricted":
		return true
	default:
		return false
	}
}

func validState(s string) bool {
	switch s {
	case "available", "draining", "unavailable":
		return true
	default:
		return false
	}
}

func cleanText(s string, max int) (string, error) {
	s = strings.TrimSpace(s)
	if s == "" || len(s) > max || strings.ContainsAny(s, "\x00\r\n") {
		return "", fail(http.StatusBadRequest, "invalid text")
	}
	return s, nil
}

func looksLikeCredential(s string) bool {
	lower := strings.ToLower(s)
	for _, prefix := range []string{"sk-", "sk_", "aeon_", "ghp_", "github_pat_", "xox", "ya29.", "glpat-", "akia"} {
		if strings.HasPrefix(lower, prefix) {
			return true
		}
	}
	if strings.Contains(lower, "begin private") || strings.Contains(lower, "begin rsa") || strings.Contains(lower, "begin openssh") {
		return true
	}
	return strings.HasPrefix(s, "eyJ") && strings.Count(s, ".") == 2
}
