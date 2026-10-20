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

Each Claude or Codex subscription is a separate enrollment with its own person
approval. For parallel accounts, use separate, already signed-in profile homes:
`aeon-agentd add-harness --harness claude --home /physical/private/profile` (or
`--harness codex`). The directory must already exist, belong to the current
user, be private (0700), and have no symlinked or replaceable ancestors. Setup
never creates or repairs a profile, crawls directories, copies credentials, or
infers vendor identities from labels in another app. The same login in two
homes is refused with `this login is already enrolled from <home>`.

Discovery, the running daemon's enrollment recheck, and runtime use the selected
home. Claude receives `CLAUDE_CONFIG_DIR` and `HOME` for a custom profile;
the default `~/.claude` retains its normal Keychain environment without
`CLAUDE_CONFIG_DIR`. Codex receives `CODEX_HOME`. If the login changes between
discovery and enrollment, the daemon refuses with
`vendor login changed before enrollment`. Each runtime account keeps its own
identity fence and quota probe through its home. Two separately approved logins
in two homes can work side by side without rotation. Account routing, allowances,
spending limits, ownership and Touch ID controls keep their existing boundaries.

Existing enrollments may still share the same Claude profile when its login is
rotated with cswap. Only the enrollment matching that home's current login is
ready; its siblings retain their identity fences. A healthy account in another
home or harness stays ready.

`status` lists each enrolled login's account ID and home. For a live identity
observation it also prints the active login and whether that login is approved
for that home on this computer. JSON adds local `account_logins`, keyed by
account ID, with an additive `home` field. An empty `active_identity` means that
no current identity observation is available; a saved enrollment alone does not
prove the active login. Active identities and homes are not uploaded with health
probes or lifecycle reports. Local rejections include their HTTP status and
bounded diagnostic text; arbitrary remote response text and terminal control
characters are not printed. `link-account` explains missing installation proof,
ambiguous account selection, failed sign-in checks and fixed server refusals.

Enrollment and account linking use the existing lifecycle proof. A status line
of `Touch ID confirmation: needs pairing upgrade` concerns privileged Touch ID
operations; it is not a prerequisite for these two flows. No new signing key,
pairing replacement or service takeover is performed by enrollment.

## Operator procedure after release

After the service owner has activated the released daemon and matching CLI,
the person signs into each explicit profile using the vendor's normal login.
Keep the pairing state and service running. For example, the person creates
private directories and signs in before enrollment:

```sh
pairing_root="$HOME/Library/Application Support/aeon/paired"
claude_second="$HOME/.claude-second"
codex_second="$HOME/.codex-second"
mkdir -m 700 "$claude_second" "$codex_second"
CLAUDE_CONFIG_DIR="$claude_second" claude auth login
CODEX_HOME="$codex_second" codex login

aeon-agentd add-harness --state-root "$pairing_root" --harness claude --home "$claude_second"
aeon-agentd status --state-root "$pairing_root"
aeon-agentd add-harness --state-root "$pairing_root" --harness codex --home "$codex_second"
aeon-agentd status --state-root "$pairing_root"
```

Use physical absolute paths; do not select a symlink. Omitting `--home` keeps
the default `~/.claude` or `~/.codex` profile. `--home` requires exactly one
Claude or Codex harness on `add-harness`; it is not a setup or profile-switching
command. Each home must stay signed into its selected login throughout approval.

For each new login, the person opens the printed `/agents/register-agent` URL,
enters its pairing code, checks the named login and computer, and approves
Connect. Choose connect-only when no verification run is intended. Optional
verification and ongoing-use/spending approvals remain separate person decisions.
Wait for enrollment to finish before adding the next login.

If a command times out or loses its connection, keep that home signed in and
retry the same `add-harness --harness NAME --home PATH` command. A matching
pending enrollment resumes. A different login or home cannot replace a pending
approval; the refusal names the pending login and its code and URL. Once an old
request expires, is denied or is revoked, rerun the command to request a fresh
person approval. A failed poll preserves the old request; restore connectivity
and retry. An already enrolled login reports that it is already connected;
use status to confirm its account rather than requesting another enrollment.

If canonical ownership still needs linking, use the account UUID from status:

```sh
aeon-agentd link-account --setup-root "$pairing_root" --harness claude --account-id ACCOUNT_UUID
```

The person opens the printed `/link` URL, enters its code and confirms the named
account, computer and person. Repeat for each unlinked account. A linked account
returns without another prompt. To re-link, its current owner first chooses
Unlink on `/link`, then the same command requests fresh person confirmation.
Use `--renew` only for an expired code. No agent transfers ownership.

Legacy shared-profile rotation remains supported: the operator selects a cswap
slot and runs `add-harness --harness claude` without `--home`, finishing that
login's approval before rotating again. LEAD holds and resumes its own automatic
rotation controller for that procedure. Workers never change that controller or
take over the service. Separate homes replace rotation when accounts need to
work in parallel.
