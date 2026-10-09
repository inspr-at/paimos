// SPDX-License-Identifier: AGPL-3.0-only
// AEON-1057 risk: a project link that points to the wrong place, a colour that
// changes between screens, or a "Now in …" notice for an item in the same project.
import { afterEach, describe, expect, it, vi } from 'vitest'
import { deskProject, draftFor, projectColor, projectHue, projectSwitch, type DeskProjection } from '../src/lib/decisionDesk'
import { commitDesk, emptySources, loadDesk, questionItem, type Question } from '../src/lib/decisionDeskApi'

afterEach(() => vi.unstubAllGlobals())

describe('Decision Desk project identity', () => {
  it('links a project by key, keeps one hue per key and leaves workspace items neutral', () => {
    const orbit = deskProject({ projectId: 'p-orbit', projectName: 'Orbit', projectKey: 'ORB 1' })
    expect(orbit).toMatchObject({ id: 'p-orbit', key: 'ORB 1', name: 'Orbit', href: '/p/ORB%201' })
    expect(orbit.hue).toBe(projectHue('ORB 1'))
    expect(deskProject({ projectId: 'p-orbit', projectName: 'Orbit (renamed)', projectKey: 'ORB 1' }).hue).toBe(orbit.hue)
    expect([190, 75, 290, 12, 150, 240, 45, 335]).toContain(orbit.hue)
    expect(projectColor(orbit)).toBe(`oklch(var(--project-l) var(--project-c) ${orbit.hue})`)
    const workspace = deskProject({ projectId: '', projectName: 'Workspace', projectKey: 'IGNORED' })
    expect(workspace).toMatchObject({ key: '', href: '', hue: null })
    expect(projectColor(workspace)).toBe('var(--ink-3)')
  })

  it('names the previous project only when the project changed', () => {
    const orbit = deskProject({ projectId: 'p-orbit', projectName: 'Orbit', projectKey: 'ORB' })
    const harbor = deskProject({ projectId: 'p-harbor', projectName: 'Harbor', projectKey: 'HBR' })
    expect(projectSwitch(undefined, orbit)).toBeUndefined()
    expect(projectSwitch(orbit, { ...orbit, name: 'Orbit' })).toBeUndefined()
    expect(projectSwitch(orbit, harbor)).toBe(orbit)
  })

  it('keeps the project link and colour on a question or handover after deciding', async () => {
    for (const source_handover_id of [undefined, 'handover']) {
      const question: Question = { id: 'question', project_id: 'p-orbit', revision: 1, state: 'open', suggested_outcome: 'once', suggestion_reason: 'ticket_default',
        input: { request_id: 'request', question: 'Continue?', options: [{ id: 'yes', title: 'Continue', description: '', answer: 'Continue.' }], meanwhile: 'parked', source_handover_id },
        askers: [], pending: [], created_at: '', updated_at: '' }
      const item = { ...questionItem(question, 'Orbit'), projectKey: 'ORB' }, sources = emptySources()
      sources.questions.set(item.id, question)
      vi.stubGlobal('fetch', vi.fn(async () => Response.json({ ...question, revision: 2, state: 'answered', answer: {
        id: 'answer', revision: 2, answer: 'Continue.', option_id: 'yes', outcome: 'once', decided_by: 'person', created_at: '', deliver_after: '',
      } })))
      const decided = await commitDesk(item, { ...draftFor(item), optionId: 'yes' }, sources, 'decision', false)
      expect(decided.decided).toBe(true)
      expect(decided.revision).toBe(2)
      expect(deskProject(decided)).toEqual(deskProject(item))
      expect(deskProject(decided).href).toBe('/p/ORB')
    }
  })

  it('keeps the canonical run approval project and resolves run approval history through its work order', async () => {
    const approval = { agent_principal_id: 'agent', scope: 'run.claim', resource_kind: 'run', rationale: 'Continue the run', proposed_at: '', expires_at: '2999-01-01T00:00:00Z' }
    const projection: DeskProjection = { items: [{ id: 'open', kind: 'approval', project_id: 'p-orbit', revision: 1, title: 'Run approval', held: true, created_at: '', href: '/decision-desk?item=a:open', source: '/api/approvals/open' }],
      counts: { open: 1, held: 1, chores: 0 }, has_more: false, as_of: '' }
    const reads: string[] = []
    vi.stubGlobal('fetch', vi.fn(async (path: string) => {
      const url = new URL(path, 'https://test.invalid'); reads.push(url.pathname)
      if (url.pathname === '/api/projects') return Response.json({ items: [{ id: 'p-orbit', key: 'ORB', title: 'Orbit' }] })
      if (url.pathname === '/api/approvals') return Response.json([
        { ...approval, id: 'open', resource_id: 'run-open', decision: null },
        { ...approval, id: 'history', resource_id: 'run-history', decision: 'approved' },
      ])
      if (url.pathname === '/api/nodes/work-order') return Response.json({ id: 'work-order', parent_id: 'p-orbit' })
      if (url.pathname === '/api/decision-desk' || url.pathname === '/api/rules/doctrine/inbox' || url.pathname === '/api/key-trim-proposals' || url.pathname === '/api/projects/p-orbit/messages') return Response.json({ items: [], has_more: false, pending: 0 })
      throw new Error(`Unexpected read: ${path}`)
    }))
    const approvalRun = vi.fn(async (id: string) => { reads.push(`/api/runs/${id}`); return { id, work_order_id: 'work-order' } as never })
    const result = await loadDesk(undefined, { approvalRun }, projection)
    for (const id of ['a:open', 'a:history']) {
      const item = result.items.find(item => item.id === id)!
      expect(deskProject(item)).toMatchObject({ id: 'p-orbit', key: 'ORB', name: 'Orbit', href: '/p/ORB' })
      expect(item.unavailable).toBeUndefined()
    }
    expect(result.items.find(item => item.id === 'a:history')!.decided).toBe(true)
    expect(reads).toContain('/api/runs/run-history')
    expect(reads).not.toContain('/api/runs/run-open')
  })
})
