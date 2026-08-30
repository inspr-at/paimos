#!/usr/bin/env bash
set -euo pipefail

ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
BACKEND="$ROOT/backend"
MODULE='github.com/inspr-at/paimos/backend'
GO_COMMAND=${GO_COMMAND:-go}
GIT_COMMAND=${GIT_COMMAND:-git}
SELECTION_SENTINEL='PAIMOS_BACKEND_SELECTION_OK_V1'
TMP_ROOT=$(mktemp -d)
trap 'rm -rf "$TMP_ROOT"' EXIT

usage() {
  echo "usage: $0 [--direct] <base> <head> | [--direct] --files-from -" >&2
  exit 2
}

DIRECT=0
if [[ "${1:-}" == '--direct' ]]; then
  DIRECT=1
  shift
fi

files=()
case "${1:-}" in
  --files-from)
    [[ "${2:-}" == "-" && $# -eq 2 ]] || usage
    while IFS= read -r file; do
      [[ -z "$file" ]] || files+=("$file")
    done
    ;;
  '')
    usage
    ;;
  *)
    [[ $# -eq 2 ]] || usage
    if ! "$GIT_COMMAND" -C "$ROOT" diff --name-only --diff-filter=ACDMRTUXB -z \
      "$1" "$2" -- backend >"$TMP_ROOT/changed-files"; then
      echo 'backend-changed-packages: git diff failed' >&2
      exit 1
    fi
    while IFS= read -r -d '' file; do
      files+=("$file")
    done <"$TMP_ROOT/changed-files"
    ;;
esac

packages=()
fallback_all=0
if [[ -n "${files[0]+present}" ]]; then
  for file in "${files[@]}"; do
    [[ "$file" == backend/* ]] || continue
    relative=${file#backend/}
    case "$relative" in
      go.mod|go.sum)
        fallback_all=1
        break
        ;;
    esac

    directory=${relative%/*}
    if [[ "$directory" == "$relative" ]]; then
      directory=.
    fi

    # A removed Go package cannot be mapped against the checked-out head. Exact
    # package resolution must succeed for Go files; walking to a parent package
    # would silently misclassify deletion of the last file in a package.
    if [[ "$relative" == *.go ]]; then
      if [[ ! -d "$BACKEND/$directory" ]] ||
         ! package=$(cd "$BACKEND" && "$GO_COMMAND" list "./$directory" 2>/dev/null); then
        fallback_all=1
        break
      fi
      packages+=("$package")
      continue
    fi

    package=
    probe=$directory
    while true; do
      if package=$(cd "$BACKEND" && "$GO_COMMAND" list "./$probe" 2>/dev/null); then
        packages+=("$package")
        break
      fi
      [[ "$probe" != "." && "$probe" == */* ]] || {
        if [[ "$probe" != "." ]]; then
          probe=.
          continue
        fi
        fallback_all=1
        break
      }
      probe=${probe%/*}
    done
    [[ "$fallback_all" -eq 0 ]] || break
  done
fi

if [[ "$fallback_all" -eq 1 ]]; then
  :
elif [[ -n "${packages[0]+present}" ]]; then
  if [[ "$DIRECT" -eq 0 ]]; then
    direct_packages=("${packages[@]}")
    # `Deps` is transitive. Reading every repository test variant once gives
    # the bounded reverse closure: packages whose production or external tests
    # exercise a directly changed package join the normal PR test set.
    if ! (cd "$BACKEND" && "$GO_COMMAND" list -test \
      -f '{{if .ForTest}}{{.ForTest}}|{{join .Deps " "}} {{join .Imports " "}}{{end}}' \
      ./...) >"$TMP_ROOT/test-dependencies"; then
      echo 'backend-changed-packages: go list -test failed' >&2
      exit 1
    fi
    while IFS='|' read -r tested dependencies; do
      [[ -n "$tested" && "$tested" == "$MODULE"* ]] || continue
      for direct in "${direct_packages[@]}"; do
        if [[ " $dependencies " == *" $direct "* ]]; then
          packages+=("$tested")
          break
        fi
      done
    done <"$TMP_ROOT/test-dependencies"
  fi
fi

# CI consumers require this explicit success record before trusting an empty
# plan. It is deliberately emitted only after every discovery command exits
# successfully, so command failure and "no affected packages" cannot collapse
# to the same byte stream.
printf '%s\n' "$SELECTION_SENTINEL"
if [[ "$fallback_all" -eq 1 ]]; then
  printf '%s\n' './...'
elif [[ -n "${packages[0]+present}" ]]; then
  printf '%s\n' "${packages[@]}" | LC_ALL=C sort -u
fi
