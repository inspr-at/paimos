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
import { PERMISSION, readMatrix } from '../../../lib/accountUse'
import {
  RegistryError, REMOVED_REASON, UNDONE_REASON, addLine, buildLines, changeOf, checkNow, checkReport, cooldownLabel, draftOf, draftsMatch, editLine, emptyDraft, getRefreshStatus, hasErrors, intervalLabel, lastChecked, lineWriteOf,
  listProfiles, metaParts, modelSlug, previewRemoval, putRefreshSettings, removalText, removeLine, restoreLine, shortDate, snapshotFromUsage, splitLevels, validateDraft,
  type DraftErrors, type LineDraft, type LinePick, type LineSnapshot, type LineUsage, type RefreshStatus, type RegistryLine, type RegistryProfile, type WriteScope,
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
// Auto-update is the "New model versions" switch of the account matrix (AEON-1054).
const autoManageable = computed(() => manageable.value && can(PERMISSION))
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
// What the editor was opened on: one usage read, whose revision and line fields stay together. editorTurn moves on with every open, close and switch of the editor, so a read or save of an earlier editor can never touch the current one.
let revisionWait: Promise<LineSnapshot> | null = null
let editorTurn = 0
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
  if (!current || autoBusy.value || !autoManageable.value) return
  const next = { ...current.settings, auto_add_profiles: !current.settings.auto_add_profiles }
  autoBusy.value = true; stripError.value = ''
  // The switch moves at once; a failed save puts it back and says so. The
  // matrix revision is read first, so a competing rule change answers 409.
  status.value = { ...current, settings: next }
  void scope.run(({ after, signal }) => after(readMatrix(signal, { limit: 1 }), matrix => after(putRefreshSettings(next, matrix.rules.revision), saved => { if (status.value) status.value = { ...status.value, settings: saved } })), {
    failed: error => { if (status.value) status.value = { ...status.value, settings: current.settings }; stripError.value = error instanceof RegistryError && error.status === 409 ? 'Someone changed the New model versions rule meanwhile. Nothing was saved; try again.' : 'Auto-update could not be changed. Try again.' },
    settled: () => { autoBusy.value = false },
  })
}
function check() {
  if (checking.value || cooling.value || !refreshable.value) return
  checking.value = true; stripError.value = ''
  void scope.run(({ after, signal }) => after(checkNow(signal), found => after(reload(), () => {
    startCooldown(COOLDOWN_MS)
    toast(checkReport(found, status.value?.settings.api_enabled !== false))
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
  closeQuestion(); opener = trigger; editing.value = key; editorTurn++; draft.value = line ? draftOf(line) : emptyDraft(); errors.value = {}; formError.value = ''; revisionWait = null
  if (line) captureRevision(line)
  void nextTick(() => editor?.focusName())
}
function closeEditor(restoreFocus: boolean) {
  const back = opener
  editing.value = null; editorTurn++; errors.value = {}; formError.value = ''; closeQuestion(); opener = null; revisionWait = null
  if (restoreFocus && back) void nextTick(() => { if (back.isConnected) back.focus() })
}
// Fields and revision come from the usage read, not from the list already on screen. The read answers only the editor that asked for it.
function captureRevision(line: RegistryLine) {
  const key = line.key, turn = editorTurn
  const opened = draftOf(line)
  let settled = false
  const pending = new Promise<LineSnapshot>((resolve, reject) => {
    const finish = (error?: unknown, value?: LineSnapshot) => {
      if (settled) return
      settled = true
      if (error || !value) reject(error ?? new RegistryError(502, 'The registry revision is not available.'))
      else resolve(value)
    }
    void scope.run(({ after, signal }) => {
      if (signal.aborted) { finish(new Error('The person or workspace changed.')); return Promise.resolve(undefined) }
      const abort = () => finish(new Error('The person or workspace changed.'))
      signal.addEventListener('abort', abort, { once: true })
      return after(previewRemoval(line.harness, line.model, signal), usage => {
        signal.removeEventListener('abort', abort)
        // Closed, switched or reopened since: this answer belongs to an editor that is gone, even when the same line is open again.
        if (turn !== editorTurn || editing.value !== key) { finish(new Error('The editor moved on.')); return }
        const snapshot = snapshotFromUsage(profiles.value, line.harness, line.model, usage)
        if (!snapshot) { finish(new RegistryError(502, 'The registry revision is not available.')); return }
        const next = draftOf(snapshot.line)
        // Edits typed on the stale list must not be saved at the newer revision.
        if (!draftsMatch(draft.value, opened) && !draftsMatch(opened, next)) { finish(new RegistryError(409, 'stale registry revision')); return }
        if (draftsMatch(draft.value, opened)) draft.value = next
        finish(undefined, snapshot)
      })
    }, { failed: error => finish(error) })
  })
  void pending.catch(() => {})
  revisionWait = pending
}
function writeScope(signal: AbortSignal): WriteScope {
  const owner = scopeKey.value
  return { signal, live: () => scopeKey.value === owner }
}
// An answer belongs to the entry it was asked for: if the person has moved to another form since, that form is left alone.
const stillOn = (key: string | null) => editing.value === key
// An edit also belongs to the editor session that started it, so a form reopened on the same line is left alone too.
const stillIn = (key: string, turn: number) => editing.value === key && editorTurn === turn
const clearError = (field: keyof DraftErrors) => { if (errors.value[field]) errors.value = { ...errors.value, [field]: undefined } }
function failure(error: unknown) {
  if (error instanceof RegistryError && error.status === 403) return 'You can’t change models. Ask a workspace admin.'
  return error instanceof Error && error.message ? `It could not be saved: ${error.message}.` : 'It could not be saved. Try again.'
}
function undo(work: (scope: WriteScope) => Promise<unknown>) {
  void scope.run(({ after, signal }) => after(work(writeScope(signal)), () => after(reload(), () => { toast('Undone') })), { failed: () => { toast('It could not be undone. Reload and check the list.', { tone: 'error' }); void reload() } })
}

function save() {
  if (saving.value || !editing.value || !manageable.value) return
  // A level still being typed counts as added.
  const written: LineDraft = { ...draft.value, efforts: splitLevels(draft.value.level, draft.value.efforts), level: '' }
  draft.value = written
  const found = validateDraft(written, mode.value)
  errors.value = found
  if (hasErrors(found)) { void nextTick(() => editor?.focusFor(found)); return }
  const opened = editedLine.value
  saving.value = true; formError.value = ''
  if (opened) {
    const key = opened.key, turn = editorTurn
    const pending = revisionWait
    void scope.run(({ after, signal }) => after(pending ?? Promise.reject(new RegistryError(502, 'The registry revision is not available.')), ({ line, revision }) => {
      // Cancelled, or the editor was reopened, while this save waited: nothing is written for a form that is gone.
      if (!stillIn(key, turn)) return
      // The snapshot may have replaced the cached draft while this save was waiting.
      const nowDraft: LineDraft = { ...draft.value, efforts: splitLevels(draft.value.level, draft.value.efforts), level: '' }
      const write = lineWriteOf(nowDraft, line)
      if (!changeOf(line, write)) { closeEditor(true); return }
      const back = lineWriteOf(draftOf(line), line)
      const body = { ...write, revision }
      return after(editLine(line.harness, line.model, body, signal), saved => after(reload(), () => {
        if (stillIn(key, turn)) closeEditor(true)
        // Undo writes the earlier values back as another version, at the revision this edit produced.
        if (!saved.revision) { toast('Saved. Undo is unavailable because the registry did not confirm its revision.', { tone: 'error' }); return }
        const undoRevision = saved.revision
        toast('Saved', { action: { label: 'Undo', run: () => undo(scope => editLine(line.harness, body.model ?? line.model, { ...back, ...(body.model ? { model: line.model } : {}), revision: undoRevision }, scope.signal)) } })
      }))
    }), {
      failed: error => {
        if (error instanceof RegistryError && error.status === 409) {
          toast('This model changed somewhere else. The list was reloaded; review it before saving again.', { tone: 'error' })
          if (stillIn(key, turn)) closeEditor(false)
          void reload()
          return
        }
        if (stillIn(key, turn)) formError.value = failure(error)
      },
      settled: () => { saving.value = false },
    })
    return
  }
  void scope.run(({ after, signal }) => after(addLine(written, new Date(), writeScope(signal)), added => after(reload(), () => {
    if (added.failed) {
      // The first level exists; the person finishes the rest from its own Edit.
      if (!stillOn('new')) { toast(`${written.name.trim()} was added with its first level only. Open its Edit to add the rest.`, { tone: 'error' }); return }
      editing.value = `${written.harness}\u0000${modelSlug(written)}`; editorTurn++
      // The form is now an edit of the line that exists. Save must send the revision captured with that line.
      const created = lineOf(editing.value)
      if (created) captureRevision(created)
      formError.value = `Added with its first level only. The rest could not be saved: ${added.failed}. Save again to add the rest.`
      return
    }
    if (stillOn('new')) closeEditor(true)
    toast('Saved', { action: { label: 'Undo', run: () => undo(async scope => { const result = await removeLine(added.profiles.map(profile => profile.id), UNDONE_REASON, scope); if (result.error) throw new Error(result.error) }) } })
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
function replacementName(pick: LinePick): string {
  if (!pick.model) return pick.line ?? ''
  return lines.value.find(other => other.harness === pick.harness && other.model === pick.model)?.name ?? pick.model
}
function previewText(line: RegistryLine, usage: LineUsage, kinds: Record<string, string>): string {
  return removalText(line.name, line.source, usage, kinds, session.identity?.principal.id ?? '', replacementName)
}
function remove() {
  const line = editedLine.value, current = question.value
  if (!line || current?.state !== 'ready' || current.removing || !manageable.value) return
  const turn = questionTurn, key = line.key
  question.value = { ...current, removing: true }
  void scope.run(({ after, signal }) => after(removeLine(line.profileIds, REMOVED_REASON, writeScope(signal)), result => after(reload(), () => {
    if (result.error) {
      if (turn === questionTurn) closeQuestion()
      if (stillOn(key)) formError.value = `Only ${result.done.length} of ${line.profileIds.length} levels were removed: ${result.error}. Remove it again to finish.`
      return
    }
    if (stillOn(key)) closeEditor(true)
    toast(`Removed ${line.name}`, { action: { label: 'Undo', run: () => undo(async scope => { const failed = await restoreLine(result.done, scope); if (failed) throw new Error(failed) }) } })
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
      <label v-if="manageable && status" class="switch reg-switch" :data-tip="autoManageable ? undefined : 'Owners and admins change this as New model versions in Settings › Accounts.'"><input type="checkbox" role="switch" data-reg-auto :checked="autoOn" :disabled="!autoManageable" :aria-busy="autoBusy" @change="toggleAuto"><span>Auto-update</span></label>
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
