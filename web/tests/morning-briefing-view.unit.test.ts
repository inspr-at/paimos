// SPDX-License-Identifier: AGPL-3.0-only
import { afterEach, expect, it } from 'vitest'
import * as Vue from 'vue'
import { readFileSync } from 'node:fs'
import { parse, compileScript } from '@vue/compiler-sfc'
import ts from 'typescript'
import * as Briefing from '../src/lib/morningBriefing'
import * as Scope from '../src/lib/identityScope'
import * as AgentState from '../src/lib/agentState'
import { APIError } from '../src/lib/api'
import { usageDashboard } from './usage-data'

const START = '2026-09-30T08:00:00.000Z', END = '2026-10-01T08:00:00.000Z'
type Node = { tag: string; text: string; props: Record<string, unknown>; children: Node[]; parent: Node | null }
const node = (tag: string, text = ''): Node => ({ tag, text, props: {}, children: [], parent: null })
function detach(child: Node) { if (child.parent) child.parent.children.splice(child.parent.children.indexOf(child), 1); child.parent = null }
const renderer = Vue.createRenderer<Node, Node>({
  createElement: tag => node(tag), createText: text => node('#text', text), createComment: () => node('#comment'),
  setText: (el, text) => { el.text = text }, setElementText: (el, text) => { el.text = text; el.children = [] },
  parentNode: el => el.parent, nextSibling: el => el.parent?.children[el.parent.children.indexOf(el) + 1] ?? null,
  patchProp: (el, key, _old, value) => { el.props[key] = value }, remove: detach,
  insert: (child, parent, anchor) => { detach(child); child.parent = parent; const i = anchor ? parent.children.indexOf(anchor) : -1; if (i < 0) parent.children.push(child); else parent.children.splice(i, 0, child) },
})
const textOf = (el: Node): string => el.text + el.children.map(textOf).join(' ')
const flatten = (el: Node): Node[] => [el, ...el.children.flatMap(flatten)]
const apps: Vue.App[] = []
afterEach(() => { for (const app of apps.splice(0)) app.unmount() })

