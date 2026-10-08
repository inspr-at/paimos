<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, reactive, ref, watch } from 'vue'
import { contentUrl, createHTMLPreview, fileKind, fileSize, hasThumbnail, isHTML, isImage, markdownRef, type Attachment } from '../../lib/attachments'
import { absoluteTime } from '../../lib/work'
import { toast } from '../../lib/toast'
import AppIcon from '../AppIcon.vue'

// The attachment viewer in the INSPR flow language: a dark stage, a glass bar,
// a filmstrip through every attachment, fit / 100% / zoom with pan, and compare
// for two screens (side by side, a slider, or onion skin for before and after).
// Thumbs in the strip, the preview size on the stage, the original only at 100%+.
const props = defineProps<{ items: Attachment[]; ticketKey: string; canWrite: boolean; setCaption: (item: Attachment, caption: string) => Promise<boolean>; names?: Map<string, string> }>()
// Who added it: the name the server sent, or one the page already knows for that person.
const addedBy = (item: Attachment) => typeof item.created_by === 'string' ? props.names?.get(item.created_by) ?? '' : item.created_by?.name ?? ''
const emit = defineEmits<{ close: [] }>()
const dialog = ref<HTMLDialogElement>()
const stage = ref<HTMLElement>()
const index = ref(0)
const scale = ref(1)
const fitScale = ref(1)
const mode = ref<'fit' | 'free'>('fit')
const offset = ref({ x: 0, y: 0 })
const details = ref(false)
const compare = ref<null | { with: number; view: 'side' | 'slider' | 'onion' }>(null)
const slider = ref(0.5)
const onion = ref(0.5)
const caption = ref('')
const loaded = ref(false)
// The picture could not be read (its bytes are not stored here): say so instead of a broken image.
const unreadable = ref(false)
const brokenThumbs = reactive(new Set<string>())
let opener: HTMLElement | null = null
const htmlURL = ref('')
const htmlState = ref<'loading' | 'ready' | 'unavailable' | 'expired'>('unavailable')
let htmlRequest: AbortController | undefined
let htmlExpiry: ReturnType<typeof setTimeout> | undefined
let htmlGeneration = 0
let htmlSelection = ''
const selectedHTMLKey = () => [current.value?.id, current.value?.sha256, current.value?.updated_at].join(':')

function clearHTML() {
  htmlGeneration++
  htmlRequest?.abort()
  clearTimeout(htmlExpiry)
  htmlURL.value = ''
  htmlSelection = ''
}
async function loadHTML() {
  clearHTML()
  const item = current.value
  if (!item || !isHTML(item) || !dialog.value?.open) return
  htmlSelection = selectedHTMLKey()
  const generation = htmlGeneration
  htmlState.value = 'loading'
  htmlRequest = new AbortController()
  try {
    const preview = await createHTMLPreview(item.id, htmlRequest.signal)
    if (generation !== htmlGeneration) return
    if (!preview.available || !preview.url || !preview.expires_at) { htmlState.value = 'unavailable'; return }
    htmlURL.value = preview.url
    htmlState.value = 'ready'
    htmlExpiry = setTimeout(() => { clearHTML(); htmlState.value = 'expired' }, Math.max(0, Date.parse(preview.expires_at) - Date.now()))
  } catch {
    if (generation === htmlGeneration) htmlState.value = 'unavailable'
  }
}

const current = computed(() => props.items[index.value])
const other = computed(() => compare.value ? props.items[compare.value.with] : undefined)
const dims = computed(() => current.value?.width && current.value.height ? `${current.value.width} × ${current.value.height}` : '')
const percent = computed(() => Math.round(scale.value * 100))
// The original only once the image shows at its own size or larger.
const variant = computed(() => scale.value >= 0.999 ? 'original' : 'preview')

