<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, nextTick, ref, watch } from 'vue'
import { useRoute } from 'vue-router'
import AppIcon from '../AppIcon.vue'
import ModelRow from './models/ModelRow.vue'
import ModelChain from './models/ModelChain.vue'
import ModelPicker from './models/ModelPicker.vue'
import KindMenu from './models/KindMenu.vue'
import ModelsPopover from './models/ModelsPopover.vue'
import ModelRefreshSettings from './ModelRefreshSettings.vue'
import ModelRegistryCard from './models/ModelRegistryCard.vue'
import { useModelsSimple } from '../../lib/useModelsSimple'
import { cantSentence, entryFor, lockTip, neverWho, orderLines, stepStates, type ChainEnd, type ModelEntry, type RowView, type SimpleNever, type WhyView } from '../../lib/modelsSimple'

// Settings › Models, minimal (AEON-999 v3): one default for all work, overrides only where it matters, reviews pick
// themselves, and one line on what runs next. The stored board, rules and situations keep working underneath.
const route = useRoute()
const model = useModelsSimple()
const { scope, doc, rows, entries, routes, next, news, busy, error, failed, announcement, draft, admin, editable, readable, owner, actionKey } = model
const defaultRow = computed(() => rows.value[0])
const exceptions = computed(() => rows.value.slice(1))
const shown = computed(() => !!doc.value)
const fresh = computed(() => new Set((doc.value?.new_lines ?? []).map(item => item.line)))
const person = computed(() => doc.value?.person_id ?? null)
const catalog = ref(route.hash === '#model-refresh')

// ----- menus ------------------------------------------------------------------------------------------------
const picker = ref<{ key: string; anchor: HTMLElement } | null>(null)
const kinds = ref<{ anchor: HTMLElement; use?: { entry: ModelEntry; canDo: string[] } } | null>(null)
const why = ref<HTMLElement | null>(null)
const pickerPop = ref<InstanceType<typeof ModelsPopover> | null>(null)
const chainPickerPop = ref<InstanceType<typeof ModelsPopover> | null>(null)
// The Why of the ticket a person clicked on a ticket row, anchored at the line about what runs next.
const ticketWhy = ref<{ view: WhyView; anchor: HTMLElement } | null>(null)
const pickRow = computed(() => picker.value ? rows.value.find(row => row.key === picker.value!.key) ?? null : null)
function closeMenus() { picker.value = null; kinds.value = null; why.value = null; ticketWhy.value = null; chainPick.value = null }
watch(actionKey, closeMenus)
watch(pickRow, row => { if (picker.value && !row) picker.value = null })

function openPicker(row: RowView, anchor: HTMLElement) {
  if (picker.value?.key === row.key) { picker.value = null; return }
  closeMenus(); picker.value = { key: row.key, anchor }
}
// Back to the row that was picked; when it is gone (a draft that matched the default), to the add button.
function refocus(key: string) { void nextTick(() => (document.getElementById(`pk-${key}`) ?? document.getElementById('add'))?.focus({ preventScroll: true })) }
async function choose(entry: ModelEntry, effort: string | null) {
  const row = pickRow.value, key = picker.value?.key
  picker.value = null
  if (!row || !key) return
  const done = await model.pick(row, entry, effort)
  if (!done && draft.value === row.column) model.discardException()
  refocus(key)
}
// Esc or a click elsewhere takes a row that was never picked for away again.
function pickerClosed() { if (draft.value && picker.value?.key === draft.value) model.discardException(); picker.value = null }
// A row that goes leaves focus on the add button; a row that resets keeps it on its picker.
async function removeRow(row: RowView) { if (await model.clear(row)) void nextTick(() => document.getElementById('add')?.focus({ preventScroll: true })) }
async function resetRow(row: RowView) { if (await model.clear(row)) refocus(row.key) }
async function lockRow(on: boolean) {
  const row = pickRow.value, key = picker.value?.key
  picker.value = null
  if (row) await model.lock(row, on)
  if (key) refocus(key)
}
const calm = () => typeof matchMedia === 'function' && matchMedia('(prefers-reduced-motion: reduce)').matches

