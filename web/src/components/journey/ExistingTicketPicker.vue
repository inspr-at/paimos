<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { APIError } from '../../lib/api'
import { confirmAction } from '../../lib/confirm'
import { confirmOptionMoves } from '../../lib/releaseAssign'
import {
  addReleaseMembership, availabilityMark, canSelectTicket, isMoveConflict, listReleaseTicketOptions,
  type MembershipResult, type MembershipTicket,
} from '../../lib/releaseMembership'
import { highlight, kindLabel, statusMeta, statusOptions } from '../../lib/work'
import AppIcon from '../AppIcon.vue'
import KeyCap from '../KeyCap.vue'
import StatusIcon from '../work/StatusIcon.vue'

// Add tickets that already exist into this planning release. Search is by key or
// title; status, epic and type narrow it. Closed and already released tickets
// are shown and cannot be picked. A ticket in another open release asks first.
const props = defineProps<{ projectId: string; releaseId: string; releaseTitle: string; epics: { id: string; key: string; title: string }[] }>()
const emit = defineEmits<{ added: [payload: { count: number | null; result: MembershipResult }]; close: [] }>()

const dialog = ref<HTMLDialogElement>()
const searchEl = ref<HTMLInputElement>()
const term = ref('')
const status = ref('')
const epic = ref('')
const type = ref('')
const tickets = ref<MembershipTicket[]>([])
const revision = ref(0)
const selected = ref(new Map<string, MembershipTicket>())
const active = ref(0)
const loading = ref(true)
const failed = ref('')
const busy = ref(false)
const note = ref('')
let timer: ReturnType<typeof setTimeout> | undefined
let generation = 0

const statuses = statusOptions()
const types = [{ value: 'ticket', label: 'Ticket' }, { value: 'task', label: 'Task' }]
const picked = computed(() => [...selected.value.values()].filter(canSelectTicket))
const addableCount = computed(() => tickets.value.filter(canSelectTicket).length)

async function load() {
  const request = ++generation
  loading.value = true
  failed.value = ''
  try {
    const page = await listReleaseTicketOptions(props.projectId, props.releaseId, {
      q: term.value, status: status.value, epic: epic.value, type: type.value, limit: 50,
    })
    if (request !== generation) return
    tickets.value = page.tickets
    revision.value = page.expected_revision
    const next = new Map(selected.value)
    for (const ticket of page.tickets) if (next.has(ticket.ticket_node_id) && !canSelectTicket(ticket)) next.delete(ticket.ticket_node_id)
    selected.value = next
    active.value = Math.min(active.value, Math.max(0, page.tickets.length - 1))
  } catch (error) {
    if (request !== generation) return
    tickets.value = []
    const missing = error instanceof APIError && (error.status === 404 || error.status === 405)
    failed.value = missing ? 'This server cannot list existing tickets yet.' : error instanceof Error ? error.message : 'Tickets could not be loaded.'
  } finally {
    if (request === generation) loading.value = false
  }
}
watch([term, status, epic, type], () => { clearTimeout(timer); timer = setTimeout(load, 160) })
onMounted(async () => { dialog.value?.showModal(); await nextTick(); searchEl.value?.focus(); void load() })
onBeforeUnmount(() => { clearTimeout(timer); generation++ })

