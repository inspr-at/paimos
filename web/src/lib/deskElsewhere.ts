// SPDX-License-Identifier: AGPL-3.0-only
// AEON-1709. Wire parsing only; stores/deskConnections owns every sibling read.
import { api } from './api'

export interface DeskSibling { tag: string; origin: string }
export interface DeskInstances { here: string; siblings: DeskSibling[] }
export interface ElsewhereLine { id: string; kind: string; projectKey: string; title: string; askedBy: string; createdAt: string; held: boolean; url: string }
export interface ElsewhereSummary { lines: ElsewhereLine[]; open: number; held: number; upcoming: string; asOf: string }
export type ElsewhereDiscovery = 'pending' | 'known' | 'unknown'

const TAG = /^[a-z][a-z0-9-]{0,15}$/
const SUMMARY_LINES = 5, TITLE_MAX = 200, NAME_MAX = 120, READ_TIMEOUT = 8_000, SIBLING_BODY_MAX = 64_000
export const ELSEWHERE_REFRESH = 30_000
// Lines one sibling may occupy in the band. The band keeps this many slots when a read fails.
export const ELSEWHERE_LINES = 2

const text = (value: unknown, max: number) => typeof value === 'string' ? value.trim().slice(0, max) : ''
function origin(value: unknown): string {
  if (typeof value !== 'string') return ''
  try { const url = new URL(value); return url.origin === value && (url.protocol === 'https:' || url.hostname === 'localhost' || url.hostname === '127.0.0.1') ? value : '' } catch { return '' }
}

/** Deployment-owned sibling names. Discovery never issues a credential. */
export async function readInstances(signal?: AbortSignal): Promise<DeskInstances> {
  const response = await api('/decision-desk/instances', { signal })
  if (!response.ok) throw new Error('Instances could not be read.')
  const body = await response.json().catch(() => null) as { here?: unknown; siblings?: unknown } | null
  const here = text(body?.here, 16)
  const siblings = (Array.isArray(body?.siblings) ? body.siblings : []).slice(0, 4).flatMap((row: { tag?: unknown; origin?: unknown }) => {
    const tag = text(row?.tag, 16), at = origin(row?.origin)
    return TAG.test(tag) && at && tag !== here ? [{ tag, origin: at }] : []
  })
  return { here: TAG.test(here) ? here : '', siblings }
}

/** A sibling's answer, checked strictly: first lines only, links only into that sibling. */
export function parseSummary(sibling: DeskSibling, body: unknown, limit = SUMMARY_LINES): ElsewhereSummary | null {
  const raw = body as { instance?: unknown; items?: unknown; counts?: { open?: unknown; held?: unknown }; upcoming?: { label?: unknown } } | null
  if (!raw || raw.instance !== sibling.tag || !Array.isArray(raw.items)) return null
  const open = raw.counts?.open, held = raw.counts?.held
  if (!Number.isSafeInteger(open) || !Number.isSafeInteger(held) || (open as number) < 0 || (held as number) < 0 || (held as number) > (open as number)) return null
  const lines = raw.items.slice(0, Math.min(100, Math.max(1, limit))).flatMap((item: Record<string, unknown>) => {
    const id = text(item?.id, 64), title = text(item?.title, TITLE_MAX), href = typeof item?.href === 'string' ? item.href : ''
    if (!id || !title || !href.startsWith('/') || href.startsWith('//') || /[\\\r\n\t]/.test(href)) return []
    const link = new URL(href, sibling.origin)
    if (link.origin !== sibling.origin) return []
    return [{ id, kind: text(item.kind, 40), projectKey: text(item.project_key, 40), title, askedBy: text(item.asked_by, NAME_MAX), createdAt: text(item.created_at, 40), held: item.held === true, url: link.href }]
  })
  const asOf = text((raw as { as_of?: unknown }).as_of, 40)
  if (!Number.isFinite(Date.parse(asOf))) return null
  return { lines, open: open as number, held: held as number, upcoming: text(raw.upcoming?.label, TITLE_MAX), asOf }
}

// Count the bytes actually read. A missing or smaller Content-Length must not admit the rest of the body.
async function boundedJSON(response: Response, limit: number): Promise<unknown> {
  const declared = Number(response.headers.get('content-length'))
  if (Number.isFinite(declared) && declared > limit) {
    await response.body?.cancel()
    throw new Error('too much')
  }
  if (!response.body) throw new Error('empty body')
  const reader = response.body.getReader(), chunks: Uint8Array[] = []
  let size = 0
  try {
    for (;;) {
      const chunk = await reader.read()
      if (chunk.done) break
      size += chunk.value.byteLength
      if (size > limit) { await reader.cancel(); throw new Error('too much') }
      chunks.push(chunk.value)
    }
  } finally { reader.releaseLock() }
  const bytes = new Uint8Array(size)
  let offset = 0
  for (const chunk of chunks) { bytes.set(chunk, offset); offset += chunk.byteLength }
  return JSON.parse(new TextDecoder('utf-8', { fatal: true }).decode(bytes))
}

export class ConnectionEnded extends Error {}
/** This fetch bypasses api(): a sibling credential must never reach this server. */
export async function readSibling(sibling: DeskSibling, token: string, signal?: AbortSignal, limit = SUMMARY_LINES): Promise<ElsewhereSummary> {
  const timeout = AbortSignal.timeout(READ_TIMEOUT)
  const all = limit > SUMMARY_LINES
  const response = await fetch(`${sibling.origin}/api/decision-desk/owner-summary${all ? '?limit=100' : ''}`, {
    credentials: 'omit', mode: 'cors', cache: 'no-store', redirect: 'error', referrerPolicy: 'no-referrer',
    headers: { Accept: 'application/json', Authorization: `Bearer ${token}` }, signal: signal ? AbortSignal.any([signal, timeout]) : timeout,
  })
  if (response.status === 401 || response.status === 403) throw new ConnectionEnded('Connection ended')
  if (!response.ok) throw new Error(`${sibling.tag} answered ${response.status}`)
  let body: unknown
  try { body = await boundedJSON(response, all ? 256_000 : SIBLING_BODY_MAX) }
  catch (error) {
    if (error instanceof Error && error.message === 'too much') throw new Error(`${sibling.tag} answered too much`)
    throw new Error(`${sibling.tag} answered an unknown shape`)
  }
  const summary = parseSummary(sibling, body, all ? 100 : SUMMARY_LINES)
  if (!summary) throw new Error(`${sibling.tag} answered an unknown shape`)
  return summary
}
