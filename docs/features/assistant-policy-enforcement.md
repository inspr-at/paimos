<!-- SPDX-License-Identifier: AGPL-3.0-only -->
# Assistant policy enforcement and lead delegation

An assistant's current policy is a server ceiling on its existing person,
role, key and project authority. Direct API permission checks apply its scope
ceiling; tenant transaction entry intersects project and individual-node
visibility with its project allowlist. Existing messaging-only identities keep
their inbox-only restriction. A policy grants no key, role or project authority.

Policy is checked under the access-change fence at inbox acceptance, the final
hosted push claim, sleeping-host pull pickup, outward action request/approval,
and agent-owned routine admission/result writes. Both principal-inbox and
project-message listening apply the same per-message pickup policy. Hosted
delivery attempts and first pull pickups reserve the receiving assistant's
`message_cost`, independently of the sender. Ordinary principals incur no
assistant charge. Classified assistants use
the registered hosted adapter or durable inbox; the generic webhook wake path
cannot execute their work. An action approval records consent only; it does
not execute an effect or grant a reusable permission.

Each decision retains an `assistant_policy_receipts` row and
`assistant.policy_evaluated` event. Policy-refused sends retain a failed inbox
receipt and cannot later be fetched. A pull-time refusal fails that message's
receipt with its policy reason and removes it from pickup through the shared
terminal-failure path: System delivery events, one "Not delivered" notice to the
sender and its inbox wake. Audit is deferred until the page's resource writes
finish; permitted messages in the same bounded page still return. Restoring
the grant requires a new send.
A refusal after project access is withdrawn is recorded without a hidden
project reference. Outward refusals/decisions keep the existing Desk, action
history and durable outcome receipts. Assistants rows and the existing Policy
and Activity tabs show policy changes and decisions, including the refusal
filter. Policy changes have a separate bounded query and audit list, so
successful admissions stay in Activity without displacing policy edits. Known
actors use their loaded names; unknown actors have a neutral member label with
their identity available in a keyboard-focusable shared tooltip, dismissible
with Escape. Admission
evidence does not imply that a host is running.

## Weekly accounting and approvals

The week resets at midnight UTC on Monday unless `weekly_reset_day` specifies
Sunday 0 through Saturday 6. Amounts use exact millionths of the configured
USD, EUR, tokens or credits unit. No exchange rate is inferred. A run may have
a positive `budget_ceiling` in its person-granted owner configuration. Without
a known maximum, admission reserves all remaining weekly allowance. Another
charge then fails before dispatch.

Reservations are atomic under the tenant fence and bind an operation key.
Repeated pulls reuse the message reservation. A routine's initial assignment
handoff reuses its run reservation when that hold covers `message_cost`, so an
unknown-cost routine can still receive its assignment without a second hold.
An insufficient run hold is refused as `assistant_message_cost_exceeds_hold`.
Every further hosted delivery attempt reserves another message amount: an
HTTP error or timeout can still incur provider cost. Holds commit at exposure
and are conservatively settled at that amount, including uncertain failures.
Failures before the claim or pickup transaction commits roll back the hold
and its audit together. No provider-reported cost releases allowance.
An omitted message cost defaults to 0.01 USD/EUR or 1 token/credit; policy
editors retain the amount and delegation allowlist even without displaying
controls for them. The policy owner can set any positive exact message amount.
A weekly budget of zero admits no work, including a previously reserved item.
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
For an assistant, the granting person must be its canonical owner or an active
canonical person explicitly listed in `delegator_person_ids`. This consent is
rechecked at grant, dispatch, pickup and result. Previously saved assignments
without consent are held with `assistant_owner_consent_required` before inbox
delivery or spending; withdrawing delegation also closes that assignment.
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
rechecked at dispatch. Exact retries reuse the run; changed facts conflict. The
assistant submits through the existing agent-result route, which rechecks the
current person grant, assistant authority, policy, instance and budget. The
ordinary writer stores the result and atomically records a pointer to the
original lead session. `lead_message_id` identifies its inbox receipt; queueing
does not claim handoff. An ended lead session or withdrawn lead authority fails
only that pointer receipt, with its reason, while retaining the authorized
result. Unexpected persistence failures still roll back the transaction. No
successor session or other principal is substituted.

## Threat notes

Actions, assistant messages addressed to people, Desk questions (including all
answer options) and routine result titles/bodies use one human-input refusal
classifier. Recognized requests for secrets, tokens or manual steps fail with
a receipt before delivery or publication. Desk callers commit only the refusal
evidence; no question or native interaction remains. This bounded recognizer
is defense in depth, not a semantic guarantee for arbitrary paraphrases or
communications outside PAIMOS.

A compromised assistant key remains limited by live key/person/role grants,
policy scopes, project RLS, budget holds and its person-granted result
destination. It cannot widen policy, remove messaging-only restrictions,
release a hold by reporting cheap work, approve outward effects, accept its
Knowledge proposal, or turn delegation into merge/deploy authority. Unknown
cost and unavailable policy fail closed. Provider response bodies and
credentials never enter policy receipts or events.

A hostile routine author cannot bind another person's assistant without that
owner's explicit delegation, spend its allowance through an old invalid grant,
or publish recognized prohibited human requests as routine results. An inbox
sender can consume only the receiving assistant's finite configured allowance
in permitted projects. The sender cannot bypass the same pickup checks through
project listening or trigger a hosted retry without another budget hold.

A malicious lead cannot substitute another owner, tenant, instance, session,
assistant or result destination, expand the person grant, or charge itself in
place of the assistant. A coordinator ceiling alone, an unbound key, an ordinary
routine without a person grant and a revoked lead key do not authorize
dispatch. Delegation transfers no credentials and starts no separate execution
engine. Existing generic builder principals retain their authority; hosted
assistant identity must be explicitly person-linked for assistant execution.

Coverage: `TestAssistantPolicyAdmissionMatrix` (including lock contention and
injected policy weeks), `TestAssistantAdapterPolicyBeforeAcceptanceAndHandoff`,
`TestAssistantAdapterMessagesChargeBudget`,
`TestAssistantProjectListenRechecksPickupPolicy`,
`TestAssistantMessageBudgetCoversPushAndPull`,
`TestAssistantRoutineAssignmentRequiresOwnerConsent`,
`TestAssistantHumanAskRefusedOnInboxAndProjectMessages`,
`TestAssistantHumanAskRefusedInDeskQuestion`,
`TestAssistantHumanAskRefusedInRoutineResult`,
`TestAssistantPullRefusalDoesNotStarveAllowedMessages`,
`TestAssistantOutwardPolicyApproversAndReceipts`,
`TestAssistantLeadDelegationPolicyAndResults`, and
`TestAssistantPolicyDirectAPICeilingAndRLS`. Assistants unit/browser cases cover
refusal presentation, preservation of unexposed policy settings and stable
controls at phone, tablet and desktop widths in both themes. The mixed-page
pickup regression checks the single System notice, its wake and failure events
across repeated pulls. History cases verify focusable actor disclosures without
native titles; the browser case tabs to both history surfaces and checks Escape
dismissal and return of focus to the assistant row.
