# HTML attachment previews

HTML uploaded with `aeon attach` or the attachment picker remains an original
download on the app origin, with a script-denying CSP. HTML fragments named
`.html`/`.htm` and UTF-8 HTML documents also receive a **text thumbnail**: a
320×200 PNG of inert text, explicitly not a browser screenshot. The bounded
rendition reads at most 64 KiB/4096 tokens with a 250 ms deadline; it has no
network, browser, profile or process access and never executes CSS or scripts.
Original blobs and PNGs stay in the existing tenant/digest-namespaced file store.

Live previews are off by default. Set `AEON_PUBLIC_URL` and
`AEON_HTML_SANDBOX_ORIGIN` to exact HTTPS origins without paths or ports, on
**different registrable domains**. Missing/unsafe configuration or HTML larger
than 2 MiB leaves the download available and disables execution. The sandbox
host must be dedicated, freshly provisioned without existing service workers,
cookies, authentication, redirects or other application routes. Route both
hosts to the same Aeon instance, preserving the exact Host header; when enabled,
other hosts receive 421. Provision TLS and ingress in the owning deployment
repository. Disable access/request-body/URL logging, analytics, CDN caching,
authentication injection and response-header rewriting for the sandbox host.
Aeon refuses sandbox requests carrying cookies, Authorization or Service-Worker
headers and never runs its app/auth middleware there. Do not enable the setting
until the hostname/TLS/cookie/logging boundary has been verified by the operator.

An authenticated person requests a preview with
`POST /api/attachments/{id}/preview`. The no-store response carries availability
and, when enabled, a one-minute view-only capability. Never log, persist or share
that URL. It is bound to the login session, tenant, project, node, attachment,
digest and attachment revision. Each use rechecks live session, permissions,
visibility and immutable bytes. Logout, expiry, deletion, moves, edits or a
process restart invalidate it; load balancing to another replica fails closed.
At most 32 live grants per person and 4096 per process are retained. No new table,
API key, long-lived signing secret, browser storage or background browser worker
is introduced. The lightbox drops its iframe at expiry and when closed; already
delivered bytes in a separately opened tab cannot be recalled.

The lightbox uses exactly `sandbox="allow-scripts"` and
`referrerpolicy="no-referrer"`; the response independently enforces the sandbox
for a direct/new-tab view. Only inline scripts/styles and data images/fonts are
supported. Fetch/WebSocket, remote resources, workers, child frames, forms,
popups, downloads and same-origin access are denied. No frame message is acted
on by the parent, and frame content cannot resize the lightbox controls.
**Residual risk:** HTML can navigate its own browsing context (especially a
direct tab), potentially sending document content or its short-lived capability
in a destination URL. CSP/sandbox is not a complete network firewall. Use only
self-contained fragments, and never describe this feature as preventing all
exfiltration. See the [iframe sandbox flags](https://developer.mozilla.org/en-US/docs/Web/HTML/Reference/Elements/iframe#sandbox)
and [CSP sandbox](https://developer.mozilla.org/en-US/docs/Web/HTTP/Reference/Headers/Content-Security-Policy/sandbox).
