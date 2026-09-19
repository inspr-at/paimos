#!/usr/bin/env bash
# Compatibility entry point for release tooling and local checks.
set -euo pipefail
root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
exec python3 "$root/scripts/check-dco.py" "${1:-}" "${2:-HEAD}"
