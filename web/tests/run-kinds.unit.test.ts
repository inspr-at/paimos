// SPDX-License-Identifier: AGPL-3.0-only
import { expect, it } from 'vitest'
import { createSSRApp } from 'vue'
import { renderToString } from 'vue/server-renderer'
import ExecutionMark from '../src/components/agents/ExecutionMark.vue'
import SessionHost from '../src/components/agents/SessionHost.vue'

it('renders distinct AI, film-strip and terminal marks', async () => {
  for (const kind of ['ai', 'media', 'terminal'] as const) {
    const html = await renderToString(createSSRApp(ExecutionMark, { kind, provider: 'xai' }))
    expect(html).toContain(kind === 'ai' ? 'data-provider="xai"' : `data-run-kind="${kind}"`)
    expect(html).not.toContain('border')
    if (kind !== 'ai') expect(html).not.toContain('data-provider=')
  }
})

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
