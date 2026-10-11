// SPDX-License-Identifier: AGPL-3.0-only
// AEON-1195: R18 controls/rows must survive focus; R2/R16 old-person reads
// must not repopulate a reset. Deferred reads establish the interleaving.
import { afterEach, expect, it, vi } from 'vitest'
import { createPinia, disposePinia, setActivePinia } from 'pinia'
import { reactive } from 'vue'
import { readFileSync, readdirSync } from 'node:fs'
import ts from 'typescript'
import { accessChanged, clearPermissions, revokePermissions } from '../src/lib/authz'
import { useProjectLeads } from '../src/stores/projectLeads'
import { useWorkQueue } from '../src/stores/workQueue'
import { useDecisionDesk } from '../src/stores/decisionDesk'
import { useCapacity } from '../src/stores/capacity'
import { readLead, readLeadDecisions, readOpenQuestions } from '../src/lib/lead'
import { readQueue } from '../src/lib/workQueue'
import { loadDeskProjection } from '../src/lib/decisionDesk'
import { defaultSchedule, listCapacity, listSchedules } from '../src/lib/capacity'
import { listPairingComputers } from '../src/lib/agentPairing'

const state = vi.hoisted(() => ({ session: { identity: { tenant: { id: 't' }, principal: { id: 'person', kind: 'person' } }, authenticationCurrent: () => true } }))
vi.mock('../src/stores/session', () => ({ useSession: () => state.session }))
vi.mock('../src/stores/agents', () => ({ useAgents: () => ({ accounts: [], now: 0, onWrite: () => () => {} }) }))
vi.mock('../src/lib/preferences', () => ({ usePreference: () => ({ value: { value: null }, save: vi.fn() }) }))
vi.mock('../src/lib/lead', async original => ({ ...await original<typeof import('../src/lib/lead')>(), readLead: vi.fn(), readLeadDecisions: vi.fn(), readOpenQuestions: vi.fn() }))
vi.mock('../src/lib/workQueue', async original => ({ ...await original<typeof import('../src/lib/workQueue')>(), readQueue: vi.fn() }))
vi.mock('../src/lib/decisionDesk', async original => ({ ...await original<typeof import('../src/lib/decisionDesk')>(), loadDeskProjection: vi.fn() }))
vi.mock('../src/lib/capacity', async original => ({ ...await original<typeof import('../src/lib/capacity')>(), listCapacity: vi.fn(), listSchedules: vi.fn() }))
vi.mock('../src/lib/agentPairing', async original => ({ ...await original<typeof import('../src/lib/agentPairing')>(), listPairingComputers: vi.fn() }))
const deferred = <T,>() => { let resolve!: (value: T) => void; const promise = new Promise<T>(done => { resolve = done }); return { promise, resolve } }
afterEach(() => { clearPermissions(); vi.unstubAllGlobals() })

it('AEON-1195: every audited store retains rows through refresh and fences delayed reads after sign-out, person switch and 401', async () => {
  vi.stubGlobal('fetch', vi.fn(async () => new Response(JSON.stringify({ workspace: { permissions: [] }, project: null }))))
  const lead = { project_id: 'p', state: 'paused' as const, revision: 4, generation: 0, session_id: null, reason: '', process_active: false }
  const queue = { items: [], manual_order: true, capacity: { hours: 0, total: 2 } }
  const projection = { counts: { open: 3 } } as Awaited<ReturnType<typeof loadDeskProjection>>
  const capacityRows = [{ account_id: 'account', schedule: defaultSchedule('UTC') }] as Awaited<ReturnType<typeof listCapacity>>
  const schedules = [{ scope: 'user' as const, schedule: defaultSchedule('UTC') }]
  const computers = [{ computer_id: 'computer', computer_state: 'active', enrollments: [] }] as unknown as Awaited<ReturnType<typeof listPairingComputers>>
  state.session = reactive(state.session)
  for (const reset of ['sign-out', 'person switch', '401']) {
    clearPermissions()
    vi.mocked(readLead).mockResolvedValue(lead)
    vi.mocked(readQueue).mockResolvedValue(queue)
    vi.mocked(loadDeskProjection).mockResolvedValue(projection)
    vi.mocked(listCapacity).mockResolvedValue(capacityRows)
    vi.mocked(listSchedules).mockResolvedValue(schedules)
    vi.mocked(listPairingComputers).mockResolvedValue(computers)
    vi.mocked(readLeadDecisions).mockResolvedValue({ items: [], after: 0, caughtUp: true })
    vi.mocked(readOpenQuestions).mockResolvedValue({ items: [], more: false })
    const pinia = createPinia(); setActivePinia(pinia)
    const leads = useProjectLeads(), work = useWorkQueue(), desk = useDecisionDesk(), capacity = useCapacity()
    try {
      await Promise.all([leads.loadLead('p'), work.load('p'), desk.refresh(), capacity.load()])
      leads.view('p').settings = { revision: 1 } as NonNullable<ReturnType<typeof leads.view>['settings']>
      const shown = { lead: leads.views.p!.lead, queue: work.snapshots.p, desk: desk.projection, account: capacity.byAccount.get('account'), computers: capacity.computers }
      const heldLead = deferred<typeof lead>(), heldQueue = deferred<typeof queue>(), heldDesk = deferred<typeof projection>()
      const heldCapacity = deferred<typeof capacityRows>(), heldSchedules = deferred<typeof schedules>(), heldComputers = deferred<typeof computers>()
      vi.mocked(readLead).mockReturnValue(heldLead.promise)
      vi.mocked(readQueue).mockReturnValue(heldQueue.promise)
      vi.mocked(loadDeskProjection).mockReturnValue(heldDesk.promise)
      vi.mocked(listCapacity).mockReturnValue(heldCapacity.promise)
      vi.mocked(listSchedules).mockReturnValue(heldSchedules.promise)
      vi.mocked(listPairingComputers).mockReturnValue(heldComputers.promise)
      const reads = [readLead, readQueue, loadDeskProjection, listCapacity, listSchedules, listPairingComputers]
      for (const read of reads) vi.mocked(read).mockClear()
      await accessChanged()
      for (const read of reads) expect(read).toHaveBeenCalledOnce()
      expect(leads.views.p!.lead).toBe(shown.lead)
      expect(work.snapshots.p).toBe(shown.queue)
      expect(desk.projection).toBe(shown.desk)
      expect(capacity.byAccount.get('account')).toBe(shown.account)
      expect(capacity.computers).toBe(shown.computers)
      expect(capacity.hasUserSchedule).toBe(true)
      const flights = [leads.load('p'), work.load('p'), desk.refresh(), capacity.load()]
      if (reset === 'person switch') state.session.identity.principal.id = 'other'
      if (reset === '401') revokePermissions(); else clearPermissions()
      const empty = () => {
        expect(Object.keys(leads.views), reset).toEqual([])
        expect(Object.keys(work.snapshots), reset).toEqual([])
        expect(desk.projection, reset).toBeNull()
        expect(capacity.byAccount.size, reset).toBe(0)
        expect(capacity.computers, reset).toEqual([])
        expect(capacity.hasUserSchedule, reset).toBe(false)
      }
      empty()
      heldLead.resolve(lead); heldQueue.resolve(queue); heldDesk.resolve(projection)
      heldCapacity.resolve(capacityRows); heldSchedules.resolve(schedules); heldComputers.resolve(computers)
      await Promise.all(flights)
      empty()
    } finally { disposePinia(pinia) }
  }
})

