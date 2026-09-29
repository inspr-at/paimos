<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, defineAsyncComponent, nextTick, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import AppHeader from './components/AppHeader.vue'
import ConfirmHost from './components/ConfirmHost.vue'
import ToastHost from './components/ToastHost.vue'
import TooltipHost from './components/TooltipHost.vue'
import AppFooter from './components/AppFooter.vue'
import ErrorPage from './components/ErrorPage.vue'
import StatusPage from './components/StatusPage.vue'
import ShortcutSheet from './components/work/ShortcutSheet.vue'
import AppIcon from './components/AppIcon.vue'
import TicketPeekHost from './components/TicketPeekHost.vue'
import { command, consume } from './lib/commands'
import { clearFatal, fatal } from './lib/fatal'
import { provideTicketPeek } from './lib/ticketPeek'
import { useSession } from './stores/session'
import { useReleases } from './stores/releases'
import { brand } from './lib/brand'
import { toast } from './lib/toast'
import { displayHeadline, getRelease } from './lib/releases'
import { useProfile } from './stores/profile'
import { headerFolded } from './lib/chrome'
import { usePoller } from './lib/usePolledData'
import { sessionFreezeApplies } from './lib/agentPairing'

const ReleasesSheet = defineAsyncComponent(() => import('./components/releases/ReleasesSheet.vue'))
const session = useSession()
const profile = useProfile()
const route = useRoute()
const router = useRouter()
const ticketPeek = provideTicketPeek()
const main = ref<HTMLElement>()
const shortcuts = ref<InstanceType<typeof ShortcutSheet>>()
const retrying = ref(false)
// Sign-in is bare: it has no header or footer and shows connection problems itself.
const bare = computed(() => !!route.meta.bare && !fatal.value)
const sessionEndedHere = computed(() => sessionFreezeApplies(route.path, session.requiresSignIn))
// Keep the mounted page readable and copyable after a 401, while every editor
// and action control becomes inert. The observer covers controls rendered after
// an in-flight request settles.
function freezePage() {
  if (!sessionEndedHere.value) return
  for (const root of document.querySelectorAll<HTMLElement>('.page-flow, .sheet-root')) {
    for (const field of root.querySelectorAll<HTMLInputElement | HTMLTextAreaElement | HTMLSelectElement>('input, textarea, select')) {
      if (field instanceof HTMLSelectElement || (field instanceof HTMLInputElement && ['checkbox', 'radio', 'file', 'button', 'submit'].includes(field.type))) {
        if (!field.disabled) field.disabled = true
      } else if (!field.readOnly) field.readOnly = true
    }
    for (const button of root.querySelectorAll<HTMLButtonElement>('button:not([data-session-keep])')) if (!button.disabled) button.disabled = true
    for (const editor of root.querySelectorAll<HTMLElement>('[contenteditable="true"]')) editor.contentEditable = 'false'
  }
}
let freezeObserver: MutationObserver | undefined
watch(sessionEndedHere, async ended => { if (ended) { await nextTick(); freezePage() } })
function signInAgain() {
  window.open('/signin?error=expired', '_blank', 'noopener')
}
// A page that fills the screen (the quote editor) may fold the header away.
const folded = computed(() => headerFolded.value && !!route.meta.foldHeader && !bare.value && !fatal.value)
watch(command, value => { if (value?.command.name === 'shortcuts') { consume(); shortcuts.value?.open() } })
// A new page clears an earlier page error.
watch(() => route.fullPath, (_path, old) => { if (old !== undefined && fatal.value?.kind !== 'update') clearFatal() })
// ---------- Release history: /releases and /releases/<version>, or ?releases= over any page ----------
const releases = useReleases()
const releasesRoute = computed(() => route.path === '/releases' || route.path.startsWith('/releases/'))
const releasesQuery = computed(() => typeof route.query.releases === 'string' ? route.query.releases : null)
const releasesOpen = computed(() => !!session.identity && !bare.value && !fatal.value && (releasesRoute.value || releasesQuery.value !== null))
const releasesTarget = computed(() => {
  if (!releasesRoute.value) return releasesQuery.value
  const value = route.params.version
  return typeof value === 'string' && value ? value : null
})
let openedHere = false
function openReleases(version?: string) {
  if (releasesOpen.value) { if (version) selectRelease(version); return }
  openedHere = true
  void router.push({ path: route.path, query: { ...route.query, releases: version ?? 'all' }, hash: route.hash })
}
function selectRelease(version: string) {
  if (releasesRoute.value) { if (route.params.version !== version) void router.replace(`/releases/${version}`) }
  else if (releasesQuery.value !== version) void router.replace({ path: route.path, query: { ...route.query, releases: version }, hash: route.hash })
}
function closeReleases() {
  if (openedHere && typeof window.history.state?.back === 'string') { openedHere = false; router.back(); return }
  openedHere = false
  if (releasesRoute.value) { void router.replace('/'); return }
  const { releases: _releases, ...rest } = route.query
  void router.replace({ path: route.path, query: rest, hash: route.hash })
}
// The release history's mark goes home: a real navigation to /, which also closes the overlay.
function goHome() { openedHere = false; void router.push('/') }
// A ticket open beside the history can go to its full page, which leaves the history.
function leaveReleasesFor(path: string) { openedHere = false; void router.push(path) }
watch(releasesOpen, open => { if (!open) openedHere = false })
watch(command, value => { if (value?.command.name === 'releases') { consume(); openReleases() } })

