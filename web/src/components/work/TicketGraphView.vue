<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, inject, nextTick, onBeforeUnmount, ref, shallowRef, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import GraphCanvas from '../graph/GraphCanvas.vue'
import AppIcon from '../AppIcon.vue'
import { useSession } from '../../stores/session'
import { fetchTicketGraph, type TicketGraph, type TicketGraphNode } from '../../lib/ticketGraph'
import { filterTicketGraph, ticketGraphData, ticketLinkStyles, ticketStatusTokens, type TicketGraphState } from '../../lib/ticketGraphRenderer'
import type { GraphNode } from '../../lib/graphRenderer'
import type { ListFilters } from '../../lib/ticketList'
import { TICKET_PEEK } from '../../lib/ticketPeek'
import { plural, statusMeta } from '../../lib/work'

const props = defineProps<{ project: { id: string; routeKey: string; title: string }; filters: ListFilters }>()
const emit = defineEmits<{ open: [key: string]; state: [state: TicketGraphState] }>()
const route = useRoute(), router = useRouter(), session = useSession()
const peek = inject(TICKET_PEEK, null)
const canvas = ref<InstanceType<typeof GraphCanvas>>()
const empty: TicketGraph = { nodes: [], links: [], truncated: false }
const data = shallowRef<TicketGraph>(empty), loading = ref(true), error = ref(''), selection = ref('')
const hovered = shallowRef<TicketGraphNode | null>(null)
const viewer = computed(() => session.identity ? `${session.identity.tenant.id}:${session.identity.principal.id}` : undefined)
const visible = computed<TicketGraph>(previous => {
  const next = filterTicketGraph(data.value, props.filters)
  // A panel URL changes the route object, not the graph. Keep the same input
  // identity so the core retains its layout/camera while the panel resizes it.
  return previous && previous.truncated === next.truncated
    && previous.nodes.length === next.nodes.length && previous.nodes.every((node, i) => node === next.nodes[i])
    && previous.links.length === next.links.length && previous.links.every((link, i) => link === next.links[i]) ? previous : next
})
const adapted = computed(() => ticketGraphData(visible.value, props.project.routeKey))
const panelOpen = computed(() => !!route.params.ticketKey)
const selected = computed(() => visible.value.nodes.find(node => panelOpen.value ? node.key.toLowerCase() === String(route.params.ticketKey).toLowerCase() : node.id === selection.value))
const summary = computed(() => `${plural(visible.value.nodes.length, 'ticket')} · ${plural(visible.value.nodes.filter(n => n.type === 'epic').length, 'epic')} · ${plural(visible.value.links.length, 'link')}`)
let request: AbortController | undefined
async function load() {
  request?.abort()
  const controller = request = new AbortController()
  loading.value = true; error.value = ''; data.value = empty; hovered.value = null
  try {
    const result = await fetchTicketGraph(props.project.id, props.filters.showClosed, controller.signal)
    if (!controller.signal.aborted) data.value = result
  } catch (e) { if (!controller.signal.aborted) error.value = e instanceof Error ? e.message : 'The ticket graph could not be loaded.' }
  finally { if (!controller.signal.aborted) loading.value = false }
}
function inThisProject(key: string) {
  const prefix = key.split('-')[0] ?? ''
  return !key.includes('-') || prefix.toUpperCase() === props.project.routeKey.toUpperCase()
}
async function open(node: GraphNode) {
  const ticket = visible.value.nodes.find(n => n.id === node.id)
  if (!ticket || loading.value) return
  // The shared panel belongs to the project shell. Leave the focus dialog first
  // so its inert background and focus trap cannot hide the opened ticket.
  if (route.query.focus === '1') await router.replace({ query: { ...route.query, focus: undefined } })
  // This project's tickets keep the project panel. A ticket from another project peeks.
  if (!inThisProject(ticket.key) && peek) {
    peek.open(ticket.key, document.activeElement instanceof HTMLElement ? document.activeElement : null)
    return
  }
  emit('open', ticket.key)
}
watch([() => props.project.id, () => props.filters.showClosed, viewer], load, { immediate: true })
watch([data, visible, loading], () => emit('state', { data: data.value, visible: visible.value, loading: loading.value }), { immediate: true })
watch(panelOpen, async (open, wasOpen) => { if (!open && wasOpen) { await nextTick(); canvas.value?.focus() } })
onBeforeUnmount(() => request?.abort())
defineExpose({ focus: () => canvas.value?.focus() })
</script>

