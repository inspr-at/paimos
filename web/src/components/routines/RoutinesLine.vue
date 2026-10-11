<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { listRoutines } from '../../lib/api'
import { onAccessChange, permissionsRevoked } from '../../lib/authz'
import { recurrenceZone, when, type Recurrence } from '../../lib/recurrences'
import { routinesLine } from '../../lib/routineRuns'
import { useIdentityScope } from '../../lib/useIdentityScope'
import { usePoller } from '../../lib/usePolledData'
import { useSession } from '../../stores/session'
import AppIcon from '../AppIcon.vue'

// The Agents page's Routines line (AEON-1192): one line, only while a routine waits
// for a person or failed, leading to Agents › Routines. One bounded page is read; a
// remaining cursor reads as "at least". A failed read shows nothing, never zero. Every
// person asks; the server answers from their workspace or project access.
const session = useSession()
const identity = useIdentityScope(() => !!session.identity && session.identity.principal.kind !== 'agent'), reads = identity.lane()
const items = ref<Recurrence[]>([]), more = ref(false)
function load() {
  void reads.run(({ after, signal }) => after(listRoutines({}, signal), page => {
    if (!Array.isArray(page?.items)) throw new Error('The routines answer was not a list.')
    items.value = page.items; more.value = !!page.next_cursor
  }), { failed: () => { items.value = []; more.value = false } })
}
// Another person never sees the previous person's line, not even for a moment.
watch(() => identity.owner.value, () => { items.value = []; more.value = false; load() }, { immediate: true, flush: 'sync' })
// A permission reset drops the read in flight with the scope (its listener runs first): read again.
const stopAccess = onAccessChange(change => { if (change === 'reset' && !permissionsRevoked()) { items.value = []; more.value = false; load() } })
const poller = usePoller(load, 60_000)
onMounted(() => poller.start())
onBeforeUnmount(() => { stopAccess(); poller.stop() })
const words = computed(() => identity.owner.value ? routinesLine(items.value, more.value, item => when(item.next_at!, recurrenceZone(item.trigger))) : null)
</script>

<template>
  <RouterLink v-if="words" class="rt-line glass-card" to="/agents/routines"><span class="glyph" aria-hidden="true"><AppIcon name="repeat" :size="14" /></span><span class="rl-t"><b>Routines</b><span>{{ words }}</span></span><AppIcon class="chev" name="chevron-right" :size="14" /></RouterLink>
</template>

<style scoped>
.rt-line { display: grid; grid-template-columns: 32px minmax(0, 1fr) 14px; align-items: center; gap: 12px; width: 100%; min-height: 64px; padding: 10px 18px; color: var(--ink); text-align: left; text-decoration: none; }
@media (hover: hover) { .rt-line:hover { background: var(--row-hover); } }
.rt-line:focus-visible { outline: none; box-shadow: var(--focus-ring); }
.glyph { display: grid; place-items: center; width: 32px; height: 32px; border-radius: 10px; color: var(--marker-on); background: var(--marker); }
.rl-t { display: grid; gap: 2px; min-width: 0; }
.rl-t b { font-size: 14px; font-weight: 650; }
.rl-t > span { font-size: 12.5px; color: var(--ink-2); overflow-wrap: anywhere; }
.chev { color: var(--ink-3); }
</style>
