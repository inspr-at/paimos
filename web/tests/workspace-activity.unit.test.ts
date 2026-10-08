// SPDX-License-Identifier: AGPL-3.0-only
import { effectScope, nextTick, reactive, ref } from 'vue'
import { expect, it, vi } from 'vitest'
import { useWorkspaceActivity } from '../src/lib/useWorkspaceActivity'
import type { WorkspaceActivityFilters, WorkspaceActivityItem, WorkspaceActivityPage } from '../src/lib/workspaceActivity'

const calls = vi.hoisted(() => ({ read: vi.fn(), undo: vi.fn() }))
const session = reactive({ identity: { tenant: { id: 'tenant' }, principal: { id: 'ada' } }, authenticationCurrent: () => true })
vi.mock('../src/stores/session', () => ({ useSession: () => session }))
vi.mock('../src/lib/api', () => ({ api: vi.fn() }))
vi.mock('../src/lib/authz', () => ({ onAccessChange: () => () => {} }))
vi.mock('../src/lib/workspaceActivity', async importOriginal => ({ ...await importOriginal<object>(), getWorkspaceActivity: calls.read, undoActivity: calls.undo }))

function deferred<T>() {
  let resolve!: (value: T) => void
  const promise = new Promise<T>(done => { resolve = done })
  return { promise, resolve }
}
const item = (revision: string): WorkspaceActivityItem => ({ event_id: 7, node_id: 'ticket', project_id: 'project', key: 'TEST-7', title: 'Visible ticket', actor: 'Status autopilot', type: 'status_autopilot.changed', rule: 'progress', reason: 'No active work', from: 'in_progress', to: 'open', at: revision, revision, automatic: true, undone: false, changed_since: false, undoable: true, requires_preview: false })
const page = (revision: string): WorkspaceActivityPage => ({ items: [item(revision)], next_cursor: null })

it('drops another person’s history and binds every delayed Undo result to the exact displayed event, ticket and revision', async () => {
  const previous = deferred<WorkspaceActivityPage>(), current = deferred<WorkspaceActivityPage>()
  calls.read.mockReturnValueOnce(previous.promise).mockReturnValueOnce(current.promise)
  const filters = ref<WorkspaceActivityFilters>({ view: 'automatic', rule: '', project_id: '', q: '' })
  const scope = effectScope(), activity = scope.run(() => useWorkspaceActivity(filters))!
  session.identity.principal.id = 'grace'
  current.resolve(page('current')); previous.resolve(page('previous-person'))
  await Promise.all([previous.promise, current.promise]); await nextTick()
  expect(activity.items.value[0]?.revision).toBe('current')
  const changedRevision = deferred<void>()
  calls.undo.mockReturnValueOnce(changedRevision.promise)
  const undo = activity.undo(activity.items.value[0]!)
  expect(calls.undo.mock.calls[0]![0]).toEqual({ event_id: 7, node_id: 'ticket', revision: 'current' })
  activity.items.value = [item('new-revision')]
  changedRevision.resolve(); await undo
  expect(activity.items.value[0]?.undone).toBe(false)
  const otherPerson = deferred<void>()
  calls.undo.mockReturnValueOnce(otherPerson.promise)
  const oldUndo = activity.undo(activity.items.value[0]!)
  calls.read.mockResolvedValueOnce(page('next-person'))
  session.identity.principal.id = 'lin'
  await nextTick()
  otherPerson.resolve(); await oldUndo; await nextTick()
  expect(activity.items.value[0]?.revision).toBe('next-person')
  expect(activity.items.value[0]?.undone).toBe(false)
  expect(activity.pending.value.size).toBe(0)
  expect(activity.rowErrors.value).toEqual({})
  scope.stop()
})
