<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { getProjects } from '../../lib/api'
import { getAutomaticChanges, getAutopilotSuggestions, getProjectAutopilot, getStatusAutopilot, saveProjectAutopilot, saveStatusAutopilot, type AutopilotSettings, type AutomaticChange, type ProjectOverride, type RuleKey } from '../../lib/statusAutopilot'
import AppIcon, { type IconName } from '../AppIcon.vue'
import StatusIcon from '../work/StatusIcon.vue'
import AutomaticChangeRow from '../work/AutomaticChangeRow.vue'
import TicketLink from '../releases/TicketLink.vue'
import SettingsCard from './SettingsCard.vue'
const settings = ref<AutopilotSettings | null>(null)
const projects = ref<{ id: string; key: string; title: string; override: ProjectOverride }[]>([])
const changes = ref<AutomaticChange[]>([])
const suggestions = ref<AutomaticChange[]>([])
const lists = [{ flag: 'triage_list', label: 'Triage list' }, { flag: 'cancel_suggested', label: 'Cancel suggested' }, { flag: 'blocked_reminder', label: 'Blocked reminders' }, { flag: 'missed_release', label: 'Missed releases' }]
const error = ref('')
const pending = ref(false)
const errors = ref<Partial<Record<RuleKey, string>>>({})
const draftDays = ref<Partial<Record<RuleKey, string>>>({})
const rules: { key: RuleKey; from: string; fromLabel: string; to: string; toLabel: string; glyph?: IconName; label: string; aria?: string }[] = [
  { key: 'new', from: 'new', fromLabel: 'New', to: 'triage_list', toLabel: 'Triage list', glyph: 'list', label: 'Listed for triage when untriaged for', aria: 'Days a ticket stays New before it is listed for triage' },
  { key: 'backlog', from: 'backlog', fromLabel: 'Backlog', to: 'cancelled', toLabel: 'Cancel suggested', label: 'Cancel suggested when untouched for', aria: 'Days untouched in Backlog before Cancelled is suggested' },
  { key: 'blocked', from: 'blocked', fromLabel: 'Blocked', to: 'reminder', toLabel: 'Reminder', glyph: 'clock', label: 'Reminder when blocked for', aria: 'Days blocked before a reminder' },
  { key: 'progress', from: 'in_progress', fromLabel: 'In progress', to: 'open', toLabel: 'Open', label: 'Back to Open, with a comment, when no session, branch or PR for', aria: 'Days without a session, branch or PR before In progress goes back to Open' },
  { key: 'done', from: 'done', fromLabel: 'Done', to: 'missed_release', toLabel: 'Missed release', glyph: 'alert', label: 'Flagged when merged but not released for', aria: 'Days merged but not released before the missed release flag' },
  { key: 'publish', from: 'done', fromLabel: 'Done', to: 'delivered', toLabel: 'Delivered', label: 'When a release with the ticket is published' },
  { key: 'accept', from: 'delivered', fromLabel: 'Delivered', to: 'accepted', toLabel: 'Accepted', label: 'Accepted when delivered without objection for', aria: 'Days delivered without objection before Accepted' },
]
async function load() {
  error.value = ''
  try {
    const s = await getStatusAutopilot(); settings.value = s
    errors.value = {}; draftDays.value = {}
    for (const rule of rules) if (s.rules[rule.key].days !== undefined) draftDays.value[rule.key] = String(s.rules[rule.key].days)
    const p = await getProjects()
    const visible = []
    for (const project of p.items) visible.push({ id: project.id, key: project.key, title: project.title, override: await getProjectAutopilot(project.id) })
    projects.value = visible
    await recent()
  } catch (e) { error.value = e instanceof Error ? e.message : 'Autopilot could not be loaded.' }
}
async function recent() {
  changes.value = (await getAutomaticChanges()).items
  suggestions.value = (await getAutopilotSuggestions()).items
}
onMounted(load)
async function save(next: AutopilotSettings) {
  pending.value = true; error.value = ''
  try { settings.value = await saveStatusAutopilot(next) }
  catch (e) { error.value = e instanceof Error ? e.message : 'Changes could not be saved.' }
  finally { pending.value = false }
}
function toggleMaster(event: Event) { if (settings.value) void save({ ...settings.value, enabled: (event.target as HTMLInputElement).checked }) }
function toggleRule(key: RuleKey, event: Event) {
  if (!settings.value) return
  void save({ ...settings.value, rules: { ...settings.value.rules, [key]: { ...settings.value.rules[key], enabled: (event.target as HTMLInputElement).checked } } })
}
function days(key: RuleKey, event: Event, persist: boolean) {
  if (!settings.value) return
  const input = event.target as HTMLInputElement, n = Number(input.value)
  draftDays.value[key] = input.value
  const valid = input.value !== '' && Number.isInteger(n) && n >= 1 && n <= 365
  errors.value[key] = valid ? '' : '1 to 365 days'
  if (valid && persist) void save({ ...settings.value, rules: { ...settings.value.rules, [key]: { ...settings.value.rules[key], days: n } } })
}
const modes = ['inherit', 'on', 'off'] as const
async function mode(project: (typeof projects.value)[number], value: ProjectOverride['mode']) {
  pending.value = true; error.value = ''
  try { project.override = await saveProjectAutopilot(project.id, value, project.override.revision) }
  catch (e) { error.value = e instanceof Error ? e.message : 'Project setting could not be saved.' }
  finally { pending.value = false }
}
function modeKey(event: KeyboardEvent, project: (typeof projects.value)[number]) {
  if (!['ArrowLeft', 'ArrowRight'].includes(event.key)) return
  event.preventDefault()
  const i = modes.indexOf(project.override.mode), next = modes[(i + (event.key === 'ArrowRight' ? 1 : -1) + modes.length) % modes.length]!
  void mode(project, next)
  const buttons = (event.currentTarget as HTMLElement).querySelectorAll<HTMLButtonElement>('button')
  buttons[modes.indexOf(next)]?.focus()
}
</script>
<template>
  <div class="autopilot" aria-label="Autopilot">
    <p class="eyebrow">Autopilot</p>
    <p v-if="error" class="set-note error" role="alert">{{ error }}<button class="btn sm" type="button" :disabled="pending" @click="load">Reload</button></p>
    <template v-if="settings">
      <SettingsCard title="Status autopilot" icon="sparkle" anchor="status-autopilot">
        <template #lead>Moves tickets on their own: Delivered when a release ships, Accepted {{ settings.rules.accept.days }} days later, back to Open when work stalls. Every change is logged with its reason and can be undone.</template>
        <template #aside><label class="switch"><input type="checkbox" role="switch" aria-label="Status autopilot" :checked="settings.enabled" :disabled="pending" @change="toggleMaster" /><span>{{ settings.enabled ? 'On' : 'Off' }}</span></label></template>
        <div class="rules" :class="{ paused: !settings.enabled }">
          <div v-for="rule in rules" :key="rule.key" class="rule" :class="{ off: !settings.rules[rule.key].enabled }">
            <div class="rule-move"><StatusIcon :state="rule.from" />{{ rule.fromLabel }}<AppIcon name="arrow" :size="12" class="arrow" /><span class="to"><AppIcon v-if="rule.glyph" :name="rule.glyph" :size="14" class="glyph" /><StatusIcon v-else :state="rule.to" />{{ rule.toLabel }}</span></div>
            <div class="rule-cond"><span class="rule-text">{{ rule.label }}</span></div>
            <div class="rule-value"><span v-if="rule.key === 'publish'" class="limit event">On publish</span><template v-else><span class="value-in"><input :id="`days-${rule.key}`" class="days" type="number" min="1" max="365" step="1" inputmode="numeric" :value="draftDays[rule.key]" :aria-label="rule.aria" :aria-describedby="`err-${rule.key}`" :aria-invalid="!!errors[rule.key]" :disabled="pending || !settings.enabled || !settings.rules[rule.key].enabled" @input="days(rule.key, $event, false)" @change="days(rule.key, $event, true)" /><span class="unit">{{ settings.rules[rule.key].days === 1 ? 'day' : 'days' }}</span></span><span :id="`err-${rule.key}`" class="rule-error" :hidden="!errors[rule.key]">1 to 365 days</span></template></div>
            <label class="switch"><input type="checkbox" role="switch" :aria-label="`${rule.fromLabel} to ${rule.toLabel}`" :checked="settings.rules[rule.key].enabled" :disabled="pending || !settings.enabled" @change="toggleRule(rule.key, $event)" /></label>
          </div>
        </div>
        <p class="set-note"><svg v-if="settings.enabled" width="14" height="14" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><circle cx="6.2" cy="5.2" r="2.5" /><path d="M1.8 13.6c0-2.6 2-4.2 4.4-4.2 1 0 1.9.3 2.6.7" /><path d="m9.8 11.6 1.7 1.7 3-3.3" /></svg><AppIcon v-else name="info" :size="14" /><span>{{ settings.enabled ? 'Tickets with a human check are skipped by the moves to Delivered and Accepted, with a comment, until someone marks them checked.' : 'Off: nothing moves on its own. Suggestions are still listed.' }}</span></p>
      </SettingsCard>
      <SettingsCard title="Autopilot per project" icon="folders" anchor="autopilot-projects">
        <template #lead>A project follows the workspace unless it sets its own. Its own setting wins for its tickets.</template>
        <div class="projects"><div class="proj head" aria-hidden="true"><span>Project</span><span>Status autopilot</span></div>
          <div v-for="project in projects" :key="project.id" class="proj"><span class="proj-name"><span class="key-badge">{{ project.key }}</span><span>{{ project.title }}</span></span><div class="proj-ctl"><span class="ctl-cap">Status autopilot</span><div class="seg" role="radiogroup" :aria-label="`Status autopilot in ${project.title}`" @keydown="modeKey($event, project)"><button v-for="value in modes" :key="value" type="button" role="radio" :aria-checked="project.override.mode === value" :tabindex="project.override.mode === value ? 0 : -1" :disabled="pending" @click="mode(project, value)">{{ value === 'inherit' ? 'Inherit' : value === 'on' ? 'On' : 'Off' }}</button></div><span v-if="project.override.mode === 'inherit'" class="eff">Workspace: {{ settings.enabled ? 'On' : 'Off' }}</span></div></div>
        </div>
      </SettingsCard>
      <SettingsCard title="Tickets needing attention" icon="list" anchor="autopilot-suggestions">
        <template #lead>Current triage suggestions, blocked reminders and missed releases stay listed until resolved.</template>
        <section v-for="list in lists" :key="list.flag" class="attention-list" :aria-label="list.label">
          <h3>{{ list.label }}</h3>
          <ul class="auto-changes"><li v-for="change in suggestions.filter(item => item.to === list.flag)" :key="change.event_id" class="change"><span class="node auto" aria-hidden="true"><AppIcon name="sparkle" :size="12" /></span><div class="change-main"><p class="change-head"><TicketLink :ticket-key="change.key" /><span class="change-title">{{ change.title }}</span></p><AutomaticChangeRow :change="change" recent @undone="recent" /></div></li></ul>
          <p v-if="!suggestions.some(item => item.to === list.flag)" class="empty">No tickets listed.</p>
        </section>
      </SettingsCard>
      <SettingsCard title="Recent automatic changes" icon="history" anchor="autopilot-recent">
        <template #lead>The latest moves by Status autopilot, with their reasons. The full record stays in each ticket’s Activity.</template>
        <ul class="auto-changes"><li v-for="change in changes" :key="change.event_id" class="change"><span class="node auto" aria-hidden="true"><AppIcon name="sparkle" :size="12" /></span><div class="change-main"><p class="change-head"><TicketLink :ticket-key="change.key" /><span class="change-title">{{ change.title }}</span></p><AutomaticChangeRow :change="change" recent @undone="recent" /></div></li></ul>
        <p v-if="!changes.length" class="empty">No automatic changes yet.</p>
      </SettingsCard>
    </template>
  </div>
