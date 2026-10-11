<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, ref, watch } from 'vue'
import AppIcon from '../../AppIcon.vue'
import ModelLineRow from './ModelLineRow.vue'
import ModelDiscovery, { type DiscoveryAccount } from './ModelDiscovery.vue'
import ModelRegistryEditor from './ModelRegistryEditor.vue'
import ModelOutdatedSheet from './ModelOutdatedSheet.vue'
import ModelsPopover from './ModelsPopover.vue'
import { api } from '../../../lib/api'
import { can, onAccessRevalidate } from '../../../lib/authz'
import { createScope, scopeOwner } from '../../../lib/identityScope'
import { toast } from '../../../lib/toast'
import { listWorkKinds } from '../../../lib/workKinds'
import { useSession } from '../../../stores/session'
import { PERMISSION, readMatrix } from '../../../lib/accountUse'
import {
  RegistryError, REMOVED_REASON, UNDONE_REASON, acceptProposal, addLine, buildLines, buildRegistry, changeOf, checkNow, checkReport, cooldownLabel, draftOf, draftsMatch, editLine, emptyDraft, getRefreshStatus, hasErrors,
  intervalLabel, lastChecked, lineWriteOf, listProfiles, mergeUsage, modelSlug, outdatedBatch, outdatedTray, pauseModel, pauseOptions, pauseUntil, previewRemoval, putDiscoveryKey, putRefreshSettings, readOutdated,
  readOutdatedBatch, refused, registryCount, removalText, removeLine, restoreLine, retireOutdated, snapshotFromUsage, splitLevels, undoOutdated, undoPause, untilText, validateDraft,
  type DraftErrors, type LineDraft, type LinePick, type LineSnapshot, type Observation, type OutdatedDocument, type PauseChoice, type RefreshSettings, type RefreshStatus, type RegistryLine, type RegistryProfile,
  type RegistryRoute, type RegistryRow, type WriteScope,
} from '../../../lib/modelRegistry'

// Settings › Models › Model registry (AEON-1012, grouped in AEON-1163). Admins manage, everyone else reads.
// Providers group the lines; each line is one row, folded by default. Everything an admin does opens below the
// control that was used, never in a dialog: a fold, the Pause… menu, the remove question, the editor of a route
// added by hand. Nothing above that control moves (AEON-541).
const COOLDOWN_MS = 5 * 60_000
const session = useSession()
const owner = computed(() => scopeOwner(session.identity))
const readable = computed(() => session.authenticationCurrent() && can('models.read'))
const isPerson = computed(() => session.authenticationCurrent() && session.identity?.principal.kind === 'person')
const manageable = computed(() => isPerson.value && can('models.manage'))
const refreshable = computed(() => isPerson.value && can('models.refresh'))
// Auto-update is the "New model versions" switch of the account matrix (AEON-1054), and its only writer (AEON-1148).
const autoManageable = computed(() => manageable.value && can(PERMISSION))
// The scope belongs to a person who may read models; it changes when the person, the workspace or that access does.
const scopeKey = computed(() => readable.value ? owner.value : '')
const scope = createScope(() => scopeKey.value)
const loadLane = scope.lane(), previewLane = scope.lane(), accountLane = scope.lane()

const profiles = ref<RegistryProfile[]>([]), status = ref<RefreshStatus | null>(null), outdated = ref<OutdatedDocument | null>(null)
const phase = ref<'loading' | 'ready' | 'error'>('loading')
const now = ref(Date.now()), cooldownUntil = ref(0), checking = ref(false)
const clock = computed(() => new Date(now.value))
// The registry lines (one harness and model id each) are what the editor edits; the grouped registry is what the page shows.
const lines = computed(() => buildLines(profiles.value))
const registry = computed(() => buildRegistry(profiles.value, clock.value))
const rows = computed(() => registry.value.groups.flatMap(group => group.rows))
const autoOn = computed(() => status.value?.settings.auto_add_profiles === true)
const stripError = ref(''), autoBusy = ref(false)
const opened = ref(new Set<string>())
const editing = ref<'new' | string | null>(null)   // 'new' or a line key
const draft = ref<LineDraft>(emptyDraft()), errors = ref<DraftErrors>({}), saving = ref(false), formError = ref('')
// The remove question of one row: the usage of every route it removes, read before anything is removed.
interface Question { key: string; state: 'loading' | 'ready' | 'error'; text?: string; failed?: string; removing?: boolean }
const question = ref<Question | null>(null)
const pauseMenu = ref<{ key: string; anchor: HTMLElement; at: number } | null>(null), pausing = ref<string | null>(null)
// What the editor was opened on: one usage read, whose revision and line fields stay together. editorTurn moves on with every open, close and switch of the editor, so a read or save of an earlier editor can never touch the current one.
let revisionWait: Promise<LineSnapshot> | null = null
let editorTurn = 0
type EditorHandle = { focusName(): void; focusFor(found: DraftErrors): void }
let editor: EditorHandle | null = null, opener: HTMLElement | null = null, ticker: ReturnType<typeof setInterval> | undefined, questionTurn = 0
let questionOpener: HTMLElement | null = null
const bindEditor = (el: unknown) => { editor = el as EditorHandle | null }
const cardEl = ref<HTMLElement | null>(null)

