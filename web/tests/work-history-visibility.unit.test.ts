// SPDX-License-Identifier: AGPL-3.0-only
import { expect, it } from 'vitest'
import { readFileSync } from 'node:fs'
import { parse } from '@vue/compiler-sfc'
import { baseParse, compile, type TemplateChildNode } from '@vue/compiler-dom'
import * as Vue from 'vue'
import { renderToString } from '@vue/server-renderer'

// Render the workspace's actual component conditions, retaining node identity.
const file = process.env.AEON_FIX5_WORKSPACE_SOURCE ?? new URL('../src/components/work/TicketWorkspace.vue', import.meta.url)
const { descriptor } = parse(readFileSync(file, 'utf8'))
const branches: Record<string, string> = {}
function visit(nodes: TemplateChildNode[]) {
  for (const node of nodes) if (node.type === 1) {
    if (['TicketOutcomes', 'TicketReviews'].includes(node.tag)) branches[node.tag] = node.loc.source
    visit(node.children)
  }
}
visit(baseParse(descriptor.template!.content).children)

for (const [component, kinds] of Object.entries({ TicketOutcomes: ['work', 'ticket'], TicketReviews: ['work', 'ticket', 'task'] })) {
  for (const kind of [...kinds, 'memory']) {
    it(`${component} ${kinds.includes(kind) ? 'preserves' : 'excludes'} ${kind} access`, async () => {
      expect(branches[component]).toBeDefined()
      const { code } = compile(`<div>${branches[component]}</div>`, { mode: 'function' })
      const render = new Function('Vue', code)(Vue)
      const stub = Vue.defineComponent({ props: ['nodeId', 'projectId'], setup: props => () => Vue.h('span', { 'data-node': props.nodeId, 'data-project': props.projectId }, component) })
      const app = Vue.createSSRApp({ components: { [component]: stub }, setup: () => ({ item: { id: 'migrated-id', kind_slug: kind }, project: { id: 'project-id' } }), render })
      const html = await renderToString(app)
      if (kinds.includes(kind)) {
        expect(html).toContain(component)
        expect(html).toContain('data-node="migrated-id"')
        if (component === 'TicketReviews') expect(html).toContain('data-project="project-id"')
      } else expect(html).not.toContain(component)
    })
  }
}
