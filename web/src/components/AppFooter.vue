<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { useRoute } from 'vue-router'
import { brand } from '../lib/brand'
import { releaseAria } from '../lib/codenames'
import { flowNameBudget, placeFlowPill } from '../lib/flowPill'
import { flowPillContext } from '../lib/flowPillContext'
import { useReleases } from '../stores/releases'
import { useSession } from '../stores/session'
import { useVersion } from '../stores/version'
import ReleaseName from './ReleaseName.vue'
import JourneyChip from './journey/JourneyChip.vue'

// The footer bar, at its very left: the product's name and the running release's
// marketing name (AEON-431), one button that opens the release history. The
// calendar version shows only on hover or focus (ReleaseName, AEON-430), and a
// small badge says how many releases are new since the last look. A release with
// no known name reads as its calendar version, as everywhere (ReleaseName), and
// while the version is unknown the product name stands alone. The journey pill
// sits in the centre; when it needs the room the product name steps aside and
// the release name stays.
defineProps<{ hidden?: boolean }>()
const emit = defineEmits<{ releases: []; pill: [shown: boolean] }>()
const route = useRoute()
const onProject = computed(() => typeof route.params.projectKey === 'string' && route.params.projectKey !== '')
const pill = computed(() => onProject.value ? flowPillContext.value : null)
const root = ref<HTMLElement>()
const slotEl = ref<HTMLElement>()
const pillOn = ref(false)
const placed = ref(false)
const tight = ref(false)
const GAP = 8
let resize: ResizeObserver | undefined
let mutations: MutationObserver | undefined
let raf = 0
let epoch = 0

function naturalWidth(el: HTMLElement, compact?: boolean): number {
  const clone = el.cloneNode(true) as HTMLElement
  if (compact !== undefined) clone.classList.toggle('compact', compact)
  clone.setAttribute('aria-hidden', 'true')
  if (clone instanceof HTMLButtonElement) clone.tabIndex = -1
  clone.style.cssText = 'position:absolute;left:0;top:0;visibility:hidden;pointer-events:none;height:auto;width:max-content;max-width:none;container-type:normal;'
  for (const part of clone.querySelectorAll<HTMLElement>('.face, .next')) {
    part.style.maxWidth = 'none'
    part.style.width = 'max-content'
    part.style.overflow = 'visible'
    part.style.textOverflow = 'clip'
  }
  el.ownerDocument.body.appendChild(clone)
  const width = Math.ceil(clone.getBoundingClientRect().width)
  clone.remove()
  return width
}

async function place() {
  const token = ++epoch
  await nextTick()
  if (token !== epoch) return
  const footer = root.value
  const slot = slotEl.value
  const chip = slot?.querySelector<HTMLElement>('.journey-chip')
  const name = footer?.querySelector<HTMLElement>('.footer-name')
  if (!footer || !slot || !chip || !name || !pillOn.value) { placed.value = false; return }
  const style = getComputedStyle(footer)
  const padL = parseFloat(style.paddingLeft) || 0
  const padR = parseFloat(style.paddingRight) || 0
  const content = footer.clientWidth - padL - padR
  const want = naturalWidth(chip)
  // Nothing sits to the pill's right, so the name alone decides: whole, or without the product name.
  const compact = flowNameBudget(content, 0, naturalWidth(name, false), want, GAP) !== null
  name.classList.toggle('compact', compact)
  const box = footer.getBoundingClientRect()
  const edge = box.right - padR
  const placedBox = placeFlowPill(box, name.getBoundingClientRect(), { left: edge + GAP, right: edge }, want, GAP)
  const left = `${Math.round(placedBox.left)}px`
  const width = `${Math.round(placedBox.width)}px`
  if (footer.style.getPropertyValue('--flow-left') !== left) footer.style.setProperty('--flow-left', left)
  if (footer.style.getPropertyValue('--flow-width') !== width) footer.style.setProperty('--flow-width', width)
  if (footer.style.getPropertyValue('--flow-shift') !== 'none') footer.style.setProperty('--flow-shift', 'none')
  tight.value = placedBox.width + 1 < want
  placed.value = placedBox.width > 0
}

function schedule() {
  cancelAnimationFrame(raf)
  raf = requestAnimationFrame(() => { void place() })
}

function onShown(shown: boolean) {
  pillOn.value = shown
  emit('pill', shown)
  if (!shown) {
    placed.value = false
    tight.value = false
    root.value?.querySelector<HTMLElement>('.footer-name')?.classList.remove('compact')
    return
  }
  schedule()
}

function openPill() { pill.value?.open() }

function watchSize() {
  const footer = root.value
  if (!footer) return
  resize?.disconnect()
  resize = new ResizeObserver(() => schedule())
  resize.observe(footer)
  for (const el of footer.querySelectorAll<HTMLElement>('.footer-name')) resize.observe(el)
}

onMounted(() => {
  watchSize()
  const footer = root.value
  if (!footer) return
  mutations = new MutationObserver(() => { watchSize(); schedule() })
  mutations.observe(footer, { childList: true, subtree: true, characterData: true })
  void document.fonts?.ready.then(() => schedule())
})
onBeforeUnmount(() => { resize?.disconnect(); mutations?.disconnect(); cancelAnimationFrame(raf); emit('pill', false) })
watch(pill, value => { if (!value) onShown(false) })
const version = useVersion()
const releases = useReleases()
const session = useSession()
void version.load()
const value = computed(() => version.value?.version ?? '')
const count = computed(() => session.identity ? releases.newCount : 0)
const label = computed(() => {
  const running = value.value ? `, ${releaseAria(version.value?.codename ?? '', value.value)}` : ''
  const fresh = count.value === null ? ', new releases since your last visit' : count.value ? `, ${count.value} new since your last visit` : ''
  return `Release history${running}${fresh}`
})
</script>

