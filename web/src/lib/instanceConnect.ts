// SPDX-License-Identifier: AGPL-3.0-only
import { api } from './api'
import { boundedJSON, type DeskSibling } from './deskElsewhere'

// The router removes the fragment before authentication, return-path handling,
// component rendering or navigation. It stays in memory for this callback only.
let callbackFragment = ''
export function holdInstanceFragment(value: string) { callbackFragment = value.length <= 2048 ? value : '' }
export function takeInstanceFragment() { const value = callbackFragment; callbackFragment = ''; return value }

export interface IssuedConnection { id: string; origin: string; instance: string; created_at: string; expires_at: string; last_used_at?: string }
export async function issuedConnections(signal?: AbortSignal): Promise<IssuedConnection[]> {
  const response = await api('/decision-desk/connections', { signal })
  if (!response.ok) throw new Error('Connections could not be read.')
  const body = await response.json()
  if (!Array.isArray(body.connections) || body.connections.length > 100) throw new Error('Connections could not be read.')
  return body.connections
}
export async function revokeIssuedConnection(id: string, signal?: AbortSignal) {
  const response = await api(`/decision-desk/connections/${encodeURIComponent(id)}`, { method: 'DELETE', signal })
  if (!response.ok) throw new Error('Connection could not be ended.')
}
export async function confirmInstance(origin: string, state: string, challenge: string, signal?: AbortSignal): Promise<string> {
  const response = await api('/decision-desk/connections', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ origin, state, code_challenge: challenge }), signal })
  if (!response.ok) throw new Error('Connection could not be completed. Please try again.')
  const body = await response.json(), target = new URL(body.redirect_url)
  if (target.origin !== origin || target.pathname !== '/instances/connected' || target.search || !target.hash || target.username || target.password) throw new Error('Connection could not be completed.')
  const fragment = new URLSearchParams(target.hash.slice(1))
  if (fragment.get('state') !== state || fragment.has('token') || !/^code_[\da-f-]{36}_[\da-f-]{36}_[A-Za-z0-9_-]{43}$/i.test(fragment.get('code') ?? '')) throw new Error('Connection could not be completed.')
  return target.href
}

export async function exchangeInstance(sibling: DeskSibling, code: string, state: string, verifier: string, signal?: AbortSignal) {
  const timeout = AbortSignal.timeout(8_000)
  const response = await fetch(`${sibling.origin}/api/decision-desk/connections/exchange`, {
    method: 'POST', credentials: 'omit', mode: 'cors', cache: 'no-store', redirect: 'error', referrerPolicy: 'no-referrer',
    headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ code, state, code_verifier: verifier }),
    signal: signal ? AbortSignal.any([signal, timeout]) : timeout,
  })
  if (!response.ok) throw new Error('Connection code could not be exchanged. Return and connect again.')
  // Never let a parser error quote any part of the credential response in UI.
  const body = await boundedJSON(response, 2048).catch(() => null) as Record<string, unknown> | null
  if (!body || typeof body !== 'object' || Array.isArray(body)) throw new Error('Connection could not be completed.')
  const token = typeof body.token === 'string' ? body.token : '', createdAt = typeof body.created_at === 'string' ? body.created_at : '', expiresAt = typeof body.expires_at === 'string' ? body.expires_at : ''
  if (body.instance !== sibling.tag || !/^desk_[\da-f-]{36}_[\da-f-]{36}_[A-Za-z0-9_-]{43}$/i.test(token) || !Number.isFinite(Date.parse(createdAt)) || !Number.isFinite(Date.parse(expiresAt)) || Date.parse(expiresAt) <= Date.now() || Date.parse(expiresAt) > Date.now() + 91 * 86400_000 || typeof body.account !== 'string' || body.account.length > 120) throw new Error('Connection could not be completed.')
  return { token, origin: sibling.origin, account: body.account, createdAt, expiresAt }
}

/** Explicit sign-out clears only the departing person's grants and pending proofs. */
export function clearPersonConnections(owner: string) {
  if (!owner) return
  if (typeof window !== 'undefined') window.dispatchEvent(new CustomEvent('aeon:desk-signout', { detail: owner }))
  try {
    const prefix = `aeon.desk-connections.${owner}.`, pending = 'aeon.desk-connections.pending.'
    const keys = Array.from({ length: localStorage.length }, (_, n) => localStorage.key(n)).filter((key): key is string => !!key)
    for (const key of keys) {
      if (key.startsWith(prefix)) localStorage.removeItem(key)
      else if (key.startsWith(pending)) {
        try { if (JSON.parse(localStorage.getItem(key) ?? 'null')?.owner === owner) localStorage.removeItem(key) } catch { /* Unreadable proofs cannot be adopted. */ }
      }
    }
  } catch { /* Storage can be unavailable. In-memory grants reset with identity. */ }
}
