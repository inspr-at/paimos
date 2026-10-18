# Feature flags

Shipped rollout flags start **OFF**. A person with `settings.manage` can open
**Settings → Workspace → Feature flags** and choose On, Off or Inherit for the
workspace or one project. A project override wins over the workspace override;
without either, the feature stays off. Resetting an override retains its revision.
The first flag, `workspace-summary`, shows project and work counts in Workspace
settings. Enable or disable it on the same page without rebuilding, restarting
or deploying. Selecting a project in Feature flags shows its work summary only
when that project's effective flag is on, including an explicit project On
while the workspace is Off. Other open pages re-evaluate on focus and navigation.
Summaries render below the flag controls. Scope reloads keep the rows in place
and disable them until the selected scope's settings arrive.

`GET /api/features?project_id=<uuid>` reads committed flags for the caller's
tenant and visible project; omit the query for the workspace baseline. It
requires `nodes.read`. Admin reads use `GET /api/settings/features`; writes use
`PUT /api/settings/features/{key}` with `enabled` (true, false or null) and
`expected_revision` from the read (zero for a new scope). Optional `project_id`
selects the override scope. Changed writes atomically append `feature.updated`
events with actor and before/after state; identical writes are idempotent and
stale writes return 409. Agent keys cannot change rollout choices.

Web code uses the typed `canFeature(key, projectId?)` helper in
`web/src/lib/features.ts` alongside its existing `can(permission, projectId?)`
check. Unread scopes, failed, unshipped and revoked answers are off. Rechecks
retain the last valid answer until its replacement arrives; failures and
session resets discard it. Server code uses
`features.Service.Enabled`, which reads the database each time and returns
false on error. **Flags never grant permissions or bypass approval gates.**
