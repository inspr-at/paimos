<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { HARNESS_NAME, LOGIN_COMMAND, type AccountRow } from '../../lib/capacity'
import type { AttachQueueRow, AttachReview } from '../../lib/attachWatch'
import AppIcon from '../AppIcon.vue'
import { useSession } from '../../stores/session'

defineProps<{ signins: AccountRow[]; attaches: AttachQueueRow[]; history: AttachQueueRow[] }>()
const emit = defineEmits<{ review: [review: AttachReview]; dismiss: [id: string] }>()
const status = ref('')
const session = useSession(), owner = computed(() => `${session.identity?.tenant.id ?? ''}:${session.identity?.principal.id ?? ''}`)
watch(owner, () => { status.value = '' }, { flush: 'sync' })
async function copy(row: AccountRow) {
  const identity = owner.value
  try { await navigator.clipboard.writeText(LOGIN_COMMAND[row.harness]!); if (identity === owner.value) status.value = 'Sign-in command copied.' }
  catch { if (identity === owner.value) status.value = 'The command could not be copied. Select the command to copy it.' }
}
</script>
<template>
  <section class="chores" aria-labelledby="chores-title">
    <h2 id="chores-title">Sign-ins and connections</h2>
    <ul>
      <li v-for="row in signins" :key="row.id">
        <button v-if="LOGIN_COMMAND[row.harness]" type="button" class="btn sm" @click="copy(row)"><AppIcon name="copy" :size="13" />Copy command</button>
        <div><strong>{{ HARNESS_NAME[row.harness] ?? row.harness }} needs a new sign-in on {{ row.host }}</strong><p><code v-if="LOGIN_COMMAND[row.harness]">{{ LOGIN_COMMAND[row.harness] }}</code> · agents skip this account until then</p></div>
      </li>
      <li v-for="row in attaches" :key="row.review.request_id">
        <button v-if="row.outcome === 'waiting' || row.outcome === 'approved'" type="button" class="btn sm" @click="emit('review', row.review)">Review connection</button>
        <button v-else type="button" class="btn sm" @click="emit('dismiss', row.review.request_id)">Dismiss</button>
        <div><strong>{{ row.what }}</strong><p>{{ row.ticket.key }} · {{ row.detail }}</p></div>
      </li>
    </ul>
    <details v-if="history.length"><summary><AppIcon name="chevron-right" :size="12" />Connection history · {{ history.length }}</summary><ul><li v-for="row in history" :key="row.review.request_id">{{ row.what }} · {{ row.ticket.key }} · {{ row.outcome }}</li></ul></details>
    <p class="chore-status" role="status">{{ status }}</p>
  </section>
</template>
<style scoped>
.chores { border-top: 1px solid var(--line); padding-top: 16px; }h2 { font-size: 14px; margin: 0; font-weight: 600; }.chore-status { min-height: 1.4em; font-size: 12px; color: var(--ink-3); margin: 8px 0; }ul { list-style: none; padding: 0; margin: 0; }li { display: grid; grid-template-columns: max-content minmax(0,1fr); gap: 12px; align-items: start; padding: 12px 0; border-bottom: 1px solid var(--line); }strong { font-size: 13px; }p, summary { font-size: 12px; color: var(--ink-3); }p { margin: 6px 0 0; overflow-wrap: anywhere; }code { user-select: all; }details { margin-top: 12px; }@media (max-width: 720px) { li { grid-template-columns: minmax(0,1fr); }.btn { justify-self: start; min-height: 44px; } }
</style>
