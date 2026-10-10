// SPDX-License-Identifier: AGPL-3.0-only
package agentpairing_test

import (
	"slices"
	"strings"
	"testing"

	"github.com/inspr-at/paimos/internal/agentpairing"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/hostcapacity"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

// Risk: stale/foreign/revoked host evidence authorizes routines, or a report
// quietly changes manual claims, leaks across computers, or bypasses revocation.
func TestUnattendedReportOnlyFreshnessOwnershipAndClaimSeam(t *testing.T) {
	f := newFixture(t)
	p := f.proposePlatform("darwin", "arm64", "claude")
	f.approve(p, "one_per_harness")
	v := f.redeem(p)
	key := "aeon_" + v.RuntimePrefix + "_" + p.runtime
	if !slices.Contains(v.ServerCapabilities, hostcapacity.UnattendedCapability) || v.HostCapacity.Unattended.Reason != "unattended_unreported" {
		t.Fatal("report capability or missing-report wait absent", v.HostCapacity)
	}
	actor := tenant.Principal{ID: *v.PrincipalID, TenantID: f.tenantID, Kind: tenant.Agent}
	read := func(tenantID, principal string) hostcapacity.UnattendedView {
		t.Helper()
		var out hostcapacity.UnattendedView
		err := db.InTenant(tenant.WithPrincipal(t.Context(), actor), f.db.App, tenantID, func(tx pgx.Tx) error {
			if err := agentpairing.LockMutation(t.Context(), tx); err != nil {
				return err
			}
			var err error
			out, err = agentpairing.UnattendedForPrincipal(t.Context(), tx, principal)
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	signals := hostcapacity.Signals{Cores: 8, MemoryPressure: "unknown", Power: "unknown", Thermal: "unknown", Unattended: &hostcapacity.UnattendedSignals{LoginSession: "ready", IdleSleep: "enabled", FileVault: "on"}}
	var report hostcapacity.View
	decodeResult(t, f.call("POST", "/api/agent-pairing/self/capacity", signals, false, key, 200), &report)
	if report.Reason != "" || report.Unattended.Status != "ready" || report.Unattended.Reason != "" || !strings.Contains(report.Unattended.Message, "idle sleep is enabled") || report.ReportedAt == nil || report.Unattended.AfterRebootReason != "unattended_filevault_after_reboot" {
		t.Fatal("readiness changed ordinary admission or lost reason", report)
	}
	if got := read(f.tenantID, actor.ID); got != *report.Unattended {
		t.Fatal("claim seam lost awake readiness with idle sleep enabled", got)
	}
	var awake agentpairing.View
	decodeResult(t, f.call("GET", "/api/agent-pairing/computers/"+*v.ComputerID, nil, true, "", 200), &awake)
	if *awake.HostCapacity.Unattended != *report.Unattended {
		t.Fatal("Agents page DTO lost awake readiness with idle sleep enabled")
	}
	// The ordinary verification claim remains governed by its existing gate.
	err := db.InTenant(tenant.WithPrincipal(t.Context(), actor), f.db.App, f.tenantID, func(tx pgx.Tx) error {
		if err := agentpairing.LockMutation(t.Context(), tx); err != nil {
			return err
		}
		return agentpairing.RunFence(t.Context(), tx, v.Enrollments[0].AccountID, *v.Enrollments[0].VerificationRunID, true)
	})
	if err != nil {
		t.Fatal("report-only readiness blocked an ordinary claim", err)
	}
	signals.Unattended.IdleSleep = "disabled"
	decodeResult(t, f.call("POST", "/api/agent-pairing/self/capacity", signals, false, key, 200), &report)
	if got := read(f.tenantID, actor.ID); got.Status != "ready" || got != *report.Unattended {
		t.Fatal("claim seam disagrees with report", got)
	}
	var listed agentpairing.View
	decodeResult(t, f.call("GET", "/api/agent-pairing/computers/"+*v.ComputerID, nil, true, "", 200), &listed)
	if *listed.HostCapacity.Unattended != *report.Unattended {
		t.Fatal("person view lost readiness")
	}
	// Server receipt time owns freshness; move stored receipt, never sleep.
	if _, err := f.db.Admin.Exec(t.Context(), `UPDATE agent_pairing_computers SET capacity_reported_at=clock_timestamp()-interval '2 minutes' WHERE id=$1`, v.ComputerID); err != nil {
		t.Fatal(err)
	}
	if got := read(f.tenantID, actor.ID); got.Reason != "unattended_host_unreachable" || got.Status != "wait" || !strings.Contains(got.Message, "asleep, lid closed or waiting for login") {
		t.Fatal("stale report remained available", got)
	}
	decodeResult(t, f.call("GET", "/api/agent-pairing/computers/"+*v.ComputerID, nil, true, "", 200), &listed)
	if listed.HostCapacity.Unattended.Reason != "unattended_host_unreachable" || !strings.Contains(listed.HostCapacity.Unattended.Message, "asleep, lid closed or waiting for login") {
		t.Fatal("Agents page DTO lost plain stale-host wording")
	}
	// An old daemon replaces the complete sample; it cannot keep prior ready
	// signals alive merely by refreshing the enclosing capacity receipt.
	signals.Unattended = nil
	decodeResult(t, f.call("POST", "/api/agent-pairing/self/capacity", signals, false, key, 200), &report)
	if report.Unattended.Reason != "unattended_signals_unknown" {
		t.Fatal("old report revived previous readiness", report)
	}
	signals.Unattended = &hostcapacity.UnattendedSignals{LoginSession: "ready", IdleSleep: "disabled", FileVault: "off"}
	f.call("POST", "/api/agent-pairing/self/capacity", signals, true, "", 403)
	signals.Unattended.IdleSleep = "garbage"
	retryErrorCode(t, f.call("POST", "/api/agent-pairing/self/capacity", signals, false, key, 400), "invalid_request")
	if got := read(f.tenantID, actor.ID); got.Reason != "unattended_signals_unknown" {
		t.Fatal("invalid report changed stored signals", got)
	}
	signals.Unattended = &hostcapacity.UnattendedSignals{LoginSession: "required", IdleSleep: "enabled", FileVault: "on"}
	decodeResult(t, f.call("POST", "/api/agent-pairing/self/capacity", signals, false, key, 200), &report)
	if report.Unattended.Status != "wait" || report.Unattended.Reason != "unattended_login_required" || !strings.Contains(report.Unattended.Message, "Waiting for login") {
		t.Fatal("missing plain waiting-for-login state", report.Unattended)
	}
	decodeResult(t, f.call("GET", "/api/agent-pairing/computers/"+*v.ComputerID, nil, true, "", 200), &listed)
	if *listed.HostCapacity.Unattended != *report.Unattended || read(f.tenantID, actor.ID) != *report.Unattended {
		t.Fatal("login wait lost between capacity, Agents page DTO and claim seam")
	}
	other := newFixtureInTenant(t, f.db, "unattended-other")
	if got := read(other.tenantID, actor.ID); got.Reason != "unattended_computer_unknown" {
		t.Fatal("cross-tenant host evidence leaked", got)
	}
	if got := read(f.tenantID, other.person); got.Reason != "unattended_computer_unknown" {
		t.Fatal("foreign principal became available", got)
	}
	f.call("POST", "/api/agent-pairing/computers/"+*v.ComputerID+"/disconnect", map[string]any{"mode": "revoke_now", "expected_revision": v.Revision}, true, "", 200)
	if got := read(f.tenantID, actor.ID); got.Reason != "unattended_computer_disconnected" {
		t.Fatal("revoked host remained available", got)
	}
	signals.Unattended.IdleSleep = "disabled"
	f.call("POST", "/api/agent-pairing/self/capacity", signals, false, key, 401)
}
