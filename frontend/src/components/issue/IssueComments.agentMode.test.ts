/*
 * PAIMOS — Your Professional & Personal AI Project OS
 * Copyright (C) 2026 Markus Barta <markus@barta.com>
 * AGPL-3.0-only — see LICENSE.
 */
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { createApp, h, nextTick, ref } from 'vue'
import { createPinia, setActivePinia } from 'pinia'

vi.mock('@/services/issueComments', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/services/issueComments')>()
  return {
    ...actual,
    loadIssueComments: vi.fn(),
    createIssueComment: vi.fn(),
    deleteIssueComment: vi.fn(),
    updateIssueCommentVisibility: vi.fn(),
  }
})

import i18n from '@/i18n'
import {
  createIssueComment,
  loadIssueComments,
  type IssueComment,
} from '@/services/issueComments'
import IssueComments from './IssueComments.vue'

function deferred<T>() {
  let resolve!: (value: T) => void
  const promise = new Promise<T>((res) => { resolve = res })
  return { promise, resolve }
}

function comment(id: number, issueId: number, body: string): IssueComment {
  return {
    id,
    issue_id: issueId,
    author_id: 1,
    author: 'mba',
    avatar_path: null,
    body,
    visibility: 'internal',
    created_at: '2026-08-20 12:00:00',
  }
}

async function flush(times = 12) {
  for (let i = 0; i < times; i += 1) {
    await Promise.resolve()
    await nextTick()
  }
}

function mountComments() {
  const root = document.createElement('div')
  document.body.appendChild(root)
  const issueId = ref(1)
  const canEdit = ref(true)
  const inFlight = ref(false)
  const pinia = createPinia()
  setActivePinia(pinia)
  const app = createApp({
    render: () => h(IssueComments, {
      issueId: issueId.value,
      canEdit: canEdit.value,
      mdMode: false,
      isMonospace: false,
      internalOnly: true,
      onInFlightChange: (value: boolean) => { inFlight.value = value },
    }),
  })
  app.use(pinia)
  app.use(i18n)
  app.mount(root)
  return { root, issueId, canEdit, inFlight, unmount: () => { app.unmount(); root.remove() } }
}

async function writeAndPost(root: HTMLElement, body: string) {
  const textarea = root.querySelector<HTMLTextAreaElement>('.comment-textarea')!
  textarea.value = body
  textarea.dispatchEvent(new Event('input', { bubbles: true }))
  await flush()
  root.querySelector<HTMLButtonElement>('.comment-form-actions .btn-primary')!.click()
}

describe('IssueComments Agent Mode identity fences', () => {
  beforeEach(() => {
    vi.mocked(loadIssueComments).mockReset().mockResolvedValue([])
    vi.mocked(createIssueComment).mockReset()
  })

  afterEach(() => {
    document.body.innerHTML = ''
  })

  it('does not render an A response in B or clear B draft after forced identity change', async () => {
    const pendingA = deferred<IssueComment>()
    const pendingB = deferred<IssueComment>()
    vi.mocked(createIssueComment)
      .mockImplementationOnce(() => pendingA.promise)
      .mockImplementationOnce(() => pendingB.promise)
    const harness = mountComments()
    await flush()
    await writeAndPost(harness.root, 'Note for A')
    await flush()
    expect(harness.inFlight.value).toBe(true)

    harness.issueId.value = 2
    await flush()
    await writeAndPost(harness.root, 'Draft for B')
    const textareaB = harness.root.querySelector<HTMLTextAreaElement>('.comment-textarea')!
    pendingA.resolve(comment(10, 1, 'Note for A'))
    await flush()

    expect(textareaB.value).toBe('Draft for B')
    expect(harness.root.textContent).not.toContain('Note for A')
    expect(harness.inFlight.value).toBe(true)

    pendingB.resolve(comment(11, 2, 'Draft for B'))
    await flush()
    expect(harness.inFlight.value).toBe(false)
    expect(harness.root.textContent).toContain('Draft for B')
    harness.unmount()
  })

  it('keeps a newer post busy when an invalidated response settles after revoke and restore', async () => {
    const pendingOld = deferred<IssueComment>()
    const pendingNew = deferred<IssueComment>()
    vi.mocked(createIssueComment)
      .mockImplementationOnce(() => pendingOld.promise)
      .mockImplementationOnce(() => pendingNew.promise)
    const harness = mountComments()
    await flush()
    await writeAndPost(harness.root, 'Old note')
    await flush()

    harness.canEdit.value = false
    await flush()
    expect(harness.root.querySelector('.comment-textarea')).toBeNull()
    harness.canEdit.value = true
    await flush()
    await writeAndPost(harness.root, 'New authorized note')
    await flush()
    expect(harness.inFlight.value).toBe(true)

    pendingOld.resolve(comment(10, 1, 'Old note'))
    await flush()
    expect(harness.inFlight.value).toBe(true)
    expect(harness.root.querySelector<HTMLButtonElement>('.comment-form-actions .btn-primary')!.disabled).toBe(true)

    pendingNew.resolve(comment(11, 1, 'New authorized note'))
    await flush()
    expect(harness.inFlight.value).toBe(false)
    expect(harness.root.textContent).toContain('New authorized note')
    expect(harness.root.textContent).not.toContain('Old note')
    harness.unmount()
  })
})
