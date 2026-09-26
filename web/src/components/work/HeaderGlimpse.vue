<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, onMounted, ref, shallowRef, watch } from 'vue'
import { useRouter } from 'vue-router'
import GraphCanvas from '../graph/GraphCanvas.vue'
import AppIcon from '../AppIcon.vue'
import { headerGlimpseAllowed, headerGlimpseGraphReady } from '../../lib/headerGlimpse'
import { fetchTicketGraph } from '../../lib/ticketGraph'
import { ticketGraphData } from '../../lib/ticketGraphRenderer'
import type { GraphData, GraphFPS } from '../../lib/graphRenderer'

const props = withDefaults(defineProps<{ projectId: string; projectKey: string; ticketCount: number; enabled: boolean; fps?: GraphFPS }>(), { fps: 60 })
const emit = defineEmits<{ active: [value: boolean] }>()
const router = useRouter()
const wideQuery = window.matchMedia('(min-width: 1280px)')
const reducedQuery = window.matchMedia('(prefers-reduced-motion: reduce)')
const wide = ref(wideQuery.matches), reduced = ref(reducedQuery.matches), idle = ref(false)
const paint = ref(false), userPaused = ref(false), occluded = ref(false)
const data = shallowRef<GraphData>({ nodes: [], links: [] })
const column = ref<HTMLElement>()
const region = ref({ top: '0px', height: '0px' })
const canvas = ref<{ setPaused: (value: boolean) => void } | null>(null)
const allowed = computed(() => headerGlimpseAllowed({ enabled: props.enabled, reduced: reduced.value, wide: wide.value, tickets: props.ticketCount }))
let request: AbortController | undefined, idleId = 0, seeing: IntersectionObserver | undefined, sizing: ResizeObserver | undefined, mounted = false

function measureRegion() {
  const box = column.value?.getBoundingClientRect()
  const text = column.value?.parentElement?.querySelector<HTMLElement>('.head-main')
  if (!box || !text) return
  const block = text.getBoundingClientRect(), padding = parseFloat(getComputedStyle(text).paddingTop) || 0
  const centre = block.top + padding + (block.height - padding) / 2 - box.top
  // Follow the full text block, including its description and journey pill.
  // The entire masked canvas stays inside its own empty grid column.
  const height = Math.max(0, Math.min((centre - 4) * 2, (box.height - centre - 4) * 2))
  region.value = { top: `${centre - height / 2}px`, height: `${height}px` }
}

function applyMotion() { canvas.value?.setPaused(userPaused.value || occluded.value || document.hidden) }
function openGraph() { void router.push({ path: `/p/${encodeURIComponent(props.projectKey)}/tickets`, query: { view: 'graph' } }) }
function togglePause() { userPaused.value = !userPaused.value; applyMotion() }
function onWide() { wide.value = wideQuery.matches }
function onReduced() { reduced.value = reducedQuery.matches }
function onVisibility() { applyMotion() }
function watchHeader() {
  seeing?.disconnect()
  sizing?.disconnect()
  const header = column.value?.closest('header')
  if (!header) return
  const root = document.getElementById('main')
  seeing = new IntersectionObserver(([entry]) => { occluded.value = !entry.isIntersecting; applyMotion() }, { root: root instanceof HTMLElement ? root : null, threshold: 0 })
  seeing.observe(header)
  sizing = new ResizeObserver(measureRegion)
  for (const el of [column.value, column.value?.parentElement?.querySelector('.head-main')]) if (el) sizing.observe(el)
  measureRegion()
}
async function load() {
  request?.abort()
  paint.value = false
  if (!mounted || !idle.value || !allowed.value) return
  const controller = request = new AbortController()
  try {
    const result = await fetchTicketGraph(props.projectId, true, controller.signal)
    if (controller.signal.aborted || !allowed.value) return
    if (!headerGlimpseGraphReady(result.nodes.length, result.links.length)) return
    data.value = ticketGraphData(result, props.projectKey)
    paint.value = true
    await nextTick()
    watchHeader()
    applyMotion()
  } catch { /* A missing graph leaves the header as it was. */ }
}
watch(paint, value => emit('active', value))
watch([allowed, idle, () => props.projectId, () => props.projectKey], () => { void load() })
onMounted(() => {
  mounted = true
  wideQuery.addEventListener('change', onWide)
  reducedQuery.addEventListener('change', onReduced)
  document.addEventListener('visibilitychange', onVisibility)
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
  wideQuery.removeEventListener('change', onWide)
  reducedQuery.removeEventListener('change', onReduced)
  document.removeEventListener('visibilitychange', onVisibility)
  emit('active', false)
})
</script>

<template>
  <div v-if="paint" ref="column" class="glimpse-col" data-header-glimpse="on">
    <div class="glimpse-canvas" :style="region" aria-hidden="true">
      <GraphCanvas ref="canvas" glimpse layout-bias="elliptic" :data="data" :fps="fps" canvas-class="header-glimpse-canvas" />
    </div>
    <div class="glimpse-hit" @click="openGraph" />
    <div class="glimpse-controls">
      <button type="button" class="btn sm" @click="openGraph"><AppIcon name="graph" :size="13" />Open graph</button>
      <button type="button" class="btn sm" :aria-pressed="userPaused" :aria-label="userPaused ? 'Resume motion' : 'Pause motion'" @click="togglePause"><AppIcon :name="userPaused ? 'play' : 'pause'" :size="13" />{{ userPaused ? 'Resume' : 'Pause' }}</button>
    </div>
  </div>
  <span v-else data-header-glimpse="off" hidden />
</template>

<style scoped>
.glimpse-col { position: relative; min-width: 0; min-height: 112px; overflow: hidden; }
.glimpse-canvas {
  position: absolute; left: 8px; right: 8px; opacity: .35; pointer-events: none;
  -webkit-mask-image: radial-gradient(ellipse 50% 50% at center, #000 25%, #0009 52%, transparent 86%);
  -webkit-mask-repeat: no-repeat;
  -webkit-mask-size: 100% 100%;
  mask-image: radial-gradient(ellipse 50% 50% at center, #000 25%, #0009 52%, transparent 86%);
  mask-repeat: no-repeat;
  mask-size: 100% 100%;
}
:global(:root[data-theme="dark"] .glimpse-canvas) { opacity: .30; }
@media (prefers-color-scheme: dark) { :global(:root:not([data-theme="light"]) .glimpse-canvas) { opacity: .30; } }
.glimpse-hit { position: absolute; inset: 0; z-index: 1; cursor: pointer; }
.glimpse-controls {
  position: absolute; z-index: 2; left: 50%; bottom: 6px; transform: translateX(-50%);
  display: flex; gap: 6px; padding: 4px; border-radius: 999px; pointer-events: none; opacity: 0;
  background: color-mix(in srgb, var(--surface-raised) 92%, transparent); box-shadow: var(--shadow);
}
.glimpse-col:hover .glimpse-controls, .glimpse-col:focus-within .glimpse-controls { opacity: 1; pointer-events: auto; }
.glimpse-controls .btn { height: 26px; padding: 0 10px; font-size: 12px; }
</style>
