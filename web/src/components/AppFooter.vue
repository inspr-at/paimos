<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { useRoute } from 'vue-router'
import { brand } from '../lib/brand'
import { footerReleaseContent } from '../lib/footerRelease'
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
// new count share the history control on the right. The opt-in flow sits between
// them, taking the wordmark's room when needed.
const props = defineProps<{ hidden?: boolean }>()
const emit = defineEmits<{ releases: []; pill: [shown: boolean] }>()
const route = useRoute()
const onProject = computed(() => typeof route.params.projectKey === 'string' && route.params.projectKey !== '')
const { showFlowControls } = useDeveloperSettings()
const pill = computed(() => showFlowControls.value && onProject.value ? flowPillContext.value : null)
const root = ref<HTMLElement>()
const releaseControl = ref<HTMLButtonElement>()
const card = ref<HTMLElement>()
const cardShown = ref(false)
const cardPosition = ref({ left: '0px', top: '0px' })
const cardNow = ref(Date.now())
let cardTimer: ReturnType<typeof setTimeout> | undefined
let cardClock: ReturnType<typeof setInterval> | undefined
let cardHovered = false
let cardFocused = false

async function showCard() {
  clearTimeout(cardTimer)
  if (props.hidden || !session.identity) return
  cardNow.value = Date.now()
  cardShown.value = true
  clearInterval(cardClock)
  cardClock = setInterval(() => { cardNow.value = Date.now() }, 60_000)
  void releases.load()
  await nextTick()
  positionCard()
}
function positionCard() {
  const control = releaseControl.value, element = card.value
  if (!control || !element) return
  const anchor = control.getBoundingClientRect(), box = element.getBoundingClientRect()
  const scale = box.width / element.offsetWidth || 1
  cardPosition.value = { left: `${Math.max(8, Math.min(anchor.right - box.width, innerWidth - box.width - 8)) / scale}px`, top: `${Math.max(8, anchor.top - box.height - 8) / scale}px` }
}
function hideCard() { clearTimeout(cardTimer); clearInterval(cardClock); cardShown.value = false }
function enterCard(event: PointerEvent) {
  if (event.pointerType === 'touch' || event.pointerType === 'pen') return
  cardHovered = true
  clearTimeout(cardTimer)
  if (!cardShown.value) cardTimer = setTimeout(() => void showCard(), 380)
}
function leaveCard() { cardHovered = false; if (!cardFocused) hideCard() }
function focusCard() {
  cardFocused = releaseControl.value?.matches(':focus-visible') ?? false
  if (cardFocused) void showCard()
}
function blurCard() { cardFocused = false; if (!cardHovered) hideCard() }
function openReleases() { hideCard(); emit('releases') }
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
  window.addEventListener('resize', hideCard)
  document.addEventListener('scroll', hideCard, true)
  watchSize()
  const footer = root.value
  if (!footer) return
  mutations = new MutationObserver(() => { watchSize(); schedule() })
  mutations.observe(footer, { childList: true, subtree: true, characterData: true })
  void document.fonts?.ready.then(() => schedule())
})
onBeforeUnmount(() => { hideCard(); window.removeEventListener('resize', hideCard); document.removeEventListener('scroll', hideCard, true); resize?.disconnect(); mutations?.disconnect(); cancelAnimationFrame(raf); emit('pill', false) })
watch(pill, value => { if (!value) onShown(false) })
const version = useVersion()
const releases = useReleases()
const session = useSession()
void version.load()
const value = computed(() => version.value?.version ?? '')
// Layout only: the pinned Pretty renderer uses tabular digits, so this hidden
// stamp reserves exactly the running version's resting width before it arrives.
const sizingVersion = '260101000000.0.0'
const codename = computed(() => version.value?.codename || codenameOf(value.value))
const count = computed(() => session.identity ? releases.newCount : 0)
const cardContent = computed(() => footerReleaseContent(value.value, version.value?.scheme, codename.value, count.value, releases.history?.releases.find(release => release.version === value.value), cardNow.value, version.failed))
watch([() => props.hidden, () => session.identity], hideCard)
watch(cardContent, async () => { if (cardShown.value) { await nextTick(); positionCard() } })
const label = computed(() => {
  const running = version.failed ? ', version unavailable' : value.value ? `, ${releaseAria(codename.value, value.value)}` : ''
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
    <button v-if="session.identity" ref="releaseControl" type="button" class="version-pill" :aria-label="label"
      @pointerenter="enterCard" @pointerleave="leaveCard" @focus="focusCard" @blur="blurCard" @keydown.esc="hideCard" @click="openReleases">
      <span class="pill-face">
        <AppIcon name="history" :size="13" />
        <span v-if="codename || (!value && !version.failed)" class="footer-codename" lang="en"><template v-if="codename">{{ codename }}</template><span v-else class="skeleton name-skeleton" aria-hidden="true" /></span>
        <span v-if="codename || (!value && !version.failed)" class="release-divider" aria-hidden="true" />
        <span v-if="!version.failed" class="version-value">
          <CalendarVersion :value="value || sizingVersion" :scheme="version.value?.scheme" rest :aria-hidden="!value || undefined" class="pill-version" :class="{ pending: !value }" />
          <span v-if="!value" class="skeleton pill-skeleton" aria-hidden="true" />
        </span>
        <span v-else class="fallback">Version unavailable</span>
        <span v-if="count !== null && count > 0" class="new-badge" aria-hidden="true"><span class="new-dot" />{{ count }} new</span>
      </span>
    </button>
    <span v-else class="version-plain">
      <CalendarVersion :value="value || sizingVersion" :rest="!value" :aria-hidden="!value || undefined" :class="{ pending: !value }" />
      <span v-if="version.failed" class="fallback">Version unavailable</span>
      <span v-else-if="!value" class="skeleton pill-skeleton" aria-hidden="true" />
    </span>
  </footer>
  <Teleport to="body">
    <div v-if="cardShown" ref="card" class="release-hover-card" :style="cardPosition" aria-hidden="true">
      <p class="eyebrow">{{ cardContent.heading }}</p>
      <p v-if="cardContent.name" class="card-name" lang="en">{{ cardContent.name }}</p>
      <p class="card-version"><CalendarVersion v-if="value" :value="value" :scheme="version.value?.scheme" full /><template v-else>{{ cardContent.version }}</template></p>
      <p v-if="cardContent.released" class="card-meta">{{ cardContent.released }}</p>
      <p class="card-foot"><AppIcon name="history" :size="13" />{{ cardContent.action }}<span v-if="cardContent.fresh" class="card-new">{{ cardContent.fresh }}</span></p>
    </div>
  </Teleport>
</template>

<style scoped>
/* Same glass language as the header rail, mirrored: a hairline on top. */
.app-footer {
  position: relative; z-index: 18; display: flex; align-items: center; gap: 12px; height: var(--footer-h); min-height: 0; padding: 0 var(--gutter); overflow: clip;
  background: var(--glass-2); box-shadow: inset 0 1px 0 var(--glass-edge), 0 -1px 0 var(--line);
  -webkit-backdrop-filter: blur(16px) saturate(1.2); backdrop-filter: blur(16px) saturate(1.2);
  color: var(--ink-2);
}
.footer-name { min-width: 0; overflow: hidden; text-overflow: ellipsis; font: 600 10px/16px var(--mono); letter-spacing: .24em; color: var(--ink-3); white-space: nowrap; font-variant-ligatures: none; }
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
/* The face uses natural widths; hover and focus never resize the target. */
.version-pill { display: inline-flex; align-items: center; height: 100%; min-width: 0; max-width: 100%; flex: 0 1 auto; margin-right: -8px; padding: 0; border: 0; background: transparent; color: var(--ink-2); }
.version-pill:focus-visible { box-shadow: none; }
.pill-face { display: inline-flex; align-items: center; min-width: 0; height: 28px; padding: 0 8px; border-radius: 8px; white-space: nowrap; transition: background-color .15s ease, color .15s ease; }
.pill-face > svg { color: var(--ink-3); margin-right: 7px; }
.footer-codename { min-width: 0; max-width: 168px; overflow: hidden; text-overflow: ellipsis; font: 500 12.5px/16px var(--font); color: var(--ink); }
.release-divider { flex: none; width: 1px; height: 12px; margin: 0 9px; background: var(--line-2); }
.name-skeleton { display: block; width: 75px; height: 8px; }
.version-value { position: relative; display: inline-flex; align-items: center; flex-shrink: 0; }
.pending { visibility: hidden; }
.pill-version { flex-shrink: 0; color: var(--ink-2); font-size: 12px; line-height: 16px; }
.pill-skeleton { position: absolute; left: 0; right: 0; height: 8px; }
.fallback { font: 500 11.5px/16px var(--font); }
.version-plain .fallback { position: absolute; left: 0; }
.new-badge { display: inline-flex; align-items: center; flex: none; gap: 5px; margin-left: 10px; font: 600 11px/16px var(--font); color: var(--gold-ink); }
.new-dot { width: 6px; height: 6px; border-radius: 50%; background: var(--gold); }
@media (hover: hover) {
  .version-pill:hover .pill-face { background-color: var(--row-hover); }
  .version-pill:hover .footer-codename, .version-pill:hover .pill-face > svg { color: var(--teal-ink); }
}
.version-pill:focus-visible .pill-face { box-shadow: var(--focus-ring); }
.version-pill:focus-visible .footer-codename, .version-pill:focus-visible .pill-face > svg { color: var(--teal-ink); }
.version-pill:active .pill-face { background-color: var(--row-selected); }
.release-hover-card { position: fixed; z-index: 80; width: 280px; max-width: calc(100% - 16px); padding: 13px 15px 12px; border-radius: 12px; pointer-events: none; background: var(--surface-raised-2); -webkit-backdrop-filter: blur(18px) saturate(1.2); backdrop-filter: blur(18px) saturate(1.2); box-shadow: var(--shadow-pop); }
.release-hover-card .eyebrow { font-size: 9.5px; }
.card-name { margin-top: 4px; font: 300 21px/1.25 var(--serif); letter-spacing: -.015em; color: var(--ink); overflow-wrap: anywhere; }
.card-version { margin-top: 3px; font-size: 13px; line-height: 18px; color: var(--ink); }
.card-meta { margin-top: 2px; font-size: 12px; line-height: 1.45; color: var(--ink-2); }
.card-foot { display: flex; align-items: center; gap: 7px; margin-top: 10px; padding-top: 9px; border-top: 1px solid var(--line); font-size: 12px; color: var(--ink-3); }
.card-new { margin-left: auto; color: var(--gold-ink); font-weight: 600; white-space: nowrap; }
.version-plain { position: relative; display: inline-flex; align-items: center; flex-shrink: 0; min-width: 112px; font-size: 12px; color: var(--ink); }
@media (max-width: 600px) {
  .app-footer { gap: 8px; padding: 0 12px; transition: transform .22s ease; }
  .app-footer.hidden { transform: translateY(100%); }
  .footer-name { font-size: 9px; letter-spacing: .18em; }
  /* Let the name shrink on narrow phones while retaining room for the flow. */
  .version-pill { flex-shrink: 0; margin-right: -6px; max-width: calc(100% - 8px); }
  .pill-face { height: 32px; padding: 0 6px; }
  .release-divider { margin: 0 7px; }
  .footer-codename { max-width: 104px; font-size: 12px; }
  .pill-version { font-size: 11.5px; }
}
@media (prefers-reduced-motion: reduce) { .app-footer, .pill-face { transition: none; } }
</style>
