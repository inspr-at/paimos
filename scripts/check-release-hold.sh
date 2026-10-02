#!/usr/bin/env bash
# SPDX-License-Identifier: AGPL-3.0-only
# Portable activation adapter for NIX-597. Read-only; never deploys anything.
set +x
set -euo pipefail

# The rollout service supplies only the dedicated Variables-read token.
# Keep it out of command arguments and never print a response body.
aeon_hold_token="${AEON_RELEASE_HOLD_READ_TOKEN:-}"
if [[ ! "$aeon_hold_token" =~ ^[A-Za-z0-9_.-]+$ ]]; then
  echo 'Release hold check refused: reader credential missing or invalid' >&2
  exit 1
fi
if ! aeon_hold_response="$(printf 'header = "Authorization: Bearer %s"\n' "$aeon_hold_token" |
  curl --silent --fail --proto '=https' --connect-timeout 5 --max-time 15 \
    --max-filesize 2097152 --config - \
    --header 'Accept: application/vnd.github+json' \
    --header 'X-GitHub-Api-Version: 2022-11-28' \
    'https://api.github.com/repos/inspr-at/paimos/actions/variables?per_page=30&page=1')"; then
  echo 'Release hold check refused: API unavailable' >&2
  exit 1
fi
if ! aeon_hold_value="$(printf '%s' "$aeon_hold_response" | jq -er '
  if (.total_count | type) != "number" or .total_count < 0 or .total_count > 30 or
     (.variables | type) != "array" or (.variables | length) != .total_count then error("incomplete") else
    if all(.variables[]; type == "object" and (.name | type) == "string" and
      (.name | length) > 0 and (.value | type) == "string") | not then error("malformed") else
    [.variables[] | select(.name == "RELEASE_HOLD")] as $holds |
    if ($holds | length) > 1 then error("ambiguous") else
      (if ($holds | length) == 0 then "" else $holds[0].value end) as $value |
      if ($value | type) != "string" or ($value != "" and ($value | test("^[a-f0-9]{40}$") | not))
      then error("malformed") else "hold=" + $value end
    end
    end
  end' 2>/dev/null)"; then
  echo 'Release hold check refused: incomplete or malformed observation' >&2
  exit 1
fi
if [[ "$aeon_hold_value" != hold= ]]; then
  echo 'Release hold engaged: activation refused' >&2
  exit 1
fi
echo 'Release hold clear'
