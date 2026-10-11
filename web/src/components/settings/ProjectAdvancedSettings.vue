<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, onBeforeUnmount, reactive, ref, watch } from 'vue'
import { useRoute } from 'vue-router'
import type { Project } from '../../stores/projects'
import { useSession } from '../../stores/session'
import { can, myProjectRole, myWorkspaceRole } from '../../lib/authz'
import { APIError } from '../../lib/api'
import { advancedRequest, MACHINE_PATHS, pullRequestNumbers, queueSlugs, type AdmissionSettings, type AdmissionSettingsInput, type Machine, type MergeBackfillInput, type MergeBackfillResult, type QueueSettings, type ShadowMode, type ShadowSettings } from '../../lib/projectAdvanced'
import { listPlanningReleases, type PlanningRelease } from '../../lib/releaseData'
import { useIdentityScope } from '../../lib/useIdentityScope'
import { toast } from '../../lib/toast'
import { vClipTip } from '../../directives/clipTip'
import AppIcon, { type IconName } from '../AppIcon.vue'
import KeyCap from '../KeyCap.vue'
import ProjectSettingsExplainer from './ProjectSettingsExplainer.vue'
import ProjectSettingsSheet from './ProjectSettingsSheet.vue'

const props = defineProps<{ project: Project }>()
const route = useRoute(), session = useSession()
const admin = computed(() => [myWorkspaceRole()?.key, myProjectRole(props.project.id)?.key].some(role => role === 'owner' || role === 'admin'))
const scope = useIdentityScope(() => admin.value)
const open = ref(route.hash === '#advanced')
type Settings = ShadowSettings | QueueSettings | AdmissionSettings | { mode: ShadowMode }
type SettingsInput = ShadowSettings | QueueSettings | AdmissionSettingsInput | { mode: ShadowMode }
type Row = { id: Machine; name: string; description: string; icon: IconName; read: string; write: string; state: Settings | null; busy: boolean; error: string; loading: boolean }
const rows = reactive<Row[]>([
  { id: 'queue', name: 'Delivery queue', description: 'Orders, holds and releases finished work in Shadow.', icon: 'queue', read: 'delivery_queue.read', write: 'delivery_queue.manage', state: null, busy: false, error: '', loading: false },
  { id: 'reviews', name: 'Reviews', description: 'Plans the cross-family review of each round in Shadow.', icon: 'shield', read: 'delivery_reviews.read', write: 'delivery_reviews.manage', state: null, busy: false, error: '', loading: false },
  { id: 'shipping', name: 'Shipping', description: 'Plans merges and releases in Shadow.', icon: 'merge', read: 'delivery_ship.read', write: 'delivery_ship.manage', state: null, busy: false, error: '', loading: false },
  { id: 'routing', name: 'Routing', description: 'Picks a model and account for each round in Shadow.', icon: 'route', read: 'delivery.read', write: 'delivery.manage', state: null, busy: false, error: '', loading: false },
  { id: 'admission', name: 'Admission', description: 'Records how the engine would admit each start. Only a person can change it.', icon: 'gauge', read: 'engine.read', write: 'engine.manage', state: null, busy: false, error: '', loading: false },
])
const queueRow = rows[0]!, queue = computed(() => queueRow.state as QueueSettings | null)
const shadowCount = computed(() => rows.filter(row => row.id !== 'admission' && mode(row) === 'shadow').length)
const shadowSummary = computed(() => rows.filter(row => row.id !== 'admission').every(row => row.state) ? `${shadowCount.value} in Shadow` : 'switches not all read')
const mergeAllowed = computed(() => admin.value && can('delivery.manage', props.project.id) && can('nodes.write', props.project.id))
function writable(row: Row) { return admin.value && can(row.write, props.project.id) && (row.id !== 'admission' || session.identity?.principal.kind === 'person') }
function mode(row: Row): ShadowMode | null { return row.state && ('mode' in row.state ? row.state.mode : row.state.shadow_enabled ? 'shadow' : 'off') }
function message(error: unknown): string {
  return error instanceof APIError && (error.status === 409 || error.status === 412) ? 'This changed. Read it again before saving; nothing was applied.' : error instanceof Error ? error.message : 'The request failed. Nothing was applied.'
}
function failed(row: Row, error: unknown) {
  row.error = message(error)
  const project = props.project.id, owner = scope.owner.value
  toast(`${row.name}: ${row.error}`, { tone: 'error', sticky: true, key: `advanced/${project}/${row.id}`, action: { label: 'Read again', run: () => { if (project === props.project.id && owner === scope.owner.value) load(row) } } })
}
function validate(row: Row, state: Settings, project: string) {
  if (!state || (row.id === 'admission' ? !('project_id' in state) || state.project_id !== project || typeof state.shadow_enabled !== 'boolean' : !('mode' in state) || !['off', 'shadow'].includes(state.mode))) throw new Error('The server returned different settings. Try again.')
  if (row.id !== 'routing' && (!('revision' in state) || !Number.isSafeInteger(state.revision) || state.revision < 0)) throw new Error('The settings revision could not be read.')
  if (row.id === 'queue' && (!('freeze' in state) || typeof state.freeze !== 'boolean' || !Array.isArray(state.release_set) || !Array.isArray(state.held_slugs) || !Array.isArray(state.held_pull_requests) || [state.release_set, state.held_slugs, state.held_pull_requests].some(values => values.length > 100))) throw new Error('The queue settings could not be read.')
}
const reads = new Map(rows.map(row => [row.id, scope.lane()]))
function load(row: Row) {
  if (!can(row.read, props.project.id) || row.busy) return
  const project = props.project.id
  row.loading = true; row.error = ''
  void reads.get(row.id)!.run(({ after, signal }) => after(advancedRequest<Settings>(project, MACHINE_PATHS[row.id], signal), state => {
    if (project !== props.project.id || !can(row.read, project)) return
    validate(row, state, project); row.state = state
  }), { failed: error => { failed(row, error); row.state = null }, settled: () => { row.loading = false } })
}
function save(row: Row, body: SettingsInput) {
  if (!row.state || row.busy || !writable(row)) return
  const project = props.project.id
  row.busy = true; row.loading = false; row.error = ''; reads.get(row.id)!.cancel()
  void scope.run(({ after, signal }) => after(advancedRequest<Settings>(project, MACHINE_PATHS[row.id], signal, body), state => {
    if (project !== props.project.id || !writable(row)) return
    validate(row, state, project); row.state = state; toast(`${row.name} saved. Recorded in history.`)
    if (row.id === 'queue') { row.busy = false; queueEdit.value = null }
  }), { failed: error => failed(row, error), settled: () => { row.busy = false } })
}
function setMode(row: Row, value: ShadowMode) {
  if (!row.state || mode(row) === value || row.id === 'admission' || row.id === 'queue' && queueEdit.value) return
  save(row, { ...row.state, mode: value })
}
function admission(event: Event, row: Row) {
  const checked = (event.target as HTMLInputElement).checked
  ;(event.target as HTMLInputElement).checked = mode(row) === 'shadow'
  if (row.state && 'shadow_enabled' in row.state) save(row, { expected_revision: row.state.revision, shadow_enabled: checked })
}
function freeze(event: Event) {
  const checked = (event.target as HTMLInputElement).checked
  ;(event.target as HTMLInputElement).checked = queue.value?.freeze ?? false
  if (queue.value && !queueEdit.value) save(queueRow, { ...queue.value, freeze: checked })
}
const queueEdit = ref<{ project: string; snapshot: QueueSettings; release: string; slugs: string; pulls: string } | null>(null), queueError = ref('')
function editQueue() {
  if (!queue.value || queueRow.busy || !writable(queueRow)) return
  queueError.value = ''; queueEdit.value = { project: props.project.id, snapshot: { ...queue.value, release_set: [...queue.value.release_set], held_slugs: [...queue.value.held_slugs], held_pull_requests: [...queue.value.held_pull_requests] }, release: queue.value.release_set.join('\n'), slugs: queue.value.held_slugs.join('\n'), pulls: queue.value.held_pull_requests.join(', ') }
}
function saveQueue() {
  const edit = queueEdit.value
  if (!edit || edit.project !== props.project.id || queueRow.busy || !writable(queueRow)) return
  try { save(queueRow, { ...edit.snapshot, release_set: queueSlugs(edit.release), held_slugs: queueSlugs(edit.slugs), held_pull_requests: pullRequestNumbers(edit.pulls, true) }); queueError.value = '' }
  catch (error) { queueError.value = message(error) }
}
const mergeOpen = ref(false), pulls = ref(''), releaseId = ref(''), releases = ref<PlanningRelease[]>([]), releaseError = ref(''), releaseTruncated = ref(false)
const mergeBusy = ref(false), mergeError = ref(''), mergeResult = ref<MergeBackfillResult | null>(null)
const mergePreview = ref<{ project: string; input: MergeBackfillInput; result: MergeBackfillResult } | null>(null)
const mergeLane = scope.lane(), releaseLane = scope.lane()
const readyCount = computed(() => mergePreview.value?.result.items.filter(item => item.result === 'ready').length ?? 0)
const unmatchedPulls = computed(() => {
  const result = mergeResult.value
  if (!result) return []
  try { return pullRequestNumbers(pulls.value).filter(number => !result.not_merged.includes(number) && !result.items.some(item => item.pull_request === number)) } catch { return [] }
})
function loadReleases() {
  if (!can('releases.read', props.project.id)) { releaseError.value = 'See releases permission is needed to choose the published release.'; return }
  const project = props.project.id
  releaseError.value = ''
  void releaseLane.run(({ after }) => after(listPlanningReleases(project), page => {
    if (project !== props.project.id) return
    releases.value = page.releases.filter(release => release.state === 'released' || release.state === 'superseded')
    releaseTruncated.value = page.truncated
    if (!releases.value.length) releaseError.value = 'No published release is available. Tickets need published release membership to become Delivered.'
  }), { failed: error => { releaseError.value = message(error) } })
}
function showMerge() { if (!mergeAllowed.value) return; mergeOpen.value = true; loadReleases() }
function resetPreview() { mergeLane.cancel(); mergePreview.value = null; mergeResult.value = null; mergeError.value = ''; mergeBusy.value = false }
function closeMerge() { resetPreview(); mergeOpen.value = false; pulls.value = ''; releaseId.value = '' }
function repair(apply: boolean) {
  if (!mergeAllowed.value || mergeBusy.value) return
  const project = props.project.id
  let input: MergeBackfillInput
  try {
    if (apply) {
      if (!mergePreview.value || mergePreview.value.project !== project || !readyCount.value) return
      input = { ...mergePreview.value.input, apply: true, preview_event_id: mergePreview.value.result.preview_event_id }
    } else {
      if (!releases.value.some(release => release.id === releaseId.value)) throw new Error('Choose the published release first.')
      input = { pull_requests: pullRequestNumbers(pulls.value), target: 'delivered', release_id: releaseId.value }
    }
  } catch (error) { mergeError.value = message(error); return }
  mergeBusy.value = true; mergeError.value = ''
  void mergeLane.run(({ after, signal }) => after(advancedRequest<MergeBackfillResult>(project, 'delivery/merge-backfill', signal, input, 'POST'), result => {
    if (project !== props.project.id || !mergeAllowed.value) return
    if (result.applied !== apply || !Number.isSafeInteger(result.preview_event_id) || result.preview_event_id < 1 || !Array.isArray(result.items) || !Array.isArray(result.not_merged) || result.items.length > 100 || result.not_merged.length > 100 || result.items.some(item => item.project_id !== project || item.to !== 'delivered' || !input.pull_requests.includes(item.pull_request) || !['ready', 'refused', 'already_complete', 'already_applied', 'applied'].includes(item.result))) throw new Error('The repair result did not match this project and request. Read a new preview.')
    mergeResult.value = result
    if (!apply) mergePreview.value = { project, input, result }
    else {
      mergePreview.value = null
      const moved = result.items.filter(item => item.result === 'applied').length, refused = result.items.filter(item => item.result === 'refused').length
      toast(`${moved} ticket${moved === 1 ? '' : 's'} marked Delivered.${refused ? ` ${refused} refused; read the results.` : ''}`, { tone: refused ? 'error' : 'info' })
    }
  }), { failed: error => { mergeError.value = message(error); mergePreview.value = null }, settled: () => { mergeBusy.value = false } })
}
watch(() => [scope.owner.value, props.project.id], () => {
  scope.reset(); queueEdit.value = null; closeMerge(); releases.value = []; releaseError.value = ''; releaseTruncated.value = false
  for (const row of rows) { row.state = null; row.busy = false; row.error = ''; row.loading = false }
  if (open.value) for (const row of rows) load(row)
}, { immediate: true, flush: 'sync' })
watch(() => rows.map(row => can(row.read, props.project.id)), () => { if (open.value) for (const row of rows) { if (can(row.read, props.project.id)) load(row); else { reads.get(row.id)!.cancel(); row.state = null } } })
watch(mergeAllowed, allowed => { if (!allowed) closeMerge() })
watch(() => writable(queueRow), allowed => { if (!allowed) queueEdit.value = null })
watch(() => route.hash, hash => { if (hash === '#advanced') open.value = true })
watch(open, value => { if (value) for (const row of rows) load(row) })
onBeforeUnmount(() => scope.reset())
</script>