function toggle(ticket: MembershipTicket) {
  if (!canSelectTicket(ticket) || busy.value) return
  const next = new Map(selected.value)
  if (next.has(ticket.ticket_node_id)) next.delete(ticket.ticket_node_id)
  else next.set(ticket.ticket_node_id, ticket)
  selected.value = next
}
function selectAddable() {
  const next = new Map(selected.value)
  for (const ticket of tickets.value) if (canSelectTicket(ticket)) next.set(ticket.ticket_node_id, ticket)
  selected.value = next
}
function move(step: number) {
  if (!tickets.value.length) return
  active.value = Math.max(0, Math.min(tickets.value.length - 1, active.value + step))
  dialog.value?.querySelector<HTMLElement>(`#existing-option-${active.value}`)?.scrollIntoView({ block: 'nearest' })
}
function keydown(event: KeyboardEvent) {
  const inSearch = event.target === searchEl.value
  if (event.key === 'ArrowDown' || event.key === 'ArrowUp') {
    event.preventDefault()
    move(event.key === 'ArrowDown' ? 1 : -1)
    return
  }
  if (event.key === ' ' && !inSearch) {
    event.preventDefault()
    const ticket = tickets.value[active.value]
    if (ticket) toggle(ticket)
    return
  }
  if (event.key === 'Enter') {
    if (event.metaKey || event.ctrlKey || !inSearch) { event.preventDefault(); void add(); return }
    event.preventDefault()
    const ticket = tickets.value[active.value]
    if (ticket) toggle(ticket)
    return
  }
  if ((event.metaKey || event.ctrlKey) && event.key.toLowerCase() === 'a' && !inSearch) {
    event.preventDefault()
    selectAddable()
  }
}
async function add() {
  const chosen = picked.value
  if (!chosen.length || busy.value) return
  note.value = ''
  if (!await confirmOptionMoves(chosen, props.releaseTitle)) return
  busy.value = true
  try {
    let confirmMove = chosen.some(ticket => ticket.availability === 'other_release')
    try {
      const result = await addReleaseMembership(props.projectId, props.releaseId, {
        expected_revision: revision.value, ticket_node_ids: chosen.map(ticket => ticket.ticket_node_id), confirm_move: confirmMove,
      })
      added(result)
    } catch (error) {
      if (!confirmMove && isMoveConflict(error)) {
        const ok = await confirmAction({
          title: 'Move into this release?',
          body: `At least one selected ticket is already in another open release. Moving it puts it in ${props.releaseTitle}.`,
          points: chosen.map(ticket => `${ticket.key} · ${ticket.title}`),
          confirmLabel: `Move into ${props.releaseTitle}`,
          cancelLabel: 'Leave them',
        })
        if (!ok) return
        const result = await addReleaseMembership(props.projectId, props.releaseId, {
          expected_revision: revision.value, ticket_node_ids: chosen.map(ticket => ticket.ticket_node_id), confirm_move: true,
        })
        added(result)
        return
      }
      throw error
    }
  } catch (error) {
    const missing = error instanceof APIError && (error.status === 404 || error.status === 405)
    note.value = missing ? 'This server cannot add tickets to a release yet.' : error instanceof Error ? error.message : 'The tickets were not added.'
  } finally { busy.value = false }
}
function added(result: MembershipResult) {
  // Parents and overlapping selections expand on the server. Older servers may
  // omit the leaf set; the selected root count cannot stand in for that result.
  emit('added', { count: result.leaf_node_ids ? new Set(result.leaf_node_ids).size : null, result })
}
function close() { dialog.value?.close(); emit('close') }
function backdrop(event: MouseEvent) { if (event.target === dialog.value) close() }
</script>

<template>
  <dialog ref="dialog" class="picker" aria-labelledby="existing-title" @cancel.prevent="close" @click="backdrop" @keydown="keydown">
    <div class="card">
      <header>
        <h2 id="existing-title">Add existing tickets</h2>
        <button type="button" class="icon-btn sm" aria-label="Close" @click="close"><AppIcon name="close" :size="14" /></button>
      </header>
      <input ref="searchEl" v-model="term" class="field" placeholder="Search by key or title" aria-label="Find a ticket" aria-controls="existing-options" aria-autocomplete="list" :aria-activedescendant="tickets.length ? `existing-option-${active}` : undefined" data-autofocus />
      <div class="filters">
        <select v-model="status" class="field" aria-label="Status">
          <option value="">Any status</option>
          <option v-for="option in statuses" :key="option.value" :value="option.value">{{ option.meta.label }}</option>
        </select>
        <select v-model="epic" class="field" aria-label="Epic">
          <option value="">Any epic</option>
          <option v-for="item in epics" :key="item.id" :value="item.id">{{ item.key }} · {{ item.title }}</option>
        </select>
        <select v-model="type" class="field" aria-label="Type">
          <option value="">Any type</option>
          <option v-for="option in types" :key="option.value" :value="option.value">{{ option.label }}</option>
        </select>
      </div>
      <div id="existing-options" class="options" role="listbox" aria-multiselectable="true" aria-label="Existing tickets" tabindex="0">
        <div
          v-for="(ticket, index) in tickets" :id="`existing-option-${index}`" :key="ticket.ticket_node_id" role="option" class="option"
          :class="{ active: index === active, picked: selected.has(ticket.ticket_node_id) }"
          :aria-selected="selected.has(ticket.ticket_node_id)" :aria-disabled="canSelectTicket(ticket) ? undefined : true"
          @pointermove="active = index" @click="toggle(ticket)"
        >
          <input type="checkbox" :checked="selected.has(ticket.ticket_node_id)" :disabled="!canSelectTicket(ticket)" :aria-label="`Select ${ticket.key}`" tabindex="-1" @click.stop @change="toggle(ticket)" />
          <StatusIcon :state="ticket.status" :size="13" />
          <span class="key mono">{{ ticket.key }}</span>
          <span class="copy">
            <span class="title" :data-tip="ticket.title"><template v-for="(part, i) in highlight(ticket.title, term)" :key="i"><mark v-if="part.match">{{ part.text }}</mark><template v-else>{{ part.text }}</template></template></span>
            <span v-if="availabilityMark(ticket)" class="mark">{{ availabilityMark(ticket) }}</span>
            <span v-else class="mark faint">{{ statusMeta(ticket.status).label }}</span>
          </span>
          <span v-if="ticket.type !== 'ticket'" class="type">{{ kindLabel(ticket.type) }}</span>
        </div>
        <p v-if="loading && !tickets.length" class="empty" role="status">Looking for tickets…</p>
        <p v-else-if="failed" class="empty error" role="alert">{{ failed }}</p>
        <p v-else-if="!tickets.length" class="empty">{{ term || status || epic || type ? 'No ticket matches.' : 'No other tickets to add.' }}</p>
      </div>
      <p v-if="note" class="note error" role="alert">{{ note }}</p>
      <footer>
        <p class="hint">
          <span class="count"><span class="mono">{{ picked.length }}</span> of {{ addableCount }} selected</span>
          <button v-if="addableCount" type="button" class="linkish" :disabled="picked.length >= addableCount || busy" @click="selectAddable">Select all {{ addableCount }}</button>
          <span class="keys"><KeyCap k="up" /><KeyCap k="down" /> move · <KeyCap k="enter" /> select</span>
        </p>
        <div class="actions">
          <button type="button" class="btn" @click="close">Cancel</button>
          <button type="button" class="btn primary" :disabled="!picked.length || busy" :aria-busy="busy" @click="add"><AppIcon name="plus" :size="14" />Add selected</button>
        </div>
      </footer>
    </div>
  </dialog>
