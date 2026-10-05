<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, onMounted, ref, toRaw, watch } from 'vue'
import { useRouter } from 'vue-router'
import { can } from '../../lib/authz'
import { ATTENTION_KINDS, listAttention, type AttentionPage } from '../../lib/attention'
import { useIdentityScope } from '../../lib/useIdentityScope'
import { toast } from '../../lib/toast'
import { getLatestAutomaticChanges, listAutopilotProjects, getStatusAutopilot, saveProjectAutopilot, saveStatusAutopilot, type AutopilotSettings, type AutomaticChange, type AutopilotProject, type RuleKey, type ProjectOverride } from '../../lib/statusAutopilot'
import AppIcon, { type IconName } from '../AppIcon.vue'
import StatusIcon from '../work/StatusIcon.vue'
import AutomaticChangeRow from '../work/AutomaticChangeRow.vue'
import TicketLink from '../releases/TicketLink.vue'
import SettingsCard from './SettingsCard.vue'
import NeedsYouList, { type NeedsYouItem } from './NeedsYouList.vue'
import ChoicePicker from './ChoicePicker.vue'
const router = useRouter(), scope = useIdentityScope(() => can('settings.manage'))
const reads = scope.lane(), pickerReads = scope.lane(), projectReads = scope.lane(), recentReads = scope.lane()
const settings = ref<AutopilotSettings | null>(null), projects = ref<AutopilotProject[]>([]), changes = ref<AutomaticChange[]>([])
const inherited = ref(0), projectNext = ref<string | null>(null), projectAfter = ref(''), attention = ref<AttentionPage | null>(null)
const intent = ref<{ master?: boolean; rule?: RuleKey; enabled?: boolean } | null>(null)
const error = ref(''), pending = ref(false), loading = ref(true), saved = ref(false)
const errors = ref<Partial<Record<RuleKey, string>>>({}), draftDays = ref<Partial<Record<RuleKey, string>>>({})
const addButton = ref<HTMLButtonElement | null>(null), pickerOpen = ref(false), pickerChoices = ref<AutopilotProject[]>([])
const pickerNext = ref<string | null>(null), pickerTerm = ref(''), pickerLoading = ref(false), pickerError = ref(''), fresh = ref('')
let freshTimer: ReturnType<typeof setTimeout>, savedTimer: ReturnType<typeof setTimeout>, searchTimer: ReturnType<typeof setTimeout>
const waitingNames: Record<string, [string, string, string]> = {
  proposed: ['proposed change waits for Apply', 'proposed changes wait for Apply', 'No proposed changes'],
  triage: ['ticket on the triage list', 'tickets on the triage list', 'Nothing on the triage list'],
  cancel: ['cancel suggestion', 'cancel suggestions', 'No cancel suggestions'],
  blocked: ['blocked reminder', 'blocked reminders', 'No blocked reminders'],
  missed: ['missed release', 'missed releases', 'No missed releases'],
}
const explanations: Record<string, string> = {
  proposed: 'Proposed moves leave tickets untouched until someone applies them.', triage: 'New tickets waiting to be triaged.',
  cancel: 'Untouched backlog tickets suggested for cancellation.', blocked: 'Tickets that have stayed blocked and need a reminder.',
  missed: 'Merged tickets that have not reached a release.',
}
const needs = computed<NeedsYouItem[]>(() => ATTENTION_KINDS.map(kind => {
  const count = attention.value?.counts[kind.id] ?? 0
  return { id: kind.id, name: count ? `${count.toLocaleString()} ${waitingNames[kind.id]![count === 1 ? 0 : 1]}` : waitingNames[kind.id]![2], count, detail: count ? explanations[kind.id]! : `${explanations[kind.id]} None right now.`, icon: kind.id === 'cancel' ? 'close' : kind.icon, action: { label: 'Open' } }
}))
const heading = computed(() => { const count = attention.value?.total ?? 0; return count ? `${count.toLocaleString()} ${count === 1 ? 'ticket needs' : 'tickets need'} a person` : 'Nothing needs a person' })
function openAttention(kind = '') { void router.push({ path: '/tickets', query: kind ? { kind } : {} }) }
function failed(e: unknown) { error.value = e instanceof Error ? e.message : 'Autopilot could not be loaded or saved.'; toast(error.value, { tone: 'error' }) }
function markSaved() { saved.value = true; clearTimeout(savedTimer); savedTimer = setTimeout(() => { saved.value = false }, 2000) }
function syncDays() { errors.value = {}; draftDays.value = {}; for (const rule of rules) if (settings.value?.rules[rule.key].days !== undefined) draftDays.value[rule.key] = String(settings.value.rules[rule.key].days) }
const rules: { key: RuleKey; from: string; fromLabel: string; to: string; toLabel: string; glyph?: IconName; label: string; aria?: string }[] = [
  { key: 'new', from: 'new', fromLabel: 'New', to: 'triage_list', toLabel: 'Triage list', glyph: 'list', label: 'Listed for triage when untriaged for', aria: 'Days a ticket stays New before it is listed for triage' },
  { key: 'backlog', from: 'backlog', fromLabel: 'Backlog', to: 'cancelled', toLabel: 'Cancel suggested', label: 'Cancel suggested when untouched for', aria: 'Days untouched in Backlog before Cancelled is suggested' },
  { key: 'blocked', from: 'blocked', fromLabel: 'Blocked', to: 'reminder', toLabel: 'Reminder', glyph: 'clock', label: 'Reminder when blocked for', aria: 'Days blocked before a reminder' },
  { key: 'progress', from: 'in_progress', fromLabel: 'In progress', to: 'open', toLabel: 'Open', label: 'Back to Open, with a comment, when no session, branch or PR for', aria: 'Days without a session, branch or PR before In progress goes back to Open' },
  { key: 'done', from: 'done', fromLabel: 'Done', to: 'missed_release', toLabel: 'Missed release', glyph: 'alert', label: 'Flagged when merged but not released for', aria: 'Days merged but not released before the missed release flag' },
  { key: 'publish', from: 'done', fromLabel: 'Done', to: 'delivered', toLabel: 'Delivered', label: 'When a release with the ticket is published' },
  { key: 'accept', from: 'delivered', fromLabel: 'Delivered', to: 'accepted', toLabel: 'Accepted', label: 'Accepted when delivered without objection for', aria: 'Days delivered without objection before Accepted' },
]

