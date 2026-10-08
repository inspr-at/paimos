# Pairing readiness and diagnostics

`aeon-agentd status` reads the last atomic setup snapshot and live daemon status
without taking `setup.lock`, reconciling enrollment, or performing cleanup.
Use `pair`/`setup` to resume setup and `disconnect` to resume cleanup. Approved
but unbound harnesses name the required `add-harness` command. Account checks
report a 60-second bound from daemon start for each initial account, or from
that account being added or unblocked by repin; refreshing unchanged accounts
does not extend the wait. Capacity capture reports a 10-second bound.

**Verify again (AEON-685).** An expired one-time verification is informational
when that enrollment has a fresh successful probe; it does not revoke separately
approved ongoing use. A sibling's readiness never qualifies it. The account row's
**Verify again** button authorizes one fresh read-only check for the signed-in
account owner with `account.manage` (the original approver owns an unlinked
account). Visibility into the internal verification project is not required,
and requesting a check grants no project access. The server checks the computer
revision and prior run again under the mutation lock, cancels only that account's
unclaimed queued check, releases its holds and creates one new run with a
thirty-minute, one-request allowance.
Claimed nonterminal checks must be reconciled first. Account identity, runtime
key, daemon generation, pairing approval and cleanup remain intact.
`aeon-agentd verify --account ID` opens the existing account's owner approval
screen; `--no-browser` prints its link. It uses read-only pairing state and neither
contacts nor restarts the daemon. The link grants no allowance: the signed-in
owner must choose **Verify again**. Use `--state-root PATH` for a fixture or a
non-default pairing root. The approval link initially expands Accounts and
computers; its Fold control still honors the person's choice.

Unsupported or incomplete verification fails with `verification_unavailable`
and a bounded cause, independently of account probing. Connect-only approval
creates no verification, and a later approval cancels older queued verification
on that same computer without interrupting claimed work.

Failed Claude sign-in checks include an allowlisted `reason_detail` in
`aeon-agentd status --json` and the person account views. No raw vendor output,
credential paths or arbitrary error strings are published. In particular,
Claude's default profile can be discovered while its directory has vendor-created
`0755` permissions, but the daemon requires a private profile before it runs the
sign-in check. This leaves verification queued with `probe_failed`. For that
specific diagnostic the operator repair is `chmod 700 "$HOME/.claude"`; the next
successful probe clears the reason and permits the existing verification to
proceed. Aeon does not change the vendor profile's permissions automatically.
Claude's quota reading still depends on supported native capture or a managed
run; fixing sign-in does not fabricate an allowance reading.

New pairing-owned macOS launchd services write diagnostics to
`~/Library/Logs/aeon-agentd/{stdout,stderr}.log` in a private `0700` directory.
Existing receipt-bound service definitions remain owned and unchanged; logging
is added when a new service is installed. Managed Nix/Home Manager services
retain their configuration ownership.

The paired daemon writes bounded `agentd polling diagnostic` lines to stderr
when the set of causes changes, then at most one reminder per cause every
15 minutes while the set persists. A healthy poll clears the set, so a recurring
cause is logged immediately. Multiple accounts sharing a cause produce one line:
`reason=probe_failed` records an account readiness failure (a failed probe/report
or a retained ownership/reporting block);
`reason=probe_timeout` confirms an account exhausted its own pending probe wait;
`reason=queue_unavailable` confirms `Queued()` returned an error before probing;
`reason=dispatch_not_allowed` confirms a dispatch fence, fence-read failure or
daemon shutdown prevented polling or an account probe. Correlate these lines
with read-only `aeon-agentd status --json` for the affected account. Queue errors
can therefore explain a subsequent probe timeout; the timeout alone does not
identify the underlying cause. Raw errors and private bindings are not logged.

Pairing reconciliation and runtime refresh failures report `pairing_sync_failed`
with a fixed short cause in local account/harness status and subsequent lifecycle
reports. They log `agentd pairing diagnostic` once per distinct cause per daemon
process, without raw errors, paths or credentials. Polling stays blocked until
reconciliation and runtime validation succeed; recovery clears the diagnostic.
Revoked enrollments retain their fences without requiring an old local candidate;
live enrollments match the approved account key and harness, independently of a
changed display label. Unapproved live accounts still block reconciliation.
