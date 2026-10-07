<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { brand } from '../lib/brand'
import { footerReleaseContent } from '../lib/footerRelease'
import { footerLive, footerSource, footerSummary, landed, PAUSED_FULL, PAUSED_SHORT, phoneRoom, pingGate, said, type FooterPart, type FooterSummary } from '../lib/footerSummary'
import { codenameOf, releaseAria } from '../lib/codenames'
import { useReleases } from '../stores/releases'
import { useSession } from '../stores/session'
import { useVersion } from '../stores/version'
import AppIcon from './AppIcon.vue'
import CalendarVersion from './CalendarVersion.vue'

// The product mark stays left; the running release's name, Pretty version and
// new count share the history control on the right.
const props = defineProps<{ hidden?: boolean }>()
const emit = defineEmits<{ releases: [] }>()
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
onMounted(() => {
  window.addEventListener('resize', hideCard)
  document.addEventListener('scroll', hideCard, true)
})
onBeforeUnmount(() => { hideCard(); window.removeEventListener('resize', hideCard); document.removeEventListener('scroll', hideCard, true) })

// ---------- The centre: what this screen says (AEON-785) ----------
// Offline reads the same on every screen: a hollow amber dot and no action.
const paused = computed(() => !!footerLive.value?.paused)
const pausedSummary = computed<FooterSummary>(() => ({
  tone: 'attention', full: [said(PAUSED_FULL)], short: [said(PAUSED_SHORT)],
  aria: `Live updates paused, retrying.${lastUpdate.value ? ` The page shows the state of ${lastUpdate.value}.` : ''}`,
}))
const lastUpdate = computed(() => { const at = footerLive.value?.updatedAt; return at ? new Date(at).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' }) : '' })
const summary = computed(() => paused.value ? pausedSummary.value : footerSummary.value)
const parts = (list: readonly FooterPart[]) => list.map((part, i) => ({ ...part, key: `${i}:${part.text}` }))
const fullParts = computed(() => parts(summary.value?.full ?? []))
const shortParts = computed(() => parts(summary.value?.short ?? []))
// A changed number gets a short highlight; a new screen's first numbers do not.
const countsText = computed(() => summary.value?.loading || paused.value ? '' : (summary.value?.full ?? []).filter(part => part.as === 'count').map(part => part.text).join('|'))
const highlight = ref(false)
watch([footerSource, countsText], ([source, text], [before, was]) => { highlight.value = source === before && !!was && !!text && text !== was })
// The dot rings once when fresh data lands, at most once per 2 s; never while paused or loading.
const ping = ref(0)
const gate = pingGate()
let seen: { source: symbol | null; at: number | null } | null = null
watch(() => ({ source: footerSource.value, at: footerLive.value?.updatedAt ?? null }), next => {
  if (landed(seen, next) && !paused.value && !summary.value?.loading && gate()) ping.value++
  seen = next
}, { flush: 'sync', immediate: true })
// Polite announcement only when the tone turns problem.
const announcement = ref('')
watch(() => summary.value?.tone, (tone, before) => { announcement.value = tone === 'problem' && before !== 'problem' ? summary.value?.aria ?? '' : '' })
function activate() { if (!paused.value) summary.value?.action?.() }
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
  <footer class="app-footer" :class="{ hidden, speaking: phoneRoom(summary) }" :inert="hidden || undefined">
    <span class="footer-name"><span class="footer-wordmark">{{ brand.wordmark }}</span></span>
    <div class="foot-c">
      <component :is="summary.action && !paused ? 'button' : 'span'" v-if="summary" class="sum" :class="{ loading: summary.loading }" :type="summary.action && !paused ? 'button' : undefined"
        :data-tone="summary.tone" :data-conn="paused ? 'off' : 'on'" :role="summary.action && !paused ? undefined : 'status'" :aria-label="summary.aria" :data-tip="summary.loading ? undefined : summary.aria" @click="activate">
        <span :key="ping" class="sum-dot" :class="{ ping: ping > 0 }" aria-hidden="true" />
        <span v-if="summary.loading" class="sum-skel skeleton" aria-hidden="true" />
        <template v-else>
          <span class="full" aria-hidden="true"><template v-for="part in fullParts" :key="part.key"><b v-if="part.as === 'count'" :class="{ flash: highlight }">{{ part.text }}</b><span v-else-if="part.as === 'exception'" class="x">{{ part.text }}</span><template v-else>{{ part.text }}</template></template></span>
          <span class="short" aria-hidden="true"><template v-for="part in shortParts" :key="part.key"><b v-if="part.as === 'count'">{{ part.text }}</b><span v-else-if="part.as === 'exception'" class="x">{{ part.text }}</span><template v-else>{{ part.text }}</template></template></span>
        </template>
      </component>
      <span class="sr-only" aria-live="polite">{{ announcement }}</span>
    </div>
    <div class="foot-r">
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
    </div>
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
  position: relative; z-index: 18; display: flex; align-items: center; gap: 12px; height: var(--footer-h); min-height: 0; padding: 0 var(--gutter); overflow: clip; container: foot / inline-size;
  background: var(--glass-2); box-shadow: inset 0 1px 0 var(--glass-edge), 0 -1px 0 var(--line);
  -webkit-backdrop-filter: blur(16px) saturate(1.2); backdrop-filter: blur(16px) saturate(1.2);
  color: var(--ink-2);
}
.footer-name { flex: 1 1 0; min-width: 0; overflow: hidden; text-overflow: ellipsis; font: 600 10px/16px var(--mono); letter-spacing: .24em; color: var(--ink-3); white-space: nowrap; font-variant-ligatures: none; }
/* The centre holds only the summary. It never changes the size of its neighbours: the mark and the release sit in their own tracks. */
.foot-c { flex: 0 1 auto; display: flex; align-items: center; justify-content: center; gap: 10px; min-width: 0; height: 100%; }
.foot-r { flex: 1 1 0; display: flex; align-items: center; justify-content: flex-end; min-width: 0; height: 100%; }
.sum { display: inline-flex; align-items: center; gap: 8px; max-width: 100%; height: 28px; padding: 0 10px 0 9px; border: 0; border-radius: 999px; background: transparent; color: var(--ink-2); font: 400 12.5px/16px var(--font); white-space: nowrap; font-variant-numeric: tabular-nums; --tone: var(--ok); }
.sum > .full, .sum > .short { min-width: 0; overflow: hidden; text-overflow: ellipsis; }
button.sum { cursor: pointer; }
@media (hover: hover) { button.sum:hover { background: var(--row-hover); color: var(--ink); } }
button.sum:focus-visible { outline: none; box-shadow: var(--focus-ring); }
button.sum:active { background: var(--row-selected); }
.sum b { color: var(--ink); font-weight: 650; }
.sum .x { color: var(--tone-ink, var(--ink)); font-weight: 650; }
.sum[data-tone="problem"] { --tone: var(--agent-problem); --tone-ink: var(--agent-problem); }
.sum[data-tone="attention"] { --tone: var(--gold); --tone-ink: var(--warn-ink); }
.sum[data-tone="deliberate"] { --tone: var(--teal); --tone-ink: var(--teal-ink); }
.sum[data-tone="idle"] { --tone: var(--st-backlog); }
.sum .short { display: none; }
.sum-dot { position: relative; flex: none; width: 7px; height: 7px; border-radius: 50%; background: var(--tone); }
.sum[data-conn="off"] { color: var(--ink-3); }
.sum[data-conn="off"] .sum-dot { background: transparent; box-shadow: inset 0 0 0 1.5px var(--gold); }
/* Live: one soft ring each time fresh data lands. No glow, no word. */
.sum-dot::after { content: ''; position: absolute; inset: -4px; border-radius: 50%; box-shadow: 0 0 0 1px var(--tone); opacity: 0; }
.sum-skel { display: inline-block; flex: none; width: 150px; height: 8px; border-radius: 4px; }
@media (prefers-reduced-motion: no-preference) {
  .sum-dot.ping::after { animation: sum-ping 1.4s ease-out 1; }
  .sum .flash { animation: sum-tick 1.6s ease-out 1; border-radius: 4px; }
}
@keyframes sum-ping { 0% { transform: scale(.55); opacity: .85; } 100% { transform: scale(1.9); opacity: 0; } }
@keyframes sum-tick { 0% { background: var(--mark-hl); } 100% { background: transparent; } }
/* Narrower footers say the short form. */
@container foot (max-width: 1000px) {
  .sum .full { display: none; }
  .sum .short { display: inline; }
}
@media (pointer: coarse) { .sum { height: 100%; min-height: 28px; } }
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
.new-badge { display: inline-flex; align-items: center; flex: none; gap: 5px; margin-left: 10px; font: 600 11px/16px var(--font); color: var(--secondary-ink); }
.new-dot { width: 6px; height: 6px; border-radius: 50%; background: var(--secondary-line); }
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
.card-new { margin-left: auto; color: var(--secondary-ink); font-weight: 600; white-space: nowrap; }
.version-plain { position: relative; display: inline-flex; align-items: center; flex-shrink: 0; min-width: 112px; font-size: 12px; color: var(--ink); }
@media (max-width: 600px) {
  .app-footer { gap: 8px; padding: 0 12px; transition: transform .22s ease; }
  .footer-name { flex: 0 1 auto; }
  .foot-c { flex: 1 1 0; }
  /* The pull toward the edge sits on the wrapper: a percentage max-width on the pill would resolve against its own size. */
  .foot-r { flex: 0 0 auto; max-width: calc(100% - 8px); margin-right: -6px; }
  /* With a summary in the middle the release keeps its name and new count; its time makes room (the design's phone footer). */
  .app-footer.speaking .version-value, .app-footer.speaking .release-divider { display: none; }
  .sum-skel { width: 64px; }
  .sum { height: 44px; padding: 0 8px; }
  .app-footer.hidden { transform: translateY(100%); }
  .footer-name { font-size: 9px; letter-spacing: .18em; }
  .version-pill { flex-shrink: 0; margin-right: 0; }
  .pill-face { height: 32px; padding: 0 6px; }
  .release-divider { margin: 0 7px; }
  .footer-codename { max-width: 104px; font-size: 12px; }
  .pill-version { font-size: 11.5px; }
}
@media (prefers-reduced-motion: reduce) { .app-footer, .pill-face { transition: none; } }
</style>
