# Git-backed doctrine (AEON-318)

`GET /api/rules/doctrine` accepts people and agent keys with `rules.read`.
Source management remains person-only (`settings.manage`). GitHub reads use
only `https://api.github.com`; redirects are refused, including repository
renames, so configure the current repository name.

The operator provisions private read-only tokens under the absolute directory
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
