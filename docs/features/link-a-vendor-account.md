# Link a vendor account to yourself

On a paired computer, select your enrolled login with `paimos use` as usual.
`paimos harness run` matches that private login home to the existing registry and
shows one line for an unlinked account: `Link this account to you: <origin>/link
· code 482 913`. Open `/link`, enter that code and confirm the named account,
computer and person once. The terminal then says `Linked to Markus` once;
future launches do not repeat it. Capacity, groups and limits need no form.

For a standalone terminal, run `aeon-agentd link-account --harness claude`
(or `codex`, `grok`, `cursor`, `pi`). Multiple local logins require
`--account-id UUID`; `--setup-root PATH` or `--socket PATH` selects private
pairing state. `harness run` accepts the same optional selectors. The daemon
verifies the enrolled vendor login through its existing adapter before offering
the code. Codes last ten minutes and are single-use. An expired offer remains
expired until `link-account --renew` explicitly requests a new code.

**Your linked accounts** on `/link` and Settings → Accounts offers **Unlink**
as one action, only for the owner. Ownership is distinct from machine pairing,
ongoing-use approval and spending authority; none of those controls change when
a person links or unlinks. Other people on the computer link their own enrolled
logins using the same flow. The person must be signed in; agent keys cannot
confirm or unlink. Tenant-scoped, persistent rate limits and revision-bound
confirmation prevent code guessing, reuse and stale-session approval. Audit
records carry account and person IDs, never codes or installation proofs.
