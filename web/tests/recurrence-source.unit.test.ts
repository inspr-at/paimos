// SPDX-License-Identifier: AGPL-3.0-only
import { afterEach, expect, it, vi } from 'vitest'
import * as Vue from 'vue'
import { readFileSync } from 'node:fs'
import { execFileSync } from 'node:child_process'
import { parse, compileScript } from '@vue/compiler-sfc'
import ts from 'typescript'
import { createScope } from '../src/lib/identityScope'
import * as Recurrences from '../src/lib/recurrences'
import * as workVocabulary from '../src/lib/workVocabulary'
import type { Recurrence } from '../src/lib/recurrences'
import type { ListItem, NodeRecurrence } from '../src/lib/api'

const scopes: Vue.EffectScope[] = []
afterEach(() => { for (const scope of scopes.splice(0)) scope.stop(); vi.unstubAllGlobals() })
const settle = async () => { for (let i = 0; i < 8; i++) await Promise.resolve(); await Vue.nextTick() }
const project = { id: 'source-project', routeKey: 'SOURCE' }
const definition = (id = 'source', projectId = project.id): Recurrence => ({ id, project_id: projectId, parent_id: projectId, template: { name: id, title: id, description: '', acceptance_criteria: [], estimate_hours: 0, priority: 'medium', tags: [], type: 'ticket' }, trigger: { kind: 'time' }, queue_each: false, overlap_policy: 'skip', catch_up_policy: 'one', paused: false, revision: 1, occurrence_count: 4, next_at: null, created_at: '2026-10-01T12:00:00Z', updated_at: '2026-10-01T12:00:00Z' })
const provenance: NodeRecurrence = { id: 'source', project_id: project.id, project_key: project.routeKey, number: 4, retired: false, trigger: { kind: 'time' } }

// Execute real SFC setup code with injected network answers, as the existing
// scope-sheet tests do. Baseline mode reads the reviewed git blob without editing
// production files or changing the assertions.
function source(path: string) {
  return process.env.AEON_637_BASELINE === '1'
    ? execFileSync('git', ['show', `${process.env.AEON_637_BASELINE_REF ?? 'a12c21c2'}:web/src/${path}`], { encoding: 'utf8' })
    : readFileSync(new URL(`../src/${path}`, import.meta.url), 'utf8')
}
function setup<T>(text: string, props: object, modules: Record<string, unknown>): T {
  const { descriptor } = parse(text)
  const { content } = compileScript(descriptor, { id: 'recurrence-source-regression' })
  const { outputText } = ts.transpileModule(content, { compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022 } })
  const exports: { default?: { setup: (props: object, context: object) => T } } = {}
  new Function('require', 'exports', outputText)((id: string) => {
    if (id.endsWith('.vue')) return { default: {} }
    if (!(id in modules)) throw new Error(`Unexpected setup dependency: ${id}`)
    return modules[id]
  }, exports)
  const scope = Vue.effectScope(); scopes.push(scope)
  return scope.run(() => exports.default!.setup(props, { expose: () => {}, emit: () => {} }))!
}
function identityScope() {
  const scope = createScope(() => 'tenant/person')
  Vue.onScopeDispose(() => scope.dispose())
  return { ...scope, owner: Vue.computed(() => 'tenant/person') }
}
function card(getRecurrence: (id: string) => Promise<Recurrence>, selectedId = 'source') {
  const props = Vue.reactive({ project, selectedId })
  const page = Array.from({ length: 100 }, (_, index) => definition(`first-${index}`))
  const get = vi.fn(getRecurrence)
  const state = setup<{ chosen: Vue.ComputedRef<Recurrence | undefined>; selected: Vue.Ref<string>; linkFailure?: Vue.Ref<string>; load: (older?: boolean) => void }>(source('components/recurrences/RecurringWorkCard.vue'), props, {
    vue: { ...Vue, onMounted: () => {}, onBeforeUnmount: () => {} },
    '../../lib/api': { getRecurrence: get, listRecurrences: async (_project: string, after?: string) => ({ items: after ? [definition()] : page, next_cursor: after ? null : 'second' }) },
    '../../lib/authz': { can: () => true, onAccessChange: () => () => {} },
    '../../lib/confirm': { confirmAction: vi.fn() }, '../../lib/toast': { toast: vi.fn() },
    '../../lib/useIdentityScope': { useIdentityScope: identityScope },
    '../../lib/usePolledData': { usePoller: () => ({ start() {}, stop() {} }) },
    '../../lib/recurrences': Recurrences,
  })
  return { props, state, get }
}

