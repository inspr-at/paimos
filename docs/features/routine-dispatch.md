# Routine dispatch (AEON-1186)

The server consumes each routine's durable execution intent after its occurrence
commits. Notifications wake the executor; a bounded recovery pass every thirty
seconds also finds missed notifications. Each tenant pass scans at most 33 IDs
and handles 32 intents. A capped pass reports partial in server logs. Each wait
is saved in the execution outbox with a retry time; it never reports a launch.

Execution remains off by default. A person must enable the project's accepted
exact-artifact qualification and consent to the routine's current definition.
Missing runtime observations, qualified admission ages or current CI readings
keep dispatch waiting. Production wiring supplies no invented qualification
readings; coordinator/OPS qualification must bind those readers before enabling.

A live lead keeps its existing owner, session, generation and exact dispatch
credential. Scheduler recovery and coordinator Queue pickup use the same route
transaction. They preserve manual and targeted order and the owner's fair
project turns. Consent, rights, work eligibility, dial, daily quota, account,
host and CI facts are rechecked inside the final fenced write. Release freeze
closes this path; main red or unavailable current CI admission also means wait.
An unreachable Mac or one requiring login receives the existing explicit
unattended-readiness reason and receives no reservation or turn. A Mac whose
daemon has not enrolled in the tenant ledger waits the same way, with its own
reason.

The transaction takes tenant, tree and pairing/account-use fences, then locks
the definition, lead, work/run, account, budget and launch records. It persists
one assignment, account reservation, run-wide budget hold and worker launch
intent. Audit events come last. A refusal rolls back all provisional writes and
saves only a redacted wait receipt. A replay retains the assignment and turn.
The daemon's later claim repeats current authority and admission checks before
launch; unknown process exit or usage retains the existing holds.

If the person has requested a lead but no generation is running, dispatch saves
one lead demand bound to that project generation, routine consent and execution
qualification. The lead runtime owns its subsequent route, reservation and
launch. Without a person-owned lead intent, or with a paused/unavailable lead,
dispatch waits. The executor never launches a process or model, changes a
project switch, starts unrelated delivery rounds, pushes or deploys.
