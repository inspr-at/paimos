<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, ref, watch } from 'vue'
import AppIcon from '../../AppIcon.vue'
import HarnessMark from '../../agents/HarnessMark.vue'
import ModelRegistryEditor, { type RemoveQuestion } from './ModelRegistryEditor.vue'
import { can, onAccessChange } from '../../../lib/authz'
import { createScope, scopeOwner } from '../../../lib/identityScope'
import { toast } from '../../../lib/toast'
import { listWorkKinds } from '../../../lib/workKinds'
import { useSession } from '../../../stores/session'
import {
  RegistryError, UNDONE_REASON, addLine, buildLines, changeOf, checkNow, cooldownLabel, draftOf, editLine, emptyDraft, getRefreshStatus, hasErrors, intervalLabel, lastChecked, lineWriteOf,
  listProfiles, metaParts, modelSlug, previewRemoval, putRefreshSettings, removeLine, restoreLine, shortDate, splitLevels, useText, validateDraft,
  type DraftErrors, type LineDraft, type LineUsage, type RefreshStatus, type RegistryLine, type RegistryProfile,
} from '../../../lib/modelRegistry'

// Settings › Models › Model registry (AEON-1012). Admins edit, everyone else reads.
// Editing happens inline under the entry, never in a dialog, and nothing above the
// control that was used ever moves: the editor, the remove question and every
// message open below it.
const COOLDOWN_MS = 5 * 60_000
const session = useSession()
const owner = computed(() => scopeOwner(session.identity))
const readable = computed(() => session.authenticationCurrent() && can('models.read'))
const isPerson = computed(() => session.authenticationCurrent() && session.identity?.principal.kind === 'person')
const manageable = computed(() => isPerson.value && can('models.manage'))
const refreshable = computed(() => isPerson.value && can('models.refresh'))
// The scope belongs to a person who may read models; it changes when the person, the workspace or that access does.
const scopeKey = computed(() => readable.value ? owner.value : '')
const scope = createScope(() => scopeKey.value)
const loadLane = scope.lane(), previewLane = scope.lane()

const profiles = ref<RegistryProfile[]>([]), status = ref<RefreshStatus | null>(null)
const phase = ref<'loading' | 'ready' | 'error'>('loading')
const lines = computed(() => buildLines(profiles.value))
const autoOn = computed(() => status.value?.settings.auto_add_profiles === true)
const stripError = ref(''), autoBusy = ref(false)
const now = ref(Date.now()), cooldownUntil = ref(0), checking = ref(false)
const editing = ref<'new' | string | null>(null)   // 'new' or a line key
const draft = ref<LineDraft>(emptyDraft()), errors = ref<DraftErrors>({}), saving = ref(false), formError = ref('')
const question = ref<RemoveQuestion | null>(null)
type EditorHandle = { focusName(): void; focusFor(found: DraftErrors): void }
let editor: EditorHandle | null = null, opener: HTMLElement | null = null, ticker: ReturnType<typeof setInterval> | undefined, questionTurn = 0
const bindEditor = (el: unknown) => { editor = el as EditorHandle | null }

// ---------- loading ----------
const readAll = (signal: AbortSignal) => Promise.all([listProfiles(signal), getRefreshStatus(signal)])
function adopt([list, refresh]: [RegistryProfile[], RefreshStatus]) { profiles.value = list; status.value = refresh; phase.value = 'ready' }
function load() {
  phase.value = 'loading'
  void loadLane.run(({ after, signal }) => after(readAll(signal), adopt), { failed: () => { phase.value = 'error' } })
}
/** A fresh read after a write: the card shows what the server holds, never what it assumes. */
const reload = () => loadLane.run(({ after, signal }) => after(readAll(signal), adopt), { failed: () => { stripError.value = 'The list could not be refreshed. Reload before making another change.' } })

