<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, shallowRef, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import AppIcon from '../AppIcon.vue'
import GraphCanvas from '../graph/GraphCanvas.vue'
import KnowledgeGraphControls from './KnowledgeGraphControls.vue'
import KnowledgeGraphLegend from './KnowledgeGraphLegend.vue'
import { listNodes } from '../../lib/api'
import { toast } from '../../lib/toast'
import { useSession } from '../../stores/session'
import { entryPath, type KnowledgeType } from '../../lib/knowledge'
import { STATUS_VIEWS, type KnowledgeFilters } from '../../lib/useKnowledge'
import { fetchKnowledgeGraph, filterGraph, graphEntry, graphMatches, graphTypeLabel, graphTypeTokens, type GraphNode, type KnowledgeGraphData } from '../../lib/knowledgeGraph'
import { knowledgeGraphData } from '../../lib/knowledgeGraphRenderer'
import type { GraphNode as CanvasNode } from '../../lib/graphRenderer'

// Knowledge owns API data, URLs and reading cards; GraphCanvas owns presentation.
const props = defineProps<{ project: { id: string; routeKey: string; title: string }; filters: KnowledgeFilters; canWrite: boolean; docked?: boolean }>()
const emit = defineEmits<{ create: []; reset: []; list: [] }>()
const route = useRoute(), router = useRouter(), session = useSession()
const canvas = ref<InstanceType<typeof GraphCanvas>>()
const viewer = computed(() => session.identity ? `${session.identity.tenant.id}:${session.identity.principal.id}` : undefined)
const empty: KnowledgeGraphData = { nodes: [], edges: [], truncated: false }
const data = shallowRef<KnowledgeGraphData>(empty)
const loading = ref(true), error = ref(''), tickets = ref(false)
const hovered = shallowRef<GraphNode | null>(null), ticketSelection = ref('')
const pointer = ref({ x: 0, y: 0 })
const visible = computed(() => filterGraph(data.value, props.filters.type))
const adapted = computed(() => knowledgeGraphData(visible.value, props.project.routeKey))
const selected = computed(() => visible.value.nodes.find(n => n.kind === 'knowledge' ? graphEntry(n) === route.query.entry : n.id === ticketSelection.value) ?? null)
const query = computed(() => props.filters.q.trim())
const matches = computed(() => graphMatches(visible.value.nodes, query.value))
const searchResults = computed(() => query.value ? visible.value.nodes.filter(n => matches.value.has(n.id)).slice(0, 5) : [])
const entries = computed(() => visible.value.nodes.filter(n => n.kind === 'knowledge').length)
const ticketCount = computed(() => visible.value.nodes.length - entries.value)
const summary = computed(() => `${entries.value} entries, ${visible.value.edges.length} ${visible.value.edges.length === 1 ? 'link' : 'links'}${ticketCount.value ? `, ${ticketCount.value} linked tickets` : ''}`)
let loadController: AbortController | null = null, mounted = false
async function load() {
  loadController?.abort()
  const controller = loadController = new AbortController()
  loading.value = true; error.value = ''
  try {
    const statuses = STATUS_VIEWS.find(v => v.value === props.filters.status)?.statuses ?? []
    const result = await fetchKnowledgeGraph(props.project.id, statuses, tickets.value, controller.signal)
    if (!controller.signal.aborted) data.value = result
  } catch (e) { if (!controller.signal.aborted) { data.value = empty; error.value = e instanceof Error ? e.message : 'The graph could not be loaded.' } }
  finally { if (!controller.signal.aborted) loading.value = false }
}
function select(node: GraphNode) {
  if (!mounted) return
  canvas.value?.interact(); ticketSelection.value = node.kind === 'ticket' ? node.id : ''
  void router.replace({ query: { ...route.query, entry: graphEntry(node) || undefined } })
  canvas.value?.focusNode(node.id); canvas.value?.focus()
}
function clear() {
  if (!mounted) return
  canvas.value?.interact(); ticketSelection.value = ''; hovered.value = null
  if (route.query.entry) void router.replace({ query: { ...route.query, entry: undefined } })
}
async function open(node: GraphNode) {
  if (!mounted) return
  if (node.kind === 'knowledge') {
    await router.push({ path: entryPath(props.project.routeKey, node.type as KnowledgeType, node.slug), query: { ...route.query, entry: undefined, focus: undefined } })
    return
  }
  // A satellite can belong to another project. Resolve its project only when
  // opening the full ticket; graph loading never requests ticket bodies.
  try {
    const page = await listNodes({ q: node.key, kind: ['ticket'], sort: 'key', limit: 100 })
    if (!mounted) return
    const project = page.items.find(item => item.id === node.id)?.project
    if (!project) throw new Error('Ticket project unavailable')
    await router.push({ path: `/p/${encodeURIComponent(project.key)}/${encodeURIComponent(node.key)}` })
  } catch { toast('The ticket could not be opened. Please try again.', { tone: 'error' }) }
}
function find(node: CanvasNode) { return visible.value.nodes.find(n => n.id === node.id) }
function selectCanvas(node: CanvasNode) { const source = find(node); if (source) select(source) }
function openCanvas(node: CanvasNode) { const source = find(node); if (source) void open(source) }
function hoverCanvas(node: CanvasNode | null) { hovered.value = node ? find(node) ?? null : null }
function move(event: PointerEvent) {
  const rect = (event.currentTarget as HTMLElement).getBoundingClientRect()
  pointer.value = { x: Math.max(8, Math.min(event.clientX - rect.left + 18, rect.width - 280)), y: Math.max(8, Math.min(event.clientY - rect.top + 18, rect.height - 150)) }
}
watch([() => props.project.id, () => props.filters.status, tickets], () => { if (mounted) void load() })
onMounted(() => { mounted = true; void load() })
onBeforeUnmount(() => { mounted = false; loadController?.abort() })
defineExpose({ focus: () => canvas.value?.focus() })
</script>