function load() {
  loading.value = true; error.value = ''
  void reads.run(({ after, signal }) => after(Promise.all([
    getStatusAutopilot(signal), listAutopilotProjects('overrides', signal), getLatestAutomaticChanges(signal),
    listAttention({ kind: '', project_id: '', assignee: '', q: '' }, signal),
  ]), ([s, p, c, a]) => {
    settings.value = s; syncDays(); projects.value = p.items; inherited.value = p.inherited_count
    projectNext.value = p.next_cursor; projectAfter.value = ''; changes.value = c.items.slice(0, 5); attention.value = a
  }), { failed, settled: () => { loading.value = false } })
}
function recent() { void recentReads.run(({ after, signal }) => after(getLatestAutomaticChanges(signal), c => { changes.value = c.items.slice(0, 5) }), { failed }) }
function projectPage(cursor = '') {
  void projectReads.run(({ after, signal }) => after(listAutopilotProjects('overrides', signal, '', cursor), p => {
    projects.value = p.items; projectNext.value = p.next_cursor; inherited.value = p.inherited_count; projectAfter.value = cursor
  }), { failed })
}
watch(scope.owner, owner => {
  closePicker(); intent.value = null; pending.value = false; saved.value = false; fresh.value = ''; error.value = ''
  clearTimeout(freshTimer); clearTimeout(savedTimer); syncDays()
  if (owner) {
    settings.value = null; projects.value = []; changes.value = []; attention.value = null
    inherited.value = 0; projectNext.value = null; projectAfter.value = ''; pickerChoices.value = []; pickerNext.value = null; pickerTerm.value = ''; pickerError.value = ''
    load()
  }
}, { flush: 'sync' })
onMounted(load)
onBeforeUnmount(() => { clearTimeout(freshTimer); clearTimeout(savedTimer); clearTimeout(searchTimer) })
function save(next: AutopilotSettings, confirmUpgrade = false, isUndo = false) {
  if (pending.value || loading.value || !settings.value || !scope.owner.value) return
  const before = structuredClone(toRaw(settings.value)), owner = scope.owner.value
  pending.value = true; error.value = ''
  void scope.run(({ after, signal }) => after(saveStatusAutopilot(next, confirmUpgrade, signal), result => {
    settings.value = result; markSaved()
    if (!confirmUpgrade && !isUndo) toast('Status autopilot saved.', { timeout: 10_000, action: { label: 'Undo', run: () => {
      if (scope.owner.value !== owner || !can('settings.manage')) return
      if (settings.value?.revision !== result.revision) { toast('Settings changed since. Reload before Undo.', { tone: 'error' }); return }
      save({ ...before, revision: result.revision }, false, true)
    } } })
    if (isUndo) syncDays()
  }), { failed, settled: () => { pending.value = false; intent.value = null } })
}
function toggleMaster(event: Event) { if (settings.value) { const enabled = (event.target as HTMLInputElement).checked; intent.value = { master: enabled }; save({ ...settings.value, enabled }) } }
function toggleRule(key: RuleKey, event: Event) { if (settings.value) { const enabled = (event.target as HTMLInputElement).checked; intent.value = { rule: key, enabled }; save({ ...settings.value, rules: { ...settings.value.rules, [key]: { ...settings.value.rules[key], enabled } } }) } }
function days(key: RuleKey, event: Event, persist: boolean) {
  if (!settings.value) return
  const input = event.target as HTMLInputElement, n = Number(input.value); draftDays.value[key] = input.value
  const valid = input.value !== '' && Number.isInteger(n) && n >= 1 && n <= 365; errors.value[key] = valid ? '' : '1 to 365 days'
  if (valid && persist && n !== settings.value.rules[key].days) save({ ...settings.value, rules: { ...settings.value.rules, [key]: { ...settings.value.rules[key], days: n } } })
}
const modes = ['on', 'off'] as const
function mode(project: AutopilotProject, value: ProjectOverride['mode'], isUndo = false) {
  if (pending.value || loading.value || !scope.owner.value) return
  projectReads.cancel()
  const snapshot = { ...project, override: { ...project.override } }, owner = scope.owner.value
  pending.value = true; error.value = ''
  void scope.run(({ after, signal }) => after(saveProjectAutopilot(snapshot.id, value, snapshot.override.revision, signal), result => {
    markSaved()
    if (value === 'inherit') { projectPage(); addButton.value?.focus({ preventScroll: true }) }
    else {
      const row = projects.value.find(p => p.id === snapshot.id)
      if (row) row.override = result
      else { fresh.value = snapshot.id; projects.value = [{ ...snapshot, override: result }, ...projects.value].slice(0, 51); inherited.value = Math.max(0, inherited.value - 1); clearTimeout(freshTimer); freshTimer = setTimeout(() => { fresh.value = '' }, 2000) }
    }
    toast(value === 'inherit' ? `${snapshot.title} follows the workspace again.` : `${snapshot.title} now sets its own: ${value === 'on' ? 'On' : 'Off'}.`, { timeout: 10_000, ...(!isUndo ? { action: { label: 'Undo', run: () => {
      if (scope.owner.value === owner && can('settings.manage')) mode({ ...snapshot, override: result }, snapshot.override.mode, true)
    } } } : {}) })
    pending.value = false
    return after(nextTick(), () => { if (value !== 'inherit') document.querySelector<HTMLButtonElement>(`[data-project="${snapshot.id}"] [aria-checked="true"]`)?.focus({ preventScroll: true }) })
  }), { failed, settled: () => { pending.value = false; intent.value = null } })
}
function modeKey(event: KeyboardEvent, project: AutopilotProject) { if (event.metaKey || event.ctrlKey || event.altKey || !['ArrowLeft', 'ArrowRight'].includes(event.key)) return; event.preventDefault(); mode(project, project.override.mode === 'on' ? 'off' : 'on') }
function pickerPage(cursor = '') {
  pickerLoading.value = true; pickerError.value = ''
  void pickerReads.run(({ after, signal }) => after(listAutopilotProjects('inherit', signal, pickerTerm.value, cursor), p => {
    pickerChoices.value = p.items; pickerNext.value = p.next_cursor
  }), { failed: e => { pickerError.value = 'Couldn’t load projects. Search again to retry.'; failed(e) }, settled: () => { pickerLoading.value = false } })
}
function searchProjects(term: string) { pickerTerm.value = term.slice(0, 200); pickerLoading.value = true; pickerReads.cancel(); pickerChoices.value = []; clearTimeout(searchTimer); searchTimer = setTimeout(() => pickerPage(), 150) }
function openPicker() { pickerChoices.value = []; pickerTerm.value = ''; pickerOpen.value = true; pickerPage() }
function closePicker(restore = false) { pickerOpen.value = false; pickerReads.cancel(); clearTimeout(searchTimer); if (restore) addButton.value?.focus({ preventScroll: true }) }
function chooseProject(id: string) { const project = pickerChoices.value.find(p => p.id === id); if (!project || !settings.value) return; closePicker(); mode(project, settings.value.enabled ? 'off' : 'on') }
</script>
<template>
  <div class="autopilot" aria-label="Autopilot">
    <div class="load-feedback"><span role="status">{{ loading ? 'Loading autopilot…' : saved ? 'Saved' : error ? 'Couldn’t load or save. Try again.' : '' }}</span><button class="btn sm" type="button" :disabled="pending || loading || !error" @click="load">Reload</button></div>
    <template v-if="settings">
      <NeedsYouList id="autopilot-suggestions" :items="needs" :heading="heading" show-calm @action="openAttention">
        <template #aside><span>Worked in Tickets, one line each</span><button type="button" class="btn sm" :disabled="!attention?.total" @click="openAttention()">Open all<AppIcon name="arrow" :size="13" /></button></template>
      </NeedsYouList>
      <SettingsCard title="Status autopilot" icon="sparkle" anchor="status-autopilot">
        <template #lead>Moves tickets on their own: Delivered when a release ships, Accepted {{ settings.rules.accept.days }} days later, back to Open when work stalls. Every change is logged with its reason and can be undone.</template>
        <template #aside><label class="switch"><input type="checkbox" role="switch" aria-label="Status autopilot" :checked="intent?.master ?? settings.enabled" :disabled="pending || loading || !scope.owner.value" @change="toggleMaster" /><span class="master-state"><span v-for="word in ['On', 'Off', 'Suggest']" :key="word" :style="{ visibility: word === (settings.effective_mode === 'suggest' ? 'Suggest' : settings.effective_mode === 'off' ? 'Off' : settings.enabled ? 'On' : 'Off') ? 'visible' : 'hidden' }">{{ word }}</span></span></label></template>
        <p v-if="settings.suggest_until && settings.effective_mode === 'suggest'" class="set-note"><AppIcon name="info" :size="14" /><span>After this upgrade, proposed changes wait for review until {{ new Date(settings.suggest_until).toLocaleString() }}. Review them in Tickets › Needs attention, or enable automatic changes now.<button v-if="can('ownership.transfer')" type="button" class="btn sm" :disabled="pending || loading || !scope.owner.value || settings.server_mode !== 'on'" @click="save(settings, true)">Enable automatic changes</button></span></p>
        <p v-if="settings.server_mode && settings.server_mode !== 'on'" class="set-note"><AppIcon name="info" :size="14" /><span>{{ settings.server_mode === 'off' ? 'The server operator has paused status autopilot. Workspace and project settings cannot enable it.' : 'The server operator requires Suggest mode. Proposed changes wait for Apply or Dismiss.' }}</span></p>
        <div class="rules" :class="{ paused: !settings.enabled }">
          <div v-for="rule in rules" :key="rule.key" class="rule" :class="{ off: !settings.rules[rule.key].enabled }">
            <div class="rule-move"><StatusIcon :state="rule.from" />{{ rule.fromLabel }}<AppIcon name="arrow" :size="12" class="arrow" /><span class="to"><AppIcon v-if="rule.glyph" :name="rule.glyph" :size="14" class="glyph" /><StatusIcon v-else :state="rule.to" />{{ rule.toLabel }}</span></div>
            <div class="rule-cond"><span class="rule-text">{{ rule.label }}</span></div>
            <div class="rule-value"><span v-if="rule.key === 'publish'" class="limit event">On publish</span><template v-else><span class="value-in"><input :id="`days-${rule.key}`" class="days" type="number" min="1" max="365" step="1" inputmode="numeric" :value="draftDays[rule.key]" :aria-label="rule.aria" :aria-describedby="`err-${rule.key}`" :aria-invalid="!!errors[rule.key]" :disabled="pending || loading || !scope.owner.value || !settings.enabled || !settings.rules[rule.key].enabled" @input="days(rule.key, $event, false)" @blur="days(rule.key, $event, true)" @keydown.enter.prevent="($event.target as HTMLInputElement).blur()" /><span class="unit">{{ settings.rules[rule.key].days === 1 ? 'day' : 'days' }}</span></span><span :id="`err-${rule.key}`" class="rule-error" :hidden="!errors[rule.key]">1 to 365 days</span></template></div>
            <label class="switch"><input type="checkbox" role="switch" :aria-label="`${rule.fromLabel} to ${rule.toLabel}`" :checked="intent?.rule === rule.key ? intent.enabled : settings.rules[rule.key].enabled" :disabled="pending || loading || !scope.owner.value || !settings.enabled" @change="toggleRule(rule.key, $event)" /></label>
          </div>
        </div>
        <p class="set-note"><svg v-if="settings.enabled" width="14" height="14" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><circle cx="6.2" cy="5.2" r="2.5" /><path d="M1.8 13.6c0-2.6 2-4.2 4.4-4.2 1 0 1.9.3 2.6.7" /><path d="m9.8 11.6 1.7 1.7 3-3.3" /></svg><AppIcon v-else name="info" :size="14" /><span class="note-words"><span :style="{ visibility: settings.enabled ? 'visible' : 'hidden' }" :aria-hidden="!settings.enabled">Tickets with a human check are skipped by the moves to Delivered and Accepted, with a comment, until someone marks them checked.</span><span :style="{ visibility: settings.enabled ? 'hidden' : 'visible' }" :aria-hidden="settings.enabled">Off: nothing moves on its own. Suggestions are still listed.</span></span></p>
      </SettingsCard>

      <SettingsCard title="Project overrides" icon="folders" anchor="autopilot-projects">
        <template #lead>Projects follow the workspace unless they set their own; only those are listed. The upgrade review period and server mode apply to every project.</template>
        <template #aside><button ref="addButton" type="button" class="btn sm" :disabled="pending || loading || !scope.owner.value" aria-haspopup="dialog" :aria-expanded="pickerOpen" @click="openPicker"><AppIcon name="plus" :size="13" />Add an override</button></template>
        <div class="project-pages" v-if="projectNext || projectAfter"><button type="button" class="btn sm" :disabled="!projectAfter" @click="projectPage()">First overrides</button><button type="button" class="btn sm" :disabled="!projectNext" @click="projectPage(projectNext!)">Next overrides</button></div>
        <div class="projects">
          <div v-for="project in projects" :key="project.id" class="proj" :class="{ fresh: project.id === fresh }" :data-project="project.id">
            <span class="proj-name"><span class="key-badge">{{ project.key }}</span><span v-clip-tip="project.title">{{ project.title }}</span></span>
            <div class="seg" role="radiogroup" :aria-label="`Status autopilot in ${project.title}`" @keydown="modeKey($event, project)"><button v-for="value in modes" :key="value" type="button" role="radio" :aria-checked="project.override.mode === value" :tabindex="project.override.mode === value ? 0 : -1" :disabled="pending || loading || !scope.owner.value" @click="mode(project, value)">{{ value === 'on' ? 'On' : 'Off' }}</button></div>
            <button type="button" class="icon-btn sm flat" :aria-label="`Remove the override: ${project.title} follows the workspace again`" :disabled="pending || loading || !scope.owner.value" @click="mode(project, 'inherit')"><AppIcon name="close" :size="13" /></button>
          </div>
          <p v-if="!projects.length" class="empty">No project sets its own. Every project follows the workspace.</p>
        </div>
        <p class="ovr-foot">{{ inherited }} other {{ inherited === 1 ? 'project follows' : 'projects follow' }} the workspace ({{ settings.enabled ? 'On' : 'Off' }}).</p>
        <ChoicePicker v-if="pickerOpen" :anchor="addButton" label="Choose a project" placeholder="Search projects" settings-keys remote :loading="pickerLoading" :error="pickerError" :has-more="!!pickerNext" :choices="pickerChoices.map(p => ({ value: p.id, label: p.title, hint: p.key, detail: `Follows: ${settings!.enabled ? 'On' : 'Off'}` }))" current="" @search="searchProjects" @more="pickerPage(pickerNext!)" @choose="chooseProject" @close="closePicker" />
      </SettingsCard>
      <SettingsCard title="Latest automatic changes" icon="history" anchor="autopilot-recent">
        <template #lead>The five newest moves by Status autopilot, with their reasons. Each ticket’s Activity keeps its own record.</template>
        <template #aside><RouterLink class="btn sm" to="/activity?view=automatic">Show all<AppIcon name="arrow" :size="13" /></RouterLink></template>
        <ul class="auto-changes"><li v-for="change in changes" :key="change.event_id" class="change"><span class="node auto" aria-hidden="true"><AppIcon name="sparkle" :size="12" /></span><div class="change-main"><p class="change-head"><TicketLink :ticket-key="change.key" /><span v-clip-tip="change.title" class="change-title">{{ change.title }}</span></p><AutomaticChangeRow :change="change" recent @undone="recent" /></div></li></ul>
        <p v-if="!changes.length" class="empty">Nothing has moved on its own yet.</p>
      </SettingsCard>
    </template>
    <p v-else role="status">{{ error ? 'Couldn’t load Autopilot. Reload to try again.' : 'Loading settings…' }}</p>
  </div>
