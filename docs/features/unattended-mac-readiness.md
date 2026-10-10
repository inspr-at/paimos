# Unattended Mac readiness

AEON-1088 adds report-only evidence for routines. It does not enable automatic
dispatch or change existing run, verification, account, owner or capacity gates.
No migration, service replacement, power-setting change, automatic login or
FileVault change is involved. The release note remains hidden until routines
consume this evidence.

The paired daemon negotiates `unattended-v1` from `server_capabilities` on
`GET /api/agent-pairing/self`. Only a supporting server receives the optional
`unattended` member of the existing `POST /api/agent-pairing/self/capacity` report.
An older server receives the prior payload. An older daemon replaces the sample
without unattended evidence, so refreshing capacity cannot revive prior readiness.

On a Mac, fixed read-only probes inspect the console owner's numeric UID,
`pmset -g` and `fdesetup status`. Only three bounded enums are reported:
`login_session`, `idle_sleep` and `filevault`. No names, UIDs, raw command output,
credentials or configuration contents leave the Mac. All host probes share the
existing two-second sample deadline and bounded output. Unreadable probes remain
`unknown`; temporary sleep assertions and display sleep do not qualify system
idle sleep. The console session must belong to the daemon user; a different
console user conservatively produces `unattended_login_required`.

Computer views and capacity responses expose `host_capacity.unattended`, with
`status`, stable `reason`, a human-readable `message`, `after_reboot_reason` and
`after_reboot_message`. A connected Mac is currently `ready` only with a report
received within one minute, a confirmed login session and disabled system idle
sleep in the current power profile. This remains an observation: manual sleep,
lid closure, switching power profiles, restart and power loss can interrupt work.
Unsupported platforms and missing evidence are `wait`, never assumed ready.

Reboot recovery is separate. Mac user LaunchAgents need login after reboot;
enabled FileVault adds an unlock requirement. FileVault does not by itself
block a currently logged-in Mac. Apple documents both the
[user-login LaunchAgent lifecycle](https://developer.apple.com/library/archive/documentation/MacOSX/Conceptual/BPSystemStartup/Chapters/CreatingLaunchdJobs.html)
and [FileVault recovery](https://support.apple.com/en-gb/guide/security/sec8447f5049/web).
An authenticated unlock path does not prove this user's LaunchAgent or agent
accounts will become ready automatically.

The server's receipt time controls freshness; the daemon supplies no timestamp.
After sleep or reboot it cannot report until it resumes. An old receipt produces
`unattended_host_unreachable`, explaining that the Mac may be asleep, restarting,
waiting for login or unreachable. The product does not infer the precise cause
from silence. A fresh report restores readiness only if the actual observations
qualify it. Capacity policy remains independent, including its existing off mode.

Routines slices S11/S13/S14 consume
`agentpairing.UnattendedForPrincipal(ctx, tx, principalID)`. It reads the exact
tenant-scoped paired computer and returns `wait` for missing, disconnected,
revoked, stale or unqualified evidence. Database errors must also produce WAIT
in the caller. Under `agentpairing.LockMutation`, call it again in the final
assignment/claim transaction before appending events, alongside the current
owner, account, generation, capability, consent and budget checks. Do not reuse
a routing observation as claim authority. Persist/show its reason on the routine
wait and exclude waiting hosts from available capacity. This ticket provides the
evidence and seam; those slices own dispatch integration and routine UI.

Validation covers read-only probe failures, login and system-sleep distinctions,
injected-clock freshness, reboot constraints, compatibility negotiation and
rollback, server persistence, ordinary claim compatibility, and tenant/principal
isolation and revocation.
