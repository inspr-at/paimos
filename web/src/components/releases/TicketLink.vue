<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, watch, watchEffect } from 'vue'
import { brand } from '../../lib/brand'
import { normalKey, showingTicketKeys, ticketRef, wantTicketKey } from '../../lib/ticketLinks'
import { useProjects } from '../../stores/projects'
import { ticketPath } from '../../lib/work'
import TicketPeekLink from '../TicketPeekLink.vue'

// One ticket key in release notes: a link to the ticket when this workspace has
// it, plain text otherwise (earlier trackers, other products), never a dead link.
// The link is TicketPeekLink: a plain click opens the peek, ctrl/cmd opens one
// tab from the press (AEON-1327), other modifiers keep the browser's meaning.
const props = withDefaults(defineProps<{ ticketKey: string; variant?: 'chip' | 'inline' }>(), { variant: 'chip' })
const projects = useProjects()
void projects.load()

// Asks for its key, and again after an access change dropped the answer.
watch(() => normalKey(props.ticketKey), (key, _old, onCleanup) => {
  onCleanup(showingTicketKeys(key))
}, { immediate: true, flush: 'sync' })
watchEffect(() => { if (ticketRef(props.ticketKey) === undefined) wantTicketKey(props.ticketKey) })
const ticket = computed(() => ticketRef(props.ticketKey))
const href = computed(() => {
  const project = ticket.value ? projects.byId(ticket.value.projectId) : undefined
  return ticket.value && project ? ticketPath(project.routeKey, ticket.value.key) : ''
})
const missing = computed(() => ticket.value === null ? `${props.ticketKey} is not a ticket in this ${brand.value.short_name} workspace` : undefined)
</script>

<template>
  <TicketPeekLink
    v-if="href" class="ticket-link" :class="[variant, { 'key-badge': variant === 'chip' }]" :ticket-key="ticketKey" :href="href"
    :aria-label="`${ticketKey}: ${ticket!.title}`" :tip="ticket!.title"
  >{{ ticketKey }}</TicketPeekLink>
  <span v-else class="ticket-plain" :class="[variant, { 'key-badge': variant === 'chip' }]" :data-tip="missing">{{ ticketKey }}</span>
</template>

<style scoped>
.ticket-link { text-decoration: none; transition: box-shadow .15s ease, background-color .15s ease, color .15s ease; }
.ticket-link:focus-visible { outline: none; box-shadow: var(--focus-ring); }

.chip { height: 24px; }
.ticket-plain.chip { background: var(--surface-2); box-shadow: inset 0 0 0 1px var(--line); color: var(--ink-2); }
@media (hover: hover) { .ticket-link.chip:hover { box-shadow: inset 0 0 0 1px var(--teal), 0 4px 10px -6px color-mix(in srgb, var(--primary-line) 50%, transparent); } }
/* The ticket open beside the history: a filled chip, not an edge mark. */
.ticket-link.chip[aria-current="true"] { background: var(--primary); box-shadow: inset 0 0 0 1px var(--primary-line); color: var(--primary-on); }

.inline { display: inline-flex; align-items: center; font: 500 11px/1.4 var(--mono); letter-spacing: .02em; font-variant-ligatures: none; white-space: nowrap; }
.ticket-link.inline { margin: 0 -4px; padding: 1px 4px; border-radius: 5px; color: var(--teal-ink); }
.ticket-plain.inline { color: var(--ink-3); }
@media (hover: hover) { .ticket-link.inline:hover { background: var(--row-selected); text-decoration: underline; text-underline-offset: 2px; } }
.ticket-link.inline[aria-current="true"] { background: var(--row-selected); box-shadow: inset 0 0 0 1px var(--chip-teal-line); }

@media (max-width: 600px) {
  .ticket-link.chip { height: 44px; padding: 0 12px; }
  .ticket-link.inline { min-height: 44px; }
}
</style>
