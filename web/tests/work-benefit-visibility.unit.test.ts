// SPDX-License-Identifier: AGPL-3.0-only
import { expect, it } from 'vitest'
import { readFileSync } from 'node:fs'
import { parse } from '@vue/compiler-sfc'
import { baseParse, compile, type TemplateChildNode } from '@vue/compiler-dom'
import * as Vue from 'vue'
import { renderToString } from '@vue/server-renderer'
import TicketBenefits from '../src/components/work/TicketBenefits.vue'
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
        completedTicketState, changeBenefit: () => {}, startEdit: () => {},
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
