<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { attachOutcome, listPendingAttach, metadataOnlyAttach, type AttachOutcome, type AttachReview } from '../../lib/attachWatch'
import { attachCopy } from '../../lib/attachCopy'
import { can, onAccessChange } from '../../lib/authz'
import { duration } from '../../lib/agentState'
import { HARNESS_NAME } from '../../lib/capacity'
import { useIdentityScope } from '../../lib/useIdentityScope'
import { usePoller } from '../../lib/usePolledData'
import { useSession } from '../../stores/session'
import AppIcon from '../AppIcon.vue'
import HarnessMark from './HarnessMark.vue'

// A started attach is waiting for its owner: say so on /agents, where they look
// for it, with one click to the review. The list never carries the code; approval
// keeps the review, both digests and the same-origin decision it always had.
const props = defineProps<{ now: number }>()
const emit = defineEmits<{ review: [request: AttachReview] }>()

const session = useSession()
const copy = attachCopy()
const allowed = computed(() => session.identity?.principal.kind === 'person' && can('account.manage'))
// Whose list this is. Every read runs in the identity scope (one person, one
// workspace, one generation): an answer for anyone else is dropped, never shown.
const scope = useIdentityScope(() => allowed.value)
const owner = scope.owner
const reads = scope.lane()
// The list carries its owner, so even a row that outlived a reset could not be shown to the next person.
const held = ref<{ owner: string; list: AttachReview[] }>({ owner: '', list: [] })
const items = computed(() => owner.value && held.value.owner === owner.value ? held.value.list : [])
const dismissed = ref(new Set<string>())
function reset() {
  scope.reset()
  held.value = { owner: '', list: [] }; dismissed.value = new Set()
}
function refresh() {
  if (!owner.value) { reset(); return }
  return reads.run(({ after, signal }) => {
    const asked = owner.value
    return after(listPendingAttach(signal), list => { held.value = { owner: asked, list } })
  }, { failed: () => { /* a missed read keeps what is shown; the next tick asks again */ } })
}
const poller = usePoller(refresh, 5_000, { enabled: () => allowed.value })
const stopAccess = onAccessChange(change => { if (change === 'reset') reset(); void refresh() })
onMounted(() => poller.start(true))
onBeforeUnmount(() => { poller.stop(); stopAccess(); reset() })
watch(owner, () => { reset(); void refresh() })
defineExpose({ refresh })

interface Row { review: AttachReview; outcome: AttachOutcome; what: string; detail: string; left: string; soon: boolean }
const RANK: Record<AttachOutcome, number> = { waiting: 0, approved: 1, expired: 2, cancelled: 2 }
const rows = computed<Row[]>(() => {
  const all = items.value.flatMap(review => {
    const outcome = attachOutcome(review, props.now)
    if (!outcome || dismissed.value.has(review.request_id)) return []
    const host = review.snapshot.host
    const harness = HARNESS_NAME[review.snapshot.harness] ?? review.snapshot.harness
    const left = Date.parse(review.expires_at) - props.now
    const detail = {
      waiting: metadataOnlyAttach(review.snapshot) ? 'Wants to attach, status only' : 'Wants to watch the conversation',
      approved: review.consent_mode === 'local_auth' ? 'Approved. Confirm with Touch ID on that Mac.' : 'Approved. Connecting…',
      expired: 'Request expired. Start a new attach in the terminal.',
      cancelled: 'Request cancelled. Start a new attach in the terminal.',
    }[outcome]
    return [{ review, outcome, what: `${harness} on ${host}`, detail, left: outcome === 'waiting' ? `Expires in ${duration(left)}` : '', soon: outcome === 'waiting' && left < 2 * 60_000 }]
  })
  // Newest first from the server; what needs a decision leads, and two ended requests are plenty.
  const ordered = all.map((row, index) => ({ row, index })).sort((a, b) => RANK[a.row.outcome] - RANK[b.row.outcome] || a.index - b.index).map(entry => entry.row)
  return [...ordered.filter(row => RANK[row.outcome] < 2), ...ordered.filter(row => RANK[row.outcome] === 2).slice(0, 2)]
})
const firstWaiting = computed(() => rows.value.findIndex(row => row.outcome === 'waiting'))
</script>

