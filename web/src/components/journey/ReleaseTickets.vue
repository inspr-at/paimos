<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import type { ListItem } from '../../lib/api'
import { hours, nextPickLabel, type TicketGroup, type WalkerTicket } from '../../lib/journey'
import type { Plan } from '../../lib/usePlan'
import AppIcon from '../AppIcon.vue'
import StatusIcon from '../work/StatusIcon.vue'
import FeatureCheck from './FeatureCheck.vue'

// A release's tickets grouped by feature: the feature line (tri-state box, name,
// epic key on the right that opens the epic in a new window), then its tickets.
// While planning, ticked tickets form the release; otherwise each shows its state.
const props = defineProps<{
  plan: Plan; editable: boolean; projectKey: string; workById: Map<string, ListItem>; showBacklog?: boolean
  only?: 'included' | 'backlog'; compact?: boolean
}>()
const emit = defineEmits<{ walk: [ticket: WalkerTicket]; open: [key: string] }>()
const visible = (group: TicketGroup) => props.only === 'included' ? group.tickets.filter(t => props.plan.included.value.has(t.ticket_node_id))
  : props.only === 'backlog' ? group.tickets.filter(t => !props.plan.included.value.has(t.ticket_node_id)) : group.tickets
const epicHref = (key: string) => `/p/${encodeURIComponent(props.projectKey)}/${encodeURIComponent(key)}`
function featureNumber(group: TicketGroup) { return props.plan.groups.value.filter(g => g.feature).indexOf(group) + 1 }
const stateOf = (ticket: WalkerTicket) => props.workById.get(ticket.ticket_node_id)?.state ?? ''
</script>

<template>
  <div class="release-tickets" :class="{ compact }">
    <section v-for="group in plan.groups.value" v-show="visible(group).length || (!only && group.feature)" :key="group.id || 'loose'" class="grp" :aria-label="group.feature ? `Feature ${group.feature.title}` : 'Tickets without a feature'">
      <header class="grp-h">
        <FeatureCheck
          v-if="group.feature && editable && group.tickets.length" :state="plan.selection(group)" :label="group.feature.title"
          :included="group.tickets.filter(t => plan.included.value.has(t.ticket_node_id)).length" :total="group.tickets.length"
          :next="nextPickLabel(plan.selection(group), plan.memory.value.get(group.id))" @toggle="plan.toggleFeature(group.id)"
        />
        <span v-else-if="editable" class="fcb-ph" aria-hidden="true" />
        <span v-if="group.feature" class="rid mono">F{{ featureNumber(group) }}</span>
        <span class="ft" :title="group.feature?.title">{{ group.feature ? group.feature.title : 'Not tied to a feature' }}</span>
        <a v-if="group.feature" class="ekey mono" :href="epicHref(group.feature.key)" target="_blank" rel="noopener" :data-tip="`Epic ${group.feature.key} · opens in a new window`">{{ group.feature.key }}<AppIcon name="external" :size="10" /></a>
      </header>
      <p v-if="!group.tickets.length" class="empty">No tickets yet · ask Aithema to break this feature down</p>
      <ul class="tks">
        <li
          v-for="ticket in visible(group)" :id="`release-ticket-${ticket.ticket_node_id}`" :key="ticket.ticket_node_id" class="tk" :class="{ faded: editable && !plan.included.value.has(ticket.ticket_node_id) }"
          @click="emit('walk', ticket)"
        >
          <label v-if="editable" class="inrel" @click.stop>
            <input type="checkbox" :checked="plan.included.value.has(ticket.ticket_node_id)" :aria-label="`${ticket.key} in the release`" @change="plan.toggleTicket(ticket.ticket_node_id)" />
          </label>
          <StatusIcon v-else-if="stateOf(ticket)" :state="stateOf(ticket)" :size="13" class="st" />
          <span v-else class="st-ph" aria-hidden="true" />
          <span class="key mono">{{ ticket.key }}</span>
          <button type="button" class="title" :data-tip="`Walk through ${ticket.key} with its screens\n${ticket.title}`" @click.stop="emit('walk', ticket)">{{ ticket.title }}</button>
          <span v-if="ticket.estimated_hours != null" class="est mono">{{ hours(ticket.estimated_hours) }}</span>
          <button type="button" class="icon-btn sm flat open" :aria-label="`Open ${ticket.key}`" data-tip="Open the ticket" @click.stop="emit('open', ticket.key)"><AppIcon name="arrow" :size="13" /></button>
        </li>
      </ul>
    </section>
  </div>
</template>

<style scoped>
.release-tickets { display: grid; grid-template-columns: minmax(0, 1fr); gap: 10px; }
.release-tickets, .grp, .tks, .tk { min-width: 0; }
.grp { display: grid; grid-template-columns: minmax(0, 1fr); gap: 2px; }
.grp-h { display: flex; align-items: center; gap: 8px; min-width: 0; padding: 6px 4px 6px 6px; border-bottom: 1px solid color-mix(in oklab, var(--gold) 45%, transparent); font-size: 12.5px; color: var(--ink-2); }
.fcb-ph { width: 16px; flex-shrink: 0; }
.rid { flex-shrink: 0; font-size: 10.5px; font-weight: 500; letter-spacing: .06em; color: var(--gold-ink); }
.ft { flex: 1; min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.ekey { display: inline-flex; align-items: center; gap: 3px; flex-shrink: 0; font-size: 10.5px; font-weight: 500; letter-spacing: .04em; color: var(--ink-3); text-decoration: none; }
.ekey:hover { color: var(--teal-ink); }
.ekey:focus-visible { border-radius: 4px; box-shadow: var(--focus-ring); }
.empty { padding: 6px 8px 6px 32px; font-size: 12.5px; color: var(--ink-3); }
.tks { display: grid; grid-template-columns: minmax(0, 1fr); margin: 0; padding: 0; list-style: none; }
.tk { position: relative; display: flex; align-items: center; gap: 10px; min-height: 36px; padding: 0 6px 0 6px; border-radius: 10px; cursor: pointer; }
.compact .tk { min-height: 32px; }
.tk:hover { background: var(--row-hover); }
.tk.faded .key, .tk.faded .title { color: var(--ink-3); }
.inrel { display: grid; place-items: center; width: 16px; flex-shrink: 0; }
.inrel input { width: 16px; height: 16px; margin: 0; accent-color: var(--teal); cursor: pointer; }
.st { flex-shrink: 0; }
.st-ph { width: 13px; flex-shrink: 0; }
.key { flex-shrink: 0; width: 92px; font-size: 11.5px; color: var(--ink-3); font-variant-ligatures: none; }
.title { flex: 1; min-width: 0; padding: 0; border: 0; background: transparent; color: var(--ink); font-size: 14px; text-align: left; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; cursor: pointer; }
.title:focus-visible { border-radius: 4px; box-shadow: var(--focus-ring); }
.est { flex-shrink: 0; font-size: 11.5px; color: var(--ink-2); }
.open { flex-shrink: 0; opacity: 0; }
.tk:hover .open, .open:focus-visible { opacity: 1; }
@media (hover: none) { .open { opacity: 1; } }
@media (max-width: 720px) { .key { width: auto; } .title { font-size: 13.5px; } }
/* Phones: titles wrap in full; nothing ends in a cut. */
@media (max-width: 600px) {
  .tk { padding-block: 6px; }
  .title, .ft { white-space: normal; overflow: visible; overflow-wrap: anywhere; line-height: 1.4; }
}
</style>
