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
import type { Attachment, Issue } from '@/types'
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

function attachment(id: number, filename: string): Attachment {
  return {
    id,
    issue_id: 1,
    object_key: `issues/1/${filename}`,
    filename,
    content_type: 'text/plain',
    size_bytes: 12,
    uploaded_by: 1,
    uploader: 'mba',
    created_at: '2026-08-20 12:00:00',
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
  readonly: ReturnType<typeof ref<boolean>>
  allowAttachments: ReturnType<typeof ref<boolean>>
  panel: PanelHandle
  unmount: () => void
}

function deferred<T>() {
  let resolve!: (value: T) => void
  let reject!: (reason?: unknown) => void
  const promise = new Promise<T>((res, rej) => {
    resolve = res
    reject = rej
  })
  return { promise, resolve, reject }
}

async function mountPanel(extra: Record<string, unknown> = {}): Promise<Harness> {
  const root = document.createElement('div')
  document.body.appendChild(root)
  const issueId = ref<number | null>(1)
  const readonly = ref(extra.readonly === true)
  const allowAttachments = ref(extra.allowAttachments !== false)
  const extraProps = { ...extra }
  delete extraProps.readonly
  delete extraProps.allowAttachments
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
      internalCommentsOnly: true,
      allowComments: true,
      allowAttachments: allowAttachments.value,
      noteAffectsNextRun: true,
      ...extraProps,
      readonly: readonly.value,
    }),
  })
  app.use(pinia)
  app.use(router)
  app.use(i18n)
  app.mount(root)
  await flush()
  return {
    root,
    issueId,
    readonly,
    allowAttachments,
    panel: panel.value!,
    unmount: () => { app.unmount(); root.remove() },
  }
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

  it('keeps an assignee 412 reload failure visible outside the quick field', async () => {
    harness = await mountPanel({ users: [{ id: 7, username: 'ada', role: 'member', status: 'active' }] })
    vi.mocked(api.put).mockRejectedValueOnce(new ApiError(412, 'conflict'))
    vi.mocked(api.get).mockImplementation(async (path: string) => {
      if (path === '/issues/1') throw new Error('reload failed')
      return [] as never
    })
    harness.root.querySelectorAll<HTMLButtonElement>('.sp-meta .meta-select-trigger')[1].click()
    await flush()
    const ada = [...document.querySelectorAll<HTMLButtonElement>('.ms-option')]
      .find((candidate) => candidate.textContent?.includes('ada'))!
    expect(ada).not.toBeUndefined()
    ada.click()
    await flush()
    expect(harness.root.querySelector('[role="alert"]')?.textContent).toContain('could not be reloaded')
  })

  it('keeps full-form 412 truth visible after reload success and failure', async () => {
    harness = await mountPanel()
    vi.mocked(api.put).mockRejectedValueOnce(new ApiError(412, 'conflict'))
    harness.root.querySelector<HTMLButtonElement>('[title="Quick Edit"]')!.click()
    await flush()
    harness.root.querySelector<HTMLInputElement>('.sp-form input[type="text"]')!.value = 'Conflicting edit'
    harness.root.querySelector<HTMLInputElement>('.sp-form input[type="text"]')!
      .dispatchEvent(new Event('input', { bubbles: true }))
    harness.root.querySelector<HTMLButtonElement>('.sp-form-actions .btn-primary')!.click()
    await flush()
    expect(harness.root.querySelector('.sp-form')).toBeNull()
    expect(harness.root.querySelector('[role="alert"]')?.textContent).toContain('Latest values were reloaded')

    harness.unmount()
    harness = await mountPanel()
    vi.mocked(api.put).mockRejectedValueOnce(new ApiError(412, 'conflict'))
    vi.mocked(api.get).mockImplementation(async (path: string) => {
      if (path === '/issues/1') throw new Error('reload failed')
      return [] as never
    })
    harness.root.querySelector<HTMLButtonElement>('[title="Quick Edit"]')!.click()
    await flush()
    const title = harness.root.querySelector<HTMLInputElement>('.sp-form input[type="text"]')!
    title.value = 'Another conflict'
    title.dispatchEvent(new Event('input', { bubbles: true }))
    harness.root.querySelector<HTMLButtonElement>('.sp-form-actions .btn-primary')!.click()
    await flush()
    expect(harness.root.querySelector('.sp-form')).toBeNull()
    expect(harness.root.querySelector('[role="alert"]')?.textContent).toContain('could not be reloaded')
  })

  it('fails closed when edit authority is revoked before submit', async () => {
    harness = await mountPanel()
    vi.mocked(api.put).mockClear()
    harness.root.querySelector<HTMLButtonElement>('[title="Quick Edit"]')!.click()
    await flush()
    const title = harness.root.querySelector<HTMLInputElement>('.sp-form input[type="text"]')!
    const detachedSave = harness.root.querySelector<HTMLButtonElement>('.sp-form-actions .btn-primary')!
    title.value = 'Must not save'
    title.dispatchEvent(new Event('input', { bubbles: true }))
    harness.readonly.value = true
    await flush()
    expect(harness.root.querySelector('.sp-form')).toBeNull()
    expect(harness.root.querySelector('.comment-textarea')).toBeNull()
    detachedSave.click()
    await flush()
    expect(api.put).not.toHaveBeenCalled()
  })

  it('fences an in-flight save across revoke, restore and a newer save', async () => {
    const staleSave = deferred<Issue>()
    vi.mocked(api.put).mockImplementationOnce(() => staleSave.promise as never)
    harness = await mountPanel()
    harness.root.querySelector<HTMLButtonElement>('[title="Quick Edit"]')!.click()
    await flush()
    let title = harness.root.querySelector<HTMLInputElement>('.sp-form input[type="text"]')!
    title.value = 'Stale unauthorized response'
    title.dispatchEvent(new Event('input', { bubbles: true }))
    harness.root.querySelector<HTMLButtonElement>('.sp-form-actions .btn-primary')!.click()
    await flush()
    expect(await harness.panel.requestLeave()).toBe(false)
    expect(harness.root.textContent).toContain('still saving')

    harness.readonly.value = true
    await flush()
    harness.readonly.value = false
    await flush()
    harness.root.querySelector<HTMLButtonElement>('[title="Quick Edit"]')!.click()
    await flush()
    title = harness.root.querySelector<HTMLInputElement>('.sp-form input[type="text"]')!
    title.value = 'Authorized newer save'
    title.dispatchEvent(new Event('input', { bubbles: true }))
    harness.root.querySelector<HTMLButtonElement>('.sp-form-actions .btn-primary')!.click()
    await flush()
    expect(harness.root.textContent).toContain('Authorized newer save')

    staleSave.resolve({ ...issue(1, '2026-08-20 14:00:00'), title: 'Stale unauthorized response' })
    await flush()
    expect(harness.root.textContent).toContain('Authorized newer save')
    expect(harness.root.textContent).not.toContain('Stale unauthorized response')
  })

  it('prevents same-ticket Save, Cancel and Quick Edit overlap while a save is pending', async () => {
    const pendingSave = deferred<Issue>()
    vi.mocked(api.put).mockImplementationOnce(() => pendingSave.promise as never)
    harness = await mountPanel()
    harness.root.querySelector<HTMLButtonElement>('[title="Quick Edit"]')!.click()
    await flush()
    const title = harness.root.querySelector<HTMLInputElement>('.sp-form input[type="text"]')!
    title.value = 'Single submitted mutation'
    title.dispatchEvent(new Event('input', { bubbles: true }))
    const quickEdit = harness.root.querySelector<HTMLButtonElement>('[title="Quick Edit"]')!
    const cancel = harness.root.querySelector<HTMLButtonElement>('.sp-form-actions .btn-ghost')!
    const save = harness.root.querySelector<HTMLButtonElement>('.sp-form-actions .btn-primary')!
    save.click()
    await flush()
    expect(cancel.disabled).toBe(true)
    expect(quickEdit.disabled).toBe(true)
    expect(save.disabled).toBe(true)

    // dispatchEvent exercises the component guards even if a test or stale
    // DOM reference bypasses native disabled-button click suppression.
    cancel.dispatchEvent(new MouseEvent('click', { bubbles: true }))
    quickEdit.dispatchEvent(new MouseEvent('click', { bubbles: true }))
    save.dispatchEvent(new MouseEvent('click', { bubbles: true }))
    await flush()
    expect(api.put).toHaveBeenCalledTimes(1)
    expect(harness.root.querySelector('.sp-form')).not.toBeNull()
    expect(harness.root.querySelector<HTMLInputElement>('.sp-form input[type="text"]')?.value)
      .toBe('Single submitted mutation')

    pendingSave.resolve({
      ...issue(1, '2026-08-20 14:00:00'),
      title: 'Single submitted mutation',
    })
    await flush()
    expect(harness.root.querySelector('.sp-form')).toBeNull()
    expect(harness.root.textContent).toContain('Single submitted mutation')
  })

  it('ignores an in-flight quick update after authority revocation', async () => {
    const staleUpdate = deferred<Issue>()
    vi.mocked(api.put).mockImplementationOnce(() => staleUpdate.promise as never)
    harness = await mountPanel()
    harness.root.querySelector<HTMLButtonElement>('.sp-meta .meta-select-trigger')!.click()
    await flush()
    option('QA').click()
    await flush()
    expect(await harness.panel.requestLeave()).toBe(false)

    harness.readonly.value = true
    await flush()
    staleUpdate.resolve({ ...issue(1, '2026-08-20 14:00:00'), status: 'qa' })
    await flush()
    expect(harness.root.textContent).toContain('In Progress')
    expect(harness.root.textContent).not.toContain('QA')
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

  it('fences and clears parent candidates across authority changes', async () => {
    const staleParents = deferred<Issue[]>()
    const currentParents = deferred<Issue[]>()
    let parentLoads = 0
    vi.mocked(api.get).mockImplementation((path: string) => {
      if (path === '/issues/1') return Promise.resolve(issue(1)) as never
      if (path === '/projects/6/issues?type=epic') {
        parentLoads += 1
        return (parentLoads === 1 ? staleParents.promise : currentParents.promise) as never
      }
      return Promise.resolve([]) as never
    })
    harness = await mountPanel()
    harness.root.querySelector<HTMLButtonElement>('[title="Quick Edit"]')!.click()
    await flush()
    expect(parentLoads).toBe(1)

    harness.readonly.value = true
    await flush()
    harness.readonly.value = false
    await flush()
    harness.root.querySelector<HTMLButtonElement>('[title="Quick Edit"]')!.click()
    await flush()
    expect(parentLoads).toBe(2)

    currentParents.resolve([{
      ...issue(91),
      type: 'epic',
      issue_key: 'PAI-91',
      title: 'Current authorized parent',
    }])
    await flush()
    staleParents.resolve([{
      ...issue(90),
      type: 'epic',
      issue_key: 'PAI-90',
      title: 'Stale revoked parent',
    }])
    await flush()

    const parentField = [...harness.root.querySelectorAll<HTMLElement>('.field')]
      .find((field) => field.querySelector('label')?.textContent?.trim() === 'Parent')!
    parentField.querySelector<HTMLButtonElement>('.meta-select-trigger')!.click()
    await flush()
    const parentOptions = document.querySelector('.meta-select-dropdown--teleported')?.textContent ?? ''
    expect(parentOptions).toContain('Current authorized parent')
    expect(parentOptions).not.toContain('Stale revoked parent')
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

  it('blocks selection/close handoff while an internal note is posting', async () => {
    const pending = deferred<never>()
    vi.mocked(api.post).mockImplementationOnce(() => pending.promise)
    harness = await mountPanel()
    const textarea = harness.root.querySelector<HTMLTextAreaElement>('.comment-textarea')!
    textarea.value = 'Pending note'
    textarea.dispatchEvent(new Event('input', { bubbles: true }))
    await flush()
    harness.root.querySelector<HTMLButtonElement>('.comment-form-actions .btn-primary')!.click()
    await flush()
    expect(await harness.panel.requestLeave()).toBe(false)
    expect(harness.root.textContent).toContain('still posting')
  })

  it('hides unsupported non-revision-safe issue mutations in Agent Mode', async () => {
    harness = await mountPanel()
    expect(harness.root.querySelector('[title="Delete issue"]')).toBeNull()
    expect(harness.root.querySelector('[title="Clone issue"]')).toBeNull()
    expect(harness.root.querySelector('.sp-time-section')).toBeNull()
    expect(harness.root.querySelector('.issue-ai-stub')).toBeNull()
  })

  it('manages current-session uploads from view mode while seeded attachments stay view-only', async () => {
    const firstUpload = deferred<Attachment>()
    const retryUpload = deferred<Attachment>()
    const progress: Array<(pct: number) => void> = []
    vi.mocked(api.get).mockImplementation(async (path: string) => {
      if (path === '/issues/1') return issue(1) as never
      if (path === '/issues/1/attachments') return [attachment(40, 'seeded.txt')] as never
      return [] as never
    })
    vi.mocked(api.upload)
      .mockImplementationOnce((_path, _body, onProgress) => {
        if (onProgress) progress.push(onProgress)
        return firstUpload.promise as never
      })
      .mockImplementationOnce((_path, _body, onProgress) => {
        if (onProgress) progress.push(onProgress)
        return retryUpload.promise as never
      })
    harness = await mountPanel()
    expect(harness.root.querySelector('.sp-form')).toBeNull()
    expect(harness.root.querySelector('[title="Open seeded.txt"]')).not.toBeNull()
    expect(harness.root.querySelector('[title="Remove seeded.txt"]')).toBeNull()

    const file = new File(['screen'], 'screen.png', { type: 'image/png' })
    harness.root.querySelector('.side-panel')!.dispatchEvent(Object.assign(
      new Event('paste', { bubbles: true, cancelable: true }),
      { clipboardData: { files: [file] } },
    ))
    await flush()
    expect(api.upload).toHaveBeenCalledTimes(1)
    expect(harness.root.querySelector('[role="progressbar"]')?.getAttribute('aria-valuenow')).toBe('0')
    progress[0](37)
    await flush()
    expect(harness.root.querySelector('[role="progressbar"]')?.getAttribute('aria-valuenow')).toBe('37')

    firstUpload.reject(new Error('network unavailable'))
    await flush()
    const retry = harness.root.querySelector<HTMLButtonElement>('[title="Retry upload of screen.png"]')!
    expect(retry).not.toBeNull()
    expect(harness.root.querySelector('[role="alert"]')?.textContent).toContain('network unavailable')
    retry.click()
    await flush()
    expect(api.upload).toHaveBeenCalledTimes(2)

    retryUpload.resolve({ ...attachment(44, 'screen.png'), content_type: 'image/png' })
    await flush()
    const remove = harness.root.querySelector<HTMLButtonElement>('[title="Remove screen.png"]')!
    expect(remove).not.toBeNull()
    remove.click()
    await flush()
    expect(api.delete).toHaveBeenCalledWith('/attachments/44')
    expect(harness.root.textContent).not.toContain('screen.png')
    expect(harness.root.textContent).toContain('seeded.txt')
  })

  it('clears pending and failed jobs on capability revoke and detached controls no-op', async () => {
    const pendingUpload = deferred<Attachment>()
    vi.mocked(api.upload)
      .mockImplementationOnce(() => pendingUpload.promise as never)
      .mockRejectedValueOnce(new Error('upload failed'))
    harness = await mountPanel()
    const panel = harness.root.querySelector('.side-panel')!

    panel.dispatchEvent(Object.assign(
      new Event('paste', { bubbles: true, cancelable: true }),
      { clipboardData: { files: [new File(['x'], 'pending.png', { type: 'image/png' })] } },
    ))
    await flush()
    const pendingRemove = harness.root.querySelector<HTMLButtonElement>('[title="Remove pending.png"]')!
    expect(pendingRemove).not.toBeNull()
    harness.allowAttachments.value = false
    await flush()
    expect(harness.root.textContent).not.toContain('pending.png')
    const revokeCallsAfterPending = vi.mocked(URL.revokeObjectURL).mock.calls.length
    pendingRemove.click()
    await flush()
    expect(api.upload).toHaveBeenCalledTimes(1)
    expect(URL.revokeObjectURL).toHaveBeenCalledTimes(revokeCallsAfterPending)

    harness.allowAttachments.value = true
    await flush()
    panel.dispatchEvent(Object.assign(
      new Event('drop', { bubbles: true, cancelable: true }),
      { dataTransfer: { files: [new File(['x'], 'failed.png', { type: 'image/png' })], types: ['Files'] } },
    ))
    await flush()
    const failedRetry = harness.root.querySelector<HTMLButtonElement>('[title="Retry upload of failed.png"]')!
    const failedRemove = harness.root.querySelector<HTMLButtonElement>('[title="Remove failed.png"]')!
    expect(failedRetry).not.toBeNull()
    expect(failedRemove).not.toBeNull()
    harness.allowAttachments.value = false
    await flush()
    expect(harness.root.textContent).not.toContain('failed.png')
    const revokeCallsAfterFailure = vi.mocked(URL.revokeObjectURL).mock.calls.length
    failedRetry.click()
    failedRemove.click()
    await flush()
    expect(api.upload).toHaveBeenCalledTimes(2)
    expect(api.delete).not.toHaveBeenCalled()
    expect(URL.revokeObjectURL).toHaveBeenCalledTimes(revokeCallsAfterFailure)
  })

  it('keeps only the newest same-issue attachment load', async () => {
    const older = deferred<Attachment[]>()
    const newer = deferred<Attachment[]>()
    let attachmentLoads = 0
    vi.mocked(api.get).mockImplementation((path: string) => {
      if (path === '/issues/1') return Promise.resolve(issue(1)) as never
      if (path === '/issues/1/attachments') {
        attachmentLoads += 1
        return (attachmentLoads === 1 ? older.promise : newer.promise) as never
      }
      return Promise.resolve([]) as never
    })
    harness = await mountPanel()
    harness.allowAttachments.value = false
    await flush()
    expect(attachmentLoads).toBe(2)

    newer.resolve([attachment(42, 'newest.txt')])
    await flush()
    older.resolve([attachment(41, 'stale.txt')])
    await flush()
    expect(harness.root.textContent).toContain('newest.txt')
    expect(harness.root.textContent).not.toContain('stale.txt')
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