it('selects the requested source outside page one and keeps it through pagination', async () => {
  const { state, get } = card(async () => definition())
  await settle()
  expect(state.chosen.value?.id).toBe('source')
  expect(get).toHaveBeenCalledWith('source', expect.any(AbortSignal))
  state.load(true); await settle()
  expect(state.selected.value).toBe('source')
  expect(state.chosen.value?.id).toBe('source')
})

it('rejects a source from another project without choosing the first row', async () => {
  const { state } = card(async () => definition('source', 'other-project'))
  await settle()
  expect(state.chosen.value).toBeUndefined()
  expect(state.linkFailure?.value).toBe('This recurrence belongs to another project.')
})

it('drops a late source response when the selected source changes', async () => {
  let answer!: (item: Recurrence) => void
  const { props, state, get } = card(id => id === 'source' ? new Promise(resolve => { answer = resolve }) : Promise.resolve(definition(id)))
  await settle()
  props.selectedId = 'next-source'; await settle()
  expect(state.chosen.value?.id).toBe('next-source')
  expect(get).toHaveBeenCalledWith('next-source', expect.any(AbortSignal))
  answer(definition()); await settle()
  expect(state.chosen.value?.id).toBe('next-source')
})

function ticket(fields: Record<string, unknown>, recurrence: NodeRecurrence | null = provenance, options: { manage?: boolean; getRecurrence?: (id: string, signal: AbortSignal) => Promise<Recurrence> } = {}) {
  const item = Vue.ref({ id: 'occurrence', kind_slug: 'ticket', fields, recurrence: recurrence ? structuredClone(recurrence) : undefined } as unknown as ListItem)
  const props = Vue.reactive({ item: item.value, project: { id: 'destination-project', routeKey: 'DEST' } })
  const text = source('components/work/TicketWorkspace.vue')
  // The recurrence block is executed verbatim; unrelated ticket editors are not
  // needed to establish which record/project its source-edit callback targets.
  const block = text.slice(text.indexOf('const repeatSource ='), text.indexOf('const humanCheckPerson ='))
  expect(block.length).toBeGreaterThan(500)
  const manage = Vue.ref(options.manage ?? true)
  const get = vi.fn(options.getRecurrence ?? (async (id: string) => definition(id)))
  const state = setup<{ originRecurrence: Vue.Ref<Recurrence | null>; recurrenceEdit: Vue.Ref<Recurrence | null>; sourceProject?: Vue.ComputedRef<typeof project | null>; mayRepeat: Vue.ComputedRef<boolean>; repeatSource: Vue.Ref<ListItem | null>; repeat: () => void; originLoaded: (value: Recurrence) => void; editRecurrence: () => void }>(`<script setup lang="ts">
import { ref, computed, watch } from 'vue'
import { item, props, ticket, can, getRecurrence, useIdentityScope, recurrenceName, toast, queueRouter } from 'fixture'
${block}
</script>`, {}, {
    vue: Vue,
    fixture: { item, props, ticket: { gone: Vue.ref(false), readOnly: Vue.ref(false) }, can: (_permission: string, projectId: string) => projectId === project.id ? manage.value : true, getRecurrence: get, useIdentityScope: identityScope, recurrenceName: Recurrences.recurrenceName, toast: vi.fn(), queueRouter: { push: vi.fn() } },
  })
  return { item, props, state, get, manage }
}

