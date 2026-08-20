/*
 * PAIMOS — Your Professional & Personal AI Project OS
 * Copyright (C) 2026 Markus Barta <markus@barta.com>
 * AGPL-3.0-only — see LICENSE.
 */
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { createApp, h, nextTick, ref } from 'vue'
import { createPinia, setActivePinia } from 'pinia'
import { createMemoryHistory, createRouter } from 'vue-router'

vi.mock('@/api/client', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/api/client')>()
  return {
    ...actual,
    api: {
      get: vi.fn(), post: vi.fn(), put: vi.fn(), patch: vi.fn(), delete: vi.fn(), upload: vi.fn(),
    },
  }
})
vi.mock('@/components/issue/IssueAiActivity.vue', () => ({
  default: { props: ['issueId'], template: '<div class="issue-ai-stub" />' },
}))

import { ApiError, api } from '@/api/client'
import i18n from '@/i18n'
import { useConfirm } from '@/composables/useConfirm'
import type { Issue } from '@/types'
import IssueSidePanel from './IssueSidePanel.vue'

function issue(id: number, updatedAt = `2026-08-20 12:0${id}:00`): Issue {
  return {
    id, project_id: 6, issue_number: 800 + id, issue_key: `PAI-${800 + id}`, type: 'ticket', parent_id: null,
    title: `Ticket ${id}`, description: 'Description', acceptance_criteria: 'AC', notes: '', report_summary: '',
    status: 'in-progress', priority: 'high', cost_unit: null, release: null, billing_type: null,
    total_budget: null, rate_hourly: null, rate_lp: null, estimate_hours: 2, estimate_lp: 3, ar_hours: null,
    ar_lp: null, time_override: null, start_date: null, end_date: null, group_state: null, sprint_state: null,
    jira_id: null, jira_version: null, jira_text: null, color: null, sprint_ids: [], archived: false,
    assignee_id: null, assignee: null, children: [], tags: [], created_at: '2026-08-20 10:00:00',
    updated_at: updatedAt, created_by: 1, created_by_name: 'mba', last_changed_by_name: 'mba', booked_hours: 0,
    time_logged: 0, time_rollup: 0, time_total: 0, accepted_at: null, accepted_by: null, invoiced_at: null,
    invoice_number: '',
  }
}

async function flush(times = 20) {
  for (let i = 0; i < times; i += 1) {
    await Promise.resolve()
    await nextTick()
  }
}

interface PanelHandle {
  requestLeave: () => Promise<boolean>
  hasUnsavedChanges: boolean
}

interface Harness {
  root: HTMLElement
  issueId: ReturnType<typeof ref<number | null>>
  panel: PanelHandle
  unmount: () => void
}

async function mountPanel(extra: Record<string, unknown> = {}): Promise<Harness> {
  const root = document.createElement('div')
  document.body.appendChild(root)
  const issueId = ref<number | null>(1)
  const panel = ref<PanelHandle | null>(null)
  const pinia = createPinia()
  setActivePinia(pinia)
  const router = createRouter({ history: createMemoryHistory(), routes: [{ path: '/', component: { render: () => h('div') } }] })
  await router.push('/')
  const app = createApp({
    render: () => h(IssueSidePanel, {
      ref: panel,
      issueId: issueId.value,
      embedded: true,
      readonly: false,
      internalCommentsOnly: true,
      allowComments: true,
      allowAttachments: true,
      noteAffectsNextRun: true,
      ...extra,
    }),
  })
  app.use(pinia)
  app.use(router)
  app.use(i18n)
  app.mount(root)
  await flush()
  return { root, issueId, panel: panel.value!, unmount: () => { app.unmount(); root.remove() } }
}

function option(text: string): HTMLButtonElement {
  return [...document.querySelectorAll<HTMLButtonElement>('.ms-option')]
    .find((el) => el.textContent?.trim() === text)!
}

