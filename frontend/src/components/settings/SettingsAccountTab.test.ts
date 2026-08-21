// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>
// Licensed under AGPL-3.0-only; see LICENSE.

import { beforeEach, describe, expect, it, vi } from 'vitest'
import { createApp, nextTick } from 'vue'

interface APIKeyFixture {
  id: number
  name: string
  key_prefix: string
  created_at: string
  last_used_at: string | null
  scopes?: string[]
}

const { apiGet, apiPost, apiPatch, apiDelete } = vi.hoisted(() => ({
  apiGet: vi.fn(),
  apiPost: vi.fn(),
  apiPatch: vi.fn(),
  apiDelete: vi.fn(),
}))

vi.mock('@/api/client', () => ({
  api: { get: apiGet, post: apiPost, patch: apiPatch, delete: apiDelete, put: vi.fn(), upload: vi.fn() },
  csrfHeaders: () => ({}),
  errMsg: (_error: unknown, fallback: string) => fallback,
}))

vi.mock('vue-router', () => ({
  useRoute: () => ({ query: {} }),
}))

vi.mock('@/stores/auth', () => ({
  useAuthStore: () => ({
    user: { id: 1, username: 'operator', role: 'member', first_name: 'Op', last_name: 'Erator' },
    isAdmin: false,
    setTOTPEnabled: vi.fn(),
    refreshMe: vi.fn(),
  }),
}))

vi.mock('@/composables/useConfirm', () => ({
  useConfirm: () => ({ confirm: vi.fn().mockResolvedValue(false) }),
}))

// Rendered as nothing: this test is about one table cell, and the child
// components drag in modals, icons, and their own fetches.
vi.mock('@/components/AppModal.vue', () => ({ default: { name: 'AppModal', render: () => null } }))
vi.mock('@/components/AppIcon.vue', () => ({ default: { name: 'AppIcon', render: () => null } }))
vi.mock('@/components/ai/AiPaperTrailPanel.vue', () => ({ default: { name: 'AiPaperTrailPanel', render: () => null } }))

import SettingsAccountTab from './SettingsAccountTab.vue'

function apiKey(id: number, name: string, scopes?: string[]): APIKeyFixture {
  const key: APIKeyFixture = {
    id,
    name,
    key_prefix: 'paimos_ab',
    created_at: '2026-08-21 09:00:00',
    last_used_at: null,
  }
  if (scopes !== undefined) key.scopes = scopes
  return key
}

async function mountWithKeys(keys: APIKeyFixture[]): Promise<HTMLElement> {
  apiGet.mockImplementation(async (path: string) => {
    if (path === '/auth/api-keys') return keys
    if (path === '/auth/totp/status') return { enabled: false }
    if (path === '/auth/auto-watch') return []
    if (path === '/schema') return { scopes: [] }
    return {}
  })
  const el = document.createElement('div')
  document.body.appendChild(el)
  createApp(SettingsAccountTab).mount(el)
  // init() chains four awaited loads before the table renders.
  for (let i = 0; i < 10; i++) await nextTick()
  return el
}

function scopeCells(el: HTMLElement): HTMLTableCellElement[] {
  const rows = [...el.querySelectorAll<HTMLTableRowElement>('table.settings-table tbody tr')]
  return rows.map(row => row.cells[2])
}

describe('SettingsAccountTab API-key scope truth (PAI-809)', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    document.body.innerHTML = ''
  })

  it('tells the truth about every scope state', async () => {
    const el = await mountWithKeys([
      apiKey(1, 'legacy-no-metadata'),
      apiKey(2, 'explicit-sentinel', ['*']),
      apiKey(3, 'sentinel-plus-named', ['*', 'projects:write']),
      apiKey(4, 'explicitly-empty', []),
      apiKey(5, 'named', ['agent-controls:write', 'projects:write']),
    ])

    const cells = scopeCells(el)
    expect(cells).toHaveLength(5)

    // Missing metadata is a legacy row: it really is full power.
    expect(cells[0].textContent?.trim()).toBe('full')
    // The explicit sentinel is full power too, with or without company.
    expect(cells[1].textContent?.trim()).toBe('full')
    expect(cells[2].textContent?.trim()).toBe('full')
    // An explicit empty list opens nothing — this used to read "full".
    expect(cells[3].textContent?.trim()).toBe('none')
    // Named scopes stay chips, one per scope, unchanged.
    const chips = [...cells[4].querySelectorAll('code.icode')].map(c => c.textContent)
    expect(chips).toEqual(['agent-controls:write', 'projects:write'])
    expect(cells[4].textContent).not.toContain('full')
    expect(cells[4].textContent).not.toContain('none')
  })

  it('gives the no-access label an accessible name', async () => {
    const el = await mountWithKeys([apiKey(1, 'explicitly-empty', [])])

    const labelled = el.querySelector('[aria-label="No scoped access"]')
    expect(labelled).not.toBeNull()
    expect(labelled?.textContent?.trim()).toBe('none')

    // Still the existing muted presentation — no new card, chip, or icon.
    expect(labelled?.classList.contains('muted')).toBe(true)
    expect(scopeCells(el)[0].querySelectorAll('code.icode')).toHaveLength(0)
  })

  it('does not label a scoped key as having no access', async () => {
    const el = await mountWithKeys([apiKey(1, 'named', ['agent-controls:runner'])])
    expect(el.querySelector('[aria-label="No scoped access"]')).toBeNull()
    expect(scopeCells(el)[0].textContent?.trim()).toBe('agent-controls:runner')
  })

  it('tells the same truth in the one-time key reveal', async () => {
    apiPost
      .mockResolvedValueOnce({ id: 11, name: 'legacy', key_prefix: 'paimos_ab', key: 'secret-1' })
      .mockResolvedValueOnce({ id: 12, name: 'wildcard', key_prefix: 'paimos_ab', key: 'secret-2', scopes: ['*'] })
      .mockResolvedValueOnce({ id: 13, name: 'empty', key_prefix: 'paimos_ab', key: 'secret-3', scopes: [] })
      .mockResolvedValueOnce({ id: 14, name: 'named', key_prefix: 'paimos_ab', key: 'secret-4', scopes: ['agent-controls:runner'] })
    const el = await mountWithKeys([])
    const input = el.querySelector<HTMLInputElement>('.apikey-create-row input')!
    const create = el.querySelector<HTMLButtonElement>('.apikey-create-row button')!

    async function submit(name: string): Promise<HTMLElement> {
      input.value = name
      input.dispatchEvent(new Event('input', { bubbles: true }))
      create.click()
      await Promise.resolve()
      for (let i = 0; i < 6; i++) await nextTick()
      return el.querySelector<HTMLElement>('.apikey-reveal .apikey-scope-chips')!
    }

    let reveal = await submit('legacy')
    expect(reveal.textContent?.replace(/\s+/g, '')).toBe('Scopes:full')

    reveal = await submit('wildcard')
    expect(reveal.textContent?.replace(/\s+/g, '')).toBe('Scopes:full')

    reveal = await submit('empty')
    expect(reveal.textContent?.replace(/\s+/g, '')).toBe('Scopes:none')
    expect(reveal.querySelector('[aria-label="No scoped access"]')).not.toBeNull()

    reveal = await submit('named')
    expect([...reveal.querySelectorAll('code.icode')].map(node => node.textContent)).toEqual(['agent-controls:runner'])
    expect(reveal.textContent).not.toContain('full')
    expect(reveal.textContent).not.toContain('none')
  })
})
