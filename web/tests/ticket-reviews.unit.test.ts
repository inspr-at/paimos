// SPDX-License-Identifier: AGPL-3.0-only
import { expect, it, vi } from 'vitest'
import { reactive } from 'vue'
import { APIError } from '../src/lib/api'
import * as reviews from '../src/lib/reviews'
import { deferred, flush, setupSource } from './record-source'

const base = 'a'.repeat(40), head = 'b'.repeat(40)
function setup(requestReview = vi.fn()) {
  const grants = reactive({ read: true, write: true, run: true })
  const props = reactive({ nodeId: 'A', projectId: 'project-a' })
  const session = reactive({ identity: { tenant: { id: 'tenant-a' }, principal: { id: 'person-a' } } })
  const component = setupSource('components/work/TicketReviews.vue', props, {
    '../../lib/api': { APIError }, '../../lib/reviews': { ...reviews, requestReview, listReviews: async () => [] },
    '../../lib/authz': { can: (permission: string) => ({ 'work_orders.read': grants.read, 'work_orders.write': grants.write, 'run.create': grants.run })[permission], onAccessRevalidate: () => () => {} },
    '../../lib/sharedRequests': { createSharedRequests: () => ({ watch: () => () => {}, stop() {} }) },
    '../../lib/agentProjection': { eventNode: () => undefined },
    '../../lib/identityScope': { scopeOwner: (identity: typeof session.identity) => `${identity.tenant.id}/${identity.principal.id}` },
    '../../stores/session': { useSession: () => session },
  })
  const state = component.state
  const fill = () => { state.repository.value = 'example/repository'; state.base.value = base; state.head.value = head; state.authorFamily.value = 'openai'; state.authorRun.value = ''; state.pullRequest.value = '' }
  fill()
  return { ...component, state, props, grants, session, fill, requestReview }
}

it('P03: review readiness requires full distinct commits, repository, author and both write grants', async () => {
  const made = setup(vi.fn(async () => ({ work_order_id: 'review-a' })))
  try {
    const { state, fill, grants, requestReview } = made
    const invalid = [
      () => { state.repository.value = 'not a repository' },
      () => { state.base.value = 'abc123' },
      () => { state.head.value = base },
      () => { state.head.value = 'B'.repeat(40) },
      () => { state.authorFamily.value = '' },
      () => { state.pullRequest.value = '0' },
      () => { state.pullRequest.value = '1.5' },
    ]
    for (const change of invalid) {
      fill(); change()
      expect(state.valid.value).toBeFalsy()
      await state.submit()
      expect(requestReview).not.toHaveBeenCalled()
    }
    fill()
    for (const permission of ['write', 'run'] as const) {
      grants[permission] = false
      expect(state.write.value).toBe(false)
      await state.submit()
      expect(requestReview).not.toHaveBeenCalled()
      grants[permission] = true
    }
    state.base.value = 'c'.repeat(64); state.head.value = 'd'.repeat(64)
    state.authorFamily.value = ''; state.authorRun.value = 'author-run'; state.pullRequest.value = '42'
    expect(state.valid.value).toBeTruthy()
    await state.submit()
    expect(requestReview).toHaveBeenCalledExactlyOnceWith('A', expect.objectContaining({ base_sha: 'c'.repeat(64), head_sha: 'd'.repeat(64), author_run_id: 'author-run', pull_request: 42 }), expect.any(AbortSignal))
    expect(requestReview.mock.calls[0][1]).not.toHaveProperty('author_family')
  } finally { made.stop() }
})

it('P03: uncertain review retries reuse the exact request while edited requests get a fresh id', async () => {
  const requestReview = vi.fn().mockRejectedValue(new Error('temporarily unavailable'))
  const made = setup(requestReview)
  try {
    await made.state.submit(); await made.state.submit()
    const first = requestReview.mock.calls[0][1], retry = requestReview.mock.calls[1][1]
    expect(retry).toEqual(first)
    expect(first).toMatchObject({ repository: 'example/repository', base_sha: base, head_sha: head, author_family: 'openai' })
    expect(first.request_id).toMatch(/^[a-f0-9-]{36}$/)
    expect(made.state.error.value).toBe('temporarily unavailable')
    expect(made.state.sending.value).toBe(false)
    made.state.head.value = 'c'.repeat(40)
    await made.state.submit()
    const edited = requestReview.mock.calls[2][1]
    expect(edited.request_id).not.toBe(first.request_id)
    expect(edited.head_sha).toBe('c'.repeat(40))
  } finally { made.stop() }
})

it('P03: late review results cannot cross ticket, project, person, tenant or unmount boundaries', async () => {
  for (const boundary of ['ticket', 'project', 'person', 'tenant', 'unmount']) for (const fails of [false, true]) {
    const pending = deferred<unknown>(), requestReview = vi.fn(() => pending.promise)
    const made = setup(requestReview)
    try {
      const sending = made.state.submit()
      expect(made.state.sending.value).toBe(true)
      const signal = requestReview.mock.calls[0][2]
      if (boundary === 'ticket') made.props.nodeId = 'B'
      if (boundary === 'project') made.props.projectId = 'project-b'
      if (boundary === 'person') made.session.identity.principal.id = 'person-b'
      if (boundary === 'tenant') made.session.identity.tenant.id = 'tenant-b'
      if (boundary === 'unmount') made.stop()
      await flush()
      expect(signal.aborted).toBe(true)
      if (boundary !== 'unmount') { made.fill(); made.state.repository.value = 'new-owner/repository' }
      if (fails) pending.reject(new Error('old owner failure'))
      else pending.resolve({ work_order_id: 'old-owner-review' })
      await sending
      expect(made.state.reviews.value).toEqual([])
      expect(made.state.error.value).toBe('')
      if (boundary !== 'unmount') {
        expect(made.state.repository.value).toBe('new-owner/repository')
        expect(made.state.sending.value).toBe(false)
      }
    } finally { made.stop() }
  }
})
