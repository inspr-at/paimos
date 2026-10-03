<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script lang="ts">
export interface LogTicket { id: string; key: string; title: string }
export interface LogRequest { ticket: LogTicket; costUnitId: string; currency: string; day: Date; seconds: number; startedAt: string; note: string }
// The cost unit last used, for the session (no device storage, like other preferences).
let lastCostUnit = ''
</script>
<script setup lang="ts">
import { computed, nextTick, onMounted, ref, watch } from 'vue'
import { listNodes } from '../../lib/api'
import type { EntryPatch, TimeEntry } from '../../lib/business'
import { recents } from '../../lib/recents'
import { dayKey, parseTimeInput, periodLabel, timeOfDay, WEEKDAYS } from '../../lib/week'
import { formatClock, formatSpan, parseDurationInput } from './duration'
import { formatAmount } from './money'
import { useBusiness } from '../../stores/business'
import AppIcon from './BizIcon.vue'
import PickerMenu, { type PickOption } from './PickerMenu.vue'

// One line to log time: a ticket, the cost unit that prices it, the day, and a
// duration typed the way people say it ("1h30", "90m", "1.5"). Enter logs it and
// keeps ticket, cost unit and day for the next entry.
// With an entry, the same line corrects it in place: prefilled, Enter saves only
// what changed, Escape cancels. An agent run's ticket and time stay as measured.
const props = defineProps<{
  days: Date[]; suggestions: LogTicket[]; busy: boolean; preset?: LogTicket | null
  defaultStartMinutes?: (day: Date) => number
  entry?: TimeEntry | null; entryTicket?: LogTicket | null; bounds?: { starts_at: string; ends_at: string } | null
  // Another form on the page holds the primary action.
  quiet?: boolean; saveLabel?: string
}>()
const emit = defineEmits<{ log: [request: LogRequest]; save: [patch: EntryPatch, ticket: LogTicket]; cancel: []; remove: [] }>()
const business = useBusiness()
// The entry is fixed for the component's life: a draft survives a conflict.
const seed = props.entry ?? null
const seedStart = seed ? new Date(seed.started_at) : null
const seedClock = seed ? timeOfDay(seed.started_at) : ''
const seedDuration = seed ? formatClock(seed.duration_seconds) : ''
const measured = seed?.source === 'agent_run'
const ticket = ref<LogTicket | null>(seed ? props.entryTicket ?? null : null)
const costUnitId = ref(seed ? seed.cost_unit_node_id : lastCostUnit)
const dayIndex = ref(0)
const duration = ref(seedDuration)
const start = ref(seedClock)
const note = ref(seed?.note ?? '')
const picker = ref<HTMLElement | null>(null)
const pickButton = ref<HTMLButtonElement>()
const durationInput = ref<HTMLInputElement>()
const noteInput = ref<HTMLInputElement>()
const tried = ref(false)

const todayKey = dayKey(new Date())
watch(() => props.days, days => {
  const wanted = seedStart ? dayKey(seedStart) : todayKey
  const found = days.findIndex(day => dayKey(day) === wanted)
  dayIndex.value = found === -1 ? 0 : found
}, { immediate: true })
watch(() => props.preset, value => { if (value && !seed) ticket.value = value }, { immediate: true })
const day = computed(() => props.days[dayIndex.value])
// A correction stays inside the entry's period.
function dayOutside(d: Date) {
  if (!props.bounds) return false
  const from = new Date(d.getFullYear(), d.getMonth(), d.getDate()).getTime()
  return from + 86_400_000 <= Date.parse(props.bounds.starts_at) || from >= Date.parse(props.bounds.ends_at)
}

