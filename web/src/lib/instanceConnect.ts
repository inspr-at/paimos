// SPDX-License-Identifier: AGPL-3.0-only
import { api } from './api'

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
export async function confirmInstance(origin: string, state: string, signal?: AbortSignal): Promise<string> {
  const response = await api('/decision-desk/connections', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ origin, state }), signal })
  if (!response.ok) throw new Error('Connection could not be completed. Please try again.')
  const body = await response.json(), target = new URL(body.redirect_url)
  if (target.origin !== origin || target.pathname !== '/instances/connected' || target.search || !target.hash || target.username || target.password) throw new Error('Connection could not be completed.')
  return target.href
}
