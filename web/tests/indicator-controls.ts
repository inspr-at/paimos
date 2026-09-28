// SPDX-License-Identifier: AGPL-3.0-only
// Dev-only Vite entry: /tests/indicator-controls.html (not a production input).
// Ring modes and inner-icon sizes for all nine renderers at their real 20, 26
// and 64 px sizes, plus LiveBot rows that read one mocked account preference
// (?ring=&size=&palette=&theme=). No real API, account or live telemetry.
import { createApp, h, ref, type Component } from 'vue'
import LiveBot from '../src/components/projects/LiveBot.vue'
import Pulse from '../src/components/indicators/Pulse.vue'
import Robot1 from '../src/components/indicators/Robot1.vue'
import Robot2 from '../src/components/indicators/Robot2.vue'
import Robot3 from '../src/components/indicators/Robot3.vue'
import Robot4 from '../src/components/indicators/Robot4.vue'
import Robot5 from '../src/components/indicators/Robot5.vue'
import Orbit from '../src/components/indicators/Orbit.vue'
import Quill from '../src/components/indicators/Quill.vue'
import Sprite from '../src/components/indicators/Sprite.vue'
import { ICON_SIZE, indicatorArtScale, indicatorRing, indicatorVariants, type AgentIndicatorStyle, type IndicatorRing } from '../src/lib/indicatorVariants'
import type { AgentState } from '../src/lib/agentSignals'
import '../src/styles/base.css'
import '../src/styles/tokens.css'

const params = new URLSearchParams(location.search)
document.documentElement.dataset.theme = params.get('theme') === 'dark' ? 'dark' : 'light'
const account: Record<string, unknown> = { style: 'robot-1', hovering: params.get('hovering') === '1' }
if (params.has('ring')) account.ring = params.get('ring')
if (params.has('size')) account.size = Number(params.get('size'))
window.fetch = async input => new Response(JSON.stringify({ value: String(input).endsWith('agent-state')
  ? { palette: params.get('palette') ?? 'standard', dimInactive: true, inactiveOpacity: 55 }
  : account }), { headers: { 'Content-Type': 'application/json' } })

const renderers: Record<AgentIndicatorStyle, Component> = {
  pulse: Pulse, 'robot-1': Robot1, 'robot-2': Robot2, 'robot-3': Robot3, 'robot-4': Robot4, 'robot-5': Robot5, orbit: Orbit, quill: Quill, sprite: Sprite,
}
const css = document.createElement('style')
css.textContent = `body { margin: 0; padding: 24px; background: var(--canvas); color: var(--ink); font: 13px/1.5 var(--font); }
main { max-width: 1400px; margin: auto; } h1 { margin: 0 0 4px; font-size: 22px; } h2 { margin: 26px 0 8px; font-size: 14px; } p { margin: 0 0 12px; color: var(--ink-2); }
nav { display: flex; gap: 8px; margin-bottom: 8px; } button { font: inherit; padding: 5px 12px; color: var(--ink); background: var(--surface-raised); border: 1px solid var(--line-2); border-radius: 8px; }
.sheet { display: grid; grid-template-columns: 118px repeat(9, minmax(124px, 1fr)); gap: 6px; }
.heading { text-align: center; font-weight: 600; font-size: 12px; } .row-label { display: flex; align-items: center; font-size: 11.5px; color: var(--ink-2); }
.cell { display: flex; align-items: center; justify-content: center; gap: 10px; min-height: 84px; padding: 8px; border: 1px solid var(--line); border-radius: 10px; background: var(--surface-raised); }
.cell > span { display: inline-grid; place-items: center; }
@media (max-width: 700px) { body { padding: 12px; } main { min-width: 1300px; } }`
document.head.append(css)

const rings: (IndicatorRing | 'own')[] = ['own', 'moving', 'still', 'off']
const middle = Math.round((ICON_SIZE.min + ICON_SIZE.max) / 10) * 5
const sizes: (number | 'drawn')[] = ['drawn', ICON_SIZE.min, middle, ICON_SIZE.max]
const states: AgentState[] = ['working', 'waiting', 'throttled', 'problem', 'stale', 'stopped']
createApp({
  setup() {
    const pulse = ref(3)
    const art = (id: AgentIndicatorStyle, ring: IndicatorRing | 'own', size: number | 'drawn', px: number) => h('span', { 'data-px': px }, [h(renderers[id], {
      state: 'working', size: px, pulse: pulse.value, seed: `${id}:${px}`, lead: true,
      ring: indicatorRing(id, ring === 'own' ? undefined : ring), artScale: indicatorArtScale(id, size === 'drawn' ? undefined : size),
    })])
    return () => h('main', [
      h('h1', `Ring and icon size · ${document.documentElement.dataset.theme}`),
      h('p', `Icon size is how much of the space inside the ring the drawing fills (${ICON_SIZE.min}–${ICON_SIZE.max}%). Unset, each style keeps its drawn size. Shown at 20, 26 and 64 px.`),
      h('nav', { 'aria-label': 'Gallery controls' }, [h('button', { onClick: () => pulse.value++ }, 'Send event')]),
      h('div', { class: 'sheet', 'aria-label': 'Ring and size matrix' }, [
        h('div'), ...indicatorVariants.map(v => h('div', { class: 'heading' }, v.name)),
        ...rings.flatMap(ring => sizes.flatMap(size => [
          h('div', { class: 'row-label' }, `${ring === 'own' ? 'Own ring' : ring} · ${size === 'drawn' ? 'as drawn' : `${size}%`}`),
          ...indicatorVariants.map(v => h('div', { class: 'cell', 'data-variant': v.id, 'data-ring': ring, 'data-size': size }, [20, 26, 64].map(px => art(v.id, ring, size, px)))),
        ])),
      ]),
      h('h2', `Account preference · ring ${String(account.ring ?? 'own')} · size ${String(account.size ?? 'as designed')}`),
      h('div', { class: 'sheet', 'aria-label': 'Account preference states' }, [
        h('div'), ...indicatorVariants.map(v => h('div', { class: 'heading' }, v.name)),
        ...states.flatMap(state => [
          h('div', { class: 'row-label' }, state),
          ...indicatorVariants.map(v => h('div', { class: 'cell live', 'data-variant': v.id, 'data-state': state }, [20, 26, 44].map(px => h(LiveBot, { state, size: px, indicatorStyle: v.id, id: `${v.id}-${state}`, eventPulse: pulse.value })))),
        ]),
      ]),
    ])
  },
}).mount('#app')
