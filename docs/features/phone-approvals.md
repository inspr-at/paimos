# Phone approvals

In **Settings → Personal → Phone approvals**, add a device passkey and explicitly
enable notifications on that device. On iPhone and iPad, first add the site to
the Home Screen and open it there ([WebKit's Web Push requirements](https://webkit.org/blog/13878/web-push-for-web-apps-on-ios-and-ipados/)).
The notification contains only a review link. Open it, sign in, review the live
request and approve or decline with fresh WebAuthn user verification. A device
may use Face ID, Touch ID or its passkey verification method; the app does not
choose a biometric or receive biometric data.

The server requires `AEON_PUBLIC_URL` to be its stable HTTPS origin (localhost
HTTP is supported for development). Push is disabled until an operator
provisions `AEON_PHONE_PUSH_VAPID_FILE` in the existing host secret storage. That
JSON file contains `public_key`, `private_key` (a matching base64url P-256 VAPID
pair) and `subject` (a `mailto:` or HTTPS contact). Keep the existing
`AEON_LINK_KEY_FILE` stable (or its existing `AEON_MESSAGING_KEY_FILE` fallback):
its derived vault key encrypts stored push
subscriptions. This feature never generates or publishes host VAPID secrets.
Delivery supports Apple, Mozilla and Google push endpoints, refuses redirects
and sends a generic notification without request details or approval proofs.

Settings, passkeys and subscriptions belong to the signed-in person and tenant.
Notifications start disabled; registration and browser subscription happen only
after explicit actions. The service worker handles push and review navigation;
it does not intercept requests or cache authenticated pages. Subscription
endpoints and browser keys are encrypted at rest and omitted from reads and
audit events. Settings expose only revocable device/passkey IDs. Pausing stops
notifications; revoking the last passkey also pauses delivery and invalidates
outstanding verification challenges. Browser notification permission can be
withdrawn in device settings. HTTP 404/410 responses revoke dead subscriptions.
The browser's push provider processes the subscription and encrypted delivery.

Quiet hours use the configured time zone; equal start/end disables quiet hours.
The first eligible person receives the request, and timed escalation adds the
next authorized person in stable person-ID order, measured from request creation.
It never expands approval authority; attach requests remain exclusive to their
paired computer owner. Existing gate, risk, resource and permission checks run
again when deciding. A watch requiring local Mac confirmation still needs it.
The two-minute, one-use challenge binds the request ID and content hash, person,
tenant, decision and reason. Verification, decision, permission grant and audit
commit together; changed or ended requests, revoked credentials and replayed
proofs are rejected. Writes are limited to 30 attempts per person per ten minutes,
with bounded notification batches and delivery retries. Approval and credential
events remain in the tenant event log under its existing retention policy.

## Decision Desk browser notices

The server adapter requires `phoneMod.DeskPush = decisiondesk.PushSource{}`
after constructing the phone module and before starting its worker in
`cmd/aeon/serve.go`. This assignment keeps the existing dependency graph acyclic.

Desk notices reuse the configured VAPID keypair, encrypted subscription vault,
push-provider allowlist and service worker. Each browser subscription has an
independent `desk_enabled` flag, off by default. A phone passkey is not required:
a notice only opens an authenticated Desk item. Phone-approval preferences,
quiet hours and escalation do not apply to Desk notices. Existing phone-approval
dispatch continues to require its preferences and passkey.

Only the intended person's opted-in devices receive a notice. Held messages use
their recipient; linked questions retain that routing. Other questions and
approvals use the project's human owner (the project lead owner where present,
then the existing person-step owner resolver), with current read and decision
permissions checked again. Eligible sources follow the Desk projection's held
work and near-expiry rules; ordinary non-held questions do not trigger a notice.
The worker listens for `question.asked` and recovers missed hints and computed
held moments on its regular scan.

An individual notice reads “PAIMOS · Decision waiting” and contains only the
first question line and project key, bounded to 240 and 80 UTF-8 bytes. Context,
findings, attachments, comments and answers are excluded. The encrypted notice
uses a stable item tag and opens `/decision-desk?item=q:<id>` (or the existing
`a:`/`m:` source prefix). A click navigates and focuses an existing same-origin
tab, or opens one. It never decides anything.

The sender waits until the first source's 60-second creation window closes.
Three or more sources in that window produce one “N decisions waiting” summary
per device, tagged `desk-summary`, opening the Desk. Remaining items carry an
explicit summary timestamp on their durable claims. Claims prevent repeat sends
across replicas and repeated events; failed or ambiguous transport is recorded as
failed and is not retried. HTTP 404/410 uses the existing subscription revocation
and audit path. More than 1000 pending source candidates produces an explicit
coverage error rather than a truncated summary.

The follow-up Settings slice can use the existing authenticated endpoints:

- `GET /api/me/phone-approvals` returns `push_available`, `vapid_public_key` and
  up to eight active `subscriptions`, each with `id`, `created_at`,
  `endpoint_hash` and `desk_enabled`. Endpoints and browser keys are omitted.
- `POST /api/me/phone-approvals/subscriptions` with
  `{"endpoint":"https://<allowlisted-provider>/…","keys":{"auth":"…","p256dh":"…"},"desk_enabled":true}`
  creates or refreshes this browser subscription and enables Desk notices.
  Send the same browser subscription with `desk_enabled:false` to disable Desk
  notices while retaining it for phone approvals. Both explicit flag values work
  without a passkey. Omitting the flag retains the existing phone registration
  requirement and preserves an existing Desk flag. The response includes `id`,
  `created_at` and `desk_enabled`; the existing eight-device cap applies.
- `DELETE /api/me/phone-approvals/subscriptions/{subscriptionId}` removes an owned
  device from both channels (204). Writes require a same-origin person session.

This server slice does not add Settings controls, a permission prompt, the Desk
offer line or the iOS installation hint. Browser permission remains a deliberate
user action implemented by the follow-up Settings slice.