</template>

<style scoped>
.picker { width: min(640px, calc(100vw - 24px)); max-height: min(760px, calc(100dvh - 24px)); padding: 0; border: 0; background: transparent; color: var(--ink); overflow: visible; }
.picker::backdrop { background: var(--scrim); backdrop-filter: blur(2px); }
.card { display: flex; flex-direction: column; gap: 10px; max-height: min(760px, calc(100dvh - 24px)); padding: 18px 18px 14px; border-radius: var(--radius); border: 1px solid var(--glass-edge); background: linear-gradient(165deg, var(--surface-raised), var(--surface-raised-2)); box-shadow: var(--shadow-pop), var(--shadow); }
header { display: flex; align-items: center; justify-content: space-between; gap: 12px; }
h2 { font-size: 18px; }
.field { height: 36px; }
.filters { display: flex; gap: 8px; }
.filters .field { flex: 1; min-width: 0; }
.options { display: flex; flex-direction: column; align-items: stretch; gap: 2px; min-height: 160px; max-height: 420px; overflow: auto; outline: none; }
.options:focus-visible { box-shadow: var(--focus-ring); border-radius: 10px; }
.option { display: flex; flex-shrink: 0; align-items: center; gap: 8px; min-height: 40px; padding: 4px 8px; border-radius: 10px; cursor: pointer; }
.option.active { background: var(--row-hover); }
.option.picked { background: color-mix(in oklab, var(--teal) 10%, transparent); }
.option[aria-disabled="true"] { cursor: default; color: var(--ink-3); }
.option input { width: 16px; height: 16px; margin: 0; accent-color: var(--teal); flex-shrink: 0; }
.key { flex-shrink: 0; width: 92px; font-size: 11.5px; color: var(--ink-3); }
.copy { display: contents; }
.title { flex: 1; min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; font-size: 14px; }
.title :deep(mark) { background: color-mix(in oklab, var(--gold) 35%, transparent); color: inherit; }
.type, .mark { flex-shrink: 0; font-size: 11.5px; color: var(--ink-3); }
.type { order: 1; }
.mark { order: 2; }
.faint { color: var(--ink-3); }
.empty, .note { padding: 8px 4px; font-size: 13px; color: var(--ink-3); }
.error { color: var(--danger); }
footer { display: flex; flex-wrap: wrap; align-items: center; justify-content: space-between; gap: 10px 12px; }
.hint { display: flex; flex-wrap: wrap; align-items: center; gap: 4px 12px; flex: 1 1 240px; margin: 0; font-size: 12px; color: var(--ink-3); }
.count { color: var(--ink-2); }
.keys { display: inline-flex; align-items: center; gap: 3px; }
.linkish { padding: 0; border: 0; background: transparent; color: var(--teal-ink); font: inherit; font-weight: 600; cursor: pointer; }
.linkish:focus-visible { border-radius: 4px; box-shadow: var(--focus-ring); }
.actions { display: flex; align-items: center; gap: 8px; margin-left: auto; }
.btn { height: 36px; }
@media (max-width: 600px) {
  .picker, .card { width: calc(100vw - 16px); max-height: calc(100dvh - 16px); }
  .filters { display: grid; grid-template-columns: repeat(3, minmax(0, 1fr)); gap: 6px; }
  .filters .field { padding: 0 4px 0 8px; font-size: 13px; text-overflow: ellipsis; }
  footer { flex-direction: column; align-items: stretch; }
  .hint { flex: 0 0 auto; }
  .keys { display: none; }
  .actions { margin-left: 0; }
  .actions .btn { flex: 1 1 0; height: 44px; justify-content: center; }
  .actions .btn.primary { flex-grow: 2; }
  .options { gap: 4px; }
  .option { align-items: flex-start; height: auto; padding: 8px 8px 10px; }
  .key { width: auto; }
  .copy { display: flex; flex: 1 1 auto; flex-direction: column; gap: 2px; min-width: 0; }
  .title { flex: 0 0 auto; white-space: normal; overflow: visible; text-overflow: unset; }
  .type, .mark { order: 0; }
  .mark { white-space: normal; }
  .option input { margin-top: 2px; }
}
</style>
