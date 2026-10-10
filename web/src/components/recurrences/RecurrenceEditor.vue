<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, onMounted, reactive, ref, useId, watch } from 'vue'
import { createRecurrence, getNode, previewRecurrenceDraft, updateRecurrence, type ListItem } from '../../lib/api'
import { can } from '../../lib/authz'
import { useIdentityScope } from '../../lib/useIdentityScope'
import { useWorkVocabulary } from '../../stores/workVocabulary'
import { workNoun } from '../../lib/workVocabulary'
import { copyRecurrenceInput, parseCriteria, recurrenceEstimate, recurrenceName, recurrenceSourceIsParent, recurrenceZone, renderTitle, ruleParts, templateFrom, templateProblems, triggerWords, weekdays, when, zones, type Recurrence, type RecurrenceInput } from '../../lib/recurrences'
import AppIcon from '../AppIcon.vue'
import KeyCap from '../KeyCap.vue'
import EpicPicker from '../work/EpicPicker.vue'
import TemplateTitle from './TemplateTitle.vue'

const props = defineProps<{ project: { id: string; routeKey: string; title?: string }; source?: ListItem | null; recurrence?: Recurrence }>()
const emit = defineEmits<{ close: []; saved: [item: Recurrence] }>()
const vocabulary = useWorkVocabulary()
const uid = useId(), dialog = ref<HTMLDialogElement>(), body = ref<HTMLElement>(), nameField = ref<HTMLInputElement>()
const initial = props.recurrence, source = props.source, rule = ruleParts(initial?.trigger.rrule)
const template = reactive(initial ? { ...copyRecurrenceInput(initial).template, type: 'work' as const } : templateFrom(source))
if (initial && !template.name) template.name = recurrenceName(initial).slice(0, 80)
const parent = ref(initial?.parent_id ?? (recurrenceSourceIsParent(source) && source ? source.id : source?.parent_id || props.project.id))
const parentLabel = ref(recurrenceSourceIsParent(source) && source ? `${source.key} · ${source.title}` : parent.value === props.project.id ? 'No parent (top level of the project)' : 'Loading parent…')
const anchorDate = initial?.trigger.start_date ? new Date(`${initial.trigger.start_date}T00:00:00Z`) : null
const schedule = reactive({ kind: initial?.trigger.kind || 'time', frequency: rule.FREQ || 'WEEKLY', days: rule.BYDAY?.split(',') || [anchorDate ? ['SU', 'MO', 'TU', 'WE', 'TH', 'FR', 'SA'][anchorDate.getUTCDay()]! : 'MO'], interval: rule.INTERVAL || '1', day: rule.BYMONTHDAY || String(anchorDate?.getUTCDate() || 1), time: initial?.trigger.time_of_day || '09:00', zone: initial?.trigger.timezone || initial?.trigger.event_timezone || (initial?.trigger.kind === 'event' ? 'UTC' : Intl.DateTimeFormat().resolvedOptions().timeZone) || 'UTC', start: initial?.trigger.event_start || 'now' })
// Name/template edits must keep overdue work and pending publications. Preserve
// legacy RRULE spelling and implicit event defaults until the trigger is edited.
const initialSchedule = JSON.stringify(schedule)
const estimate = ref(template.estimate_hours ? `${template.estimate_hours} h` : ''), criteria = ref(template.acceptance_criteria.join('\n'))
const queue = ref(initial?.queue_each ?? false), skip = ref(initial?.overlap_policy !== 'create')
const picker = ref<HTMLElement | null>(null), busy = ref(false), failure = ref(''), previewError = ref(''), previewBusy = ref(false), times = ref<string[]>([])
const allowed = computed(() => can('recurrences.manage', props.project.id))
const scope = useIdentityScope(() => allowed.value), previews = scope.lane()
const mac = /Mac|iPhone|iPad/.test(navigator.platform || navigator.userAgent)
const zoneOptions = computed(() => [...new Set([schedule.zone, ...zones])])
const input = computed<RecurrenceInput>(() => ({ project_id: props.project.id, parent_id: parent.value, template: { ...template, name: template.name?.trim(), estimate_hours: recurrenceEstimate(estimate.value) || 0, acceptance_criteria: parseCriteria(criteria.value) }, trigger: initial && JSON.stringify(schedule) === initialSchedule ? { ...initial.trigger } : schedule.kind === 'event' ? { ...(initial?.trigger.kind === 'event' ? copyRecurrenceInput(initial).trigger : { kind: 'event' as const, event: 'release.published' as const }), event_start: schedule.start, event_timezone: schedule.zone } : { kind: 'time', rrule: `FREQ=${schedule.frequency}${schedule.frequency === 'WEEKLY' ? `;BYDAY=${weekdays.filter(([day]) => schedule.days.includes(day)).map(([day]) => day).join(',')}` : schedule.frequency === 'MONTHLY' ? `;INTERVAL=${schedule.interval};BYMONTHDAY=${schedule.day}` : ''}`, time_of_day: schedule.time, timezone: schedule.zone, ...(initial?.trigger.start_date ? { start_date: initial.trigger.start_date } : {}) }, queue_each: queue.value, overlap_policy: skip.value ? 'skip' : 'create', catch_up_policy: 'one' }))
const problems = computed(() => templateProblems(input.value, estimate.value))
let timer: ReturnType<typeof setTimeout> | undefined
watch(() => JSON.stringify(input.value), () => {
  clearTimeout(timer); previews.cancel(); times.value = []; previewError.value = ''; previewBusy.value = !problems.value.length
  if (problems.value.length) return
  const snapshot = copyRecurrenceInput(input.value)
  timer = setTimeout(() => void previews.run(({ after, signal }) => after(previewRecurrenceDraft(snapshot, signal), result => { times.value = result.times }), { failed: error => { previewError.value = error instanceof Error ? error.message : 'Preview could not be loaded.' }, settled: () => { previewBusy.value = false } }), 160)
}, { immediate: true })
watch(() => scope.owner.value, (owner, previous) => { if (!owner && previous) emit('close') })
watch([() => props.project.id, () => props.source?.id, () => props.recurrence?.id], () => { scope.reset(); emit('close') }, { flush: 'sync' })
const zoneNote = computed(() => schedule.kind === 'time' && schedule.zone !== 'Europe/Vienna' && times.value[0] ? `= ${new Intl.DateTimeFormat('en-GB', { timeZone: 'Europe/Vienna', hour: '2-digit', minute: '2-digit' }).format(new Date(times.value[0]))} in Vienna` : '')
const daylightNote = computed(() => {
  if (!times.value.length) return ''
  const fmt = new Intl.DateTimeFormat('en-GB', { timeZone: schedule.zone, timeZoneName: 'shortOffset' })
  const offsets = times.value.map(at => fmt.formatToParts(new Date(at)).find(p => p.type === 'timeZoneName')?.value)
  return new Set(offsets).size > 1 ? `The clocks change within these dates; runs stay at ${schedule.time} local time.` : ''
})
function previewTitle(at: string, index: number) { return renderTitle(template.title, (initial?.occurrence_count || 0) + index + 1, at, recurrenceZone(input.value.trigger)) }
function close() { if (!busy.value) emit('close') }
function save() {
  if (!allowed.value || busy.value || problems.value.length || previewError.value || previewBusy.value) return
  const snapshot = copyRecurrenceInput(input.value), id = initial?.id, revision = initial?.revision
  busy.value = true; failure.value = ''
  void scope.run(({ after, signal }) => after(id ? updateRecurrence(id, snapshot, revision!, signal) : createRecurrence(snapshot, signal), result => { emit('saved', result) }), { failed: error => { failure.value = error instanceof Error ? error.message : 'The recurrence was not saved.' }, settled: () => { busy.value = false } })
}
function keys(event: KeyboardEvent) {
  if (event.key === 'Enter' && (mac ? event.metaKey && !event.ctrlKey : event.ctrlKey && !event.metaKey) && !event.altKey && !event.shiftKey) { event.preventDefault(); save(); return }
  if (event.key === 'Escape') {
    if (picker.value) return
    event.preventDefault(); const target = event.target as HTMLElement
    if (target.matches('input, textarea, select') || target.isContentEditable) body.value?.focus({ preventScroll: true })
    else close()
  }
  if (['ArrowLeft', 'ArrowRight'].includes(event.key) && !event.metaKey && !event.ctrlKey && !event.altKey) {
    const group = (event.target as HTMLElement).closest('[role="radiogroup"]')
    if (!group) return
    const buttons = [...group.querySelectorAll<HTMLButtonElement>('button')], index = buttons.indexOf(event.target as HTMLButtonElement)
    event.preventDefault(); const button = buttons[(index + (event.key === 'ArrowRight' ? 1 : -1) + buttons.length) % buttons.length]; button?.click(); button?.focus()
  }
}
function chooseParent(value: { id: string; key: string; title: string } | null) { parent.value = value?.id || props.project.id; parentLabel.value = value ? `${value.key} · ${value.title}` : 'No parent (top level of the project)'; const anchor = picker.value; picker.value = null; anchor?.focus() }
onMounted(() => {
  dialog.value?.showModal(); void nextTick(() => nameField.value?.focus())
  if (parent.value !== props.project.id && !recurrenceSourceIsParent(source)) void scope.run(({ after }) => after(getNode(parent.value), node => { parentLabel.value = `${node.key} · ${node.title}` }), { failed: () => { parentLabel.value = 'Parent unavailable' } })
})
onBeforeUnmount(() => { clearTimeout(timer); dialog.value?.close() })
</script>

