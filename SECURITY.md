# Security

Report a vulnerability through a private GitHub security advisory on this repository (`inspr-at/aeon`). Do not open a public issue, pull request, or chat message that includes exploit detail, credentials, or tenant data. After the repository is renamed to `inspr-at/paimos`, use the advisory flow on the name GitHub redirects to. Give the affected calendar coordinate, what an attacker can do, and whether you have already seen it used.

## Supported versions

The supported release is the latest published INSPR calendar coordinate (`YYMMDDhhmmss.0.0`, the `version` field of `version.json` and the `v` tag that matches it). Older coordinates are not supported. `dev` builds are not a supported release.

## Security model

People sign in with the operator's configured OpenID Connect (OIDC) identity provider. A session belongs to one tenant. Email does not choose a tenant or join one. Agents do not use that login. They authenticate with a scoped API key.

Every tenant row is isolated by Postgres row-level security on `tenant_id`. Production must use a database role that is not a superuser and does not bypass row-level security. A superuser would ignore the policies.

ADR-003 is the permission model. Authority is a permission, not a job title. Built-in roles (owner, admin, member, viewer, guest, customer) and tenant custom roles bundle those permissions. A workspace binding applies in the tenant. A project binding adds that role's project permissions on one project. Visibility is enforced in the database: a workspace role that holds `nodes.read` can see every project, and anyone else sees only projects they are bound to with that permission. Deactivated principals lose their sessions and keys.

Agent keys are deny-by-default. An empty scope list grants no API access. Each scope is an outer ceiling taken from the permission registry. A route with no agent mapping answers 403 to a key. An approval cannot grant a scope the key does not already allow.

A public quote link does not store the raw capability as a lookup key. The public-link projection keeps a SHA-256 verifier hash, the frozen offer digest, and the event ids that created or revoked the link. A retained copy of the capability is encrypted separately and is not used as the lookup value.
