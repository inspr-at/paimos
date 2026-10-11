# Routine launch intents and workspaces

Routine demand carries an immutable run marker. A routine can claim only its
persisted launch intent, with the exact tenant, owner, project, account,
registered Mac, assignment/consent revisions, daemon generation, qualification
pins and remaining budget grant. Existing manual runs keep their claim protocol.
Missing capability or incomplete intent returns a bounded refusal.

Routine lineage retains S03/S05 scope visibility. A launch grant supplies no
read override for a private personal definition. The current claim path requires
authorized visibility to the linked routine run; private execution needs S13's
owner-scoped validation integration before that capability can be qualified.

Automatic execution remains off. S04 person consent and accepted qualification
are required independently of the daemon's advertised capability. S13 must bind
`agentruns.BindRoutineLaunch` to trusted runtime observations and S09's current
admission/hold validation, then persist the assignment using
`PrepareRoutineLaunchTx`. These callbacks perform bounded transactional work;
no native process or network call runs in the claim transaction. The daemon
repeats the claim immediately before its durable possible-fork boundary.

The local `RoutineWorkspaceConfig` is owner-approved configuration, never a
prompt-derived repository URL or path. Each mapping binds the project/account
to an existing physical repository, exact base commit, private worktree root,
computer and qualified artifact pins. A routine creates a fresh branch and Git
worktree. Allocation intent is persisted before Git runs; incomplete allocations
remain disputed. Each checkout has its own durable owner record and OS lock,
independent of the pairing ledger. Replaying an assignment cannot create another
writer. Worktrees and branches survive process exit, failure and recovery.
Capacity continuation binds the new launch to the previous local checkout and
branch only after exact root and fixture exit proof. The new grant retains its
own resource lease and current claim authority; it cannot adopt a different
owner, account, host or routine lineage.

Pre-launch checkout disables hooks, fsmonitor and effective filters, including
included and worktree-specific configuration. Metadata is allocated without
checkout first, so destination-specific includes are sanitized before files
are materialized. Interrupted routing replays the persisted routine pickup and
keeps checkout, resource and ledger ownership evidence through unconfirmed
refusals. After a route conflict, an independent run read may prove the attempt
obsolete: terminal or already-started runs release the ledger hold and the
never-launched resource/checkout owners. A queued routine remains retained,
including a bound account whose route response may have been lost. The terminal
route intent stays in the journal as the allocation's recovery anchor; cleanup
errors are reported and retried. The branch and worktree files remain intact.

Managed Git uses the owned linked worktree's index and HEAD, common objects and
only its branch's writable refs and logs. Sibling checkouts and their private
worktree metadata remain outside the terminal sandbox.

Resource ownership uses AEON-1089's lease surface (`Environment`, `HandoffPorts`,
`Running`, `Finish`), through `AcquireResources`. S14 adapts the existing
`runisolation.Acquire` allocator when that dependency is integrated. There is no
second port/database allocator. The lease supplies unique ports, database and
temporary directory plus fixture ownership. Missing resource wiring refuses
launch. A qualified S10/S15 adapter must declare routine support and apply the
resource environment to every native child and fixture. Existing adapters do
not declare that qualification and remain unavailable for automatic routines.

The journal schema advances to 6 with forward migrations for 2–5. Existing
routine provenance remains intact; continuation adds its original checkout
launch identity. Older readers refuse the new schema before mutation. Unknown
process exit never clears a checkout or pairing hold. Root exit alone does not
prove fixture cleanup: resource release requires independent fixture-exit
evidence, or durable proof that native launch was never attempted. Unconfirmed
cleanup retains the resource allocation. Exit receipts are append-only and do
not manufacture budget settlement.

The daemon also retains its live checkout fence and refuses shutdown when
fixture exit is unknown, even after root-process exit. Existing allowance
reservations remain active until an accepted routine exit receipt permits
settlement. Reconciliation preserves the exact assigned daemon generation.