it('editable metadata cannot create source-edit state without authoritative provenance', () => {
  const { state } = ticket({ recurrence_id: 'editable-fake', occurrence_number: 99 }, null)
  state.originLoaded(definition('editable-fake'))
  expect(state.originRecurrence.value).toBeNull()
  state.editRecurrence()
  expect(state.recurrenceEdit.value).toBeNull()
})

it('a moved ticket edits the receipt source project and ignores conflicting metadata', async () => {
  const { state, get } = ticket({ recurrence_id: 'editable-fake', occurrence_number: 99 })
  state.originLoaded(definition())
  state.editRecurrence(); await settle()
  expect(state.recurrenceEdit.value?.id).toBe('source')
  expect(get).toHaveBeenCalledWith('source', expect.any(AbortSignal))
  expect(state.sourceProject?.value).toEqual(project)
})

it.each(['withheld', 'retired'])('%s provenance clears loaded source and open source-edit state', async stateChange => {
  const { item, state } = ticket({ recurrence_id: 'source', occurrence_number: 4 })
  state.originLoaded(definition()); state.editRecurrence(); await settle()
  expect(state.recurrenceEdit.value?.id).toBe('source')
  if (stateChange === 'retired') item.value.recurrence!.retired = true
  else item.value.recurrence = undefined
  expect(state.originRecurrence.value).toBeNull()
  expect(state.recurrenceEdit.value).toBeNull()
})

it('withholding provenance drops an in-flight source-edit response', async () => {
  // The source read is delayed by a barrier rather than a timer.
  const item = Vue.ref({ id: 'occurrence', kind_slug: 'ticket', fields: { recurrence_id: 'source' }, recurrence: structuredClone(provenance) } as unknown as ListItem)
  let answer!: (value: Recurrence) => void
  const text = source('components/work/TicketWorkspace.vue')
  const block = text.slice(text.indexOf('const repeatSource ='), text.indexOf('const humanCheckPerson ='))
  const state = setup<{ originLoaded: (value: Recurrence) => void; editRecurrence: () => void; recurrenceEdit: Vue.Ref<Recurrence | null> }>(`<script setup lang="ts">
import { ref, computed, watch } from 'vue'
import { item, props, ticket, can, getRecurrence, useIdentityScope, recurrenceName, toast, queueRouter } from 'fixture'
${block}
</script>`, {}, { vue: Vue, fixture: { item, props: Vue.reactive({ item: item.value, project }), ticket: { gone: Vue.ref(false), readOnly: Vue.ref(false) }, can: () => true, getRecurrence: () => new Promise<Recurrence>(resolve => { answer = resolve }), useIdentityScope: identityScope, recurrenceName: Recurrences.recurrenceName, toast: vi.fn(), queueRouter: { push: vi.fn() } } })
  state.originLoaded(definition()); state.editRecurrence()
  item.value.recurrence = undefined
  answer(definition()); await settle()
  expect(state.recurrenceEdit.value).toBeNull()
})

it('delayed source permissions preserve loaded provenance and enable editing', async () => {
  const { state, get, manage } = ticket({}, provenance, { manage: false })
  const loaded = definition()
  state.originLoaded(loaded)
  expect(state.originRecurrence.value).toEqual(loaded)
  state.editRecurrence()
  expect(get).not.toHaveBeenCalled()
  expect(state.recurrenceEdit.value).toBeNull()
  manage.value = true
  expect(state.originRecurrence.value).toEqual(loaded)
  state.editRecurrence(); await settle()
  expect(get).toHaveBeenCalledWith('source', expect.any(AbortSignal))
  expect(state.recurrenceEdit.value).toEqual(loaded)
})