</template>
<style scoped>
.autopilot { display: grid; gap: 14px; }
.set-note { display: grid; grid-template-columns: auto minmax(0, 1fr); align-items: start; gap: 8px; margin-top: 12px; padding: 10px 12px; border-radius: 10px; background: var(--surface-2); font-size: 13px; line-height: 1.5; color: var(--ink-2); }
.set-note > svg { margin-top: 2.5px; color: var(--ink-3); }
.error { color: var(--danger); grid-template-columns: 1fr auto; }
.rules { display: grid; margin-top: 14px; }
.rule { display: grid; grid-template-columns: 236px minmax(0, 1fr) 132px 44px; align-items: center; column-gap: 20px; row-gap: 4px; min-height: 48px; padding: 8px 0; border-top: 1px solid var(--line); }
.rule > .switch { justify-self: end; }
.rule-value { display: grid; justify-items: end; gap: 2px; }
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
.rule-error { font-size: 12px; color: var(--danger); }
.rule.off .rule-move, .rule.off .rule-cond { color: var(--ink-3); }
.rules.paused .rule { opacity: .55; }
.projects { display: grid; }
.proj { display: grid; grid-template-columns: minmax(140px, 1fr) auto; align-items: center; gap: 8px 18px; min-height: 52px; padding: 8px 0; border-top: 1px solid var(--line); }
.proj.head { min-height: 0; padding: 0 0 6px; border-top: 0; font: 500 10.5px/1.4 var(--mono); letter-spacing: .12em; text-transform: uppercase; color: var(--ink-3); }
.proj-name { display: flex; align-items: center; gap: 8px; min-width: 0; font-size: 13.5px; font-weight: 600; color: var(--ink); }
.proj-name span:last-child { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.proj-ctl { display: grid; gap: 3px; justify-items: start; min-width: 0; }
.ctl-cap { display: none; font: 500 10px/1.4 var(--mono); letter-spacing: .12em; text-transform: uppercase; color: var(--ink-3); }
.eff { font-size: 11.5px; color: var(--ink-3); padding-left: 12px; }
.auto-changes { display: grid; margin: 0; padding: 0; list-style: none; }
.change { display: grid; grid-template-columns: 26px minmax(0, 1fr); align-items: start; gap: 4px 12px; padding: 10px 0; border-top: 1px solid var(--line); }
.change:first-child { border-top: 0; padding-top: 0; }
.change-main { display: grid; gap: 2px; min-width: 0; }
.change-head { display: flex; align-items: center; gap: 8px; min-width: 0; }
.change-title { min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; font-size: 13.5px; color: var(--ink); }
.auto { display: grid; place-items: center; width: 22px; height: 22px; margin-top: 1px; border-radius: 7px; background: var(--surface-sunken); box-shadow: inset 0 0 0 1px var(--line-2); color: var(--teal-ink); }
.attention-list + .attention-list { margin-top: 16px; }
.attention-list h3 { margin-bottom: 8px; font-size: 13px; font-weight: 600; }
.empty { color: var(--ink-3); font-size: 13px; }
@media (max-width: 760px) {
 .proj { grid-template-columns: minmax(0, 1fr); gap: 8px; padding: 12px 0; }
 .proj.head { display: none; }.ctl-cap { display: block; }
 .rule { grid-template-columns: minmax(0, 1fr) auto; }.rule-cond { grid-column: 1 / -1; grid-row: 2; }.rule-value { grid-column: 1 / -1; grid-row: 3; justify-items: start; }
}
</style>
