#!/usr/bin/env bash
# SPDX-License-Identifier: MIT
# Source: markus-barta/apple-signing ci/sign-notarize.sh — vendor this file
# unchanged and note the vault commit it came from.
# Vendored from vault commit 2ed7f65.
#
# Developer ID signing + notarization for bare macOS binaries in CI.
#
#   sign-notarize.sh [--identifier ID] [--entitlements FILE] [--no-notarize] BINARY...
#
# Environment (from the consumer's protected GitHub environment):
#   APPLE_CERTIFICATE           base64 of the Developer ID Application .p12
#   APPLE_CERTIFICATE_PASSWORD  its password
#   APPLE_TEAM_ID               10-character team id the signature must carry
#   notarization, one of:
#     APPLE_API_KEY (base64 .p8), APPLE_API_KEY_ID, APPLE_API_ISSUER
#     APPLE_ID, APPLE_PASSWORD (app-specific password)
#
# Signs with hardened runtime and a secure timestamp, verifies the team id
# in the signature, notarizes all binaries in one submission and fails
# unless Apple accepts it. Bare binaries cannot be stapled: Gatekeeper
# checks the notarization ticket online on first run.
# Uses a throwaway keychain that is always deleted, even on failure.
set -euo pipefail

identifier=""
entitlements=""
notarize=1
while [ $# -gt 0 ]; do
  case "$1" in
    --identifier) identifier="$2"; shift 2 ;;
    --entitlements) entitlements="$2"; shift 2 ;;
    --no-notarize) notarize=0; shift ;;
    --) shift; break ;;
    -*) echo "unknown option $1" >&2; exit 2 ;;
    *) break ;;
  esac
done
[ $# -gt 0 ] || { echo "usage: $0 [options] BINARY..." >&2; exit 2; }

fail() { echo "::error::$*" >&2; exit 1; }
for v in APPLE_CERTIFICATE APPLE_CERTIFICATE_PASSWORD APPLE_TEAM_ID; do
  [ -n "${!v:-}" ] || fail "$v is not set (is this job in the signing environment?)"
done
if [ "$notarize" = 1 ]; then
  if [ -n "${APPLE_API_KEY:-}" ]; then
    [ -n "${APPLE_API_KEY_ID:-}" ] && [ -n "${APPLE_API_ISSUER:-}" ] ||
      fail "APPLE_API_KEY needs APPLE_API_KEY_ID and APPLE_API_ISSUER"
  else
    [ -n "${APPLE_ID:-}" ] && [ -n "${APPLE_PASSWORD:-}" ] ||
      fail "notarization needs APPLE_API_KEY… or APPLE_ID + APPLE_PASSWORD"
  fi
fi
for b in "$@"; do [ -f "$b" ] || fail "no such binary: $b"; done

work="$(mktemp -d "${RUNNER_TEMP:-${TMPDIR:-/tmp}}/sign.XXXXXX")"
keychain="$work/signing.keychain-db"
old_keychains="$(security list-keychains -d user | sed -e 's/^[[:space:]]*"//' -e 's/"$//')"
cleanup() {
  # shellcheck disable=SC2086
  security list-keychains -d user -s $old_keychains >/dev/null 2>&1 || true
  security delete-keychain "$keychain" >/dev/null 2>&1 || true
  rm -rf "$work"
}
trap cleanup EXIT

kc_pw="$(openssl rand -hex 24)"
security create-keychain -p "$kc_pw" "$keychain"
security set-keychain-settings -lut 3600 "$keychain"
security unlock-keychain -p "$kc_pw" "$keychain"

umask 077
printf '%s' "$APPLE_CERTIFICATE" | base64 --decode >"$work/cert.p12"
security import "$work/cert.p12" -k "$keychain" -f pkcs12 \
  -P "$APPLE_CERTIFICATE_PASSWORD" -T /usr/bin/codesign >/dev/null
rm -f "$work/cert.p12"
security set-key-partition-list -S apple-tool:,apple:,codesign: -s -k "$kc_pw" "$keychain" >/dev/null
# shellcheck disable=SC2086
security list-keychains -d user -s "$keychain" $old_keychains

identity="$(security find-identity -v -p codesigning "$keychain" |
  awk -v t="($APPLE_TEAM_ID)" 'index($0, "Developer ID Application") && index($0, t) { print $2; exit }')"
