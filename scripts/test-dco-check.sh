#!/usr/bin/env bash

set -euo pipefail

root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
fixture=$(mktemp -d -t paimos-dco-test-XXXXXX)
trap 'rm -rf -- "$fixture"' EXIT

git -C "$fixture" init -q
git -C "$fixture" config user.name 'DCO Fixture'
git -C "$fixture" config user.email 'dco-fixture@example.test'

touch "$fixture/fixture"
git -C "$fixture" add fixture
git -C "$fixture" commit -q --signoff -m 'test: establish base'
base=$(git -C "$fixture" rev-parse HEAD)

printf 'signed\n' >> "$fixture/fixture"
git -C "$fixture" commit -q --signoff -am 'test: signed commit'
signed_head=$(git -C "$fixture" rev-parse HEAD)
git -C "$fixture" -c advice.detachedHead=false checkout -q --detach "$base"

printf 'unsigned\n' >> "$fixture/fixture"
git -C "$fixture" commit -q -am 'test: unsigned commit'
unsigned_head=$(git -C "$fixture" rev-parse HEAD)

(cd "$fixture" && "$root/scripts/check-dco.sh" "$base" "$signed_head" >/dev/null)
if (cd "$fixture" && "$root/scripts/check-dco.sh" "$base" "$unsigned_head" >/dev/null 2>&1); then
  echo 'DCO self-test: unsigned commit was incorrectly accepted' >&2
  exit 1
fi

echo 'DCO self-test: passed'
