# Git-backed doctrine (AEON-318)

`GET /api/rules/doctrine` accepts people and agent keys with `rules.read`.
Source management remains person-only (`settings.manage`). GitHub reads use
only `https://api.github.com`; redirects are refused, including repository
renames, so configure the current repository name.

For existing token-file sources, the operator provisions private read-only tokens under the absolute directory
`AEON_DOCTRINE_CREDENTIALS_DIR`. A reference such as `doctrine-private-read`
requires both the token file of that name and `doctrine-private-read.allowlist.json`:

```json
{"grants":[{"tenant_id":"<workspace UUID>","repository":"owner/repository"}]}
```

Each grant authorizes exactly one tenant/repository pair (names are compared
case-insensitively); repeat pairs to share a credential deliberately. The
server reads this policy before resolving a pin, fetching an index, or serving
cached doctrine. Missing, invalid, empty or nonmatching policies fail closed
with `credential unavailable`. Existing token files need this policy before
use. Removing a grant immediately prevents subsequent requests from reading
its cached doctrine or fetching again. Aeon has no API to write these grants:
the operator owns the directory and files, and the service has read access
only. Tenant owners may name references but cannot authorize them. Never put
token values in tenant configuration, API requests, logs or this repository.

## Deployment proposal repositories (AEON-1043)

Proposal destinations are host policy, separate from the sources a workspace
can read and index:

- `AEON_DOCTRINE_PUBLIC_REPOSITORY`: GitHub `owner/repository`; when unset,
  defaults to `inspr-at/inspr-modules`. Set it explicitly to an empty string
  to disable public proposals, including inbox and outcome-analysis drafts.
- `AEON_DOCTRINE_PRIVATE_REPOSITORY`: GitHub `owner/repository`; when unset,
  defaults to `inspr-at/inspr-doctrine-private`. An explicitly empty value is
  invalid: public publication always needs a private quotation boundary.

Use GitHub's canonical owner/repository spelling. Malformed names and the same
repository on both sides (even with different case) fail startup before
credential files are read. The configured source visibility and the App
installation's reported visibility must match the public/private boundary; visibility changes are refused before writes.

For an Example Business private-only deployment, set the public variable to an
empty string and the private variable to `example-business-team/agm-doctrine`.
Public sources such as `inspr-at/inspr-modules` remain readable and indexable,
but accept no proposals from this deployment, even with an App grant.

`AEON_DOCTRINE_APP_TENANT_ID` remains the sole writer workspace. Existing App,
DCO, independent review gate, credential allowlist and
`AEON_DOCTRINE_GUARD_KEY_FILE` requirements still apply. Credentials are refused
in both repositories. Public identity and quotation checks use the configured
pair, including the full private tree; missing, stale or revoked private
corpora fail closed. Reindex the configured private source to populate its
quotation guard; startup can rebuild an absent or outdated guard. Agents save
inbox drafts; a person publishes, and the independent gate controls merge and
release dispatch. Repository configuration grants none of those authorities.

Migration `1308_doctrine_repository_boundary.sql` widens the released proposal
and machine-pin repository checks to bounded GitHub names without changing
rows or tenant isolation. Its exact-byte policy exception is a draft review
artifact: coordinator approval and the previous-binary compatibility gate are
required before merge or release. Configure destinations only after this
migration is deployed through the normal release process.


## Sources without static tokens (AEON-1063)

`credential_ref: github-app` uses the existing PAIMOS App key, installation
and `AEON_DOCTRINE_APP_TENANT_ID`. The key's existing
`<AEON_DOCTRINE_APP_KEY_REF>.allowlist.json` must grant that workspace and the
exact configured public or private repository. No file named `github-app`
or additional token is needed. Each resolve, index or guard rebuild requests
one repository with `contents:read` only, verifies the returned repository,
visibility and permissions, and revokes the short-lived token after the
operation, including failed fetches. Missing installations, extra permissions
and mismatched repositories fail closed. Read authority does not require or
grant the DCO and independent publication gate; proposals retain those checks.

An explicitly set `AEON_DOCTRINE_PRIVATE_REPOSITORY`, together with
`AEON_DOCTRINE_APP_TENANT_ID`, enables host-owned default registration at
startup and when a person in that host-configured workspace reindexes a source.
Default-source maintenance is best-effort: its failure is logged and does not
block reindexing a different source. Other workspaces never trigger it.
The private repository is added
with `main`, default `docs/AGENTS-*.md` paths and `github-app`, then indexed.
Concurrent starts and restarts keep the same source and produce just one
`doctrine.source_added` event, attributed to System. A tenant-registered source
at that repository keeps its credential, paths and pin. Missing host grants
never create a source. Leaving the private repository variable unset keeps the
previous registration behavior.

### PMA host mirror

The PMA deployment uses its own host authority. An INSPR App key is not copied
into that trust context. For `example-business-team/agm-doctrine`, configure:

```text
AEON_DOCTRINE_PUBLIC_REPOSITORY=
AEON_DOCTRINE_PRIVATE_REPOSITORY=example-business-team/agm-doctrine
AEON_DOCTRINE_APP_TENANT_ID=<PMA workspace UUID>
AEON_DOCTRINE_MIRROR_DIR=/run/paimos/doctrine-mirrors
```

A nonempty mirror directory selects `credential_ref: host-mirror` for default
registration. No App configuration or credential directory is needed for
mirror reads. OPS owns a host fetch job and its read-only deploy key; neither
the key nor git metadata is mounted into PAIMOS. The job publishes an exported
tree at:

```text
/run/paimos/doctrine-mirrors/example-business-team/agm-doctrine/
  .aeon-commit
  docs/AGENTS-KERNEL.md
  ...
/run/paimos/doctrine-mirrors/host-mirror.allowlist.json
```

`.aeon-commit` contains the full lowercase 40-digit commit SHA, optionally
followed by one newline. The allowlist uses the same `grants` JSON format
shown above, naming the PMA workspace and `example-business-team/agm-doctrine`.
This is host policy with no tenant write API. Publish a complete immutable
export and marker together, mounted read-only in PAIMOS's filesystem namespace;
file mode bits alone are insufficient. The reader checks the actual mount of
the opened directory and each file, confines paths, refuses symlinks and git
metadata, and bounds tree depth, file count and bytes. Missing or malformed
markers, writable mounts and escaping paths fail closed.

Mirror reads make no network calls. The visible index and the full private
quotation guard use the same bounded snapshot at the marker commit. A changed
marker keeps the authorized indexed pin available for rule delivery, with a
stale warning in the visible index and session-file release pointer. Delivery
uses only cached bytes at that pin, never the new export. The private quotation
guard refuses a moved marker until reindex; reindex advances the pin and both
indexes atomically. Restarts also refresh a moved mirror. Removing a host grant
or invalidating the mirror hides cached content immediately.
Tenant source management remains person-only; host default registration is the
sole registration path that does not require a person. Publication and release
authorities remain governed by the existing gates.
