// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>
// Licensed under AGPL-3.0-only; see LICENSE.

import { beforeEach, describe, expect, it, vi } from 'vitest'
import { createApp, nextTick, ref } from 'vue'

const { apiPut, refresh } = vi.hoisted(() => ({
  apiPut: vi.fn(),
  refresh: vi.fn(),
}))

vi.mock('@/api/client', () => ({
  api: { put: apiPut, upload: vi.fn() },
  errMsg: (_error: unknown, fallback: string) => fallback,
}))

vi.mock('@/composables/useBranding', () => ({
  useBranding: () => ({
    branding: ref({
      name: 'PAIMOS',
      company: 'PAIMOS',
      product: 'PAIMOS',
      tagline: 'Project Management Online',
      website: 'https://paimos.com',
      logo: '/logo.svg',
      favicon: '/favicon.svg',
      backgroundPattern: 'triangle',
      colors: {
        primary: '#52525b', primaryDark: '#3f3f46', primaryLight: '#a1a1aa', primaryPale: '#f4f4f5',
        accent: '#16a34a', sidebarBg: '#18181b', sidebarText: '#e4e4e7', loginBg: '#18181b',
        loginPattern: '#27272a', typeEpic: '#5e35b1', typeTicket: '#3f3f46', typeTask: '#2e7d32',
        tableRowBorder: '#e8eaed', tableRowAlt: '#f8f9fa', accrualsAccent: '#006497',
      },
      pageTitle: 'PAIMOS',
      contractor: [],
    }),
    refresh,
  }),
}))

import SettingsBrandingTab from './SettingsBrandingTab.vue'

describe('SettingsBrandingTab background pattern (PAI-738)', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    apiPut.mockResolvedValue({})
    refresh.mockResolvedValue(undefined)
    document.body.innerHTML = ''
  })

  it('offers all patterns and persists the selected value', async () => {
    const el = document.createElement('div')
    document.body.appendChild(el)
    const app = createApp(SettingsBrandingTab)
    app.mount(el)
    await nextTick()

    const radios = [...el.querySelectorAll<HTMLInputElement>('input[name="background-pattern"]')]
    expect(radios.map(input => input.value)).toEqual(['triangle', 'square', 'hex', 'lines', 'none'])
    expect(radios.find(input => input.value === 'triangle')?.checked).toBe(true)

    const lines = radios.find(input => input.value === 'lines')!
    lines.checked = true
    lines.dispatchEvent(new Event('change'))
    await nextTick()

    const save = [...el.querySelectorAll<HTMLButtonElement>('button')]
      .find(button => button.textContent?.includes('Save branding'))!
    save.click()
    await nextTick()
    await nextTick()

    expect(apiPut).toHaveBeenCalledWith('/branding', expect.objectContaining({
      backgroundPattern: 'lines',
    }))
    expect(refresh).toHaveBeenCalledOnce()

    app.unmount()
    el.remove()
  })
})
