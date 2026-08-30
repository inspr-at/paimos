#!/usr/bin/env bash
set -euo pipefail

echo 'backend-gate fixture: deliberate command failure' >&2
exit 42