it('AEON-1195: access listeners use the shared boundary and cannot clear state in their refresh callback', () => {
  const violations: string[] = []
  let audited = 0
  function scan(dir: URL) {
    for (const entry of readdirSync(dir, { withFileTypes: true })) {
      const path = new URL(entry.name, dir)
      if (entry.isDirectory()) { scan(new URL(`${entry.name}/`, dir)); continue }
      if (!/\.(ts|vue)$/.test(entry.name) || entry.name === 'authz.ts') continue
      let source = readFileSync(path, 'utf8')
      if (entry.name.endsWith('.vue')) source = source.match(/<script[^>]*>([\s\S]*?)<\/script>/)?.[1] ?? ''
      const tree = ts.createSourceFile(entry.name, source, ts.ScriptTarget.Latest, true)
      const functions = new Map<string, ts.Node>()
      const visit = (node: ts.Node, run: (node: ts.Node) => void) => { run(node); ts.forEachChild(node, child => visit(child, run)) }
      visit(tree, node => {
        if (ts.isFunctionDeclaration(node) && node.name && node.body) functions.set(node.name.text, node.body)
        if (ts.isVariableDeclaration(node) && ts.isIdentifier(node.name) && node.initializer && ts.isArrowFunction(node.initializer)) functions.set(node.name.text, node.initializer.body)
        if (ts.isImportSpecifier(node) && (node.propertyName ?? node.name).text === 'onAccessChange') violations.push(`${path.pathname}: raw listener bypasses reset/refresh boundary`)
      })
      visit(tree, node => {
        if (!ts.isCallExpression(node) || node.expression.getText(tree) !== 'onAccessRevalidate') return
        audited++
        const options = node.arguments[0]
        if (!options || !ts.isObjectLiteralExpression(options)) { violations.push(`${path.pathname}: explicit callbacks required`); return }
        const reset = options.properties.find(prop => prop.name?.getText(tree) === 'reset')
        if (!reset) violations.push(`${path.pathname}: reset callback required`)
        const refresh = options.properties.find(prop => prop.name?.getText(tree) === 'refresh')
        if (!refresh || !ts.isPropertyAssignment(refresh)) return
        const body = ts.isIdentifier(refresh.initializer) ? functions.get(refresh.initializer.text) : refresh.initializer
        if (!body) { violations.push(`${path.pathname}: refresh cannot be audited`); return }
        visit(body, child => {
          if (ts.isCallExpression(child)) {
            const name = child.expression.getText(tree).split('.').pop() ?? ''
            if (/^(clear|reset|invalidate|close|stop|forget|discard|drop)/i.test(name)) violations.push(`${path.pathname}: refresh calls ${name}`)
          }
          if (ts.isBinaryExpression(child) && child.operatorToken.kind === ts.SyntaxKind.EqualsToken && child.left.getText(tree).endsWith('.value')) {
            const target = child.left.getText(tree)
            if (!/error|message|busy|loading|notice/i.test(target) && /^(null|undefined|\[\]|\{\}|''|"")$/.test(child.right.getText(tree))) violations.push(`${path.pathname}: refresh clears ${target}`)
          }
        })
      })
    }
  }
  scan(new URL('../src/', import.meta.url))
  expect(audited).toBeGreaterThanOrEqual(33)
  expect(violations).toEqual([])
})