// ---------- auto-update and Check now ----------
const cooling = computed(() => cooldownUntil.value > now.value)
function stopTicker() { if (ticker) clearInterval(ticker); ticker = undefined }
function startCooldown(ms: number) {
  now.value = Date.now(); cooldownUntil.value = now.value + ms
  stopTicker()
  ticker = setInterval(() => { now.value = Date.now(); if (now.value >= cooldownUntil.value) stopTicker() }, 1000)
}
const when = computed(() => lastChecked(status.value?.last_run_at ?? null, new Date(now.value)))
const stripText = computed(() => {
  if (phase.value === 'loading') return 'Loading models…'
  const current = status.value
  if (!current) return ''
  const every = intervalLabel(current.settings.interval_minutes)
  if (manageable.value) {
    if (autoOn.value) return when.value ? `Last checked ${when.value} · checks every ${every}` : `Not checked yet · checks every ${every}`
    return when.value ? `Off · last checked ${when.value}` : 'Off · not checked yet'
  }
  if (!autoOn.value) return 'Auto-update is off'
  return `Auto-update is on · ${when.value ? `last checked ${when.value}` : 'not checked yet'} · checks every ${every}`
})
const checkState = computed<'now' | 'checking' | 'cooling'>(() => checking.value ? 'checking' : cooling.value ? 'cooling' : 'now')
const cooldownText = computed(() => cooldownLabel(Math.max(0, (cooldownUntil.value - now.value) / 1000)))

function toggleAuto() {
  const current = status.value
  if (!current || autoBusy.value || !manageable.value) return
  const next = { ...current.settings, auto_add_profiles: !current.settings.auto_add_profiles }
  autoBusy.value = true; stripError.value = ''
  // The switch moves at once; a failed save puts it back and says so.
  status.value = { ...current, settings: next }
  void scope.run(({ after }) => after(putRefreshSettings(next), saved => { if (status.value) status.value = { ...status.value, settings: saved } }), {
    failed: () => { if (status.value) status.value = { ...status.value, settings: current.settings }; stripError.value = 'Auto-update could not be changed. Try again.' },
    settled: () => { autoBusy.value = false },
  })
}
function check() {
  if (checking.value || cooling.value || !refreshable.value) return
  checking.value = true; stripError.value = ''
  void scope.run(({ after }) => after(checkNow(), found => after(reload(), () => {
    startCooldown(COOLDOWN_MS)
    const fresh = found.new_lines?.length ?? 0
    toast(fresh ? `${fresh} new ${fresh === 1 ? 'model' : 'models'} found` : status.value?.settings.api_enabled === false ? 'Checked · vendor model lists are off, so nothing new could be found' : 'Up to date · nothing new')
  })), {
    failed: error => {
      if (error instanceof RegistryError && error.status === 429) startCooldown((error.retryAfter ?? 300) * 1000)
      else stripError.value = error instanceof RegistryError && error.status === 403 ? 'You can’t check for models.' : 'The check could not run. Try again.'
    },
    settled: () => { checking.value = false },
  })
}

// ---------- the inline editor ----------
const lineOf = (key: string | null) => lines.value.find(line => line.key === key) ?? null
const editedLine = computed(() => editing.value && editing.value !== 'new' ? lineOf(editing.value) : null)
const mode = computed<'new' | 'manual' | 'auto'>(() => !editedLine.value ? 'new' : editedLine.value.source === 'auto' ? 'auto' : 'manual')
function openEditor(key: 'new' | string, trigger: HTMLElement | null) {
  if (!manageable.value || saving.value || question.value?.removing) return
  if (editing.value === key) { closeEditor(true); return }
  const line = key === 'new' ? null : lineOf(key)
  if (key !== 'new' && !line) return
  closeQuestion(); opener = trigger; editing.value = key; draft.value = line ? draftOf(line) : emptyDraft(); errors.value = {}; formError.value = ''
  void nextTick(() => editor?.focusName())
}
function closeEditor(restoreFocus: boolean) {
  const back = opener
  editing.value = null; errors.value = {}; formError.value = ''; closeQuestion(); opener = null
  if (restoreFocus && back) void nextTick(() => { if (back.isConnected) back.focus() })
}
// An answer belongs to the entry it was asked for: if the person has moved to another form since, that form is left alone.
const stillOn = (key: string | null) => editing.value === key
const clearError = (field: keyof DraftErrors) => { if (errors.value[field]) errors.value = { ...errors.value, [field]: undefined } }
function failure(error: unknown) {
  if (error instanceof RegistryError && error.status === 403) return 'You can’t change models. Ask a workspace admin.'
  return error instanceof Error && error.message ? `It could not be saved: ${error.message}.` : 'It could not be saved. Try again.'
}
function undo(work: () => Promise<unknown>) {
  void scope.run(({ after }) => after(work(), () => after(reload(), () => { toast('Undone') })), { failed: () => { toast('It could not be undone. Reload and check the list.', { tone: 'error' }); void reload() } })
}

