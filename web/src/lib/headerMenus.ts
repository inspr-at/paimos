// SPDX-License-Identifier: AGPL-3.0-only
// The header's gear and avatar menus (AEON-312): the pure parts, kept free of
// imports so they are tested without a browser.

// ---------- System status: /api/health and /api/ready, read when the gear opens ----------
export type SystemState = 'checking' | 'healthy' | 'degraded' | 'down'
export interface SystemProbe { health: { status?: string; db?: string } | null; ready: boolean | null }
export interface SystemSummary { state: SystemState; label: string; detail: string }
// Operational only once the server is up, its database answers and readiness is
// confirmed; anything unconfirmed says so in words (colour only repeats it).
export function summarizeStatus(probe: SystemProbe | null): SystemSummary {
  if (!probe) return { state: 'checking', label: 'Checking…', detail: '' }
  if (!probe.health) return { state: 'down', label: 'Unreachable', detail: 'The server did not answer.' }
  if (probe.health.db !== 'ok' || probe.health.status !== 'ok') return { state: 'degraded', label: 'Degraded', detail: 'The database is not answering.' }
  if (probe.ready === null) return { state: 'degraded', label: 'Unavailable', detail: 'Readiness could not be checked.' }
  if (!probe.ready) return { state: 'degraded', label: 'Degraded', detail: 'The server is not ready for requests.' }
  return { state: 'healthy', label: 'Operational', detail: '' }
}

// ---------- Agent inbox hooks: messages reach an agent at every turn ----------
export const HOOK_COMMANDS = [
  { harness: 'Claude Code', command: 'aeon hook install --harness claude --scope user' },
  { harness: 'Codex', command: 'aeon hook install --harness codex --scope user' },
] as const
export const HOOK_DOCS_ANCHOR = 'operator-installed-turn-boundary-hooks-aeon-281'
export function hookDocsUrl(repository: string | null | undefined): string {
  const repo = repository && /^[\w.-]+\/[\w.-]+$/.test(repository) ? repository : 'inspr-at/paimos'
  return `https://github.com/${repo}/blob/main/docs/AGENT_INTEGRATION.md#${HOOK_DOCS_ANCHOR}`
}

// ---------- Feedback: one inbox message to the workspace owner ----------
export const FEEDBACK_MAX = 4000
// The message says what it is and where it was written; the person's name is the sender already.
export function feedbackBody(text: string, context: { path: string; version: string }): string {
  const where = [context.path && `on ${context.path}`, context.version && `version ${context.version}`].filter(Boolean).join(', ')
  return `Feedback from the app${where ? ` (${where})` : ''}\n\n${text.trim()}`
}
