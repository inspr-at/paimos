<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, inject, nextTick, onBeforeUnmount, onMounted, ref, shallowRef } from 'vue'
import { DELIVERY_ACTIONS } from '../../lib/deliveryChanges'
import { releaseName, type PlanningRelease } from '../../lib/deliveryPlanning'
import { refusal } from '../../lib/deliveryMoves'
import { actionReason, deadlineValue, lifecycleCopy, localDeadline, releasePath, requestRelease, type ReleaseAction, type ReleaseRecord, type ReleaseRights } from '../../lib/releaseActions'
import { useReleaseRecovery } from '../../lib/useReleaseRecovery'
import KeyCap from '../KeyCap.vue'

const props = defineProps<{ release: ReleaseRecord; rights: ReleaseRights; identity: string; currentIdentity: () => string; releases: PlanningRelease[]; screen?: 'menu' | 'plan' | 'abandoned' }>()
const emit = defineEmits<{ close: []; move: [action: 'top' | 'after']; saved: [release: ReleaseRecord]; feedback: [message: string] }>()
const dialog = ref<HTMLDialogElement>(), screen = ref<ReleaseAction | 'menu' | 'plan' | 'abandoned'>(props.screen ?? 'menu')
const captured = { ...props.release }, record = shallowRef<ReleaseRecord>({ ...captured })
const actions = inject(DELIVERY_ACTIONS, undefined), controller = new AbortController()
let disposed = false, generation = 0
const current = () => !disposed && props.identity === props.currentIdentity() && !controller.signal.aborted
const recovery = useReleaseRecovery({ release: captured, current, allowed: () => props.rights.person && props.rights.write, actions, signal: controller.signal })
const { rows, counts, cursors, ready, incomplete, all, selected, total, busy: recovering, error: recoveryError, result: recoveryResult } = recovery
const recoveryExcluded = (id: string) => recovery.excluded.value.has(id)
const laterName = (id?: string) => { const release = props.releases.find(row => row.release_id === id); return release ? releaseName(release) : `Later release${id ? ` · ${id}` : ''}` }
const busy = ref(false), error = ref(''), hint = ref(''), loaded = ref(false)
const title = ref(captured.title), body = ref(captured.body ?? ''), visibility = ref(captured.visibility), deadline = ref(localDeadline(captured.entry_closes_at))
const scheme = ref(captured.version_scheme || ''), version = ref(''), reservation = ref(''), creationKey = crypto.randomUUID()
type Window = { timezone: string; slots: { days: number[]; from: string; to: string }[] }
type Settings = { budget_agent_hours?: number | null; max_agents?: number | null; largest_ticket_hours?: number | null; window?: Window | null }
const settings = ref<Settings>({}), defaults = ref<Settings>({}), resolved = ref<Settings>({})
const settingsFields = [{ key: 'budget_agent_hours', name: 'Budget', unit: 'Agent-hours per window' }, { key: 'max_agents', name: 'Most agents at once', unit: 'Within the person’s own limit' }, { key: 'largest_ticket_hours', name: 'Largest ticket', unit: 'Agent-hours per ticket' }] as const
const override = ref<Record<string, boolean>>({}), values = ref<Record<string, number | undefined>>({})
const windowOverride = ref(false), windowTimezone = ref(Intl.DateTimeFormat().resolvedOptions().timeZone), windowFrom = ref('20:00'), windowTo = ref('02:00'), windowDays = ref<number[]>([0,1,2,3,4,5,6]), windowEdited = ref(false)
const names: Partial<Record<ReleaseAction | 'menu' | 'plan' | 'abandoned', string>> = { menu: 'Release actions', settings: 'Release settings', freeze: 'Freeze', unfreeze: 'Unfreeze', cut: 'Cut', publish: 'Publish', close: 'Close', abandon: 'Abandon', building: 'Mark building', planned: 'Return to planned', notes: 'Notes', plan: 'Plan a release', abandoned: 'Abandoned releases' }
const label = computed(() => `${names[screen.value]}${['plan','abandoned'].includes(screen.value) ? '' : ` · ${releaseName(record.value)}`}`)
const locked = computed(() => busy.value || recovering.value)
const blocked = computed(() => screen.value === 'plan' ? !props.rights.person ? 'Only a person can plan releases.' : !props.rights.write ? 'Release edit permission is required.' : '' : ['menu','abandoned'].includes(screen.value) ? '' : actionReason(screen.value as ReleaseAction, { ...record.value, revision: recovery.revision.value }, props.rights))
const saveDisabled = computed(() => locked.value || !!blocked.value || !current() || !!error.value && !loaded.value || screen.value === 'settings' && !loaded.value)
const entries = computed(() => [
  { action: 'build', label: 'Build', key: 'b' }, { action: 'settings', label: 'Settings…', key: 's' },
  { action: 'freeze', label: 'Freeze', key: 'f' }, { action: 'unfreeze', label: 'Unfreeze', key: 'u' }, { action: 'cut', label: 'Cut', key: 'c' },
  { action: record.value.visibility === 'internal' ? 'close' : 'publish', label: record.value.visibility === 'internal' ? 'Close' : 'Publish', key: 'p' },
  { action: record.value.state === 'building' ? 'planned' : 'building', label: record.value.state === 'building' ? 'Return to planned' : 'Mark building', key: 'l' },
  { action: 'notes', label: 'Notes', key: 'n' }, { action: 'top', label: 'Move to top', key: 't' }, { action: 'after', label: 'Move after…', key: 'm' }, { action: 'abandon', label: 'Abandon', key: 'x' },
] as { action: ReleaseAction; label: string; key: string }[])
const notes = shallowRef<{ frozen?: boolean; unavailable?: boolean; reason?: string; tickets?: { key: string; unavailable?: string; fields: Record<string, string> }[] } | null>(null)
const abandoned = shallowRef<PlanningRelease[]>([]), abandonedCursor = ref('')
function explain(action: ReleaseAction) { hint.value = actionReason(action, record.value, props.rights) || (action === 'freeze' && !props.rights.person ? 'An agent’s freeze includes no completed work.' : lifecycleCopy[action] ?? '') }
function assertCurrent() { if (!current()) throw new Error('The project, person or release changed. Reopen this sheet.') }
async function detail() {
  const answer = await requestRelease(releasePath(captured.project_id, captured.release_id), { signal: controller.signal }) as ReleaseRecord & { build_settings?: Settings; resolved_build_settings?: Settings }
  assertCurrent()
  if (answer.project_id !== captured.project_id || answer.release_id !== captured.release_id || answer.revision !== recovery.revision.value) throw new Error('This release changed. Reopen its actions before saving.')
  record.value = { ...record.value, ...answer }; title.value = answer.title; body.value = answer.body ?? ''; visibility.value = answer.visibility; deadline.value = localDeadline(answer.entry_closes_at)
  settings.value = answer.build_settings ?? {}; resolved.value = answer.resolved_build_settings ?? {}
  for (const field of settingsFields) { override.value[field.key] = settings.value[field.key] != null; values.value[field.key] = settings.value[field.key] ?? resolved.value[field.key] ?? undefined }
  const window = settings.value.window ?? resolved.value.window
  windowOverride.value = !!settings.value.window
  if (window) { windowTimezone.value = window.timezone; const slot = window.slots[0]; if (slot) { windowFrom.value = slot.from; windowTo.value = slot.to; windowDays.value = [...slot.days] } }
}
async function choose(action: ReleaseAction) {
  if (locked.value) return
  explain(action)
  if (actionReason(action, record.value, props.rights)) return
  if (action === 'top' || action === 'after') { emit('move', action); return }
  screen.value = action; error.value = ''; loaded.value = false; hint.value = ''; busy.value = true
  const token = ++generation
  try {
    await detail()
    if (token !== generation || !current()) return
    if (action === 'settings') {
      const project = await requestRelease(`/projects/${encodeURIComponent(captured.project_id)}/delivery`, { signal: controller.signal }) as { project_id: string; build_defaults?: Settings }
      assertCurrent(); if (project.project_id !== captured.project_id) throw new Error('Invalid project settings.'); defaults.value = project.build_defaults ?? {}
    }
    if (action === 'notes') notes.value = await requestRelease(`${releasePath(captured.project_id, captured.release_id)}/note-snapshot`, { signal: controller.signal }) as typeof notes.value
    assertCurrent(); loaded.value = true
    if (action === 'freeze' || ['cut','close','publish'].includes(action)) await recovery.load()
  } catch (e) { if (current() && token === generation) error.value = refusal(e) }
  finally { if (token === generation) busy.value = false }
}
function setOverride(key: typeof settingsFields[number]['key'], enabled: boolean) { override.value[key] = enabled; values.value[key] = enabled ? settings.value[key] ?? resolved.value[key] ?? 1 : resolved.value[key] ?? undefined }
function settingsPayload(): Settings {
  const next: Settings = {}
  for (const field of settingsFields) if (override.value[field.key]) {
    const value = values.value[field.key], limit = defaults.value[field.key]
    if (!value || !Number.isFinite(value) || value <= 0 || value > (field.key === 'max_agents' ? 1000 : 1_000_000) || field.key === 'max_agents' && !Number.isInteger(value)) throw new Error(`Choose a valid positive value for ${field.name}.`)
    if (!props.rights.deploy && (limit == null || value > limit)) throw new Error('Widening a project limit requires deployment permission.')
    next[field.key] = value
  }
  if (windowOverride.value) {
    next.window = !windowEdited.value && settings.value.window ? settings.value.window : { timezone: windowTimezone.value, slots: [{ days: [...windowDays.value], from: windowFrom.value, to: windowTo.value }] }
    if (!next.window.slots.length || !windowDays.value.length) throw new Error('Choose at least one build day.')
    if (!props.rights.deploy && !defaults.value.window) throw new Error('Setting a build window without a project limit requires deployment permission.')
  }
  if (new TextEncoder().encode(JSON.stringify(next)).length > 2048) throw new Error('Build settings are too large.')
  return next
}
async function submit() {
  if (saveDisabled.value) return
  busy.value = true; error.value = ''
  let identity: string | undefined
  try {
    assertCurrent()
    let path = releasePath(captured.project_id, captured.release_id), method = 'POST'
    const action = screen.value
    let payload: Record<string, unknown> = { expected_revision: recovery.revision.value }
    if (action === 'settings' || action === 'plan') {
      const bytes = new TextEncoder()
      if (!title.value.trim() || bytes.encode(title.value.trim()).length > 512 || bytes.encode(body.value).length > 65536) throw new Error('Use a name up to 512 bytes and a brief up to 64 KiB.')
      if (action === 'plan') { path = releasePath(captured.project_id); payload = { title: title.value.trim(), visibility: visibility.value, entry_closes_at: deadlineValue(deadline.value), creation_key: creationKey } }
      else { method = 'PATCH'; payload.title = title.value.trim(); payload.body = body.value; payload.build_settings = settingsPayload(); if (record.value.state === 'planned') { payload.visibility = visibility.value; payload.entry_closes_at = deadlineValue(deadline.value) } }
    } else if (['freeze','unfreeze','abandon','building','planned'].includes(action)) {
      path += '/state'; payload.to = ({ freeze: 'frozen', unfreeze: 'building', abandon: 'abandoned', building: 'building', planned: 'planned' } as Record<string, string>)[action]
    } else {
      path += `/${action}`
      if (action === 'cut') { if (!scheme.value || !version.value.trim() || version.value.length > 128) throw new Error('Choose the project’s version scheme and enter its exact reserved version.'); payload.version_scheme = scheme.value; payload.version = version.value.trim() }
      if (action === 'publish' && !props.rights.product) { if (!reservation.value.trim() || reservation.value.length > 512) throw new Error('Add the reservation PR or tag URL (up to 512 characters).'); payload.reservation_ref = reservation.value.trim() }
    }
    identity = actions?.begin()
    const answer = await requestRelease(path, { method, signal: controller.signal, headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(payload) }) as ReleaseRecord & { recovery?: { completed_unplaced: number; completed_later: number; incomplete: boolean } }
    assertCurrent()
    if (answer.project_id !== captured.project_id || !answer.release_id || action !== 'plan' && answer.release_id !== captured.release_id || !Number.isSafeInteger(answer.revision) || answer.revision < recovery.revision.value) throw new Error('Invalid release result. Reload before continuing.')
    const updated = { ...record.value, ...(action === 'settings' || action === 'plan' ? { title: title.value.trim(), display_name: props.rights.product && visibility.value === 'published' ? record.value.display_name : title.value.trim(), body: body.value, visibility: visibility.value } : {}), ...answer }
    if (actions && identity && !actions.commit(identity, { kind: 'lifecycle', result: updated })) throw new Error('The action belongs to a previous view; its result was discarded.')
    emit('saved', updated)
    const remaining = answer.recovery ? answer.recovery.completed_unplaced + answer.recovery.completed_later : undefined
    emit('feedback', `${names[action]} saved for ${releaseName(updated)}.${remaining === undefined ? '' : ` ${answer.recovery?.incomplete ? 'At least ' : ''}${remaining} completed items left out.`}`)
    emit('close')
  } catch (e) { if (identity) actions?.failed(identity); if (current()) error.value = refusal(e) }
  finally { busy.value = false }
}
async function loadAbandoned(cursor = '') {
  if (busy.value) return
  busy.value = true; error.value = ''
  try {
    const query = new URLSearchParams({ state: 'abandoned', limit: '50' }); if (cursor) query.set('cursor', cursor)
    const page = await requestRelease(`${releasePath(captured.project_id)}?${query}`, { signal: controller.signal }) as { items: PlanningRelease[]; next_cursor?: string }
    assertCurrent(); if (!Array.isArray(page.items) || page.items.length > 50 || page.items.some(row => row.project_id !== captured.project_id || row.state !== 'abandoned') || cursor && page.next_cursor === cursor) throw new Error('Invalid abandoned release page.')
    abandoned.value = page.items; abandonedCursor.value = page.next_cursor ?? ''; loaded.value = true
  } catch (e) { if (current()) error.value = refusal(e) }
  finally { busy.value = false }
}
function close() { emit('close') }
function keys(e: KeyboardEvent) {
  const field = e.target instanceof Element && !!e.target.closest('input,textarea,select,[contenteditable="true"]')
  if (e.key === 'Escape') { e.preventDefault(); e.stopPropagation(); if (field) (e.target as HTMLElement).blur(); else close(); return }
  const mac = /Mac|iPhone|iPad/.test(navigator.platform)
  if (e.key === 'Enter' && (mac ? e.metaKey && !e.ctrlKey : e.ctrlKey && !e.metaKey) && !e.altKey && !e.shiftKey) { e.preventDefault(); if (!['menu','notes','abandoned'].includes(screen.value)) void submit(); return }
  if (field || e.metaKey || e.ctrlKey || e.altKey || e.shiftKey || screen.value !== 'menu') return
  const entry = entries.value.find(row => row.key === e.key)
  if (entry) { e.preventDefault(); void choose(entry.action) }
}
onMounted(async () => { await nextTick(); dialog.value?.showModal(); dialog.value?.querySelector<HTMLButtonElement>('[data-cancel]')?.focus({ preventScroll: true }); if (screen.value === 'abandoned') void loadAbandoned(); if (screen.value === 'plan') { title.value = ''; body.value = ''; deadline.value = ''; loaded.value = true } })
onBeforeUnmount(() => { disposed = true; generation++; controller.abort() })
</script>
<template>
  <dialog ref="dialog" class="release-sheet" :class="{ long: screen === 'freeze' }" :aria-label="label" @keydown="keys" @cancel.prevent="close">
    <div class="sheet-frame">
      <header><h2 v-clip-tip="label">{{ label }}</h2></header>
      <div class="sheet-actions" :class="{ 'freeze-actions': screen === 'freeze' }" role="group" aria-label="Release sheet actions">
        <button type="button" data-cancel @click="close">{{ ['menu','notes','abandoned'].includes(screen) ? 'Done' : 'Cancel' }} <KeyCap k="Esc" /></button>
        <template v-if="screen === 'freeze'"><button type="button" :disabled="locked || !ready || !selected || !rights.person || !rights.write" @click="recovery.include">Include ({{ selected }})</button><button type="button" class="primary" :disabled="saveDisabled || !loaded" @click="submit">Freeze <KeyCap k="mod" /><KeyCap k="enter" /></button></template>
        <button v-else-if="!['menu','notes','abandoned'].includes(screen)" type="button" class="primary" :disabled="saveDisabled" @click="submit">{{ screen === 'settings' ? 'Save' : names[screen] }} <KeyCap k="mod" /><KeyCap k="enter" /></button>
        <button v-if="screen === 'abandoned'" type="button" :disabled="busy || !abandonedCursor" @click="loadAbandoned(abandonedCursor)">Load more</button>
      </div>
      <div class="sheet-body" :aria-busy="locked">
        <div v-if="screen === 'menu'" class="menu-options" role="group" aria-label="Release actions">
          <button v-for="entry in entries" :key="entry.action" type="button" :aria-disabled="!!actionReason(entry.action, record, rights)" :data-tip="actionReason(entry.action, record, rights)" @focus="explain(entry.action)" @pointerenter="explain(entry.action)" @click="choose(entry.action)"><span>{{ entry.label }}</span><KeyCap :k="entry.key" /></button>
        </div>
        <template v-if="screen === 'settings' || screen === 'plan'">
          <div class="settings-controls" role="group" aria-label="Release settings">
            <label>Name<input v-model="title" aria-label="Release name" maxlength="512" :disabled="locked || screen === 'settings' && !loaded" /></label>
            <label>Kind<select v-model="visibility" aria-label="Release kind" :disabled="locked || screen === 'settings' && (record.state !== 'planned' || !loaded)"><option value="published">Published</option><option value="internal" :disabled="screen === 'settings' && record.visibility === 'published'">Internal</option></select></label>
            <label class="wide">Entry closes · optional<input v-model="deadline" aria-label="Entry closes" type="datetime-local" :disabled="locked || screen === 'settings' && (record.state !== 'planned' || !loaded)" /></label>
            <template v-if="screen === 'settings'">
              <h3 class="wide">Build settings</h3>
              <div v-for="field in settingsFields" :key="field.key" class="setting-row wide">
                <label>{{ field.name }}<span>{{ field.unit }}</span><input v-model.number="values[field.key]" :aria-label="field.name" type="number" min="0.01" :max="field.key === 'max_agents' ? 1000 : 1000000" :step="field.key === 'max_agents' ? 1 : 'any'" :disabled="locked || !loaded || !override[field.key]" :placeholder="resolved[field.key] == null ? 'No project limit' : String(resolved[field.key])" /></label>
                <label class="override"><input type="checkbox" :checked="override[field.key]" :disabled="locked || !loaded" :aria-label="`Override ${field.name}`" @change="setOverride(field.key, ($event.target as HTMLInputElement).checked)" />Override</label>
                <button type="button" :disabled="locked || !loaded || !override[field.key]" :aria-label="`Reset ${field.name}`" @click="setOverride(field.key, false)">Reset</button>
              </div>
              <div class="window-controls wide"><label class="override"><input v-model="windowOverride" type="checkbox" :disabled="locked || !loaded" aria-label="Override Build window" />Override build window</label><button type="button" :disabled="locked || !loaded || !windowOverride" @click="windowOverride = false; windowEdited = false">Reset window</button></div>
              <label>Timezone<input v-model="windowTimezone" aria-label="Build timezone" :disabled="locked || !loaded || !windowOverride" @input="windowEdited = true" /></label>
              <div class="window-times"><label>From<input v-model="windowFrom" type="time" aria-label="Build from" :disabled="locked || !loaded || !windowOverride" @input="windowEdited = true" /></label><label>To<input v-model="windowTo" type="time" aria-label="Build to" :disabled="locked || !loaded || !windowOverride" @input="windowEdited = true" /></label></div>
              <fieldset class="days wide"><legend>Build days</legend><label v-for="(day, index) in ['Sun','Mon','Tue','Wed','Thu','Fri','Sat']" :key="day"><input v-model="windowDays" type="checkbox" :value="index" :disabled="locked || !loaded || !windowOverride" @change="windowEdited = true" />{{ day }}</label></fieldset>
            </template>
          </div>
          <div class="copy"><p>After entry closes, only a person with release edit rights can add work. Once frozen, nobody can.</p><p>{{ screen === 'plan' || record.state === 'planned' ? 'The optional entry deadline is editable while planned and is separate from build limits.' : `Entry and kind are locked because this release is ${record.state}.` }}</p><p v-if="screen === 'settings'">An override can tighten the project’s limit; widening it needs deployment permission. Unchanged multi-slot windows are preserved; editing the window replaces it with the days and times above.</p><p v-else>{{ visibility === 'internal' ? 'Internal work uses a plain title, with no number, codename, version or publication.' : 'A published release joins the project’s sequence; Cut reserves its version later.' }}</p></div>
          <label v-if="screen === 'settings'" class="brief">Brief<textarea v-model="body" aria-label="Release brief" :disabled="locked || !loaded" maxlength="65536" rows="3" /></label>
        </template>
        <template v-if="screen === 'freeze'">
          <p class="copy">First include finished work if it belongs here. Once frozen, nobody can add work. Items left out stay listed for a later Freeze.</p>
          <div class="recovery-controls" role="group" aria-label="Recovery choices"><label class="override"><input type="checkbox" :checked="all" :disabled="locked || !ready || incomplete || !rights.person || !rights.write" aria-label="All completed work" @change="recovery.toggleAll(($event.target as HTMLInputElement).checked)" />All {{ incomplete ? '5,000+' : total }}</label><span>{{ recovery.visible.value.length }} shown · {{ incomplete ? 'incomplete list' : `${total} listed` }}</span></div>
          <p v-if="!rights.person" class="copy">An agent includes nothing. Freeze leaves the completed recovery work out.</p>
          <p v-else-if="!rights.write" class="copy">Release edit permission is required to Include work.</p>
          <section v-for="source in (['unplaced','later'] as const)" :key="source" class="recovery-section">
            <div class="recovery-heading"><h3>{{ source === 'unplaced' ? 'Completed, not in any release' : 'Completed in a later release' }} · {{ counts[source] }}{{ incomplete ? '+' : '' }}</h3><button type="button" :disabled="locked || !cursors[source]" @click="recovery.more(source)">Next {{ source === 'unplaced' ? 'unplaced' : 'later' }} page</button></div>
            <label v-for="item in rows[source]" :key="item.item_id" class="recovery-row" :data-recovery-item="item.item_id"><input type="checkbox" :checked="!recovery.visible.value.length ? false : !recoveryExcluded(item.item_id)" :disabled="locked || !rights.person || !rights.write" :aria-label="`Include ${item.key}`" @change="recovery.checked(item.item_id, ($event.target as HTMLInputElement).checked)" /><span><b>{{ item.key }}</b> {{ item.title }}<small>{{ source === 'unplaced' ? 'Not in a release' : laterName(item.release_id) }} · {{ item.state }}</small></span></label>
            <p v-if="ready && !rows[source].length" class="empty">No completed work on this page.</p>
          </section>
        </template>
        <template v-if="!['menu','settings','plan','freeze','notes','abandoned'].includes(screen)">
          <div v-if="screen === 'cut'" class="lifecycle-controls" role="group" aria-label="Cut version"><label>Version scheme<select v-model="scheme" aria-label="Version scheme" :disabled="locked"><option value="">Choose the project’s scheme</option><option value="legacy">Legacy</option><option value="inspr-calendar-v1">Calendar v1</option><option value="inspr-calendar-v2">Calendar v2</option></select></label><label>Reserved version<input v-model="version" aria-label="Reserved version" maxlength="128" :disabled="locked" /></label></div>
          <label v-if="screen === 'publish' && !rights.product" class="brief">Reservation PR or tag URL<input v-model="reservation" aria-label="Reservation reference" maxlength="512" :disabled="locked" /></label>
          <div class="copy"><p>{{ lifecycleCopy[screen as ReleaseAction] }}</p><p>Confirming applies immediately. This action has no Undo.</p><p v-if="['cut','close','publish'].includes(screen)">{{ ready ? `${incomplete ? 'At least ' : ''}${total} completed items remain outside this release. They are left out; this warning does not block the action.` : 'The completed-work warning could not be read. No recovery work is included by this action.' }}</p><p v-if="screen === 'publish' && !rights.product">This reservation is attested by the person publishing it. It is not verified against the product’s history.</p><p v-if="record.reservation_basis">Reservation {{ record.reservation_basis }}{{ record.released_by ? ` by ${record.released_by}` : '' }}.</p></div>
        </template>
        <template v-if="screen === 'notes'"><p class="copy">{{ notes?.unavailable ? notes.reason || 'Notes are unavailable.' : notes?.frozen ? 'Stored publication notes.' : 'Draft · cumulative notes preview. Nothing is published yet.' }}</p><article v-for="entry in notes?.tickets ?? []" :key="entry.key" class="note"><h3>{{ entry.key }} · {{ entry.fields?.pill_en || entry.fields?.pill_de || entry.key }}</h3><p>{{ entry.unavailable || entry.fields?.benefit_en || entry.fields?.benefit_de || 'No release-note text recorded.' }}</p></article><p v-if="loaded && !notes?.tickets?.length && !notes?.unavailable" class="empty">No visible note entries.</p></template>
        <template v-if="screen === 'abandoned'"><p class="copy">Finished for good. Names and versions stay reserved; abandoned releases hold no work.</p><article v-for="release in abandoned" :key="release.release_id" class="note"><h3>{{ releaseName(release) }}</h3><p v-if="release.version">{{ release.version }} · remains reserved</p></article><p v-if="loaded && !abandoned.length" class="empty">No abandoned releases on this page.</p></template>
        <p v-if="screen === 'menu'" class="menu-explanation" role="status">{{ hint || 'Moves have Undo. Lifecycle actions ask for confirmation and have no Undo.' }}</p>
      </div>
      <div class="sheet-feedback" role="status" aria-live="polite"><span>{{ error || blocked || recoveryError || recoveryResult || (locked ? 'Working…' : '') }}</span><button v-if="screen === 'freeze'" type="button" :disabled="locked" @click="recovery.load">Refresh list</button><button v-if="screen === 'abandoned'" type="button" :disabled="locked" @click="loadAbandoned()">Retry</button></div>
    </div>
  </dialog>
