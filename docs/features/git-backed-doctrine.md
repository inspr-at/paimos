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