// ---------- loading ----------
// The outdated read is a hint for admins: when it fails, every version still shows in the list with its tags, so the registry loads without it.
// Its first page comes with the list; later pages only when the earlier ones offer less than a batch (AEON-1164).
async function readAll(signal: AbortSignal): Promise<[RegistryProfile[], RefreshStatus, OutdatedDocument | null]> {
  const [list, refresh, first] = await Promise.all([listProfiles(signal), getRefreshStatus(signal), manageable.value ? readOutdated(signal).catch(() => null) : null])
  return [list, refresh, first && await readOutdatedBatch(first, list, signal)]
}
// Dates on the rows ("Paused until …", "Retiring …") are read against the time of the read that brought them.
function adopt([list, refresh, old]: [RegistryProfile[], RefreshStatus, OutdatedDocument | null]) { now.value = Date.now(); profiles.value = list; status.value = refresh; outdated.value = old; phase.value = 'ready' }
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
const when = computed(() => lastChecked(status.value?.last_run_at ?? null, clock.value))
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

// ---------- how new models are found (the former Model catalog form, merged here: AEON-1138) ----------
// Agent reports, vendor lists, the interval, discovery keys and waiting models. Auto-update keeps its one writer
// above: every write here sends the settings with auto_add_profiles unchanged, so it never needs the matrix revision.
const discoveryOpen = ref(false), discoveryBusy = ref(false), discoveryError = ref(''), discoveryNote = ref('')
const accounts = ref<DiscoveryAccount[] | null>(null)
const VENDOR: Record<string, string> = { codex: 'openai', claude: 'anthropic', grok: 'xai', pi: 'openrouter' }
function loadAccounts() {
  if (accounts.value || !manageable.value || !can('account.read')) return
  void accountLane.run(({ after, signal }) => after(api('/agent-accounts', { signal }).then(async response => response.ok ? await response.json() as unknown : []), list => {
    accounts.value = Array.isArray(list) ? (list as { id?: unknown; label?: unknown; harness?: unknown }[])
      .filter(account => typeof account?.id === 'string' && typeof account.harness === 'string' && !!VENDOR[account.harness])
      .map(account => ({ id: String(account.id), label: typeof account.label === 'string' ? account.label : String(account.id), harness: String(account.harness), vendor: VENDOR[String(account.harness)]! })) : []
  }), { failed: () => { accounts.value = [] } })
}
function toggleDiscovery() { discoveryOpen.value = !discoveryOpen.value; if (discoveryOpen.value) loadAccounts() }
function discoveryWrite(work: (signal: AbortSignal) => Promise<unknown>, done: string, fail: string, before?: () => void) {
  if (discoveryBusy.value || !manageable.value) return
  discoveryBusy.value = true; discoveryError.value = ''; discoveryNote.value = ''
  void scope.run(({ after, signal }) => after(work(signal), () => after(reload(), () => { discoveryNote.value = done })), {
    failed: error => { before?.(); discoveryError.value = error instanceof RegistryError && error.status === 403 ? 'You can’t change how models are found. Ask a workspace admin.' : fail },
    settled: () => { discoveryBusy.value = false },
  })
}
function setDiscovery(change: Partial<Pick<RefreshSettings, 'agent_reports_enabled' | 'api_enabled' | 'interval_minutes'>>) {
  const current = status.value
  if (!current || discoveryBusy.value || !manageable.value) return
  // The whole object goes back with auto_add_profiles as the server holds it; the control moves at once and a failed save puts it back.
  const next = { ...current.settings, ...change }
  status.value = { ...current, settings: next }
  discoveryWrite(signal => putRefreshSettings(next).then(saved => { if (signal.aborted) return; if (status.value) status.value = { ...status.value, settings: saved } }), 'Saved.', 'It could not be saved. Try again.', () => { if (status.value) status.value = { ...status.value, settings: current.settings } })
}
const saveKey = (account: DiscoveryAccount, key: string) => discoveryWrite(signal => putDiscoveryKey(account.id, account.vendor, key, signal), key ? 'The discovery key was saved.' : 'The discovery key was removed.', key ? 'The key could not be saved. Try again.' : 'The key could not be removed. Try again.')
function accept(observation: Observation) {
  discoveryWrite(signal => acceptProposal(observation, signal), `${observation.model} at ${observation.effort} can run now.`, 'It could not be accepted. Try again.')
}

