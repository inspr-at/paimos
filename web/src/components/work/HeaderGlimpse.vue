<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, onMounted, ref, shallowRef, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import GraphCanvas from '../graph/GraphCanvas.vue'
import AppIcon from '../AppIcon.vue'
import { projectSection } from './projectNavigation'
import { GLIMPSE_CLEAR_SELECTOR, glimpseControlSpot, glimpseTextMask, loadTicketGraphContext, headerGlimpseAllowed, headerGlimpseGraphReady } from '../../lib/headerGlimpse'
import { filtersFromQuery, filtersToQuery, type ListFilters } from '../../lib/ticketList'
import { ticketGraphData } from '../../lib/ticketGraphRenderer'
import type { GraphData, GraphFPS } from '../../lib/graphRenderer'

// HG2 is mounted inside .project-head. The existing shell resolves saved views
// into the Tickets URL; hosts can also supply that effective ListFilters value.
// Geometry includes the saved-view strip and ends at the static toolbar marker.
const props = withDefaults(defineProps<{ projectId: string; projectKey: string; ticketCount: number; enabled: boolean; filters?: ListFilters; fps?: GraphFPS }>(), { fps: 60 })
const emit = defineEmits<{ active: [value: boolean] }>()
const router = useRouter(), route = useRoute()
const filters = computed(() => props.filters ?? filtersFromQuery(projectSection(route) === 'tickets' ? route.query : {}))
const wideQuery = window.matchMedia('(min-width: 1280px)')
const reducedQuery = window.matchMedia('(prefers-reduced-motion: reduce)')
const wide = ref(wideQuery.matches), reduced = ref(reducedQuery.matches), idle = ref(false)
const paint = ref(false), userPaused = ref(false), occluded = ref(false)
const data = shallowRef<GraphData>({ nodes: [], links: [] })
const column = ref<HTMLElement>()
const region = ref({ top: '0px', height: '0px' })
const mask = ref('none'), measured = ref(false), measuredViewport = ref(0)
const controls = ref<HTMLElement>(), controlSpot = ref<{ left: string; top: string; width: string; height: string } | null>(null)
const shown = ref(0), truncated = ref(false)
const canvas = ref<{ setPaused: (value: boolean) => void } | null>(null)
const allowed = computed(() => headerGlimpseAllowed({ enabled: props.enabled, reduced: reduced.value, wide: wide.value, tickets: props.ticketCount }))
let request: AbortController | undefined, idleId = 0, seeing: IntersectionObserver | undefined, sizing: ResizeObserver | undefined, mounted = false, changes: MutationObserver | undefined, measureFrame = 0

function measureRegion() {
  const header = column.value?.closest<HTMLElement>('.project-head')
  const page = header?.closest<HTMLElement>('.project-page')
  if (!header || !page || !column.value) return
  const box = header.getBoundingClientRect()
  const padding = parseFloat(getComputedStyle(page).paddingTop) || 0
  // The marker retains its content position when the toolbar becomes sticky.
  const boundary = page.querySelector('.stick-mark')?.getBoundingClientRect()
  const bottom = boundary?.top ?? box.bottom
  const top = box.top - padding, height = Math.max(0, bottom - top)
  region.value = { top: `${-padding}px`, height: `${height}px` }
  const boxes = [...page.querySelectorAll<HTMLElement>(GLIMPSE_CLEAR_SELECTOR)]
    .filter(el => el.getClientRects().length)
    .map(el => { const rect = el.getBoundingClientRect(); return { x: rect.left - box.left, y: rect.top - top, width: rect.width, height: rect.height } })
    .filter(box => box.width > 0 && box.height > 0)
  mask.value = glimpseTextMask(box.width, height, boxes)
  const spot = glimpseControlSpot(box.width, height, boxes, { width: controls.value?.offsetWidth ?? 310, height: controls.value?.offsetHeight ?? 34 })
  controlSpot.value = spot ? { left: `${spot.x}px`, top: `${spot.y}px`, width: `${spot.width}px`, height: `${spot.height}px` } : null
  measuredViewport.value = window.innerWidth
  measured.value = true
}
function scheduleMeasure() {
  // Resize/content changes invalidate both the mask and the hit target until
  // their next shared measurement; stale controls cannot cover new text.
  measured.value = false
  cancelAnimationFrame(measureFrame)
  measureFrame = requestAnimationFrame(measureRegion)
}

function applyMotion() { canvas.value?.setPaused(userPaused.value || occluded.value || document.hidden) }
function openGraph() { void router.push({ path: `/p/${encodeURIComponent(props.projectKey)}/tickets`, query: { ...filtersToQuery(filters.value), view: 'graph' } }) }
function togglePause() { userPaused.value = !userPaused.value; applyMotion() }
function onWide() { wide.value = wideQuery.matches }
function onReduced() { reduced.value = reducedQuery.matches }
function onVisibility() { applyMotion() }
function watchHeader() {
  seeing?.disconnect(); sizing?.disconnect(); changes?.disconnect()
  const header = column.value?.closest('header'), page = header?.closest('.project-page')
  if (!header || !page) return
  const root = document.getElementById('main')
  seeing = new IntersectionObserver(([entry]) => { occluded.value = !entry.isIntersecting; applyMotion() }, { root: root instanceof HTMLElement ? root : null, threshold: 0 })
  seeing.observe(column.value!)
  sizing = new ResizeObserver(scheduleMeasure)
  for (const el of [header, controls.value, ...page.querySelectorAll(GLIMPSE_CLEAR_SELECTOR)]) if (el) sizing.observe(el)
  changes = new MutationObserver(records => {
    if (records.some(record => !column.value?.contains(record.target))) {
      for (const el of page.querySelectorAll(GLIMPSE_CLEAR_SELECTOR)) sizing?.observe(el)
      scheduleMeasure()
    }
  })
  changes.observe(page, { subtree: true, childList: true, characterData: true })
  measureRegion()
}