</template>
<style scoped>
.note-words { display: grid; }.note-words > span { grid-area: 1 / 1; }
.master-state { display: grid; }.master-state > span { grid-area: 1 / 1; }
.autopilot { display: grid; gap: 14px; }
.set-note { display: grid; grid-template-columns: auto minmax(0, 1fr); align-items: start; gap: 8px; margin-top: 12px; padding: 10px 12px; border-radius: 10px; background: var(--surface-2); font-size: 13px; line-height: 1.5; color: var(--ink-2); }
.set-note > svg { margin-top: 2.5px; color: var(--ink-3); }
.error { color: var(--danger); grid-template-columns: 1fr auto; }
.rules { display: grid; grid-template-columns: max-content minmax(0, 1fr) max-content max-content; column-gap: 20px; margin-top: 14px; }
.rule { display: grid; grid-template-columns: subgrid; grid-column: 1 / -1; align-items: center; column-gap: 20px; row-gap: 4px; min-height: 48px; padding: 8px 0; border-top: 1px solid var(--line); }
.rule > .switch { justify-self: end; }
.rule-value { position: relative; display: grid; justify-items: end; gap: 2px; }
.value-in { display: inline-grid; grid-template-columns: 64px 36px; align-items: center; gap: 8px; }
.unit { font-size: 13px; color: var(--ink-2); }
.limit.event { display: grid; place-items: center; width: 108px; height: 30px; border-radius: 8px; background: var(--code-bg); font: 500 11.5px/1 var(--mono); color: var(--teal-ink); }
.rule-text { min-width: 0; line-height: 1.4; }
.rule-value .rule-error { text-align: right; }
.rule:first-child { border-top: 1px solid var(--line); }
.rule-move { display: flex; flex-wrap: nowrap; align-items: center; gap: 6px; min-width: 0; font-size: 13px; font-weight: 600; color: var(--ink); white-space: nowrap; }
.rule-move .arrow, .rule-move .glyph { color: var(--ink-3); }
.rule-move .to { display: inline-flex; align-items: center; gap: 6px; white-space: nowrap; }
.rule-cond { display: flex; flex-wrap: wrap; align-items: center; gap: 4px 6px; min-width: 0; font-size: 13px; color: var(--ink-2); }
.days { width: 64px; height: 30px; padding: 0 8px; border: 1px solid var(--line-2); border-radius: 8px; background: var(--surface); color: var(--ink); font: 500 13px/1 var(--mono); font-variant-numeric: tabular-nums; text-align: right; }
.days:focus { box-shadow: var(--focus-ring); }
.days[aria-invalid="true"] { border-color: var(--danger-line); box-shadow: 0 0 0 1px var(--danger-line); }
.days:disabled { opacity: .5; cursor: not-allowed; }
.rule-error { position: absolute; top: 100%; font-size: 12px; color: var(--danger); }
.rule.off .rule-move, .rule.off .rule-cond { color: var(--ink-3); }
.rules.paused .rule { opacity: .55; }
.projects { display: grid; max-height: 360px; overflow-y: auto; }
.proj { display: grid; grid-template-columns: minmax(0, 1fr) auto auto; align-items: center; gap: 8px 18px; min-height: 52px; padding: 8px 0; border-top: 1px solid var(--line); }
.proj-name { display: flex; align-items: center; gap: 8px; min-width: 0; font-size: 13.5px; font-weight: 600; color: var(--ink); }
.proj-name span:last-child { min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.auto-changes { display: grid; margin: 0; padding: 0; list-style: none; }
.change { display: grid; grid-template-columns: 26px minmax(0, 1fr); align-items: start; gap: 4px 12px; padding: 10px 0; border-top: 1px solid var(--line); }
.change:first-child { border-top: 0; padding-top: 0; }
.change-main { display: grid; gap: 2px; min-width: 0; }
.change-head { display: flex; align-items: center; gap: 8px; min-width: 0; }
.change-title { min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; font-size: 13.5px; color: var(--ink); }
.auto { display: grid; place-items: center; width: 22px; height: 22px; margin-top: 1px; border-radius: 7px; background: var(--surface-sunken); box-shadow: inset 0 0 0 1px var(--line-2); color: var(--teal-ink); }
.empty { color: var(--ink-3); font-size: 13px; }
@container body (max-width: 760px) {
 .proj { grid-template-columns: minmax(0, 1fr) auto; gap: 8px; padding: 12px 0; }
  .rules { grid-template-columns: minmax(0, 1fr) auto; }.rule { grid-template-columns: subgrid; }.rule > .switch { grid-column: 2; grid-row: 1; }.proj-name { grid-column: 1 / -1; }.rule-cond { grid-column: 1 / -1; grid-row: 2; }.rule-value { grid-column: 1 / -1; grid-row: 3; justify-items: start; }
}
@media (max-width: 720px) {
  .proj-name span:last-child, .change-title { white-space: normal; overflow-wrap: anywhere; display: -webkit-box; -webkit-box-orient: vertical; -webkit-line-clamp: 2; }
  .proj-name, .change-head { align-items: flex-start; }
}
.load-feedback { display: flex; align-items: center; justify-content: space-between; gap: 8px; font-size: 12px; color: var(--ink-3); }
.load-feedback > span { flex: 1; }
.load-feedback .btn:disabled { visibility: hidden; }
.ovr-foot { margin-top: 12px; padding-top: 12px; border-top: 1px solid var(--line); font-size: 13px; color: var(--ink-2); }
.project-pages { display: flex; gap: 8px; margin-bottom: 12px; }
.fresh { background: var(--row-selected); }
@media (pointer: coarse), (max-width: 720px) { .btn, .seg button, .icon-btn, .days, .switch, :deep(.auto-body .btn) { min-height: 44px; }.icon-btn, .switch { min-width: 44px; } }
</style>