<template>
  <GraphCanvas ref="canvas" :data="adapted" :viewer-key="viewer" title="Tickets, connected"
    :summary="loading ? 'Finding the connections…' : summary" :selected-id="selected?.id"
    open-on-click :keyboard-active="!panelOpen || route.query.focus === '1'" :min-stage-height="320" canvas-class="ticket-graph-canvas"
    @select="node => selection = node.id" @open="open" @clear="selection = ''"
    @hover="node => hovered = visible.nodes.find(n => n.id === node?.id) ?? null">
    <template #default="{ focused }">
      <div v-if="loading || error || !visible.nodes.length" class="tg-state" role="status">
        <AppIcon :name="error ? 'alert' : 'graph'" :size="26" />
        <template v-if="error"><h3>Connections are taking a moment</h3><p role="alert">{{ error }}</p><button type="button" class="btn" @click="load">Try again</button></template>
        <p v-else-if="loading">Loading ticket connections…</p>
        <template v-else><h3>{{ data.nodes.length ? 'No tickets match these filters' : 'No tickets to show yet' }}</h3><p>{{ data.nodes.length ? 'Adjust the search or filters to see more connections.' : 'Try showing closed tickets, or add tickets in List view.' }}</p></template>
      </div>
      <div v-if="hovered && !loading && !error" class="tg-tooltip" :class="{ focused }" role="tooltip">
        <span>{{ hovered.key }} · {{ statusMeta(hovered.status).label }} · {{ plural(hovered.link_count, 'link') }}</span><strong>{{ hovered.title }}</strong>
      </div>
    </template>
    <template #footer>
      <div class="tg-legend" role="group" aria-label="Ticket graph legend">
        <div class="tg-legend-group" aria-label="Ticket status">
          <span v-for="(token, category) in ticketStatusTokens" :key="category"><i class="tg-dot" :style="{ background: `var(${token})` }" />{{ category === 'doing' ? 'In progress' : category === 'done' ? 'Closed' : 'Open' }}</span>
          <span><svg width="26" height="16" viewBox="0 0 26 16" aria-hidden="true"><circle cx="5" cy="9" r="3" fill="currentColor" opacity=".5" /><circle cx="19" cy="8" r="6" fill="currentColor" opacity=".7" /></svg>Epic hub</span>
        </div>
        <div class="tg-legend-group" aria-label="Ticket relations">
          <span v-for="(style, kind) in ticketLinkStyles" :key="kind" :data-relation="kind">
            <svg width="28" height="16" viewBox="0 0 28 16" fill="none" :style="{ color: `var(${style.color})` }" aria-hidden="true">
              <path :d="kind === 'duplicates' ? 'M1 11 Q14 0 26 11' : 'M1 8 H26'" stroke="currentColor" :stroke-width="style.width + .3" />
              <path v-if="style.directed" :d="kind === 'duplicates' ? 'm22 6 4 5-6 1' : 'm22 4 4 4-4 4'" stroke="currentColor" :stroke-width="style.width + .3" />
            </svg>{{ kind === 'parent' ? 'Parent' : kind.charAt(0).toUpperCase() + kind.slice(1) }}
          </span>
        </div>
      </div>
      <p class="tg-help">More links, larger nodes. Select to open · Drag to explore · Scroll to zoom</p>
      <p v-if="data.truncated" class="tg-notice" role="status">This project’s graph is truncated; some tickets or links are not shown.</p>
    </template>
  </GraphCanvas>
</template>

<style scoped>
.tg-state { position: absolute; inset: 0; display: flex; flex-direction: column; align-items: center; justify-content: center; gap: 14px; padding: 24px; text-align: center; background: var(--canvas); color: var(--ink-2); }
.tg-state h3 { font-size: 18px; color: var(--ink); font-weight: 550; }
.tg-state p { max-width: 340px; font-size: 13px; line-height: 1.6; }
.tg-help { margin-top: 12px; font-size: 11px; line-height: 1.6; color: var(--ink-3); }
.tg-tooltip { position: absolute; top: 12px; left: 20px; max-width: min(340px, calc(100% - 40px)); padding: 12px 14px; background: var(--surface-raised); border: 1px solid var(--line); border-radius: 12px; box-shadow: var(--shadow-pop); pointer-events: none; display: grid; gap: 5px; }
.tg-tooltip.focused { top: var(--graph-overlay-top); }
.tg-tooltip span { font: 10px/1.5 var(--mono); color: var(--ink-3); }
.tg-tooltip strong { font-size: 13px; font-weight: 550; overflow-wrap: anywhere; }
.tg-legend, .tg-legend-group { display: flex; flex-wrap: wrap; align-items: center; gap: 10px 18px; }
.tg-legend { justify-content: space-between; }
.tg-legend-group > span { display: inline-flex; align-items: center; gap: 7px; font-size: 11px; color: var(--ink-2); white-space: nowrap; }
.tg-legend-group svg { flex: none; }
.tg-dot { width: 8px; height: 8px; border-radius: 50%; }
.tg-notice { font-size: 12px; color: var(--ink-2); margin-top: 12px; }
@media (max-width: 600px) { .tg-legend-group { gap: 10px 14px; } }
</style>
