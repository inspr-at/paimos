#!/bin/sh
# SPDX-License-Identifier: AGPL-3.0-only
# Executed only for a fresh Postgres volume, as the database superuser.
set -eu
IFS= read -r app_password < /run/secrets/db-app
psql -v ON_ERROR_STOP=1 -U postgres -d postgres -v pw="$app_password" <<'SQL'
CREATE ROLE aeon LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOBYPASSRLS PASSWORD :'pw';
CREATE DATABASE aeon OWNER aeon;
SQL
psql -v ON_ERROR_STOP=1 -U postgres -d aeon -c 'CREATE EXTENSION IF NOT EXISTS vector' >/dev/null
unset app_password