<template>
  <Teleport to="body">
    <dialog ref="dialog" class="recurrence-editor" :aria-labelledby="`${uid}-heading`" @cancel.prevent="close" @keydown.stop="keys" @click="event => { if (event.target === dialog) close() }">
      <header class="editor-head">
        <div class="editor-titles"><h2 :id="`${uid}-heading`">{{ initial ? 'Edit recurring work' : 'Repeat' }}</h2><p>{{ initial ? `Edit ${recurrenceName(initial)}` : recurrenceSourceIsParent(source) && source ? `Creates a child ${workNoun(vocabulary.leaf.name)} of ${source.key} each time` : source ? `Repeats ${source.key} as a template` : `New recurring work in ${project.title || project.routeKey}` }}</p></div>
        <div class="editor-actions"><button type="button" class="btn sm ghost" :disabled="busy" @click="close">Cancel<KeyCap k="esc" /></button><button type="button" class="btn sm primary save" :disabled="!allowed || busy || !!problems.length || previewBusy || !!previewError" :aria-keyshortcuts="mac ? 'Meta+Enter' : 'Control+Enter'" @click="save"><span>{{ busy ? 'Saving…' : initial ? 'Save' : 'Create' }}</span><span class="keys"><KeyCap k="mod" /><KeyCap k="enter" /></span></button></div>
      </header>
      <div ref="body" class="editor-body" tabindex="-1">
        <section class="editor-section" :aria-labelledby="`${uid}-what`"><p :id="`${uid}-what`" class="eyebrow">What</p>
          <div class="field-row"><label :for="`${uid}-name`">Name</label><input :id="`${uid}-name`" ref="nameField" v-model="template.name" class="field" maxlength="80" placeholder="e.g. Weekly tool sweep" /></div>
          <div class="field-row top"><span :id="`${uid}-title`" class="field-label">Title</span><TemplateTitle v-model="template.title" :event="schedule.kind === 'event' && input.trigger.event === 'release.published'" :label-id="`${uid}-title`" /></div>
          <div class="field-row"><span class="field-label">{{ vocabulary.leaf.name }}</span><div class="field-line"><select v-model="template.priority" class="field priority" aria-label="Priority"><option v-for="value in ['critical', 'high', 'medium', 'low']" :key="value" :value="value">{{ value[0]!.toUpperCase() + value.slice(1) }}</option></select><input v-model="estimate" class="field estimate" aria-label="Estimate" placeholder="Estimate" /></div></div>
          <div class="field-row top"><label :for="`${uid}-description`">Description</label><textarea :id="`${uid}-description`" v-model="template.description" class="field" rows="3" maxlength="65536" placeholder="What each run is about" /></div>
          <div class="field-row top"><label :for="`${uid}-criteria`">Criteria</label><textarea :id="`${uid}-criteria`" v-model="criteria" class="field mono" rows="3" placeholder="- [ ] What must be true when it is done" /></div>
        </section>
        <section class="editor-section" :aria-labelledby="`${uid}-where`"><p :id="`${uid}-where`" class="eyebrow">Where</p><div class="field-row"><span class="field-label">Parent</span><button type="button" class="field parent" aria-label="Parent" :disabled="!!initial" :data-tip="initial ? 'The parent is fixed for an existing recurrence' : undefined" aria-haspopup="dialog" :aria-expanded="!!picker" @click="picker = $event.currentTarget as HTMLElement"><AppIcon :name="parent === project.id ? 'folder' : 'epic'" :size="13" /><span>{{ parentLabel }}</span><AppIcon name="chevron" :size="12" /></button></div></section>
        <section class="editor-section" :aria-labelledby="`${uid}-when`"><p :id="`${uid}-when`" class="eyebrow">When</p>
          <div class="field-row"><span class="field-label">Trigger</span><div class="seg" role="radiogroup" aria-label="Trigger"><button type="button" role="radio" :aria-checked="schedule.kind === 'time'" @click="schedule.kind = 'time'"><AppIcon name="calendar" :size="13" />Schedule</button><button type="button" role="radio" :aria-checked="schedule.kind === 'event'" @click="schedule.kind = 'event'"><AppIcon name="journey" :size="13" />Event</button></div></div>
          <div class="trigger-slot">
            <div class="trigger-pane" :aria-hidden="schedule.kind !== 'time'" :inert="schedule.kind !== 'time'">
              <div class="field-row"><span class="field-label">Repeat</span><div class="seg" role="radiogroup" aria-label="Repeat"><button v-for="frequency in ['DAILY', 'WEEKLY', 'MONTHLY']" :key="frequency" type="button" role="radio" :aria-checked="schedule.frequency === frequency" @click="schedule.frequency = frequency">{{ frequency[0] + frequency.slice(1).toLowerCase() }}</button></div></div>
              <div class="field-row"><span class="field-label">On</span><div class="day-slot">
                <p :aria-hidden="schedule.frequency !== 'DAILY'" :inert="schedule.frequency !== 'DAILY'">Every day, weekends included</p>
                <div class="seg weekdays" role="group" aria-label="Weekdays" :aria-hidden="schedule.frequency !== 'WEEKLY'" :inert="schedule.frequency !== 'WEEKLY'"><button v-for="[day, label] in weekdays" :key="day" type="button" :aria-label="label" :aria-pressed="schedule.days.includes(day)" @click="schedule.days = schedule.days.includes(day) ? schedule.days.filter(d => d !== day) : [...schedule.days, day]">{{ label.slice(0, 2) }}</button></div>
                <div class="field-line" :aria-hidden="schedule.frequency !== 'MONTHLY'" :inert="schedule.frequency !== 'MONTHLY'"><select v-model="schedule.interval" class="field interval" aria-label="Every"><option v-for="n in [1, 2, 3, 6]" :key="n" :value="String(n)">{{ n === 1 ? 'Every month' : `Every ${n} months` }}</option></select><span class="small">on the</span><select v-model="schedule.day" class="field month-day" aria-label="Day of the month"><option v-for="day in 31" :key="day" :value="String(day)">{{ day }}</option><option value="-1">Last day</option><option v-if="!Array.from({ length: 31 }, (_, i) => String(i + 1)).includes(schedule.day) && schedule.day !== '-1'" :value="schedule.day">Days {{ schedule.day }} (existing rule)</option></select></div>
              </div></div>
              <div class="field-row"><label :for="`${uid}-time`">At</label><div class="field-line zone-line"><input :id="`${uid}-time`" v-model="schedule.time" class="field time" type="time" /><select v-model="schedule.zone" class="field zone" aria-label="Time zone"><option v-for="zone in zoneOptions" :key="zone" :value="zone">{{ zone }}</option></select><span class="zone-note">{{ zoneNote }}</span></div></div>
            </div>
            <div class="trigger-pane" :aria-hidden="schedule.kind !== 'event'" :inert="schedule.kind !== 'event'">
              <div class="field-row"><label :for="`${uid}-event`">After</label><select :id="`${uid}-event`" class="field event"><option>{{ triggerWords({ kind: 'event', event: initial?.trigger.event || 'release.published' }).replace(/^After /, '') }}</option></select></div>
              <div class="field-row"><span class="field-label">Start</span><div class="seg" role="radiogroup" aria-label="Start"><button v-for="[value, label] in ([['now', 'Right away'], ['hour', '1 hour later'], ['morning', 'Next morning']] as const)" :key="value" type="button" role="radio" :aria-checked="schedule.start === value" @click="schedule.start = value">{{ label }}</button></div></div>
              <div class="field-row"><span class="field-label">{{ input.trigger.event === 'release.published' ? 'Release' : 'Event' }}</span><div class="release-note"><span v-if="input.trigger.event === 'release.published'">The Release token becomes its marketing name, e.g. Sunlit Sonde.</span><span v-else>Source identifiers are included in each created ticket.</span><span v-if="schedule.start === 'morning'">Next morning means 06:00 in {{ schedule.zone }}.</span></div></div>
            </div>
          </div>
        </section>
        <section class="editor-section" :aria-labelledby="`${uid}-options`"><p :id="`${uid}-options`" class="eyebrow">Options</p><label class="option"><input v-model="queue" type="checkbox" /><span><b>Put each one into the work queue</b><small>Agents pick it up in queue order; it needs an estimate and criteria.</small></span></label><label class="option"><input v-model="skip" type="checkbox" /><span><b>Skip while the previous one is still open</b><small>Otherwise the next one is created anyway.</small></span></label><p class="fixed-rule"><AppIcon name="info" :size="13" />After downtime, at most one missed run is created, never all at once.</p></section>
        <section class="editor-section preview" :aria-labelledby="`${uid}-next`" aria-live="polite"><div class="preview-head"><p :id="`${uid}-next`" class="eyebrow">Next</p><span class="small">{{ triggerWords(input.trigger) }}</span></div><ol class="preview-rows"><li v-for="index in 4" :key="index"><template v-if="schedule.kind === 'time'"><span class="when">{{ times[index - 1] ? when(times[index - 1]!, schedule.zone) : index === 1 ? previewBusy ? 'Loading preview…' : 'No preview yet' : '—' }}</span><span class="what">{{ times[index - 1] ? initial?.open_previous && skip && index === 1 ? `Skipped if #${initial.open_previous.number} is still open` : previewTitle(times[index - 1]!, index - 1) : '' }}</span></template><template v-else><span class="when">{{ index === 1 ? 'Next release' : 'Following release' }}</span><span class="what">{{ renderTitle(template.title, (initial?.occurrence_count || 0) + index, new Date().toISOString(), schedule.zone, { name: '(next release)', version: '(version)' }) }}</span></template></li></ol>
          <ul class="notes"><li v-for="problem in problems" :key="problem" class="warning"><AppIcon name="alert" :size="13" /><span>{{ problem }}</span></li><li v-if="failure || previewError" class="warning" role="alert"><AppIcon name="alert" :size="13" /><span>{{ failure || previewError }}</span></li><li v-if="daylightNote"><AppIcon name="clock" :size="13" /><span>{{ daylightNote }}</span></li><li v-if="schedule.kind === 'time'"><AppIcon name="clock" :size="13" /><span>Nonexistent times when clocks move forward are skipped. Repeated times run once, at the first instant.</span></li><li v-if="schedule.kind === 'time' && schedule.frequency === 'MONTHLY'"><AppIcon name="calendar" :size="13" /><span>{{ schedule.day === '-1' ? 'Last day follows each month, including February.' : 'Months without the selected day are skipped.' }}</span></li><li v-if="schedule.kind === 'event'"><AppIcon name="info" :size="13" /><span>Publication dates cannot be predicted. Past releases are not created automatically.</span></li></ul>
          <p class="editor-hint"><KeyCap k="mod" /><KeyCap k="enter" /> {{ initial ? 'save' : 'create' }} · <KeyCap k="esc" /> leaves a field, then closes</p>
        </section>
      </div>
      <EpicPicker v-if="picker" :anchor="picker" :project-id="project.id" :current="parent === project.id ? null : parent" subject="recurring work" allow-none @choose="chooseParent" @close="restore => { const anchor = picker; picker = null; if (restore) anchor?.focus() }" />
    </dialog>
  </Teleport>
