/*
 * PAIMOS — Your Professional & Personal AI Project OS
 * Copyright (C) 2026 Markus Barta <markus@barta.com>
 * Licensed under AGPL-3.0-only; see LICENSE.
 */

import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { createApp, nextTick } from 'vue'

const { apiGet, apiPost, confirmMock } = vi.hoisted(() => ({
  apiGet: vi.fn(),
  apiPost: vi.fn(),
  confirmMock: vi.fn(),
}))

vi.mock('@/api/client', () => ({
  api: { get: apiGet, post: apiPost },
  errMsg: (_error: unknown, fallback: string) => fallback,
}))

vi.mock('@/composables/useConfirm', () => ({
  useConfirm: () => ({ confirm: confirmMock }),
}))

vi.mock('@/stores/auth', () => ({
  useAuthStore: () => ({ user: { id: 2, username: 'Markus' }, isSuperAdmin: true }),
}))

import HoursWeekView from '@/views/HoursWeekView.vue'

const users = [
  { id: 2, username: 'Markus', status: 'active' },
  { id: 3, username: 'David', status: 'active' },
]

const weekReport = {
  from: '2026-08-24',
  to: '2026-08-30',
  users: [
    {
      user_id: 2,
      username: 'Markus',
      days: [{ date: '2026-08-24', hours: 2, entries: 1, assumed: false }],
    },
    { user_id: 3, username: 'David', days: [] },
  ],
}

async function flush() {
  for (let i = 0; i < 8; i++) {
    await Promise.resolve()
    await nextTick()
  }
}

async function mountView() {
  const header = document.createElement('div')
  header.id = 'app-header-left'
  document.body.appendChild(header)
  const el = document.createElement('div')
  document.body.appendChild(el)
  const app = createApp(HoursWeekView)
  app.mount(el)
  await flush()
  return {
    el,
    unmount() {
      app.unmount()
      el.remove()
      header.remove()
    },
  }
}

describe('HoursWeekView', () => {
  beforeEach(() => {
    vi.useFakeTimers()
    vi.setSystemTime(new Date('2026-08-27T10:00:00Z'))
    vi.clearAllMocks()
    apiGet.mockImplementation((path: string) => {
      if (path === '/users') return Promise.resolve(users)
      if (path.startsWith('/time-entries/week?')) return Promise.resolve(weekReport)
      if (path === '/issues/AISP-3') return Promise.resolve({ id: 33, issue_key: 'AISP-3' })
      return Promise.reject(new Error(`unexpected GET ${path}`))
    })
    apiPost.mockResolvedValue({})
  })

  afterEach(() => {
    vi.useRealTimers()
    document.body.innerHTML = ''
  })

  it('renders Markus and David with the target and assumed zero weekdays', async () => {
    const mounted = await mountView()
    expect(mounted.el.textContent).toContain('Markus')
    expect(mounted.el.textContent).toContain('David')
    expect(mounted.el.textContent).toContain('38.5h target')
    expect(mounted.el.textContent).toContain('assumed')
    expect(mounted.el.querySelector<HTMLInputElement>('[aria-label="David 2026-08-24 assumed hours"]')?.value).toBe('7.7')
    mounted.unmount()
  })

  it('asks for confirmation and cancel writes nothing', async () => {
    confirmMock.mockResolvedValue(false)
    const mounted = await mountView()
    mounted.el.querySelector<HTMLButtonElement>('.file-week')!.click()
    await flush()
    expect(confirmMock).toHaveBeenCalledOnce()
    expect(apiPost).not.toHaveBeenCalled()
    mounted.unmount()
  })

  it('files only missing assumed weekdays after confirmation', async () => {
    confirmMock.mockResolvedValue(true)
    const mounted = await mountView()
    mounted.el.querySelector<HTMLButtonElement>('.file-week')!.click()
    await flush()

    expect(apiPost).toHaveBeenCalledTimes(9)
    expect(apiPost.mock.calls.every(([path]) => path === '/issues/33/time-entries')).toBe(true)
    const bodies = apiPost.mock.calls.map(([, body]) => body as Record<string, unknown>)
    expect(bodies.every(body => body.override === 7.7 && String(body.comment).toLowerCase().includes('assumed'))).toBe(true)
    expect(bodies.some(body => body.started_at === '2026-08-24T12:00:00Z' && body.user_id == null)).toBe(false)
    expect(bodies.some(body => body.started_at === '2026-08-24T12:00:00Z' && body.user_id === 3)).toBe(true)
    mounted.unmount()
  })
})