async function mount(options: { denied?: boolean; detailsFailure?: boolean; fullQueue?: boolean; pendingFailure?: boolean; noCost?: boolean; projectCost?: boolean; action?: string; autopilot?: boolean; autopilotFailure?: boolean; autopilotTruncated?: boolean; merge?: boolean; ticketLogFailure?: boolean; partialAllowance?: boolean; truncatedAllowance?: boolean } = {}) {
  const paths: string[] = [], saves: Briefing.BriefingPreference[] = []
  const projects = ['allowed', 'guest'].map(id => ({ id, routeKey: id }))
  const can = (permission: string, project?: string) => permission !== 'harness.read' || !options.noCost && (!options.projectCost || project === 'allowed')
  const pending = { id: 'approval', resource_kind: 'node', resource_id: 'ticket', scope: 'nodes.write', rationale: 'Review me', decision: null, expires_at: '2099-01-01T00:00:00Z' }
  const dashboard = usageDashboard('reported')
  if (options.partialAllowance) Object.assign(dashboard.allowance, { state: 'partial', truncated: !!options.truncatedAllowance })
  dashboard.totals.cost_state = 'known'
  dashboard.totals.cost_unknown_rows = 0; dashboard.totals.unreported_sessions = 0; dashboard.totals.provisional_rows = 0
  const json = async (path: string, _signal?: AbortSignal, init?: RequestInit) => {
    paths.push(path)
    if (path.startsWith('/preferences')) {
      if (init?.method === 'PUT') { saves.push(JSON.parse(String(init.body)).value); return {} }
      return { value: { last_visit: START } }
    }
    if (path.startsWith('/approvals')) {
      if (options.pendingFailure) throw new APIError(500, 'failed')
      return options.fullQueue ? Array.from({ length: 200 }, (_, i) => ({ ...pending, id: `approval-${i}` })) : []
    }
    if (path.includes('/messages')) return { items: options.fullQueue ? Array.from({ length: 200 }, (_, i) => ({ id: `m-${i}`, is_action_request: true, human_resolution_outcome: null, body: 'A check' })) : [] }
    if (path.includes('/journey')) {
      const next_action = { key: options.action ?? 'wait_for_build', label: options.action ?? 'Wait', available: true }
      return path.startsWith('/journey/next-actions') ? { items: projects.map(p => ({ project_node_id: p.id, next_action })) } : { next_action }
    }
    throw new Error(`Unexpected source ${path}`)
  }
  const modules: Record<string, unknown> = {
    vue: { ...Vue, vModelText: {} },
    '../lib/api': { APIError, listNodes: async () => { if (options.detailsFailure) throw new APIError(500, 'failed'); return { items: options.autopilot || options.merge ? [{ id: 'ticket', key: 'AEON-454', kind_slug: 'ticket', project: { id: 'allowed' } }] : [] } } },
    '../lib/authz': { can, ensurePermissions: async () => 'known', refreshPermissions: async () => {}, onAccessChange: () => () => {} },
    '../lib/identityScope': Scope, '../lib/agentState': AgentState,
    '../lib/usageDashboard': { loadUsageDashboard: async (params: { project?: string }) => { paths.push(`/usage/dashboard${params.project ? `?project=${params.project}` : ''}`); return dashboard } },
    '../lib/planning': { formatDollars: (value: number) => `$${value}` }, '../lib/work': { absoluteTime: (value: string) => value },
    '../stores/session': { useSession: () => ({ identity: { principal: { id: 'person', kind: 'person' }, tenant: { id: 'tenant' } } }) },
    '../stores/projects': { useProjects: () => ({ projects, load: async () => {}, byId: (id: string) => projects.find(p => p.id === id) }) },
    '../lib/morningBriefing': {
      ...Briefing, briefingJSON: json,
      loadBriefingPreference: async () => ({ last_visit: START }), saveBriefingPreference: async (value: Briefing.BriefingPreference) => { saves.push(value) },
      loadBriefingOutcomes: async () => { if (options.denied) throw new APIError(403, 'denied'); return { items: [], truncated: false } },
      loadBriefingEvents: async (_range: unknown, _signal: unknown, autopilot: unknown) => {
        paths.push(`events:${JSON.stringify(autopilot)}`)
        if (autopilot && options.autopilotFailure) throw new APIError(500, 'failed')
        const base = { node_id: 'ticket', at: START }
        return { items: autopilot && options.autopilot ? [
          { ...base, id: 454, type: 'status_autopilot.changed', before: { state: 'done' }, after: { state: 'delivered' } },
          { ...base, id: 455, type: 'status_autopilot.skipped', before: { state: 'done', human_check: 'Touch ID' }, after: { state: 'done', human_check: 'Touch ID' } },
        ] : [], truncated: !!options.autopilotTruncated }
      },
      loadBriefingWindow: async () => {
        if (options.ticketLogFailure) throw new APIError(500, 'failed')
        return { range: { from: START, to: END, first: false, capped: false }, events: { items: options.merge ? [
          { id: 456, node_id: 'ticket', type: 'node.updated', before: { state: 'done', fields: {} }, after: { state: 'done', fields: { merge_commit: 'abcdef1234567', pr_url: 'https://github.com/example/repo/pull/1' } }, at: START },
        ] : [], truncated: false } }
      },
    },
    '../components/AppIcon.vue': { __esModule: true, default: { render: () => Vue.h('svg', { 'aria-hidden': 'true' }) } },
    '../components/work/PlanningCell.vue': { __esModule: true, default: { render: () => Vue.h('span') } },
  }
  const source = readFileSync(new URL('../src/views/MorningBriefingView.vue', import.meta.url), 'utf8')
  const { descriptor } = parse(source)
  const { content } = compileScript(descriptor, { id: 'briefing-test', inlineTemplate: true, templateOptions: { compilerOptions: { hoistStatic: false } } })
  const { outputText } = ts.transpileModule(content, { compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022 } })
  const exports: { default?: Vue.Component } = {}
  new Function('require', 'exports', outputText)((id: string) => { if (!(id in modules)) throw new Error(`Unexpected dependency ${id}`); return modules[id] }, exports)
  const app = renderer.createApp(exports.default!)
  app.component('RouterLink', { props: ['to'], setup: (props, { slots }) => () => Vue.h('a', { href: props.to }, slots.default?.()) })
  const root = node('root'); apps.push(app); app.mount(root)
  for (let i = 0; i < 80; i++) await Promise.resolve()
  await Vue.nextTick()
  return { root, saves, paths }
}

