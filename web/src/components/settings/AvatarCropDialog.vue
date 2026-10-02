<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, onMounted, ref } from 'vue'
import { centred, cropOf, MAX_ZOOM, MIN_ZOOM, pan, prepareAvatarUpload, scaleOf, zoomAt, type CropView } from '../../lib/avatar'
import { uploadAvatar, type Profile } from '../../lib/profile'
import AppIcon from '../AppIcon.vue'
import KeyCap from '../KeyCap.vue'

// Crop a new photo: a square crop under a circular mask. Drag (or arrows) to move,
// the slider, the wheel, a pinch (or + and −) to zoom; previews show it at 32, 64
// and 128 px. Saving fits large photos to the upload limits; the server makes the sizes.
const props = defineProps<{ file: File }>()
const emit = defineEmits<{ saved: [profile: Profile]; close: [] }>()
const dialog = ref<HTMLDialogElement>()
const stage = ref<HTMLElement>()
const photo = ref<HTMLImageElement>()
const stageSize = Math.round(Math.min(320, window.innerWidth - 72))
const url = URL.createObjectURL(props.file)
const view = ref<CropView | null>(null)
const failed = ref('')
const error = ref('')
const progress = ref<number | null>(null)
const preparing = ref(false)
let upload: { abort: () => void } | null = null
let saveGeneration = 0
const PREVIEWS = [128, 64, 32]

function loaded(event: Event) {
  const img = event.target as HTMLImageElement
  if (!img.naturalWidth || !img.naturalHeight) { failed.value = 'This file could not be read as an image.'; return }
  view.value = centred(stageSize, img.naturalWidth, img.naturalHeight)
  void nextTick(() => stage.value?.focus())
}
const scale = computed(() => view.value ? scaleOf(view.value) : 1)
const imageStyle = (k = 1) => view.value ? { width: `${view.value.width}px`, height: `${view.value.height}px`, transform: `translate(${view.value.x * k}px, ${view.value.y * k}px) scale(${scale.value * k})` } : {}
const zoomValue = computed({
  get: () => view.value?.zoom ?? 1,
  set: (value: number) => { if (view.value && progress.value === null) view.value = zoomAt(view.value, Number(value)) },
})
const zoomPercent = computed(() => `${Math.round((view.value?.zoom ?? 1) * 100)} %`)

// ---------- Pointer: drag to pan, pinch or wheel to zoom ----------
const pointers = new Map<number, { x: number; y: number }>()
let pinch: { distance: number; zoom: number } | null = null
function local(event: { clientX: number; clientY: number }) {
  const box = stage.value!.getBoundingClientRect()
  return { x: event.clientX - box.left, y: event.clientY - box.top }
}
function down(event: PointerEvent) {
  if (!view.value || progress.value !== null) return
  stage.value?.setPointerCapture(event.pointerId)
  pointers.set(event.pointerId, { x: event.clientX, y: event.clientY })
  if (pointers.size === 2) {
    const [a, b] = [...pointers.values()]
    pinch = { distance: Math.hypot(a.x - b.x, a.y - b.y), zoom: view.value.zoom }
  }
}
function move(event: PointerEvent) {
  const before = pointers.get(event.pointerId)
  if (!before || !view.value || progress.value !== null) return
  pointers.set(event.pointerId, { x: event.clientX, y: event.clientY })
  if (pointers.size === 1) view.value = pan(view.value, event.clientX - before.x, event.clientY - before.y)
  else if (pinch && pointers.size === 2) {
    const [a, b] = [...pointers.values()]
    const mid = local({ clientX: (a.x + b.x) / 2, clientY: (a.y + b.y) / 2 })
    view.value = zoomAt(view.value, pinch.zoom * Math.hypot(a.x - b.x, a.y - b.y) / pinch.distance, mid.x, mid.y)
  }
}
function up(event: PointerEvent) { pointers.delete(event.pointerId); if (pointers.size < 2) pinch = null }
function wheel(event: WheelEvent) {
  if (!view.value || progress.value !== null) return
  event.preventDefault()
  const at = local(event)
  view.value = zoomAt(view.value, view.value.zoom * Math.exp(-event.deltaY * 0.0015), at.x, at.y)
}
// ---------- Keys: arrows move, + and − zoom, 0 resets, Enter saves ----------
function keys(event: KeyboardEvent) {
  if (!view.value || progress.value !== null || event.metaKey || event.ctrlKey || event.altKey) return
  const step = event.shiftKey ? 32 : 8
  const moves: Record<string, [number, number]> = { ArrowLeft: [step, 0], ArrowRight: [-step, 0], ArrowUp: [0, step], ArrowDown: [0, -step] }
  if (moves[event.key]) { event.preventDefault(); view.value = pan(view.value, ...moves[event.key]) }
  else if (event.key === '+' || event.key === '=') { event.preventDefault(); view.value = zoomAt(view.value, view.value.zoom * 1.1) }
  else if (event.key === '-' || event.key === '_') { event.preventDefault(); view.value = zoomAt(view.value, view.value.zoom / 1.1) }
  else if (event.key === '0') { event.preventDefault(); view.value = centred(view.value.view, view.value.width, view.value.height) }
  else if (event.key === 'Enter') { event.preventDefault(); void save() }
}
function nudgeZoom(factor: number) { if (view.value && progress.value === null) view.value = zoomAt(view.value, view.value.zoom * factor) }
function dialogKeys(event: KeyboardEvent) {
  if ((event.metaKey || event.ctrlKey) && !event.altKey && event.key === 'Enter') {
    event.preventDefault(); void save()
  } else if (event.key === 'Escape' && event.target instanceof HTMLInputElement) {
    event.preventDefault(); event.stopPropagation(); stage.value?.focus()
  }
}

