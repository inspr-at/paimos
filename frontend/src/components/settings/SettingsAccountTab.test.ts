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
  credential_kind?: 'general' | 'machine_notifier' | 'conversation_service'
}

const { apiGet, apiPost, apiPatch, apiDelete, authState, confirmAction } = vi.hoisted(() => ({
  apiGet: vi.fn(),
  apiPost: vi.fn(),
  apiPatch: vi.fn(),
  apiDelete: vi.fn(),
  authState: { isAdmin: false },
  confirmAction: vi.fn(),
}))

vi.mock('@/api/client', () => ({
  api: { get: apiGet, post: apiPost, patch: apiPatch, delete: apiDelete, put: vi.fn(), upload: vi.fn() },
  ApiError: class ApiError extends Error {},
  permissionsEpoch: { value: null },
  permissionsEpochGeneration: { value: 0 },
  csrfHeaders: () => ({}),
  errMsg: (_error: unknown, fallback: string) => fallback,
}))

vi.mock('vue-router', () => ({
  useRoute: () => ({ query: {} }),
}))

vi.mock('@/stores/auth', () => ({
  useAuthStore: () => ({
    user: { id: 1, username: 'operator', role: 'member', first_name: 'Op', last_name: 'Erator' },
    get isAdmin() {
      return authState.isAdmin
    },
    setTOTPEnabled: vi.fn(),
    refreshMe: vi.fn(),
  }),
}))

vi.mock('@/composables/useConfirm', () => ({
  useConfirm: () => ({ confirm: confirmAction }),
}))

// Rendered as nothing: this test is about one table cell, and the child
// components drag in modals, icons, and their own fetches.
vi.mock('@/components/AppModal.vue', () => ({ default: { name: 'AppModal', render: () => null } }))
vi.mock('@/components/AppIcon.vue', () => ({ default: { name: 'AppIcon', render: () => null } }))
vi.mock('@/components/ai/AiPaperTrailPanel.vue', () => ({ default: { name: 'AiPaperTrailPanel', render: () => null } }))

import SettingsAccountTab from './SettingsAccountTab.vue'

