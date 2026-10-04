// SPDX-License-Identifier: AGPL-3.0-only
import { expect, it } from 'vitest'
import { createSSRApp } from 'vue'
import { renderToString } from 'vue/server-renderer'
import SessionHost from '../src/components/agents/SessionHost.vue'

it('renders the registered host by default, an owner override, and an ellipsis-ready long label', async () => {
  for (const label of [undefined, "David's MacBook", 'A'.repeat(128)]) {
    const html = await renderToString(createSSRApp(SessionHost, { host: 'mbp2606', label, editable: true }))
    expect(html).toContain(label?.replaceAll("'", '&#39;') || 'mbp2606')
    expect(html).toContain('class="host-name"')
    expect(html).toContain('Rename computer mbp2606 for yourself')
  }
  const agent = await renderToString(createSSRApp(SessionHost, { host: 'mbp2606', editable: false }))
  expect(agent).toContain('disabled')
  expect(agent).not.toContain('class="host-pencil"')
})