</template>
<style scoped>
.release-sheet { position: fixed; inset: 7vh auto auto 50%; transform: translateX(-50%); margin: 0; padding: 0; width: min(46rem, calc(100vw - 32px)); max-width: none; max-height: 86dvh; border: 1px solid var(--line-2); border-radius: 16px; color: var(--ink); background: var(--surface-raised); box-shadow: var(--shadow-pop); }
.release-sheet::backdrop { background: var(--scrim); }
.sheet-frame { display: flex; flex-direction: column; max-height: 86dvh; }
header { flex-shrink: 0; padding: 18px 20px 10px; }
h2 { margin: 0; font-size: 17px; line-height: 22px; height: 44px; display: -webkit-box; -webkit-line-clamp: 2; -webkit-box-orient: vertical; overflow: hidden; overflow-wrap: anywhere; }
h3 { margin: 0; font-size: 13px; line-height: 20px; }
.sheet-actions { display: grid; grid-auto-flow: column; grid-auto-columns: minmax(0, 1fr); gap: 8px; padding: 8px 20px; flex-shrink: 0; border-bottom: 1px solid var(--line); }
button { min-height: 44px; padding: 8px 10px; color: var(--ink); background: transparent; border: 0; border-radius: 5px; font-size: 12px; line-height: 18px; }
button:hover:not(:disabled) { background: var(--row-hover); }
button:focus-visible, input:focus-visible, select:focus-visible, textarea:focus-visible { outline: none; box-shadow: var(--focus-ring); }
.primary { color: var(--teal-ink); background: var(--chip-teal-bg); box-shadow: inset 0 0 0 1px var(--chip-teal-line); }
.sheet-body { padding: 12px 20px 16px; overflow: auto; min-height: 0; }
.sheet-feedback { order: 4; flex-shrink: 0; display: flex; align-items: center; gap: 8px; padding: 0 20px; height: 64px; border-top: 1px solid var(--line); font-size: 12px; line-height: 18px; }
.sheet-feedback span { flex: 1; min-width: 0; max-height: 54px; overflow: auto; overflow-wrap: anywhere; }
.sheet-feedback button { flex-shrink: 0; }
.menu-options { display: grid; }
.menu-options button { display: flex; justify-content: space-between; align-items: center; height: 44px; border-bottom: 1px solid var(--line); border-radius: 0; text-align: left; }
.menu-options button[aria-disabled="true"] { color: var(--ink-3); }
.menu-explanation { margin: 10px 0 0; height: 54px; overflow: auto; color: var(--ink-2); font-size: 12px; line-height: 18px; }
.settings-controls, .lifecycle-controls { display: grid; grid-template-columns: minmax(0, 1fr) minmax(0, 1fr); gap: 12px; }
label { display: grid; gap: 5px; min-width: 0; font-size: 12px; }
input:not([type="checkbox"]), select, textarea { width: 100%; min-width: 0; box-sizing: border-box; padding: 8px; border: 1px solid var(--line-2); border-radius: 5px; color: var(--ink); background: var(--field-bg); font-size: 13px; }
input:not([type="checkbox"]), select { height: 44px; }
.wide { grid-column: 1 / -1; }
.setting-row { display: grid; grid-template-columns: minmax(0, 1fr) auto auto; align-items: center; gap: 12px; border-bottom: 1px solid var(--line); padding-bottom: 12px; }
.setting-row label span { color: var(--ink-3); font-size: 11px; }
.override { display: flex; align-items: center; min-height: 44px; gap: 8px; }
input[type="checkbox"] { width: 18px; height: 18px; margin: 0; accent-color: var(--teal); flex-shrink: 0; }
.window-controls { display: flex; align-items: center; justify-content: space-between; gap: 8px; }
.window-times { display: grid; grid-template-columns: minmax(0, 1fr) minmax(0, 1fr); gap: 8px; }
.days { display: flex; flex-wrap: wrap; border: 0; padding: 0; gap: 4px 10px; }
.days label { display: flex; align-items: center; gap: 5px; min-height: 44px; }
.copy, .empty, .note p { margin: 10px 0; color: var(--ink-2); font-size: 13px; line-height: 1.55; overflow-wrap: anywhere; }
.copy p { margin: 10px 0; }
.brief { margin-top: 12px; }
.long .sheet-frame { height: min(46rem, 86dvh); }
.long .sheet-body { flex: 1; }
.freeze-actions { order: 5; border-top: 1px solid var(--line); border-bottom: 0; }
.recovery-controls { display: flex; align-items: center; justify-content: space-between; gap: 8px; font-size: 12px; }
.recovery-heading { position: sticky; top: -12px; display: grid; grid-template-columns: minmax(0, 1fr) auto; gap: 8px; align-items: center; background: var(--surface-raised); border-bottom: 1px solid var(--line); z-index: 1; }
.recovery-row { display: grid; grid-template-columns: 18px minmax(0, 1fr); align-items: start; gap: 10px; padding: 12px 0; border-bottom: 1px solid var(--line); font-size: 13px; line-height: 20px; overflow-wrap: anywhere; }
.recovery-row small { display: block; color: var(--ink-3); font-size: 11px; }
.recovery-row b { font-family: var(--mono); font-size: 11px; }
.note { padding: 12px 0; border-bottom: 1px solid var(--line); overflow-wrap: anywhere; }
@media (max-width: 720px) {
  .release-sheet { inset: 0; transform: none; width: 100%; height: 100dvh; max-height: 100dvh; border: 0; border-radius: 0; }
  .sheet-frame, .long .sheet-frame { height: 100dvh; max-height: 100dvh; padding-top: env(safe-area-inset-top); box-sizing: border-box; }
  header { padding-inline: 12px; }
  .sheet-body { flex: 1; padding-inline: 12px; }
  .sheet-actions { order: 5; padding: 8px 12px calc(8px + env(safe-area-inset-bottom)); border-top: 1px solid var(--line); border-bottom: 0; }
  .sheet-feedback { padding-inline: 12px; }
  .settings-controls, .lifecycle-controls { grid-template-columns: minmax(0, 1fr); }
  .setting-row { gap: 8px; }
  .recovery-controls { font-size: 11px; }
}
@media (pointer: coarse) { .override, .days label { min-height: 44px; } }
</style>
