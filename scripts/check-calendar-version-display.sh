#!/usr/bin/env bash
# PAI-1025: verify the complete offline INSPR renderer/config/license bundle.
# The legacy doctrine checker remains available for its existing consumers.
set -euo pipefail
repo_root=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)
cd "$repo_root"
node scripts/verify-calendar-version-bundle.mjs
exec node --test scripts/verify-calendar-version-bundle.test.mjs