describe('IssueSidePanel Agent Mode integration (PAI-806)', () => {
  let harness: Harness | null = null

  beforeEach(() => {
    vi.mocked(api.get).mockReset()
    vi.mocked(api.post).mockReset()
    vi.mocked(api.put).mockReset()
    vi.mocked(api.patch).mockReset()
    vi.mocked(api.delete).mockReset()
    vi.mocked(api.upload).mockReset()
    vi.mocked(api.get).mockImplementation(async (path: string) => {
      if (/^\/issues\/\d+$/.test(path)) return issue(Number(path.split('/').pop())) as never
      return [] as never
    })
    vi.mocked(api.put).mockImplementation(async (path: string, payload) => ({
      ...issue(Number(path.split('/').pop()), '2026-08-20 13:00:00'),
      ...(payload as Record<string, unknown>),
    }) as never)
    vi.mocked(api.post).mockResolvedValue({ id: 10, body: 'note', visibility: 'internal', author_id: 1, author: 'mba', created_at: 'now' } as never)
    vi.mocked(api.delete).mockResolvedValue(undefined as never)
    vi.mocked(api.upload).mockResolvedValue({ id: 44 } as never)
    Object.assign(URL, { createObjectURL: vi.fn(() => 'blob:test'), revokeObjectURL: vi.fn() })
  })

  afterEach(() => {
    harness?.unmount()
    harness = null
    document.body.innerHTML = ''
    vi.unstubAllGlobals()
  })

  it('uses If-Match for status, assignee and full-form saves', async () => {
    harness = await mountPanel({ users: [{ id: 7, username: 'ada', role: 'member', status: 'active' }] })
    const triggers = harness.root.querySelectorAll<HTMLButtonElement>('.sp-meta .meta-select-trigger')
    triggers[0].click()
    await flush()
    option('QA').click()
    await flush()
    expect(api.put).toHaveBeenLastCalledWith('/issues/1', { status: 'qa' }, {
      headers: { 'If-Match': '"issue-1-2026-08-20T12:01:00"' },
    })

    harness.root.querySelectorAll<HTMLButtonElement>('.sp-meta .meta-select-trigger')[1].click()
    await flush()
    const assigneeOptions = [...document.querySelectorAll<HTMLButtonElement>('.ms-option')]
    const assigneeOption = assigneeOptions[assigneeOptions.length - 1]
    expect(assigneeOption.textContent).toContain('ada')
    assigneeOption.click()
    await flush()
    expect(api.put).toHaveBeenLastCalledWith('/issues/1', { assignee_id: 7 }, {
      headers: { 'If-Match': '"issue-1-2026-08-20T13:00:00"' },
    })

    harness.root.querySelector<HTMLButtonElement>('[title="Quick Edit"]')!.click()
    await flush()
    const title = harness.root.querySelector<HTMLInputElement>('.sp-form input[type="text"]')!
    title.value = 'Edited safely'
    title.dispatchEvent(new Event('input', { bubbles: true }))
    harness.root.querySelector<HTMLButtonElement>('.sp-form-actions .btn-primary')!.click()
    await flush()
    const putCalls = vi.mocked(api.put).mock.calls
    expect(putCalls[putCalls.length - 1]?.[2]).toEqual({
      headers: { 'If-Match': '"issue-1-2026-08-20T13:00:00"' },
    })
  })

  it('reloads on a 412 and never silently overwrites', async () => {
    harness = await mountPanel()
    vi.mocked(api.put).mockRejectedValueOnce(new ApiError(412, 'conflict'))
    harness.root.querySelector<HTMLButtonElement>('.sp-meta .meta-select-trigger')!.click()
    await flush()
    option('QA').click()
    await flush()
    expect(harness.root.textContent).toContain('Latest values were reloaded')
    expect(vi.mocked(api.get).mock.calls.filter(([path]) => path === '/issues/1')).toHaveLength(2)
  })

  it('drops stale load results when selection changes', async () => {
    let resolveOne!: (value: Issue) => void
    vi.mocked(api.get).mockImplementation((path: string) => {
      if (path === '/issues/1') return new Promise((resolve) => { resolveOne = resolve as (value: Issue) => void }) as never
      if (path === '/issues/2') return Promise.resolve(issue(2)) as never
      return Promise.resolve([]) as never
    })
    harness = await mountPanel()
    harness.issueId.value = 2
    await flush()
    resolveOne(issue(1))
    await flush()
    expect(harness.root.querySelector('.sp-key')?.textContent).toContain('PAI-802')
    expect(harness.root.textContent).not.toContain('Ticket 1')
  })

  it('locks notes to internal, shows next-run truth, and treats the draft as dirty', async () => {
    harness = await mountPanel()
    expect(harness.root.textContent).toContain('will affect the next run')
    expect(harness.root.querySelector('[role="radiogroup"]')).toBeNull()
    const textarea = harness.root.querySelector<HTMLTextAreaElement>('.comment-textarea')!
    textarea.value = 'Reviewer note'
    textarea.dispatchEvent(new Event('input', { bubbles: true }))
    await flush()
    expect(harness.panel.hasUnsavedChanges).toBe(true)
    harness.root.querySelector<HTMLButtonElement>('.comment-form-actions .btn-primary')!.click()
    await flush()
    expect(api.post).toHaveBeenCalledWith('/issues/1/comments', { body: 'Reviewer note', visibility: 'internal' })
  })

  it('gates file paste and drop fail-closed, ignores ordinary text, and blocks leave while upload is in flight', async () => {
    harness = await mountPanel({ allowAttachments: false })
    expect(harness.root.querySelector('.att-drop')).toBeNull()
    const deniedFile = new File(['x'], 'denied.png', { type: 'image/png' })
    harness.root.querySelector('.side-panel')!.dispatchEvent(Object.assign(new Event('paste', { bubbles: true, cancelable: true }), {
      clipboardData: { files: [deniedFile] },
    }))
    await flush()
    expect(api.upload).not.toHaveBeenCalled()
    harness.root.querySelector('.side-panel')!.dispatchEvent(Object.assign(new Event('drop', { bubbles: true, cancelable: true }), {
      dataTransfer: { files: [deniedFile], types: ['Files'] },
    }))
    await flush()
    expect(api.upload).not.toHaveBeenCalled()
    harness.unmount()

    let finish!: (value: unknown) => void
    vi.mocked(api.upload).mockImplementation(() => new Promise((resolve) => { finish = resolve }) as never)
    harness = await mountPanel()
    const panel = harness.root.querySelector('.side-panel')!
    panel.dispatchEvent(Object.assign(new Event('paste', { bubbles: true, cancelable: true }), {
      clipboardData: { files: [], getData: () => 'ordinary text' },
    }))
    const file = new File(['x'], 'screen.png', { type: 'image/png' })
    panel.dispatchEvent(Object.assign(new Event('paste', { bubbles: true, cancelable: true }), {
      clipboardData: { files: [file] },
    }))
    await flush()
    expect(api.upload).toHaveBeenCalledTimes(1)
    expect(await harness.panel.requestLeave()).toBe(false)
    expect(harness.root.textContent).toContain('upload is still in progress')
    finish({ id: 44 })
    await flush()

    vi.mocked(api.upload).mockResolvedValue({ id: 45 } as never)
    const dropped = new File(['x'], 'dropped.txt', { type: 'text/plain' })
    panel.dispatchEvent(Object.assign(new Event('drop', { bubbles: true, cancelable: true }), {
      dataTransfer: { files: [dropped], types: ['Files'] },
    }))
    await flush()
    expect(api.upload).toHaveBeenCalledTimes(2)
  })

  it('guards dirty close and isolates Escape in editor controls', async () => {
    harness = await mountPanel()
    harness.root.querySelector<HTMLButtonElement>('[title="Quick Edit"]')!.click()
    await flush()
    const title = harness.root.querySelector<HTMLInputElement>('.sp-form input[type="text"]')!
    title.value = 'Dirty'
    title.dispatchEvent(new Event('input', { bubbles: true }))
    title.focus()
    title.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true, cancelable: true }))
    await flush()
    expect(harness.root.querySelector('.side-panel')).not.toBeNull()
    const leaving = harness.panel.requestLeave()
    await flush()
    expect(useConfirm().visible.value).toBe(true)
    useConfirm().resolve(false)
    expect(await leaving).toBe(false)
  })
})
