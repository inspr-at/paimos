// SPDX-License-Identifier: AGPL-3.0-only
// Isolated components served by Vite for the geometry regressions. They share
// the fixture app's context so the sheet uses its mocked session and router.
import { h, reactive, render, type App, type Component } from 'vue'
import AccessSheet from '../../src/components/access/AccessSheet.vue'
import RulesDialog from '../../src/components/rules/RulesDialog.vue'
import FloatingPanel from '../../src/components/work/FloatingPanel.vue'
import KeepEditor from '../../src/components/agents/KeepEditor.vue'
import ScheduleEditor from '../../src/components/agents/ScheduleEditor.vue'
import { defaultSchedule } from '../../src/lib/capacity'

let host: HTMLElement | null = null
let anchor: HTMLElement | null = null
const state = reactive({ long: false })
export type Shell = 'access' | 'rules' | 'floating' | 'keep' | 'night'

export function mountShell(kind: Shell, phone: boolean, above = false) {
  if (host) { render(null, host); host.remove() }
  anchor?.remove()
  host = document.createElement('div')
  document.body.append(host)
  state.long = false
  const app = (document.querySelector('#app') as HTMLElement & { __vue_app__: App }).__vue_app__
  const content = () => h('p', { class: 'fixture-content' }, state.long ? 'A longer explanation that wraps. '.repeat(200) : 'A short explanation.')
  const actions = () => h('button', { class: 'btn', type: 'button' }, 'Accept')
  const editors = { schedule: defaultSchedule(), sheet: phone, now: Date.now(), accounts: [], pools: [], timezone: 'UTC', saving: false, style: phone ? {} : { position: 'fixed', top: '96px', left: '16px' } }
  let component: Component
  let props: Record<string, unknown>
  let slots: Record<string, () => ReturnType<typeof h> | ReturnType<typeof h>[]> = {}
  if (kind === 'access') {
    component = AccessSheet; props = { title: 'Short task', size: 'center' }; slots = { default: content, foot: actions }
  } else if (kind === 'rules') {
    component = RulesDialog; props = { title: 'Short task' }; slots = { default: content, footer: actions }
  } else if (kind === 'floating') {
    anchor = document.createElement('button')
    anchor.textContent = 'Fixture trigger'
    Object.assign(anchor.style, { position: 'fixed', left: '20px', top: above ? `${innerHeight - 40}px` : '100px', height: '24px' })
    document.body.append(anchor)
    component = FloatingPanel; props = { anchor, label: 'Fixture picker' }
    slots = { default: () => [h('input', { 'aria-label': 'Find a result' }), content()] }
  } else if (kind === 'keep') {
    component = KeepEditor; props = { ...editors, away: '', poolReserves: {} }
  } else {
    component = ScheduleEditor; props = { ...editors, kind: 'night' }
  }
  const vnode = h(component, props, slots)
  vnode.appContext = app._context
  render(vnode, host)
}

export function setLongContent(long: boolean) { state.long = long }