it('saves the database snapshot cutoff even when the browser clock is ahead', async () => {
  const { saves } = await mount()
  expect(saves[0]?.last_visit).toBe(END)
})
it('advances completed logs even with 200 pending approvals and human requests', async () => {
  const { saves, paths } = await mount({ fullQueue: true })
  expect(saves).toHaveLength(1)
  expect(paths.some(path => path.startsWith('/approvals?') && path.includes('pending=true'))).toBe(true)
})
it('keeps pending-source failure separate from a successfully read log window', async () => {
  expect((await mount({ pendingFailure: true })).saves).toHaveLength(1)
})
it('reads money only for harness-granted projects and cites only successful sources', async () => {
  const scoped = await mount({ projectCost: true })
  expect(scoped.paths.filter(path => path.startsWith('/usage'))).toEqual(['/usage/dashboard?project=allowed'])
  const denied = await mount({ noCost: true })
  expect(flatten(denied.root).filter(el => el.tag === 'a' && textOf(el) === 'Usage source')).toHaveLength(0)
})
it('preserves the prior visit when a time-bounded outcome read is denied', async () => {
  const { saves, root } = await mount({ denied: true })
  expect(saves).toHaveLength(0)
  expect(textOf(root)).toContain('unavailable with your current access')
})
it.each(['open_first_release', 'start_build', 'plan_next_release', 'mark_candidate', 'retry_deploy'])('includes the available person action %s through one batch read', async action => {
  const { root, paths } = await mount({ action })
  expect(textOf(root)).toContain(action)
  expect(paths.filter(path => path.includes('/journey'))).toHaveLength(1)
})
it('renders stored ticket merge evidence for project-only readers before saving the cutoff', async () => {
  const { paths, root, saves } = await mount({ projectCost: true, merge: true })
  expect(textOf(root)).toContain('AEON-454 · Merge reported')
  expect(textOf(root)).toContain('abcdef1234567')
  expect(flatten(root).find(el => el.tag === 'a' && textOf(el) === 'Source event')?.props.href).toBe('/api/events?node_id=ticket&after=455&limit=1')
  expect(saves[0]?.last_visit).toBe(END)
  // The policy hides other workers' run telemetry from project readers. No
  // empty page from that domain is accepted as complete merge history.
  expect(paths.filter(path => path.startsWith('events:'))).toEqual(['events:true'])
})
it('retains project-only visits when their readable merge log fails', async () => {
  expect((await mount({ projectCost: true, merge: true, ticketLogFailure: true })).saves).toHaveLength(0)
})
it('renders autopilot delivery and human-check skips from the bounded log', async () => {
  const { root, saves } = await mount({ projectCost: true, autopilot: true })
  expect(textOf(root)).toContain('AEON-454 · Marked delivered')
  expect(textOf(root)).toContain('AEON-454 · Human check')
  expect(textOf(root)).toContain('Touch ID')
  expect(saves[0]?.last_visit).toBe(END)
})
it.each(['autopilotFailure', 'autopilotTruncated'] as const)('keeps the earlier visit on %s', async option => {
  const { saves, root } = await mount({ [option]: true })
  expect(saves).toHaveLength(0)
  expect(textOf(root)).toMatch(/could not be loaded|previous visit has been kept/)
})
it('renders measured in visible cost text for screen readers', async () => {
  const { root } = await mount()
  expect(textOf(root)).toMatch(/measured/i)
})

it('keeps a pending-only detail failure separate from completed logs', async () => {
  expect((await mount({ fullQueue: true, detailsFailure: true })).saves).toHaveLength(1)
})

it.each([false, true])('shows returned partial allowance windows and coverage (truncated=%s)', async truncatedAllowance => {
  const { root } = await mount({ projectCost: true, partialAllowance: true, truncatedAllowance })
  const text = textOf(root)
  expect(text).toContain('Accounts now')
  expect(text).toContain(usageDashboard('reported').allowance.windows[0]!.label)
  expect(text).not.toContain('No account budget windows recorded.')
  expect(text).toContain(truncatedAllowance ? 'Account budget windows are truncated.' : 'Some account budgets are withheld.')
})
