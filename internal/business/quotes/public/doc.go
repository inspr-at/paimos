// SPDX-License-Identifier: AGPL-3.0-only

// Package public serves quote capability links. New and NewWithStore expose
// httpapi.Module values for the coordinator to mount. Its Postgres-backed
// sliding rate limiter shares state across replicas under tenant RLS.
package public
