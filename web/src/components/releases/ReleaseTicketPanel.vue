<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, ref, watch } from 'vue'
import { listNodes, type ListItem } from '../../lib/api'
import { can, onAccessChange } from '../../lib/authz'
import { confirmAction } from '../../lib/confirm'
import { EMPTY_FILTERS, WORK_KINDS } from '../../lib/ticketList'
import { normalKey, resolveTicketKeys, ticketRef } from '../../lib/ticketLinks'
import { useTicketList } from '../../lib/useTicketList'
import { useProjects } from '../../stores/projects'
import { useSession } from '../../stores/session'
import StatusMenu from '../work/StatusMenu.vue'
import TicketWorkspace from '../work/TicketWorkspace.vue'

// A ticket named in the release history, open beside it: the app's own ticket
// side panel (same parts, same writes), resolved from the key. Links followed
// inside the panel stay in it, with the panel's back trail.
const props = withDefaults(defineProps<{
  ticketKey: string; now: number
  // inline sits in the release history's grid; dock is the app's fixed side panel.
  layout?: 'inline' | 'dock'; openInProject?: boolean; backLabel?: string
}>(), { layout: 'inline', openInProject: false, backLabel: '' })
const emit = defineEmits<{ close: []; navigate: [path: string] }>()
const projects = useProjects()
const session = useSession()

const current = ref(normalKey(props.ticketKey))
const trail = ref<string[]>([])
watch(() => props.ticketKey, key => { current.value = normalKey(key); trail.value = [] })

const ref_ = computed(() => ticketRef(current.value))
const projectId = computed(() => ref_.value?.projectId ?? null)
const project = computed(() => projectId.value ? projects.byId(projectId.value) : undefined)
const list = useTicketList(projectId, ref(EMPTY_FILTERS))

// ---------- The ticket ----------
// Each resolution has a generation: an access or identity change starts a new
// one, and an answer from an older one never shows (it was asked as someone else).
const item = ref<ListItem | null>(null)
const resolving = ref(false)
const error = ref('')
let generation = 0
async function resolve() {
  const key = current.value
  const request = ++generation
  const stale = () => request !== generation
  item.value = null
  error.value = ''
  resolving.value = true
  try {
    if (ticketRef(key) === undefined) await resolveTicketKeys([key])
    if (stale()) return
    const found = ticketRef(key)
    if (!found) throw new Error(`${key} is not a ticket in this workspace.`)
    await projects.load()
    if (stale()) return
    const page = await listNodes({ within: found.projectId, q: found.key, sort: 'key', limit: 50 })
    if (stale()) return
    const hit = page.items.find(row => row.key === found.key)
    if (!hit) throw new Error(`${found.key} could not be found.`)
    item.value = hit
  } catch (e) {
    if (!stale()) error.value = e instanceof Error ? e.message : 'The ticket could not be loaded.'
  } finally {
    if (!stale()) resolving.value = false
  }
}
watch(current, resolve, { immediate: true })
// Another person, workspace or an ended session: whatever is shown or on its way
// goes, and the ticket is resolved again as the new caller. The same person's
// access asked again only restarts a resolution still on its way; a shown ticket
// stays until the keys asked again say otherwise (below).
const stopAccess = onAccessChange(change => { if (change === 'reset' || resolving.value) void resolve() })
onBeforeUnmount(stopAccess)
// The key asked again answers differently (a lost project, a moved ticket).
// Its own first answer (unknown to known) and a reset (handled above) do not count.
watch(() => { const found = ticketRef(current.value); return found === undefined ? undefined : found?.id ?? null }, (now, before) => {
  if (before !== undefined && now !== undefined && now !== before) void resolve()
})

// ---------- Who may do what, and to whom ----------
const scope = computed(() => projectId.value ?? undefined)
const me = computed(() => session.identity ? { id: session.identity.principal.id, name: session.identity.principal.name } : null)
const people = ref<{ id: string; name: string }[]>([])
// The states the project's work uses, as the project list offers them (a workspace
// spelling in-progress keeps it in the status menu).
const projectStates = ref<string[]>([])
const knownStates = computed(() => [...new Set([...projectStates.value, ...(item.value ? [item.value.state] : [])])])
watch(projectId, async id => {
  people.value = []
  projectStates.value = []
  if (!id) return
  try {
    const page = await listNodes({ within: id, kind: WORK_KINDS, facets: ['assignee', 'state'], limit: 1 })
    if (projectId.value !== id) return
    projectStates.value = Object.keys(page.facets?.state ?? {})
    const ids = Object.keys(page.facets?.assignee ?? {}).filter(value => value !== 'none')
    await list.resolveNames(ids)
    if (projectId.value === id) people.value = ids.map(person => ({ id: person, name: list.names.get(person) ?? 'Someone' }))
  } catch { /* the menus still offer you, Unassigned and the usual states */ }
}, { immediate: true })

