// SPDX-License-Identifier: AGPL-3.0-only
// Dev-only Vite entry: /tests/indicator-gallery.html (not a production input).
// IV1 imports Robot2/3/4.vue by filename with exactly state/size/pulse/seed/lead.
import { createApp, h, ref, type Component } from 'vue'
import LiveBot from '../src/components/projects/LiveBot.vue'
import Robot5 from '../src/components/indicators/Robot5.vue'
import Robot2 from '../src/components/indicators/Robot2.vue'
import Robot3 from '../src/components/indicators/Robot3.vue'
import Robot4 from '../src/components/indicators/Robot4.vue'
import '../src/styles/tokens.css'

// LiveBot alone reads preferences. Keep manual previews offline as well as
// Playwright's mocked pages; the new variants never read or persist anything.
const originalFetch = window.fetch.bind(window)
window.fetch = (input, init) => String(input) === '/api/preferences/agent-indicator'
  ? Promise.resolve(new Response(JSON.stringify({ value: { style: 'calm', hovering: false } }), { headers: { 'Content-Type': 'application/json' } }))
  : originalFetch(input, init)

const stylesheet = document.createElement('style')
stylesheet.textContent = `
  * { box-sizing: border-box; }
  body { margin: 0; padding: 28px; background: var(--canvas); color: var(--ink); font: 14px/1.5 system-ui, sans-serif; }
  main { max-width: 1100px; margin: auto; }
  header { display: flex; align-items: center; justify-content: space-between; gap: 24px; }
  h1 { font-size: 24px; font-weight: 600; letter-spacing: -.6px; margin: 0; }
  p { margin: 4px 0 20px; color: var(--ink-2); }
  nav { display: flex; flex-wrap: wrap; gap: 8px; }
  button { color: var(--ink); background: var(--surface-raised); border: 1px solid var(--line-2); border-radius: 8px; padding: 8px 12px; cursor: pointer; font: inherit; }
  button:focus-visible { outline: 2px solid var(--teal); outline-offset: 3px; }
  button[aria-pressed=true] { background: var(--surface-sunken); }
  .columns, .state-row { display: grid; grid-template-columns: 112px repeat(5, minmax(130px, 1fr)); gap: 12px; }
  .columns { margin-top: 20px; margin-bottom: 10px; font-weight: 600; }
  .columns small { display: block; color: var(--ink-3); font-weight: 400; }
  .state-row { margin-bottom: 12px; align-items: stretch; }
  .state-label { align-self: center; font-size: 14px; margin: 0; font-weight: 600; }
  .sample { background: var(--surface-raised); border: 1px solid var(--line); border-radius: 12px; padding: 18px 10px 12px; min-height: 137px; }
  .sizes { display: flex; align-items: end; justify-content: center; gap: 20px; }
  figure { display: flex; flex-direction: column; align-items: center; justify-content: end; gap: 10px; margin: 0; }
  figcaption { font-size: 10px; color: var(--ink-3); font-variant-numeric: tabular-nums; }
  .quiet { display: flex; align-items: center; justify-content: center; gap: 9px; margin-top: 10px; }
  .quiet span { font-size: 10px; color: var(--ink-3); }
  .size-sweep { display: flex; gap: 24px; align-items: center; justify-content: space-between; padding: 14px 20px; background: var(--surface-raised); border: 1px solid var(--line); border-radius: 12px; }
  .sweep-robots { display: flex; align-items: center; gap: 12px; }
  h2 { font-size: 14px; margin: 20px 0 8px; }
  output { display: block; color: var(--ink-3); margin-top: 16px; font-size: 12px; }
  @media(max-width: 800px) { body { padding: 16px; } main { min-width: 930px; } }
`
document.head.append(stylesheet)

const variants: { label: string; description: string; component: Component }[] = [
  { label: 'Robot 1', description: 'Existing · calm', component: LiveBot },
  { label: 'Robot 2', description: 'Calm-friendly', component: Robot2 },
  { label: 'Robot 3', description: 'Friendly · middle', component: Robot3 },
  { label: 'Robot 4', description: 'Lively', component: Robot4 },
  { label: 'Robot 5', description: 'Existing · playful', component: Robot5 },
]
const states = ['working', 'waiting', 'stale'] as const
type State = typeof states[number]
createApp({
  setup() {
    const pulse = ref(7) // Nonzero initial history must never produce a glint.
    const lead = ref(true)
    const theme = ref('light')
    const liveState = ref<State>('working')
    const robot = (index: number, state: State, size: number, isLead = lead.value, seed = `agent-${index}`) => {
      const variant = variants[index]!
      return h(variant.component, index === 0
        ? { state, size, lead: isLead, eventPulse: pulse.value, id: seed, indicatorStyle: 'calm' }
        : { state, size, lead: isLead, seed, pulse: pulse.value })
    }
    const setTheme = (value: string) => { theme.value = value; document.documentElement.dataset.theme = value }
    return () => h('main', [
      h('header', [h('div', [h('h1', 'A little more personality.'), h('p', 'Five robots, one family. Working · waiting for approval · stale.')]),
        h('nav', { 'aria-label': 'Gallery controls' }, [
          ...['light', 'dark'].map(value => h('button', { 'aria-pressed': theme.value === value, onClick: () => setTheme(value) }, value === 'light' ? 'Light' : 'Dark')),
          h('button', { onClick: () => pulse.value++ }, 'Send event'),
          h('button', { onClick: () => pulse.value = 0 }, 'Reset counter'),
          h('button', { 'aria-pressed': lead.value, onClick: () => lead.value = !lead.value }, 'Lead motion'),
        ]),
      ]),
      h('div', { class: 'columns' }, [h('span', 'State'), ...variants.map(v => h('div', [v.label, h('small', v.description)]))]),
      ...states.map(state => h('section', { class: 'state-row', 'aria-label': state }, [
        h('h2', { class: 'state-label' }, state === 'waiting' ? 'Approval' : state === 'working' ? 'Working' : 'Stale'),
        ...variants.map((_variant, index) => h('div', { class: 'sample', 'data-variant': index + 1, 'data-state': state }, [
          h('div', { class: 'sizes' }, [26, 64].map(size => h('figure', { 'data-size': size }, [robot(index, state, size), h('figcaption', `${size} px`)]))),
          h('div', { class: 'quiet', 'data-quiet': true }, [robot(index, state, 18, false), h('span', 'Quiet · 18 px')]),
        ])),
      ])),
      h('h2', 'Scale and live state changes'),
      h('div', { class: 'size-sweep' }, [
        ...[18, 26, 40, 64, 72].map(size => h('figure', [h('div', { class: 'sweep-robots', 'data-sweep-size': size }, [1, 2, 3].map(index => robot(index, liveState.value, size))), h('figcaption', `${size} px`)])),
      ]),
      h('nav', { 'aria-label': 'Live state', style: 'margin-top: 12px' }, states.map(state => h('button', { 'aria-pressed': liveState.value === state, onClick: () => liveState.value = state }, `Set ${state}`))),
      h('output', `Event counter: ${pulse.value} · ${lead.value ? 'Lead' : 'Quiet'} motion · ${theme.value} theme`),
    ])
  },
}).mount('#gallery')
