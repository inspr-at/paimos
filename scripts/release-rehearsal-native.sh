#!/usr/bin/env bash
# SPDX-License-Identifier: AGPL-3.0-only
set -euo pipefail
version="$(python3 -c 'import json; print(json.load(open("version.json"))["version"])')"
test "$(dist/paimos-agentd-darwin-${AEON_DARWIN_ARCH:?} --version)" = "paimos-agentd $version ledger-v1"
GOMAXPROCS=2 go test -p 2 ./cmd/aeon-agentd -run '^TestRelease' -count=1
GOMAXPROCS=2 go test -p 2 ./internal/agentd -run '^TestAttachMacOSRefusesUnsignedImages$' -count=1
bash -n scripts/sign-notarize.sh
echo 'Unsigned native build only. Developer ID, notarization, paired Keychain and Touch ID remain post-tag human gates.' >> "$GITHUB_STEP_SUMMARY"
