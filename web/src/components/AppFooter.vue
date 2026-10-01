<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { useRoute } from 'vue-router'
import { brand } from '../lib/brand'
import { codenameOf, releaseAria } from '../lib/codenames'
import { useDeveloperSettings } from '../lib/developerSettings'
import { flowNameBudget, placeFlowPill } from '../lib/flowPill'
import { flowPillContext } from '../lib/flowPillContext'
import { useReleases } from '../stores/releases'
import { useSession } from '../stores/session'
import { useVersion } from '../stores/version'
import AppIcon from './AppIcon.vue'
import CalendarVersion from './CalendarVersion.vue'
import JourneyChip from './journey/JourneyChip.vue'

// The product mark stays left; the running release's name, Pretty version and
// new count share the history pill on the right. The opt-in flow sits between
// them, taking the wordmark's room when needed.
defineProps<{ hidden?: boolean }>()
const emit = defineEmits<{ releases: []; pill: [shown: boolean] }>()
const route = useRoute()
const onProject = computed(() => typeof route.params.projectKey === 'string' && route.params.projectKey !== '')
const { showFlowControls } = useDeveloperSettings()
const pill = computed(() => showFlowControls.value && onProject.value ? flowPillContext.value : null)
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

function naturalWidth(el: HTMLElement): number {
  const clone = el.cloneNode(true) as HTMLElement
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
  const version = footer?.querySelector<HTMLElement>('.version-pill, .version-plain')
  if (!footer || !slot || !chip || !name || !version || !pillOn.value) { placed.value = false; return }
  const style = getComputedStyle(footer)
  const padL = parseFloat(style.paddingLeft) || 0
  const padR = parseFloat(style.paddingRight) || 0
  const content = footer.clientWidth - padL - padR
  const leading = naturalWidth(name)
  const want = naturalWidth(chip)
  const budget = flowNameBudget(content, version.offsetWidth, leading, want, GAP)
  const nextCap = budget === null ? '' : '0px'
  if (name.style.maxWidth !== nextCap) name.style.maxWidth = nextCap
  const placedBox = placeFlowPill(footer.getBoundingClientRect(), name.getBoundingClientRect(), version.getBoundingClientRect(), want, GAP)
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
    root.value?.querySelector<HTMLElement>('.footer-name')?.style.removeProperty('max-width')
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
  for (const el of footer.querySelectorAll<HTMLElement>('.version-pill, .version-plain')) resize.observe(el)
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
const codename = computed(() => version.value?.codename || codenameOf(value.value))
const count = computed(() => session.identity ? releases.newCount : 0)
const label = computed(() => {
  const running = value.value ? `, ${releaseAria(codename.value, value.value)}` : ''
  const fresh = count.value === null ? ', new releases since your last visit' : count.value ? `, ${count.value} new since your last visit` : ''
  return `Release history${running}${fresh}`
})
</script>

<template>
  <footer ref="root" class="app-footer" :class="{ hidden }" :inert="hidden || undefined">
    <span class="footer-name"><span class="footer-wordmark">{{ brand.wordmark }}</span></span>
    <div v-if="pill" ref="slotEl" class="flow-slot" :class="{ placed: placed && pillOn, tight: tight && pillOn }" v-show="pillOn">
      <JourneyChip :key="pill.projectId" :project-id="pill.projectId" :active="pill.active" @go="openPill" @shown="onShown" />
    </div>
    <span class="spacer" />
    <button v-if="session.identity" type="button" class="version-pill" :aria-label="label" data-tip="Release history" @click="emit('releases')">
      <span class="pill-face">
        <AppIcon name="history" :size="13" />
        <span v-if="codename" class="footer-codename" lang="en">{{ codename }}</span>
        <span v-if="version.failed" class="fallback">Version unavailable</span>
        <CalendarVersion v-else-if="value" :value="value" class="pill-version" />
        <span v-else class="skeleton pill-skeleton" />
        <span v-if="count !== 0" class="new-badge" aria-hidden="true">{{ count === null ? 'New' : `${count} new` }}</span>
      </span>
    </button>
    <span v-else class="version-plain"><CalendarVersion v-if="value" :value="value" /></span>
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
.footer-name { min-width: 0; overflow: hidden; text-overflow: ellipsis; font: 600 10px/1 var(--mono); letter-spacing: .24em; color: var(--ink-3); white-space: nowrap; font-variant-ligatures: none; }
.spacer { flex: 1 1 0; }
/* Centred on the page when the sides allow it; the version pill stays in its slot. */
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
/* The button is the whole bar height (a comfortable target); the pill is its face. */
.version-pill { display: inline-flex; align-items: center; height: 100%; min-width: 0; max-width: 100%; flex: 0 1 auto; padding: 0; border: 0; background: transparent; color: var(--ink-2); }
.version-pill:focus-visible { box-shadow: none; }
.pill-face {
  display: inline-flex; align-items: center; gap: 7px; min-width: 0; height: 26px; padding: 0 10px 0 9px; border: 1px solid var(--glass-edge); border-radius: 999px;
  background: var(--field-bg); box-shadow: 0 0 0 1px var(--line); font: 500 12px/1 var(--mono); white-space: nowrap;
  transition: box-shadow .15s ease, background .15s ease, color .15s ease;
}
@media (hover: hover) { .version-pill:hover .pill-face { color: var(--teal-ink); background: var(--btn-bg-hover); box-shadow: 0 0 0 1px var(--glass-rim), 0 4px 12px -6px rgba(32, 60, 61, .3); } }
.version-pill:active .pill-face { background: var(--row-selected); }
.version-pill:focus-visible .pill-face { box-shadow: var(--focus-ring); }
.footer-codename { min-width: 0; max-width: 128px; overflow: hidden; text-overflow: ellipsis; font: 500 11.5px/1 var(--font); }
.pill-face > svg, .pill-version, .fallback, .pill-skeleton { flex-shrink: 0; }
.pill-version { color: var(--ink); font-size: 12px; line-height: 1; }
/* As wide as a calendar version in the pill, so the pill keeps its size when the version lands. */
.pill-skeleton { width: 94px; height: 8px; }
.fallback { font-family: var(--font); font-size: 11.5px; }
.new-badge {
  display: inline-flex; align-items: center; flex-shrink: 0; height: 17px; margin-right: -5px; padding: 0 7px; border-radius: 999px;
  background: var(--gold-2); color: #3a2804; font: 700 10px/1 var(--mono); letter-spacing: .02em; box-shadow: 0 0 0 1px rgba(154, 107, 18, .35);
}
.version-plain { font-size: 12px; color: var(--ink); }
@media (max-width: 600px) {
  .app-footer { gap: 8px; padding: 0 12px; transition: transform .22s ease; }
  .app-footer.hidden { transform: translateY(100%); }
  .footer-name { font-size: 9px; letter-spacing: .18em; }
  .pill-face { height: 30px; gap: 6px; }
  .footer-codename { max-width: 80px; font-size: 11px; }
  .pill-version { font-size: 11.5px; }
  .pill-skeleton { width: 90px; }
}
@media (prefers-reduced-motion: reduce) { .app-footer, .pill-face { transition: none; } }
</style>