it('source permission revocation closes the editor and permits a fresh edit after regrant', async () => {
  const { state, get, manage } = ticket({})
  state.originLoaded(definition()); state.editRecurrence(); await settle()
  expect(state.recurrenceEdit.value?.id).toBe('source')
  manage.value = false
  expect(state.recurrenceEdit.value).toBeNull()
  state.editRecurrence()
  expect(get).toHaveBeenCalledTimes(1)
  manage.value = true
  expect(state.recurrenceEdit.value).toBeNull()
  state.editRecurrence(); await settle()
  expect(get).toHaveBeenCalledTimes(2)
  expect(state.recurrenceEdit.value?.id).toBe('source')
})

it('source permission revocation cancels an in-flight edit even after regrant', async () => {
  let answer!: (value: Recurrence) => void
  const { state, get, manage } = ticket({}, provenance, { getRecurrence: () => new Promise(resolve => { answer = resolve }) })
  state.originLoaded(definition()); state.editRecurrence()
  expect(get).toHaveBeenCalledWith('source', expect.any(AbortSignal))
  const signal = get.mock.calls[0]![1]
  expect(signal.aborted).toBe(false)
  manage.value = false
  expect(signal.aborted).toBe(true)
  manage.value = true
  answer(definition()); await settle()
  expect(state.recurrenceEdit.value).toBeNull()
})

it('canonical leaves and parents can repeat, and identity changes close the bound snapshot', async () => {
  const { item, props, state } = ticket({}, null)
  for (const is_leaf of [true, false]) {
    item.value = { ...item.value, kind_slug: 'work', is_leaf, work_children_count: is_leaf ? 0 : 2, fields: { description: 'Original' } }
    expect(state.mayRepeat.value).toBe(true)
    state.repeat()
    expect(state.repeatSource.value).toMatchObject({ id: item.value.id, kind_slug: 'work', is_leaf })
    expect(state.repeatSource.value!.fields).not.toBe(item.value.fields)
    item.value.fields.description = 'Changed'
    expect(state.repeatSource.value!.fields.description).toBe('Original')
  }
  item.value = { ...item.value, id: 'next-work' }
  props.item = item.value
  await settle()
  expect(state.repeatSource.value).toBeNull()
})

it('Repeat creates children of a canonical parent and siblings of a canonical leaf', () => {
  vi.stubGlobal('navigator', { platform: 'Mac' })
  for (const is_leaf of [false, true]) {
    const sourceItem = { id: 'work-source', key: 'WORK-1', title: 'Recurring work', kind_slug: 'work', parent_id: 'existing-parent', is_leaf, work_children_count: is_leaf ? 0 : 2, fields: {}, body: '', priority: 'medium' } as ListItem
    const state = setup<{ parent: Vue.Ref<string>; parentLabel: Vue.Ref<string>; input: Vue.ComputedRef<Recurrences.RecurrenceInput> }>(source('components/recurrences/RecurrenceEditor.vue'), { project, source: sourceItem }, {
      vue: { ...Vue, onMounted: () => {}, onBeforeUnmount: Vue.onScopeDispose },
      '../../lib/api': { createRecurrence: vi.fn(), updateRecurrence: vi.fn(), getNode: vi.fn(), previewRecurrenceDraft: async () => ({ times: [] }) },
      '../../lib/authz': { can: () => true }, '../../lib/useIdentityScope': { useIdentityScope: identityScope }, '../../lib/recurrences': Recurrences,
      '../../stores/workVocabulary': { useWorkVocabulary: () => ({ leaf: { name: 'Arbeitsschritt', icon: 'check' } }) }, '../../lib/workVocabulary': workVocabulary,
    })
    expect(state.parent.value).toBe(is_leaf ? 'existing-parent' : 'work-source')
    expect(state.input.value.template.type).toBe('work')
    if (!is_leaf) expect(state.parentLabel.value).toBe('WORK-1 · Recurring work')
  }
})