<template>
  <section v-if="admin" id="advanced" class="glass-card adv" aria-labelledby="adv-sum">
    <button id="adv-sum" type="button" class="adv-sum" :aria-expanded="open" aria-controls="adv-fold" @click="open = !open"><AppIcon name="chevron-right" :size="13" :class="{ rotated: open }" /><span class="adv-ic"><AppIcon name="shield" :size="14" /></span><b>Advanced</b><span v-clip-tip="`The coordinator’s machinery · ${shadowSummary} · admins only`" class="pv">The coordinator’s machinery · {{ shadowSummary }} · admins only</span></button>
    <div v-show="open" id="adv-fold" class="adv-in" role="region" aria-labelledby="adv-sum">
      <p class="rule warn"><AppIcon name="alert" :size="15" /><span><b>The project’s lead normally sets these.</b> Change one only to stop or test something. Every change is recorded, and the lead sees it. Shadow never starts, merges or ships anything; it records what the machine would have done. <ProjectSettingsExplainer id="shadow" :project-key="project.routeKey" /></span></p>
      <p class="eh">Shadow machines</p>
      <div v-for="row in rows" :key="row.id" class="arow" :data-machine="row.id">
        <AppIcon :name="row.icon" :size="15" /><div class="lbl"><b>{{ row.name }}</b><small>{{ row.description }} <ProjectSettingsExplainer :id="row.id" :project-key="project.routeKey" /></small>
          <div v-if="row.id === 'queue'" class="asub" :class="{ concealed: mode(row) !== 'shadow' }" :inert="mode(row) !== 'shadow'">
            <label class="switch sm"><input id="adv-freeze" type="checkbox" role="switch" :checked="queue?.freeze ?? false" :disabled="!queue || !writable(row) || row.busy || !!queueEdit" @change="freeze"><span>Freeze the queue</span></label>
            <span class="asub-t">{{ queue?.release_set.length ?? '—' }} in the release set · {{ queue ? queue.held_slugs.length + queue.held_pull_requests.length : '—' }} held · <button type="button" class="link-btn quiet" :disabled="!queue || !writable(row) || row.busy" @click="editQueue">Edit…</button></span>
          </div>
          <div :id="`machine-${row.id}-feedback`" class="sr-only" aria-live="polite"><span v-if="row.error" role="alert">{{ row.error }}</span><span v-else-if="row.loading">Reading settings…</span><span v-else-if="!can(row.read, project.id)">Requires {{ row.read }} to read this setting.</span><span v-else-if="!writable(row)">{{ row.id === 'admission' && session.identity?.principal.kind !== 'person' ? 'Only a person may change admission.' : `Requires ${row.write} to change this setting.` }}</span></div>
        </div>
        <div class="actl">
          <label v-if="row.id === 'admission'" class="switch"><input id="adv-adm" type="checkbox" role="switch" aria-label="Record shadow admission" :aria-describedby="`machine-${row.id}-feedback`" :checked="mode(row) === 'shadow'" :disabled="!row.state || !writable(row) || row.busy" @change="admission($event, row)"></label>
          <span v-else class="seg" role="group" :aria-label="row.name" :aria-describedby="`machine-${row.id}-feedback`"><button v-for="value in (['off', 'shadow'] as const)" :key="value" type="button" :data-adv="`${row.id}:${value}`" :aria-pressed="mode(row) === value" :disabled="!row.state || !writable(row) || row.busy || row.id === 'queue' && !!queueEdit" @click="setMode(row, value)">{{ value === 'off' ? 'Off' : 'Shadow' }}</button></span>
        </div>
      </div>
      <p class="eh">Chat</p>
      <div class="arow"><AppIcon name="send" :size="15" /><div class="lbl"><b>Chat roles</b><small>Chat roles use the workspace’s chat-v1 setup. Each person binds only their own role. <ProjectSettingsExplainer id="chat" :project-key="project.routeKey" /></small></div><RouterLink class="link-btn quiet actl" to="/settings/workspace">Features <AppIcon name="chevron-right" :size="12" /></RouterLink></div>
      <p class="eh">Maintenance</p>
      <div class="arow maintenance"><AppIcon name="merge" :size="15" /><div class="lbl"><b>Mark merged pull requests delivered</b><small>For merges PAIMOS missed: paste pull request numbers, check the preview, then apply. <ProjectSettingsExplainer id="mbf" :project-key="project.routeKey" /></small>
        <form v-if="mergeOpen" class="mbf" @submit.prevent="repair(false)">
          <label class="sr-only" for="mbf-in">Pull request numbers</label><textarea id="mbf-in" v-model="pulls" class="field ta" rows="2" maxlength="2400" :disabled="mergeBusy" @input="resetPreview" />
          <label class="release-label">Published release<select v-model="releaseId" class="field" :disabled="mergeBusy || !releases.length" @change="resetPreview"><option value="">Choose a published release</option><option v-for="release in releases" :key="release.id" :value="release.id">{{ release.title }}</option></select></label>
          <div class="mbf-acts"><button type="button" class="btn sm" @click="closeMerge">Cancel</button><button type="submit" class="btn sm" :disabled="mergeBusy || !releaseId">Preview</button><button type="button" class="btn sm primary" :disabled="mergeBusy || !readyCount" :aria-busy="mergeBusy" @click="repair(true)">Apply preview</button></div>
          <p v-if="releaseError" class="error" role="status">{{ releaseError }}</p><p v-if="releaseTruncated" role="status">The release list is partial; only the returned published releases can be chosen.</p>
          <p v-if="mergeError" class="error" role="alert">{{ mergeError }}</p>
          <div v-if="mergeResult" class="mbf-prev" aria-live="polite"><b>{{ mergeResult.applied ? 'Result:' : 'Preview:' }}</b><p v-if="!mergeResult.applied">Nothing changes until you apply. {{ readyCount }} ticket{{ readyCount === 1 ? '' : 's' }} ready.</p><ul><li v-for="item in mergeResult.items" :key="`${item.ticket_node_id}/${item.pull_request}`">{{ item.key }} · #{{ item.pull_request }} · {{ item.result === 'ready' ? 'will become Delivered' : item.result === 'applied' ? 'marked Delivered' : item.result === 'already_complete' || item.result === 'already_applied' ? 'already complete' : 'refused' }}{{ item.reason ? `: ${item.reason}` : '' }}</li><li v-for="number in mergeResult.not_merged" :key="number">#{{ number }} · not merged</li><li v-for="number in unmatchedPulls" :key="`unmatched-${number}`">#{{ number }} · no matching ticket</li></ul></div>
        </form>
        <small v-else-if="!mergeAllowed">Requires Manage delivery and Edit nodes in this project.</small>
      </div><button type="button" class="btn sm actl" :disabled="!mergeAllowed" :aria-expanded="mergeOpen" @click="showMerge">Preview…</button></div>
    </div>
    <ProjectSettingsSheet v-if="queueEdit" title="Delivery queue" description="Release-set branch prefixes and exact holds. This changes Shadow records only." @close="queueEdit = null" @submit="saveQueue">
      <form id="queue-settings-form" class="queue-form" @submit.prevent="saveQueue"><label>Release set<textarea v-model="queueEdit.release" class="field ta" rows="3" maxlength="12100" :disabled="queueRow.busy" autofocus /></label><label>Held branch slugs<textarea v-model="queueEdit.slugs" class="field ta" rows="3" maxlength="12100" :disabled="queueRow.busy" /></label><label>Held pull request numbers<textarea v-model="queueEdit.pulls" class="field ta" rows="2" maxlength="2400" :disabled="queueRow.busy" /></label><p>Up to 100 per list. Separate entries with commas or new lines. Unfreezing does not requeue parked rounds.</p><p v-if="queueError || queueRow.error" class="error" role="alert">{{ queueError || queueRow.error }}</p></form>
      <template #actions><button type="button" class="btn" @click="queueEdit = null">Cancel <KeyCap k="Esc" /></button><button type="submit" form="queue-settings-form" class="btn primary" :disabled="queueRow.busy" :aria-busy="queueRow.busy">Save queue <KeyCap k="mod" /><KeyCap k="enter" /></button></template>
    </ProjectSettingsSheet>
  </section>