const seconds = computed(() => parseDurationInput(duration.value))
const startMinutes = computed(() => start.value.trim() ? parseTimeInput(start.value) : null)
const startChanged = computed(() => !!seed && start.value.trim() !== seedClock)
const durationChanged = computed(() => !!seed && duration.value.trim() !== seedDuration)
// One start instant for pricing and submission. New entries use the same
// stacked start as the week; corrections retain their untouched stored start.
const newStart = computed<Date | null>(() => {
  if (!day.value) return null
  if (!seed || !seedStart) {
    const minutes = start.value.trim() ? startMinutes.value : props.defaultStartMinutes?.(day.value) ?? 9 * 60
    return minutes === null ? null : new Date(day.value.getFullYear(), day.value.getMonth(), day.value.getDate(), 0, minutes)
  }
  if (dayKey(day.value) === dayKey(seedStart) && !startChanged.value) return seedStart
  const minutes = startChanged.value ? startMinutes.value : seedStart.getHours() * 60 + seedStart.getMinutes()
  if (minutes === null) return null
  return new Date(day.value.getFullYear(), day.value.getMonth(), day.value.getDate(), 0, minutes, startChanged.value ? 0 : seedStart.getSeconds())
})
const newSeconds = computed(() => seed ? (durationChanged.value ? seconds.value : seed.duration_seconds) : seconds.value)
// The server prices a new entry and re-prices a correction on the start's UTC date.
const utcDay = computed(() => {
  return newStart.value ? newStart.value.toISOString().slice(0, 10) : seedStart?.toISOString().slice(0, 10) ?? ''
})
// Keep selector choices while a time such as "9:" is incomplete. Rates and
// submission still use the actual start; a completed time revalidates choices.
const eligibilityDay = computed<string>(previous => newStart.value ? utcDay.value : previous ?? '')
const inForce = (r: { unit: string; currency: string; effective_from: string; effective_until: string | null }) =>
  r.unit === 'hour' && (!seed || r.currency === seed.currency) && r.effective_from <= eligibilityDay.value && (!r.effective_until || r.effective_until > eligibilityDay.value)
// Hourly cost units with a rate on that day (in the entry's currency when correcting).
const hourly = computed(() => business.costUnits.filter(unit => unit.node.id === seed?.cost_unit_node_id || (!['cancelled', 'archived', 'done'].includes(unit.node.state) && unit.rates.some(inForce))))
watch(hourly, units => { if (!seed && !units.some(unit => unit.node.id === costUnitId.value)) costUnitId.value = units.length === 1 ? units[0].node.id : '' }, { immediate: true })
watch(costUnitId, value => { if (value && !seed) lastCostUnit = value })
const rate = computed<{ bill_amount: string; currency: string } | null>(() => {
  if (seed) {
    const repriced = costUnitId.value !== seed.cost_unit_node_id || utcDay.value !== new Date(seed.started_at).toISOString().slice(0, 10)
    return repriced ? business.rateOn(costUnitId.value, 'hour', seed.currency, utcDay.value) : { bill_amount: seed.rate_amount, currency: seed.currency }
  }
  const unit = business.costUnit(costUnitId.value)
  const currencies = [...new Set(unit?.rates.filter(r => r.unit === 'hour').map(r => r.currency) ?? [])].sort((a, b) => (a === 'EUR' ? -1 : b === 'EUR' ? 1 : a.localeCompare(b)))
  for (const currency of currencies) { const found = business.rateOn(costUnitId.value, 'hour', currency, utcDay.value); if (found) return found }
  return null
})
const problem = computed(() => {
  if (!ticket.value) return 'Choose a ticket.'
  if (!costUnitId.value) return hourly.value.length ? 'Choose a cost unit.' : 'No cost unit has an hourly rate on this day. Add one under Rates.'
  if (!rate.value) return seed ? `This cost unit has no hourly rate in ${seed.currency} on that day.` : 'This cost unit has no hourly rate on this day.'
  if (!duration.value.trim()) return 'Enter how long, like 1h30 or 90m.'
  if ((!seed || durationChanged.value) && !seconds.value) return 'Use a duration like 1h30, 90m or 1.5 (up to 24h).'
  if (seed && !start.value.trim()) return 'Enter a start time, like 9:30.'
  if (start.value.trim() && (!seed || startChanged.value) && startMinutes.value === null) return 'Start is a time like 9:30.'
  if (seed && newStart.value && newSeconds.value) {
    const end = new Date(newStart.value.getTime() + newSeconds.value * 1000)
    if (dayKey(end) !== dayKey(newStart.value) && end.getHours() + end.getMinutes() + end.getSeconds() > 0) return 'That would run past midnight. Split it across two days.'
    if (props.bounds && (newStart.value.getTime() < Date.parse(props.bounds.starts_at) || end.getTime() > Date.parse(props.bounds.ends_at))) return `That falls outside its period, ${periodLabel(props.bounds.starts_at, props.bounds.ends_at)}.`
  }
  return ''
})
const preview = computed(() => newSeconds.value && rate.value ? `${formatSpan(newSeconds.value)} · ${formatAmount(multiply(rate.value.bill_amount, newSeconds.value), rate.value.currency)} ${rate.value.currency}` : '')
const status = computed(() => {
  if (tried.value && problem.value) return problem.value
  if (!seed) return preview.value || 'Enter logs it. Ticket, cost unit and day stay for the next entry.'
  const note = measured ? 'The ticket and time come from the agent run.' : ''
  return [preview.value, note].filter(Boolean).join(' · ')
})
// Exact amount the server will book: rate × seconds / 3600, rounded half up to four places.
function multiply(amount: string, secs: number) {
  const [whole, frac = ''] = amount.split('.')
  const units = BigInt(whole + frac.padEnd(4, '0').slice(0, 4)) * BigInt(secs)
  const rounded = (units + 1800n) / 3600n
  const text = rounded.toString().padStart(5, '0')
  return `${text.slice(0, -4)}.${text.slice(-4)}`
}

