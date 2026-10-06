// SPDX-License-Identifier: AGPL-3.0-only
// Mount the real floating list callers with enough choices to exceed their old
// inner caps. The fixture app provides their router, session and mocked APIs.
import { defineComponent, h, reactive, ref, render, type App, type Component } from 'vue'
import ChoicePicker from '../../src/components/settings/ChoicePicker.vue'
import MoveToGroup from '../../src/components/projects/MoveToGroup.vue'
import EpicPicker from '../../src/components/work/EpicPicker.vue'
import OptionMenu from '../../src/components/work/OptionMenu.vue'
import LabelMenu from '../../src/components/work/LabelMenu.vue'
import RelationPicker from '../../src/components/work/RelationPicker.vue'
import ChoiceFacet from '../../src/components/business/ChoiceFacet.vue'
import PickerMenu from '../../src/components/business/PickerMenu.vue'
import SessionHost from '../../src/components/agents/SessionHost.vue'
import RolePicker from '../../src/components/access/RolePicker.vue'
import { useAccess } from '../../src/stores/access'
import { recents } from '../../src/lib/recents'

export type FloatingList = 'choice' | 'group' | 'epic' | 'option' | 'label' | 'relation' | 'facet' | 'business'
let host: HTMLElement | null = null
let anchor: HTMLElement | null = null

export function mountFloatingList(kind: FloatingList, longNames: boolean | string = false) {
  if (host) { render(null, host); host.remove() }
  anchor?.remove()
  host = document.createElement('div')
  document.body.append(host)
  anchor = document.createElement('button')
  anchor.textContent = 'Fixture trigger'
  Object.assign(anchor.style, { position: 'fixed', left: '20px', top: '80px', height: '24px' })
  document.body.append(anchor)
  const choices = Array.from({ length: 16 }, (_, index) => {
    const label = typeof longNames === 'string' ? `${longNames} ${index + 1}` : longNames ? `Projektübergreifende Entwicklungszusammenarbeit und Qualitätsverantwortung ${index + 1}` : `Choice ${index + 1}`
    return { value: `choice-${index}`, label, name: label, color: '', on: 0, count: 1 }
  })
  const components: Record<FloatingList, Component> = { choice: ChoicePicker, group: MoveToGroup, epic: EpicPicker, option: OptionMenu, label: LabelMenu, relation: RelationPicker, facet: ChoiceFacet, business: PickerMenu }
  let props: Record<string, unknown> = { anchor }
  // Match production: a single choice unmounts the menu through its parent.
  // Disclosure and navigation are measured while it is open; selection must
  // still emit once, close it, and discard any tooltip attached to that row.
  const opened = ref(true)
  const events: unknown[] = []
  Object.assign(window, { __floatingEvents: events })
  const choose = (value: unknown) => { events.push(value); if (kind !== 'facet') opened.value = false }
  const close = (restore: boolean) => { opened.value = false; if (restore) anchor?.focus() }
  if (kind === 'choice') props = { ...props, label: 'Choose a setting', choices, current: choices[0]!.value }
  if (kind === 'group') props = { ...props, subject: 'Pharos', options: choices.map(choice => ({ group: { id: choice.value, name: choice.label, kind: 'shared', color: '' }, count: 1, current: false })) }
  if (kind === 'epic') props = { ...props, subject: 'PHAROS-11', projectId: 'p-pharos', current: null }
  if (kind === 'option') props = { ...props, title: 'Type', subject: 'PHAROS-11', kind: 'type', options: choices, current: choices[0]!.value, searchable: true }
  if (kind === 'label') props = { ...props, labels: choices, count: 1 }
  if (kind === 'relation') {
    recents.splice(0, recents.length, ...choices.slice(0, 8).map((choice, index) => ({ type: 'ticket' as const, key: `PHAROS-${index + 100}`, title: choice.label, state: 'open', kind: 'ticket', projectKey: 'PHAROS' })))
    props = { ...props, subject: 'PHAROS-11', selfId: 'n11', projectKey: 'PHAROS', related: [], link: async (...args: unknown[]) => { events.push(args); return null } }
  }
  if (kind === 'facet') {
    const selected = reactive<string[]>([])
    props = { label: 'Choices', options: choices, selected, onToggle: (value: string) => { choose(value); const at = selected.indexOf(value); if (at < 0) selected.push(value); else selected.splice(at, 1) }, onClear: () => { events.push('clear'); selected.splice(0) } }
  }
  if (kind === 'business') props = { ...props, title: 'Choose a customer', options: choices }
  const app = (document.querySelector('#app') as HTMLElement & { __vue_app__: App }).__vue_app__
  const parent = defineComponent({ setup: () => () => opened.value
    ? h(components[kind], { ...props, ...(kind === 'facet' ? {} : { onClose: close }), ...(['facet', 'label', 'relation'].includes(kind) ? {} : { onChoose: choose }) })
    : null })
  const vnode = h(parent)
  vnode.appContext = app._context
  render(vnode, host)
}

export function mountSessionHost(registeredHost: string) {
  if (host) { render(null, host); host.remove() }
  anchor?.remove()
  host = document.createElement('div')
  Object.assign(host.style, { position: 'fixed', top: '80px', left: '20px', zIndex: '60' })
  document.body.append(host)
  const app = (document.querySelector('#app') as HTMLElement & { __vue_app__: App }).__vue_app__
  const vnode = h(SessionHost, { host: registeredHost, editable: true })
  vnode.appContext = app._context
  render(vnode, host)
}

export function mountProjectRolePicker(subject: string, place: string) {
  if (host) { render(null, host); host.remove() }
  anchor?.remove()
  host = document.createElement('div')
  document.body.append(host)
  anchor = document.createElement('button')
  Object.assign(anchor.style, { position: 'fixed', top: '80px', left: '20px' })
  document.body.append(anchor)
  const app = (document.querySelector('#app') as HTMLElement & { __vue_app__: App }).__vue_app__
  const access = useAccess()
  const vnode = h(RolePicker, { anchor, subject, place, roles: access.roles, registry: access.registry, mine: new Set(access.registry.map(permission => permission.key)), scope: 'project', current: 'role-member' })
  vnode.appContext = app._context
  render(vnode, host)
}