// ----- fallback folds (AEON-1161) ---------------------------------------------------------------------------------
// Each row's order opens in place under its pick. Steps are picked in the same menu as rows, offering only lines the
// order does not hold yet; every edit saves at once with Undo, and focus returns to the control that made it.
const folds = ref(new Set<string>())
function toggleFold(key: string) { const open = new Set(folds.value); if (open.has(key)) open.delete(key); else open.add(key); folds.value = open }
const chainPick = ref<{ key: string; index: number | null; anchor: HTMLElement } | null>(null)
const chainRow = computed(() => chainPick.value ? rows.value.find(row => row.key === chainPick.value!.key && !row.draft) ?? null : null)
watch(chainRow, row => { if (chainPick.value && !row) chainPick.value = null })
const picking = (row: RowView) => chainPick.value?.key === row.key ? chainPick.value.index ?? 'add' : null
const states = computed(() => new Map(rows.value.map(row => [row.key, stepStates(row, doc.value?.unavailable ?? [])] as const)))
function openStep(row: RowView, index: number | null, anchor: HTMLElement) {
  if (chainPick.value?.key === row.key && chainPick.value.index === index) { chainPick.value = null; return }
  closeMenus(); chainPick.value = { key: row.key, index, anchor }
}
const stepFor = computed(() => {
  const open = chainPick.value, row = chainRow.value
  if (!open || !row) return null
  const kind = row.isDefault ? 'the default' : row.label, step = open.index === null ? null : row.chain[open.index]
  if (open.index !== null && !step) return null
  // A line runs once per order: the menu offers the lines the order does not hold yet, and the step's own.
  const taken = new Set(orderLines(row).filter((_, index) => open.index === null || index !== open.index + 1))
  return {
    label: open.index === null ? `Add a fallback for ${kind}` : `Step ${open.index + 2} for ${kind}`, name: open.index === null ? `a fallback for ${kind}` : `step ${open.index + 2} for ${kind}`,
    row: step ? { ...row, line: step.line, effort: step.effort, entry: entryFor(entries.value, step.line) } : { ...row, line: null },
    entries: entries.value.filter(entry => !taken.has(entry.line)), column: row.column,
  }
})
const refocusSoon = (selector: string) => void nextTick(() => document.querySelector<HTMLElement>(selector)?.focus({ preventScroll: true }))
async function chooseStep(entry: ModelEntry, effort: string | null) {
  const open = chainPick.value, row = chainRow.value
  chainPick.value = null
  if (!open || !row) return
  await model.editChain(row, open.index === null ? { kind: 'add', line: entry.line, effort } : { kind: 'step', index: open.index, line: entry.line, effort })
  refocusSoon(open.index === null ? `#chadd-${row.key}` : `#chp-${row.key}-${open.index}`)
}
async function moveStep(row: RowView, from: number, to: number, keyboard: boolean) {
  const done = await model.editChain(row, { kind: 'move', from, to })
  if (keyboard) refocusSoon(`[data-move="${row.key}:${done ? to : from}"]`)
}
async function removeStep(row: RowView, index: number) { if (await model.editChain(row, { kind: 'remove', index })) refocusSoon(`#chadd-${row.key}`) }
async function setEnd(row: RowView, end: ChainEnd) { if (await model.editChain(row, { kind: 'end', end })) refocusSoon(`[data-end="${row.key}:${end}"]`) }
async function allowLine(row: RowView, never: SimpleNever) { if (await model.allowAgain(row, never)) refocusSoon(`[data-fold="${row.key}"]`) }
const neverBy = (never: SimpleNever) => neverWho(never, id => model.names.value.get(id) ?? (id === person.value ? 'You' : 'An admin'))
const canAllow = (row: RowView) => (never: SimpleNever) => model.canAllow(row, never)

