import { beforeEach, describe, expect, it, vi } from 'vitest'

vi.mock('@/api/client', () => ({
  ApiError: class ApiError extends Error {
    constructor(public status: number, message: string) { super(message) }
  },
  api: { get: vi.fn(), post: vi.fn(), delete: vi.fn(), patch: vi.fn() },
}))

import { api } from '@/api/client'
import {
  createIssueComment,
  deleteIssueComment,
  loadIssueComments,
  updateIssueCommentVisibility,
} from './issueComments'

describe('issueComments service', () => {
  beforeEach(() => vi.clearAllMocks())

  it('loads comments', async () => {
    vi.mocked(api.get).mockResolvedValue([{ id: 1 }] as never)
    const comments = await loadIssueComments(9)
    expect(comments).toHaveLength(1)
    expect(api.get).toHaveBeenCalledWith('/issues/9/comments')
  })

  it('creates with default internal visibility', async () => {
    vi.mocked(api.post).mockResolvedValue({ id: 2 } as never)
    await createIssueComment(9, 'hello')
    expect(api.post).toHaveBeenCalledWith('/issues/9/comments', {
      body: 'hello',
      visibility: 'internal',
    })
  })

  it('creates with explicit external visibility', async () => {
    vi.mocked(api.post).mockResolvedValue({ id: 2 } as never)
    await createIssueComment(9, 'visible to customer', 'external')
    expect(api.post).toHaveBeenCalledWith('/issues/9/comments', {
      body: 'visible to customer',
      visibility: 'external',
    })
  })

  it('adds the exact-once key only when explicitly requested', async () => {
    vi.mocked(api.post).mockResolvedValue({ id: 3 } as never)
    const signal = new AbortController().signal
    await createIssueComment(9, 'private voice note', 'internal', {
      clientRequestId: 'amv1-0123456789abcdef',
      signal,
    })
    expect(api.post).toHaveBeenCalledWith('/issues/9/comments', {
      body: 'private voice note',
      visibility: 'internal',
      client_request_id: 'amv1-0123456789abcdef',
    }, { signal })
  })

  it('rejects an exact-once key on an external comment before transport', () => {
    expect(() => createIssueComment(9, 'must stay private', 'external', {
      clientRequestId: 'amv1-0123456789abcdef',
    })).toThrow('only valid for internal')
    expect(api.post).not.toHaveBeenCalled()
  })

  it('flips visibility via PATCH', async () => {
    vi.mocked(api.patch).mockResolvedValue({ id: 7, visibility: 'external' } as never)
    const out = await updateIssueCommentVisibility(7, 'external')
    expect(api.patch).toHaveBeenCalledWith('/comments/7', { visibility: 'external' })
    expect(out.visibility).toBe('external')
  })

  it('deletes a comment', async () => {
    vi.mocked(api.delete).mockResolvedValue(undefined as never)
    await deleteIssueComment(2)
    expect(api.delete).toHaveBeenCalledWith('/comments/2')
  })
})
