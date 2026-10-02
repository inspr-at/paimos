// SPDX-License-Identifier: AGPL-3.0-only
import { expect, it } from 'vitest'
import * as Vue from 'vue'
import { readFileSync } from 'node:fs'
import { compileScript, parse } from '@vue/compiler-sfc'
import ts from 'typescript'

// The real properties and hours components run in Vue's in-memory host. The
// browser spec separately measures the controls' pixel bounds in both layouts.
type Node = { tag: string; props: Record<string, unknown>; children: Node[]; parent: Node | null; onInsert?: () => void }
const node = (tag: string): Node => ({ tag, props: {}, children: [], parent: null })
function detach(child: Node) {
  const siblings = child.parent?.children
  if (siblings) siblings.splice(siblings.indexOf(child), 1)
  child.parent = null
}
const renderer = Vue.createRenderer<Node, Node>({
  createElement: tag => node(tag), createText: () => node('#text'), createComment: () => node('#comment'),
  setText() {}, setElementText() {}, parentNode: child => child.parent,
  nextSibling: child => child.parent?.children[child.parent.children.indexOf(child) + 1] ?? null,
  patchProp: (el, key, _old, value) => { el.props[key] = value }, remove: detach,
  insert: (child, parent, anchor) => {
    detach(child); child.parent = parent
    const i = anchor ? parent.children.indexOf(anchor) : -1
    if (i < 0) parent.children.push(child); else parent.children.splice(i, 0, child)
    for (let current: Node | null = parent; current; current = current.parent) current.onInsert?.()
  },
})
function component(path: string, modules: Record<string, unknown>): Vue.Component {
  const source = readFileSync(new URL(`../src/components/${path}.vue`, import.meta.url), 'utf8')
  const { descriptor } = parse(source)
  const { content } = compileScript(descriptor, { id: path, inlineTemplate: true })
  const { outputText } = ts.transpileModule(content, { compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022 } })
  const exports: { default?: Vue.Component } = {}
  new Function('require', 'exports', outputText)((id: string) => {
    if (id in modules) {
      const value = modules[id]
      return value && typeof value === 'object' && 'default' in value ? { __esModule: true, ...value } : value
    }
    throw new Error(`Unexpected dependency: ${id}`)
  }, exports)
  return exports.default!
}
const flatten = (root: Node): Node[] => [root, ...root.children.flatMap(flatten)]

it.each(['column', 'row'])('late logged hours leave the %s placement control prefix intact', async layout => {
  let releaseTotals!: () => void, requested!: () => void, calls = 0
  const totals = new Promise<void>(resolve => { releaseTotals = resolve })
  const requestsStarted = new Promise<void>(resolve => { requested = resolve })
  const empty = { default: () => Vue.h('span') }
  const hours = component('business/TicketHours', {
    vue: Vue,
    '../../lib/business': { getTimeTotals: () => { if (++calls === 2) requested(); return totals.then(() => ({ duration_seconds: 3600, amounts: [] })) } },
    '../../stores/business': { useBusiness: () => ({ open: { hours: true }, loadPlugins: () => Promise.resolve() }) },
    './duration': { formatSpan: () => '1h' }, './money': { formatAmount: () => '0' }, './BizIcon.vue': empty,
  })
  const props = component('work/TicketProperties', {
    vue: Vue,
    '../../lib/releaseMembership': { releaseCell: () => ({ kind: 'none' }) },
    '../../lib/work': { absoluteTime: () => '', kindLabel: () => 'Ticket', priorityLabel: () => 'High', relativeTime: () => '', statusMeta: () => ({ label: 'Open' }) },
    '../AppIcon.vue': empty, './PersonAvatar.vue': empty, './PriorityIcon.vue': empty, './StatusIcon.vue': empty, './QueueIndicator.vue': empty, '../agents/AgentStateLabel.vue': empty,
    '../business/TicketHours.vue': { default: hours }, './TicketEstimate.vue': empty,
    './TicketPlacement.vue': { default: () => ['area', 'complexity'].map(name => Vue.h('div', { class: 'prop' }, [Vue.h('select', { 'aria-label': name })])) },
    '../../stores/agents': { useAgents: () => ({ forTicket: () => [], ensureTicket: () => Promise.resolve() }) },
    '../../lib/usePolledData': { usePoller: () => ({ start() {}, stop() {} }) },
  })
  const root = node('root')
  let hoursInserted!: () => void
  const renderedHours = new Promise<void>(resolve => { hoursInserted = resolve })
  root.onInsert = () => {
    if (flatten(root).some(el => String(el.props.class).includes('ticket-hours'))) hoursInserted()
  }
  const app = renderer.createApp({ render: () => Vue.h(props, { layout, editable: true, now: 0, item: { id: 'ticket', kind_slug: 'ticket', fields: {}, state: 'open', created_at: '', updated_at: '' } }) })
  app.component('RouterLink', { setup: (_props, { slots }) => () => Vue.h('a', slots.default?.()) })
  app.mount(root)
  try {
    await requestsStarted
    const controls = flatten(root).filter(el => el.tag === 'select')
    expect(controls).toHaveLength(2)
    const prefix = (control: Node) => {
      let row = control
      while (row.parent && row.parent.tag !== 'dl') row = row.parent
      if (!row.parent) throw new Error('Control is outside properties')
      return row.parent.children.slice(0, row.parent.children.indexOf(row)).filter(el => !el.tag.startsWith('#'))
    }
    const before = controls.map(prefix)
    releaseTotals()
    await renderedHours
    expect(flatten(root).filter(el => String(el.props.class).includes('ticket-hours'))).toHaveLength(1)
    for (const [i, control] of controls.entries()) expect(prefix(control)).toEqual(before[i])
  } finally { app.unmount() }
})
