<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, nextTick, ref, watch } from 'vue'
import { useRoute } from 'vue-router'
import AppIcon from '../AppIcon.vue'
import ModelRow from './models/ModelRow.vue'
import ModelPicker from './models/ModelPicker.vue'
import KindMenu from './models/KindMenu.vue'
import ModelsPopover from './models/ModelsPopover.vue'
import ModelRefreshSettings from './ModelRefreshSettings.vue'
import ModelRegistryCard from './models/ModelRegistryCard.vue'
import { useModelsSimple } from '../../lib/useModelsSimple'
import { lineFallback, lockTip, pickText, reasonText, type ModelEntry, type RowView } from '../../lib/modelsSimple'

// Settings › Models, minimal (AEON-999 v3): one default for all work, overrides only where it matters, reviews pick
// themselves, and one line on what runs next. The stored board, rules and situations keep working underneath.
const route = useRoute()
const model = useModelsSimple()
const { scope, doc, rows, entries, next, news, busy, error, failed, announcement, draft, admin, editable, readable, owner, actionKey } = model
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
const pickRow = computed(() => picker.value ? rows.value.find(row => row.key === picker.value!.key) ?? null : null)
function closeMenus() { picker.value = null; kinds.value = null; why.value = null }
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
const instead = (row: RowView) => row.unavailable ? pickText(entries.value.find(entry => entry.line === row.unavailable!.runs_instead) ?? null, row.unavailable.effort, lineFallback(row.unavailable.runs_instead)) : ''
// ?why=1 (a ticket's "Why this model?" link) opens the trace for the next queued ticket, once.
let whyOpened = false
watch(() => [route.query.why, shown.value, !!next.value.why], async () => {
  if (route.query.why !== '1' || !shown.value || !next.value.why || whyOpened) return
  whyOpened = true; await nextTick(); why.value = document.getElementById('why')
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
          <ModelRow v-if="defaultRow" :row="defaultRow" :expanded="picker?.key === defaultRow.key" :lock-tip="tip(defaultRow)" :instead="instead(defaultRow)" :reason="defaultRow.unavailable ? reasonText(defaultRow.unavailable.reason) : ''" @open="openPicker(defaultRow, $event)" @reset="resetRow(defaultRow)" @remove="removeRow(defaultRow)" />
          <div class="ex-group" role="group" aria-labelledby="ex-h">
            <p v-if="exceptions.length" id="ex-h" class="ex-h">Except for</p><span v-else id="ex-h" class="sr-only">Overrides</span>
            <TransitionGroup name="rw">
              <div v-for="row in exceptions" :key="row.key" class="rw" :data-key="row.key"><div class="rw-in"><ModelRow :row="row" :expanded="picker?.key === row.key" :lock-tip="tip(row)" :instead="instead(row)" :reason="row.unavailable ? reasonText(row.unavailable.reason) : ''" @open="openPicker(row, $event)" @reset="resetRow(row)" @remove="removeRow(row)" /></div></div>
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
    <ModelsPopover v-if="pickRow && picker" :open="true" :anchor="picker.anchor" :label="`Model for ${pickRow.isDefault ? 'the default' : pickRow.label}`" role="presentation" :width="Math.max(picker.anchor.offsetWidth, 520)" @close="pickerClosed">
      <ModelPicker :entries="entries" :row="pickRow" :name="pickRow.isDefault ? 'the default' : pickRow.label" :cant="line => model.cantReason(pickRow!.column, line)" :fresh="fresh" :lock="scope === 'default' && admin && !pickRow.draft" :locked="!!pickRow.lock" @choose="choose" @lock="lockRow" />
    </ModelsPopover>
    <ModelsPopover v-if="kinds" :open="true" :anchor="kinds.anchor" label="Kinds of work" role="presentation" :width="280" @update:open="value => { if (!value) kinds = null }">
      <KindMenu :kinds="menuKinds" :note="menuNote" :link="!kinds.use" @choose="chooseKind" @navigate="closeMenus" />
    </ModelsPopover>
    <ModelsPopover v-if="why && next.why" :open="true" :anchor="why" :label="next.why.title" kind="why" labelledby="why-t" @update:open="value => { if (!value) why = null }">
      <h3 id="why-t">{{ next.why.title }}</h3>
      <ol><li v-for="(step, index) in next.why.steps" :key="index" :class="{ skip: step[0]?.text.startsWith('Skipped') }"><template v-for="(part, at) in step" :key="at"><b v-if="part.strong">{{ part.text }}</b><template v-else>{{ part.text }}</template></template></li></ol>
    </ModelsPopover>
  </div>
</template>
<style src="../../styles/models.css"></style>