function open(id: string, withId?: string) {
  const at = props.items.findIndex(item => item.id === id)
  index.value = Math.max(0, at)
  opener = document.activeElement as HTMLElement
  compare.value = null
  if (withId) { const b = props.items.findIndex(item => item.id === withId); if (b !== -1 && b !== index.value) compare.value = { with: b, view: 'slider' } }
  dialog.value?.showModal()
  // Start on the stage, not on the first toolbar button.
  void nextTick(() => stage.value?.focus({ preventScroll: true }))
  reset()
}
function close() { clearHTML(); dialog.value?.close(); emit('close'); opener?.focus({ preventScroll: true }) }
function go(step: number) {
  if (!props.items.length) return
  index.value = (index.value + step + props.items.length) % props.items.length
  if (current.value && !isImage(current.value)) compare.value = null
  if (compare.value && compare.value.with === index.value) compare.value.with = (index.value + 1) % props.items.length
  reset()
}
function reset() { mode.value = 'fit'; offset.value = { x: 0, y: 0 }; loaded.value = false; unreadable.value = false; caption.value = current.value?.caption ?? ''; void loadHTML(); void nextTick(fit) }
// Fit: the whole image in the stage with a margin; never enlarged past 100%.
function fit() {
  const item = current.value, box = stage.value?.getBoundingClientRect()
  if (!item || !box) return
  const w = item.width ?? 1600, h = item.height ?? 1000
  const pad = compare.value?.view === 'side' ? 2 : 1
  const margin = box.width < 600 ? 24 : 64
  fitScale.value = Math.min(1, (box.width / pad - margin) / w, (box.height - 48) / h)
  if (mode.value === 'fit') { scale.value = fitScale.value; offset.value = { x: 0, y: 0 } }
}
function setScale(next: number, around?: { x: number; y: number }) {
  const clamped = Math.max(0.05, Math.min(8, next))
  if (around && stage.value) {
    const box = stage.value.getBoundingClientRect()
    const cx = around.x - box.left - box.width / 2 - offset.value.x, cy = around.y - box.top - box.height / 2 - offset.value.y
    const ratio = clamped / scale.value
    offset.value = { x: offset.value.x - cx * (ratio - 1), y: offset.value.y - cy * (ratio - 1) }
  }
  scale.value = clamped
  mode.value = Math.abs(clamped - fitScale.value) < 0.001 ? 'fit' : 'free'
  if (mode.value === 'fit') offset.value = { x: 0, y: 0 }
}
const zoomIn = () => setScale(scale.value * 1.25)
const zoomOut = () => setScale(scale.value / 1.25)
const actual = () => { setScale(1); offset.value = { x: 0, y: 0 } }
const toFit = () => { mode.value = 'fit'; fit() }

// Wheel: Ctrl/⌘ or a trackpad pinch zooms at the pointer; otherwise pans a zoomed image.
function wheel(event: WheelEvent) {
  if (!current.value || !isImage(current.value)) return
  event.preventDefault()
  if (event.ctrlKey || event.metaKey) setScale(scale.value * Math.exp(-event.deltaY * 0.01), { x: event.clientX, y: event.clientY })
  else if (mode.value === 'free') offset.value = { x: offset.value.x - event.deltaX, y: offset.value.y - event.deltaY }
}
// Drag to pan; two fingers pinch.
const pointers = new Map<number, { x: number; y: number }>()
let pan: { x: number; y: number; ox: number; oy: number } | null = null
let pinch: { distance: number; scale: number } | null = null
function down(event: PointerEvent) {
  if (!current.value || !isImage(current.value)) return
  if ((event.target as HTMLElement).closest('button, input, .slider-handle')) return
  pointers.set(event.pointerId, { x: event.clientX, y: event.clientY })
  ;(event.currentTarget as HTMLElement).setPointerCapture(event.pointerId)
  if (pointers.size === 2) {
    const [a, b] = [...pointers.values()]
    pinch = { distance: Math.hypot(a.x - b.x, a.y - b.y), scale: scale.value }
    pan = null
  } else pan = { x: event.clientX, y: event.clientY, ox: offset.value.x, oy: offset.value.y }
}
function moveTo(event: PointerEvent) {
  if (!pointers.has(event.pointerId)) return
  pointers.set(event.pointerId, { x: event.clientX, y: event.clientY })
  if (pinch && pointers.size === 2) {
    const [a, b] = [...pointers.values()]
    setScale(pinch.scale * Math.hypot(a.x - b.x, a.y - b.y) / pinch.distance, { x: (a.x + b.x) / 2, y: (a.y + b.y) / 2 })
  } else if (pan && (mode.value === 'free' || scale.value > fitScale.value)) {
    offset.value = { x: pan.ox + event.clientX - pan.x, y: pan.oy + event.clientY - pan.y }
  }
}
function up(event: PointerEvent) {
  // A horizontal swipe on a fitted image goes to the next or previous attachment.
  if (event.type === 'pointerup' && pan && !pinch && !compare.value && mode.value === 'fit' && pointers.size === 1 && props.items.length > 1) {
    const dx = event.clientX - pan.x, dy = event.clientY - pan.y
    if (Math.abs(dx) > 60 && Math.abs(dx) > Math.abs(dy) * 1.5) go(dx < 0 ? 1 : -1)
  }
  pointers.delete(event.pointerId); if (pointers.size < 2) pinch = null; if (!pointers.size) pan = null
}
function doubleClick(event: MouseEvent) { if (mode.value === 'fit') setScale(1, { x: event.clientX, y: event.clientY }); else toFit() }

