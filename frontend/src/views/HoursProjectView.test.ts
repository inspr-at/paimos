/*
 * PAIMOS — Your Professional & Personal AI Project OS
 * Copyright (C) 2026 Markus Barta <markus@barta.com>
 * Licensed under AGPL-3.0-only; see LICENSE.
 */

import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { createApp, nextTick } from 'vue'

const { apiGet } = vi.hoisted(() => ({ apiGet: vi.fn() }))

vi.mock('@/api/client', () => ({
  api: { get: apiGet },
  errMsg: (_error: unknown, fallback: string) => fallback,
}))

vi.mock('vue-router', () => ({
  RouterLink: { props: ['to'], template: '<a :href="to"><slot /></a>' },
}))

import HoursProjectView from '@/views/HoursProjectView.vue'

async function flush() {
  for (let i = 0; i < 6; i++) {
    await Promise.resolve()
    await nextTick()
  }
}

describe('HoursProjectView', () => {
  beforeEach(() => {
    vi.useFakeTimers()
    vi.setSystemTime(new Date('2026-08-27T10:00:00Z'))
    vi.clearAllMocks()
    apiGet.mockImplementation((path: string) => {
      if (path === '/projects') return Promise.resolve([{ id: 17, key: 'AISP', name: 'Augmentoring AI Services Platform' }])
      if (path.startsWith('/projects/17/time-report?')) {
        return Promise.resolve({
          total_hours: 12.5,
          total_entries: 3,
          by_issue: [{ issue_id: 31, issue_key: 'AISP-4', title: 'Grant delivery', hours: 8.5, entries: 2 }],
          by_user: [{ user_id: 3, username: 'David', hours: 8.5, entries: 2 }],
        })
      }
      return Promise.reject(new Error(`unexpected GET ${path}`))
    })
  })

  afterEach(() => {
    vi.useRealTimers()
    document.body.innerHTML = ''
  })

  it('resolves AISP and renders by-issue and by-person time-report rows', async () => {
    const header = document.createElement('div')
    header.id = 'app-header-left'
    document.body.appendChild(header)
    const el = document.createElement('div')
    document.body.appendChild(el)
    const app = createApp(HoursProjectView)
    app.mount(el)
    await flush()

    expect(apiGet).toHaveBeenCalledWith('/projects')
    expect(apiGet.mock.calls.some(([path]) => String(path).startsWith('/projects/17/time-report?'))).toBe(true)
    expect(el.textContent).toContain('AISP-4')
    expect(el.textContent).toContain('Grant delivery')
    expect(el.textContent).toContain('David')
    expect(el.querySelector<HTMLAnchorElement>('a')?.getAttribute('href')).toBe('/issues/AISP-4')

    app.unmount()
  })
})
