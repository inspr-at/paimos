<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { useSession } from '../../stores/session'
import { readQuotaWarnings, type QuotaWarningSession } from '../../lib/quotaWarnings'
import { usePolledData, usePoller } from '../../lib/usePolledData'
import type { SessionView } from '../../stores/agents'
const props = defineProps<{ sessions: SessionView[] }>()
const session = useSession()
const now = ref(Date.now())
const reads = usePolledData(readQuotaWarnings, { items: [] as QuotaWarningSession[], next_after: null as string | null })
// A failed refresh immediately withholds prior quota detail, including after a
// sharing change. Do not display a retained private snapshot as current advice.
const rows = computed(() => reads.stale.value || reads.status.value.state !== 'ready' || now.value - (reads.status.value.updatedAt ?? 0) > 30_000 ? [] : reads.data.value.items)
const names = computed(() => new Map(props.sessions.map(s => [s.session.id, s.name])))
const visible = computed(() => rows.value.filter(warning => names.value.has(warning.session_id)))
const poller = usePoller(async () => { now.value = Date.now(); await reads.refresh() }, 20_000, { invalidate: reads.invalidate })
watch(() => `${session.identity?.tenant.id}/${session.identity?.principal.id}`, () => { reads.invalidate(); reads.data.value = { items: [], next_after: null }; poller.restart() })
onMounted(() => poller.start(true))
onBeforeUnmount(poller.stop)
</script>
<template>
  <section v-if="visible.length || reads.stale.value || reads.status.value.state === 'error'" class="quota-notices" aria-label="Account availability notices">
    <h2>Account availability</h2>
    <ul>
      <li v-for="warning in visible" :key="warning.session_id">
        <RouterLink :to="`/agents/${warning.session_id}`">{{ names.get(warning.session_id) }}</RouterLink>
        <span v-if="warning.details_redacted">Limited availability</span>
        <span v-else>{{ warning.severity === 'urgent' ? 'Urgent notice' : 'Early notice' }} · {{ warning.remaining_percent?.toLocaleString(undefined, { maximumFractionDigits: 1 }) }}% remaining</span>
      </li>
    </ul>
    <p v-if="reads.data.value.next_after">More affected sessions are outside this page.</p>
    <p v-if="reads.stale.value || reads.status.value.state === 'error'" role="status">Account availability could not be refreshed.</p>
  </section>
</template>
<style scoped>
.quota-notices { border: 1px solid var(--line); padding: 12px 16px; background: var(--surface); }
h2 { font-size: 13px; font-weight: 600; margin-bottom: 8px; }
ul { list-style: none; padding: 0; margin: 0; }
li { display: flex; flex-wrap: wrap; gap: 4px 12px; padding: 4px 0; font-size: 13px; }
li span, p { color: var(--ink-2); font-size: 12px; }
a { color: var(--ink); }
</style>