const ticketOptions = computed<PickOption[]>(() => {
  const seen = new Set<string>()
  const out: PickOption[] = []
  for (const item of [...props.suggestions, ...recents.filter(r => r.type === 'ticket').map(r => ({ id: '', key: r.key, title: r.title }))]) {
    if (seen.has(item.key)) continue
    seen.add(item.key)
    out.push({ value: item.id || `key:${item.key}`, label: item.title, badge: item.key })
  }
  return out.slice(0, 10)
})
async function searchTickets(term: string): Promise<PickOption[]> {
  const page = await listNodes({ q: term, kind: ['ticket', 'task', 'epic'], sort: /^[a-z]+-\d*$/i.test(term) ? 'key' : '-updated_at', limit: 10 })
  return page.items.map(item => ({ value: item.id, label: item.title, badge: item.key, hint: item.project?.title }))
}
async function chooseTicket(option: PickOption) {
  picker.value = null
  let id = option.value
  if (id.startsWith('key:')) {
    const key = id.slice(4)
    const page = await listNodes({ q: key, sort: 'key', limit: 5 })
    id = page.items.find(item => item.key === key)?.id ?? ''
    if (!id) return
  }
  ticket.value = { id, key: option.badge ?? '', title: option.label }
  await nextTick(); durationInput.value?.focus()
}
function closePicker(restore: boolean) { picker.value = null; if (restore) pickButton.value?.focus() }
// Only what differs from the stored entry; an empty patch means nothing changed.
function changes(): EntryPatch {
  const out: EntryPatch = {}
  if (!seed || !ticket.value) return out
  if (ticket.value.id !== seed.node_id) out.node_id = ticket.value.id
  if (costUnitId.value !== seed.cost_unit_node_id) out.cost_unit_node_id = costUnitId.value
  if (note.value.trim() !== seed.note.trim()) out.note = note.value.trim()
  if (newStart.value && seedStart && newStart.value.getTime() !== seedStart.getTime()) out.started_at = newStart.value.toISOString()
  if (durationChanged.value && seconds.value && seconds.value !== seed.duration_seconds) out.duration_seconds = seconds.value
  return out
}
function submit() {
  tried.value = true
  if (problem.value || props.busy || !ticket.value || !rate.value || !newSeconds.value || !day.value) return
  if (seed) { emit('save', changes(), ticket.value); return }
  if (!newStart.value) return
  emit('log', { ticket: ticket.value, costUnitId: costUnitId.value, currency: rate.value.currency, day: day.value, seconds: newSeconds.value, startedAt: newStart.value.toISOString(), note: note.value.trim() })
}
function reset() { duration.value = ''; start.value = ''; note.value = ''; tried.value = false; void nextTick(() => durationInput.value?.focus()) }
function keys(event: KeyboardEvent) {
  if (event.key === 'Enter' && (event.target as HTMLElement).tagName === 'INPUT') { event.preventDefault(); submit() }
  else if (event.key === 'Escape' && seed && !event.defaultPrevented) { event.preventDefault(); event.stopPropagation(); emit('cancel') }
}
function focusFirst() {
  const field = measured ? noteInput.value : ticket.value ? durationInput.value : pickButton.value
  field?.focus()
  if (seed && field instanceof HTMLInputElement) field.select()
}
onMounted(() => { if (seed) void nextTick(focusFirst) })
defineExpose({ reset, focus: focusFirst, openPicker: () => { picker.value = pickButton.value ?? null } })
</script>