function apiKey(
  id: number,
  name: string,
  scopes?: string[],
  credentialKind?: 'general' | 'machine_notifier' | 'conversation_service',
): APIKeyFixture {
  const key: APIKeyFixture = {
    id,
    name,
    key_prefix: 'paimos_ab',
    created_at: '2026-08-21 09:00:00',
    last_used_at: null,
  }
  if (scopes !== undefined) key.scopes = scopes
  if (credentialKind !== undefined) key.credential_kind = credentialKind
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
    authState.isAdmin = false
    confirmAction.mockResolvedValue(false)
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

function input(el: HTMLElement, id: string, value: string) {
  const node = el.querySelector<HTMLInputElement | HTMLTextAreaElement>(`#${id}`)!
  node.value = value
  node.dispatchEvent(new Event('input', { bubbles: true }))
}

async function settle() {
  await Promise.resolve()
  for (let index = 0; index < 8; index++) await nextTick()
}

function encryptedEnrollment(overrides: Record<string, unknown> = {}) {
  return {
    id: 41,
    name: 'gateway notifier',
    key_prefix: 'paimos_ab',
    expires_at: '2027-01-01T00:00:00.000Z',
    binding: {
      project_id: 17,
      sender: 'gateway',
      to: 'agent:receiver',
      target_id: 'target-current',
      target_version: 4,
    },
    credential_delivery: 'age',
    key_age_base64: window.btoa('age-encryption.org/v1\nencrypted credential bytes'),
    ...overrides,
  }
}

async function fillNotifierForm(el: HTMLElement, recipients = 'age1publicrecipient') {
  input(el, 'notifier-name', 'gateway notifier')
  input(el, 'notifier-project', '17')
  input(el, 'notifier-sender', 'gateway')
  input(el, 'notifier-to', 'agent:receiver')
  input(el, 'notifier-target', 'target-current')
  input(el, 'notifier-target-version', '4')
  input(el, 'notifier-expiry', '2027-01-01T00:00:00Z')
  input(el, 'notifier-recipients', recipients)
  await nextTick()
}

describe('SettingsAccountTab machine notifier enrollment (PAI-1021)', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    authState.isAdmin = false
    confirmAction.mockResolvedValue(false)
    document.body.innerHTML = ''
    Object.defineProperty(URL, 'createObjectURL', {
      configurable: true,
      writable: true,
      value: vi.fn(() => 'blob:notifier-age'),
    })
    Object.defineProperty(URL, 'revokeObjectURL', {
      configurable: true,
      writable: true,
      value: vi.fn(),
    })
  })

  it('does not expose enrollment to a non-admin', async () => {
    const el = await mountWithKeys([])
    expect(el.querySelector('[data-testid="machine-notifier-enrollment"]')).toBeNull()
    expect(el.querySelector('[data-testid="conversation-enrollment"]')).toBeNull()
    expect(apiPost).not.toHaveBeenCalledWith('/auth/machine-notifiers', expect.anything())
  })

  it('submits the exact binding and downloads only validated age ciphertext', async () => {
    vi.useFakeTimers()
    authState.isAdmin = true
    apiPost.mockResolvedValueOnce(encryptedEnrollment())
    const click = vi.spyOn(HTMLAnchorElement.prototype, 'click').mockImplementation(() => {})
    const el = await mountWithKeys([])
    await fillNotifierForm(el, 'age1first\nssh-ed25519 AAAApublic')

    el.querySelector<HTMLButtonElement>(
      '[data-testid="machine-notifier-enrollment"] button[type="submit"]',
    )!.click()
    await settle()

    expect(apiPost).toHaveBeenCalledWith('/auth/machine-notifiers', {
      name: 'gateway notifier',
      project_id: 17,
      sender: 'gateway',
      to: 'agent:receiver',
      target_id: 'target-current',
      target_version: 4,
      expires_at: '2027-01-01T00:00:00Z',
      age_recipients: ['age1first', 'ssh-ed25519 AAAApublic'],
    })
    expect(URL.createObjectURL).toHaveBeenCalledOnce()
    const blob = vi.mocked(URL.createObjectURL).mock.calls[0]![0] as Blob
    expect(await blob.text()).toBe('age-encryption.org/v1\nencrypted credential bytes')
    expect(click).toHaveBeenCalledOnce()
    expect(URL.revokeObjectURL).not.toHaveBeenCalled()
    expect(document.querySelector('a[download="gateway-notifier.age"]')).not.toBeNull()
    expect(el.textContent).toContain('Encrypted credential ready as gateway-notifier.age. Download started.')
    expect(el.textContent).toContain('Machine notifier')
    expect(el.textContent).toContain('fixed notifier route')
    expect(el.textContent).not.toContain(encryptedEnrollment().key_age_base64)
    expect(el.querySelector('.apikey-reveal')).toBeNull()

    const retry = [...el.querySelectorAll<HTMLButtonElement>('button')].find(
      button => button.textContent?.trim() === 'Download encrypted credential again',
    )!
    retry.click()
    await nextTick()
    expect(apiPost).toHaveBeenCalledTimes(1)
    expect(URL.createObjectURL).toHaveBeenCalledTimes(2)
    const retryBlob = vi.mocked(URL.createObjectURL).mock.calls[1]![0] as Blob
    expect(await retryBlob.text()).toBe(await blob.text())
    expect(click).toHaveBeenCalledTimes(2)
    expect(document.querySelectorAll('a[download="gateway-notifier.age"]')).toHaveLength(2)

    vi.runOnlyPendingTimers()
    expect(URL.revokeObjectURL).toHaveBeenCalledTimes(2)
    expect(document.querySelector('a[download="gateway-notifier.age"]')).toBeNull()
    vi.useRealTimers()
  })

  it('rejects plaintext or mismatched responses without creating a download surface', async () => {
    authState.isAdmin = true
    apiPost
      .mockResolvedValueOnce({ ...encryptedEnrollment(), key: 'plaintext-must-not-render' })
      .mockResolvedValueOnce(
        encryptedEnrollment({ binding: { ...encryptedEnrollment().binding, target_version: 5 } }),
      )
      .mockResolvedValueOnce(
        encryptedEnrollment({ key_age_base64: window.btoa('not an age credential') }),
      )
    const click = vi.spyOn(HTMLAnchorElement.prototype, 'click').mockImplementation(() => {})
    const el = await mountWithKeys([])
    await fillNotifierForm(el)

    const submit = el.querySelector<HTMLButtonElement>(
      '[data-testid="machine-notifier-enrollment"] button[type="submit"]',
    )!
    submit.click()
    await settle()
    expect(el.textContent).toContain('Failed to create a valid encrypted notifier credential.')
    expect(el.textContent).not.toContain('plaintext-must-not-render')
    expect(URL.createObjectURL).not.toHaveBeenCalled()

    submit.click()
    await settle()
    expect(URL.createObjectURL).not.toHaveBeenCalled()

    submit.click()
    await settle()
    expect(URL.createObjectURL).not.toHaveBeenCalled()
    expect(click).not.toHaveBeenCalled()
  })

  it('rejects duplicate recipients before calling the enrollment endpoint', async () => {
    authState.isAdmin = true
    const el = await mountWithKeys([])
    await fillNotifierForm(el, 'age1duplicate\nage1duplicate')
    el.querySelector<HTMLButtonElement>(
      '[data-testid="machine-notifier-enrollment"] button[type="submit"]',
    )!.click()
    await settle()
    expect(apiPost).not.toHaveBeenCalled()
    expect(el.textContent).toContain('Add 1 to 8 unique public age recipients')
    expect(URL.createObjectURL).not.toHaveBeenCalled()
  })

  it('labels both credential kinds and preserves the shared revoke endpoint', async () => {
    authState.isAdmin = true
    confirmAction.mockResolvedValue(true)
    const el = await mountWithKeys([
      apiKey(1, 'ordinary', ['*'], 'general'),
      apiKey(2, 'notifier', [], 'machine_notifier'),
      apiKey(3, 'conversation', [], 'conversation_service'),
    ])
    const rows = [...el.querySelectorAll<HTMLTableRowElement>('table.settings-table tbody tr')]
    expect(rows[0]!.textContent).toContain('API key')
    expect(rows[1]!.textContent).toContain('Machine notifier')
    expect(rows[1]!.textContent).toContain('fixed notifier route')
    expect(rows[2]!.textContent).toContain('Conversation service')
    expect(rows[2]!.textContent).toContain('fixed conversation binding')

    rows[0]!.querySelector<HTMLButtonElement>('button')!.click()
    await settle()
    expect(apiDelete).toHaveBeenCalledWith('/auth/api-keys/1')
    expect(confirmAction).toHaveBeenCalledWith(
      expect.objectContaining({ message: expect.stringContaining('API key') }),
    )

    rows[2]!.querySelector<HTMLButtonElement>('button')!.click()
    await settle()
    expect(apiDelete).toHaveBeenCalledWith('/auth/api-keys/3')
    expect(confirmAction).toHaveBeenLastCalledWith(
      expect.objectContaining({ message: expect.stringContaining('conversation service credential') }),
    )
  })
})
