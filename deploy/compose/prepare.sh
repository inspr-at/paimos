#!/usr/bin/env bash
# SPDX-License-Identifier: AGPL-3.0-only
# One-time preparation on the target Linux host. Never overwrites existing state.
set -euo pipefail
if [[ "$(uname -s)" != Linux || "$(id -u)" != 0 ]]; then
  echo 'Run as root on the target Linux Docker host.' >&2
  exit 1
fi
data_dir="${1:?Usage: prepare.sh /absolute/path/to/aeon-data}"
if [[ "$data_dir" != /* || "$data_dir" == / ]]; then
  echo 'Supply an absolute, dedicated data directory.' >&2
  exit 1
fi
command -v openssl >/dev/null
if [[ -e "$data_dir/secrets" || -L "$data_dir/secrets" || -e "$data_dir/files" || -L "$data_dir/files" || -L "$data_dir" ]]; then
  echo 'Existing state found; preparation refuses to overwrite it.' >&2
  exit 1
fi
umask 077
install -d -m 700 "$data_dir" "$data_dir/secrets"
install -d -m 750 -o 65532 -g 65532 "$data_dir/files"
for name in db-super db-app session messaging; do
  openssl rand -hex 32 > "$data_dir/secrets/$name"
done
# Both postgres and aeon read db-app; individual mounts bypass the root-only
# host parent directory. Session/messaging are readable by aeon alone.
chmod 444 "$data_dir/secrets/db-super" "$data_dir/secrets/db-app"
chown 65532:65532 "$data_dir/secrets/session" "$data_dir/secrets/messaging"
chmod 400 "$data_dir/secrets/session" "$data_dir/secrets/messaging"
echo 'Data directory and four persistent secret files prepared.'