<template>
  <form class="log-bar" :class="{ editing: !!seed }" :aria-label="seed ? 'Edit entry' : 'Log time'" @submit.prevent="submit" @keydown="keys">
    <button ref="pickButton" type="button" class="pick ticket-pick" :class="{ unset: !ticket }" :disabled="measured" aria-haspopup="dialog" :aria-expanded="!!picker" :aria-label="ticket ? `Ticket: ${ticket.key} ${ticket.title}` : 'Ticket: choose a ticket'" @click="picker = picker ? null : ($event.currentTarget as HTMLElement)">
      <span v-if="ticket" class="key-badge">{{ ticket.key }}</span><AppIcon v-else name="ticket" :size="14" />
      <span class="pick-text">{{ ticket?.title ?? 'Ticket' }}</span><AppIcon :name="measured ? 'lock' : 'chevron'" :size="12" class="chev" />
    </button>
    <label class="select-wrap">
      <span class="sr-only">Cost unit</span>
      <select v-model="costUnitId" class="pick select" :class="{ unset: !costUnitId }" aria-label="Cost unit">
        <option value="" disabled>Cost unit</option>
        <option v-for="unit in hourly" :key="unit.node.id" :value="unit.node.id">{{ unit.node.title }}</option>
      </select>
      <AppIcon name="chevron" :size="12" class="select-chev" />
    </label>
    <div class="seg days" role="radiogroup" aria-label="Day">
      <button v-for="(d, i) in days" :key="i" type="button" role="radio" :aria-checked="dayIndex === i" :aria-label="`${WEEKDAYS[i]} ${d.getDate()}`" :class="{ today: dayKey(d) === todayKey }" :disabled="measured || dayOutside(d)" @click="dayIndex = i">{{ WEEKDAYS[i].slice(0, 2) }}</button>
    </div>
    <input v-model="start" class="field start" inputmode="numeric" placeholder="Start" :aria-label="seed ? 'Start time' : 'Start time (optional)'" autocomplete="off" :disabled="measured" :data-tip="seed ? undefined : 'Optional, like 9:30. Empty: after the day’s last entry'" />
    <input ref="durationInput" v-model="duration" class="field duration" :class="{ bad: tried && !!duration.trim() && (!seed || durationChanged) && !seconds }" placeholder="1h30" aria-label="Duration" autocomplete="off" :disabled="measured" />
    <input ref="noteInput" v-model="note" class="field note" placeholder="Note" aria-label="Note (optional)" maxlength="500" autocomplete="off" />
    <template v-if="seed">
      <button type="button" class="btn cancel-btn" aria-keyshortcuts="Escape" @click="emit('cancel')">Cancel</button>
      <button type="submit" class="btn primary log-btn" :disabled="busy"><AppIcon name="check" :size="13" />{{ busy ? 'Saving…' : saveLabel ?? 'Save' }}</button>
    </template>
    <button v-else type="submit" class="btn log-btn" :class="{ primary: !quiet }" :disabled="busy"><AppIcon name="plus" :size="13" />{{ busy ? 'Logging…' : 'Log' }}</button>
    <div class="log-foot">
      <p class="log-status" :class="{ warn: tried && !!problem }" aria-live="polite">{{ status }}</p>
      <template v-if="seed">
        <span class="edit-keys"><kbd class="keycap">Enter</kbd> saves · <kbd class="keycap">Esc</kbd> cancels</span>
        <button type="button" class="btn sm ghost danger remove-btn" @click="emit('remove')"><AppIcon name="trash" :size="13" />Delete entry</button>
      </template>
    </div>
    <PickerMenu v-if="picker" :anchor="picker" title="Ticket" :options="ticketOptions" :search="searchTickets" :current="ticket?.id" placeholder="Search tickets by key or title…" :width="380" empty="No ticket matches." @choose="chooseTicket" @close="closePicker" />
  </form>