async function load() {
  request?.abort()
  paint.value = false; measured.value = false
  if (!mounted || !idle.value || !allowed.value) return
  const controller = request = new AbortController()
  try {
    const { visible } = await loadTicketGraphContext(props.projectId, filters.value, controller.signal)
    if (controller.signal.aborted || !allowed.value) return
    if (!headerGlimpseGraphReady(visible.nodes.length, visible.links.length)) return
    shown.value = visible.nodes.length; truncated.value = visible.truncated
    data.value = ticketGraphData(visible, props.projectKey)
    paint.value = true
    await nextTick()
    watchHeader()
    applyMotion()
  } catch { /* A missing graph leaves the header as it was. */ }
}
watch(paint, value => emit('active', value))
watch([allowed, idle, () => props.projectId, () => props.projectKey, () => JSON.stringify(filtersToQuery(filters.value))], () => { void load() })
onMounted(() => {
  mounted = true
  wideQuery.addEventListener('change', onWide)
  reducedQuery.addEventListener('change', onReduced)
  document.addEventListener('visibilitychange', onVisibility)
  window.addEventListener('resize', scheduleMeasure)
  const start = () => { if (mounted) idle.value = true }
  if (typeof window.requestIdleCallback === 'function') idleId = window.requestIdleCallback(start, { timeout: 1500 })
  else requestAnimationFrame(() => requestAnimationFrame(start))
})
onBeforeUnmount(() => {
  mounted = false
  request?.abort()
  if (idleId) window.cancelIdleCallback?.(idleId)
  seeing?.disconnect()
  sizing?.disconnect()
  changes?.disconnect()
  cancelAnimationFrame(measureFrame)
  window.removeEventListener('resize', scheduleMeasure)
  wideQuery.removeEventListener('change', onWide)
  reducedQuery.removeEventListener('change', onReduced)
  document.removeEventListener('visibilitychange', onVisibility)
  emit('active', false)
})
</script>

<template>
  <div v-if="paint" ref="column" class="glimpse-col" :style="region" :data-measured="measured" :data-measured-viewport="measuredViewport" data-header-glimpse="on" :data-shown="shown">
    <div class="glimpse-canvas" :style="{ '--glimpse-text-mask': mask }" aria-hidden="true">
      <GraphCanvas ref="canvas" glimpse layout-bias="elliptic" :data="data" :fps="fps" canvas-class="header-glimpse-canvas" />
    </div>
    <div v-if="controlSpot" class="glimpse-hit" :style="controlSpot" @click="openGraph" />
    <div ref="controls" class="glimpse-controls" :style="controlSpot ? { left: controlSpot.left, top: controlSpot.top } : { visibility: 'hidden' }">
      <span class="glimpse-count" :title="truncated ? 'Graph limit reached; some matching tickets are not shown' : 'Tickets in the current context'">{{ shown }}{{ truncated ? '+' : '' }} / {{ ticketCount }}</span>
      <button type="button" class="btn sm" @click="openGraph"><AppIcon name="graph" :size="13" />Open graph</button>
      <button type="button" class="btn sm" :aria-pressed="userPaused" :aria-label="userPaused ? 'Resume motion' : 'Pause motion'" @click="togglePause"><AppIcon :name="userPaused ? 'play' : 'pause'" :size="13" />{{ userPaused ? 'Resume' : 'Pause' }}</button>
    </div>
  </div>
  <span v-else data-header-glimpse="off" hidden />
</template>

<style scoped>
/* The header owns the backdrop; the graph never takes a layout column. */
:global(.project-head:has(.glimpse-col)) { position: relative; }
:global(.head-flex.with-glimpse:has(.glimpse-col)) { display: flex; align-items: flex-end; }
.glimpse-col { position: absolute; left: 0; right: 0; overflow: hidden; pointer-events: none; }
.glimpse-col[data-measured="false"] { visibility: hidden; }
.glimpse-canvas {
  position: absolute; inset: 0; opacity: .35; pointer-events: none;
  mask-image: var(--glimpse-text-mask), linear-gradient(to right, transparent, #000 4%, #000 96%, transparent), linear-gradient(to bottom, transparent, #000 10%, #000 88%, transparent);
  mask-composite: intersect;
  mask-repeat: no-repeat;
  mask-size: 100% 100%;
}
:global(:root[data-theme="dark"] .glimpse-canvas) { opacity: .30; }
@media (prefers-color-scheme: dark) { :global(:root:not([data-theme="light"]) .glimpse-canvas) { opacity: .30; } }
.glimpse-hit { position: absolute; z-index: 2; cursor: pointer; pointer-events: auto; }
.glimpse-controls {
  position: absolute; z-index: 3; width: max-content; display: flex; align-items: center; gap: 6px; padding: 4px; border-radius: 999px; pointer-events: none; opacity: 0;
  background: var(--surface-raised); box-shadow: var(--shadow);
}
:global(.project-head:hover .glimpse-controls), .glimpse-col:focus-within .glimpse-controls { opacity: 1; pointer-events: auto; }
.glimpse-controls .btn { height: 26px; padding: 0 10px; font-size: 12px; }
.glimpse-count { padding: 0 6px 0 8px; font: 10px var(--mono); color: var(--ink-3); white-space: nowrap; }
</style>
