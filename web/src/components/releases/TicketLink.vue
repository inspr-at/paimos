<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, inject, watch, watchEffect } from 'vue'
import { useRouter } from 'vue-router'
import { brand } from '../../lib/brand'
import { TICKET_PEEK } from '../../lib/ticketPeek'
import { normalKey, showingTicketKeys, ticketRef, wantTicketKey } from '../../lib/ticketLinks'
import { useProjects } from '../../stores/projects'

// One ticket key in release notes: a link to the ticket when this workspace has
// it, plain text otherwise (earlier trackers, other products), never a dead link.
const props = withDefaults(defineProps<{ ticketKey: string; variant?: 'chip' | 'inline' }>(), { variant: 'chip' })
const projects = useProjects()
void projects.load()
const router = useRouter()
const peek = inject(TICKET_PEEK, null)

// Asks for its key, and again after an access change dropped the answer.
watch(() => normalKey(props.ticketKey), (key, _old, onCleanup) => {
  onCleanup(showingTicketKeys(key))
}, { immediate: true, flush: 'sync' })
watchEffect(() => { if (ticketRef(props.ticketKey) === undefined) wantTicketKey(props.ticketKey) })
const ticket = computed(() => ticketRef(props.ticketKey))
const href = computed(() => {
  const project = ticket.value ? projects.byId(ticket.value.projectId) : undefined
  return ticket.value && project ? `/p/${encodeURIComponent(project.routeKey)}/${encodeURIComponent(ticket.value.key)}` : ''
})
const open = computed(() => !!peek && peek.openKey.value === normalKey(props.ticketKey))
const missing = computed(() => ticket.value === null ? `${props.ticketKey} is not a ticket in this ${brand.value.short_name} workspace` : undefined)

// Modified and middle clicks keep the browser's own meaning (new tab, window).
function click(event: MouseEvent) {
  if (event.defaultPrevented || event.button !== 0 || event.metaKey || event.ctrlKey || event.shiftKey || event.altKey) return
  event.preventDefault()
  if (peek) peek.open(props.ticketKey, event.currentTarget as HTMLElement)
  else void router.push(href.value)
}
</script>

<template>
  <a
    v-if="href" class="ticket-link" :class="[variant, { 'key-badge': variant === 'chip' }]" :href="href" :aria-current="open ? 'true' : undefined"
    :aria-label="`${ticketKey}: ${ticket!.title}`" :data-tip="ticket!.title" @click="click"
  >{{ ticketKey }}</a>
  <span v-else class="ticket-plain" :class="[variant, { 'key-badge': variant === 'chip' }]" :data-tip="missing">{{ ticketKey }}</span>
</template>

<style scoped>
.ticket-link { text-decoration: none; transition: box-shadow .15s ease, background-color .15s ease, color .15s ease; }
.ticket-link:focus-visible { outline: none; box-shadow: var(--focus-ring); }

.chip { height: 24px; }
.ticket-plain.chip { background: var(--surface-2); box-shadow: inset 0 0 0 1px var(--line); color: var(--ink-2); }
@media (hover: hover) { .ticket-link.chip:hover { box-shadow: inset 0 0 0 1px var(--teal), 0 4px 10px -6px rgba(14, 111, 108, .5); } }
/* The ticket open beside the history: a filled chip, not an edge mark. */
.ticket-link.chip[aria-current="true"] { background: var(--primary); box-shadow: inset 0 0 0 1px var(--primary-line); color: var(--primary-on); }

.inline { display: inline-flex; align-items: center; font: 500 11px/1.4 var(--mono); letter-spacing: .02em; font-variant-ligatures: none; }
.ticket-link.inline { margin: 0 -4px; padding: 1px 4px; border-radius: 5px; color: var(--teal-ink); }
.ticket-plain.inline { color: var(--ink-3); }
@media (hover: hover) { .ticket-link.inline:hover { background: var(--row-selected); text-decoration: underline; text-underline-offset: 2px; } }
.ticket-link.inline[aria-current="true"] { background: var(--row-selected); box-shadow: inset 0 0 0 1px var(--chip-teal-line); }

@media (max-width: 600px) {
  .ticket-link.chip { height: 44px; padding: 0 12px; }
  .ticket-link.inline { min-height: 44px; }
}
</style>
