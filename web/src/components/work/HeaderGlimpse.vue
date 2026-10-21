<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, onMounted, ref, shallowRef, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import GraphCanvas from '../graph/GraphCanvas.vue'
import AppIcon from '../AppIcon.vue'
import { projectSection } from './projectNavigation'
import { GLIMPSE_CLEAR_SELECTOR, GLIMPSE_MIN_WIDTH, glimpsePlaceControls, glimpseTextMask, loadTicketGraphContext, headerGlimpseAllowed, headerGlimpseGraphReady, headerGlimpsePaintClears, headerGlimpseViewportWide, nextHeaderGlimpseWide } from '../../lib/headerGlimpse'
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
const wideMedia = `(min-width: ${GLIMPSE_MIN_WIDTH}px)`
const wideQuery = window.matchMedia(wideMedia)
const reducedQuery = window.matchMedia('(prefers-reduced-motion: reduce)')
const visualViewport = window.visualViewport
let observedViewportWidth = Infinity
const wide = ref(headerGlimpseViewportWide(wideQuery.matches, viewportWidth())), reduced = ref(reducedQuery.matches), idle = ref(false)
const paint = ref(false), userPaused = ref(false), occluded = ref(false)
const data = shallowRef<GraphData>({ nodes: [], links: [] })
const column = ref<HTMLElement>()
const region = ref({ top: '0px', height: '0px' })
const mask = ref('none'), measured = ref(false), measuredViewport = ref(0)
const controls = ref<HTMLElement>(), controlSpot = ref<{ left: string; top: string; width: string; height: string } | null>(null)
const compact = ref(false), spotLabel = ref('none')
const shown = ref(0), truncated = ref(false)
const canvas = ref<{ setPaused: (value: boolean) => void } | null>(null)
const allowed = computed(() => headerGlimpseAllowed({ enabled: props.enabled, reduced: reduced.value, wide: wide.value, tickets: props.ticketCount }))
let request: AbortController | undefined, idleId = 0, seeing: IntersectionObserver | undefined, sizing: ResizeObserver | undefined, mounted = false, changes: MutationObserver | undefined, foldWatch: MutationObserver | undefined, measureFrame = 0, sizingPaused = false
let scrollRoot: HTMLElement | undefined

function controlBox(compactMode: boolean) {
  const el = controls.value
  if (!el) return { width: 310, height: 34 }
  // Read both widths in this turn. The observer is told to ignore the trial
  // sizes; reconnecting it would schedule a new pass on every measurement.
  el.classList.toggle('is-compact', compactMode)
  el.classList.remove('is-parked')
  return { width: el.offsetWidth || 310, height: el.offsetHeight || 34 }
}
function measureRegion() {
  const header = column.value?.closest<HTMLElement>('.project-head')
  const page = header?.closest<HTMLElement>('.project-page')
  if (!header || !page || !column.value) return
  sizingPaused = true
  try {
    const box = header.getBoundingClientRect()
    const padding = parseFloat(getComputedStyle(page).paddingTop) || 0
    // The marker retains its content position when the toolbar becomes sticky.
    const boundary = page.querySelector('.stick-mark')?.getBoundingClientRect()
    const bottom = boundary?.top ?? box.bottom
    const top = box.top - padding, height = Math.max(0, bottom - top)
    region.value = { top: `${-padding}px`, height: `${height}px` }
    const boxes = [...page.querySelectorAll<HTMLElement>(GLIMPSE_CLEAR_SELECTOR)]
      .filter(el => el.getClientRects().length && !column.value?.contains(el))
      .map(el => { const rect = el.getBoundingClientRect(); return { x: rect.left - box.left, y: rect.top - top, width: rect.width, height: rect.height } })
      .filter(item => item.width > 0 && item.height > 0)
    mask.value = glimpseTextMask(box.width, height, boxes)
    const placed = glimpsePlaceControls(box.width, height, boxes, { full: controlBox(false), compact: controlBox(true) })
    compact.value = placed?.compact ?? false
    controls.value?.classList.toggle('is-compact', compact.value)
    controls.value?.classList.toggle('is-parked', !placed)
    controlSpot.value = placed ? { left: `${placed.spot.x}px`, top: `${placed.spot.y}px`, width: `${placed.spot.width}px`, height: `${placed.spot.height}px` } : null
    spotLabel.value = placed ? `${placed.compact ? 'compact ' : ''}${Math.round(placed.spot.x)},${Math.round(placed.spot.y)},${Math.round(placed.spot.width)},${Math.round(placed.spot.height)}` : 'none'
    measuredViewport.value = window.innerWidth
    measured.value = true
  } finally {
    sizingPaused = false
  }
  // The new height is in the DOM on the next flush. Read it then, not from the observer's queue.
  void nextTick(() => { if (mounted) syncOcclusion() })
}
function scheduleMeasure() {
  // Trial widths change the bar while a measurement is already reading it.
  // Resize/content changes invalidate both the mask and the hit target until
  // their next shared measurement; stale controls cannot cover new text.
  if (sizingPaused) return
  measured.value = false
  cancelAnimationFrame(measureFrame)
  measureFrame = requestAnimationFrame(measureRegion)
}

