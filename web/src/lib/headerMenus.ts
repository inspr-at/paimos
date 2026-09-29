// SPDX-License-Identifier: AGPL-3.0-only
// The header's gear and avatar menus (AEON-312): the pure parts, kept free of
// imports so they are tested without a browser.

// ---------- System status: /api/health and /api/ready, read when the gear opens ----------
export type SystemState = 'checking' | 'healthy' | 'degraded' | 'down'
export interface SystemProbe { health: { status?: string; db?: string } | null; ready: boolean | null }
export interface SystemSummary { state: SystemState; label: string; detail: string }
// Unreachable, a database that is down, or a server that is not ready each say so
// in words; colour only repeats it.
export function summarizeStatus(probe: SystemProbe | null): SystemSummary {
  if (!probe) return { state: 'checking', label: 'Checking…', detail: '' }
  if (!probe.health) return { state: 'down', label: 'Unreachable', detail: 'The server did not answer.' }
  if (probe.health.db && probe.health.db !== 'ok') return { state: 'degraded', label: 'Degraded', detail: 'The database is not answering.' }
  if (probe.ready === false) return { state: 'degraded', label: 'Degraded', detail: 'The server is not ready for requests.' }
  return { state: 'healthy', label: 'Operational', detail: '' }
}

// ---------- Feedback: one inbox message to the workspace owner ----------
export const FEEDBACK_MAX = 4000
// The message says what it is and where it was written; the person's name is the sender already.
export function feedbackBody(text: string, context: { path: string; version: string }): string {
  const where = [context.path && `on ${context.path}`, context.version && `version ${context.version}`].filter(Boolean).join(', ')
  return `Feedback from the app${where ? ` (${where})` : ''}\n\n${text.trim()}`
}