function save() {
  if (saving.value || !editing.value || !manageable.value) return
  // A level still being typed counts as added.
  const written: LineDraft = { ...draft.value, efforts: splitLevels(draft.value.level, draft.value.efforts), level: '' }
  draft.value = written
  const found = validateDraft(written, mode.value)
  errors.value = found
  if (hasErrors(found)) { void nextTick(() => editor?.focusFor(found)); return }
  const line = editedLine.value
  saving.value = true; formError.value = ''
  if (line) {
    const write = lineWriteOf(written, line)
    if (!changeOf(line, write)) { saving.value = false; closeEditor(true); return }
    const back = lineWriteOf(draftOf(line), line)
    const key = line.key
    void scope.run(({ after }) => after(editLine(line.harness, line.model, write), () => after(reload(), () => {
      if (stillOn(key)) closeEditor(true)
      // Undo writes the earlier values back as another version; it never rewrites history.
      toast('Saved', { action: { label: 'Undo', run: () => undo(() => editLine(line.harness, write.model ?? line.model, { ...back, ...(write.model ? { model: line.model } : {}) })) } })
    })), { failed: error => { if (stillOn(key)) formError.value = failure(error) }, settled: () => { saving.value = false } })
    return
  }
  void scope.run(({ after }) => after(addLine(written), added => after(reload(), () => {
    if (added.failed) {
      // The first level exists; the person finishes the rest from its own Edit.
      if (!stillOn('new')) { toast(`${written.name.trim()} was added with its first level only. Open its Edit to add the rest.`, { tone: 'error' }); return }
      editing.value = `${written.harness}\u0000${modelSlug(written)}`
      formError.value = `Added with its first level only. The rest could not be saved: ${added.failed}. Save again to add the rest.`
      return
    }
    if (stillOn('new')) closeEditor(true)
    toast('Saved', { action: { label: 'Undo', run: () => undo(async () => { const result = await removeLine(added.profiles.map(profile => profile.id), UNDONE_REASON); if (result.error) throw new Error(result.error) }) } })
  })), { failed: error => { if (stillOn('new')) formError.value = failure(error) }, settled: () => { saving.value = false } })
}

// ---------- remove, with its preview first ----------
function closeQuestion() { questionTurn++; previewLane.cancel(); question.value = null }
function askRemove() {
  const line = editedLine.value
  if (!line || saving.value || !manageable.value) return
  if (question.value) { closeQuestion(); return }
  const turn = ++questionTurn
  question.value = { state: 'loading' }
  void previewLane.run(({ after, signal }) => after(Promise.all([previewRemoval(line.harness, line.model, signal), kindLabels(signal)]), ([usage, kinds]) => {
    if (turn !== questionTurn || editing.value !== line.key) return
    question.value = { state: 'ready', text: previewText(line, usage, kinds) }
  }), { failed: () => { if (turn === questionTurn) question.value = { state: 'error' } } })
}
function retryRemove() { closeQuestion(); askRemove() }
async function kindLabels(signal: AbortSignal): Promise<Record<string, string>> {
  try { return Object.fromEntries((await listWorkKinds(signal)).items.map(kind => [kind.slug, kind.label])) } catch { return {} }
}
function previewText(line: RegistryLine, usage: LineUsage, kinds: Record<string, string>): string {
  const me = session.identity?.principal.id ?? ''
  const auto = line.source === 'auto' ? ' Auto-update won’t add it back.' : ''
  const hidden = usage.incomplete ? ' Other people’s choices are not shown.' : ''
  if (!usage.used_by.length) return `Remove ${line.name}? ${usage.incomplete ? 'Nothing you can see uses it.' : 'Nothing uses it.'}${hidden && usage.incomplete ? hidden : ''}${auto}`
  const successor = usage.replacement.model ? lines.value.find(other => other.harness === usage.replacement.harness && other.model === usage.replacement.model)?.name ?? usage.replacement.model : ''
  const uses = [...new Set(usage.used_by.map(use => useText(use, kinds, me)))].join(', ')
  return `${line.name} is in use: ${uses}. ${successor ? `${successor} takes over there.` : 'The next model in line takes over there.'}${hidden}${auto}`
}
function remove() {
  const line = editedLine.value, current = question.value
  if (!line || current?.state !== 'ready' || current.removing || !manageable.value) return
  const turn = questionTurn, key = line.key
  question.value = { ...current, removing: true }
  void scope.run(({ after }) => after(removeLine(line.profileIds), result => after(reload(), () => {
    if (result.error) {
      if (turn === questionTurn) closeQuestion()
      if (stillOn(key)) formError.value = `Only ${result.done.length} of ${line.profileIds.length} levels were removed: ${result.error}. Remove it again to finish.`
      return
    }
    if (stillOn(key)) closeEditor(true)
    toast(`Removed ${line.name}`, { action: { label: 'Undo', run: () => undo(async () => { const failed = await restoreLine(result.done); if (failed) throw new Error(failed) }) } })
  })), { failed: error => { if (turn === questionTurn) closeQuestion(); if (stillOn(key)) formError.value = failure(error) } })
}

