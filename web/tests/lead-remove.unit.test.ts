// SPDX-License-Identifier: AGPL-3.0-only
// Risk: a confirmation removes a changed lead/viewer, or failed removal looks successful.
import { afterEach, expect, it, vi } from 'vitest'
import { reactive } from 'vue'
import * as leadAPI from '../src/lib/lead'
import { deferred, flush, setupSource } from './record-source'

const scopes: { stop(): void }[] = []
afterEach(() => scopes.splice(0).forEach(scope => scope.stop()))
function menu() {
  const props = reactive({ projectId: 'p1', projectKey: 'AIT', lead: { project_id: 'p1', revision: 2, generation: 0, session_id: null, state: 'paused', reason: '', process_active: false } as leadAPI.ProjectLead })
  const session = reactive({ identity: { tenant: { id: 't1' }, principal: { id: 'person1', kind: 'person' } } })
  const remove = vi.fn(), permission = reactive({ allowed: true })
  const component = setupSource('components/lead/LeadMenu.vue', props, {
    'vue-router': { useRouter: () => ({ push: vi.fn() }) },
    '../../lib/authz': { can: () => permission.allowed },
    '../../lib/lead': leadAPI,
    '../../stores/session': { useSession: () => session },
    '../../stores/projectLeads': { useProjectLeads: () => ({ busy: {}, remove }) },
  })
  scopes.push(component)
  return { ...component, props, session, permission, remove }
}

it('AEON-1040: inline confirmation cancels, binds the shown revision and reports a failed removal', async () => {
  const c = menu()
  expect(c.state.removable.value).toBe(true)
  await c.state.remove()
  expect(c.remove).not.toHaveBeenCalled()
  c.state.cancel()
  expect(c.state.confirmation.value).toBeNull()
  await c.state.remove()
  c.remove.mockRejectedValueOnce(new Error('lead revision conflict'))
  await c.state.remove()
  expect(c.remove).toHaveBeenCalledWith('p1', 2)
  expect(c.state.error.value).toBe('lead revision conflict')
  expect(c.state.confirmation.value).not.toBeNull()
  c.props.lead = { ...c.props.lead }; await flush()
  expect(c.state.error.value).toBe('lead revision conflict')
  expect(c.state.confirmation.value).not.toBeNull()
  c.remove.mockResolvedValueOnce({ state: 'none' })
  await c.state.remove()
  expect(c.state.confirmation.value).toBeNull()
})

it('AEON-1040: project, revision, viewer and authority changes invalidate removal and stale errors', async () => {
  const c = menu()
  for (const change of [() => { c.props.lead.revision++ }, () => { c.props.projectId = 'p2' }, () => { c.session.identity.principal.id = 'person2' }, () => { c.permission.allowed = false }]) {
    await c.state.remove()
    expect(c.state.confirmation.value).not.toBeNull()
    change(); await flush()
    expect(c.state.confirmation.value).toBeNull()
    expect(c.remove).not.toHaveBeenCalled()
  }
  c.permission.allowed = true; await flush()
  await c.state.remove()
  const writing = deferred()
  c.remove.mockReturnValueOnce(writing.promise)
  const result = c.state.remove()
  c.session.identity.principal.id = 'person3'; await flush()
  writing.reject(new Error('old viewer error'))
  await result
  expect(c.state.error.value).toBe('')
  c.props.lead.generation = 1
  expect(c.state.removable.value).toBe(false)
  c.props.lead.generation = 0; c.props.lead.session_id = 's1'
  expect(c.state.removable.value).toBe(false)
})
