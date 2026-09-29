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
  CGO_ENABLED=1 GOOS=darwin GOARCH="$arch" go build -trimpath -ldflags "$ldflags" -o "$out" ./cmd/aeon-agentd
  require_buildinfo "$out" darwin "$arch" 1
  if ! otool -L "$out" | grep -F -q 'LocalAuthentication.framework'; then
    echo "LocalAuthentication.framework not linked" >&2
    exit 1
  fi
  # TODO(AEON-285): Developer ID-sign this binary and enable the hardened runtime
  # before publish. Unsigned and ad-hoc builds fail Mac confirmation closed.
  # Signing waits for Markus; do not add secrets or codesign steps here.
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
  for pair in darwin/arm64 darwin/amd64 linux/amd64 linux/arm64; do
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
  done
}

case "$mode" in
  darwin-agentd) darwin_agentd ;;
  linux-agentd) linux_agentd ;;
  cli) cli_bins ;;
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
