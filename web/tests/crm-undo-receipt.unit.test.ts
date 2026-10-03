// SPDX-License-Identifier: AGPL-3.0-only
import { afterEach, expect, it, vi } from 'vitest'
import * as API from '../src/lib/api'
import * as Toast from '../src/lib/toast'
import { sourceModule } from './record-source'
const CRM = sourceModule<typeof import('../src/lib/crm')>('lib/crm.ts', { './api.ts': API, './toast.ts': Toast })

afterEach(() => { vi.unstubAllGlobals(); Toast.resetToasts() })
it('S9-002: a save receipt keeps event 10 when event 11 arrives before Undo', async () => {
  const paths: string[] = []
  vi.stubGlobal('fetch', vi.fn(async (input: unknown, init?: RequestInit) => {
    const path = String(input); paths.push(path)
    if (init?.method === 'PATCH') return new Response(JSON.stringify({ id: 'A', revision: 2 }), { headers: { 'X-Aeon-Event-Ids': '10' } })
    if (path.includes('/events?')) return new Response(JSON.stringify({ items: [{ id: 11, type: 'crm.customer_updated', undo_of: null }] }))
    return new Response('{}')
  }))
  const saved = await CRM.updateCustomer('A', CRM.blankCustomer('A'), 1)
  // Before the fix this is the actual toast's click-time lookup. No replacement
  // implementation: after the fix the same call is supplied the exact receipt.
  if (CRM.undoEvents) await CRM.undoEvents(saved.event_ids)
  else await (CRM as unknown as { undoLatest: (targets: unknown[]) => Promise<void> }).undoLatest([{ node: 'A', types: ['crm.customer_updated'] }])
  expect(paths).toContain('/api/events/10/undo')
  expect(paths.some(path => path.includes('/events?') || path.includes('/events/11/undo'))).toBe(false)
})

it('S9-002: missing receipts and receipts from a previous sign-in refuse Undo', async () => {
  const fetch = vi.fn(async () => new Response('{}'))
  vi.stubGlobal('fetch', fetch)
  await expect(CRM.undoEvents(undefined)).rejects.toThrow('receipt')
  expect(fetch).not.toHaveBeenCalled()
  const ids = CRM.mutationEventIds(new Response('{}', { headers: { 'X-Aeon-Event-Ids': '11,10' } }))
  expect(ids).toEqual(['11', '10'])
  Toast.resetToasts()
  await expect(CRM.undoEvents(ids)).rejects.toThrow('Sign-in changed')
  expect(fetch).not.toHaveBeenCalled()
})