function openAdd(event: MouseEvent) { const anchor = event.currentTarget as HTMLElement; if (kinds.value?.anchor === anchor && !kinds.value.use) { kinds.value = null; return } closeMenus(); kinds.value = { anchor } }
function openUse(event: MouseEvent) {
  const anchor = event.currentTarget as HTMLElement, item = news.value
  if (!item) return
  if (kinds.value?.anchor === anchor) { kinds.value = null; return }
  closeMenus(); kinds.value = { anchor, use: { entry: item.entry, canDo: item.canDo } }
}
const menuKinds = computed(() => {
  const open = kinds.value
  if (!open) return []
  if (open.use) return model.kinds.value.filter(kind => open.use!.canDo.includes(kind.column)).map(kind => ({ column: kind.column, label: kind.label }))
  return model.freeKinds.value.map(kind => ({ column: kind.column, label: kind.label }))
})
const menuNote = computed(() => {
  const open = kinds.value?.use
  if (!open) return ''
  const missing = model.kinds.value.filter(kind => !open.canDo.includes(kind.column))
  if (!missing.length) return ''
  const reasons = [...new Set(missing.map(kind => model.cantReason(kind.column, open.entry.line)).filter(Boolean))]
  return `${missing.map(kind => kind.label).join(', ')} ${missing.length === 1 ? 'isn’t' : 'aren’t'} offered${reasons.length ? `: ${reasons.map(reason => reason.charAt(0).toLowerCase() + reason.slice(1)).join('; ')}` : ''}.`
})
async function chooseKind(column: string) {
  const open = kinds.value
  kinds.value = null
  if (!open) return
  if (open.use) { await model.use(open.use.entry, column); return }
  model.startException(column)
  await nextTick()
  if (!calm()) await new Promise(resolve => setTimeout(resolve, 260))
  const anchor = document.getElementById(`pk-${column}`)
  if (anchor && draft.value === column) picker.value = { key: column, anchor }
}
async function notNow() { if (news.value) await model.dismiss(news.value.entry.line) }

