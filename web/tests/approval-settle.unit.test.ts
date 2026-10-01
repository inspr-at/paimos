// SPDX-License-Identifier: AGPL-3.0-only
// AEON-505: a decision confirms on its card only after the server answered, then folds.
import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import type { Approval } from '../src/lib/agents'
import { COLLAPSE_MS, SUCCESS_MS, createApprovalSettle, settledAnnouncement, settledLine } from '../src/components/agents/approvalSettle'

const approval = (id = 'a-1'): Approval => ({
  id, agent_principal_id: 'agent', agent_name: 'Harbor Clerk', scope: 'nodes.write', resource_kind: 'node', resource_id: 'n-1', rationale: 'Record findings.',
  expires_at: new Date(Date.now() + 60_000).toISOString(), proposed_at: new Date().toISOString(), decision: null,
})
function deferred<T>() {
  let resolve!: (value: T) => void, reject!: (error: unknown) => void
  const promise = new Promise<T>((yes, no) => { resolve = yes; reject = no })
  return { promise, resolve, reject }
}
beforeEach(() => { vi.useFakeTimers() })
afterEach(() => { vi.useRealTimers() })

it('moves pending → confirming → success → collapsing → collapsed, with success only after the server', async () => {
  const answer = deferred<Approval>()
  const confirmed = vi.fn(), settled = vi.fn()
  const flow = createApprovalSettle({ decide: () => answer.promise, measure: () => 184, onConfirmed: confirmed, onSettled: settled })
  const item = approval()
  expect(flow.phase(item.id)).toBe('pending')
  const submitted = flow.submit(item, 'approved', 'Fine for this run.')
  expect(flow.phase(item.id)).toBe('confirming')
  expect(flow.active.value).toBe(true)
  expect(flow.holding(item.id)).toBe(true)
  // However long the server takes, nothing claims success before it answers.
  await vi.advanceTimersByTimeAsync(SUCCESS_MS * 3)
  expect(flow.phase(item.id)).toBe('confirming')
  expect(confirmed).not.toHaveBeenCalled()
  answer.resolve({ ...item, decision: 'approved' })
  await submitted
  expect(flow.phase(item.id)).toBe('success')
  expect(flow.entries.get(item.id)).toMatchObject({ decision: 'approved', reason: 'Fine for this run.', height: 184 })
  expect(confirmed).toHaveBeenCalledOnce()
  await vi.advanceTimersByTimeAsync(SUCCESS_MS - 1)
  expect(flow.phase(item.id)).toBe('success')
  await vi.advanceTimersByTimeAsync(1)
  expect(flow.phase(item.id)).toBe('collapsing')
  // Decided takes it as the fold begins.
  expect(flow.holding(item.id)).toBe(false)
  await vi.advanceTimersByTimeAsync(COLLAPSE_MS)
  expect(flow.phase(item.id)).toBe('collapsed')
  expect(flow.active.value).toBe(false)
  expect(settled).toHaveBeenCalledOnce()
})

it('a failed call returns the card to pending, actionable, and passes the error on', async () => {
  const confirmed = vi.fn()
  const flow = createApprovalSettle({ decide: () => Promise.reject(new Error('The request expired while you were deciding')), onConfirmed: confirmed })
  const item = approval()
  await expect(flow.submit(item, 'denied', 'Use the staging account instead.')).rejects.toThrow('The request expired while you were deciding')
  expect(flow.phase(item.id)).toBe('pending')
  expect(flow.active.value).toBe(false)
  expect(confirmed).not.toHaveBeenCalled()
  await vi.advanceTimersByTimeAsync(SUCCESS_MS + COLLAPSE_MS)
  expect(flow.phase(item.id)).toBe('pending')
  // It can be decided again.
  const retry = createApprovalSettle({ decide: async request => ({ ...request, decision: 'denied' as const }) })
  await retry.submit(item, 'denied', '')
  expect(retry.phase(item.id)).toBe('success')
})

it('with reduced motion the success state is replaced without a fold', async () => {
  const flow = createApprovalSettle({ decide: async request => ({ ...request, decision: 'denied' as const }), reducedMotion: () => true })
  const item = approval()
  await flow.submit(item, 'denied', '')
  expect(flow.phase(item.id)).toBe('success')
  await vi.advanceTimersByTimeAsync(SUCCESS_MS)
  expect(flow.phase(item.id)).toBe('collapsed')
})

it('shows the server’s decision, not the one asked for, and ignores a second submit in flight', async () => {
  const answer = deferred<Approval>()
  const decide = vi.fn(() => answer.promise)
  const flow = createApprovalSettle({ decide })
  const item = approval()
  const first = flow.submit(item, 'approved', '')
  await flow.submit(item, 'approved', '')
  expect(decide).toHaveBeenCalledOnce()
  answer.resolve({ ...item, decision: 'denied' })
  await first
  expect(flow.entries.get(item.id)?.decision).toBe('denied')
  expect(flow.entries.get(item.id)?.reason).toBeUndefined()
})

it('stop cancels the timers of a card that is still showing', async () => {
  const flow = createApprovalSettle({ decide: async request => ({ ...request, decision: 'approved' as const }) })
  const item = approval()
  await flow.submit(item, 'approved', '')
  flow.stop()
  await vi.advanceTimersByTimeAsync(SUCCESS_MS + COLLAPSE_MS)
  expect(flow.phase(item.id)).toBe('success')
})

it('says the outcome with the human-readable scope', () => {
  expect(settledLine('approved', 'Harbor Clerk', 'nodes.write')).toBe('Harbor Clerk may change tickets')
  expect(settledLine('denied', 'Harbor Clerk', 'nodes.write')).toBe('Harbor Clerk may not change tickets')
  expect(settledLine('approved', 'nova', 'harness.control')).toBe('nova may interrupt or stop agent sessions')
  expect(settledAnnouncement('approved', 'Harbor Clerk', 'nodes.write', 'Fine for this run.')).toBe('Approved. Harbor Clerk may change tickets. Reason: Fine for this run.')
  expect(settledAnnouncement('denied', 'Harbor Clerk', 'nodes.write')).toBe('Denied. Harbor Clerk may not change tickets.')
})