// Signed in: remember what was seen, count what is new, and notice a newer version on the server.
watch(() => session.identity?.principal.id, id => {
  if (!id) return
  void releases.start()
  setTimeout(() => { if (session.identity) void releases.load() }, 2500)
}, { immediate: true })
function checkUpdate() { if (session.identity && document.visibilityState === 'visible') void releases.checkForUpdate() }
const updateTimer = usePoller(checkUpdate, 60_000, { enabled: () => !!session.identity })
onMounted(() => {
  freezeObserver = new MutationObserver(freezePage)
  freezeObserver.observe(document.body, { subtree: true, childList: true, attributes: true, attributeFilter: ['disabled', 'readonly', 'contenteditable'] })
  updateTimer.start()
  window.addEventListener('focus', checkUpdate)
})
onBeforeUnmount(() => { freezeObserver?.disconnect(); updateTimer.stop(); window.removeEventListener('focus', checkUpdate) })
watch(() => releases.available, async version => {
  if (!version) return
  // The new server knows what the release was about; say it in its reading form.
  const release = await getRelease(version)
  if (version !== releases.available) return
  const about = release?.headline ? `: ${displayHeadline(release, profile.profile?.locale)}` : ''
  toast(`${brand.value.wordmark} was updated to ${version}${about}`, {
    sticky: true, key: 'update',
    actions: [{ label: 'What’s new', run: () => openReleases(version) }, { label: 'Reload', run: () => window.location.reload() }],
  })
})

// ---------- Footer: on phones it folds away while reading down and returns on the way up ----------
const footerHidden = ref(false)
const phoneQuery = window.matchMedia('(max-width: 600px)')
let lastTop = 0, travel = 0, settleUntil = 0
function scrolled() {
  const el = main.value
  if (!el) return
  const top = el.scrollTop
  const delta = top - lastTop
  lastTop = top
  if (!phoneQuery.matches || Date.now() < settleUntil) return
  // Near the end of the page the footer stays: it is where the page ends.
  const nearEnd = el.scrollHeight - el.clientHeight - top < 48
  travel = Math.sign(delta) === Math.sign(travel) ? travel + delta : delta
  const next = nearEnd || top < 24 ? false : travel > 24 ? true : travel < -16 ? false : footerHidden.value
  if (next !== footerHidden.value) { footerHidden.value = next; travel = 0; settleUntil = Date.now() + 260 }
}
watch(() => route.path, () => { footerHidden.value = false })
phoneQuery.addEventListener('change', event => { if (!event.matches) footerHidden.value = false })

async function retry() {
  retrying.value = true
  await session.refresh()
  if (!session.error) await router.replace(session.identity ? '/' : '/signin')
  retrying.value = false
}
// Move focus to the page on real page changes; opening a ticket panel within a
// project keeps the list's focus and scroll position.
watch(() => [route.path, route.params.projectKey, route.params.ticketKey, route.matched.at(-1)] as const, async ([path, project, , record], old) => {
  if (old && project && project === old[1]) return
  if (old && path === old[0]) return
  // Moving within a route that manages its own focus (Settings > Access tabs and details) keeps it.
  if (old && record && record === old[3] && record.meta.keepsFocus) return
  await nextTick()
  main.value?.focus({ preventScroll: true })
})
</script>

