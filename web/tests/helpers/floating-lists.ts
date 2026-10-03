// SPDX-License-Identifier: AGPL-3.0-only
// Mount the real floating list callers with enough choices to exceed their old
// inner caps. The fixture app provides their router, session and mocked APIs.
import { h, reactive, render, type App, type Component } from 'vue'
import ChoicePicker from '../../src/components/settings/ChoicePicker.vue'
import MoveToGroup from '../../src/components/projects/MoveToGroup.vue'
import EpicPicker from '../../src/components/work/EpicPicker.vue'
import OptionMenu from '../../src/components/work/OptionMenu.vue'
import LabelMenu from '../../src/components/work/LabelMenu.vue'
import RelationPicker from '../../src/components/work/RelationPicker.vue'
import ChoiceFacet from '../../src/components/business/ChoiceFacet.vue'
import PickerMenu from '../../src/components/business/PickerMenu.vue'
import { recents } from '../../src/lib/recents'

export type FloatingList = 'choice' | 'group' | 'epic' | 'option' | 'label' | 'relation' | 'facet' | 'business'
let host: HTMLElement | null = null
let anchor: HTMLElement | null = null

export function mountFloatingList(kind: FloatingList) {
  if (host) { render(null, host); host.remove() }
  anchor?.remove()
  host = document.createElement('div')
  document.body.append(host)
  anchor = document.createElement('button')
  anchor.textContent = 'Fixture trigger'
  Object.assign(anchor.style, { position: 'fixed', left: '20px', top: '80px', height: '24px' })
  document.body.append(anchor)
  const choices = Array.from({ length: 16 }, (_, index) => ({ value: `choice-${index}`, label: `Choice ${index + 1}`, name: `Choice ${index + 1}`, color: '', on: 0, count: 1 }))
  const components: Record<FloatingList, Component> = { choice: ChoicePicker, group: MoveToGroup, epic: EpicPicker, option: OptionMenu, label: LabelMenu, relation: RelationPicker, facet: ChoiceFacet, business: PickerMenu }
  let props: Record<string, unknown> = { anchor }
  if (kind === 'choice') props = { ...props, label: 'Choose a setting', choices, current: choices[0]!.value }
  if (kind === 'group') props = { ...props, subject: 'Pharos', options: choices.map(choice => ({ group: { id: choice.value, name: choice.label, kind: 'shared', color: '' }, count: 1, current: false })) }
  if (kind === 'epic') props = { ...props, subject: 'PHAROS-11', projectId: 'p-pharos', current: null }
  if (kind === 'option') props = { ...props, title: 'Type', subject: 'PHAROS-11', kind: 'type', options: choices, current: choices[0]!.value, searchable: true }
  if (kind === 'label') props = { ...props, labels: choices, count: 1 }
  if (kind === 'relation') {
    recents.splice(0, recents.length, ...choices.slice(0, 8).map((choice, index) => ({ type: 'ticket' as const, key: `PHAROS-${index + 100}`, title: choice.label, state: 'open', kind: 'ticket', projectKey: 'PHAROS' })))
    props = { ...props, subject: 'PHAROS-11', selfId: 'n11', projectKey: 'PHAROS', related: [], link: async () => null }
  }
  if (kind === 'facet') {
    const selected = reactive<string[]>([])
    props = { label: 'Choices', options: choices, selected, onToggle: (value: string) => { const at = selected.indexOf(value); if (at < 0) selected.push(value); else selected.splice(at, 1) } }
  }
  if (kind === 'business') props = { ...props, title: 'Choose a customer', options: choices }
  const app = (document.querySelector('#app') as HTMLElement & { __vue_app__: App }).__vue_app__
  const vnode = h(components[kind], props)
  vnode.appContext = app._context
  render(vnode, host)
}