function applyMotion() { canvas.value?.setPaused(userPaused.value || occluded.value || document.hidden) }
function viewportWidth() {
  // ResizeObserver has a fresh layout even when Chromium's window width and
  // media-query hints still describe the preceding viewport. A narrow root
  // must clear the canvas in that delivery, including a queued wide change.
  return Math.min(window.innerWidth, document.documentElement.getBoundingClientRect().width, observedViewportWidth)
}
// Re-read a fresh query. The list from setup can miss `change` while the
// canvas is painting, and its matches flag can stay true after a shrink.
function readWide() {
  return headerGlimpseViewportWide(window.matchMedia(wideMedia).matches, viewportWidth())
}
function syncWide() {
  const next = readWide()
  if (wide.value !== next) wide.value = next
}
let viewportWatch: ResizeObserver | undefined, viewportFrame = 0, paintSample = 0, paintTask = 0
function followViewport() {
  syncWide()
  clearIfViewportLeftWide()
  if (viewportFrame) return
  let left = 2
  const tick = () => {
    syncWide()
    clearIfViewportLeftWide()
    viewportFrame = --left > 0 ? requestAnimationFrame(tick) : 0
  }
  viewportFrame = requestAnimationFrame(tick)
}
function applyDeliveredWide(mediaMatches: boolean) {
  const next = nextHeaderGlimpseWide({ mediaMatches, layoutWidth: viewportWidth() })
  if (wide.value !== next) wide.value = next
  if (next) scheduleMeasure()
}
function onWide(event: MediaQueryListEvent) {
  // A delivered media change is authoritative while innerWidth catches up by
  // one pixel. A queued matches:true must not turn the glimpse back on after
  // the layout has already left 1280. The next frames re-read the live query
  // and can only clear a wide that the layout no longer supports.
  applyDeliveredWide(event.matches)
  if (viewportFrame) return
  let left = 2
  const tick = () => {
    const next = nextHeaderGlimpseWide({ mediaMatches: window.matchMedia(wideMedia).matches, layoutWidth: viewportWidth() })
    if (!next && wide.value) wide.value = false
    viewportFrame = --left > 0 ? requestAnimationFrame(tick) : 0
  }
  viewportFrame = requestAnimationFrame(tick)
}
// #main's scroll and the observer can disagree. A delivery may carry the
// pre-scroll sample first, or the scroll may never be delivered. The box on
// screen is the pause decision. Collapsing the header or opening a full-page
// ticket hides the ancestor with v-show and leaves this canvas mounted with an
// empty box; that is occlusion. visibility:hidden still has a box. The height
// is written for the next flush, and the caller reads the box again after layout.
function backdropOutOfView() {
  const el = column.value
  const root = document.getElementById('main')
  if (!el || !(root instanceof HTMLElement)) return false
  // Collapsed density hides the fold. The absolute column can keep a box after
  // the fold's own border box is already empty, which is still occlusion.
  const page = el.closest('.project-page')
  if (page?.classList.contains('header-collapsed')) return true
  const fold = document.getElementById('project-header-fold')
  if (fold && getComputedStyle(fold).display === 'none') return true
  const box = el.getBoundingClientRect(), view = root.getBoundingClientRect()
  if (box.width <= 0 || box.height <= 0) return true
  const overlapW = Math.min(box.right, view.right) - Math.max(box.left, view.left)
  const overlapH = Math.min(box.bottom, view.bottom) - Math.max(box.top, view.top)
  return overlapW <= 0 || overlapH <= 0
}
function syncOcclusion() { occluded.value = backdropOutOfView(); applyMotion() }
function detachScroll() { scrollRoot?.removeEventListener('scroll', syncOcclusion); scrollRoot = undefined }
function openGraph() { void router.push({ path: `/p/${encodeURIComponent(props.projectKey)}/tickets`, query: { ...filtersToQuery(filters.value), view: 'graph' } }) }
function togglePause() { userPaused.value = !userPaused.value; applyMotion() }
function onReduced() { reduced.value = reducedQuery.matches }
function onVisibility() { applyMotion() }
function onResize() { followViewport(); scheduleMeasure() }
// The observers above can all stay silent while the canvas is painting.
// Keep reading until the paint ends, and only clear.
function clearIfViewportLeftWide() {
  if (!mounted || !wide.value) return
  // Clear-only: a one-pixel catch-up (1279 with a delivered widen) stays wide.
  // 390 and any layout that has left that pixel do not.
  const signal = { mediaMatches: window.matchMedia(wideMedia).matches, layoutWidth: viewportWidth() }
  if (headerGlimpsePaintClears(wide.value, signal)) wide.value = false
}
function checkPaintedViewport() {
  if (!mounted || !paint.value) return false
  clearIfViewportLeftWide()
  return true
}
// Paint restarts and the two-frame follow-up both drop the timeout chain while
// software GL is still on the previous width (CI: innerWidth stays wide for
// seconds, then updates with no new event). A false flicker used to stop this
// clock, and a later widen could not re-arm the dead id, so 1280 → 390 stayed
// painted. The interval is not tied to paint or to wide. It stays armed from
// mount until unmount; the clear itself no-ops while the viewport is still wide.
let narrowWatch = 0
function stopNarrowWatch() {
  if (!narrowWatch) return
  window.clearInterval(narrowWatch)
  narrowWatch = 0
}
function ensureNarrowWatch() {
  if (!mounted || narrowWatch) return
  narrowWatch = window.setInterval(clearIfViewportLeftWide, 250)
}
function clearPaintTask() {
  // setTimeout and setInterval share one id pool. A completed paint task can
  // leave its id in paintTask after the interval has reused it.
  if (paintTask && paintTask !== narrowWatch) clearTimeout(paintTask)
  paintTask = 0
}
function samplePaintedViewport() {
  paintSample = 0
  if (!checkPaintedViewport()) return
  paintSample = requestAnimationFrame(samplePaintedViewport)
}
// Software GL can stall animation frames along with every viewport delivery.
// A task still runs in that gap. One pending check while painted bounds the
// fallback work; ordinary events and paint frames keep their immediate path.
function samplePaintedViewportTask() {
  paintTask = 0
  if (!checkPaintedViewport()) return
  paintTask = window.setTimeout(samplePaintedViewportTask, 250)
}
function startPaintSample() {
  if (!mounted || !paint.value) return
  if (!paintSample) samplePaintedViewport()
  if (!paintTask) paintTask = window.setTimeout(samplePaintedViewportTask, 250)
}
function attachSizing() {
  const header = column.value?.closest('header'), page = header?.closest('.project-page')
  if (!sizing || !header || !page) return
  for (const el of [header, controls.value, ...page.querySelectorAll(GLIMPSE_CLEAR_SELECTOR)]) {
    if (!el || (el !== controls.value && column.value?.contains(el))) continue
    sizing.observe(el)
  }
}
function watchHeader() {
  seeing?.disconnect(); sizing?.disconnect(); changes?.disconnect(); foldWatch?.disconnect(); detachScroll()
  const header = column.value?.closest('header'), page = header?.closest('.project-page')
  if (!header || !page) return
  const root = document.getElementById('main')
  seeing = new IntersectionObserver(() => { syncOcclusion() }, { root: root instanceof HTMLElement ? root : null, threshold: 0 })
  seeing.observe(column.value!)
  if (root instanceof HTMLElement) { scrollRoot = root; root.addEventListener('scroll', syncOcclusion, { passive: true }) }
  // The header box moves when the viewport shrinks even if resize, the media
  // change and the document-element observer stay silent while the canvas paints.
  sizing = new ResizeObserver(() => { clearIfViewportLeftWide(); scheduleMeasure() })
  attachSizing()
  changes = new MutationObserver(records => {
    if (records.some(record => !column.value?.contains(record.target))) {
      attachSizing()
      scheduleMeasure()
    }
  })
  changes.observe(page, { subtree: true, childList: true, characterData: true })
  // Density writes a class on the page and display on the fold. Neither is a
  // child or character change, so the observer above stays silent.
  foldWatch = new MutationObserver(() => { scheduleMeasure(); syncOcclusion() })
  foldWatch.observe(page, { attributes: true, attributeFilter: ['class'] })
  const fold = document.getElementById('project-header-fold')
  if (fold) foldWatch.observe(fold, { attributes: true, attributeFilter: ['style'] })
  measureRegion()
  syncOcclusion()
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
watch(paint, value => {
  emit('active', value)
  cancelAnimationFrame(paintSample)
  paintSample = 0
  clearPaintTask()
  if (value) startPaintSample()
})
watch(wide, value => { if (value) ensureNarrowWatch() })
watch([allowed, idle, () => props.projectId, () => props.projectKey, () => JSON.stringify(filtersToQuery(filters.value))], () => { void load() })
onMounted(() => {
  mounted = true
  wideQuery.addEventListener('change', onWide)
  reducedQuery.addEventListener('change', onReduced)
  document.addEventListener('visibilitychange', onVisibility)
  window.addEventListener('resize', onResize)
  // Software GL can delay animation frames along with the layout viewport's
  // notifications. This independent delivery checks the same width gate now.
  visualViewport?.addEventListener('resize', onResize)
  viewportWatch = new ResizeObserver(entries => {
    // Keep the delivered layout sample: rereading window/media/geometry here
    // can still return the preceding wide frame. Body-only deliveries must
    // not overwrite the root's viewport width or revive a cleared glimpse.
    const viewport = entries.find(entry => entry.target === document.documentElement)
    if (viewport) observedViewportWidth = viewport.borderBoxSize[0]?.inlineSize ?? viewport.contentRect.width
    followViewport()
  })
  viewportWatch.observe(document.documentElement)
  // A delivery that names only the document element can be dropped while the
  // body box still changes with the viewport. Read that box too.
  if (document.body) viewportWatch.observe(document.body)
  syncWide()
  ensureNarrowWatch()
  const start = () => { if (mounted) idle.value = true }
  if (typeof window.requestIdleCallback === 'function') idleId = window.requestIdleCallback(start, { timeout: 1500 })
  else requestAnimationFrame(() => requestAnimationFrame(start))
})
onBeforeUnmount(() => {
  mounted = false
  request?.abort()
  if (idleId) window.cancelIdleCallback?.(idleId)
  seeing?.disconnect()
  detachScroll()
  sizing?.disconnect()
  changes?.disconnect()
  foldWatch?.disconnect()
  cancelAnimationFrame(measureFrame)
  cancelAnimationFrame(viewportFrame)
  cancelAnimationFrame(paintSample)
  clearPaintTask()
  stopNarrowWatch()
  viewportWatch?.disconnect()
  window.removeEventListener('resize', onResize)
  visualViewport?.removeEventListener('resize', onResize)
  wideQuery.removeEventListener('change', onWide)
  reducedQuery.removeEventListener('change', onReduced)
  document.removeEventListener('visibilitychange', onVisibility)
  emit('active', false)
})
</script>

<template>
  <div v-if="paint" ref="column" class="glimpse-col" :style="region" :data-measured="measured" :data-measured-viewport="measuredViewport" :data-spot="spotLabel" data-header-glimpse="on" :data-shown="shown">
    <div class="glimpse-canvas" :style="{ '--glimpse-text-mask': mask }" aria-hidden="true">
      <GraphCanvas ref="canvas" glimpse layout-bias="elliptic" :data="data" :fps="fps" canvas-class="header-glimpse-canvas" />
    </div>
    <div v-if="controlSpot" class="glimpse-hit" :style="controlSpot" @click="openGraph" />
    <div ref="controls" class="glimpse-controls" :class="{ 'is-compact': compact, 'is-parked': !controlSpot }" :style="controlSpot ? { left: controlSpot.left, top: controlSpot.top } : undefined">
      <span class="glimpse-count" :title="truncated ? 'Graph limit reached; some matching tickets are not shown' : 'Tickets in the current context'">{{ shown }}{{ truncated ? '+' : '' }} / {{ ticketCount }}</span>
      <button type="button" class="btn sm" aria-label="Open graph" @click="openGraph"><AppIcon name="graph" :size="13" /><span class="glimpse-label">Open graph</span></button>
      <button type="button" class="btn sm" :aria-pressed="userPaused" :aria-label="userPaused ? 'Resume motion' : 'Pause motion'" @click="togglePause"><AppIcon :name="userPaused ? 'play' : 'pause'" :size="13" /><span class="glimpse-label">{{ userPaused ? 'Resume' : 'Pause' }}</span></button>
    </div>
  </div>
  <span v-else data-header-glimpse="off" hidden />
</template>

<style scoped>
/* The header owns the backdrop; the graph never takes a layout column. */
:global(.project-head:has(.glimpse-col)) { position: relative; }
:global(.head-flex.with-glimpse:has(.glimpse-col)) { display: flex; align-items: flex-end; }
.glimpse-col { position: absolute; left: 0; right: 0; overflow: hidden; pointer-events: none; }
/* The fold can be hidden with an empty border box while this absolute column
   still stretches across the page. Both axes have to be empty (AEON-1622). */
:global(.project-page.header-collapsed) .glimpse-col {
  width: 0 !important; height: 0 !important; min-width: 0 !important; min-height: 0 !important;
  right: auto !important; overflow: hidden !important;
}
.glimpse-col[data-measured="false"] { visibility: hidden; }
.glimpse-canvas {
  position: absolute; inset: 0; opacity: .35; pointer-events: none;
  mask-image: var(--glimpse-text-mask), linear-gradient(to right, transparent 8px, #000 4%, #000 96%, transparent calc(100% - 8px)), linear-gradient(to bottom, transparent, #000 10%, #000 88%, transparent);
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
.glimpse-controls.is-parked { left: 0; top: 0; width: 0; height: 0; padding: 0; overflow: hidden; visibility: hidden; }
.glimpse-controls.is-compact .glimpse-label { position: absolute; width: 1px; height: 1px; padding: 0; margin: -1px; overflow: hidden; clip-path: inset(50%); white-space: nowrap; border: 0; }
:global(.project-head:hover .glimpse-controls), .glimpse-col:focus-within .glimpse-controls { opacity: 1; pointer-events: auto; }
.glimpse-controls .btn { height: 26px; padding: 0 10px; font-size: 12px; }
.glimpse-count { padding: 0 6px 0 8px; font: 10px var(--mono); color: var(--ink-3); white-space: nowrap; }
</style>
