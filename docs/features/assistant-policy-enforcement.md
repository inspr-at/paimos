<!-- SPDX-License-Identifier: AGPL-3.0-only -->
# Assistant policy enforcement and lead delegation

An assistant's current policy is a server ceiling on its existing person,
role, key and project authority. Direct API permission checks apply its scope
ceiling; tenant transaction entry intersects project and individual-node
visibility with its project allowlist. Existing messaging-only identities keep
their inbox-only restriction. A policy grants no key, role or project authority.

Policy is checked under the access-change fence at inbox acceptance, the final
hosted push claim, sleeping-host pull pickup, outward action request/approval,
and agent-owned routine admission/result writes. Ordinary internal inbox
transport is free. Hosted sends and routine work reserve the receiving
assistant's allowance independently of the caller. Classified assistants use
the registered hosted adapter or durable inbox; the generic webhook wake path
cannot execute their work. An action approval records consent only; it does
not execute an effect or grant a reusable permission.

Each decision retains an `assistant_policy_receipts` row and
`assistant.policy_evaluated` event. Policy-refused sends retain a failed inbox
receipt and cannot later be fetched. A pull-time refusal leaves queued work
unread and returns a policy error. Restoring the grant permits another pickup.
A refusal after project access is withdrawn is recorded without a hidden
project reference. Outward refusals/decisions keep the existing Desk, action
history and durable outcome receipts. Assistants rows and the existing Policy
and Activity tabs show decisions, including the refusal filter. Admission
evidence does not imply that a host is running.

## Weekly accounting and approvals

The week resets at midnight UTC on Monday unless `weekly_reset_day` specifies
Sunday 0 through Saturday 6. Amounts use exact millionths of the configured
USD, EUR, tokens or credits unit. No exchange rate is inferred. A run may have
a positive `budget_ceiling` in its person-granted owner configuration. Without
a known maximum, admission reserves all remaining weekly allowance. Another
charge then fails before dispatch.

Reservations are atomic under the tenant fence and bind an operation key.
Retries and the adapter handoff of the same run reuse its reservation.
Assistant-reported costs remain unverified telemetry and never release a hold,
including a reported zero. Changing budget units while this week contains
other-unit reservations fails closed. A changed reset weekday still counts
reservation timestamps in the newly selected calendar window. These are
admission holds, not measured vendor invoices; the existing UI does not label
them as measured spend or claim control over off-platform billing.

Outward actions always require an interactive person with existing project
decision permissions. `approver_person_ids` optionally restricts that list;
empty uses the canonical assistant owner. Current policy, requester authority
and budget availability are rechecked on approval. A new policy refusal
resolves the request as denied with its reason and outcome receipt. A lead's
agent key cannot approve its own outward request.

## Delegation flow

The person creates a project routine and grants its agent owner and fixed
ticket-comment, new-ticket or proposed-Knowledge destination through the
existing agent-owner endpoint. The grant may include `budget_ceiling`.
Messaging-only principals cannot acquire routine or result-write authority
through this flow. A person may link an already scoped regular agent into the
assistant class only when every usable key is bound to that same canonical
person. Linking changes no key, role, scope or messaging-only marker.

The coordinator calls `POST /api/recurrences/{recurrenceId}/delegate` with
`session_id`, `instance`, `scopes`, `idempotency_key` and `expected_revision`.
The task is the granted routine's frozen template. Requested scopes must
match the granted result/read scopes and fit the assistant policy. The current
coordinator session, key's person binding, routine grant and assistant owner
must agree in this tenant. Instance identity must match server configuration;
no peer instance is contacted.

Accepted delegation reserves the assistant budget before creating its
occurrence and uses the existing routine outbox/inbox dispatch. The original
lead key, session, owner, scopes and instance remain bound to the run and are
rechecked at dispatch and result return. Exact retries reuse the run; changed
facts conflict. The assistant submits through the existing agent-result route.
The ordinary writer stores the result and atomically queues a pointer to the
original lead session. `lead_message_id` identifies its inbox receipt; queueing
does not claim handoff. Failure to queue the pointer rolls back the result.
No successor session or other principal is substituted.

## Threat notes

A compromised assistant key remains limited by live key/person/role grants,
policy scopes, project RLS, budget holds and its person-granted result
destination. It cannot widen policy, remove messaging-only restrictions,
release a hold by reporting cheap work, approve outward effects, accept its
Knowledge proposal, or turn delegation into merge/deploy authority. Unknown
cost and unavailable policy fail closed. Provider response bodies and
credentials never enter policy receipts or events.

A malicious lead cannot substitute another owner, tenant, instance, session,
assistant or result destination, expand the person grant, or charge itself in
place of the assistant. A coordinator ceiling alone, an unbound key, an ordinary
routine without a person grant and a revoked lead key do not authorize
dispatch. Delegation transfers no credentials and starts no separate execution
engine. Existing generic builder principals retain their authority; hosted
assistant identity must be explicitly person-linked for assistant execution.

Coverage: `TestAssistantPolicyAdmissionMatrix` (including lock contention and
injected policy weeks), `TestAssistantAdapterPolicyBeforeAcceptanceAndHandoff`,
`TestAssistantOutwardPolicyApproversAndReceipts`,
`TestAssistantLeadDelegationPolicyAndResults`, and
`TestAssistantPolicyDirectAPICeilingAndRLS`. Assistants unit/browser cases cover
refusal presentation, preservation of unexposed policy settings and stable
controls at phone, tablet and desktop widths in both themes.
