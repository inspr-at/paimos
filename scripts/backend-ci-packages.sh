#!/usr/bin/env bash
set -euo pipefail

ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
SELECTOR_COMMAND=${BACKEND_SELECTOR_COMMAND:-"$ROOT/scripts/backend-changed-packages.sh"}
SELECTION_SENTINEL='PAIMOS_BACKEND_SELECTION_OK_V1'

# Command substitution propagates the selector's exit status. The sentinel is
# a second, independent fail-closed check: a broken/mocked selector cannot turn
# an incomplete discovery into a trusted empty package list.
selection=$("$SELECTOR_COMMAND" "$@")
first=${selection%%$'\n'*}
[[ "$first" == "$SELECTION_SENTINEL" ]] || {
  echo 'backend-ci-packages: selector output omitted success sentinel' >&2
  exit 1
}

if [[ "$selection" == *$'\n'* ]]; then
  packages=${selection#*$'\n'}
  [[ -z "$packages" ]] || printf '%s\n' "$packages"
fi