<template>
  <div class="kg">
    <GraphCanvas ref="canvas" :data="adapted" :viewer-key="viewer" title="Knowledge, connected" :summary="loading ? 'Finding the connections…' : summary" :selected-id="selected?.id" :matches="matches" :searching="!!query" canvas-class="kg-canvas" @select="selectCanvas" @open="openCanvas" @hover="hoverCanvas" @clear="clear" @pointer="move">
      <template #controls><KnowledgeGraphControls :tickets="tickets" @tickets="tickets = !tickets" /></template>
      <template #default="{ dimension, focused }">
      <div v-if="loading || error || !visible.nodes.length" class="kg-state">
        <span v-if="loading" class="kg-loading" aria-hidden="true"><AppIcon name="link" :size="24" /></span>
        <template v-if="error"><AppIcon name="alert" :size="24" /><h3>Connections are taking a moment</h3><p role="alert">{{ error }}</p><button class="btn" type="button" @click="load()">Try again</button></template>
        <template v-else-if="!loading"><AppIcon name="book" :size="26" /><h3>{{ data.nodes.length ? 'No entries of this kind' : 'A place for connections to grow' }}</h3><p>Link entries with <code>[[slug]]</code> mentions or relations.</p><button v-if="data.nodes.length" class="btn" type="button" @click="emit('reset')">Show all knowledge</button><button v-else-if="canWrite" class="btn primary" type="button" @click="emit('create')"><AppIcon name="plus" :size="14" />Write the first entry</button><button v-else class="btn" type="button" @click="emit('list')">Show the entries</button></template>
      </div>
      <div v-if="!loading && !error && query" class="kg-results" :class="{ focused }">
        <p>{{ matches.size }} {{ matches.size === 1 ? 'match' : 'matches' }} <span>in this graph</span></p>
        <button v-for="node in searchResults" :key="node.id" type="button" :aria-pressed="selected?.id === node.id" @click="select(node)"><span class="kg-dot" :style="{ background: `var(${graphTypeTokens[node.type]})` }" /><span>{{ node.title }}</span><AppIcon name="arrow" :size="12" /></button>
        <button v-if="!matches.size" type="button" @click="emit('reset')">Clear the filters<AppIcon name="close" :size="12" /></button>
      </div>
      <div v-if="!loading && visible.nodes.length && (!visible.edges.length || visible.nodes.length < 5) && !selected && !query" class="kg-sparse"><AppIcon name="link" :size="14" /><span>Add <code>[[slug]]</code> mentions or relations to connect these entries.</span><button class="btn sm" type="button" @click="open(visible.nodes[0])">Open an entry<AppIcon name="arrow" :size="12" /></button></div>
      <div v-if="selected && (focused || !(docked && selected.kind === 'knowledge'))" class="kg-selection" :class="{ focused }" aria-live="polite">
        <div class="kg-selection-meta"><span class="kg-dot" :style="{ background: `var(${graphTypeTokens[selected.type]})` }" />{{ graphTypeLabel(selected) }}<span class="mono">{{ selected.degree }} {{ selected.degree === 1 ? 'link' : 'links' }}</span><button type="button" class="icon-btn sm flat" aria-label="Clear graph selection" @click="clear"><AppIcon name="close" :size="12" /></button></div>
        <h3>{{ selected.title }}</h3><p class="mono">{{ selected.slug || selected.key }}</p>
        <button type="button" class="btn sm" @click="open(selected)">Open {{ selected.kind === 'ticket' ? 'ticket' : 'entry' }}<AppIcon name="external" :size="12" /></button><span class="kg-enter"><kbd class="keycap">Enter</kbd></span>
      </div>
      <div v-if="hovered && hovered.id !== selected?.id" class="kg-tooltip" role="tooltip" :style="{ left: `${pointer.x}px`, top: `${pointer.y}px` }"><span>{{ graphTypeLabel(hovered) }} · {{ hovered.degree }} {{ hovered.degree === 1 ? 'link' : 'links' }}</span><strong>{{ hovered.title }}</strong><code>{{ hovered.slug || hovered.key }}</code></div>
      <span v-if="!loading && visible.nodes.length && !query && !selected && !focused" class="kg-navigation">{{ dimension === '3d' ? 'Drag to orbit' : 'Drag to pan' }}<span>Scroll to zoom</span><span>Select to explore</span></span>
      </template>
      <template #footer><KnowledgeGraphLegend :nodes="visible.nodes" /><p v-if="data.truncated" class="kg-truncated" role="status">Showing up to 2,000 entries and tickets, and 8,000 links. Choose a status to narrow the graph.</p></template>
    </GraphCanvas>
  </div>