</template>

<style scoped>
.adv { padding: 0; }.adv-sum { display: grid; grid-template-columns: 14px 28px auto minmax(0, 1fr); align-items: center; column-gap: 10px; width: 100%; min-height: 64px; padding: 10px 22px 10px 20px; border: 0; border-radius: var(--radius); background: transparent; color: var(--ink); text-align: left; }.adv-sum:hover { background: var(--row-hover); }.adv-sum b { font-size: 15px; font-weight: 650; }.adv-ic { display: grid; place-items: center; width: 28px; height: 28px; border-radius: 8px; background: var(--surface-sunken); box-shadow: inset 0 0 0 1px var(--line); color: var(--ink-2); }.adv-sum .pv { min-width: 0; font-size: 12.5px; color: var(--ink-3); overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }.rotated { transform: rotate(90deg); }.adv-in { padding: 2px 24px 22px; }
.rule { display: flex; align-items: flex-start; gap: 10px; padding: 11px 14px; margin-bottom: 8px; border-radius: 12px; background: var(--queue-wait-bg); box-shadow: inset 0 0 0 1px var(--queue-wait-line); font-size: 13px; line-height: 1.55; color: var(--ink-2); }.rule > svg { flex: none; margin-top: 3px; color: var(--queue-wait-ink); }.rule b { color: var(--ink); font-weight: 600; }.eh { margin-top: 16px; padding-bottom: 4px; border-bottom: 1px solid var(--line); font: 500 10.5px/1.6 var(--mono); letter-spacing: .12em; text-transform: uppercase; color: var(--ink-3); }
.arow { display: grid; grid-template-columns: 28px minmax(0, 1fr) auto; align-items: center; column-gap: 12px; min-height: 64px; padding: 10px 2px; border-bottom: 1px solid var(--line); }.arow:last-child { border-bottom: 0; }.arow > svg { justify-self: center; align-self: start; margin-top: 3px; color: var(--ink-3); }.lbl { display: grid; gap: 2px; min-width: 0; }.lbl b { font-size: 13.5px; font-weight: 600; }.lbl small { font-size: 12.5px; line-height: 1.5; color: var(--ink-3); }.actl { justify-self: end; align-self: start; }.seg { display: inline-flex; gap: 2px; padding: 3px; border-radius: 999px; background: var(--seg-bg); box-shadow: inset 0 1px 2px color-mix(in srgb, var(--ink) 8%, transparent); }.seg button { display: inline-flex; align-items: center; justify-content: center; min-height: 26px; min-width: 28px; padding: 0 11px; border: 0; border-radius: 999px; background: transparent; color: var(--ink-2); font: 600 12.5px var(--font); white-space: nowrap; }.seg button:hover { color: var(--teal-ink); background: var(--row-hover); }.seg button[aria-pressed="true"] { background: var(--seg-on); color: var(--teal-ink); box-shadow: var(--shadow-btn); }
.switch { display: inline-flex; align-items: center; justify-content: center; gap: 9px; min-width: 44px; min-height: 44px; cursor: pointer; font-size: 13px; color: var(--ink-2); white-space: nowrap; }.switch input { appearance: none; position: relative; flex: none; width: 34px; height: 20px; margin: 0; border-radius: 999px; background: var(--track); box-shadow: inset 0 0 0 1px var(--line-2); cursor: pointer; }.switch input::after { content: ''; position: absolute; top: 2px; left: 2px; width: 16px; height: 16px; border-radius: 50%; background: var(--surface-raised); box-shadow: var(--shadow-btn); }.switch input:checked { background: var(--teal); }.switch input:checked::after { transform: translateX(14px); }.switch input:disabled { cursor: default; opacity: .55; }
.asub { display: flex; flex-wrap: wrap; align-items: center; gap: 4px 16px; margin-top: 6px; }.asub.concealed { visibility: hidden; }.asub .switch { min-height: 32px; font-size: 12.5px; }.asub-t { font-size: 12.5px; color: var(--ink-2); }.link-btn { display: inline-flex; align-items: center; justify-content: center; gap: 2px; border: 0; padding: 0; background: transparent; color: var(--ink-2); font: 500 12.5px var(--font); text-decoration: underline; text-underline-offset: 2px; }.error { color: var(--danger); overflow-wrap: anywhere; }
.mbf { display: grid; gap: 8px; margin-top: 10px; min-width: 0; }.ta { width: 100%; min-width: 0; resize: none; font: 500 12.5px/1.5 var(--mono); }.mbf .ta { min-height: 60px; }.mbf-prev { padding: 8px 10px; border-radius: 10px; background: var(--surface-sunken); font-size: 12.5px; line-height: 1.5; color: var(--ink-2); overflow-wrap: anywhere; }.mbf-prev b { color: var(--ink); }.mbf-prev ul { padding-left: 18px; }.mbf-acts { display: flex; flex-wrap: wrap; gap: 8px; }.release-label { display: grid; gap: 6px; font-size: 12.5px; }.release-label .field { width: 100%; min-width: 0; }.queue-form { display: grid; gap: 18px; padding-top: 8px; font-size: 13px; }.queue-form label { display: grid; gap: 6px; }.queue-form p { font-size: 12.5px; line-height: 1.5; color: var(--ink-2); }
@container project-settings-body (max-width: 600px) { .arow { grid-template-columns: 24px minmax(0, 1fr); row-gap: 10px; }.arow > .actl { grid-column: 2; grid-row: 1; justify-self: start; }.arow > .lbl { grid-column: 2; grid-row: 2; }.arow > svg { grid-row: 1; }.adv-sum { grid-template-columns: 14px 28px minmax(0, 1fr); row-gap: 2px; padding: 12px 14px; }.adv-sum .pv { grid-column: 3; white-space: normal; }.adv-in { padding: 2px 14px 16px; } }
@media (pointer: coarse), (max-width: 600px) { .btn, .seg button, .link-btn, .field { min-height: 44px; }.seg button, .link-btn { min-width: 44px; }.asub .switch { min-height: 44px; } }
</style>
