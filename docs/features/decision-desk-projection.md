# Decision Desk projection

`GET /api/decision-desk/projection` supplies the Decision Desk's canonical
order and exact counts. Held approvals sort by expiry, then other held work by
age, then ordinary open items by age, with source ID ties. Questions with several
askers count once; a question replaces its linked action request. Sign-ins remain
separate chores. Counts cover currently readable sources independently of page
size; coverage above 1000 projects returns an explicit 422. The Agents badge
counts the approvals and held requests its panel can display.

AEON-568's notification adapter (`internal/decisiondesk`) exposes bounded
`NoticesTx`, final `ClaimTx` admission and `CurrentTx` reauthorization for the
existing AEON-455 phone scheduler. Eligible work has a real unfinished work
link or a correlated unresolved held request; a parked label alone is insufficient.
The proposed approval expiry-warning window is 15 minutes, subject to the phone scheduler's quiet hours and escalation.
Notice scans filter project/workspace decision authority before their limit.
The scheduler must admit every candidate through ClaimTx before its transport
check: an approval lacking its native scope authority gets a terminal skipped
claim and returns false, allowing later scans to advance. Final claims take the tree fence,
tenant access fence and source row lock, matching project-access mutations, then recheck only that source's current project,
revision, state and decision authority. Recipient indexes compare native UUIDs.
Claims persist once per source/revision/recipient across devices and replicas,
including action requests later represented by their canonical question.
An ambiguous transport failure retains its claim and records failure rather
than repeating a push. Payloads contain only source pointers. Answer commits,
grace edits, corrections and expiry never emit success notifications; doctrine
keeps its existing per-person toast claim and has no competing desk push.
Phone scheduler activation remains dependent on AEON-455 landing on main;
this package adds no scheduler or competing subscription store. Full P7 acceptance
remains open: AEON-455 owns runtime delivery, quiet-hour/escalation/subscription
tests, agreement on the warning window and integrated doctrine toast suppression.
The current adapter treats approval requests against unfinished ticket/task work
as held; the coordinator must confirm that policy before activation. Durable
claims retain retry evidence rather than being pruned while a source can recur.
