/*
 * PAIMOS — Your Professional & Personal AI Project OS
 * Copyright (C) 2026 Markus Barta <markus@barta.com>
 * AGPL-3.0-only — see LICENSE.
 */

// Backend cursor tokens are raw-url base64 for exactly 158 opaque bytes.
// The restricted final alphabet rejects non-canonical trailing-bit aliases
// that decode to the same bytes but fail the backend's strict re-encode check.
const AGENT_MODE_CURSOR_PATTERN = /^[A-Za-z0-9_-]{210}[AEIMQUYcgkosw048]$/

export function isCanonicalAgentModeCursor(value: unknown): value is string {
  return typeof value === 'string' && AGENT_MODE_CURSOR_PATTERN.test(value)
}