// ---------- Actions ----------
const href = (key: string) => {
  const found = ticketRef(key)
  const owner = found ? projects.byId(found.projectId) : undefined
  return found && owner ? `/p/${encodeURIComponent(owner.routeKey)}/${encodeURIComponent(found.key)}` : ''
}
async function openKey(key: string, newTab: boolean) {
  const wanted = normalKey(key)
  if (newTab) {
    if (ticketRef(wanted) === undefined) await resolveTicketKeys([wanted])
    const path = href(wanted)
    if (path) window.open(path, '_blank', 'noopener')
    return
  }
  trail.value = [...trail.value, current.value]
  current.value = wanted
}
function trailBack(steps: number) {
  const at = Math.max(0, trail.value.length - steps)
  const key = trail.value[at]
  if (!key) return
  trail.value = trail.value.slice(0, at)
  current.value = key
}
function expand() { const path = href(current.value); if (path) emit('navigate', `${path}?view=full`) }
async function goToProject() {
  if (ws.value?.isDirty() && !(await confirmAction({ title: 'Discard your changes?', body: `Your edits to ${current.value} have not been saved.`, confirmLabel: 'Discard', danger: true }))) return
  const path = href(current.value)
  if (path) emit('navigate', path)
}
function newTab() { const path = href(current.value); if (path) window.open(path, '_blank', 'noopener') }

const statusAnchor = ref<HTMLElement | null>(null)
function chooseStatus(state: string) {
  const anchor = statusAnchor.value
  statusAnchor.value = null
  anchor?.focus()
  if (item.value) void list.setStatus(item.value, state).then(changed => { if (changed) void projects.load(true) })
}
function closeStatus(restore: boolean) { const anchor = statusAnchor.value; statusAnchor.value = null; if (restore) anchor?.focus() }

// Closing with unsaved text asks first, like leaving the ticket elsewhere.
const ws = ref<InstanceType<typeof TicketWorkspace>>()
async function requestClose() {
  if (ws.value?.isDirty() && !(await confirmAction({ title: 'Discard your changes?', body: `Your edits to ${current.value} have not been saved.`, confirmLabel: 'Discard', danger: true }))) return
  emit('close')
}
defineExpose({
  requestClose,
  focus: async () => { await nextTick(); ws.value?.focus() },
  startEdit: () => ws.value?.startEdit(),
  openStatus: () => ws.value?.openStatus(),
  openPriority: () => ws.value?.openPriority(),
  openAssignee: () => ws.value?.openAssignee(),
  openLink: () => ws.value?.openLink(),
  focusComposer: () => ws.value?.focusComposer(),
})
</script>

<template>
  <div class="peek" :class="layout">
    <TicketWorkspace
      ref="ws" :item="item" :ticket-key="item?.key ?? ref_?.key ?? current" :resolving="resolving" :resolve-error="error" :position="null" :now="now" mode="panel"
      :project="{ id: projectId ?? '', routeKey: project?.routeKey ?? '' }" :names="list.names" :me="me" :people="people" :trail="trail"
      :can-write="can('nodes.write', scope)" :can-delete="can('nodes.delete', scope)" :can-move="can('nodes.move', scope)"
      :can-link="can('relations.write', scope)" :can-unlink="can('relations.delete', scope)" :can-comment="can('comments.write', scope)"
      :can-delete-comment="can('comments.delete', scope)" :can-attach="can('attachments.write', scope) && can('attachments.delete', scope)"
      :open-in-project="openInProject" :back-label="backLabel"
      @close="requestClose" @expand="expand" @new-tab="newTab" @open-key="openKey" @trail-back="trailBack" @retry="resolve"
      @open-in-project="goToProject" @status="anchor => { statusAnchor = anchor }" @removed="emit('close')"
    />
    <StatusMenu :project-id="projectId ?? undefined" v-if="statusAnchor && item" :anchor="statusAnchor" :current="item.state" :known-states="knownStates" :ticket-key="item.key" @choose="chooseStatus" @close="closeStatus" />
  </div>
</template>

<style scoped>
/* The app's side panel, placed in the history's grid instead of floating over the page. */
.peek { display: flex; min-width: 0; min-height: 0; }
.peek.inline { padding: 2px 0 16px; }
.peek.inline :deep(.ticket-ws.panel) { position: relative; inset: auto; z-index: auto; flex: 1; width: auto; height: auto; min-width: 0; }
/* No list to step through here: no previous and next arrows either. */
.peek :deep(.panel-bar .nav) { display: none; }
@media (max-width: 760px) {
  /* Phones: the ticket takes the whole screen, over the history. The app dock uses the panel's own sheet. */
  .peek.inline { position: fixed; inset: 0; z-index: 10; padding: 0; background: var(--canvas); }
  .peek.inline :deep(.ticket-ws.panel) { border: 0; border-radius: 0; background: var(--canvas); box-shadow: none; }
}
</style>
