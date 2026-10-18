<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import GraphControls from './GraphControls.vue'
import { createGraphRenderer, type GraphData, type GraphDimension, type GraphFPS, type GraphLabels, type GraphNode, type GraphRenderer, type MotionPhase } from '../../lib/graphRenderer'
import { useGraphMotion } from '../../lib/graphMotion'
import { exitGraphFullscreen, requestGraphFullscreen } from '../../lib/graphFocus'

// TG1: supply GraphData, a tenant/principal viewerKey and selectedId. Selection
// and open events return adapter node fields (href is advisory, never auto-
// navigated). Slots add domain-specific toolbar, overlay and footer content.
// fps defaults to 60; updates emit update:fps. focusQuery defaults to ?focus=1.
// Ticket views opt into single-click opening, suspend canvas keys behind their
// panel, and may lower minStageHeight. Knowledge keeps all existing defaults.
// glimpse is a fitted decoration: no chrome, labels, keyboard or pointer capture.
// The host page supplies the mask, the hit target and when to pause or dispose.
// Like QuoteWorkspace, one mounted workspace serves normal/full-area layouts;
// rarely used settings stay collapsed and all canvas/camera state survives.
const props = withDefaults(defineProps<{
  data: GraphData; viewerKey?: string; title?: string; summary?: string; selectedId?: string
  matches?: Set<string>; searching?: boolean; fps?: GraphFPS; focusQuery?: string; canvasClass?: string
  openOnClick?: boolean; keyboardActive?: boolean; minStageHeight?: number; glimpse?: boolean; layoutBias?: 'elliptic'
}>(), { title: 'Graph', summary: '', selectedId: '', searching: false, fps: 60, focusQuery: 'focus', canvasClass: '', openOnClick: false, keyboardActive: true, minStageHeight: 480, glimpse: false })
const emit = defineEmits<{ select: [node: GraphNode, via?: 'pointer' | 'key']; open: [node: GraphNode]; hover: [node: GraphNode | null]; clear: []; 'update:fps': [fps: GraphFPS]; pointer: [event: PointerEvent] }>()
const route = useRoute(), router = useRouter()
const root = ref<HTMLElement>(), host = ref<HTMLElement>(), stage = ref<HTMLElement>(), header = ref<HTMLElement>(), footer = ref<HTMLElement>()
const media = window.matchMedia('(prefers-reduced-motion: reduce)'), scheme = window.matchMedia('(prefers-color-scheme: dark)')
const dimension = ref<GraphDimension>(media.matches ? '2d' : '3d'), paused = ref(media.matches)
const ready = ref(false), labelsReady = ref(false), error = ref(''), fallback = ref(false), labels = ref<GraphLabels>('smart'), rate = ref<GraphFPS>(props.fps)
const hovered = ref(''), phase = ref<MotionPhase>(paused.value ? 'paused' : 'orbiting'), height = ref(480)
const focused = computed(() => !props.glimpse && route.query[props.focusQuery] === '1')
// Orbit period is an account preference, not a canvas prop, so knowledge,
// tickets and the project glimpse all follow the one setting.
const { seconds: orbitSeconds } = useGraphMotion()
const storageKey = computed(() => props.viewerKey ? `aeon:graph:${props.viewerKey}:labels` : '')
const surfaceLabel = computed(() => `${props.title}: ${props.summary}. Left and right arrows select nodes; Enter opens; Escape ${focused.value ? 'exits focus' : 'clears selection'}.`)
let renderer: GraphRenderer | null = null, controller: AbortController | undefined, resize: ResizeObserver | undefined, theme: MutationObserver | undefined
let focusFrame = 0
let mounted = false, beforeFocus: HTMLElement | null = null
let requestNativeFocus = false, nativeFocused = false
let placing = false, pointers = 0, holdScroll = false, userMoved = false
let background: { el: HTMLElement; inert: boolean }[] = []
function loadLabels(key: string, previous?: string) {
  // An empty key is "the person is not known yet", not "this person prefers Smart".
  // Reading then would keep the default and, on the next person, could miss theirs.
  if (!key) return
  let saved: string | null = null
  try { saved = localStorage.getItem(key) } catch { return }
  if (saved === 'all' || saved === 'off') labels.value = saved
  else if (previous) labels.value = 'smart'
}
function setLabels(value: GraphLabels) {
  labels.value = value
  try { if (storageKey.value) localStorage.setItem(storageKey.value, value) } catch { /* Preference remains available for this visit. */ }
}
function emphasis() {
  const neighbours = new Set<string>([props.selectedId])
  for (const link of props.data.links) {
    if (link.source === props.selectedId) neighbours.add(link.target)
    if (link.target === props.selectedId) neighbours.add(link.source)
  }
  renderer?.emphasis({ selected: props.selectedId, neighbours, matches: props.matches ?? new Set(), searching: props.searching, hovered: hovered.value })
}
function setPaused(value: boolean) { paused.value = value; renderer?.motion(value) }
function footerInset() {
  return parseFloat(getComputedStyle(document.documentElement).getPropertyValue('--footer-h')) || 0
}
function measure(immediate = false) {
  if (props.glimpse) { if (host.value) renderer?.resize(host.value.clientWidth, host.value.clientHeight); return }
  if (!stage.value || !root.value) return
  root.value.style.setProperty('--graph-overlay-top', `${(header.value?.offsetHeight ?? 0) + 34}px`)
  root.value.style.setProperty('--graph-overlay-bottom', `${(footer.value?.offsetHeight ?? 0) + 28}px`)
  if (!focused.value) {
    // Measure actual page chrome instead of a fixed project-header estimate.
    // The placement loop writes the height itself so it can read the box in
    // this turn. A resize observer must not: writing it there loops.
    const next = Math.max(props.minStageHeight, window.innerHeight - root.value.getBoundingClientRect().top - (header.value?.offsetHeight ?? 0) - (footer.value?.offsetHeight ?? 0) - footerInset() - 12)
    if (next !== height.value) height.value = next
    if (immediate && stage.value.style.height !== `${next}px`) stage.value.style.height = `${next}px`
  }
  if (host.value) renderer?.resize(host.value.clientWidth, host.value.clientHeight)
}
// A tall header (the phone work bar wraps onto its own rows) clamps the stage
// to its minimum and the rest continues below the viewport. Fit then centres
// the node on that off-screen stage, so a tap at the canvas centre misses.
// Bring the centre back above the footer, then measure again into the room
// that is actually on screen. A later header growth scrolls only; it does not
// resize under a pointer, and a person scrolling away is left where they are.
function stageCentreHidden() {
  const el = stage.value
  if (!el || focused.value || props.glimpse) return false
  const rect = el.getBoundingClientRect()
  if (rect.width < 2 || rect.height < 2) return false
  const centre = rect.top + rect.height / 2
  return centre < 8 || centre > window.innerHeight - footerInset() - 8
}
function scrollParent(el: HTMLElement) {
  for (let node = el.parentElement; node; node = node.parentElement) {
    const overflow = getComputedStyle(node).overflowY
    if ((overflow === 'auto' || overflow === 'scroll') && node.scrollHeight > node.clientHeight + 1) return node
  }
  return null
}
function revealStageCentre() {
  const el = stage.value
  if (!el || pointers) return false
  const rect = el.getBoundingClientRect()
  const centre = rect.top + rect.height / 2
  const low = 8, high = window.innerHeight - footerInset() - 8
  const delta = centre < low ? centre - low : centre > high ? centre - high : 0
  const scroller = delta ? scrollParent(el) : null
  if (!scroller) return false
  const before = scroller.scrollTop
  holdScroll = true
  scroller.scrollTop = before + delta
  holdScroll = false
  return scroller.scrollTop !== before
}
function placeStageNow() {
  if (placing || !mounted) return
  // Nothing to place for a glimpse, focus or a held pointer, but a new renderer
  // still starts at the window's size until it is measured into its host.
  if (pointers || props.glimpse || focused.value || !stage.value) { measure(); return }
  placing = true
  try {
    for (let pass = 0; pass < 3; pass++) {
      measure(true)
      if (!stage.value || !stageCentreHidden()) return
      if (!revealStageCentre()) return
    }
    measure(true)
    revealStageCentre()
  } finally { placing = false }
}
function settleStage() {
  // One follow-up after the header finishes its own layout. A body observer
  // loops: measuring the stage changes the page box that the observer watches.
  const follow = () => { if (!placing && !pointers && !userMoved && mounted && stageCentreHidden()) revealStageCentre() }
  if (typeof requestIdleCallback === 'function') requestIdleCallback(follow, { timeout: 700 })
  else setTimeout(follow, 0)
}
function pointerDown() { pointers++ }
function pointerUp() { pointers = Math.max(0, pointers - 1) }
function onUserScroll() { if (!holdScroll && !placing) userMoved = true }
function onResize() {
  measure()
  if (!placing && !pointers && !props.glimpse && !focused.value && stageCentreHidden()) revealStageCentre()
}
async function start() {
  controller?.abort(); renderer?.dispose(); renderer = null; ready.value = false; labelsReady.value = false
  const request = controller = new AbortController()
  await nextTick()
  if (!host.value || !mounted || request.signal.aborted) return
  error.value = ''
  try {
    const next = await createGraphRenderer(host.value, dimension.value, {
      reduced: media.matches, signal: request.signal, fps: rate.value, labels: props.glimpse ? 'off' : labels.value, glimpse: props.glimpse, layoutBias: props.layoutBias,
      select: n => { if (props.glimpse) return; select(n, 'pointer'); if (props.openOnClick) emit('open', n) }, open: n => { if (!props.glimpse) emit('open', n) }, clear: () => { if (!props.glimpse) emit('clear') },
      hover: n => { if (props.glimpse) return; hovered.value = n?.id ?? ''; emit('hover', n); emphasis() }, motionState: value => { phase.value = value },
      labelsSettled: value => { if (request.signal.aborted || !mounted) return; labelsReady.value = value },
    })
    if (!next) return
    if (request.signal.aborted || !mounted) { next.dispose(); return }
    renderer = next
    if (dimension.value === '3d' && next.dimension === '2d') { fallback.value = true; dimension.value = '2d' }
    renderer.motion(paused.value); renderer.data(props.data); emphasis(); placeStageNow(); settleStage()
    if (request.signal.aborted || !mounted || renderer !== next) return
    ready.value = true
    if (props.selectedId) renderer.focus(props.selectedId)
  } catch { if (!request.signal.aborted) error.value = 'The graph could not start. You can still explore every entry in the list.' }
}
function select(node: GraphNode, via: 'pointer' | 'key' = 'key') { renderer?.interact(); emit('select', node, via); renderer?.focus(node.id); host.value?.focus({ preventScroll: true }) }
function fit() { renderer?.interact(); renderer?.fit() }
function setDimension(value: GraphDimension) { if (dimension.value !== value) { dimension.value = value; hovered.value = ''; emit('hover', null); void start() } }
function toggleMotion() { paused.value = !paused.value; renderer?.motion(paused.value) }
function setFPS(value: GraphFPS) { rate.value = value; emit('update:fps', value) }
async function toggleFocus() {
  requestNativeFocus = !focused.value
  const failure = await router.replace({ query: { ...route.query, [props.focusQuery]: focused.value ? undefined : '1' }, hash: route.hash })
  if (failure) requestNativeFocus = false
}
function fullscreenChanged() {
  const wasNative = nativeFocused
  nativeFocused = document.fullscreenElement === root.value
  // Native Escape is handled by the browser before our keyboard listener.
  if (wasNative && !nativeFocused && focused.value) void toggleFocus()
  measure()
}
function unlock() { for (const { el, inert } of background) el.inert = inert; background = [] }
async function syncFocus(value: boolean) {
  const tryNative = value && requestNativeFocus
  requestNativeFocus = false
  if (!value && root.value) void exitGraphFullscreen(root.value)
  if (value) beforeFocus = document.activeElement instanceof HTMLElement && root.value?.contains(document.activeElement) ? document.activeElement : host.value ?? null
  unlock(); await nextTick()
  if (!mounted || value !== focused.value) return
  if (value && root.value) {
    // Teleport hides the shell visually; inert also hides its controls from Tab
    // and assistive technology. Restore each prior value on exit/unmount.
    background = [...document.body.children].filter((el): el is HTMLElement => el instanceof HTMLElement && el !== root.value && !['SCRIPT', 'STYLE', 'LINK'].includes(el.tagName)).map(el => ({ el, inert: el.inert }))
    for (const { el } of background) el.inert = true
    host.value?.focus({ preventScroll: true })
  } else {
    cancelAnimationFrame(focusFrame)
    focusFrame = requestAnimationFrame(() => { if (mounted) (beforeFocus?.isConnected ? beforeFocus : host.value)?.focus({ preventScroll: true }) })
  }
  measure()
  if (!value && stageCentreHidden()) revealStageCentre()
  // Wait for Teleport to settle: moving a fullscreen element would exit it.
  // Deep links use in-app focus directly; native fullscreen needs a gesture.
  if (tryNative && root.value) void requestGraphFullscreen(root.value, () => mounted && focused.value)
}
function keydown(event: KeyboardEvent) {
  if (props.glimpse || event.defaultPrevented || !props.keyboardActive) return
  if (event.key === 'Escape' && focused.value) {
    event.preventDefault(); event.stopImmediatePropagation(); toggleFocus(); return
  }
  if (event.key === 'Tab' && focused.value && root.value) {
    const controls = [...root.value.querySelectorAll<HTMLElement>('button:not(:disabled), select, summary, [tabindex="0"]')].filter(el => el.getClientRects().length)
    const first = controls[0], last = controls.at(-1)
    if (event.shiftKey && document.activeElement === first) { event.preventDefault(); last?.focus() }
    else if (!event.shiftKey && document.activeElement === last) { event.preventDefault(); first?.focus() }
  }
  if (event.ctrlKey || event.metaKey || event.altKey || (event.target instanceof HTMLElement && event.target.closest('input, textarea, select, [contenteditable="true"], dialog'))) return
  if (event.key === 'Escape' && props.selectedId) { event.preventDefault(); emit('clear'); host.value?.focus({ preventScroll: true }) }
  if (event.target !== host.value) return
  const selected = props.data.nodes.find(n => n.id === props.selectedId)
  if (event.key === 'Enter' && selected) { event.preventDefault(); emit('open', selected) }
  if (['ArrowRight', 'ArrowLeft'].includes(event.key) && props.data.nodes.length) {
    const at = props.data.nodes.findIndex(n => n.id === props.selectedId), list = props.data.nodes
    const node = list[(at + (event.key === 'ArrowRight' ? 1 : -1) + list.length) % list.length]
    if (node) { event.preventDefault(); select(node) }
  }
}
function onReduced() { paused.value = media.matches; if (media.matches) dimension.value = '2d'; void start() }
function retheme() { renderer?.theme() }
watch(storageKey, (key, previous) => { if (!props.glimpse) loadLabels(key, previous) }, { immediate: true })
watch(labels, value => { if (!props.glimpse) renderer?.labels(value) })
watch(() => props.fps, value => { rate.value = value })
watch(rate, value => renderer?.frameRate(value))
watch(() => props.data, value => { hovered.value = ''; emit('hover', null); renderer?.data(value); emphasis() })
watch([() => props.selectedId, () => props.matches, () => props.searching], emphasis)
watch(() => props.selectedId, id => { if (id) { renderer?.interact(); renderer?.focus(id) } })
watch(focused, syncFocus)
onMounted(() => {
  mounted = true
  resize = new ResizeObserver(() => { if (!placing) measure() })
  for (const el of [root.value, host.value, header.value, footer.value]) if (el) resize.observe(el)
  if (!props.glimpse) {
    window.addEventListener('pointerdown', pointerDown, true)
    window.addEventListener('pointerup', pointerUp, true)
    window.addEventListener('pointercancel', pointerUp, true)
    window.addEventListener('scroll', onUserScroll, true)
  }
  theme = new MutationObserver(retheme); theme.observe(document.documentElement, { attributes: true, attributeFilter: ['data-theme', 'style', 'class'] })
  media.addEventListener('change', onReduced); scheme.addEventListener('change', retheme)
  window.addEventListener('resize', onResize); if (!props.glimpse) window.addEventListener('keydown', keydown, true)
  if (!props.glimpse) document.addEventListener('fullscreenchange', fullscreenChanged)
  if (focused.value) void syncFocus(true)
  measure(); void start()
})
onBeforeUnmount(() => {
  mounted = false; document.removeEventListener('fullscreenchange', fullscreenChanged)
  if (root.value) void exitGraphFullscreen(root.value)
  cancelAnimationFrame(focusFrame); controller?.abort(); resize?.disconnect(); theme?.disconnect(); renderer?.dispose(); unlock()
  media.removeEventListener('change', onReduced); scheme.removeEventListener('change', retheme)
  window.removeEventListener('resize', onResize); window.removeEventListener('keydown', keydown, true)
  window.removeEventListener('pointerdown', pointerDown, true); window.removeEventListener('pointerup', pointerUp, true); window.removeEventListener('pointercancel', pointerUp, true)
  window.removeEventListener('scroll', onUserScroll, true)
})
defineExpose({ focus: () => host.value?.focus({ preventScroll: true }), focusNode: (id: string) => renderer?.focus(id), interact: () => renderer?.interact(), setPaused })
</script>
<template>
  <div v-if="glimpse" ref="root" class="graph-glimpse">
    <div ref="host" class="graph-surface" :class="canvasClass" aria-hidden="true" data-glimpse="" :data-dimension="dimension" :data-ready="ready" :data-labels-ready="labelsReady" :data-motion="paused ? 'still' : 'on'" :data-motion-phase="phase" data-labels="off" :data-fps="rate" :data-orbit-seconds="orbitSeconds" />
  </div>
  <Teleport v-else to="body" :disabled="!focused">
    <section ref="root" class="graph-viewer" :class="{ 'graph-focus': focused }" :role="focused ? 'dialog' : undefined" :aria-modal="focused ? true : undefined" :aria-label="title" :data-focus="focused">
      <header ref="header" class="graph-header"><div class="graph-heading"><h2>{{ title }}</h2><p role="status">{{ summary }}</p></div><GraphControls :dimension="dimension" :paused="paused" :fallback="fallback" :labels="labels" :fps="rate" :focus="focused" @fit="fit" @dimension="setDimension" @pause="toggleMotion" @labels="setLabels" @fps="setFPS" @focus="toggleFocus"><slot name="controls" /></GraphControls></header>
      <div ref="stage" class="graph-stage" @pointermove="emit('pointer', $event)" :style="focused ? undefined : { height: `${height}px`, minHeight: `${minStageHeight}px` }">
        <div ref="host" class="graph-surface" :class="canvasClass" role="img" tabindex="0" :aria-label="surfaceLabel" :data-dimension="dimension" :data-ready="ready" :data-labels-ready="labelsReady" :data-viewer="viewerKey ?? ''" :data-motion="paused ? 'still' : 'on'" :data-motion-phase="phase" :data-labels="labels" :data-fps="rate" :data-orbit-seconds="orbitSeconds" />
        <div v-if="error" class="graph-error" role="alert"><p>{{ error }}</p><button type="button" class="btn" @click="start">Try again</button></div>
        <slot :dimension="dimension" :focused="focused" />
      </div>
      <footer ref="footer" class="graph-footer"><slot name="footer" /><p v-if="fallback">3D is unavailable here. The 2D graph has the same entries and controls.</p></footer>
    </section>
  </Teleport>
