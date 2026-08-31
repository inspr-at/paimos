# scripts/release/

Helpers used by the [release-v2.yml](../../.github/workflows/release-v2.yml) workflow
(PAI-99 — signed + notarized macOS CLI release).

## Apple Developer secrets

`release-v2.yml` expects these five repo secrets in `inspr-at/paimos`
(they live only in the encrypted GitHub secret store — never in the tree):

- `APPLE_CERTIFICATE`            — base64 of the .p12
- `APPLE_CERTIFICATE_PASSWORD`   — password for the .p12
- `APPLE_ID`                     — Apple Developer account email
- `APPLE_PASSWORD`               — app-specific password (notarytool auth)
- `APPLE_TEAM_ID`                — 10-char Apple team id

They were provisioned via a one-shot mirror workflow, removed after use in
PAI-687 (it previously mirrored from an org the maintainer no longer
controls). Nothing in this repo depends on that org today.

### Where these actually come from (PAI-688)

**The canonical source of truth is the Apple Developer account**, not the
`.p12`. Everything above is *derived* from it:

- the certificate is re-issuable from
  [developer.apple.com](https://developer.apple.com/account/resources/certificates)
  ("Developer ID Application") — you re-issue, re-export, re-upload — **but
  not unconditionally**: Apple caps how many Developer ID Application
  certificates an account may hold at once (historically five), and they
  **cannot be revoked from the portal**; freeing a slot means contacting
  Apple Support. So a lost `.p12` is usually an inconvenience, and in the
  worst case (every slot occupied by certificates whose private keys are
  gone) a support ticket with lead time. Do not assume same-day recovery;
- `APPLE_PASSWORD` is an *app-specific* password for `notarytool`, revocable
  and re-issuable from the Apple ID security settings;
- what genuinely cannot be recreated is **access to the Apple Developer
  account itself** (Apple ID + its 2FA recovery).

So the thing to protect is the account, and it is custodied the same way as
every other maintainer credential — see
[`docs/CONTINUITY.md`](../../docs/CONTINUITY.md) §3.2 and §"What this
document does not contain". Per that document's own rule, the vault location
is deliberately **not** named here.

### Certificate expiry

| | |
| --- | --- |
| Identity | `Developer ID Application: Markus Barta (P66J39QV6V)` |
| Expires | **2031-05-12** (intermediate "Developer ID Certification Authority G2": 2031-09-17) |

Verified from the published signature, not from the secret:

```bash
tar xzf paimos_<release-version>_darwin_universal.tar.gz
codesign -dvvv --extract-certificates=cert- ./paimos
openssl x509 -inform DER -in cert-0 -noout -subject -enddate
```

A Developer ID certificate is **not** auto-renewed. The release workflow
warns 120 days out and fails once the certificate has actually expired
(`Check signing certificate expiry` in `release-v2.yml`), which is the
renewal trigger — there is no calendar entry to forget.

The check binds to the SHA-1 of the identity that will actually sign, not
to the certificate name, so a stale same-named certificate in the
keychain cannot produce a misleading verdict. It fails **only** on a
positively-determined expiry; if the check itself cannot run it warns and
lets the release proceed, because `codesign` is the real gate.

### Re-provisioning / rotation

After re-issuing the certificate (or rotating the app-specific password):

```bash
base64 -i cert.p12 -o cert.p12.b64
gh secret set APPLE_CERTIFICATE -R inspr-at/paimos < cert.p12.b64
gh secret set APPLE_CERTIFICATE_PASSWORD -R inspr-at/paimos   # prompts
gh secret set APPLE_ID -R inspr-at/paimos                     # prompts
gh secret set APPLE_PASSWORD -R inspr-at/paimos               # prompts
gh secret set APPLE_TEAM_ID -R inspr-at/paimos                # prompts
gh secret list -R inspr-at/paimos   # confirm the 5 names
rm -f cert.p12.b64
```

Then cut a patch release and confirm `spctl -a -vv` on the artifact reports
`source=Notarized Developer ID` (see [`docs/INSTALL.md`](../../docs/INSTALL.md)).

Long-term the intent is to move custody into Janus; that is deferred and
blocked — see PAI-688 Phase 2 and JANUS-420/421/422.