[ -n "$identity" ] || fail "no valid 'Developer ID Application … ($APPLE_TEAM_ID)' identity in the certificate"

# Expiry: warn 120 days ahead, fail once expired (codesign stays the gate).
end="$(security find-certificate -Z -a -p "$keychain" |
  awk -v h="$identity" '/^SHA-1 hash:/ { keep = ($3 == h) } keep' |
  openssl x509 -noout -enddate 2>/dev/null | sed 's/^notAfter=//')" || true
if [ -n "$end" ]; then
  end_s="$(date -j -f '%b %e %T %Y %Z' "$end" +%s 2>/dev/null || echo "")"
  if [ -n "$end_s" ]; then
    days=$(( (end_s - $(date +%s)) / 86400 ))
    [ "$days" -ge 0 ] || fail "Developer ID certificate expired on $end; re-issue it and update the vault"
    [ "$days" -ge 120 ] || echo "::warning::Developer ID certificate expires in $days days ($end); renew via the apple-signing vault"
  fi
fi

sign_args=(--force --options runtime --timestamp --keychain "$keychain" --sign "$identity")
[ -z "$identifier" ] || sign_args+=(--identifier "$identifier")
[ -z "$entitlements" ] || sign_args+=(--entitlements "$entitlements")

for b in "$@"; do
  echo "→ codesign $b"
  codesign "${sign_args[@]}" "$b"
  codesign --verify --strict --verbose=2 "$b"
  details="$(codesign -dvv "$b" 2>&1)"
  printf '%s\n' "$details" | grep -qx "TeamIdentifier=$APPLE_TEAM_ID" ||
    fail "$b: signature does not carry team $APPLE_TEAM_ID"
  printf '%s\n' "$details" | grep -q 'flags=.*runtime' ||
    fail "$b: hardened runtime flag missing"
done

[ "$notarize" = 1 ] || { echo "✓ signed (notarization skipped)"; exit 0; }

zip="$work/notarize.zip"
# ditto zips one source, so all binaries go into one folder first.
mkdir "$work/bundle"
cp -p "$@" "$work/bundle/"
ditto -c -k --keepParent "$work/bundle" "$zip"

auth=()
if [ -n "${APPLE_API_KEY:-}" ]; then
  printf '%s' "$APPLE_API_KEY" | base64 --decode >"$work/AuthKey.p8"
  auth=(--key "$work/AuthKey.p8" --key-id "$APPLE_API_KEY_ID" --issuer "$APPLE_API_ISSUER")
else
  auth=(--apple-id "$APPLE_ID" --password "$APPLE_PASSWORD" --team-id "$APPLE_TEAM_ID")
fi

echo "→ notarytool submit (waits for Apple)"
set +e
out="$(xcrun notarytool submit "$zip" "${auth[@]}" --wait --timeout 30m --output-format json 2>&1)"
rc=$?
set -e
id="$(printf '%s' "$out" | sed -n 's/.*"id"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' | head -1)"
status="$(printf '%s' "$out" | sed -n 's/.*"status"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' | head -1)"
echo "  submission ${id:-?}: ${status:-unknown} (exit $rc)"
if [ "$rc" != 0 ] || [ "$status" != Accepted ]; then
  printf '%s\n' "$out" | grep -v -i password >&2 || true
  [ -z "$id" ] || xcrun notarytool log "$id" "${auth[@]}" >&2 || true
  fail "notarization not accepted"
fi

# The ticket reaches Gatekeeper's online lookup within a minute or two.
for b in "$@"; do
  okn=0
  for _ in 1 2 3 4 5 6; do
    if codesign --verify --strict --test-requirement="=notarized" "$b" 2>/dev/null; then okn=1; break; fi
    sleep 20
  done
  if [ "$okn" = 1 ]; then echo "✓ $b notarized"; else echo "::warning::$b: notarization accepted, online ticket not visible yet"; fi
done