// ----- text -------------------------------------------------------------------------------------------------
const tip = (row: RowView) => row.lock ? lockTip(row.lock, { mine: !!row.lock.by && row.lock.by === person.value, name: id => model.names.value.get(id) ?? 'An admin', admin: admin.value, scope: scope.value }) : ''
const cant = (row: RowView) => cantSentence(row.unavailable, entries.value)
// ?why=1 (a ticket's "Why this model?" link) explains the ticket that was clicked, once per link (Tick, AEON-1138): the
// queued trace only when it is that ticket, else the ticket's own resolution. Without a ticket it opens the queued trace.
// A query-only navigation keeps this page, so another link closes the explanation of the last one and asks again; an
// answer for a link no longer in the address is dropped (code health rule 4).
let whyLink: string | null = null, whyDone = false, whyTurn = 0
watch(() => [route.query.why, route.query.ticket, shown.value, !!next.value.why], async () => {
  const ticket = typeof route.query.ticket === 'string' ? route.query.ticket : ''
  const link = route.query.why === '1' ? ticket : null
  if (link !== whyLink) { whyLink = link; whyDone = false; whyTurn++; why.value = null; ticketWhy.value = null }
  if (link === null || !shown.value || whyDone) return
  const turn = whyTurn, still = () => turn === whyTurn
  if (!ticket || ticket === doc.value?.next?.ticket) {
    if (!next.value.why) return
    whyDone = true; await nextTick(); if (still()) why.value = document.getElementById('why'); return
  }
  whyDone = true
  const shownFor = actionKey.value, view = await model.explain(ticket, still)
  if (!view || !still() || shownFor !== actionKey.value) return
  await nextTick()
  const anchor = document.querySelector<HTMLElement>('[data-next-line]')
  if (anchor && still()) { closeMenus(); ticketWhy.value = { view, anchor } }
}, { immediate: true })
// #model-refresh opens the catalog fold and scrolls to the model registry card under the models card.
watch(() => [route.hash, shown.value], async () => {
  if (route.hash !== '#model-refresh' || !shown.value) return
  catalog.value = true
  await nextTick(); document.getElementById('model-refresh')?.scrollIntoView({ block: 'start' })
}, { immediate: true })
</script>
<template>
  <div v-if="readable" class="models-section" data-models-section :data-models-ready="shown ? 'true' : 'false'">
    <!-- The catalog toggle stays above the models card, so a refusal, a new row or a skeleton below never moves it (AEON-541).
         The model registry card (AEON-1012) sits under that card and is the #model-refresh target. Catalog refresh settings
         stay one fold away and do not take that anchor. -->
    <div class="m-fold">
      <button type="button" class="fold" data-catalog-fold :aria-expanded="catalog" aria-controls="catalog-body" @click="catalog = !catalog"><AppIcon name="chevron-right" :size="14" :class="{ open: catalog }" /><b>Model catalog</b><span>How new models are found</span></button>
      <div v-if="catalog" id="catalog-body"><ModelRefreshSettings :key="owner" /></div>
    </div>
    <section class="glass-card m-card" aria-labelledby="m-title" :aria-busy="busy || undefined">
      <div class="m-head">
        <div class="titles"><h2 id="m-title">Models</h2><p class="lead">One default for all work, overrides only where you care.</p></div>
        <div v-if="admin" class="seg scope" role="group" aria-label="Who this changes" data-scope-group>
          <button type="button" data-scope="default" :aria-pressed="scope === 'default'" @click="model.setScope('default')">For everyone</button>
          <button type="button" data-scope="me" :aria-pressed="scope === 'me'" @click="model.setScope('me')">Just me</button>
        </div>
      </div>
      <div v-if="failed && !shown" class="m-err" role="alert"><AppIcon name="alert" :size="18" /><p>Models couldn’t load. Agents keep running on the last saved choices.</p><button type="button" class="btn" data-retry @click="model.load()"><AppIcon name="refresh" :size="14" />Try again</button></div>
      <div v-else-if="!shown" class="m-list" aria-busy="true" aria-label="Loading models" role="status"><div class="row def"><span class="sk" style="width: 120px" /><span class="sk f" style="grid-area: p" /></div><div v-for="n in 2" :key="n" class="row"><span class="sk" style="width: 96px" /><span class="sk f" style="grid-area: p" /></div><div class="next"><span class="sk" style="width: 70%" /></div></div>
      <template v-else>
        <Transition name="rw"><div v-if="news" class="rw" data-news><div class="rw-in"><div class="news-in"><AppIcon name="sparkle" :size="15" /><span class="grow"><b>{{ news.entry.name }}</b> is new. <button id="news-use" type="button" class="link-btn" aria-haspopup="listbox" :aria-expanded="!!kinds?.use" data-news-use @click="openUse">Use it for…</button></span><button type="button" class="icon-btn sm flat" data-news-x aria-label="Not now" data-tip="Not now" :disabled="busy" @click="notNow"><AppIcon name="close" :size="14" /></button></div></div></div></Transition>
        <div class="m-list">
          <ModelRow v-if="defaultRow" :row="defaultRow" :expanded="picker?.key === defaultRow.key" :lock-tip="tip(defaultRow)" :cant="cant(defaultRow)" @open="openPicker(defaultRow, $event)" @reset="resetRow(defaultRow)" @remove="removeRow(defaultRow)">
            <ModelChain
              v-if="defaultRow.line" :row="defaultRow" :base="defaultRow" :entries="entries" :routes="routes" :states="states.get(defaultRow.key) ?? new Map()" :open="folds.has(defaultRow.key)" :everyone="scope === 'default'" :admin="admin" :fixed="model.fixedSteps(defaultRow)" :picking="picking(defaultRow)" :allow="canAllow(defaultRow)" :who="neverBy"
              @toggle="toggleFold(defaultRow.key)" @move="(from, to, keyboard) => moveStep(defaultRow!, from, to, keyboard)" @remove="index => removeStep(defaultRow!, index)" @pick="(index, anchor) => openStep(defaultRow!, index, anchor)" @end="end => setEnd(defaultRow!, end)" @allow="never => allowLine(defaultRow!, never)"
            />
          </ModelRow>
          <div class="ex-group" role="group" aria-labelledby="ex-h">
            <p v-if="exceptions.length" id="ex-h" class="ex-h">Except for</p><span v-else id="ex-h" class="sr-only">Overrides</span>
            <TransitionGroup name="rw">
              <div v-for="row in exceptions" :key="row.key" class="rw" :data-key="row.key"><div class="rw-in"><ModelRow :row="row" :expanded="picker?.key === row.key" :lock-tip="tip(row)" :cant="cant(row)" @open="openPicker(row, $event)" @reset="resetRow(row)" @remove="removeRow(row)">
                <ModelChain
                  v-if="row.line && !row.draft" :row="row" :base="defaultRow" :entries="entries" :routes="routes" :states="states.get(row.key) ?? new Map()" :open="folds.has(row.key)" :everyone="scope === 'default'" :admin="admin" :fixed="model.fixedSteps(row)" :picking="picking(row)" :allow="canAllow(row)" :who="neverBy"
                  @toggle="toggleFold(row.key)" @move="(from, to, keyboard) => moveStep(row, from, to, keyboard)" @remove="index => removeStep(row, index)" @pick="(index, anchor) => openStep(row, index, anchor)" @end="end => setEnd(row, end)" @allow="never => allowLine(row, never)"
                />
              </ModelRow></div></div>
            </TransitionGroup>
            <div v-if="editable && model.freeKinds.value.length" class="addrow"><button id="add" type="button" class="add" aria-haspopup="listbox" :aria-expanded="!!kinds && !kinds.use" data-add :disabled="busy" @click="openAdd"><AppIcon name="plus" :size="15" /><span>Different model for…</span></button></div>
          </div>
          <div class="row rv" data-reviews>
            <div class="rl"><span class="rn">Reviews</span></div>
            <div class="rv-v"><span class="hg" aria-hidden="true"><AppIcon name="compare" :size="13" /></span><span>Automatic · always another family</span><span class="grow" /><RouterLink class="link-btn quiet" to="/settings/policies">Rule in Policies</RouterLink></div>
          </div>
        </div>
        <p class="next" data-next-line><AppIcon name="arrow" :size="14" /><span class="next-copy"><template v-for="(part, index) in next.parts" :key="index"><b v-if="part.strong">{{ part.text }}</b><template v-else>{{ part.text }}</template></template></span><button v-if="next.why" id="why" type="button" class="link-btn" aria-haspopup="dialog" :aria-expanded="!!why" data-why @click="why = why ? null : ($event.currentTarget as HTMLElement)">Why?</button></p>
        <!-- A refusal grows under the rows. Putting it above them shifts every picker (AEON-541). -->
        <div v-if="error" class="m-note" role="alert" data-models-error><AppIcon name="alert" :size="14" /><span>{{ error }}</span></div>
      </template>
    </section>
    <ModelRegistryCard />
    <p class="sr-only" role="status" aria-live="polite">{{ announcement }}</p>
    <ModelsPopover v-if="pickRow && picker" ref="pickerPop" :open="true" :anchor="picker.anchor" :label="`Model for ${pickRow.isDefault ? 'the default' : pickRow.label}`" role="presentation" :width="Math.max(picker.anchor.offsetWidth, 540)" @close="pickerClosed">
      <ModelPicker :entries="entries" :row="pickRow" :name="pickRow.isDefault ? 'the default' : pickRow.label" :cant="line => model.cantReason(pickRow!.column, line)" :fresh="fresh" :lock="scope === 'default' && admin && !pickRow.draft" :locked="!!pickRow.lock" @choose="choose" @lock="lockRow" @grew="pickerPop?.place()" />
    </ModelsPopover>
    <ModelsPopover v-if="chainPick && stepFor" ref="chainPickerPop" :open="true" :anchor="chainPick.anchor" :label="stepFor.label" role="presentation" :width="Math.max(chainPick.anchor.offsetWidth, 540)" @update:open="value => { if (!value) chainPick = null }">
      <ModelPicker :entries="stepFor.entries" :row="stepFor.row" :name="stepFor.name" :cant="line => model.cantReason(stepFor!.column, line)" :fresh="fresh" @choose="chooseStep" @grew="chainPickerPop?.place()" />
    </ModelsPopover>
    <ModelsPopover v-if="kinds" :open="true" :anchor="kinds.anchor" label="Kinds of work" role="presentation" :width="280" @update:open="value => { if (!value) kinds = null }">
      <KindMenu :kinds="menuKinds" :note="menuNote" :link="!kinds.use" @choose="chooseKind" @navigate="closeMenus" />
    </ModelsPopover>
    <ModelsPopover v-if="why && next.why" :open="true" :anchor="why" :label="next.why.title" kind="why" labelledby="why-t" @update:open="value => { if (!value) why = null }">
      <h3 id="why-t">{{ next.why.title }}</h3>
      <ol><li v-for="(step, index) in next.why.steps" :key="index" :class="{ skip: step[0]?.text.startsWith('Skipped') }"><template v-for="(part, at) in step" :key="at"><b v-if="part.strong">{{ part.text }}</b><template v-else>{{ part.text }}</template></template></li></ol>
    </ModelsPopover>
    <ModelsPopover v-if="ticketWhy" :open="true" :anchor="ticketWhy.anchor" :label="ticketWhy.view.title" kind="why" labelledby="ticket-why-t" @update:open="value => { if (!value) ticketWhy = null }">
      <h3 id="ticket-why-t">{{ ticketWhy.view.title }}</h3>
      <ol data-ticket-why><li v-for="(step, index) in ticketWhy.view.steps" :key="index" :class="{ skip: step[0]?.text.startsWith('Skipped') }"><template v-for="(part, at) in step" :key="at"><b v-if="part.strong">{{ part.text }}</b><template v-else>{{ part.text }}</template></template></li></ol>
    </ModelsPopover>
  </div>
</template>
<style src="../../styles/models.css"></style>
