// SPDX-License-Identifier: AGPL-3.0-only
// AEON-1042: the project-header graph is a per-person developer opt-in, off by
// default. Risks: the Display menu keeps offering the switch, an earlier
// list:display "on" revives the graph, or the developer switch saves without
// reaching the header's source of truth.
import { beforeEach, expect, it, vi } from 'vitest'
import { computed, createSSRApp, effectScope, nextTick, reactive, ref } from 'vue'
import { createPinia, setActivePinia } from 'pinia'
import { renderToString } from '@vue/server-renderer'
import type { Identity } from '../src/lib/api'

const session = reactive<{ identity: Identity | null }>({ identity: null })
const display = ref({ effortMeter: true, modelNames: 'short', modelVersion: 'show' })
vi.mock('../src/stores/session', () => ({ useSession: () => session }))
vi.mock('../src/lib/prefs', () => ({ useModelDisplay: () => ({ modelDisplay: display, ready: Promise.resolve(), set: vi.fn() }) }))
vi.mock('../src/lib/preferences', () => ({ readPreference: vi.fn(), writePreference: vi.fn() }))
import { readPreference, writePreference } from '../src/lib/preferences'
import { useDeveloperSettings } from '../src/lib/developerSettings'
import DisplayPanel from '../src/components/work/DisplayPanel.vue'
import DeveloperSection from '../src/components/settings/DeveloperSection.vue'

let id = 0
const person = (): Identity => ({ principal: { id: `person-${++id}`, name: 'Person', kind: 'person' }, tenant: { id: 'workspace', name: 'workspace' } })
beforeEach(() => {
  setActivePinia(createPinia())
  session.identity = person()
  vi.mocked(readPreference).mockReset().mockResolvedValue(null)
  vi.mocked(writePreference).mockReset().mockResolvedValue(true)
})

it('the Display menu has no header graph switch', async () => {
  const filters = { group: 'none', sort: [] } as unknown as InstanceType<typeof DisplayPanel>['$props']['filters']
  for (const sheet of [false, true]) {
    const html = await renderToString(createSSRApp(DisplayPanel, { filters, view: 'list', density: 'comfortable', sheet }))
    expect(html).toContain('Row height')
    expect(html).not.toContain('Graph in project header')
    expect(html).not.toContain('type="checkbox"')
  }
})

it('the developer setting is off by default, also after an earlier list:display choice, and toggles the graph', async () => {
  // The old Display choice lived in list:display; it is not read for the developer opt-in.
  vi.mocked(readPreference).mockImplementation(async key => key === 'list:display' ? { headerGraph: true } : { show_expert_start: true })
  const scope = effectScope()
  const prefs = scope.run(() => useDeveloperSettings())!
  // ProjectView mounts the glimpse only from this value once the read settled.
  const headerGraph = scope.run(() => computed(() => !prefs.loading.value && prefs.showHeaderGraph.value))!
  await prefs.ready.value
  await nextTick()
  expect(readPreference).toHaveBeenCalledWith('developer-ui')
  expect(readPreference).not.toHaveBeenCalledWith('list:display')
  expect(headerGraph.value).toBe(false)
  await prefs.setShowHeaderGraph(true)
  expect(writePreference).toHaveBeenLastCalledWith('developer-ui', { show_expert_start: true, show_header_graph: true })
  expect(headerGraph.value).toBe(true)
  vi.mocked(writePreference).mockResolvedValueOnce(false)
  await prefs.setShowHeaderGraph(false)
  // A failed save keeps the graph as it was and reports the failure.
  expect(headerGraph.value).toBe(true)
  expect(prefs.failed.value).toBe(true)
  await prefs.setShowHeaderGraph(false)
  expect(writePreference).toHaveBeenLastCalledWith('developer-ui', { show_expert_start: true, show_header_graph: false })
  expect(headerGraph.value).toBe(false)
  scope.stop()
})

it('Settings › Developer offers the graph switch, off until the person turns it on', async () => {
  const render = () => renderToString(createSSRApp(DeveloperSection).use(createPinia()))
  const graphSwitch = (html: string) => html.match(/<input[^>]*aria-describedby="header-graph-hint"[^>]*>/)?.[0] ?? ''
  expect(graphSwitch(await render())).toMatch(/role="switch"/)
  expect(graphSwitch(await render())).not.toMatch(/checked/)
  session.identity = person()
  vi.mocked(readPreference).mockResolvedValue({ show_header_graph: true })
  const scope = effectScope()
  await scope.run(() => useDeveloperSettings())!.ready.value
  expect(graphSwitch(await render())).toMatch(/checked/)
  scope.stop()
})
