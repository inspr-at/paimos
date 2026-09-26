// SPDX-License-Identifier: AGPL-3.0-only
import { createApp, h, ref } from 'vue'
import Orbit from '../src/components/indicators/Orbit.vue'
import Quill from '../src/components/indicators/Quill.vue'
import Sprite from '../src/components/indicators/Sprite.vue'
import '../src/styles/tokens.css'

// Local, API-free Vite fixture; never wired into the app or production build.
// Coordinator: npm run test -- --workers=2 tests/indicator-creative-gallery.spec.ts
// Visual review: /tests/indicator-creative-gallery.html?theme=light (or dark).
const variants = { Orbit, Quill, Sprite }
const descriptions = { Orbit: 'Creative calm · a measured orbit', Quill: 'Creative friendly · a little flourish', Sprite: 'Creative playful · a working firefly' }
const states = ['working', 'waiting', 'stale'] as const
type State = typeof states[number]
const parameters = new URLSearchParams(location.search)
const theme = parameters.get('theme') === 'dark' ? 'dark' : 'light'
document.documentElement.dataset.theme = theme

const style = document.createElement('style')
style.textContent = `
  * { box-sizing: border-box; }
  body { margin: 0; background: var(--canvas); color: var(--ink); font: 14px/1.5 var(--font); }
  main { max-width: 1040px; padding: 32px; margin: auto; }
  h1 { margin: 4px 0; font-size: 28px; letter-spacing: -.8px; font-weight: 650; }
  h2 { margin: 0; font-size: 17px; font-weight: 600; }
  p { margin: 0; color: var(--ink-2); }
  .eyebrow { font: 11px/1.5 var(--mono); letter-spacing: 1.2px; text-transform: uppercase; }
  .columns, .variant { display: grid; grid-template-columns: 220px repeat(3, 1fr); gap: 12px; align-items: center; }
  .columns { margin: 26px 0 12px; font: 11px/1.5 var(--mono); color: var(--ink-2); text-align: center; }
  .variant { margin-bottom: 12px; }
  .variant > header p { max-width: 170px; margin-top: 6px; font-size: 12px; }
  .state-pair { display: flex; align-items: center; justify-content: space-evenly; gap: 12px; height: 136px; background: var(--surface-raised); border: 1px solid var(--line); border-radius: 12px; }
  figure { display: flex; flex-direction: column; align-items: center; justify-content: center; gap: 12px; margin: 0; }
  .sample { display: grid; place-items: center; width: 74px; height: 74px; }
  figcaption { font: 10px/1 var(--mono); color: var(--ink-3); }
  footer { margin-top: 22px; font-size: 12px; color: var(--ink-2); }
  .bench { margin-top: 40px; padding: 24px; border: 1px solid var(--line); border-radius: 12px; background: var(--surface-raised); }
  .controls, .probes { display: flex; align-items: center; flex-wrap: wrap; gap: 10px; margin-top: 16px; }
  button { padding: 7px 12px; border: 1px solid var(--line-2); border-radius: 7px; background: var(--surface-raised); color: var(--ink); font: inherit; cursor: pointer; }
  button:focus-visible { outline: 2px solid var(--teal); outline-offset: 2px; }
  .probe { min-width: 130px; display: grid; justify-items: center; gap: 10px; padding: 16px; }
`
document.head.append(style)

createApp({
  setup() {
    const pulse = ref(Number(parameters.get('pulse') ?? 7))
    const state = ref<State>('working')
    const lead = ref(true)
    const size = ref<number>()
    const seed = ref('agent-a')
    const mounted = ref(true)
    const button = (label: string, action: () => void) => h('button', { type: 'button', onClick: action }, label)
    return () => h('main', [
      h('section', { id: 'contact-sheet' }, [
        h('p', { class: 'eyebrow' }, `PAIMOS AEON / IV3 / ${theme}`),
        h('h1', 'Three ways to see work happen'),
        h('p', 'Creative indicators · actual size · calm to playful'),
        h('div', { class: 'columns' }, [h('span', 'STYLE'), ...states.map(value => h('span', value === 'waiting' ? 'AWAITING APPROVAL' : value.toUpperCase()))]),
        ...Object.entries(variants).map(([name, component]) => h('section', { class: 'variant', 'data-variant': name }, [
          h('header', [h('h2', name), h('p', descriptions[name as keyof typeof variants])]),
          ...states.map(value => h('div', { class: 'state-pair' }, [26, 64].map(size => h('figure', [
            h('div', { class: 'sample', 'data-state': value, 'data-size': size }, [
              h(component, { state: value, size, pulse: 7, seed: `${name}-gallery`, lead: true }),
            ]),
            h('figcaption', `${size} px`),
          ])))),
        ])),
        h('footer', 'Teal works. Amber waits for approval. Grey is stale. Gold appears only on a real event.'),
      ]),
      h('section', { class: 'bench' }, [
        h('h2', 'Motion bench'),
        h('p', 'The initial event count is already seven. Mounting never flashes.'),
        h('div', { class: 'controls' }, [
          button('Real event', () => { pulse.value++ }),
          button('Repeat counter', () => { pulse.value = pulse.value }),
          button('Reset counter', () => { pulse.value = 0 }),
          ...states.map(value => button(value, () => { state.value = value })),
          button('Toggle lead', () => { lead.value = !lead.value }),
          button('Change seed', () => { seed.value = seed.value === 'agent-a' ? 'agent-b' : 'agent-a' }),
          ...[18, 26, 64, 72].map(value => button(`${value} px`, () => { size.value = value })),
          button('Remount', () => { mounted.value = !mounted.value }),
        ]),
        h('div', { class: 'probes' }, mounted.value ? Object.entries(variants).map(([name, component]) => h('div', { class: 'probe', 'data-testid': `probe-${name}` }, [
          h(component, { state: state.value, size: size.value, pulse: pulse.value, seed: seed.value, lead: lead.value }),
          h('span', name),
        ])) : []),
      ]),
    ])
  },
}).mount('#gallery')
