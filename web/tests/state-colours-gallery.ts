// SPDX-License-Identifier: AGPL-3.0-only
// Local visual contract, excluded from the production build. No real API.
import { createApp, h } from 'vue'
import LiveBot from '../src/components/projects/LiveBot.vue'
import AgentStateLabel from '../src/components/agents/AgentStateLabel.vue'
import { indicatorVariants } from '../src/lib/indicatorVariants'
import { STATE_LABEL, type AgentState } from '../src/lib/agentSignals'
import '../src/styles/base.css'
import '../src/styles/tokens.css'

const params = new URLSearchParams(location.search)
document.documentElement.dataset.theme = params.get('theme') === 'dark' ? 'dark' : 'light'
window.fetch = async input => new Response(JSON.stringify({ value: String(input).endsWith('agent-state')
  ? { palette: params.get('palette') ?? 'standard', dimInactive: true, inactiveOpacity: 55 }
  : { style: 'robot-1', hovering: true } }), { headers: { 'Content-Type': 'application/json' } })
const css = document.createElement('style')
css.textContent = `body { margin: 0; padding: 28px; background: var(--canvas); color: var(--ink); font: 14px/1.5 var(--font); }
main { max-width: 1280px; margin: auto; } h1 { margin: 0 0 6px; font-size: 24px; } p { color: var(--ink-2); margin-bottom: 24px; }
.sheet { display: grid; grid-template-columns: 130px repeat(9, minmax(85px,1fr)); gap: 8px; }
.heading { padding: 8px 0; text-align: center; font-weight: 600; }
.sample { display: grid; place-items: center; min-height: 83px; padding: 10px; border: 1px solid var(--line); border-radius: 10px; background: var(--surface-raised); }
.state-name { display: flex; align-items: center; }`
document.head.append(css)
const states: AgentState[] = ['working', 'waiting', 'throttled', 'problem', 'idle', 'stale', 'stopped']
createApp({ render: () => h('main', [
  h('h1', `Agent states · ${document.documentElement.dataset.theme} · ${params.get('palette') ?? 'standard'}`),
  h('p', 'One state system. Working · Needs something · Throttled · Problem · Idle · Stopped.'),
  h('div', { class: 'sheet' }, [h('div'), ...indicatorVariants.map(v => h('div', { class: 'heading' }, v.name)),
    ...states.flatMap(state => [h('div', { class: 'state-name' }, [h(AgentStateLabel, { state })]),
      ...indicatorVariants.map(v => h('div', { class: 'sample', 'data-variant': v.id, 'data-state': state, title: `${v.name}: ${STATE_LABEL[state]}` }, [h(LiveBot, { state, size: 40, indicatorStyle: v.id, id: 'sc1' })])),
    ]),
  ]),
]) }).mount('#app')
