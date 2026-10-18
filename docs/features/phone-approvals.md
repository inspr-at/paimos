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
