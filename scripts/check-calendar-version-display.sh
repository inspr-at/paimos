#!/usr/bin/env bash
# The calendar v2 display design is bundled at build time from an in-repo copy
# of the doctrine data file (PAI-994, INSPR-414). CI does not check out the
# doctrine submodule, so the copy is what the frontend embeds. The vendored
# inspr-modules v0.10.0 checker verifies the immutable published pin offline and,
# when doctrine is initialized, also verifies its HEAD and source bytes.
set -euo pipefail
repo_root=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)
cd "$repo_root"
exec ./scripts/check-calendar-version-display-pin.sh \
  frontend/src/brand/calendar-version-display.json \
  863 \
  2fdc8b4f6fcaf71cf3a7c8333e63c0f61bae32ebb8bd334eef0e3f67c59725e0 \
  a45fe06250ff5ca3ae5b31d26b4cd16c982ce402
