<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { brand, pageName } from '../lib/brand'
import { computed, nextTick, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import mark from '../assets/brand/aeon-mark.svg'
import { useSession } from '../stores/session'
import { useProjects } from '../stores/projects'
import { useAgents } from '../stores/agents'
import { useBusiness } from '../stores/business'
import { useCustomers } from '../stores/customers'
import { useQuotes } from '../stores/quotes'
import { useProfile } from '../stores/profile'
import { dark, toggleTheme } from '../lib/theme'
import { can } from '../lib/authz'
import { command, consume, run } from '../lib/commands'
import { fatal } from '../lib/fatal'
import { usePoller } from '../lib/usePolledData'
import { placeOf, releaseChordOpen, sequence, visiblePlaces, type PlaceId } from '../lib/places'
import { SETTINGS_SECTIONS, sectionOf } from '../lib/settings'
import AppIcon from './AppIcon.vue'
import KeyCap from './KeyCap.vue'
import BizIcon from './business/BizIcon.vue'
import CommandPalette from './CommandPalette.vue'
import AccountMenu from './header/AccountMenu.vue'
import AppMenu from './header/AppMenu.vue'

const session = useSession()
const projects = useProjects()
const agents = useAgents()
const business = useBusiness()
const customers = useCustomers()
const quotes = useQuotes()
const profile = useProfile()
const route = useRoute()
const router = useRouter()
const palette = ref<InstanceType<typeof CommandPalette>>()
const writable = computed(() => can('nodes.write', project.value?.id))

// The legacy workspace view keeps its own search; everywhere else search is global.
const globalSearch = computed(() => !!session.identity)
const projectKey = computed(() => typeof route.params.projectKey === 'string' ? route.params.projectKey : '')
const project = computed(() => projectKey.value ? projects.byRouteKey(projectKey.value) : undefined)
// Full-page tickets get their own crumb; the side panel keeps the list as the page.
const fullTicket = computed(() => typeof route.params.ticketKey === 'string' && route.query.view === 'full' ? route.params.ticketKey.toUpperCase() : '')
// Knowledge: Projects / PHAROS Pharos / Knowledge / deploy-flow, and Projects / Knowledge across projects.
const projectKnowledge = computed(() => !!projectKey.value && route.path.includes('/knowledge'))
const knowledgeSlug = computed(() => projectKnowledge.value && typeof route.params.slug === 'string' ? route.params.slug : '')
const allKnowledge = computed(() => route.path === '/knowledge')
const pageTitle = computed(() => !activePlace.value && !route.path.startsWith('/settings') && route.path !== '/signin' ? String(route.meta.title ?? '') : '')
// The three places, in order of use; the active one is where this page lives.
// The breadcrumb continues from it: Projects / PHAROS Pharos / PHAROS-11.
const places = computed(() => visiblePlaces({ signedIn: !!session.identity, business: business.placeOpen }))
// The places and the trail appear together once it is known whether Business is one
// of the places (read, or remembered from this browser's last visit), so the trail
// never slides sideways when Business turns up a moment after the first paint.
const navReady = computed(() => !session.identity || business.placeKnown)
const activePlace = computed(() => placeOf(route.path))
const PLACE_ROOT: Record<PlaceId, string> = { projects: '/', agents: '/agents', business: '/business' }
const atPlaceRoot = computed(() => !!activePlace.value && (route.path === PLACE_ROOT[activePlace.value] || (activePlace.value === 'agents' && route.path.startsWith('/agents/'))))
const agentsPage = computed(() => activePlace.value === 'agents')
const businessPage = computed(() => activePlace.value === 'business')
// A customer's page reads Customers / its name; the other Business pages their title.
const businessCrumbs = computed(() => {
  if (!businessPage.value || route.path === '/business') return []
  if (/^\/business\/customers\/[^/]+$/.test(route.path)) return [{ label: 'Customers', to: '/business/customers' }, { label: pageName.value || 'Customer', to: '' }]
  if (/^\/business\/quotes\/[^/]+$/.test(route.path)) return [{ label: 'Quotes', to: '/business/quotes' }, { label: pageName.value || 'Quote', to: '' }]
  return [{ label: String(route.meta.title ?? ''), to: '' }]
})
// Document profiles live under Business: Settings / Business / Document profiles.
const profilesPage = computed(() => route.path.startsWith('/settings/business/profiles'))
const settingsSection = computed(() => route.path.startsWith('/settings') ? SETTINGS_SECTIONS.find(section => section.id === (profilesPage.value ? 'business' : sectionOf(route.params.section))) ?? null : null)
const placeLabel = (id: PlaceId) => id === 'agents' && agents.needsCount ? `Agents, ${agents.needsCount} ${agents.needsCount === 1 ? 'needs' : 'need'} you` : undefined

watch(command, value => { if (value?.command.name === 'palette') { consume(); palette.value?.open() } })
function typing(target: EventTarget | null) {
  return target instanceof HTMLElement && (target.isContentEditable || ['INPUT', 'TEXTAREA', 'SELECT'].includes(target.tagName))
}
// Pages with their own list search keep '/'; everywhere else it opens the palette.
const pageOwnsSlash = computed(() => route.path === '/' || route.path === '/business/customers' || route.path === '/business/quotes' || route.path === '/knowledge' || (!!projectKey.value && route.query.view !== 'full' && !knowledgeSlug.value))
function shortcut(event: KeyboardEvent) {
  if (!globalSearch.value) return
  if ((event.metaKey || event.ctrlKey) && !event.altKey && event.key.toLowerCase() === 'k') { event.preventDefault(); palette.value?.open(); return }
  if (event.metaKey || event.ctrlKey || event.altKey || event.defaultPrevented || typing(event.target) || document.querySelector('dialog[open], .floating')) return
  if (event.key === '/' && !pageOwnsSlash.value) { event.preventDefault(); palette.value?.open() }
  else if (event.key === '?') { event.preventDefault(); run({ name: 'shortcuts' }) }
}
// g p · g a · g b: go to a place. Listened for first (capture), so the second key
// never reaches the page (p is Priority on an open ticket, a approves on Agents).
// g on an open ticket also opens the release menu; that one layer must not swallow
// the place key that follows. Any other menu, dialog or focused field cancels the
// arm, and an expired arm must not re-arm inside one.
const nextKey = sequence()
let armedAt: number | null = null
function layerLabels(): string[] {
  return [...document.querySelectorAll('dialog[open], .floating')].map(layer => layer.getAttribute('aria-label') ?? '')
}
function releaseFromChord(now = Date.now()): Element | null {
  if (!releaseChordOpen(layerLabels(), armedAt, now)) return null
  return document.querySelector('dialog[open], .floating')
}
function disarmChord(now = Date.now()) {
  if (armedAt === null) return
  nextKey('\0', now, places.value)
  armedAt = null
}
function placeKeys(event: KeyboardEvent) {
  if (!session.identity || event.metaKey || event.ctrlKey || event.altKey || event.repeat) return
  const now = Date.now()
  if (typing(event.target) || (document.querySelector('dialog[open], .floating') && !releaseFromChord(now))) {
    disarmChord(now)
    return
  }
  const hit = nextKey(event.key, now, places.value)
  armedAt = hit === 'armed' ? now : null
  if (!hit || hit === 'armed') return
  event.preventDefault(); event.stopImmediatePropagation()
  if (route.path !== hit.to) void router.push(hit.to)
}
function chordPointer(event: PointerEvent) {
  if (armedAt === null) return
  const layer = releaseFromChord()
  if (layer && event.target instanceof Node && layer.contains(event.target)) return
  disarmChord()
}
function chordFocus(event: FocusEvent) { if (typing(event.target)) disarmChord() }
// The Agents badge: permission requests and held action requests, checked each minute.
const needsPoll = usePoller(() => agents.loadNeeds(true), 60_000, { enabled: () => !!session.identity && !agentsPage.value, invalidate: agents.invalidatePolls })
watch(() => session.identity?.principal.id, id => {
  if (!id) { business.reset(); customers.reset(); quotes.reset(); profile.reset(); return }
  void profile.load(true)
  void agents.loadNeeds(true)
  void business.loadPlugins(true)
}, { immediate: true })
// Phones: the moon steps aside only when the breadcrumb's page (a ticket key) would
// otherwise clip, and comes back as soon as the free room holds it (AEON-312). The
// decision is measured, never guessed from the number of places; its quick toggle
// then leads the avatar sheet. Room within a tenth of a pixel counts as enough, so
// subpixel noise never keeps it away; should its return clip the key anyway, it
// stays away at that width until the width, the page or the fonts change.
const header = ref<HTMLElement>()
const moonAway = ref(false)
const narrow = window.matchMedia('(max-width: 600px)')
let returnedAt = -1
let heldAt = -1
function fitMoon() {
  const el = header.value
  if (!el || !narrow.matches) { moonAway.value = false; return }
  const crumb = el.querySelector<HTMLElement>('.crumbs > .crumb.current')
  const overflow = crumb ? crumb.scrollWidth - crumb.clientWidth : 0
  const width = el.clientWidth
  if (!moonAway.value) {
    if (overflow > 0.5) { moonAway.value = true; if (returnedAt === width) heldAt = width }
    return
  }
  if (heldAt === width) return
  const spacer = el.querySelector<HTMLElement>('.spacer')
  const gap = parseFloat(getComputedStyle(el).columnGap) || 0
  if (spacer && spacer.getBoundingClientRect().width - Math.max(0, overflow) >= 44 + gap - 0.1) { moonAway.value = false; returnedAt = width }
}
let fitFrame = 0
function refit() { cancelAnimationFrame(fitFrame); fitFrame = requestAnimationFrame(() => { fitMoon(); fitFrame = requestAnimationFrame(fitMoon) }) }
function remeasure() { heldAt = -1; refit() }
const resized = new ResizeObserver(refit)
const crumbsChanged = new MutationObserver(refit)
// A web font swapping in changes the room without resizing the header.
const fonts = 'fonts' in document ? document.fonts : undefined
watch([() => route.fullPath, () => places.value.length, navReady], () => { heldAt = -1; void nextTick(refit) })
onMounted(() => {
  if (header.value) { resized.observe(header.value); crumbsChanged.observe(header.value, { childList: true, subtree: true, characterData: true }) }
  narrow.addEventListener('change', remeasure)
  fonts?.addEventListener('loadingdone', remeasure)
  void fonts?.ready.then(remeasure)
  refit()
  window.addEventListener('keydown', shortcut); window.addEventListener('keydown', placeKeys, true)
  window.addEventListener('pointerdown', chordPointer, true); window.addEventListener('focusin', chordFocus, true)
  needsPoll.start()
})
onBeforeUnmount(() => { resized.disconnect(); crumbsChanged.disconnect(); narrow.removeEventListener('change', remeasure); fonts?.removeEventListener('loadingdone', remeasure); cancelAnimationFrame(fitFrame); window.removeEventListener('keydown', shortcut); window.removeEventListener('keydown', placeKeys, true); window.removeEventListener('pointerdown', chordPointer, true); window.removeEventListener('focusin', chordFocus, true); needsPoll.stop() })
</script>

<template>
  <header ref="header" class="app-header">
    <RouterLink class="lockup" to="/" :aria-label="`${brand.wordmark} home`" :class="{ compact: !!projectKey || !!pageTitle || !!settingsSection || businessCrumbs.length > 0 }">
      <span class="mark-backing"><img :src="mark" width="26" height="26" alt="" /></span>
      <span class="wordmark">{{ brand.product }}<sup>{{ brand.release_name }}</sup></span>
    </RouterLink>
    <nav v-if="places.length && !fatal && navReady" class="places" aria-label="Places">
      <RouterLink
        v-for="place in places" :key="place.id" :to="place.to" class="place" :class="{ active: activePlace === place.id }"
        :aria-current="activePlace === place.id ? (atPlaceRoot ? 'page' : 'true') : undefined" :aria-label="placeLabel(place.id)"
        :data-tip="agents.needsCount && place.id === 'agents' ? `${agents.needsCount} waiting for you · g then a` : `g then ${place.key}`"
      >
        <BizIcon v-if="place.id === 'business'" name="briefcase" :size="16" />
        <AppIcon v-else :name="place.id === 'agents' ? 'agent' : 'folder'" :size="16" />
        <span class="place-text">{{ place.label }}</span>
        <span v-if="place.id === 'agents' && agents.needsCount" class="needs-badge" aria-hidden="true">{{ agents.needsCount > 99 ? '99+' : agents.needsCount }}</span>
      </RouterLink>
    </nav>
    <nav v-if="session.identity && !fatal && navReady && (projectKey || businessCrumbs.length || settingsSection || pageTitle || allKnowledge)" class="crumbs" :class="{ lead: !activePlace }" aria-label="Breadcrumb">
      <template v-if="projectKey">
        <span class="sep" aria-hidden="true">/</span>
        <RouterLink class="crumb project-crumb" :to="`/p/${encodeURIComponent(project?.routeKey ?? projectKey)}`" :aria-current="fullTicket || projectKnowledge ? undefined : 'page'">
          <span class="key-badge">{{ project?.routeKey ?? projectKey.toUpperCase() }}</span>
          <span class="crumb-name">{{ project?.title ?? '' }}</span>
        </RouterLink>
        <template v-if="fullTicket">
          <span class="sep" aria-hidden="true">/</span>
          <span class="crumb current mono-crumb" aria-current="page">{{ fullTicket }}</span>
        </template>
        <template v-else-if="projectKnowledge">
          <span class="sep" aria-hidden="true">/</span>
          <RouterLink v-if="knowledgeSlug" class="crumb fixed-crumb" :to="`/p/${encodeURIComponent(project?.routeKey ?? projectKey)}/knowledge`">Knowledge</RouterLink>
          <span v-else class="crumb current" aria-current="page">Knowledge</span>
          <template v-if="knowledgeSlug">
            <span class="sep" aria-hidden="true">/</span>
            <span class="crumb current mono-crumb slug-crumb" aria-current="page" :data-tip="knowledgeSlug.length > 28 ? knowledgeSlug : undefined">{{ knowledgeSlug }}</span>
          </template>
        </template>
      </template>
      <template v-else-if="allKnowledge">
        <span class="sep" aria-hidden="true">/</span>
        <span class="crumb current" aria-current="page">Knowledge</span>
      </template>
      <template v-else-if="businessCrumbs.length">
        <template v-for="crumb in businessCrumbs" :key="crumb.label">
          <span class="sep" aria-hidden="true">/</span>
          <RouterLink v-if="crumb.to" class="crumb" :to="crumb.to">{{ crumb.label }}</RouterLink>
          <span v-else class="crumb current" aria-current="page">{{ crumb.label }}</span>
        </template>
      </template>
      <template v-else-if="settingsSection">
        <RouterLink class="crumb settings-crumb" to="/settings"><AppIcon name="gear" :size="14" /><span class="crumb-label">Settings</span></RouterLink>
        <span class="sep" aria-hidden="true">/</span>
        <template v-if="profilesPage">
          <RouterLink class="crumb" to="/settings/business">{{ settingsSection.label }}</RouterLink>
          <span class="sep" aria-hidden="true">/</span>
          <span class="crumb current" aria-current="page"><span class="crumb-long">Document profiles</span><span class="crumb-short">Profiles</span></span>
        </template>
        <span v-else class="crumb current" aria-current="page">{{ settingsSection.label }}</span>
      </template>
      <span v-else class="crumb current" aria-current="page">{{ pageTitle }}</span>
    </nav>
    <span class="spacer" />
    <button v-if="globalSearch" class="search-pill" type="button" aria-label="Search everything" aria-keyshortcuts="Control+K Meta+K" @click="palette?.open()">
      <AppIcon name="search" :size="15" />
      <span class="pill-text">Search</span>
      <span class="pill-keys"><KeyCap k="mod" /><KeyCap k="K" /></span>
    </button>
    <!-- Quick light or dark; the avatar menu has the full choice, System included. -->
    <button class="icon-btn header-btn theme-btn" :class="{ away: moonAway }" type="button" :aria-label="dark ? 'Switch to light theme' : 'Switch to dark theme'" :data-tip="dark ? 'Light theme' : 'Dark theme'" @click="toggleTheme()">
      <AppIcon :name="dark ? 'sun' : 'moon'" />
    </button>
    <AppMenu />
    <AccountMenu />
    <CommandPalette v-if="globalSearch" ref="palette" :can-write="writable" />
  </header>
</template>

<style scoped>
.app-header {
  position: relative; z-index: 20; height: var(--header-h); padding: 0 var(--gutter); display: flex; align-items: center; gap: 14px;
  background: var(--glass-2); border-bottom: 1px solid var(--glass-edge); box-shadow: 0 1px 0 var(--line);
  -webkit-backdrop-filter: blur(16px) saturate(1.2); backdrop-filter: blur(16px) saturate(1.2);
}
.lockup { display: inline-flex; align-items: center; gap: 10px; min-height: 40px; padding-right: 4px; color: var(--ink); flex-shrink: 0; border-radius: 10px; }
.lockup:focus-visible { box-shadow: var(--focus-ring); }
.mark-backing { display: grid; place-items: center; width: 32px; height: 32px; border-radius: 9px; background: #f7f6f2; box-shadow: 0 0 0 1px var(--glass-rim); }
.mark-backing img { display: block; }
.wordmark { font: 600 13px/1 var(--mono); letter-spacing: .28em; white-space: nowrap; font-variant-ligatures: none; }
.wordmark sup { position: relative; top: -.15em; margin-left: 2px; font: 600 8px/1 var(--mono); letter-spacing: .16em; color: var(--teal-ink); }
/* Places: a quiet segmented group; the active place is the raised segment. */
.places { display: flex; align-items: center; gap: 2px; flex-shrink: 0; padding: 3px; border-radius: 999px; background: var(--seg-bg); box-shadow: inset 0 1px 2px rgba(32, 60, 61, .08); }
.place {
  position: relative; display: inline-flex; align-items: center; gap: 7px; height: 30px; padding: 0 12px 0 10px; border-radius: 999px;
  color: var(--ink-2); font-size: 13px; font-weight: 600; text-decoration: none; white-space: nowrap;
}
.place svg { color: var(--ink-3); }
@media (hover: hover) { .place:hover { color: var(--teal-ink); background: var(--row-hover); } .place:hover svg { color: var(--teal-ink); } }
.place:active { background: var(--row-selected); }
.place:focus-visible { box-shadow: var(--focus-ring); }
.place.active { background: var(--seg-on); color: var(--teal-ink); box-shadow: 0 1px 2px rgba(32, 60, 61, .12), inset 0 0 0 1px var(--glass-edge); }
.place.active svg { color: var(--teal); }
.place .needs-badge { margin: 0 -4px 0 1px; }
/* The breadcrumb continues from the active place; pages outside a place start it. */
.crumbs { display: flex; align-items: center; gap: 10px; flex: 0 1 auto; min-width: 0; overflow: hidden; height: 30px; font-size: 13.5px; }
.crumbs.lead { padding-left: 14px; }
.settings-crumb svg { color: var(--ink-3); }
.crumb { display: inline-flex; align-items: center; gap: 8px; min-width: 0; height: 30px; padding: 0 8px; margin: 0 -8px; border-radius: 8px; color: var(--ink-2); font-weight: 600; white-space: nowrap; }
.crumb:hover { color: var(--teal-ink); background: var(--row-hover); }
.crumb:active { background: var(--row-selected); }
.crumb:focus-visible { box-shadow: var(--focus-ring); }
.crumb[aria-current="page"] { color: var(--ink); }
.crumb.current { color: var(--ink); cursor: default; }
/* A long trail shrinks from the start: Settings folds to its gear, earlier crumbs
   clip, the page you are on stays whole. */
@media (max-width: 1180px) { .crumbs:has(> .crumb ~ .crumb ~ .crumb) .settings-crumb .crumb-label { position: absolute; width: 1px; height: 1px; overflow: hidden; clip-path: inset(50%); white-space: nowrap; } }
.crumbs > .crumb:not(.current) { flex-shrink: 1; overflow: hidden; }
.crumbs > .crumb.current, .crumbs > .sep, .crumbs > .crumb.fixed-crumb { flex-shrink: 0; }
/* A long knowledge slug gives way first, with an ellipsis and the full slug as a tip. */
.crumbs > .crumb.slug-crumb { display: block; flex-shrink: 1; min-width: 48px; overflow: hidden; text-overflow: ellipsis; line-height: 30px; }
.crumb.current:hover { background: transparent; }
.crumb-name { overflow: hidden; text-overflow: ellipsis; color: var(--ink); }
.crumb-short { display: none; }
.project-crumb { min-width: 0; }
.sep { color: var(--ink-3); font-weight: 300; font-size: 16px; }
.mono-crumb { font: 500 12px/1 var(--mono); letter-spacing: .02em; font-variant-ligatures: none; }
.spacer { flex: 1 1 0; min-width: 0; }
.search-pill, .header-btn { flex-shrink: 0; }
.needs-badge { display: inline-grid; place-items: center; min-width: 18px; height: 18px; padding: 0 5px; border-radius: 999px; background: var(--gold-2); color: #3a2804; font: 700 10.5px/1 var(--mono); font-variant-numeric: tabular-nums; box-shadow: 0 0 0 2px var(--surface-raised); }
.search-pill {
  display: inline-flex; align-items: center; gap: 9px; width: 240px; height: 34px; padding: 0 6px 0 12px; border: 1px solid var(--glass-edge); border-radius: 999px;
  background: var(--field-bg); box-shadow: var(--field-inset), 0 0 0 1px var(--line); color: var(--ink-3); font-size: 13px;
}
.search-pill:hover { color: var(--ink-2); box-shadow: var(--field-inset), 0 0 0 1px var(--glass-rim); }
.search-pill:active { background: var(--row-selected); }
.search-pill:focus-visible { box-shadow: var(--focus-ring); }
.pill-text { flex: 1; text-align: left; }
.pill-keys { display: inline-flex; gap: 3px; }
/* Narrower desktops: the wordmark steps back on inner pages, then the place labels. */
@media (max-width: 1180px) { .lockup.compact .wordmark { display: none; } }
@media (max-width: 980px) {
  .place { width: 36px; padding: 0; justify-content: center; }
  .place-text { position: absolute; width: 1px; height: 1px; overflow: hidden; clip: rect(0 0 0 0); white-space: nowrap; }
  .place .needs-badge { position: absolute; top: -4px; right: -6px; margin: 0; }
}
/* The search pill gives up width before a breadcrumb has to clip. */
@media (max-width: 1100px) { .search-pill { width: 200px; } }
@media (max-width: 900px) { .search-pill { width: 180px; } }
/* Just wider than the phone header, the fixed pill is a few pixels too wide for the row. */
@media (max-width: 720px) {
  .search-pill { flex-shrink: 1; min-width: 44px; overflow: hidden; }
  .search-pill .pill-text { min-width: 0; overflow: hidden; text-overflow: ellipsis; }
}
@media (max-width: 600px) {
  .app-header { gap: 4px; padding: 0 10px; }
  .lockup { min-height: 44px; min-width: 44px; justify-content: center; }
  /* Signed in, the Projects place is home and the footer carries the name: the lockup steps aside for the places. */
  .app-header:has(.places) .lockup { display: none; }
  .lockup.compact .wordmark { display: none; }
  .wordmark { font-size: 11.5px; letter-spacing: .22em; }
  /* Phones: the places are round header buttons like search and the avatar. */
  .places { gap: 2px; padding: 0; background: none; box-shadow: none; }
  .place { width: 44px; height: 44px; }
  .place:not(.active) { border: 1px solid var(--glass-edge); background: var(--btn-bg); box-shadow: var(--shadow-btn); }
  .place.active { box-shadow: 0 1px 2px rgba(32, 60, 61, .12), inset 0 0 0 1px var(--chip-teal-line); }
  .place .needs-badge { top: 2px; right: 0; }
  /* The breadcrumb keeps only where you are. */
  .crumbs { gap: 6px; }
  .crumbs > :not(:last-child) { display: none; }
  .crumbs.lead { padding-left: 2px; }
  .crumb { height: 44px; margin: 0; padding: 0 4px; }
  /* A long name ends in an ellipsis instead of a hard cut. */
  .crumbs > .crumb.current { display: block; flex-shrink: 1; min-width: 0; max-width: 100%; overflow: hidden; text-overflow: ellipsis; line-height: 44px; }
  .crumb-name { display: none; }
  .crumb-long { display: none; }
  .crumb-short { display: inline; }
  .search-pill { width: 44px; height: 44px; padding: 0; justify-content: center; }
  .pill-text, .pill-keys { display: none; }
  .app-header :deep(.header-btn) { width: 44px; height: 44px; }
}
/* The narrowest phones: round buttons sit close, as the places do, so a ticket
   key keeps its room. The moon steps aside only when measured room runs out. */
@media (max-width: 430px) { .app-header { gap: 2px; padding: 0 8px; } }
@media (max-width: 600px) { .theme-btn.away { display: none; } }
</style>