</template>
<style scoped>
.graph-glimpse { position: absolute; inset: 0; min-width: 0; pointer-events: none; background: transparent; }
.graph-glimpse .graph-surface { position: absolute; inset: 0; width: auto; height: auto; background: transparent; }
.graph-viewer { position: relative; min-width: 0; background: var(--canvas); color: var(--ink); border-radius: var(--radius); box-shadow: var(--shadow); }
.graph-header { display: flex; justify-content: space-between; flex-wrap: wrap; align-items: center; gap: 16px; padding: 22px 24px 16px; position: relative; z-index: 5; }
.graph-heading h2 { font-size: 17px; font-weight: 580; letter-spacing: -.025em; }
.graph-heading p { font: 11px/1.6 var(--mono); color: var(--ink-3); margin-top: 5px; }
.graph-stage { position: relative; min-height: 480px; min-width: 0; overflow-anchor: none; }
.graph-surface { position: relative; width: 100%; height: 100%; overflow: hidden; outline: none; }
.graph-surface:focus-visible { box-shadow: inset 0 0 0 2px var(--teal); border-radius: 8px; }
.graph-surface :deep(canvas) { display: block; }
.graph-surface :deep(.graph-labels) { position: absolute; inset: 0; pointer-events: none; overflow: hidden; }
.graph-surface :deep(.graph-label) { position: absolute; top: 0; left: 0; white-space: nowrap; font: 500 11px/16px var(--font); padding: 2px 7px; color: var(--ink); background: color-mix(in srgb, var(--surface-raised) 88%, transparent); border: 1px solid var(--line); border-radius: 12px; transition: opacity 0.7s ease-in-out; }
.graph-surface :deep(.graph-label[hidden]) { display: none; }
@media (prefers-reduced-motion: reduce) { .graph-surface :deep(.graph-label) { transition: none; } }
.graph-footer { padding: 16px 24px 20px; }
.graph-footer > p { font-size: 11.5px; color: var(--ink-3); margin-top: 10px; }
.graph-error { position: absolute; inset: 0; display: grid; place-content: center; justify-items: center; gap: 16px; padding: 24px; background: var(--canvas); }
.graph-focus { position: fixed; inset: 0; z-index: 120; border-radius: 0; height: 100dvh; overflow: hidden; }
.graph-focus .graph-stage { height: 100%; min-height: 0; }
.graph-focus .graph-header { position: absolute; top: 18px; left: 22px; right: 22px; padding: 12px 16px; border: 1px solid var(--line); border-radius: 16px; background: color-mix(in srgb, var(--surface-raised) 90%, transparent); backdrop-filter: blur(16px); box-shadow: var(--shadow-pop); }
.graph-focus .graph-footer { position: absolute; bottom: 16px; left: 22px; right: 22px; padding: 10px 14px; border-radius: 12px; background: color-mix(in srgb, var(--surface-raised) 88%, transparent); pointer-events: none; }
@media (max-width: 1100px) { .graph-header { padding: 18px 18px 10px; gap: 12px; } .graph-footer { padding: 14px 18px; } }
@media (max-width: 600px) { .graph-heading h2 { font-size: 16px; } .graph-focus .graph-header { top: 10px; left: 10px; right: 10px; padding: 10px; gap: 8px; } .graph-focus .graph-heading { display: none; } .graph-focus .graph-footer { left: 10px; right: 10px; bottom: 10px; } }
</style>