</template>

<style scoped>
.kg { min-width: 0; align-self: start; }
.kg-truncated { font-size: 11.5px; color: var(--ink-3); margin-top: 10px; }
.kg-state { position: absolute; inset: 0; display: flex; flex-direction: column; align-items: center; justify-content: center; gap: 15px; padding: 30px; text-align: center; background: var(--canvas); color: var(--ink-2); }
.kg-state h3 { font-size: 20px; font-weight: 550; color: var(--ink); letter-spacing: -.025em; }
.kg-state p { font-size: 13px; max-width: 360px; }
.kg-loading { display: grid; place-items: center; width: 64px; height: 64px; border-radius: 50%; background: var(--surface-2); color: var(--teal); }
.kg-navigation { position: absolute; bottom: 12px; left: 0; right: 0; display: flex; justify-content: center; flex-wrap: wrap; gap: 16px; pointer-events: none; font-size: 11px; color: var(--ink-3); }
.kg-navigation span { opacity: .8; }
.kg-dot { width: 7px; height: 7px; flex: 0 0 auto; border-radius: 50%; }
.kg-selection, .kg-results, .kg-tooltip { background: var(--surface-raised-2); border-radius: 12px; box-shadow: var(--shadow-pop); -webkit-backdrop-filter: blur(14px); backdrop-filter: blur(14px); color: var(--ink); }
.kg-selection { position: absolute; bottom: 18px; left: 22px; width: min(310px, calc(100% - 44px)); padding: 14px 16px; }
.kg-selection-meta { display: flex; align-items: center; gap: 7px; font-size: 11px; color: var(--ink-2); }
.kg-selection-meta .mono { margin-left: auto; font-size: 10px; }
.kg-selection-meta .icon-btn { margin: -5px -8px -5px 0; }
.kg-selection h3 { font-size: 15px; line-height: 1.4; margin: 8px 0 4px; font-weight: 600; overflow-wrap: anywhere; }
.kg-selection > p { font-size: 10px; color: var(--ink-3); margin-bottom: 13px; overflow-wrap: anywhere; }
.kg-enter { margin-left: 10px; color: var(--ink-3); font-size: 10px; }
.kg-tooltip { position: absolute; pointer-events: none; padding: 12px 14px; width: 260px; z-index: 3; display: grid; gap: 5px; }
.kg-tooltip > span { font-size: 10px; color: var(--ink-3); }
.kg-tooltip strong { font-size: 13px; line-height: 1.4; font-weight: 550; overflow-wrap: anywhere; }
.kg-tooltip code { font-size: 10px; color: var(--ink-3); overflow-wrap: anywhere; }
.kg-results { position: absolute; top: 8px; left: 22px; padding: 12px; width: min(310px, calc(100% - 44px)); }
.kg-results.focused { top: var(--graph-overlay-top, 100px); }
.kg-selection.focused { bottom: var(--graph-overlay-bottom, 100px); }
.kg-results > p { font-size: 11px; font-weight: 600; padding: 0 6px 8px; }
.kg-results > p span { color: var(--ink-3); font-weight: 400; }
.kg-results > button { width: 100%; display: flex; align-items: center; gap: 9px; text-align: left; border: 0; background: transparent; color: var(--ink); padding: 8px 6px; font-size: 12px; border-radius: 7px; }
.kg-results > button > span:nth-child(2) { flex: 1; overflow: hidden; white-space: nowrap; text-overflow: ellipsis; }
.kg-results > button:hover, .kg-results > button[aria-pressed="true"] { background: var(--row-selected); }
.kg-results > button:focus-visible { box-shadow: var(--focus-ring); }
.kg-sparse { position: absolute; bottom: 52px; left: 22px; right: 22px; display: flex; align-items: center; justify-content: center; flex-wrap: wrap; gap: 10px; color: var(--ink-2); font-size: 12px; }
@media (max-width: 600px) { .kg-tooltip { display: none; } .kg-navigation { gap: 10px; font-size: 10px; } }
</style>
