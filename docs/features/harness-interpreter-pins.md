# Harness interpreter pins

Guided setup pins Node's physical path and version privately for npm-launched
Codex, Cursor, Claude and pi. `--node-path` selects an installed Node outside the
workspace, including for shell wrappers. Setup checks the launcher using the
service PATH (`/usr/bin:/bin:/usr/sbin:/sbin`) with the pinned Node directory first;
interactive shell paths and Node injection variables cannot mask missing runtime
dependencies. The same pin is used for account probes and run launches. A missing,
partial, drifted, invalid, or unsafe pin blocks only that account: the paired
daemon and its other accounts keep running, and that harness does not launch
until the pin is fixed. Status names the account, a reason code (`pin_missing`,
`pin_partial`, `pin_drifted`, `pin_invalid`, `pin_unsafe`), and the fix as the
same `{kind, command}` object used by harness details. Claude launches with the
shared Node/SDK pins, so every Claude pin problem, a missing pin included, is
fixed with `aeon-agentd repin --harness claude`. For Codex, Cursor and pi,
`aeon-agentd add-harness --harness NAME` renews only the blocked pin of the
already connected account: the signed-in identity and launcher must match, no
request or approval is created, and a healthy pin is never replaced. If the
identity or launcher changed, remove the enrollment and add the harness again.
Repin and pin renewal never renew authority. Native launchers and saved pi
bindings remain supported. Paths and interpreter versions stay local.
