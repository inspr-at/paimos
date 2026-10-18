// SPDX-License-Identifier: AGPL-3.0-only
import { expect, it, vi } from 'vitest'
import { readFileSync } from 'node:fs'
import { compileScript, parse } from '@vue/compiler-sfc'
import ts from 'typescript'
import * as ticketBenefits from '../src/lib/ticketBenefits'
import { baseParse, compile, type TemplateChildNode } from '@vue/compiler-dom'
import * as Vue from 'vue'
import { renderToString } from '@vue/server-renderer'
vi.mock('../src/stores/session', () => ({ useSession: () => ({ identity: null }) }))
import TicketBenefits from '../src/components/work/TicketBenefits.vue'
import { rowStore } from '../src/lib/rowStore'
import { completedTicketState } from '../src/lib/ticketBenefits'

// Compile the actual workspace branches: the reusable editor alone cannot
// catch a workspace that never offers it to migrated work.
const file = process.env.AEON_FIX4_WORKSPACE_SOURCE ?? new URL('../src/components/work/TicketWorkspace.vue', import.meta.url)
const { descriptor } = parse(readFileSync(file, 'utf8'))
const branches: string[] = []
function visit(nodes: TemplateChildNode[]) {
  for (const node of nodes) if (node.type === 1) {
    if (node.tag === 'TicketBenefits') branches.push(node.loc.source)
    visit(node.children)
  }
}
visit(baseParse(descriptor.template!.content).children)
const fields = { pill_en: 'Clear work benefits', pill_de: 'Verständlicher Nutzen der Arbeit', benefit_en: 'Work explains its benefit.', benefit_de: 'Arbeit erklärt ihren Nutzen.' }

for (const editing of [false, true]) {
  for (const kind of ['work', 'ticket']) {
    it(`${kind} offers its benefit ${editing ? 'editor' : 'reading section'} in the workspace`, async () => {
      const branch = branches.find(source => source.includes(editing ? 'class="edit-benefits"' : 'class="ws-benefits"'))
      expect(branch).toBeDefined()
      const { code } = compile(`<div>${branch}</div>`, { mode: 'function' })
      const render = new Function('Vue', code)(Vue)
      const app = Vue.createSSRApp({ components: { TicketBenefits }, setup: () => ({
        item: { kind_slug: kind, state: 'open', fields }, draft: fields,
        saving: false, editable: true, benefitNotice: '', benefitInvalidKey: '',
        completedTicketState, rowStore, changeBenefit: () => {}, startEdit: () => {},
      }), render })
      const html = await renderToString(app)
      expect(html).toContain('User benefit')
      if (editing) {
        expect(html).toContain('Pill · English')
        expect(html).toContain('Pill · Deutsch')
        expect(html).toContain('Benefit · English')
        expect(html).toContain('Benefit · Deutsch')
        expect(html).toContain('value="Clear work benefits"')
      } else {
        expect(html).toContain(fields.benefit_en)
        expect(html).toContain(fields.benefit_de)
      }
    })
  }
}

// Risk: the ticket editor loses the content mark or couples it to note hiding.
it('the no-release toggle edits its own boolean, stays controlled and respects disabled editing', async () => {
  type Host = { tag: string; props: Record<string, unknown>; children: Host[]; parent?: Host }
  const renderer = Vue.createRenderer<Host, Host>({
    createElement: tag => ({ tag, props: {}, children: [] }),
    createText: text => ({ tag: '#text', props: { text }, children: [] }),
    createComment: text => ({ tag: '#comment', props: { text }, children: [] }),
    setText: (node, text) => { node.props.text = text },
    setElementText: (node, text) => { node.props.text = text },
    parentNode: node => node.parent ?? null,
    nextSibling: node => { const siblings = node.parent?.children ?? []; return siblings[siblings.indexOf(node) + 1] ?? null },
    insert: (node, parent, anchor) => { node.parent = parent; const index = anchor ? parent.children.indexOf(anchor) : -1; if (index < 0) parent.children.push(node); else parent.children.splice(index, 0, node) },
    remove: node => { const siblings = node.parent?.children; if (siblings) siblings.splice(siblings.indexOf(node), 1) },
    patchProp: (node, key, _previous, next) => { node.props[key] = next },
  })
  const { descriptor: editorSource } = parse(readFileSync(new URL('../src/components/work/TicketBenefits.vue', import.meta.url), 'utf8'))
  const { content: editorCode } = compileScript(editorSource, { id: 'content-editor', inlineTemplate: true })
  const exported: { default?: Vue.Component } = {}
  const modules: Record<string, unknown> = { vue: Vue, '../../lib/ticketBenefits': ticketBenefits, '../AppIcon.vue': {}, './ParentBenefitGeneration.vue': {} }
  new Function('require', 'exports', ts.transpileModule(editorCode, { compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022 } }).outputText)((name: string) => modules[name], exported)
  const draft = Vue.reactive({ ...fields, no_release_needed: false, hide_from_release_notes: false })
  const disabled = Vue.ref(false)
  const changes: [string, string | boolean][] = []
  const root: Host = { tag: 'root', props: {}, children: [] }
  const app = renderer.createApp({ render: () => Vue.h(exported.default!, { fields: draft, editing: true, disabled: disabled.value, onChange: (key: string, value: string | boolean) => { changes.push([key, value]); Object.assign(draft, { [key]: value }) } }) })
  app.mount(root)
  try {
    const inputs = (node: Host): Host[] => [node, ...node.children.flatMap(inputs)].filter(item => item.tag === 'input' && item.props.type === 'checkbox')
    const [content, hidden] = inputs(root)
    expect(content).toBeDefined(); expect(hidden).toBeDefined()
    const toggle = content.props.onChange as (event: { target: { checked: boolean } }) => void
    toggle({ target: { checked: true } }); await Vue.nextTick()
    expect(changes).toEqual([['no_release_needed', true]])
    expect(content.props.checked).toBe(true); expect(hidden.props.checked).toBe(false)
    toggle({ target: { checked: false } }); await Vue.nextTick()
    expect(content.props.checked).toBe(false)
    disabled.value = true; await Vue.nextTick()
    expect(content.props.disabled).toBe(true); expect(hidden.props.disabled).toBe(true)
  } finally { app.unmount() }
})
