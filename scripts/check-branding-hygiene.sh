#!/usr/bin/env bash
set -euo pipefail

ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
cd "$ROOT"

fail=0
tracked_paths=(backend frontend data docs scripts .github)
legacy_colors=(
  '#2e''6da4'
  '#1f''4d75'
  '#4a''8fc2'
  '#dc''e9f4'
  '#1a''2d42'
  '#c8''d5e2'
  '#24''3650'
)
legacy_rgb=(
  '46[[:space:]]*,[[:space:]]*109[[:space:]]*,[[:space:]]*164'
  '31[[:space:]]*,[[:space:]]*77[[:space:]]*,[[:space:]]*117'
  '74[[:space:]]*,[[:space:]]*143[[:space:]]*,[[:space:]]*194'
  '220[[:space:]]*,[[:space:]]*233[[:space:]]*,[[:space:]]*244'
  '26[[:space:]]*,[[:space:]]*45[[:space:]]*,[[:space:]]*66'
  '200[[:space:]]*,[[:space:]]*213[[:space:]]*,[[:space:]]*226'
  '36[[:space:]]*,[[:space:]]*54[[:space:]]*,[[:space:]]*80'
)
retired_name='P''MO'

for color in "${legacy_colors[@]}"; do
  matches=$(git grep -n -I -i -F "$color" -- "${tracked_paths[@]}" \
    ':(exclude)docs/CHANGELOG.md' ':(exclude)scripts/check-branding-hygiene.sh' || true)
  if [[ -n "$matches" ]]; then
    echo "branding hygiene: retired palette color remains: $color" >&2
    echo "$matches" >&2
    fail=1
  fi
done

for rgb in "${legacy_rgb[@]}"; do
  matches=$(git grep -n -I -i -E "$rgb" -- "${tracked_paths[@]}" \
    ':(exclude)docs/CHANGELOG.md' ':(exclude)scripts/check-branding-hygiene.sh' || true)
  if [[ -n "$matches" ]]; then
    echo "branding hygiene: retired palette RGB value remains: $rgb" >&2
    echo "$matches" >&2
    fail=1
  fi
done

matches=$(git grep -n -I -i -w "$retired_name" -- "${tracked_paths[@]}" \
  ':(exclude)docs/CHANGELOG.md' ':(exclude)scripts/check-branding-hygiene.sh' || true)
if [[ -n "$matches" ]]; then
  echo "branding hygiene: retired product name remains" >&2
  echo "$matches" >&2
  fail=1
fi

if [[ $fail -ne 0 ]]; then
  exit 1
fi

echo "branding hygiene: ok"
