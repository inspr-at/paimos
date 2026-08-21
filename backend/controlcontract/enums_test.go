// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU Affero General Public License as
// published by the Free Software Foundation, version 3.

package controlcontract

import (
	"reflect"
	"testing"
)

func TestFrozenControlRegistries(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		got  []string
		want []string
	}{
		{"actions", Actions(), []string{"issue.priority.set", "run.cancel.queued", "run.cancel.running", "input.respond", "run.pause", "run.resume"}},
		{"command statuses", CommandStatuses(), []string{"pending_confirmation", "accepted", "applied", "rejected", "expired"}},
		{"safe outcomes", SafeOutcomes(), []string{"applied", "rejected", "outcome_unknown"}},
		{"challenge templates", ChallengeTemplates(), []string{"issue_priority_set", "run_cancel_queued", "run_cancel_running", "input_approve", "input_reject", "input_choice", "run_pause", "run_resume"}},
		{"input kinds", InputKinds(), []string{"approval", "choice"}},
		{"input prompt templates", InputPromptTemplates(), []string{"approval_required", "choice_required"}},
		{"input terminal events", InputTerminalEventKinds(), []string{"approve", "reject", "choice", "superseded", "expired", "run_terminal", "cancelled"}},
		{"input options", InputOptionCodes(), []string{"choice_1", "choice_2", "choice_3", "choice_4", "choice_5", "choice_6", "choice_7", "choice_8"}},
		{"runtime states", RuntimeStates(), []string{"running", "paused"}},
		{"operation kinds", OperationKinds(), []string{"grant.put", "grant.revoke", "lease.issue", "lease.renew", "lease.revoke", "input.create", "command.create", "command.confirm", "command.withdraw", "command.claim", "command.result"}},
		{"outbox states", OutboxStates(), []string{"queued", "claimed", "acknowledged", "abandoned"}},
		{"cancellation causes", CancellationCauses(), []string{"operator_command", "execution_timeout", "silence_timeout", "runner_shutdown", "server_cancel"}},
		{"event kinds", EventKinds(), []string{"grant_issued", "grant_renewed", "grant_revoked", "grant_expired", "lease_issued", "lease_renewed", "lease_revoked", "lease_expired", "input_requested", "input_resolved", "input_superseded", "input_expired", "input_cancelled", "input_run_terminal", "runtime_changed", "command_created", "command_expired", "command_withdrawn", "command_accepted", "command_applied", "command_rejected", "effect_queued", "effect_claimed", "effect_outcome_unknown", "effect_acknowledged", "effect_abandoned", "effect_reconciled", "cancellation_recorded"}},
		{"safe reasons", SafeReasons(), []string{"withdrawn", "confirmation_expired", "stale_target", "capability_unavailable", "capability_revoked", "capability_expired", "lease_revoked", "lease_expired", "policy_requires_second_approver", "credential_revoked", "authority_changed", "input_superseded", "input_expired", "run_terminal", "cancelled", "runtime_state_changed", "runner_lost", "natural_exit", "unsupported_platform", "effect_rejected", "process_termination_failed"}},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if !reflect.DeepEqual(test.got, test.want) {
				t.Fatalf("registry drifted\n got: %#v\nwant: %#v", test.got, test.want)
			}
		})
	}
}

func TestRegistryAccessorsReturnCopies(t *testing.T) {
	accessors := []func() []string{
		Actions, CommandStatuses, SafeOutcomes, ChallengeTemplates, InputKinds,
		InputPromptTemplates, InputTerminalEventKinds, InputOptionCodes,
		RuntimeStates, OperationKinds, OutboxStates, CancellationCauses,
		EventKinds, SafeReasons,
	}
	for _, accessor := range accessors {
		first := accessor()
		if len(first) == 0 {
			t.Fatal("registry unexpectedly empty")
		}
		original := first[0]
		first[0] = "mutated"
		if got := accessor()[0]; got != original {
			t.Fatalf("registry accessor leaked mutable backing storage: got %q, want %q", got, original)
		}
	}
}
