# Enroll logins while agentd runs

`aeon-agentd add-harness --harness NAME` hands enrollment to the running paired
daemon through its authenticated, owner-only socket. The daemon serializes each
step with its normal lifecycle reconciliation, releases the setup lock between
polls, and leaves the service under its existing owner. A stale poll cannot
advance another request. Selecting a different login first polls the saved
request. An expired, denied or revoked request allows a fresh approval request;
a completed request is enrolled before the next login is requested. If approval
is still pending, the refusal names that login and its code and URL. Finish that
approval before selecting another login. An older daemon without this route must
be updated by its service owner before using the new CLI; an unavailable socket
never triggers a competing offline enrollment after a request has been handed over.

Each Claude subscription is a separate enrollment with its own person approval.
Several enrollments may share the same physical Claude profile when its normal
login is rotated. Only the enrollment matching the current subscription is
ready; other enrolled logins retain their identity fences. Claude is ready when
any one of its approved enrollments matches. An unapproved login blocks Claude
with `login_required`, while healthy harnesses remain ready. Account routing,
allowances, spending limits, ownership and Touch ID controls keep their existing
boundaries.

`status` prints the active Claude login, whether it is approved on this computer,
and each enrolled login's account ID. JSON adds local `account_logins`, keyed by
account ID. Active identities are not uploaded with health probes or lifecycle
reports. Local rejections include their HTTP status and bounded diagnostic text;
arbitrary remote response text and terminal control characters are not printed.
`link-account` explains missing installation proof, ambiguous account selection,
failed sign-in checks and fixed server refusals.

Enrollment and account linking use the existing lifecycle proof. A status line
of `Touch ID confirmation: needs pairing upgrade` concerns privileged Touch ID
operations; it is not a prerequisite for these two flows. No new signing key,
pairing replacement or service takeover is performed by enrollment.

## Operator procedure after release

Run the installed `cswap list` before starting and confirm the intended three
logins. The example below uses slots 1, 2 and 3; use the matching slots if the
operator has changed that mapping.
LEAD must hold its automatic Claude rotation for this procedure, using its own
controller; a login must remain selected until its approval and local enrollment
finish. The quota guard must be resumed afterwards. No worker changes that
controller or takes over the service.

If a command times out or loses its connection, keep that login selected and
retry the same `add-harness` command. A matching pending enrollment resumes;
selecting another login before the current approval finishes is refused with
the pending login's code and URL. To enroll a different login after an old
request has expired, been denied or been revoked, select it and rerun
`add-harness`: it polls the old request and asks for a fresh person approval.
This also recovers after a Claude update changes the physical executable path.
If a matching retry reports that the original login's request has ended, rerun
the same command once more to request its fresh approval code. A failed poll
preserves the old request; restore connectivity and retry. Keep the existing
pairing state and service running throughout recovery.

After the service owner has activated the released daemon and matching CLI,
run the following on the paired Mac. Keep the service running:

```sh
pairing_root="$HOME/Library/Application Support/aeon/paired"
aeon-agentd status --state-root "$pairing_root"
"$HOME/.local/bin/cswap" list

"$HOME/.local/bin/cswap" switch 1
aeon-agentd add-harness --state-root "$pairing_root" --harness claude
aeon-agentd status --state-root "$pairing_root"

"$HOME/.local/bin/cswap" switch 2
aeon-agentd add-harness --state-root "$pairing_root" --harness claude
aeon-agentd status --state-root "$pairing_root"

"$HOME/.local/bin/cswap" switch 3
aeon-agentd add-harness --state-root "$pairing_root" --harness claude
aeon-agentd status --state-root "$pairing_root"
```

For **each new login**, the person opens the printed `/agents/register-agent` URL,
enters its pairing code, checks the named login and computer, and approves Connect.
Choose connect-only when no verification run is intended. Optional verification
and ongoing-use/spending approvals remain separate person decisions. Wait for
the command to finish and status to show that login as approved before switching.
An already enrolled login reports that it is already connected; use status to
confirm its matching account rather than requesting another enrollment.

If canonical ownership still needs linking, keep that login selected and use
its UUID from the preceding status output:

```sh
aeon-agentd link-account --setup-root "$pairing_root" --harness claude --account-id ACCOUNT_UUID
```

The person opens the printed `/link` URL, enters its code and confirms the named
account, computer and person. Repeat this optional step for each unlinked login
while its slot is active. A linked account returns without another prompt. To
re-link an account, its current owner first chooses Unlink on `/link`, then the
same command requests a fresh person confirmation. Use `--renew` only for an
expired code. Nothing here transfers ownership through an agent.

Finally switch through slots 1, 2 and 3 again and run status after each normal
daemon probe. Each active login must say `approved on this computer` and Claude
must be ready unless another explicit hold or limit applies. Inactive enrollments
can say `login_required`; they cannot run under a sibling's login. Restore the
operator's chosen slot and resume LEAD's rotation guard.

The running CLI/socket handoff, lock interleaving, polling, re-link transport,
three separately approved shared-profile enrollments, stale-request rejection
and approved/unapproved rotation matrix are covered by fixture tests. The real
post-release approvals and live three-slot check remain person/operator steps.
