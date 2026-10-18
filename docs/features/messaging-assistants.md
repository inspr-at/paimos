<!-- SPDX-License-Identifier: AGPL-3.0-only -->

# Messaging assistants

An assistant has a principal inbox over HTTPS and exactly `inbox.read`, `inbox.send` and `inbox.receipt`. Its persistent `messaging_only` marker refuses every route outside the principal inbox, including agent creation, keys, scope editing, project messaging, status help and identity discovery. It has no workspace or project role binding. Broader keys or later role assignments cannot lift this restriction.

Provision with a person who currently holds `agents.create` and `keys.manage`, or a person-owned coordinator/full-access key with live workspace coordinator grants. The owner person must currently hold both permissions. These checks run again inside the mutation transaction, serialized with permission changes. Ordinary agent and key creation retain their existing person/workstation restrictions.

```sh
aeon agents assistant create --name Amy --expires 30d --key-out assistant-token
aeon agents assistant rotate <principal-uuid> --expires 7d --key-out replacement-token
aeon agents assistant revoke <principal-uuid>
```

Expiry is required, positive and at most 30 days. There may be three assistants with active keys per person. Expired assistants may be renewed through `rotate` if the quota permits. Names are unique in the tenant, ignoring case. Rotation revokes all previous keys immediately, within the permitted maximum 24-hour overlap. Revocation deactivates the assistant and revokes all its keys. Renewal and revocation require the same live owner-bound predicate and refuse assistants owned by another person.

`--key-out` creates a new file exclusively with mode 0600; existing files and symlinks are refused before the request. `--key-out -` writes only the secret plus a newline to non-terminal stdout for piping into an encryptor. Metadata goes to stderr in pipe mode. JSON metadata and errors omit the token. The server returns the secret once with `Cache-Control: no-store` and persists only its SHA-256 hash. There is no sealed-delivery feature yet.

The existing **Agents → Agent keys** surface (Settings → Access → Agents) shows each assistant and its key scopes and expiry; its existing revoke control remains available. No new UI or layout is introduced. The assistant can use `aeon tell <principal-uuid> --inbox -m …`, `aeon listen --inbox`, and receipts without project or identity discovery.

Each lifecycle transaction writes a System FYI message to the owner through the existing inbox notice/wake path and appends the workspace `messaging_assistant.provisioned`, `.rotated` or `.revoked` audit event last. The event records caller, owner, principal, key metadata and expiry; the notice requires no action. Neither contains the secret. Failure to persist the notice or audit rolls back the principal/key changes.

Security evidence is registered as ESSENTIAL: `TestMessagingAssistantProvisioningBounds`, `TestMessagingAssistantLiveOwnerAndCallerBounds`, `TestMessagingAssistantQuotaSerializes`, `TestMessagingAssistantDeniesEveryNonInboxRoute`, `TestMessagingAssistantProvisionedThroughProductionAuth`, `TestMessagingAssistantCLISecretOutput` and `TestMessagingAssistantFinalWriteRechecksOwnerRights`. Quota contention uses a database query barrier and observes the competing tenant-lock wait. Expiry boundaries use an injected clock.