// Compare: the slider handle drags the divide.
let sliding = false
function slideStart(event: PointerEvent) { sliding = true; (event.currentTarget as HTMLElement).setPointerCapture(event.pointerId); slideMove(event) }
function slideMove(event: PointerEvent) {
  if (!sliding) return
  const frame = (event.currentTarget as HTMLElement).closest('.compare-frame')?.getBoundingClientRect()
  if (frame) slider.value = Math.max(0, Math.min(1, (event.clientX - frame.left) / frame.width))
}
function slideEnd() { sliding = false }
function toggleCompare() {
  if (!current.value || !isImage(current.value)) return
  if (compare.value) { compare.value = null; void nextTick(fit); return }
  if (props.items.length < 2) return
  compare.value = { with: (index.value + 1) % props.items.length, view: 'slider' }
  toFit()
}
function pickStrip(i: number) {
  if (compare.value && i !== index.value && isImage(props.items[i])) { compare.value.with = i; return }
  compare.value = null
  index.value = i; reset()
}

function keydown(event: KeyboardEvent) {
  const target = event.target as HTMLElement
  if (target.tagName === 'INPUT' || target.tagName === 'TEXTAREA') { if (event.key === 'Escape') { event.preventDefault(); target.blur() } return }
  const keys: Record<string, () => void> = {
    ArrowRight: () => go(1), ArrowLeft: () => go(-1), '+': zoomIn, '=': zoomIn, '-': zoomOut, '0': toFit, '1': actual,
    c: toggleCompare, d: () => { details.value = !details.value; void nextTick(fit) },
  }
  const run = keys[event.key]
  if (run && !event.metaKey && !event.ctrlKey && !event.altKey) { event.preventDefault(); run() }
}
async function copyLink() {
  const item = current.value
  if (!item) return
  const link = `${location.origin}${contentUrl(item.id, 'original')}`
  try { await navigator.clipboard.writeText(link); toast('Copied the link to the original') } catch { toast('The link could not be copied', { tone: 'error' }) }
}
async function copyMarkdown() {
  const item = current.value
  if (!item) return
  try { await navigator.clipboard.writeText(markdownRef(item)); toast('Copied. Paste it into the description to show the image inline.') } catch { toast('It could not be copied', { tone: 'error' }) }
}
async function saveCaption() {
  const item = current.value
  if (item && caption.value.trim() !== item.caption) await props.setCaption(item, caption.value.trim())
}
const onResize = () => fit()
window.addEventListener('resize', onResize)
onBeforeUnmount(() => { clearHTML(); window.removeEventListener('resize', onResize) })
watch(() => props.items.length, length => { if (!length && dialog.value?.open) close(); else if (index.value >= length) index.value = Math.max(0, length - 1) })
// A live collection update can replace the selected row without navigation.
// Never display a previous attachment's capability under the new row's name.
watch(selectedHTMLKey, key => { if (dialog.value?.open && key !== htmlSelection) void loadHTML() })
watch(() => compare.value?.view, () => void nextTick(fit))
defineExpose({ open })
const transform = computed(() => `translate(${offset.value.x}px, ${offset.value.y}px) scale(${scale.value})`)
</script>

