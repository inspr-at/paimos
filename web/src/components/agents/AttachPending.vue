<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { getNode } from '../../lib/api'
import { attachOutcome, listPendingAttach, metadataOnlyAttach, type AttachOutcome, type AttachQueueRow, type AttachReview } from '../../lib/attachWatch'
import { can, onAccessChange } from '../../lib/authz'
import { duration } from '../../lib/agentState'
import { HARNESS_NAME } from '../../lib/capacity'
import { useIdentityScope } from '../../lib/useIdentityScope'
import { usePoller } from '../../lib/usePolledData'
import { useSession } from '../../stores/session'

// A started attach is waiting for its owner: say so on /agents, where they look
// for it, with one click to the review. The list never carries the code; approval
// keeps the review, both digests and the same-origin decision it always had.
const props = defineProps<{ now: number }>()
const emit = defineEmits<{ count: [count: number]; rows: [rows: AttachQueueRow[]]; history: [rows: AttachQueueRow[]]; updated: [requests: AttachReview[]] }>()

const session = useSession()
const allowed = computed(() => session.identity?.principal.kind === 'person' && can('account.manage'))
// Whose list this is. Every read runs in the identity scope (one person, one
// workspace, one generation): an answer for anyone else is dropped, never shown.
const scope = useIdentityScope(() => allowed.value)
const owner = scope.owner
const reads = scope.lane()
const labelReads = scope.lane()
const tickets = ref<Record<string, { key: string; title: string }>>({})
const declined = ref(new Set<string>())
const history = ref<AttachQueueRow[]>([])
const endedAt = new Map<string, number>()
// The list carries its owner, so even a row that outlived a reset could not be shown to the next person.
const held = ref<{ owner: string; list: AttachReview[] }>({ owner: '', list: [] })
const items = computed(() => owner.value && held.value.owner === owner.value ? held.value.list : [])
const dismissed = ref(new Set<string>())
function reset() {
  scope.reset()
  held.value = { owner: '', list: [] }; dismissed.value = new Set(); tickets.value = {}; declined.value = new Set(); history.value = []; endedAt.clear()
}
function refresh() {
  if (!owner.value) { reset(); return }
  return reads.run(({ after, signal }) => {
    const asked = owner.value
    return after(listPendingAttach(signal), list => {
      // Once the Mac activates a request it leaves this endpoint. Preserve the
      // confirmed Allow in Decided without inferring why other requests vanished.
      for (const row of rows.value) {
        if (row.outcome === 'approved' && !list.some(item => item.request_id === row.review.request_id)) archive(row)
      }
      held.value = { owner: asked, list }; emit('updated', list)
      const ids = [...new Set(list.map(item => item.snapshot.ticket_id))].filter(id => !tickets.value[id])
      if (ids.length) void labelReads.run(({ after }) => after(Promise.allSettled(ids.map(getNode)), names => {
        names.forEach((name, i) => { if (name.status === 'fulfilled') tickets.value[ids[i]!] = { key: name.value.key, title: name.value.title } })
      }))
    })
  }, { failed: () => { /* a missed read keeps what is shown; the next tick asks again */ } })
}
const poller = usePoller(refresh, 5_000, { enabled: () => allowed.value })
const stopAccess = onAccessChange(change => { if (change === 'reset') reset(); void refresh() })
onMounted(() => poller.start(true))
onBeforeUnmount(() => { poller.stop(); stopAccess(); reset() })
watch(owner, () => { reset(); void refresh() })
function settle(result: AttachReview, wasDeclined: boolean) {
  if (!owner.value) return
  if (wasDeclined) declined.value = new Set(declined.value).add(result.request_id)
  const current = items.value
  held.value = { owner: owner.value, list: current.some(item => item.request_id === result.request_id) ? current.map(item => item.request_id === result.request_id ? result : item) : [result, ...current] }
  void refresh()
}
function archive(row: AttachQueueRow) {
  history.value = [row, ...history.value.filter(item => item.review.request_id !== row.review.request_id)].slice(0, 20)
}
function dismiss(id: string) {
  const row = rows.value.find(item => item.review.request_id === id)
  if (row) archive(row)
  dismissed.value = new Set(dismissed.value).add(id)
}
defineExpose({ refresh, settle, dismiss })

const RANK: Record<AttachOutcome, number> = { waiting: 0, approved: 1, expired: 2, cancelled: 2 }
const rows = computed<AttachQueueRow[]>(() => {
  const all = items.value.flatMap<AttachQueueRow>(review => {
    const observed = attachOutcome(review, props.now)
    const outcome = observed === 'cancelled' && declined.value.has(review.request_id) ? 'declined' : observed
    if (!outcome || dismissed.value.has(review.request_id)) return []
    const host = review.snapshot.host
    const harness = HARNESS_NAME[review.snapshot.harness] ?? review.snapshot.harness
    const left = Date.parse(review.expires_at) - props.now
    const detail = {
      waiting: metadataOnlyAttach(review.snapshot) ? 'Wants to attach, status only' : 'Wants to watch the conversation',
      approved: review.consent_mode === 'local_auth' ? `Confirm with Touch ID on ${host}. Nothing is shared until you do.` : 'Allowed. Connecting…',
      expired: 'Request expired. Run aeon-agentd attach again.',
      cancelled: 'Request cancelled. Stopped in the terminal or declined in another tab. Nothing was shared.',
      declined: 'You declined it. Nothing was shared.',
    }[outcome]
    return [{ review, outcome, what: `${harness} on ${host}`, detail, ticket: tickets.value[review.snapshot.ticket_id] ?? { key: review.snapshot.ticket_id, title: '' }, left: outcome === 'waiting' ? `Expires in ${duration(left)}` : '', soon: outcome === 'waiting' && left < 2 * 60_000 }]
  })
  // Newest first from the server; what needs a decision leads, and two ended requests are plenty.
  const ordered = all.map((row, index) => ({ row, index })).sort((a, b) => RANK[a.row.outcome === 'declined' ? 'cancelled' : a.row.outcome] - RANK[b.row.outcome === 'declined' ? 'cancelled' : b.row.outcome] || Date.parse(a.row.review.expires_at) - Date.parse(b.row.review.expires_at) || a.index - b.index).map(entry => entry.row)
  return [...ordered.filter(row => RANK[row.outcome === 'declined' ? 'cancelled' : row.outcome] < 2), ...ordered.filter(row => RANK[row.outcome === 'declined' ? 'cancelled' : row.outcome] === 2).slice(0, 2)]
})
watch(rows, rows => {
  for (const row of rows) if (row.outcome !== 'waiting' && row.outcome !== 'approved' && !endedAt.has(row.review.request_id)) endedAt.set(row.review.request_id, props.now)
  emit('rows', rows)
}, { immediate: true })
watch(() => props.now, now => {
  for (const [id, at] of endedAt) if (now - at >= 6_000) { dismiss(id); endedAt.delete(id) }
})
watch(history, rows => emit('history', rows), { immediate: true })
watch(() => rows.value.filter(row => row.outcome === 'waiting').length, count => emit('count', count), { immediate: true })
</script>

<template><slot :rows="rows" /></template>