// ---------- Save ----------
async function save() {
  if (!view.value || !photo.value || progress.value !== null) return
  const generation = ++saveGeneration
  const selected = { ...view.value }
  error.value = ''
  progress.value = 0
  preparing.value = true
  try {
    const prepared = await prepareAvatarUpload(props.file, photo.value)
    if (generation !== saveGeneration) return
    preparing.value = false
    // Viewport coordinates stay fixed as source dimensions shrink. Computing
    // the crop again preserves the selected area, including pan and zoom.
    const task = uploadAvatar(prepared.file, cropOf({ ...selected, width: prepared.width, height: prepared.height }), fraction => { progress.value = fraction })
    upload = task
    const profile = await task.done
    if (generation === saveGeneration) emit('saved', profile)
  }
  catch (e) { if (generation === saveGeneration && !(e as { aborted?: boolean }).aborted) error.value = e instanceof Error ? e.message : 'Your photo could not be saved.' }
  finally { if (generation === saveGeneration) { progress.value = null; preparing.value = false; upload = null } }
}
function cancel() {
  if (progress.value !== null) {
    ++saveGeneration
    upload?.abort(); upload = null; progress.value = null; preparing.value = false
  } else emit('close')
}
function escape(event: Event) { event.preventDefault(); cancel() }

onMounted(() => { dialog.value?.showModal() })
onBeforeUnmount(() => { ++saveGeneration; upload?.abort(); URL.revokeObjectURL(url) })
</script>

<template>
  <dialog ref="dialog" class="crop-dialog" aria-labelledby="crop-title" @cancel="escape" @keydown="dialogKeys">
    <div class="card">
      <header class="head">
        <h2 id="crop-title">Crop your photo</h2>
        <button type="button" class="icon-btn flat sm" aria-label="Cancel" @click="cancel"><AppIcon name="close" :size="14" /></button>
      </header>
      <div class="foot">
        <button type="button" class="btn" @click="cancel">{{ progress !== null ? 'Stop' : 'Cancel' }}<KeyCap k="Esc" /></button>
        <button type="button" class="btn primary" :disabled="!view || !!failed || progress !== null" @click="save"><AppIcon name="check" :size="14" />{{ progress !== null ? 'Saving…' : 'Save photo' }}<KeyCap k="mod" /><KeyCap k="enter" /></button>
      </div>
      <p v-if="failed" class="problem" role="alert"><AppIcon name="alert" :size="14" />{{ failed }}</p>
      <div v-else class="body">
        <div class="left">
          <div
            ref="stage" class="stage" :style="{ width: `${stageSize}px`, height: `${stageSize}px` }" tabindex="0" role="application"
            aria-label="Photo position" aria-describedby="crop-help" @pointerdown="down" @pointermove="move" @pointerup="up" @pointercancel="up" @wheel="wheel" @keydown="keys"
          >
            <img ref="photo" :src="url" alt="" class="photo" draggable="false" :style="imageStyle()" @load="loaded" @error="failed = 'This file could not be read as an image.'" />
            <span class="mask" aria-hidden="true" />
          </div>
          <div class="zoom">
            <button type="button" class="icon-btn flat sm" aria-label="Zoom out" :disabled="!view || progress !== null || view.zoom <= MIN_ZOOM" @click="nudgeZoom(1 / 1.2)"><AppIcon name="minus" :size="14" /></button>
            <input v-model.number="zoomValue" type="range" class="slider" :disabled="!view || progress !== null" :min="MIN_ZOOM" :max="MAX_ZOOM" step="0.01" aria-label="Zoom" :aria-valuetext="zoomPercent" />
            <button type="button" class="icon-btn flat sm" aria-label="Zoom in" :disabled="!view || progress !== null || view.zoom >= MAX_ZOOM" @click="nudgeZoom(1.2)"><AppIcon name="plus" :size="14" /></button>
          </div>
          <p id="crop-help" class="help">
            <span>Drag to move · scroll or pinch to zoom</span>
            <span class="keys"><KeyCap k="left" /><KeyCap k="right" /> move · <kbd class="keycap">+</kbd><kbd class="keycap">−</kbd> zoom · <kbd class="keycap">0</kbd> reset</span>
          </p>
        </div>
        <div class="previews" role="group" aria-label="Previews">
          <p class="eyebrow">Preview</p>
          <div class="preview-row">
            <span v-for="size in PREVIEWS" :key="size" class="preview" :style="{ width: `${size}px`, height: `${size}px` }">
              <img v-if="view" :src="url" alt="" draggable="false" :style="imageStyle(size / view.view)" />
            </span>
          </div>
          <p class="sizes mono"><span v-for="size in PREVIEWS" :key="size">{{ size }}</span></p>
        </div>
      </div>
      <p v-if="error" class="problem" role="alert"><AppIcon name="alert" :size="14" />{{ error }}</p>
      <div class="status-line">
        <span v-if="preparing" role="status">Preparing your photo…</span>
        <div v-else-if="progress !== null" class="progress" role="progressbar" aria-label="Uploading your photo" :aria-valuenow="Math.round(progress * 100)" aria-valuemin="0" aria-valuemax="100">
          <span class="bar"><i :style="{ width: `${Math.round(progress * 100)}%` }" /></span>
          <span class="mono">{{ Math.round(progress * 100) }} %</span>
        </div>
      </div>
    </div>
  </dialog>