</template>

<style scoped>
.recurrence-editor { position: fixed; inset: auto; top: 64px; left: 50%; transform: translateX(-50%); margin: 0; padding: 0; width: min(var(--dialog-l), calc(100vw - 32px)); max-height: calc(100dvh - 80px); color: var(--ink); border: 1px solid var(--glass-edge); border-radius: 16px; background: var(--surface-raised); box-shadow: var(--shadow-pop); overflow: hidden; }
.recurrence-editor[open] { display: flex; flex-direction: column; }
.recurrence-editor::backdrop { background: var(--scrim); }
.editor-head { display: flex; align-items: center; gap: 10px; flex: none; height: 60px; padding: 10px 14px 10px 20px; border-bottom: 1px solid var(--line); }
.editor-titles { flex: 1; min-width: 0; }.editor-titles h2 { font: 600 16px/1.3 var(--font); }.editor-titles p { font-size: 12.5px; color: var(--ink-3); overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.editor-actions { display: flex; gap: 8px; flex: none; }.save { min-width: 112px; }.keys { display: inline-flex; gap: 2px; }
.editor-body { overflow: auto; overscroll-behavior: contain; padding: 4px 20px 18px; }
.editor-section { min-width: 0; display: grid; gap: 10px; padding: 16px 0; border-top: 1px solid var(--line); }.editor-section:first-child { border: 0; }
.field-row { display: grid; grid-template-columns: 84px minmax(0, 1fr); gap: 12px; align-items: center; min-height: 34px; }.field-row > label, .field-label { font-size: 12.5px; color: var(--ink-3); }.field-row.top { align-items: start; }.top > label, .top > .field-label { padding-top: 8px; }
textarea.field { height: auto; min-height: 70px; line-height: 1.5; resize: vertical; }.field { font-size: 13px; min-width: 0; }.field-row > .seg { justify-self: start; }.field-line { display: flex; align-items: center; flex-wrap: wrap; gap: 8px 10px; min-width: 0; }.priority { width: 112px; }.estimate { width: 100px; }.time { width: 120px; }.zone { flex: 1; width: 200px; }.interval { width: 140px; }.month-day { width: 105px; }.event { width: 260px; }
.parent { display: flex; align-items: center; gap: 8px; text-align: left; }.parent span { flex: 1; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.trigger-slot, .day-slot { display: grid; }.trigger-pane { grid-area: 1/1; display: grid; gap: 10px; align-content: start; }.day-slot > * { grid-area: 1/1; align-self: center; }.day-slot > .seg { justify-self: start; }[aria-hidden="true"] { visibility: hidden; }
.weekdays button { min-width: 34px; padding: 0 7px; }.seg button[aria-pressed="true"] { background: var(--seg-on); color: var(--teal-ink); box-shadow: inset 0 0 0 1px var(--glass-edge); }.zone-note { flex-basis: 100%; height: 18px; color: var(--ink-3); font-size: 12px; }.small { font-size: 12px; color: var(--ink-3); }.release-note { display: grid; gap: 3px; height: 60px; align-content: start; font-size: 12px; color: var(--ink-3); }
.option { display: grid; grid-template-columns: 18px minmax(0, 1fr); gap: 10px; cursor: pointer; }.option input { margin: 3px 0 0; accent-color: var(--teal); }.option b { display: block; font-size: 13px; font-weight: 600; }.option small { display: block; font-size: 12px; color: var(--ink-3); }.fixed-rule { display: flex; align-items: center; gap: 8px; padding-left: 28px; font-size: 12px; color: var(--ink-3); }
.preview-head { min-width: 0; display: flex; gap: 12px; align-items: baseline; justify-content: space-between; }.preview-head .small { min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }.preview-rows { padding: 0; list-style: none; }.preview-rows li { display: grid; grid-template-columns: 216px minmax(0, 1fr); align-items: center; gap: 12px; height: 32px; border-top: 1px solid var(--line); font-size: 13px; }.preview-rows li:first-child { border: 0; }.when { font-variant-numeric: tabular-nums; white-space: nowrap; }.what { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; color: var(--ink-2); }
.notes { list-style: none; padding: 0; display: grid; gap: 6px; }.notes li { display: grid; grid-template-columns: 14px minmax(0, 1fr); gap: 8px; font-size: 12.5px; color: var(--ink-2); }.notes svg { margin-top: 2px; }.notes .warning { color: var(--warn-ink); }.editor-hint { display: flex; align-items: center; gap: 4px; font-size: 12px; color: var(--ink-3); }
@media (max-width: 600px) { .recurrence-editor { top: 8px; left: 0; transform: none; width: 100%; max-width: none; height: calc(100dvh - 8px); max-height: none; border-radius: 16px 16px 0 0; border-bottom: 0; }.editor-head { padding: 8px 10px 8px 16px; }.editor-actions :deep(.keycap) { display: none; }.save { min-width: 84px; }.editor-body { padding: 0 16px 24px; }.field-row { grid-template-columns: minmax(0, 1fr); gap: 6px; }.top > label, .top > .field-label { padding: 0; }.zone { min-width: 150px; }.event { width: 100%; }.weekdays button { min-width: 34px; }.release-note { height: 68px; }.preview-rows li { grid-template-columns: minmax(0, 1fr); height: 50px; gap: 0; align-content: center; }.editor-hint { display: none; } }
</style>
