// SPDX-License-Identifier: AGPL-3.0-only
// Isolated visual/contract fixture: no account, API or simulated live telemetry.
import { createApp, h, ref } from 'vue'
import Pulse from '../src/components/indicators/Pulse.vue'
import Robot1 from '../src/components/indicators/Robot1.vue'
import Robot5 from '../src/components/indicators/Robot5.vue'
import AgentIndicatorSettings from '../src/components/settings/AgentIndicatorSettings.vue'
import '../src/styles/base.css'
import '../src/styles/tokens.css'

const parameters = new URLSearchParams(location.search)
const theme = parameters.get('theme') === 'dark' ? 'dark' : 'light'
// The Settings fixture has one explicit, memory-only mocked API response.
if (parameters.get('view') === 'settings') {
  window.fetch = async () => new Response(JSON.stringify({ value: { style: 'robot-1', hovering: false } }), { headers: { 'Content-Type': 'application/json' } })
}
document.documentElement.dataset.theme = theme
const css = document.createElement('style')
css.textContent = `
body { margin: 0; padding: 32px; color: var(--ink); background: var(--surface); font: 13px/1.5 system-ui, sans-serif; }
main { max-width: 900px; margin: auto; } h1 { font-size: 22px; margin: 0; } p { color: var(--ink-2); }
.controls { display: flex; gap: 18px; align-items: center; margin: 18px 0; } button { font: inherit; padding: 6px 14px; }
.sheet { display: grid; grid-template-columns: 110px repeat(3, 1fr); gap: 1px; background: var(--line); border: 1px solid var(--line); border-radius: 12px; overflow: hidden; }
.cell { display: grid; place-items: center; background: var(--surface-raised); min-height: 92px; padding: 8px; }
.settings { max-width: 620px; }
@media (max-width: 600px) { body { padding: 16px; } }
.heading { min-height: 36px; font-weight: 600; } .label { color: var(--ink-2); font-size: 12px; text-align: center; }
`
document.head.append(css)
createApp({ setup() {
  const pulse = ref(7), lead = ref(true)
  const variants = [['Pulse', Pulse], ['Robot 1', Robot1], ['Robot 5', Robot5]] as const
  return () => parameters.get('view') === 'settings' ? h('main', { class: 'settings' }, [h('h1', 'Appearance'), h(AgentIndicatorSettings)]) : h('main', [
    h('h1', `Agent indicators · ${theme}`),
    h('p', 'Actual SVGs at 26 px and 64 px. Working, waiting for approval, and stale.'),
    h('div', { class: 'controls' }, [h('button', { onClick: () => pulse.value++ }, 'Real event'), h('label', [h('input', { type: 'checkbox', checked: lead.value, onChange: () => { lead.value = !lead.value } }), ' Lead animation'])]),
    h('div', { class: 'sheet' }, [h('div', { class: 'cell heading' }, 'State / size'), ...variants.map(([name]) => h('div', { class: 'cell heading' }, name)),
      ...([26, 64] as const).flatMap(size => (['working', 'waiting', 'stale'] as const).flatMap(state => [
        h('div', { class: 'cell label' }, `${state} · ${size} px`),
        ...variants.map(([name, component]) => h('div', { class: 'cell', 'data-variant': name, 'data-state': state, 'data-size': size }, [h(component, { state, size, pulse: pulse.value, seed: `${name}:${size}`, lead: lead.value })])),
      ])),
    ]),
  ])
} }).mount('#app')
