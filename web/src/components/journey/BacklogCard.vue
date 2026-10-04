<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, ref } from 'vue'
import { isWorkItem, isWorkParent } from '../../lib/workVocabulary'
import { isJourneyLeaf, workParentId } from '../../lib/journeyWork'
import type { ListItem } from '../../lib/api'
import { useJourneyContext } from '../../lib/journeyContext'
import { plural, statusMeta } from '../../lib/work'
import AppIcon from '../AppIcon.vue'
import StatusIcon from '../work/StatusIcon.vue'

// The project's open tickets before any release is open: what release 1 is chosen
// from, grouped by epic in key order, each opening the ticket. Real tickets only.
const ctx = useJourneyContext()
const PER_GROUP = 5
const expanded = ref(new Set<string>())
const keyOrder = (a: ListItem, b: ListItem) => a.key.localeCompare(b.key, undefined, { numeric: true })
const epicOf = workParentId
const work = computed(() => ctx.data.work.value.value)
const open = computed(() => work.value.filter(i => isJourneyLeaf(i) && !statusMeta(i.state).closed))
const groups = computed(() => {
  const epics = work.value.filter(i => isWorkItem(i) && isWorkParent(i)).sort(keyOrder)
  const out = epics.map(epic => ({ id: epic.id, epic: epic as ListItem | null, tickets: open.value.filter(t => epicOf(t) === epic.id).sort(keyOrder) }))
    .filter(g => g.tickets.length)
  const known = new Set(epics.map(e => e.id))
  const loose = open.value.filter(t => { const e = epicOf(t); return !e || !known.has(e) }).sort(keyOrder)
  if (loose.length) out.push({ id: 'loose', epic: null, tickets: loose })
  return out
})
function toggle(id: string) {
  const next = new Set(expanded.value)
  if (next.has(id)) next.delete(id); else next.add(id)
  expanded.value = next
}
</script>

<template>
  <section class="j-card" aria-labelledby="plan-backlog">
    <header class="j-card-head">
      <p id="plan-backlog" class="eyebrow">Backlog · what release 1 is chosen from</p>
      <span class="j-count">{{ plural(open.length, 'open ticket') }}</span>
    </header>
    <p v-if="ctx.data.work.status.value === 'error'" class="j-note" role="alert">{{ ctx.data.work.error.value }}</p>
    <div v-else-if="!open.length" class="j-empty">
      <span class="j-empty-icon"><AppIcon name="layers" :size="16" /></span>
      <strong>The backlog is empty</strong>
      <span>Agreeing requirements generates tickets; a ticket added on the project lands here too.</span>
    </div>
    <template v-else>
      <p class="j-note">Opening release 1 lets you tick the tickets that form it; the rest stay here.</p>
      <div v-for="group in groups" :key="group.id" class="group">
        <p class="group-head">
          <span class="group-title" :title="group.epic?.title">{{ group.epic ? group.epic.title : 'Not tied to an epic' }}</span>
          <span v-if="group.epic" class="mono group-key">{{ group.epic.key }}</span>
          <span class="group-count">{{ group.tickets.length }}</span>
        </p>
        <ul class="j-rows">
          <li v-for="ticket in expanded.has(group.id) ? group.tickets : group.tickets.slice(0, PER_GROUP)" :key="ticket.id">
            <StatusIcon :state="ticket.state" :size="12" />
            <span class="mono key">{{ ticket.key }}</span>
            <button type="button" class="grow row-title" :data-tip="`Open ${ticket.key}\n${ticket.title}`" @click="ctx.open(ticket.key)">{{ ticket.title }}</button>
          </li>
        </ul>
        <button v-if="group.tickets.length > PER_GROUP" type="button" class="btn sm ghost more" :aria-expanded="expanded.has(group.id)" @click="toggle(group.id)">
          {{ expanded.has(group.id) ? 'Show fewer' : `Show ${group.tickets.length - PER_GROUP} more` }}
        </button>
      </div>
    </template>
  </section>
</template>

<style scoped>
.group { display: grid; grid-template-columns: minmax(0, 1fr); gap: 4px; min-width: 0; }
.group + .group { margin-top: 4px; }
.group-head { display: flex; align-items: baseline; gap: 8px; min-width: 0; padding: 0 6px; font-size: 12.5px; }
.group-title { min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; font-weight: 600; color: var(--ink); }
.group-key { flex-shrink: 0; font-size: 10.5px; color: var(--ink-3); font-variant-ligatures: none; }
.group-count { margin-left: auto; flex-shrink: 0; font-size: 12px; color: var(--ink-3); font-variant-numeric: tabular-nums; }
/* Bound the list's track as well as its flex rows: an implicit auto track
   otherwise grows to the title's intrinsic width, including the separators. */
.j-rows { grid-template-columns: minmax(0, 1fr); min-width: 0; }
.j-rows > li { min-width: 0; }
.key { flex-shrink: 0; width: 76px; }
.row-title { align-self: stretch; padding: 0; border: 0; background: transparent; color: var(--ink); font: inherit; text-align: left; cursor: pointer; }
.row-title:hover { color: var(--teal-ink); text-decoration: underline; }
.row-title:focus-visible { border-radius: 4px; box-shadow: var(--focus-ring); }
.more { justify-self: start; }
/* Phones: titles wrap in full; nothing ends in a cut. */
@media (max-width: 600px) {
  .key { width: 64px; }
  .j-rows > li { align-items: flex-start; min-height: 44px; padding-top: 10px; padding-bottom: 10px; }
  .j-rows > li > :first-child { margin-top: 3px; }
  .row-title { white-space: normal; overflow-wrap: anywhere; line-height: 1.4; }
}
</style>