// ---------- row text ----------
const clock = computed(() => new Date(now.value))
const retiring = (line: RegistryLine) => line.retireAt ? `Retiring ${shortDate(line.retireAt, clock.value)}` : ''
const meta = (line: RegistryLine) => metaParts(line, clock.value)

// An entry that disappears under an open editor (another person removed it) closes it.
watch(lines, () => { if (editing.value && editing.value !== 'new' && !lineOf(editing.value)) closeEditor(false) })
function reset() {
  scope.reset(); closeEditor(false); stopTicker(); profiles.value = []; status.value = null; stripError.value = ''; autoBusy.value = false; checking.value = false; cooldownUntil.value = 0; saving.value = false
  phase.value = 'loading'
  if (scopeKey.value) load()
}
// Everything above is declared before the first run: a change of person or access starts over.
watch(scopeKey, reset, { immediate: true, flush: 'sync' })
const stopAccess = onAccessChange(reset)
onBeforeUnmount(() => { stopAccess(); scope.dispose(); stopTicker() })
</script>

<template>
  <section id="model-refresh" class="reg-card glass-card" data-model-registry aria-labelledby="reg-title" :aria-busy="phase === 'loading'">
    <header class="reg-head">
      <div class="reg-titles">
        <h2 id="reg-title">Model registry</h2>
        <p class="reg-lead">Every model PAIMOS can run. With auto-update, newer versions of models in use are taken on by themselves; a new model waits until someone uses it.</p>
      </div>
      <button v-if="manageable && phase === 'ready'" type="button" class="btn sm reg-add" data-reg-add :aria-expanded="editing === 'new'" @click="openEditor('new', $event.currentTarget as HTMLElement)"><AppIcon name="plus" :size="14" />Add model</button>
    </header>

    <div v-if="readable" class="reg-strip" data-reg-strip>
      <label v-if="manageable && status" class="switch reg-switch"><input type="checkbox" role="switch" data-reg-auto :checked="autoOn" :aria-busy="autoBusy" @change="toggleAuto"><span>Auto-update</span></label>
      <p v-if="stripError" class="reg-text reg-text-error" role="alert"><AppIcon name="alert" :size="14" />{{ stripError }}</p>
      <p v-else class="reg-text" data-reg-when>{{ stripText }}</p>
      <span class="reg-grow" />
      <button v-if="refreshable && status" type="button" class="btn sm ghost reg-check" data-reg-check :aria-disabled="checkState !== 'now'" :aria-busy="checking" data-tip="Manual checks wait 5 minutes between runs." @click="check">
        <!-- The three states share one box, so the button never changes size or place when it is used. -->
        <span class="btn-label reg-check-label">
          <span :aria-hidden="checkState !== 'now'"><AppIcon name="refresh" :size="14" />Check now</span>
          <span :aria-hidden="checkState !== 'checking'"><AppIcon name="refresh" :size="14" class="spin" />Checking…</span>
          <span :aria-hidden="checkState !== 'cooling'"><AppIcon name="check" :size="14" />Checked · {{ cooldownText }}</span>
        </span>
      </button>
    </div>

    <div class="reg-body">
      <p v-if="!readable" class="reg-note" role="status">The registry is visible to people who can read models. Ask a workspace admin.</p>
      <p v-else-if="phase === 'loading'" class="reg-note" role="status">Loading models…</p>
      <div v-else-if="phase === 'error'" class="reg-note" role="status" data-reg-load-error>
        <span>Models couldn’t load. Agents keep running on the last saved choices.</span>
        <button type="button" class="btn sm" data-reg-retry @click="load">Try again</button>
      </div>
      <ul v-else class="reg-list" aria-label="Registered models">
        <li v-if="editing === 'new'" class="reg-item">
          <ModelRegistryEditor :ref="bindEditor" v-model:draft="draft" mode="new" :line="null" :errors="errors" :saving="saving" :form-error="formError" :question="null" @save="save" @cancel="closeEditor(true)" @clear="clearError" />
        </li>
        <li v-for="line in lines" :key="line.key" class="reg-item" :data-reg-row="line.model">
          <div class="reg-row">
            <span class="reg-mark" aria-hidden="true"><HarnessMark :harness="line.harness" :provider="line.route" :size="13" /></span>
            <div class="reg-main">
              <span class="rg-n">{{ line.name }}
                <template v-if="line.isNew"><span class="tag-new">New</span><span class="tag-off">off until used</span></template>
                <span v-else-if="line.retireAt" class="tag-ret"><AppIcon name="clock" :size="12" />{{ retiring(line) }}</span>
              </span>
              <span v-if="line.note" class="rg-note">{{ line.note }}</span>
              <span class="rg-m"><template v-for="(part, index) in meta(line)" :key="index"><template v-if="index">{{ ' · ' }}</template><code v-if="part === line.model">{{ part }}</code><template v-else>{{ part }}</template></template></span>
            </div>
            <span class="lv" :aria-label="`Thinking levels: ${line.efforts.join(', ')}`"><span v-for="level in line.efforts" :key="level">{{ level }}</span></span>
            <button v-if="manageable" type="button" class="reg-edit" data-reg-edit :aria-expanded="editing === line.key" :aria-label="`Edit ${line.name}`" @click="openEditor(line.key, $event.currentTarget as HTMLElement)">Edit</button>
          </div>
          <ModelRegistryEditor v-if="editing === line.key" :ref="bindEditor" v-model:draft="draft" :mode="mode" :line="line" :errors="errors" :saving="saving" :form-error="formError" :question="question"
            @save="save" @cancel="closeEditor(true)" @clear="clearError" @ask-remove="askRemove" @keep="closeQuestion" @remove="remove" @retry="retryRemove" />
        </li>
        <li v-if="!lines.length && editing !== 'new'" class="reg-note" role="status">No models are registered yet.</li>
      </ul>
    </div>
  </section>
