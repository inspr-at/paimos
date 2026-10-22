# Live inbox delivery into owned sessions

AEON-1340 connects exact-session durable inbox messages to the session that
agentd already owns. Desk answers take priority over ordinary inbox work.
Committed, content-free harness notification hints trigger reconciliation;
the existing bounded polling path recovers missed hints. A hint grants no
process authority. Messages create no start, resume, adoption or idle-wake request.
The inbox retains the original message and remains the durable record.

Managed harness drains expose additive `priority` metadata for direct inbox,
agent and Desk input addressed to their exact generation. A person's explicit
after-turn project-chat choice keeps its existing meaning. Busy providers
without steering support wait for their supported input boundary, while
stop and interrupt remain serviceable. Paired attached hooks keep their
existing foreground handoff protocol and gain no managed-process authority.

C0-owned structured sessions admit their exact inbox leases through the
existing native command/claim path. The lease ID is the P3a operation ID and
the message ID binds request/input identity. Before registration and each
possible native write, the existing authorities recheck current original
person consent, paired worker lease, exact binding/runtime, local profile,
provider capabilities and claim deadline. No person principal is installed
on the daemon's behalf, and no message can approve a tool or native decision.
Unqualified and terminal sessions keep their inbox fallback.

P3a fsyncs the possible-write marker before provider I/O. Confirmed replay
returns its existing receipt without another native write. Uncertainty holds
the input and prevents automatic reinjection; correlated late evidence can
resolve the hold. The server projects native write acknowledgement separately
from correlated turn evidence. Ordinary inbox hooks cannot concurrently
consume an open managed/native delivery lease.

Each dispatched Desk answer also records metadata-only FYIs for the explicitly
bound project lead and active sessions bound to the same ticket or its immediate
parent. Recipients require current project `questions.read` and `nodes.read`
inside the final fenced transaction. Private question context and answer text
are excluded, and no action or reply obligation is created. A stable
answer/session key deduplicates notices across retries and askers. The selector
has a 128-session bound; overflow is an explicit retryable delivery failure,
never silent truncation. Ended, paused, unrelated and other-project sessions
receive no FYI. Existing leaf-only work lifecycle guards remain in force.

The additive `QuestionPending.delivery` projection supplies the later memo UI
with `decided-grace`, `sent`, `seen`, `inbox`, `successor` and `failed` states,
exact session identity/label, verified-successor flag, safe failure reason and
evidence timestamps. Dispatch and lease acceptance never count as delivery.
Native timestamps come from correlated P3a observations; managed receiver
confirmation retains its existing meaning. Seen does not claim comprehension
or completed work. Verified reciprocal successor routing remains the only
rerouting path; an ended generation is never restarted by delivery.

The memo's web delivery line remains a separate slice after AEON-1681, using
the AEON-1339 design. This change adds no web UI, migration, permission,
production enablement flag or deployment. New files are covered by the existing
agent runtime, inbox and questions audit slices. Coverage extends existing
registered behavior tests; tier manifests and web specs are unchanged.

Remote verification used the approved entry points, without vendor/model calls:
the full inbox, questions, native store and P3a receipt packages passed with
`-race -count=1` on `8d6be5d8af` (mbp2606). The final trusted-Desk ordering
checks passed on `fd76159032` (native composition and harness drain, agm2).
The busy-owner case, including control-shaped self-sent content and a lost
completion acknowledgement, passed on `481fdc3bf6`. Native composition also
proved uncertain-write holds and late acknowledgement followed by separate
correlated seen evidence. Existing successor tests assert the additive memo
projection as well as exact-generation routing.

The priority negative control used an unreferenced stash snapshot
`c0e536b062`, outside the delivery branch's history. Removing only the busy
priority condition made the registered behavior test fail with
`busy priority input reached the owned process 0 times; want 1`. The delivery
branch was restored before further work; no negative-control commit remains
on it. The broader affected race run also passed the other native-session,
integration, harness and API-contract packages, including reportercontract.

Full validation remains open. The full agentd race run reproduced the exact
fixture failures assigned to AEON-1685: the three pause heartbeat/wake tests,
`TestUncertainDeliveryDoesNotMaskHeartbeatFailure`,
`TestInboxUnconfirmedDoesNotReinject`, and
`TestSupervisorQueueHintsUnavailableBackoff/401`. Those fixtures are unchanged
here apart from the minimal priority-case extension in harness notifications.
AEON-1685 was still open when the latest main was merged. The static wrapper
returned exit 3 twice with `remote static check paused at night (night protocol:
mbp2606 is shared with the CI pool); rely on the PR's hosted CI static job`.
Handoff therefore stays at 95 percent, pending that shared race fix and hosted
static checks. No origin push, deployment or model review gate was run.