<template>
  <div class="app-shell" :class="{ bare, 'footer-hidden': footerHidden && !bare, 'header-folded': folded }">
    <a class="skip-link" href="#main">Skip to content</a>
    <AppHeader v-if="!bare && !folded" />
    <main id="main" ref="main" tabindex="-1" @scroll.passive="scrolled">
      <div v-if="sessionEndedHere" class="session-ended" role="alert">
        <span>Your session has ended. Sign in again in a new tab; what you typed stays on this page.</span>
        <button type="button" class="btn sm" @click="signInAgain">Sign in</button>
      </div>
      <div class="page-flow" :class="{ fill: route.meta.fill && !session.error && !fatal, 'peek-open': !!ticketPeek.openKey.value && !releasesOpen }">
        <ErrorPage v-if="fatal" :error="fatal" />
        <StatusPage v-else-if="session.error && !bare && !route.meta.public" eyebrow="Connection interrupted" title="Let’s try that again." tone="problem">
          <p role="alert">{{ session.error }}</p>
          <template #actions>
            <button class="btn primary" type="button" :disabled="retrying" @click="retry"><AppIcon name="refresh" :size="14" />{{ retrying ? 'Connecting…' : 'Try again' }}</button>
          </template>
        </StatusPage>
        <RouterView v-else />
      </div>
    </main>
    <!-- A row of the shell: the page, docked panels and toasts all end above it. -->
    <AppFooter v-if="!bare" :hidden="footerHidden" @releases="openReleases()" />
    <ReleasesSheet v-if="releasesOpen" :target="releasesTarget" @select="selectRelease" @close="closeReleases" @home="goHome" @navigate="leaveReleasesFor" />
    <TicketPeekHost v-if="ticketPeek.openKey.value && !releasesOpen" :ref="ticketPeek.bind" :ticket-key="ticketPeek.openKey.value" :back-label="ticketPeek.backLabel.value" @close="ticketPeek.close()" />
    <ToastHost />
    <ConfirmHost />
    <ShortcutSheet ref="shortcuts" />
    <TooltipHost />
  </div>
</template>

<style scoped>
/* One column the width of the window: a long breadcrumb shrinks, it never widens the page. */
/* viewport-fit=cover (AEON-273): keep the shell out of the notch and home indicator. */
.app-shell { box-sizing: border-box; height: 100%; padding: 0 env(safe-area-inset-right) env(safe-area-inset-bottom) env(safe-area-inset-left); display: grid; grid-template-columns: minmax(0, 1fr); grid-template-rows: var(--header-h) minmax(0, 1fr) var(--footer-h); }
.app-shell.bare { --footer-h: 0px; grid-template-rows: minmax(0, 1fr); }
.app-shell.header-folded { grid-template-rows: minmax(0, 1fr) var(--footer-h); }
@media (max-width: 600px) {
  .app-shell { transition: --footer-h .22s ease; }
  .app-shell.footer-hidden { --footer-h: 0px; }
}
@media (prefers-reduced-motion: reduce) { .app-shell { transition: none; } }
/* The gutter is reserved so a scrollbar appearing as content loads never shifts the page sideways. */
main { position: relative; min-height: 0; overflow: auto; scrollbar-gutter: stable; outline: none; scroll-padding-top: 96px; }
main:focus-visible { box-shadow: none; }
.session-ended { position: sticky; top: 0; z-index: 50; display: flex; align-items: center; justify-content: space-between; flex-wrap: wrap; gap: 10px; padding: 10px 28px; background: var(--chip-teal-bg); box-shadow: inset 0 -1px 0 var(--line); font-size: 13px; }
.page-flow { display: flex; flex-direction: column; min-height: 100%; }
.page-flow > :first-child { flex: 1 0 auto; }
.page-flow.fill { height: 100%; }
.page-flow.fill > :first-child { flex: 1 1 auto; min-height: 0; }
@media (min-width: 1100px) {
  .page-flow.peek-open { padding-right: calc(var(--panel-w) + 22px); }
}
/* Out of sight until focused; its shadow too, or it smudges the top of bare pages. */
.skip-link { position: fixed; z-index: 90; top: 8px; left: 16px; padding: 10px 16px; border-radius: 999px; background: var(--surface-raised); box-shadow: none; transform: translateY(-160%); }
.skip-link:focus { transform: translateY(0); box-shadow: var(--shadow-pop); }
</style>