</template>

<style scoped>
.reg-card { padding: 26px 28px 8px; scroll-margin-top: 20px; container-type: inline-size; }
.reg-head { display: flex; align-items: flex-start; gap: 16px; padding-bottom: 20px; }
.reg-titles { flex: 1; min-width: 0; }
h2 { font: 600 18px/1.3 var(--font); letter-spacing: -.01em; color: var(--ink); }
.reg-lead { margin-top: 4px; font-size: 13.5px; line-height: 1.5; color: var(--ink-2); max-width: 78ch; }
.reg-add { flex: none; margin-top: 2px; }
.reg-grow { flex: 1 1 auto; }

.reg-strip { display: flex; align-items: center; gap: 12px; min-height: 52px; padding: 4px 16px; border-radius: 12px; background: var(--surface-sunken); font-size: 13px; color: var(--ink-2); }
.reg-switch { min-height: 40px; font-weight: 600; color: var(--ink); }
.reg-text { color: var(--ink-3); min-width: 0; }
.reg-text-error { display: inline-flex; align-items: center; gap: 6px; color: var(--danger); }
.reg-check { flex: none; min-height: 32px; }
.reg-check[aria-disabled="true"] { cursor: default; color: var(--ink-3); }
.reg-check[aria-disabled="true"]:hover { background: transparent; }