</template>

<style scoped>
.crop-dialog { width: min(660px, calc(100vw - 24px)); max-width: none; max-height: calc(100dvh - 24px); margin: 24px auto 0; inset-block-start: 0; padding: 0; border: 0; background: transparent; color: var(--ink); overflow: visible; }
.crop-dialog::backdrop { background: var(--scrim); }
.card { display: grid; gap: 14px; max-height: calc(100dvh - 24px); overflow: auto; padding: 18px 20px; border-radius: var(--radius); border: 1px solid var(--glass-edge); background: var(--surface-raised); box-shadow: var(--shadow-pop), var(--shadow); }
.head { display: flex; align-items: center; justify-content: space-between; }
.head h2 { font-size: 17px; }
.body { display: grid; grid-template-columns: auto minmax(0, 1fr); gap: 24px; align-items: start; }
.left { display: grid; gap: 10px; justify-items: center; max-width: 320px; }
.stage { position: relative; overflow: hidden; border-radius: 14px; background: #0c1a1c; cursor: grab; touch-action: none; user-select: none; outline: none; }
.stage:active { cursor: grabbing; }
.stage:focus-visible { box-shadow: var(--focus-ring); }
.photo { position: absolute; top: 0; left: 0; max-width: none; transform-origin: 0 0; pointer-events: none; }
/* The circle the avatar shows; outside it dims. A hairline marks its edge. */
.mask { position: absolute; inset: 0; pointer-events: none; background: radial-gradient(circle closest-side, transparent calc(100% - 1px), rgba(255, 255, 255, .85) calc(100% - 1px), rgba(255, 255, 255, .85) 100%, rgba(8, 18, 20, .58) calc(100% + .5px)); }
.zoom { display: grid; grid-template-columns: auto minmax(0, 1fr) auto; align-items: center; gap: 6px; width: 100%; }
.slider { width: 100%; accent-color: var(--teal); }
@media (max-width: 600px) { .slider { height: 44px; } }
.help { display: grid; justify-items: center; gap: 4px; font-size: 12px; color: var(--ink-2); text-align: center; line-height: 1.6; }
.help .keys { display: inline-flex; flex-wrap: wrap; justify-content: center; align-items: center; gap: 3px 4px; }
/* Touch screens have no keys to press: the drag and pinch line says it all. */
@media (hover: none) and (pointer: coarse) { .help .keys { display: none; } }
.previews { display: grid; gap: 10px; align-content: start; }
.previews .eyebrow { margin: 0; }
.preview-row { display: flex; align-items: flex-end; gap: 14px; flex-wrap: wrap; }
.previews { min-width: 0; }
.preview { position: relative; overflow: hidden; flex-shrink: 0; border-radius: 50%; background: var(--surface-2); box-shadow: 0 0 0 1px var(--line-2); }
.preview img { position: absolute; top: 0; left: 0; max-width: none; transform-origin: 0 0; }
.sizes { display: flex; gap: 14px; font-size: 11px; color: var(--ink-3); }
.sizes span:nth-child(1) { width: 128px; } .sizes span:nth-child(2) { width: 64px; } .sizes span:nth-child(3) { width: 32px; }
.problem { display: flex; align-items: flex-start; gap: 8px; padding: 10px 12px; border-radius: 10px; background: var(--danger-bg); color: var(--ink); font-size: 13px; }
.problem svg { margin-top: 2px; color: var(--danger); }
.foot { display: flex; justify-content: flex-end; gap: 8px; }
.foot .btn { width: 110px; flex: none; }
.foot .primary { width: 170px; }
.status-line { min-height: 20px; font-size: 12px; color: var(--ink-2); }
.progress { display: flex; align-items: center; gap: 8px; min-width: 180px; font-size: 12px; color: var(--ink-2); }
.progress .bar { flex: 1; }
@media (max-width: 600px) {
  .card { padding: 16px; }
  .body { grid-template-columns: minmax(0, 1fr); gap: 16px; }
  .preview-row, .sizes { justify-content: center; }
  .previews .eyebrow { text-align: center; }
  .foot .btn { height: 44px; }
}
</style>
