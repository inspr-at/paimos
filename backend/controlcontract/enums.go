// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU Affero General Public License as
// published by the Free Software Foundation, version 3.

// Package controlcontract owns the closed, provider-neutral vocabulary of the
// supervisory-control protocol. Keep persistence, HTTP, and runner code on
// these registries so an enum cannot silently acquire a second source of
// truth.
package controlcontract

var actions = []string{
	"issue.priority.set",
	"run.cancel.queued",
	"run.cancel.running",
	"input.respond",
	"run.pause",
	"run.resume",
}

var commandStatuses = []string{
	"pending_confirmation",
	"accepted",
	"applied",
	"rejected",
	"expired",
}

var safeOutcomes = []string{
	"applied",
	"rejected",
	"outcome_unknown",
}

var challengeTemplates = []string{
	"issue_priority_set",
	"run_cancel_queued",
	"run_cancel_running",
	"input_approve",
	"input_reject",
	"input_choice",
	"run_pause",
	"run_resume",
}

var inputKinds = []string{
	"approval",
	"choice",
}

var inputPromptTemplates = []string{
	"approval_required",
	"choice_required",
}

var inputTerminalEventKinds = []string{
	"approve",
	"reject",
	"choice",
	"superseded",
	"expired",
	"run_terminal",
	"cancelled",
}

var inputOptionCodes = []string{
	"choice_1",
	"choice_2",
	"choice_3",
	"choice_4",
	"choice_5",
	"choice_6",
	"choice_7",
	"choice_8",
}

var runtimeStates = []string{
	"running",
	"paused",
}

var operationKinds = []string{
	"grant.put",
	"grant.revoke",
	"lease.issue",
	"lease.renew",
	"lease.revoke",
	"input.create",
	"command.create",
	"command.confirm",
	"command.withdraw",
	"command.claim",
	"command.result",
}

var outboxStates = []string{
	"queued",
	"claimed",
	"acknowledged",
	"abandoned",
}

var cancellationCauses = []string{
	"operator_command",
	"execution_timeout",
	"silence_timeout",
	"runner_shutdown",
	"server_cancel",
}

var eventKinds = []string{
	"grant_issued",
	"grant_renewed",
	"grant_revoked",
	"grant_expired",
	"lease_issued",
	"lease_renewed",
	"lease_revoked",
	"lease_expired",
	"input_requested",
	"input_resolved",
	"input_superseded",
	"input_expired",
	"input_cancelled",
	"input_run_terminal",
	"runtime_changed",
	"command_created",
	"command_expired",
	"command_withdrawn",
	"command_accepted",
	"command_applied",
	"command_rejected",
	"effect_queued",
	"effect_claimed",
	"effect_outcome_unknown",
	"effect_acknowledged",
	"effect_abandoned",
	"effect_reconciled",
	"cancellation_recorded",
}

var safeReasons = []string{
	"withdrawn",
	"confirmation_expired",
	"stale_target",
	"capability_unavailable",
	"capability_revoked",
	"capability_expired",
	"lease_revoked",
	"lease_expired",
	"policy_requires_second_approver",
	"credential_revoked",
	"authority_changed",
	"input_superseded",
	"input_expired",
	"run_terminal",
	"cancelled",
	"runtime_state_changed",
	"runner_lost",
	"natural_exit",
	"unsupported_platform",
	"effect_rejected",
	"process_termination_failed",
}

func clone(values []string) []string {
	return append([]string(nil), values...)
}

func Actions() []string                 { return clone(actions) }
func CommandStatuses() []string         { return clone(commandStatuses) }
func SafeOutcomes() []string            { return clone(safeOutcomes) }
func ChallengeTemplates() []string      { return clone(challengeTemplates) }
func InputKinds() []string              { return clone(inputKinds) }
func InputPromptTemplates() []string    { return clone(inputPromptTemplates) }
func InputTerminalEventKinds() []string { return clone(inputTerminalEventKinds) }
func InputOptionCodes() []string        { return clone(inputOptionCodes) }
func RuntimeStates() []string           { return clone(runtimeStates) }
func OperationKinds() []string          { return clone(operationKinds) }
func OutboxStates() []string            { return clone(outboxStates) }
func CancellationCauses() []string      { return clone(cancellationCauses) }
func EventKinds() []string              { return clone(eventKinds) }
func SafeReasons() []string             { return clone(safeReasons) }