.reg-check-label { justify-items: end; }
.reg-check-label > span { display: inline-flex; align-items: center; gap: 6px; }
.reg-check .spin { animation: reg-spin 1s linear infinite; }
@keyframes reg-spin { to { transform: rotate(360deg); } }
@media (prefers-reduced-motion: reduce) { .reg-check .spin { animation: none; } }

.reg-body { margin-top: 10px; }
.reg-note { display: flex; flex-wrap: wrap; align-items: center; gap: 8px 14px; padding: 16px; font-size: 13px; color: var(--ink-2); }
.reg-list { list-style: none; margin: 0; padding: 0; }
.reg-item { border-bottom: 1px solid var(--line); }
.reg-item:last-child { border-bottom: 0; }
.reg-row { display: grid; grid-template-columns: 22px minmax(0, 1fr) auto 64px; align-items: center; column-gap: 14px; min-height: 64px; padding: 12px 16px; }
.reg-mark { display: grid; place-items: center; width: 22px; height: 22px; border-radius: 7px; background: var(--surface-raised); box-shadow: inset 0 0 0 1px var(--line); }
.reg-main { display: grid; min-width: 0; }
.rg-n { display: flex; align-items: center; flex-wrap: wrap; gap: 4px 8px; font-size: 14px; font-weight: 600; color: var(--ink); overflow-wrap: anywhere; }
.rg-note { font-size: 12.5px; line-height: 1.5; color: var(--ink-2); overflow-wrap: anywhere; }
.rg-m { font-size: 12.5px; line-height: 1.5; color: var(--ink-3); overflow-wrap: anywhere; }
.rg-m code { font: 500 11.5px/1.4 var(--mono); color: var(--ink-2); }
.lv { display: flex; flex-wrap: wrap; justify-content: flex-end; gap: 4px; max-width: 220px; }
.lv span { padding: 2px 7px; border-radius: 999px; background: var(--surface-sunken); font: 500 11.5px/1.5 var(--mono); color: var(--ink-2); }
/* A quiet text link; base.css adds the 44 px hit area on touch screens. */
.reg-edit { justify-self: end; min-height: 24px; min-width: 24px; padding: 0 3px; border: 0; background: none; font: 500 12.5px/1.4 var(--font); color: var(--ink-2); text-decoration: underline; text-decoration-color: var(--line-2); text-underline-offset: 2px; }
.reg-edit:hover { color: var(--teal-ink); text-decoration-color: currentColor; }
.tag-new { font: 600 9.5px/1 var(--mono); letter-spacing: .08em; text-transform: uppercase; color: var(--teal-ink); background: var(--chip-teal-bg); box-shadow: inset 0 0 0 1px var(--chip-teal-line); padding: 4px 5px; border-radius: 6px; }
.tag-off { font-size: 12px; font-weight: 500; color: var(--ink-3); }
.tag-ret { display: inline-flex; align-items: center; gap: 4px; font-size: 12px; font-weight: 500; color: var(--warn-ink); }

@container (max-width: 600px) {
  .reg-card { padding: 18px 14px 4px; }
  .reg-head { flex-wrap: wrap; row-gap: 12px; }
  .reg-titles { flex-basis: 100%; }
  .reg-strip { flex-wrap: wrap; gap: 4px 10px; padding: 8px 12px; }
  .reg-check { margin-left: auto; }
  .reg-text { order: 3; flex: 1 1 100%; padding-bottom: 4px; }
  .reg-row { grid-template-columns: 22px minmax(0, 1fr) auto; row-gap: 8px; padding: 12px; }
  .reg-mark { align-self: start; margin-top: 2px; }
  .lv { grid-column: 2 / -1; grid-row: 2; justify-content: flex-start; max-width: none; }
  .reg-edit { grid-column: 3; grid-row: 1; align-self: start; }
}
</style>
<style scoped src="../../../styles/settingsButtons.css"></style>