// ---------- the rows ----------
function toggleRow(key: string) {
  const next = new Set(opened.value)
  if (next.has(key)) next.delete(key); else next.add(key)
  opened.value = next
}
const rowOf = (key: string | null) => rows.value.find(row => row.key === key) ?? null
const editingIn = (row: RegistryRow) => !!editing.value && editing.value !== 'new' && row.model.routes.some(route => route.lineKeys.includes(editing.value!))
const busyRow = (row: RegistryRow) => pausing.value === row.key || (question.value?.key === row.key && !!question.value.removing)

// ---------- the inline editor: Add model, and routes added by hand ----------
const lineOf = (key: string | null) => lines.value.find(line => line.key === key) ?? null
const editedLine = computed(() => editing.value && editing.value !== 'new' ? lineOf(editing.value) : null)
const mode = computed<'new' | 'manual'>(() => editedLine.value ? 'manual' : 'new')
function openEditor(key: 'new' | string, trigger: HTMLElement | null) {
  if (!manageable.value || saving.value) return
  if (editing.value === key) { closeEditor(true); return }
  const line = key === 'new' ? null : lineOf(key)
  if (key !== 'new' && !line) return
  closeQuestion(); opener = trigger; editing.value = key; editorTurn++; draft.value = line ? draftOf(line) : emptyDraft(); errors.value = {}; formError.value = ''; revisionWait = null
  if (line) captureRevision(line)
  void nextTick(() => editor?.focusName())
}
const editRoute = (route: RegistryRoute, trigger: HTMLElement) => { if (route.hand && route.lineKeys[0]) openEditor(route.lineKeys[0], trigger) }
function closeEditor(restoreFocus: boolean) {
  const back = opener
  editing.value = null; editorTurn++; errors.value = {}; formError.value = ''; opener = null; revisionWait = null
  if (restoreFocus && back) void nextTick(() => { if (back.isConnected) back.focus() })
}
// Fields and revision come from the usage read, not from the list already on screen. The read answers only the editor that asked for it.
function captureRevision(line: RegistryLine) {
  const key = line.key, turn = editorTurn
  const openedDraft = draftOf(line)
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
        if (!draftsMatch(draft.value, openedDraft) && !draftsMatch(openedDraft, next)) { finish(new RegistryError(409, 'stale registry revision')); return }
        if (draftsMatch(draft.value, openedDraft)) draft.value = next
        finish(undefined, snapshot)
      })
    }, { failed: error => finish(error) })
  })
  void pending.catch(() => {})
  revisionWait = pending
}
function writeScope(signal: AbortSignal): WriteScope {
  const captured = scopeKey.value
  return { signal, live: () => scopeKey.value === captured }
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
  const line0 = editedLine.value
  saving.value = true; formError.value = ''
  if (line0) {
    const key = line0.key, turn = editorTurn
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

// ---------- remove a version, with its preview first ----------
function closeQuestion(restoreFocus = false) {
  questionTurn++; previewLane.cancel(); question.value = null
  const back = questionOpener
  questionOpener = null
  if (restoreFocus && back) void nextTick(() => { if (back.isConnected) back.focus() })
}
async function kindLabels(signal: AbortSignal): Promise<Record<string, string>> {
  try { return Object.fromEntries((await listWorkKinds(signal)).items.map(kind => [kind.slug, kind.label])) } catch { return {} }
}
function replacementName(pick: LinePick): string {
  if (!pick.model) return pick.line ?? ''
  return lines.value.find(other => other.harness === pick.harness && other.model === pick.model)?.name ?? pick.model
}
/** Asks where the version shown in a row is used: one read per route, one sentence for all of them. */
function askRemove(row: RegistryRow, trigger: HTMLElement | null) {
  if (!manageable.value || saving.value || question.value?.removing) return
  if (question.value?.key === row.key) { closeQuestion(true); return }
  if (editing.value) closeEditor(false)
  closeQuestion()
  questionOpener = trigger
  const turn = ++questionTurn, model = row.model
  question.value = { key: row.key, state: 'loading' }
  const routes = model.routes.filter(route => route.models[0])
  void previewLane.run(({ after, signal }) => after(Promise.all([Promise.all(routes.map(route => previewRemoval(route.harness, route.models[0]!, signal))), kindLabels(signal)]), ([answers, kinds]) => {
    if (turn !== questionTurn || question.value?.key !== row.key) return
    const source = model.routes.every(route => route.hand) ? 'manual' : 'auto'
    question.value = { key: row.key, state: 'ready', text: removalText(model.name, source, mergeUsage(answers), kinds, session.identity?.principal.id ?? '', replacementName) }
  }), { failed: () => { if (turn === questionTurn) question.value = { key: row.key, state: 'error' } } })
}
function retryRemove() { const row = rowOf(question.value?.key ?? null), back = questionOpener; if (!row) return; closeQuestion(); askRemove(row, back) }
function remove() {
  const current = question.value, row = rowOf(current?.key ?? null)
  if (!row || current?.state !== 'ready' || current.removing || !manageable.value) return
  const turn = questionTurn, model = row.model
  question.value = { ...current, removing: true, failed: undefined }
  void scope.run(({ after, signal }) => after(removeLine(model.profileIds, REMOVED_REASON, writeScope(signal)), result => after(reload(), () => {
    if (result.error) {
      const partly = `Only ${result.done.length} of ${model.profileIds.length} levels of ${model.name} were removed: ${result.error}. Remove it again to finish.`
      // The person may have moved on to another form meanwhile; the outcome is still said.
      if (turn === questionTurn && question.value) question.value = { ...question.value, removing: false, failed: partly }
      else toast(partly, { tone: 'error' })
      return
    }
    if (turn === questionTurn) closeQuestion()
    // The row may be gone; focus lands on the card's first control instead of the page.
    void nextTick(() => { if (!cardEl.value?.contains(document.activeElement)) cardEl.value?.querySelector<HTMLElement>('[data-reg-add], [data-reg-check]')?.focus({ preventScroll: true }) })
    toast(`Removed ${model.name}`, { action: { label: 'Undo', run: () => undo(async scope => { const failed = await restoreLine(result.done, scope); if (failed) throw new Error(failed) }) } })
  })), { failed: error => { if (turn === questionTurn && question.value) question.value = { ...question.value, removing: false, failed: failure(error) }; else toast(failure(error), { tone: 'error' }) } })
}
function questionKeys(event: KeyboardEvent) {
  if (event.key !== 'Escape' || event.isComposing) return
  event.preventDefault(); event.stopPropagation()
  if (!question.value?.removing) closeQuestion(true)
}

// ---------- pause a version ----------
const pauseRow = computed(() => rowOf(pauseMenu.value?.key ?? null))
// The menu's dates are those of the moment it opened ("For a week · Until 18 Oct").
const pauseChoices = computed(() => pauseRow.value && pauseMenu.value ? pauseOptions(pauseRow.value.model.routes.some(route => route.pause || route.partlyPaused), new Date(pauseMenu.value.at)) : [])
function openPause(row: RegistryRow, anchor: HTMLElement) {
  if (!manageable.value || pausing.value) return
  if (pauseMenu.value?.key === row.key) { pauseMenu.value = null; return }
  pauseMenu.value = { key: row.key, anchor, at: Date.now() }
}
function pauseKeys(event: KeyboardEvent) {
  const items = [...(event.currentTarget as HTMLElement).querySelectorAll<HTMLElement>('[role="menuitem"]')]
  const at = items.indexOf(document.activeElement as HTMLElement)
  const go = (index: number) => { event.preventDefault(); items[(index + items.length) % items.length]?.focus() }
  if (event.key === 'ArrowDown') go(at + 1)
  else if (event.key === 'ArrowUp') go(at - 1)
  else if (event.key === 'Home') go(0)
  else if (event.key === 'End') go(items.length - 1)
}
function choosePause(choice: PauseChoice | 'resume') {
  const open = pauseMenu.value, row = rowOf(open?.key ?? null)
  pauseMenu.value = null
  if (!row || !open || pausing.value || !manageable.value) return
  const model = row.model, anchor = open.anchor, until = choice === 'resume' ? null : pauseUntil(choice, new Date())
  pausing.value = row.key
  const refocus = () => void nextTick(() => { if (anchor.isConnected) anchor.focus({ preventScroll: true }) })
  void scope.run(({ after, signal }) => after(pauseModel(model, model.family, choice === 'resume' ? 'resume' : { until }, writeScope(signal)), result => after(reload(), () => {
    refocus()
    const back = result.done
    const undoAction = back.length ? { action: { label: 'Undo', run: () => undo(scope => undoPause(back, new Date(), scope).then(failed => { if (failed) throw new Error(failed) })) } } : {}
    const steps = model.routes.length
    if (result.error) {
      toast(`${choice === 'resume' ? 'Resumed' : 'Paused'} only ${back.length} of its route${steps === 1 ? '' : 's'}: ${result.error}. Try again to finish.`, { tone: 'error', ...undoAction })
      return
    }
    toast(choice === 'resume' ? `Resumed ${model.name}` : `Paused ${model.name} ${untilText(until, new Date())}`, undoAction)
  })), {
    failed: error => { refocus(); toast(error instanceof RegistryError && error.status === 403 ? 'You can’t pause models. Ask a workspace admin.' : 'It could not be changed. Try again.', { tone: 'error' }) },
    settled: () => { pausing.value = null },
  })
}

// ---------- old versions: the tray and the review sheet (AEON-1164) ----------
const sheetOpen = ref(false), removingOld = ref(false)
const batch = computed(() => manageable.value && outdated.value ? outdatedBatch(outdated.value, profiles.value, clock.value) : null)
const entries = computed(() => batch.value?.entries ?? [])
const more = computed(() => batch.value?.more === true)
const tray = computed(() => entries.value.length ? outdatedTray(entries.value, more.value, outdated.value?.incomplete === true) : null)
// A list read again with nothing left to review closes the sheet.
watch(entries, list => { if (!list.length) sheetOpen.value = false })
function openSheet() { if (manageable.value && entries.value.length && phase.value === 'ready') sheetOpen.value = true }
const RETIRE_CONFLICT: Record<string, string> = {
  'stale outdated preview': 'The old versions changed meanwhile. Nothing was removed; the list was refreshed, so review it and remove again.',
  'version is no longer outdated': 'One of them is no longer outdated. Nothing was removed; the list was refreshed.',
  'successor is also selected for retirement': 'One selected version is the successor of another. Nothing was removed; remove them one at a time.',
  'used version has no registered successor': 'One of them has no newer version that can run. Nothing was removed; the list was refreshed.',
  'successor has no matching harness route': 'A newer version doesn’t run through the same harness as an old one. Nothing was removed.',
  'replacement line has a conflicting rule': 'A rule on the newer version conflicts with one on the old version. Nothing was removed.',
}
/** Removes the versions shown in the sheet, at the revision they were read with: every use moves first, in the same step. */
function retireOld(versions: string[]) {
  const doc = outdated.value
  if (!doc || removingOld.value || !manageable.value || !versions.length) return
  const revision = doc.revision
  removingOld.value = true
  void scope.run(({ after, signal }) => after(retireOutdated(versions, revision, signal), done => {
    sheetOpen.value = false
    return after(reload(), () => {
      const n = done.removed.length, moved = done.moved.length
      const text = `Removed ${n} old version${n === 1 ? '' : 's'} · ${moved} use${moved === 1 ? '' : 's'} moved`
      // The Review button went with the tray; focus lands on the card's first control instead of the page.
      void nextTick(() => { if (!cardEl.value?.contains(document.activeElement)) cardEl.value?.querySelector<HTMLElement>('[data-reg-add]')?.focus({ preventScroll: true }) })
      if (!done.undo_token) { toast(`${text}. Undo is unavailable because the registry did not confirm it.`, { tone: 'error' }); return }
      const token = done.undo_token
      toast(text, { timeout: 10_000, action: { label: 'Undo', run: () => undo(scope => undoOutdated(token, scope.signal)) } })
    })
  }), {
    failed: error => {
      // No answer, a lost or unreadable one, or a gateway error: the step may have happened, so say so and show what the server holds.
      if (!refused(error)) {
        toast('The registry didn’t confirm the removal, so it may have happened. The list was refreshed; check it before removing again.', { tone: 'error' })
        void reload()
        return
      }
      if (error.status === 409) {
        toast(RETIRE_CONFLICT[error.message] ?? `Nothing was removed: ${error.message}. The list was refreshed.`, { tone: 'error' })
        void reload()
        return
      }
      toast(error.status === 403 ? 'You can’t remove old versions or move their uses. Ask a workspace admin.'
        : error.status === 413 ? 'Too many uses move at once. Nothing was removed; select fewer versions.'
          : 'Nothing was removed. Try again.', { tone: 'error' })
    },
    settled: () => { removingOld.value = false },
  })
}

// An entry that disappears under an open editor (another person removed it) closes it; so does a gone row's question or menu.
watch(lines, () => { if (editing.value && editing.value !== 'new' && !lineOf(editing.value)) closeEditor(false) })
watch(rows, () => {
  if (question.value && !rowOf(question.value.key) && !question.value.removing) closeQuestion()
  if (pauseMenu.value && !rowOf(pauseMenu.value.key)) pauseMenu.value = null
})
function reset() {
  scope.reset(); closeEditor(false); closeQuestion(); stopTicker(); profiles.value = []; status.value = null; stripError.value = ''; autoBusy.value = false; checking.value = false; cooldownUntil.value = 0; saving.value = false
  outdated.value = null; sheetOpen.value = false; removingOld.value = false; opened.value = new Set(); pauseMenu.value = null; pausing.value = null
  discoveryOpen.value = false; discoveryBusy.value = false; discoveryError.value = ''; discoveryNote.value = ''; accounts.value = null
  phase.value = 'loading'
  if (scopeKey.value) load()
}
// Everything above is declared before the first run: a change of person or access starts over.
watch(scopeKey, reset, { immediate: true, flush: 'sync' })
const stopAccess = onAccessRevalidate({ reset, refresh: () => { if (scopeKey.value && !saving.value && !autoBusy.value && !checking.value && !question.value?.removing && !removingOld.value && !pausing.value && !discoveryBusy.value) void reload() } })
onBeforeUnmount(() => { stopAccess(); scope.dispose(); stopTicker() })
</script>

<template>
  <section id="model-refresh" ref="cardEl" class="reg-card glass-card" data-model-registry aria-labelledby="reg-title" :aria-busy="phase === 'loading'">
    <header class="reg-head">
      <div class="reg-titles">
        <h2 id="reg-title">Model registry</h2>
        <p class="reg-lead">Every model PAIMOS can run, by provider. Facts come from the provider or the harness that runs it; models added by hand stay editable.</p>
      </div>
      <button v-if="manageable && phase === 'ready'" type="button" class="btn sm reg-add" data-reg-add :aria-expanded="editing === 'new'" @click="openEditor('new', $event.currentTarget as HTMLElement)"><AppIcon name="plus" :size="14" />Add model</button>
    </header>

    <div v-if="readable" class="reg-strip" data-reg-strip>
      <label v-if="manageable && status" class="switch reg-switch" :data-tip="autoManageable ? undefined : 'Owners and admins change this as New model versions in Settings under Accounts and computers.'"><input type="checkbox" role="switch" data-reg-auto :checked="autoOn" :disabled="!autoManageable" :aria-busy="autoBusy" @change="toggleAuto"><span>Auto-update</span></label>
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
    <ModelDiscovery v-if="readable && status && phase === 'ready'" :status="status" :open="discoveryOpen" :manage="manageable" :busy="discoveryBusy" :error="discoveryError" :note="discoveryNote" :accounts="accounts"
      @toggle="toggleDiscovery" @set="setDiscovery" @save-key="saveKey" @accept="accept" />
    <div v-if="tray && phase === 'ready'" class="reg-tray" data-reg-tray>
      <AppIcon name="history" :size="16" class="reg-tray-ic" />
      <span class="reg-tray-t" data-reg-tray-text><template v-for="(pair, index) in tray.named" :key="index"><template v-if="index">{{ ' · ' }}</template><b>{{ pair.to }}</b> replaces {{ pair.from }}</template>{{ ' · ' }}{{ tray.count }}{{ tray.unused }}</span>
      <button type="button" class="btn sm reg-review" data-reg-review aria-haspopup="dialog" :aria-expanded="sheetOpen" @click="openSheet">Review…</button>
    </div>

    <div class="reg-body">
      <p v-if="!readable" class="reg-note" role="status">The registry is visible to people who can read models. Ask a workspace admin.</p>
      <p v-else-if="phase === 'loading'" class="reg-note" role="status">Loading models…</p>
      <div v-else-if="phase === 'error'" class="reg-note" role="status" data-reg-load-error>
        <span>Models couldn’t load. Agents keep running on the last saved choices.</span>
        <button type="button" class="btn sm" data-reg-retry @click="load">Try again</button>
      </div>
      <template v-else>
        <p v-if="registry.rows" class="reg-count" data-reg-count>{{ registryCount(registry) }}</p>
        <div class="reg-list" aria-label="Registered models" role="group">
          <div v-if="editing === 'new'" class="reg-new">
            <ModelRegistryEditor :ref="bindEditor" v-model:draft="draft" mode="new" :line="null" :errors="errors" :saving="saving" :form-error="formError" @save="save" @cancel="closeEditor(true)" @clear="clearError" />
          </div>
          <div v-for="group in registry.groups" :key="group.provider" class="pg" role="group" :aria-labelledby="`reg-pg-${group.provider}`" :data-reg-provider="group.provider">
            <p :id="`reg-pg-${group.provider}`" class="pg-h"><span>{{ group.label }}</span><span class="pg-n">{{ group.rows.length }} {{ group.rows.length === 1 ? 'line' : 'lines' }}</span></p>
            <ul class="pg-list">
              <li v-for="row in group.rows" :key="row.key" :data-reg-row="row.key">
                <ModelLineRow :row="row" :open="opened.has(row.key)" :manage="manageable" :now="clock" :editing="editing" :pausing="pauseMenu?.key === row.key" :busy="busyRow(row)"
                  @toggle="toggleRow(row.key)" @edit="editRoute" @pause="openPause(row, $event)" @remove="askRemove(row, $event)">
                  <div v-if="question?.key === row.key" class="reg-confirm" data-reg-confirm :role="question.state === 'error' || question.failed ? 'alert' : 'status'" @keydown="questionKeys">
                    <p v-if="question.state === 'loading'">Checking where {{ row.model.name }} is used…</p>
                    <p v-else-if="question.state === 'error'">It could not be checked where {{ row.model.name }} is used, so it can’t be removed yet.</p>
                    <p v-else>{{ question.text }}</p>
                    <p v-if="question.failed" class="reg-confirm-err" data-reg-error><AppIcon name="alert" :size="14" />{{ question.failed }}</p>
                    <div class="reg-confirm-acts">
                      <button type="button" class="btn sm ghost" data-reg-keep :disabled="question.removing" @click="closeQuestion(true)">Keep it</button>
                      <button v-if="question.state === 'error'" type="button" class="btn sm" data-reg-retry-remove @click="retryRemove">Try again</button>
                      <button type="button" class="btn sm danger" data-reg-confirm-remove :aria-disabled="question.state !== 'ready' || question.removing" :aria-busy="question.removing" @click="remove">Remove</button>
                    </div>
                  </div>
                </ModelLineRow>
                <ModelRegistryEditor v-if="editingIn(row)" :ref="bindEditor" v-model:draft="draft" :mode="mode" :line="editedLine" :errors="errors" :saving="saving" :form-error="formError"
                  @save="save" @cancel="closeEditor(true)" @clear="clearError" />
              </li>
            </ul>
          </div>
          <p v-if="!registry.rows && editing !== 'new'" class="reg-note" role="status">No models are registered yet.</p>
        </div>
      </template>
    </div>
    <ModelOutdatedSheet v-if="sheetOpen && entries.length" :entries="entries" :auto-on="autoOn" :incomplete="outdated?.incomplete === true" :more="more" :busy="removingOld"
      @close="sheetOpen = false" @remove="retireOld" />
    <ModelsPopover v-if="pauseMenu && pauseRow" :open="true" :anchor="pauseMenu.anchor" :label="`Pause ${pauseRow.model.name}`" role="presentation" :width="280" @update:open="value => { if (!value) pauseMenu = null }">
      <div class="reg-menu" role="menu" :aria-label="`Pause ${pauseRow.model.name}`" data-reg-pause-menu @keydown="pauseKeys">
        <button v-for="(option, index) in pauseChoices" :key="option.choice" type="button" class="mi" role="menuitem" :data-autofocus="index === 0 ? '' : undefined" :data-reg-pause-choice="option.choice" @click="choosePause(option.choice)">
          <AppIcon :name="option.choice === 'resume' ? 'play' : 'pause'" :size="14" /><span class="t">{{ option.label }}</span><span class="d">{{ option.detail }}</span>
        </button>
      </div>
    </ModelsPopover>
  </section>
</template>

<style scoped>
.reg-card { padding: 26px 28px 12px; scroll-margin-top: 20px; container-type: inline-size; }
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

/* Tabular figures: "again in 1 min" and "again in 5 min" take one width, so the shared box never changes size. */
.reg-check-label { justify-items: end; font-variant-numeric: tabular-nums; }
.reg-check-label > span { display: inline-flex; align-items: center; gap: 6px; }
.reg-check .spin { animation: reg-spin 1s linear infinite; }
@keyframes reg-spin { to { transform: rotate(360deg); } }
@media (prefers-reduced-motion: reduce) { .reg-check .spin { animation: none; } }

.reg-tray { display: grid; grid-template-columns: 16px minmax(0, 1fr) auto; align-items: center; column-gap: 10px; row-gap: 8px; margin-top: 10px; padding: 8px 8px 8px 14px; border-radius: 12px; background: var(--row-selected); font-size: 13px; line-height: 1.5; color: var(--ink-2); }
.reg-tray-ic { color: var(--teal-ink); }
.reg-tray-t { min-width: 0; }
.reg-tray-t b { font-weight: 600; color: var(--ink); }

.reg-body { margin-top: 4px; }
.reg-count { padding: 14px 16px 0; font-size: 12px; color: var(--ink-3); }
.reg-note { display: flex; flex-wrap: wrap; align-items: center; gap: 8px 14px; padding: 16px; font-size: 13px; color: var(--ink-2); }
.reg-list { margin-top: 4px; }
.reg-new { padding-top: 10px; }
.pg { margin-top: 14px; }
.pg-h { display: flex; justify-content: space-between; gap: 12px; padding: 0 16px 6px; border-bottom: 1px solid var(--line); font: 500 10.5px/1.5 var(--mono); letter-spacing: .14em; text-transform: uppercase; color: var(--ink-3); }
.pg-n { letter-spacing: .06em; text-transform: none; }
.pg-list { list-style: none; margin: 0; padding: 0; }
.pg-list > li:last-child :deep(.ln) { border-bottom: 0; }

/* The remove question grows under the row's actions; its buttons sit on its first line's right, then stack. */
.reg-confirm { display: flex; flex-wrap: wrap; align-items: flex-start; justify-content: flex-end; gap: 8px 16px; margin-top: 10px; padding-top: 12px; border-top: 1px solid var(--line); }
.reg-confirm > p { flex: 1 1 320px; font-size: 13px; line-height: 1.5; color: var(--ink-2); }
.reg-confirm > .reg-confirm-err { display: flex; align-items: flex-start; gap: 8px; flex-basis: 100%; order: 3; color: var(--danger); }
.reg-confirm-err :deep(svg) { flex: none; margin-top: 3px; }
.reg-confirm-acts { display: flex; flex-wrap: wrap; gap: 8px; }
.reg-confirm-acts [aria-disabled="true"] { opacity: .55; cursor: default; }

/* The Pause… menu: teleported, so these rules reach it through the scope of this template. */
.reg-menu { display: grid; padding: 6px; }
.mi { display: grid; grid-template-columns: 20px minmax(0, 1fr); align-items: center; gap: 2px 10px; width: 100%; min-height: 48px; padding: 8px 10px; border: 0; border-radius: 8px; background: transparent; color: var(--ink); font: inherit; text-align: left; cursor: pointer; }
.mi:hover, .mi:focus-visible { background: var(--row-hover); outline: none; }
.mi:focus-visible { box-shadow: inset 0 0 0 1px var(--line-2); }
.mi :deep(svg) { grid-row: span 2; align-self: start; margin-top: 3px; color: var(--ink-2); }
.mi .t { font-size: 13.5px; font-weight: 600; }
.mi .d { font-size: 12px; line-height: 1.4; color: var(--ink-3); }

@container (max-width: 600px) {
  .reg-card { padding: 18px 14px 6px; }
  .reg-head { flex-wrap: wrap; row-gap: 12px; }
  .reg-titles { flex-basis: 100%; }
  .reg-strip { flex-wrap: wrap; gap: 4px 10px; padding: 8px 12px; }
  .reg-check { margin-left: auto; }
  .reg-text { order: 3; flex: 1 1 100%; padding-bottom: 4px; }
  /* On a phone, Review… gets its own line under the sentence instead of squeezing it (Tick, AEON-1164). */
  .reg-tray { grid-template-columns: 16px minmax(0, 1fr); padding: 10px 12px; }
  .reg-tray-ic { align-self: start; margin-top: 2px; }
  .reg-review { grid-column: 2; justify-self: start; }
  .reg-count, .pg-h { padding-left: 12px; padding-right: 12px; }
}
@media (pointer: coarse) { .mi { min-height: 52px; } }
</style>
<style scoped src="../../../styles/settingsButtons.css"></style>
<style src="../../../styles/models.css"></style>