<template>
  <div v-if="allowed && rows.length" class="attach-pending" :class="{ quiet: firstWaiting === -1 }" role="group" aria-label="Attach requests">
    <ul>
      <li v-for="(row, index) in rows" :key="row.review.request_id" class="row" :class="row.outcome" :data-outcome="row.outcome">
        <span class="mark" aria-hidden="true">
          <HarnessMark v-if="row.outcome === 'waiting' || row.outcome === 'approved'" :harness="row.review.snapshot.harness" :size="16" />
          <AppIcon v-else name="clock" :size="15" />
        </span>
        <p class="text">
          <strong class="what" :title="row.what">{{ row.what }}</strong>
          <span v-if="row.outcome === 'waiting' || row.outcome === 'approved'" class="detail">{{ copy.computerPaired }} · {{ copy.sessionUnlinked }}</span>
          <span class="detail">{{ row.detail }}</span>
        </p>
        <div v-if="row.outcome !== 'approved'" class="side">
          <time v-if="row.left" class="left" :class="{ soon: row.soon }" :datetime="row.review.expires_at" :title="new Date(row.review.expires_at).toLocaleString()">{{ row.left }}</time>
          <button v-if="row.outcome === 'waiting'" type="button" class="btn sm" :class="{ primary: index === firstWaiting }" @click="emit('review', row.review)">Review</button>
          <button v-else-if="row.outcome === 'expired' || row.outcome === 'cancelled'" type="button" class="dismiss" aria-label="Dismiss this attach request" @click="dismissed = new Set(dismissed).add(row.review.request_id)"><AppIcon name="close" :size="14" /></button>
        </div>
      </li>
    </ul>
  </div>
</template>

<style scoped>
/* Something waits: the warm full tint and soft ring "Needs you" uses, never an edge bar.
   With nothing to decide it drops to a plain hairline card. */
.attach-pending { padding: 6px; border-radius: 16px; background: linear-gradient(165deg, color-mix(in srgb, var(--gold-2) 16%, var(--surface-raised-2)), color-mix(in srgb, var(--gold-2) 10%, var(--glass)) 60%); box-shadow: var(--shadow), 0 0 0 1px color-mix(in srgb, var(--gold) 38%, transparent); }
.attach-pending.quiet { background: var(--surface-raised); box-shadow: 0 0 0 1px var(--line); }
ul { margin: 0; padding: 0; list-style: none; display: grid; gap: 2px; }
.row { display: grid; grid-template-columns: 30px minmax(0, 1fr) auto; align-items: center; gap: 12px; padding: 8px 8px 8px 10px; border-radius: 12px; }
.mark { display: grid; place-items: center; width: 30px; height: 30px; border-radius: 9px; background: var(--chip-teal-bg); box-shadow: inset 0 0 0 1px var(--chip-teal-line); color: var(--teal-ink); }
.row.expired .mark, .row.cancelled .mark { background: var(--chip-bg); box-shadow: inset 0 0 0 1px var(--chip-line); color: var(--ink-3); }
.text { display: grid; gap: 1px; min-width: 0; margin: 0; }
.what { min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; font-size: 14px; font-weight: 650; color: var(--ink); }
.row.expired .what, .row.cancelled .what { color: var(--ink-2); font-weight: 600; }
.detail { font-size: 12.5px; line-height: 1.4; color: var(--ink-2); }
.row.expired .detail, .row.cancelled .detail { color: var(--ink-3); }
.side { display: inline-flex; align-items: center; gap: 12px; }
.left { font-size: 12px; color: var(--ink-3); white-space: nowrap; font-variant-numeric: tabular-nums; }
.left.soon { color: var(--gold-ink); font-weight: 600; }
.btn { display: inline-flex; align-items: center; justify-content: center; }
.dismiss { display: inline-flex; align-items: center; justify-content: center; width: 28px; height: 28px; padding: 0; border: 0; border-radius: 8px; background: transparent; color: var(--ink-3); cursor: pointer; }
@media (hover: hover) { .dismiss:hover { background: var(--row-hover); color: var(--ink); } }
.dismiss:focus-visible { outline: none; box-shadow: var(--focus-ring); }
/* Phones: a waiting request gives its text the full row; the expiry and the action sit under it. */
@media (max-width: 520px) {
  .row.waiting { grid-template-columns: 30px minmax(0, 1fr); align-items: start; }
  .row.waiting .side { grid-column: 2; justify-content: space-between; }
}
</style>