<template>
  <dialog ref="dialog" class="lightbox" :aria-label="current ? `${current.caption || current.name}, attachment ${index + 1} of ${items.length}` : 'Attachment viewer'" @cancel.prevent="close" @keydown="keydown">
    <template v-if="current">
      <header class="lb-bar">
        <div class="title-block">
          <p class="eyebrow">{{ ticketKey }} · Attachment {{ index + 1 }} of {{ items.length }}</p>
          <h2 class="name">{{ current.caption || current.name }}</h2>
          <p class="meta"><span v-if="current.caption" class="file">{{ current.name }}</span><span v-if="dims">{{ dims }}</span><span>{{ fileSize(current.size) }}</span></p>
        </div>
        <span class="spacer" />
        <div v-if="isImage(current)" class="zoom pill" role="group" aria-label="Zoom">
          <button type="button" class="pill-btn icon" aria-label="Zoom out" aria-keyshortcuts="-" data-tip="Zoom out · −" @click="zoomOut"><AppIcon name="minus" :size="14" /></button>
          <button type="button" class="pill-btn" :aria-pressed="mode === 'fit'" aria-keyshortcuts="0" data-tip="Fit · 0" @click="toFit">Fit</button>
          <button type="button" class="pill-btn" :aria-pressed="mode === 'free' && percent === 100" aria-keyshortcuts="1" data-tip="Actual size · 1" @click="actual">100%</button>
          <button type="button" class="pill-btn icon" aria-label="Zoom in" aria-keyshortcuts="+" data-tip="Zoom in · +" @click="zoomIn"><AppIcon name="plus" :size="14" /></button>
          <span class="percent mono" aria-live="polite">{{ percent }}%</span>
        </div>
        <button v-if="items.length > 1 && isImage(current)" type="button" class="pill-btn solo compare-btn" :aria-pressed="!!compare" aria-label="Compare" aria-keyshortcuts="c" data-tip="Compare two screens · c" @click="toggleCompare"><AppIcon name="compare" :size="14" /><span class="label">Compare</span></button>
        <button type="button" class="pill-btn solo details-btn" :aria-pressed="details" aria-label="Details" aria-keyshortcuts="d" data-tip="Details · d" @click="details = !details; $nextTick(fit)"><AppIcon name="info" :size="15" /><span class="label">Details</span></button>
        <a class="pill-btn round" :href="contentUrl(current.id, 'original')" :download="current.name" :aria-label="`Download ${current.name}`" data-tip="Download the original"><AppIcon name="download" :size="15" /></a>
        <a v-if="isHTML(current)" class="pill-btn round" :href="htmlURL || undefined" :aria-disabled="!htmlURL" :tabindex="htmlURL ? 0 : -1" aria-label="Open preview in new tab" data-tip="Open preview in new tab" target="_blank" rel="noopener noreferrer" referrerpolicy="no-referrer"><AppIcon name="external" :size="15" /></a>
        <button type="button" class="pill-btn round copy-link" aria-label="Copy link" data-tip="Copy link to the original" @click="copyLink"><AppIcon name="link" :size="15" /></button>
        <button type="button" class="pill-btn round" aria-label="Close viewer" aria-keyshortcuts="Escape" data-tip="Close · Esc" @click="close"><AppIcon name="close" :size="15" /></button>
      </header>

      <div class="body" :class="{ 'with-details': details }">
        <div ref="stage" class="stage" tabindex="-1" :aria-label="current.caption || current.name" :class="{ grab: mode === 'free' }" @wheel="wheel" @pointerdown="down" @pointermove="moveTo" @pointerup="up" @pointercancel="up" @dblclick="doubleClick">
          <!-- Compare -->
          <template v-if="compare && other && isImage(current) && isImage(other)">
            <div v-if="compare.view === 'side'" class="side-by-side">
              <figure v-for="(item, i) in [current, other]" :key="item.id" class="side">
                <img :src="contentUrl(item.id, 'preview')" :alt="item.caption || item.name" draggable="false" />
                <figcaption><span class="tag">{{ i === 0 ? 'A' : 'B' }}</span>{{ item.caption || item.name }}</figcaption>
              </figure>
            </div>
            <div v-else class="compare-frame" :style="{ transform, aspectRatio: current.width && current.height ? `${current.width} / ${current.height}` : undefined, width: `${current.width ?? 1600}px` }">
              <img class="base" :src="contentUrl(current.id, variant)" :alt="current.caption || current.name" draggable="false" />
              <img
                class="over" :src="contentUrl(other.id, variant)" :alt="other.caption || other.name" draggable="false"
                :style="compare.view === 'slider' ? { clipPath: `inset(0 0 0 ${slider * 100}%)` } : { opacity: onion }"
              />
              <div v-if="compare.view === 'slider'" class="divide" :style="{ left: `${slider * 100}%` }">
                <button
                  type="button" class="slider-handle" role="slider" aria-label="Compare divide" :aria-valuenow="Math.round(slider * 100)" aria-valuemin="0" aria-valuemax="100"
                  @pointerdown.stop="slideStart" @pointermove="slideMove" @pointerup="slideEnd" @keydown.left.prevent.stop="slider = Math.max(0, slider - .05)" @keydown.right.prevent.stop="slider = Math.min(1, slider + .05)"
                ><AppIcon name="chevron-left" :size="12" /><AppIcon name="chevron-right" :size="12" /></button>
              </div>
            </div>
          </template>
          <!-- One attachment -->
          <iframe v-else-if="isHTML(current) && htmlURL" :key="htmlURL" class="html-preview" :src="htmlURL" :title="`HTML preview: ${current.caption || current.name}`" sandbox="allow-scripts" referrerpolicy="no-referrer" />
          <div v-else-if="isHTML(current)" class="file-stage" role="status">
            <p class="file-name">{{ current.name }}</p>
            <p class="file-meta">{{ htmlState === 'loading' ? 'Opening HTML preview…' : htmlState === 'expired' ? 'This preview has expired.' : 'Live preview is unavailable. The original can still be downloaded.' }}</p>
            <button v-if="htmlState !== 'loading'" type="button" class="btn" @click="loadHTML">Reload preview</button>
            <a class="btn primary" :href="contentUrl(current.id, 'original')" :download="current.name"><AppIcon name="download" :size="14" />Download</a>
          </div>
          <img
            v-else-if="isImage(current) && !unreadable" :key="current.id" class="photo" :class="{ ready: loaded }" :src="contentUrl(current.id, variant)" :alt="current.caption || current.name"
            :width="current.width ?? undefined" :height="current.height ?? undefined" :style="{ transform }" draggable="false" @load="loaded = true" @error="unreadable = true"
          />
          <div v-else class="file-stage">
            <span class="file-badge">{{ fileKind(current) }}</span>
            <p class="file-name">{{ current.name }}</p>
            <p class="file-meta">{{ fileSize(current.size) }} · {{ current.content_type }}</p>
            <p v-if="unreadable" class="file-meta" role="status">This picture cannot be shown: its file is not stored here.</p>
            <a v-if="!unreadable" class="btn primary" :href="contentUrl(current.id, 'original')" :download="current.name"><AppIcon name="download" :size="14" />Download</a>
          </div>

          <button v-if="items.length > 1" type="button" class="nav prev" aria-label="Previous attachment" aria-keyshortcuts="ArrowLeft" @click="go(-1)"><AppIcon name="chevron-left" :size="20" /></button>
          <button v-if="items.length > 1" type="button" class="nav next" aria-label="Next attachment" aria-keyshortcuts="ArrowRight" @click="go(1)"><AppIcon name="chevron-right" :size="20" /></button>
        </div>

        <aside v-if="details" class="details" aria-label="Attachment details">
          <p class="eyebrow">Caption</p>
          <input v-model="caption" class="field caption" :disabled="!canWrite" placeholder="Add a caption" aria-label="Caption" maxlength="300" @keydown.enter.prevent="saveCaption" @blur="saveCaption" />
          <p class="eyebrow">File</p>
          <dl>
            <div><dt>Name</dt><dd>{{ current.name }}</dd></div>
            <div v-if="dims"><dt>Size</dt><dd>{{ dims }} px</dd></div>
            <div><dt>Weight</dt><dd>{{ fileSize(current.size) }}</dd></div>
            <div><dt>Added</dt><dd><template v-if="addedBy(current)">{{ addedBy(current) }}, </template>{{ absoluteTime(current.created_at) }}</dd></div>
          </dl>
          <p class="eyebrow">Use it</p>
          <button type="button" class="btn sm" @click="copyMarkdown"><AppIcon name="copy" :size="13" />Copy Markdown to show it inline</button>
        </aside>
      </div>

      <footer class="foot">
        <div v-if="compare" class="pill" role="radiogroup" aria-label="Compare view">
          <button v-for="view in (['side', 'slider', 'onion'] as const)" :key="view" type="button" role="radio" class="pill-btn" :aria-checked="compare.view === view" @click="compare.view = view">
            {{ view === 'side' ? 'Side by side' : view === 'slider' ? 'Slider' : 'Onion skin' }}
          </button>
          <label class="onion" :class="{ inactive: compare.view !== 'onion' }" :aria-hidden="compare.view !== 'onion' ? true : undefined"><span class="sr-only">Overlay opacity</span><input v-model.number="onion" type="range" min="0" max="1" step="0.01" :disabled="compare.view !== 'onion'" /></label>
          <span class="compare-note">A: {{ current.caption || current.name }} · B: {{ other?.caption || other?.name }} — pick B in the strip</span>
        </div>
        <ol v-if="items.length > 1" class="strip" aria-label="All attachments">
          <li v-for="(item, i) in items" :key="item.id">
            <button
              type="button" class="thumb" :class="{ on: i === index, b: compare?.with === i }" :aria-current="i === index ? 'true' : undefined"
              :aria-label="`${compare && i !== index ? 'Compare with' : 'Show'} ${item.caption || item.name}`" @click="pickStrip(i)"
            >
              <img v-if="hasThumbnail(item) && !brokenThumbs.has(item.id)" :src="contentUrl(item.id, 'thumb')" alt="" loading="lazy" @error="brokenThumbs.add(item.id)" />
              <span v-else class="thumb-file">{{ fileKind(item) }}</span>
              <span v-if="compare?.with === i" class="b-tag">B</span>
            </button>
          </li>
        </ol>
      </footer>
    </template>
  </dialog>