</template>

<style scoped>
.log-bar { display: grid; grid-template-columns: minmax(180px, 1.4fr) minmax(130px, .8fr) auto 72px 84px minmax(100px, 1fr) auto; align-items: center; gap: 8px; }
.pick { display: flex; align-items: center; gap: 8px; min-width: 0; height: 34px; padding: 0 10px 0 11px; border: 1px solid var(--glass-edge); border-radius: var(--radius-s); background: var(--field-bg); box-shadow: var(--field-inset), 0 0 0 1px var(--line); color: var(--ink); font-size: 13.5px; text-align: left; }
.pick:hover { box-shadow: var(--field-inset), 0 0 0 1px var(--glass-rim); }
.pick:focus-visible { box-shadow: var(--focus-ring); }
.pick.unset { color: var(--ink-3); }
.pick svg { flex-shrink: 0; color: var(--ink-3); }
.pick .key-badge { height: 20px; padding: 0 6px; font-size: 10.5px; }
.pick-text { flex: 1; min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.select-wrap { position: relative; display: block; min-width: 0; }
.select { width: 100%; appearance: none; padding-right: 26px; cursor: pointer; }
.select-chev { position: absolute; right: 10px; top: 50%; margin-top: -6px; color: var(--ink-3); pointer-events: none; }
.days button { min-width: 30px; padding: 0 7px; font-family: var(--mono); font-size: 11.5px; text-transform: uppercase; }
.days button.today:not([aria-checked="true"]) { color: var(--teal-ink); }
.field { height: 34px; font-size: 13.5px; }
.start, .duration { font-family: var(--mono); font-size: 13px; font-variant-ligatures: none; }
.duration.bad { box-shadow: var(--field-inset), 0 0 0 1px var(--danger-line); color: var(--danger); }
.log-btn { height: 34px; }
.pick:disabled, .field:disabled, .days button:disabled { cursor: not-allowed; opacity: .55; }
.log-foot { grid-column: 1 / -1; display: flex; align-items: center; gap: 12px; min-height: 18px; }
.log-status { flex: 1; min-width: 0; font-size: 12px; color: var(--ink-3); }
.log-status.warn { color: var(--gold-ink); }
.edit-keys { display: inline-flex; align-items: center; gap: 4px; font-size: 11.5px; color: var(--ink-3); white-space: nowrap; }
.remove-btn { gap: 6px; }
/* Correcting an entry: the same line plus Cancel beside Save. */
.editing { grid-template-columns: minmax(170px, 1.4fr) minmax(120px, .8fr) auto 64px 72px minmax(90px, 1fr) auto auto; }
.cancel-btn { height: 34px; }
@container (max-width: 980px) {
  .log-bar { grid-template-columns: minmax(0, 1fr) minmax(0, 1fr); }
  .days { grid-column: 1 / -1; justify-self: start; }
  .note { grid-column: 1 / -1; }
  .log-btn { grid-column: 2; justify-self: end; }
  .editing .cancel-btn { grid-column: 1; justify-self: end; }
  .editing .log-btn { grid-column: 2; justify-self: start; }
  .edit-keys { display: none; }
}
@media (max-width: 720px) {
  .log-bar { grid-template-columns: minmax(0, 1fr) minmax(0, 1fr); }
  .ticket-pick, .select-wrap, .note, .days { grid-column: 1 / -1; }
  .days { display: grid; grid-template-columns: repeat(7, 1fr); justify-self: stretch; }
  .days button { height: 36px; }
  .pick, .field, .log-btn { height: 44px; font-size: 16px; }
  .log-btn { grid-column: 1 / -1; justify-self: stretch; }
  .cancel-btn { height: 44px; font-size: 16px; }
  .editing .cancel-btn { grid-column: 1; justify-self: stretch; }
  .editing .log-btn { grid-column: 2; justify-self: stretch; }
  .log-foot { flex-wrap: wrap; }
  .remove-btn { height: 40px; }
}
</style>
