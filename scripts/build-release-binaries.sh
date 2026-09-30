#!/usr/bin/env bash
# SPDX-License-Identifier: AGPL-3.0-only
# Release binaries for paimos-agentd and aeon-cli.
# Darwin paimos-agentd links LocalAuthentication and must be built on that Mac.
# Linux paimos-agentd and every aeon-cli target stay CGO_ENABLED=0.
set -euo pipefail
cd "$(dirname "$0")/.."
mode="${1:-host}"
version="$(python3 -c 'import json; print(json.load(open("version.json"))["version"])')"
if [ -n "${VERSION:-}" ] && [ "$VERSION" != "$version" ]; then
  echo "version.json ${version} does not match VERSION ${VERSION}" >&2
  exit 1
fi
ldflags="-X github.com/inspr-at/paimos/internal/version.Version=${version}"
# Developer ID team the darwin daemon must be signed by for Mac confirmation
# (internal/agentd.expectedTeamID). Not secret. The Release workflow signs with
# this team; unsigned or differently signed builds still fail closed.
team="${AEON_DEVELOPER_ID_TEAM:-P66J39QV6V}"
if ! printf '%s' "$team" | grep -Eq '^[A-Z0-9]{10}$'; then
  echo "AEON_DEVELOPER_ID_TEAM must be a 10-character Apple team id" >&2
  exit 1
fi
team_ldflag="-X github.com/inspr-at/paimos/internal/agentd.expectedTeamID=${team}"
mkdir -p dist

require_buildinfo() {
  local bin="$1" os="$2" arch="$3" cgo="$4" info
  info="$(go version -m "$bin")"
  if ! printf '%s\n' "$info" | grep -F -q "CGO_ENABLED=${cgo}"; then
    echo "$bin missing CGO_ENABLED=${cgo}" >&2
    exit 1
  fi
  if ! printf '%s\n' "$info" | grep -F -q "GOOS=${os}"; then
    echo "$bin missing GOOS=${os}" >&2
    exit 1
  fi
  if ! printf '%s\n' "$info" | grep -F -q "GOARCH=${arch}"; then
    echo "$bin missing GOARCH=${arch}" >&2
    exit 1
  fi
}

# -trimpath drops -ldflags from the build info, so look for the -X value itself.
require_team() {
  if ! LC_ALL=C grep -a -F -q "$team" "$1"; then
    echo "$1 missing expected Developer ID team ${team}" >&2
    exit 1
  fi
}

darwin_agentd() {
  if [ "$(uname -s)" != "Darwin" ]; then
    echo "darwin CGO build must run on Darwin" >&2
    exit 1
  fi
  local arch host out
  arch="${AEON_DARWIN_ARCH:?set AEON_DARWIN_ARCH to arm64 or amd64}"
  case "$arch" in
    arm64|amd64) ;;
    *) echo "AEON_DARWIN_ARCH must be arm64 or amd64" >&2; exit 1 ;;
  esac
  host="$(go env GOARCH)"
  if [ "$host" != "$arch" ]; then
    echo "darwin CGO build must run on ${arch}; host is ${host}" >&2
    exit 1
  fi
  out="dist/paimos-agentd-darwin-${arch}"
  CGO_ENABLED=1 GOOS=darwin GOARCH="$arch" go build -trimpath -ldflags "$ldflags $team_ldflag" -o "$out" ./cmd/aeon-agentd
  require_buildinfo "$out" darwin "$arch" 1
  require_team "$out"
  if ! otool -L "$out" | grep -F -q 'LocalAuthentication.framework'; then
    echo "LocalAuthentication.framework not linked" >&2
    exit 1
  fi
  # Signing happens in the Release workflow (agentd-darwin job, "Sign and
  # notarize paimos-agentd", scripts/sign-notarize.sh) with secrets from the
  # release-signing environment. Local builds stay unsigned and fail closed.
}

linux_agentd() {
  local arch out
  for arch in arm64 amd64; do
    out="dist/paimos-agentd-linux-${arch}"
    CGO_ENABLED=0 GOOS=linux GOARCH="$arch" go build -trimpath -ldflags "$ldflags" -o "$out" ./cmd/aeon-agentd
    require_buildinfo "$out" linux "$arch" 0
    if ! file "$out" | grep -F -q 'statically linked'; then
      echo "linux agentd is not statically linked" >&2
      exit 1
    fi
  done
}

cli_bins() {
  local pair os arch out
  for pair in ${1:-darwin/arm64 darwin/amd64 linux/amd64 linux/arm64}; do
    os="${pair%/*}"
    arch="${pair#*/}"
    out="dist/aeon-cli-${os}-${arch}"
    CGO_ENABLED=0 GOOS="$os" GOARCH="$arch" go build -trimpath -ldflags "$ldflags" -o "$out" ./cmd/aeon
    require_buildinfo "$out" "$os" "$arch" 0
  done
}

verify_darwin() {
  local arch bin
  for arch in arm64 amd64; do
    bin="dist/paimos-agentd-darwin-${arch}"
    if [ ! -f "$bin" ]; then
      echo "missing ${bin}" >&2
      exit 1
    fi
    require_buildinfo "$bin" darwin "$arch" 1
    require_team "$bin"
  done
}

case "$mode" in
  darwin-agentd) darwin_agentd ;;
  linux-agentd) linux_agentd ;;
  cli) cli_bins ;;
  cli-linux) cli_bins "linux/amd64 linux/arm64" ;;
  cli-darwin) cli_bins "darwin/${AEON_DARWIN_ARCH:?set AEON_DARWIN_ARCH}" ;;
  verify-darwin) verify_darwin ;;
  host)
    if [ "$(uname -s)" = "Darwin" ]; then
      AEON_DARWIN_ARCH="$(go env GOARCH)"
      darwin_agentd
    fi
    linux_agentd
    ;;
  *)
    echo "unknown mode ${mode}" >&2
    exit 1
    ;;
esac