<template>
  <footer ref="root" class="app-footer" :class="{ hidden }" :inert="hidden || undefined">
    <button v-if="session.identity" type="button" class="footer-name" :aria-label="label" data-tip="Release history" @click="emit('releases')">
      <span class="name-face">
        <span class="footer-wordmark">{{ brand.wordmark }}</span>
        <template v-if="value"><span class="footer-dot" aria-hidden="true">·</span><ReleaseName :version="value" :name="version.value?.codename" class="footer-codename" /></template>
        <span v-else-if="!version.failed" class="skeleton name-skeleton" />
        <span v-if="count !== 0" class="new-badge" aria-hidden="true">{{ count === null ? 'New' : `${count} new` }}</span>
      </span>
    </button>
    <span v-else class="footer-name">
      <span class="name-face">
        <span class="footer-wordmark">{{ brand.wordmark }}</span>
        <template v-if="value"><span class="footer-dot" aria-hidden="true">·</span><ReleaseName :version="value" :name="version.value?.codename" class="footer-codename" /></template>
      </span>
    </span>
    <div v-if="pill" ref="slotEl" class="flow-slot" :class="{ placed: placed && pillOn, tight: tight && pillOn }" v-show="pillOn">
      <JourneyChip :key="pill.projectId" :project-id="pill.projectId" :active="pill.active" @go="openPill" @shown="onShown" />
    </div>
  </footer>
</template>

<style scoped>
/* Same glass language as the header rail, mirrored: a hairline on top. */
.app-footer {
  position: relative; z-index: 18; display: flex; align-items: center; gap: 12px; height: var(--footer-h); min-height: 0; padding: 0 var(--gutter); overflow: clip;
  background: var(--glass-2); box-shadow: inset 0 1px 0 var(--glass-edge), 0 -1px 0 var(--line);
  -webkit-backdrop-filter: blur(16px) saturate(1.2); backdrop-filter: blur(16px) saturate(1.2);
  color: var(--ink-2);
}
/* The button is the bar's height (a comfortable target); the name is its face. */
.footer-name { display: inline-flex; align-items: center; align-self: stretch; flex-shrink: 1; min-width: 0; overflow: hidden; margin-left: -6px; padding: 0; border: 0; background: transparent; color: var(--ink-3); white-space: nowrap; }
.name-face { display: inline-flex; align-items: center; gap: 0; min-width: 0; padding: 5px 6px; border-radius: 6px; transition: background .15s ease, color .15s ease; }
.footer-wordmark { flex-shrink: 0; font: 600 10px/1 var(--mono); letter-spacing: .24em; font-variant-ligatures: none; }
.footer-dot { flex-shrink: 0; margin: 0 7px 0 5px; font: 400 11px/1 var(--font); }
/* The release's name reads as a name, not as more lettering; hover or focus swaps in its version. */
.footer-codename { color: var(--ink-2); }
.footer-codename :deep(.rn-name) { font: 500 11.5px/1 var(--font); }
/* The journey pill may take the product name's room; the release name stays. */
.footer-name.compact .footer-wordmark, .footer-name.compact .footer-dot { display: none; }
button.footer-name { cursor: pointer; }
button.footer-name:focus-visible { box-shadow: none; }
button.footer-name:focus-visible .name-face { box-shadow: var(--focus-ring); }
@media (hover: hover) { button.footer-name:hover .name-face { background: var(--surface-2); color: var(--ink-2); } button.footer-name:hover .footer-codename { color: var(--teal-ink); } }
button.footer-name:active .name-face { background: var(--row-selected); }
/* As wide as a codename, so the bar keeps its shape while the version loads. */
.name-skeleton { width: 72px; height: 8px; margin-left: 12px; }
/* Centred on the page when the sides allow it. */
.flow-slot {
  position: absolute; z-index: 1; top: 0; left: var(--flow-left, 50%); width: var(--flow-width, max-content); max-width: calc(100% - 24px); height: 100%;
  transform: var(--flow-shift, translateX(-50%)); display: flex; align-items: center; justify-content: center; min-width: 0; visibility: hidden; pointer-events: none;
}
.flow-slot.placed { visibility: visible; }
.flow-slot :deep(.journey-chip) { pointer-events: auto; }
/* The dot and arrow go when the full line does not fit. The stage and the next action stay. */
.flow-slot.tight :deep(.led),
.flow-slot.tight :deep(.go) { display: none; }
.flow-slot.tight :deep(.face) { gap: 6px; padding-right: 10px; }
.new-badge {
  display: inline-flex; align-items: center; flex-shrink: 0; height: 17px; margin-left: 9px; padding: 0 7px; border-radius: 999px;
  background: var(--gold-2); color: #3a2804; font: 700 10px/1 var(--mono); letter-spacing: .02em; box-shadow: 0 0 0 1px rgba(154, 107, 18, .35);
}
@media (max-width: 600px) {
  .app-footer { gap: 8px; padding: 0 12px; transition: transform .22s ease; }
  .app-footer.hidden { transform: translateY(100%); }
  .footer-wordmark { font-size: 9px; letter-spacing: .18em; }
  .footer-codename :deep(.rn-name) { font-size: 11px; }
}
@media (prefers-reduced-motion: reduce) { .app-footer, .name-face { transition: none; } }
</style>
