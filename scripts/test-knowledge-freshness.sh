#!/usr/bin/env bash
set -euo pipefail

ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
TMP_ROOT=$(mktemp -d "${TMPDIR:-/tmp}/paimos-knowledge-freshness.XXXXXX")
trap 'rm -rf "$TMP_ROOT"' EXIT

fail() {
  echo "test-knowledge-freshness: $*" >&2
  exit 1
}

version=$(tr -d '[:space:]' < "$ROOT/VERSION")
schema_version=$(sed -nE 's/^const SchemaVersion = "([^"]+)"/\1/p' "$ROOT/backend/handlers/schema.go")
[[ -n "$schema_version" ]] || fail "could not read backend SchemaVersion"

cat > "$TMP_ROOT/curl" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail

url=${!#}
case "$url" in
  */api/health)
    printf '{"version":"%s"}\n' "$PAIMOS_TEST_VERSION"
    ;;
  */api/schema)
    printf '{"version":"%s"}\n' "$PAIMOS_TEST_SCHEMA"
    ;;
  *)
    echo "unexpected curl URL: $url" >&2
    exit 1
    ;;
esac
EOF
chmod +x "$TMP_ROOT/curl"

if ! output=$(
  PATH="$TMP_ROOT:$PATH" \
    PAIMOS_CHECK_LIVE=1 \
    PAIMOS_TEST_VERSION="$version" \
    PAIMOS_TEST_SCHEMA="$schema_version" \
    "$ROOT/scripts/check-knowledge-freshness.sh" 2>&1
); then
  echo "$output" >&2
  fail "live contract check rejected the repository schema version"
fi

grep -qF 'knowledge freshness: ok' <<< "$output" \
  || fail "missing success marker"

echo "test-knowledge-freshness: ok"
