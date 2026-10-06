#!/usr/bin/env bash
# SPDX-License-Identifier: AGPL-3.0-only
set -euo pipefail
# Build only the shared dependency output. --rebuild requires an existing
# output, so first realize it on a cold store (a no-op on a cache hit).
args=(build .#aeon.goModules -L --no-link --no-write-lock-file --cores 2 --max-jobs 1)
nix "${args[@]}"
# A cached fixed-output path must not conceal a stale vendorHash.
exec nix "${args[@]}" --rebuild