</template>

<style scoped>
.lightbox {
  --lb-ink: var(--ink); --lb-ink-2: var(--ink-2); --lb-glass: var(--glass); --lb-edge: var(--line-2);
  width: 100vw; height: 100dvh; max-width: none; max-height: none; margin: 0; padding: 0; border: 0; color: var(--lb-ink);
  background: radial-gradient(120% 90% at 50% 40%, var(--surface) 0%, var(--canvas) 60%, var(--canvas-lo, var(--canvas)) 100%);
  display: none; grid-template-columns: minmax(0, 1fr); grid-template-rows: auto minmax(0, 1fr) auto; overflow: hidden;
}
.lightbox[open] { display: grid; }
.lightbox::backdrop { background: color-mix(in srgb, var(--overlay-ink) 70%, transparent); }
@media (prefers-reduced-motion: no-preference) { .lightbox[open] { animation: lb-in .2s ease; } @keyframes lb-in { from { opacity: 0; } to { opacity: 1; } } }
.lb-bar { position: relative; z-index: 2; display: flex; align-items: center; gap: 10px; min-height: 72px; padding: 10px 18px 10px 22px; background: var(--lb-glass); border-bottom: 1px solid var(--lb-edge); -webkit-backdrop-filter: blur(18px); backdrop-filter: blur(18px); }
.title-block { display: grid; gap: 2px; min-width: 0; flex: 0 1 auto; }
.eyebrow { color: var(--ink-3); overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.name { font-size: 17px; font-weight: 600; letter-spacing: -.01em; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; color: var(--lb-ink); }
.meta { display: flex; gap: 10px; min-width: 0; font-size: 12px; color: var(--lb-ink-2); font-variant-numeric: tabular-nums; white-space: nowrap; }
.meta .file { flex: 0 1 auto; min-width: 0; max-width: 28ch; overflow: hidden; text-overflow: ellipsis; }
.spacer { flex: 1; }
.pill { display: inline-flex; align-items: center; gap: 2px; padding: 3px; border-radius: 999px; background: color-mix(in srgb, var(--ink) 8%, transparent); box-shadow: inset 0 0 0 1px var(--lb-edge); }
.pill-btn {
  display: inline-flex; align-items: center; justify-content: center; gap: 6px; height: 30px; min-width: 30px; padding: 0 12px; border: 0; border-radius: 999px;
  background: transparent; color: var(--lb-ink); font-size: 12.5px; font-weight: 600; text-decoration: none; cursor: pointer;
}
.pill-btn:hover { background: color-mix(in srgb, var(--primary-tint) 14%, transparent); }
.pill-btn:focus-visible { outline: 2px solid var(--primary-line); outline-offset: 2px; }
.pill-btn[aria-pressed="true"], .pill-btn[aria-checked="true"] { background: linear-gradient(180deg, var(--primary-hi), var(--primary)); color: var(--primary-on); box-shadow: 0 4px 12px -6px color-mix(in srgb, var(--primary-line) 90%, transparent); }
.pill-btn.icon { padding: 0; width: 30px; }
.pill-btn.solo { background: color-mix(in srgb, var(--ink) 8%, transparent); box-shadow: inset 0 0 0 1px var(--lb-edge); }
.pill-btn.round { width: 36px; height: 36px; padding: 0; background: color-mix(in srgb, var(--ink) 10%, transparent); box-shadow: inset 0 0 0 1px var(--lb-edge); }
.percent { min-width: 46px; padding: 0 8px 0 4px; font-size: 12px; color: var(--lb-ink-2); text-align: right; }
.body { position: relative; display: grid; grid-template-columns: minmax(0, 1fr); min-height: 0; }
.body.with-details { grid-template-columns: minmax(0, 1fr) 320px; }
.stage { position: relative; display: grid; grid-template: minmax(0, 1fr) / minmax(0, 1fr); place-items: center; overflow: hidden; touch-action: none; user-select: none; outline: none; }
.html-preview { width: calc(100% - 120px); height: calc(100% - 32px); border: 0; background: white; }
@media (max-width: 600px) { .html-preview { width: calc(100% - 24px); height: calc(100% - 96px); } }
/* The image and the compare frame keep their own size and sit on the stage centre;
   the transform (pan, then scale around the centre) does the rest. */
.photo, .compare-frame { position: absolute; left: 50%; top: 50%; translate: -50% -50%; }
/* The stage is where focus starts so the keys work at once; no ring around the whole picture. */
.stage:focus-visible { box-shadow: none; }
.stage.grab { cursor: grab; }
.stage.grab:active { cursor: grabbing; }
.photo, .side img { max-width: none; transform-origin: center; box-shadow: 0 30px 80px -30px color-mix(in srgb, var(--shadow-black) 80%, transparent), 0 0 0 1px color-mix(in srgb, var(--ink) 8%, transparent); border-radius: 6px; background: var(--surface-highlight); }
.photo { opacity: 0; transition: opacity .2s ease; }
.photo.ready { opacity: 1; }
.side-by-side { display: grid; grid-template-columns: 1fr 1fr; gap: 24px; width: 100%; height: 100%; padding: 24px; }
.side { display: grid; grid-template-rows: minmax(0, 1fr) auto; place-items: center; gap: 10px; margin: 0; min-width: 0; overflow: hidden; }
.side img { max-width: 100%; max-height: 100%; object-fit: contain; }
figcaption { display: flex; align-items: center; gap: 8px; font-size: 12.5px; color: var(--lb-ink-2); }
.tag, .b-tag { display: inline-grid; place-items: center; width: 20px; height: 20px; border-radius: 6px; background: var(--secondary); color: var(--secondary-on); font: 700 11px/1 var(--mono); }
.compare-frame { max-width: none; transform-origin: center; border-radius: 6px; overflow: hidden; box-shadow: 0 30px 80px -30px color-mix(in srgb, var(--shadow-black) 80%, transparent); background: var(--surface-highlight); }
.compare-frame img { position: absolute; inset: 0; width: 100%; height: 100%; object-fit: contain; }
.compare-frame .base { position: relative; display: block; }
.divide { position: absolute; top: 0; bottom: 0; width: 2px; margin-left: -1px; background: var(--secondary-line); pointer-events: none; }
.slider-handle {
  position: absolute; top: 50%; left: 50%; display: inline-flex; align-items: center; justify-content: center; gap: 0; width: 40px; height: 40px; margin: -20px 0 0 -20px;
  border: 2px solid var(--secondary-line); border-radius: 50%; background: var(--surface); color: var(--secondary-ink); cursor: ew-resize; pointer-events: auto; touch-action: none; box-shadow: 0 6px 18px -6px color-mix(in srgb, var(--shadow-black) 60%, transparent);
}
.slider-handle:focus-visible { outline: 3px solid var(--primary-line); outline-offset: 2px; }
.nav {
  position: absolute; top: 50%; display: grid; place-items: center; width: 44px; height: 44px; margin-top: -22px; border: 0; border-radius: 50%;
  background: color-mix(in srgb, var(--ink) 10%, transparent); box-shadow: inset 0 0 0 1px var(--lb-edge); color: var(--lb-ink); cursor: pointer;
}
.nav:hover { background: color-mix(in srgb, var(--primary-tint) 20%, transparent); }
.nav:focus-visible { outline: 2px solid var(--primary-line); outline-offset: 2px; }
.nav.prev { left: 18px; } .nav.next { right: 18px; }
.file-stage { display: grid; justify-items: center; gap: 10px; padding: 40px; border-radius: 18px; background: var(--lb-glass); box-shadow: inset 0 0 0 1px var(--lb-edge); }
.file-badge { display: grid; place-items: center; width: 72px; height: 88px; border-radius: 12px; background: color-mix(in srgb, var(--ink) 10%, transparent); box-shadow: inset 0 0 0 1px var(--lb-edge); font: 700 14px/1 var(--mono); color: var(--primary-line); }
.file-name { font-size: 16px; font-weight: 600; color: var(--lb-ink); }
.file-meta { font-size: 12.5px; color: var(--lb-ink-2); }
.details { display: grid; align-content: start; gap: 10px; padding: 20px; background: var(--lb-glass); border-left: 1px solid var(--lb-edge); overflow: auto; }
.details .eyebrow { margin-top: 6px; }
.details .caption { width: 100%; background: color-mix(in srgb, var(--ink) 6%, transparent); color: var(--lb-ink); border-color: var(--lb-edge); }
.details dl { display: grid; gap: 6px; margin: 0; font-size: 12.5px; }
.details dl div { display: grid; grid-template-columns: 64px minmax(0, 1fr); gap: 8px; }
.details dt { color: var(--ink-3); }
.details dd { margin: 0; color: var(--lb-ink); overflow-wrap: anywhere; }
.details .btn { justify-self: start; }
.foot { display: grid; justify-items: center; gap: 10px; padding: 10px 18px 16px; }
.foot > .pill { max-width: 100%; min-width: 0; }
.foot:empty { display: none; }
.onion { display: inline-flex; align-items: center; padding: 0 10px; }
.onion.inactive { visibility: hidden; }
.onion input { width: 140px; accent-color: var(--secondary); }
.compare-note { padding: 0 12px; font-size: 12px; color: var(--lb-ink-2); white-space: nowrap; overflow: hidden; text-overflow: ellipsis; max-width: 46ch; }
.strip { display: flex; gap: 8px; max-width: 100%; margin: 0; padding: 4px; list-style: none; overflow-x: auto; scrollbar-width: none; }
.thumb { position: relative; display: grid; place-items: center; width: 76px; height: 52px; padding: 0; border: 0; border-radius: 8px; overflow: hidden; background: color-mix(in srgb, var(--ink) 8%, transparent); box-shadow: inset 0 0 0 1px var(--lb-edge); opacity: .6; cursor: pointer; }
.thumb img { width: 100%; height: 100%; object-fit: cover; }
.thumb:hover { opacity: .9; }
.thumb.on { opacity: 1; box-shadow: 0 0 0 2px var(--primary-line); }
.thumb.b { opacity: 1; box-shadow: 0 0 0 2px var(--secondary); }
.thumb:focus-visible { outline: 2px solid var(--primary-line); outline-offset: 2px; }
.thumb-file { font: 700 11px/1 var(--mono); color: var(--primary-line); }
.b-tag { position: absolute; top: 4px; right: 4px; width: 16px; height: 16px; font-size: 10px; }
@media (pointer: coarse) {
  .pill-btn { min-width: 44px; min-height: 44px; }
  .onion input { min-height: 44px; }
  .slider-handle { width: 44px; height: 44px; margin: -22px 0 0 -22px; }
}
@media (max-width: 720px) {
  .lb-bar { gap: 6px; min-height: 60px; padding: 8px 10px 8px 14px; }
  .title-block { flex: 1 1 auto; }
  .name { font-size: 15.5px; }
  .zoom, .copy-link { display: none; }
  .details-btn, .compare-btn, .pill-btn.round { width: 44px; height: 44px; padding: 0; }
  .details-btn .label, .compare-btn .label { display: none; }
  .pill-btn.round, .details-btn, .compare-btn { flex: none; }
  .body.with-details { grid-template-columns: minmax(0, 1fr); grid-template-rows: minmax(0, 1fr) auto; }
  .details { border-left: 0; border-top: 1px solid var(--lb-edge); max-height: 40dvh; }
  /* Swipe to move on a phone; the arrows sit low so they do not cover the image. */
  .nav { top: auto; bottom: 12px; width: 44px; height: 44px; margin-top: 0; }
  .nav.prev { left: 8px; } .nav.next { right: 8px; }
  .side-by-side { grid-template-columns: 1fr; grid-template-rows: 1fr 1fr; }
  .foot > .pill { display: grid; grid-template-columns: repeat(3, minmax(0, 1fr)); width: 100%; border-radius: 16px; }
  .foot > .pill .pill-btn { min-height: 44px; padding-inline: 4px; }
  .onion input { min-height: 44px; }
  .slider-handle { width: 44px; height: 44px; margin: -22px 0 0 -22px; }
  .compare-note { grid-column: 1 / -1; max-width: 100%; min-width: 0; padding: 4px 8px; }
  .onion { grid-column: 1 / -1; justify-content: center; }
}
</style>
