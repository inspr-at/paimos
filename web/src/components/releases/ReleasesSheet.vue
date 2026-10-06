<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, onMounted, provide, reactive, ref, watch } from 'vue'
import { useRoute } from 'vue-router'
import mark from '../../assets/brand/aeon-mark.svg'
import { brand, setOverlayTitle } from '../../lib/brand'
import { codenameOf } from '../../lib/codenames'
import { useDeveloperSettings } from '../../lib/developerSettings'
import { groupByDay, isCalendarVersion, liveServer, matches, presentRelease, railLine, releaseCopy, releasedAt, releaseLang, releaseLangKey, releaseNotice, releaseView, span, ticketsOf, visibleReleases, type Release, type ReleaseLang, type ReleaseView } from '../../lib/releases'
import { clockSince } from '../../lib/releaseStats'
import { useProfile } from '../../stores/profile'
import { useSession } from '../../stores/session'
import { normalKey } from '../../lib/ticketLinks'
import { absoluteTime, relativeTime } from '../../lib/work'
import { useReleases } from '../../stores/releases'
import { useVersion } from '../../stores/version'
import AppIcon from '../AppIcon.vue'
import CalendarVersion from '../CalendarVersion.vue'
import ReleaseName from '../ReleaseName.vue'
import LangBadge from './LangBadge.vue'
import ReleaseCodename from './ReleaseCodename.vue'
import ReleaseCompare from './ReleaseCompare.vue'
import ReleaseDetail from './ReleaseDetail.vue'
import ReleaseStats from './ReleaseStats.vue'
import ReleaseTicketPanel from './ReleaseTicketPanel.vue'
import ReleaseVersionCopy from './ReleaseVersionCopy.vue'
import TicketLink from './TicketLink.vue'
import { onAccessChange, permissionsRevoked } from '../../lib/authz'
import { appendPendingChanges, getPendingChanges, pendingLine, type PendingChanges } from '../../lib/releasePending'
import { plainSubject, shortCommit } from '../../lib/releases'
import { TICKET_PEEK } from '../../lib/ticketPeek'

// The release history: a full-screen sheet over the page. Releases by day on the
// left, the selected one (or a comparison of two) on the right; on phones the
// detail replaces the list. j/k move, / searches, c compares, Enter opens,
// e shows the evidence, ? lists the keys, Esc steps back and finally closes.
// A ticket key opens that ticket in the app's side panel beside the history.
const props = defineProps<{ target: string | null }>()
const emit = defineEmits<{ select: [version: string]; query: [key: 'release_lang' | 'release_view', value: string]; close: []; home: []; navigate: [path: string] }>()
const store = useReleases()
const { showReservedVersions } = useDeveloperSettings()
const version = useVersion()
const profile = useProfile()
const session = useSession()
const route = useRoute()
// EN | DE and Highlights | Details (AEON-323). Both live in the address under
// their own keys, since the page behind the sheet may use ?view= itself, so a
// link, a reload and a version change keep them. The language is also this
// person's remembered choice on this device, else the profile's. The labels
// read the same in both languages.
const langKey = computed(() => session.identity ? releaseLangKey(session.identity.principal.id) : '')
const remembered = ref<string | null>(null)
watch(langKey, key => { try { remembered.value = key ? localStorage.getItem(key) : null } catch { remembered.value = null } }, { immediate: true })
// A choice waits here until the address has it, so two quick clicks both land.
type SwitchKey = 'release_lang' | 'release_view'
const pending = reactive<Partial<Record<SwitchKey, string>>>({})
watch(() => route.query, query => { for (const key of ['release_lang', 'release_view'] as const) if (query[key] === pending[key]) delete pending[key] })
const lang = computed(() => releaseLang(pending.release_lang ?? route.query.release_lang, profile.profile?.locale, remembered.value))
const view = computed(() => releaseView(pending.release_view ?? route.query.release_view))
const locale = lang
const copyText = computed(() => releaseCopy(lang.value))
const LANGS = ['en', 'de'] as const
const VIEWS = ['highlights', 'details'] as const
// The address changes where the release does: in the app, so a choice and a
// selection it causes (a filter hiding the selected release) land together.
function putQuery(key: SwitchKey, value: string) {
  if ((pending[key] ?? route.query[key]) === value) return
  pending[key] = value
  emit('query', key, value)
}
function chooseLang(next: ReleaseLang) {
  remembered.value = next
  try { if (langKey.value) localStorage.setItem(langKey.value, next) } catch { /* private window: the address keeps it */ }
  putQuery('release_lang', next)
}
function chooseView(next: ReleaseView) { putQuery('release_view', next) }
// The W3C radio group: Right and Down choose the next option, Left and Up the
// one before, wrapping; Home and End the first and last. Focus follows.
const SWITCH_STEP: Record<string, number> = { ArrowRight: 1, ArrowDown: 1, ArrowLeft: -1, ArrowUp: -1 }
function switchKeys(event: KeyboardEvent, current: string, options: readonly string[], choose: (value: string) => void) {
  const key = event.key
  if (!(key in SWITCH_STEP) && key !== 'Home' && key !== 'End') return
  event.preventDefault()
  event.stopPropagation()
  const index = Math.max(0, options.indexOf(current))
  const next = key === 'Home' ? 0 : key === 'End' ? options.length - 1 : (index + SWITCH_STEP[key]! + options.length) % options.length
  choose(options[next]!)
  const group = event.currentTarget as HTMLElement
  void nextTick(() => group.querySelectorAll<HTMLButtonElement>('[role="radio"]')[next]?.focus())
}

const dialog = ref<HTMLDialogElement>()
const listbox = ref<HTMLElement>()
const detailScroll = ref<HTMLElement>()
const searchInput = ref<HTMLInputElement>()
const detail = ref<InstanceType<typeof ReleaseDetail>>()
const now = ref(Date.now())
const filter = reactive({ q: '', features: false, fixes: false, other: false })
const cursor = ref<string | null>(null)
const mode = ref<'browse' | 'compare'>('browse')
const compareFrom = ref<string | null>(null)
// A direct release URL opens the detail on phones from the first frame.
// `current` (the footer, before the running version is known) stays on the list
// until that intent resolves to a real release.
const showDetail = ref(window.matchMedia('(max-width: 760px)').matches && !!props.target && props.target !== 'all' && props.target !== 'current')
const help = ref(false)
const evidence = ref(false)
const missing = ref('')
// Keys stay inert until the open-time refetch has chosen a row. The dialog is
// focused while that request is in flight, and a key then would hit an empty
// list or a history that is about to be replaced.
const keysReady = ref(false)
// The latest version this sheet has asked the address to show and not yet seen
// come back. The navigation waits on the session, so the address lands after
// the cursor has moved — often into Compare, which never writes the address.
// Only that echo is ignored. A newer request replaces it. A navigation that
// settles on a failure, or any other address, drops it: Back, Forward and an
// in-app link then select that release, and Compare closes.
let pendingEcho = ''
function targetVersion(target: string | null) {
  return target && target !== 'all' && target !== 'current' ? target.replace(/^v/, '') : ''
}
function publishSelection(version: string) {
  // A request that already matches the address will not change it, so it is not
  // an echo — and it supersedes one that was still in flight.
  pendingEcho = version === targetVersion(props.target) ? '' : version
  emit('select', version)
}
// The address did not adopt this version: the replace was cancelled, duplicated
// or rejected. A later visit to it is someone opening that release.
function navigationSettled(version: string | undefined, failure: unknown) {
  if (!failure || !version || version !== pendingEcho) return
  pendingEcho = ''
}
defineExpose({ navigationSettled })
const phoneQuery = window.matchMedia('(max-width: 760px)')
const phone = ref(phoneQuery.matches)
const onPhone = (event: MediaQueryListEvent) => { phone.value = event.matches }
// Once the overview stacks, both cards belong in the scrolling list, including
// tablet landscape widths where the list and detail still sit side by side.
const scrollStatsQuery = window.matchMedia('(max-width: 900px)')
const scrollStats = ref(scrollStatsQuery.matches)
const onScrollStats = (event: MediaQueryListEvent) => { scrollStats.value = event.matches }

const history = computed(() => store.history)
const releases = computed(() => [...(history.value?.releases ?? [])].sort((a, b) => b.version.localeCompare(a.version)))
const eligible = computed(() => visibleReleases(releases.value, showReservedVersions.value))
// Result counts and statistics describe the full history, even when rows are hidden.
const matching = computed(() => releases.value.filter(r => matches(r, filter, locale.value, view.value)))
const visible = computed(() => visibleReleases(matching.value, showReservedVersions.value))
const days = computed(() => groupByDay(visible.value, now.value))
const order = computed(() => days.value.flatMap(d => d.releases))
const indexOf = computed(() => new Map(order.value.map((r, i) => [r.version, i])))
const byVersion = computed(() => new Map(releases.value.map(r => [r.version, r])))
// Each row's name in the chosen language, with the language it fell back to.
const rails = computed(() => new Map(visible.value.map(r => [r.version, railLine(r, locale.value)])))
const selected = computed(() => cursor.value ? byVersion.value.get(cursor.value) ?? null : null)
const hiddenReserved = computed(() => selected.value?.state === 'reserved' && !showReservedVersions.value)
const current = computed(() => history.value?.current ?? '')
// The version this page loaded with. The history's current is the server, which
// can already be newer while this page is still the old build.
const pageRuns = computed(() => version.value?.version ?? '')
const runningHere = computed(() => pageRuns.value || current.value)
// live_since belongs to the server version. It does not describe an older page.
const runningSince = computed(() => runningHere.value === current.value ? history.value?.live_since ?? null : null)
const currentKnown = computed(() => byVersion.value.has(current.value))
// The published release before the one the server runs: where a rollback would go.
const rollbackTarget = computed(() => currentKnown.value ? releases.value.find(r => r.state === 'published' && r.version < current.value)?.version ?? null : null)
const filtering = computed(() => !!filter.q.trim() || filter.features || filter.fixes || filter.other)
const compareTo = computed(() => mode.value === 'compare' && cursor.value && cursor.value !== compareFrom.value ? cursor.value : null)
// One notice. An outdated page says a newer version is live; it does not also
// warn that this build's history lacks that version. The server is chosen once:
// the newer of the cached history and the version the update poll already saw.
const server = computed(() => liveServer(current.value, store.available))
const notice = computed(() => releaseNotice(pageRuns.value, server.value, missing.value))
// The title (AEON-488) is the release live on the server, by its codename: the
// newer of what the server and this page know. The eyebrow names the product;
// The content owns the generation and counts; the header stays steady.
const hero = computed(() => { const v = liveServer(server.value, pageRuns.value); return isCalendarVersion(v) ? v : '' })
const heroName = computed(() => byVersion.value.get(hero.value)?.codename)
const eyebrow = computed(() => `${brand.value.wordmark} · RELEASE`)
// One status line under it: since when it runs here, or that this page is older.
const liveAt = computed(() => runningSince.value ? Date.parse(runningSince.value) : NaN)
const liveLine = computed(() => Number.isNaN(liveAt.value) ? 'Live here' : `Live here since ${clockSince(liveAt.value, now.value)} · ${span(Math.max(60_000, now.value - liveAt.value))}`)
const selectedIndex = computed(() => cursor.value ? indexOf.value.get(cursor.value) ?? -1 : -1)
const behindLine = computed(() => {
  const published = releases.value.filter(r => r.state === 'published')
  const behind = published.findIndex(r => r.version === cursor.value) - published.findIndex(r => r.version === current.value)
  const liveName = byVersion.value.get(current.value)?.codename || codenameOf(current.value) || current.value
  return `${Math.max(0, behind)} ${behind === 1 ? 'release' : 'releases'} behind live · back to ${liveName}`
})
const optionId = (v: string) => `release-${v.replace(/\./g, '-')}`

// ---------- Selection ----------
// `releases=all` (the header) stays on the list with nothing selected. `releases=current`
// (the footer, when the running version is not known yet) becomes that release once
// the history arrives. A phone opens the detail only then: a failed or empty history
// stays on the list, where the error or the empty message is.
function initialSelection() {
  if (props.target === 'current') {
    if (!history.value) { showDetail.value = false; return }
    const version = currentKnown.value ? current.value : releases.value[0]?.version ?? ''
    if (!version) { showDetail.value = false; return }
    if (cursor.value === version && byVersion.value.has(version)) return
    missing.value = ''
    mode.value = 'browse'
    cursor.value = version
    if (phone.value) showDetail.value = true
    return
  }
  if (cursor.value && byVersion.value.has(cursor.value)) return
  if (props.target === 'all') { showDetail.value = false; return }
  const wanted = props.target ? props.target.replace(/^v/, '') : ''
  if (wanted && byVersion.value.has(wanted)) { cursor.value = wanted; if (phone.value) showDetail.value = true; return }
  // A version this build does not have cannot open as detail. Stay on the list,
  // where the one notice explains it.
  if (wanted) { missing.value = wanted; showDetail.value = false }
  cursor.value = currentKnown.value ? current.value : releases.value[0]?.version ?? null
}
watch(() => props.target, target => {
  // `current` is the footer before the running version is known. It resolves
  // to that release when the history arrives; it is not a version to echo.
  if (target === 'current') { pendingEcho = ''; initialSelection(); return }
  const wanted = targetVersion(target)
  const echo = !!wanted && wanted === pendingEcho
  // Any other address ends the echo, including one this build cannot show.
  if (!echo) pendingEcho = ''
  if (!wanted || !byVersion.value.has(wanted)) return
  if (echo) { pendingEcho = ''; return }
  if (wanted === cursor.value) return
  mode.value = 'browse'
  cursor.value = wanted
})
watch(history, () => { if (props.target === 'current') initialSelection() })
watch(cursor, async (value, old) => {
  if (!value) return
  if (mode.value === 'browse') publishSelection(value)
  await nextTick()
  document.getElementById(optionId(value))?.scrollIntoView({ block: 'nearest' })
  if (detailScroll.value && old) detailScroll.value.scrollTop = 0
})
// A filter that hides the selection moves it to the first match.
watch(order, list => { if (list.length && cursor.value && !indexOf.value.has(cursor.value) && mode.value === 'browse' && !hiddenReserved.value) cursor.value = list[0].version })

function step(delta: number) {
  const list = order.value
  if (!list.length) return
  const currentVersion = cursor.value
  const at = currentVersion ? indexOf.value.get(currentVersion) ?? -1 : -1
  if (at === -1 && currentVersion) {
    const neighbor = delta > 0 ? list.find(r => r.version < currentVersion) : [...list].reverse().find(r => r.version > currentVersion)
    if (neighbor) cursor.value = neighbor.version
    return
  }
  cursor.value = list[Math.max(0, Math.min(list.length - 1, at === -1 ? 0 : at + delta))].version
}
function choose(v: string) {
  cursor.value = v
  if (phone.value && (mode.value === 'browse' || v !== compareFrom.value)) showDetail.value = true
}
async function open() {
  if (!cursor.value) return
  if (phone.value) showDetail.value = true
  await nextTick()
  detail.value?.focus()
}

// ---------- Compare ----------
watch(eligible, list => {
  if (mode.value !== 'compare') return
  const versions = new Set(list.map(r => r.version))
  if (compareFrom.value && !versions.has(compareFrom.value)) compareFrom.value = list[0]?.version ?? null
  if (cursor.value && !versions.has(cursor.value)) cursor.value = list.find(r => r.version !== compareFrom.value)?.version ?? compareFrom.value
})
function startCompare() {
  // A hidden reservation may be open by URL, but it cannot be a picker endpoint.
  if (!cursor.value || !indexOf.value.has(cursor.value)) cursor.value = order.value[0]?.version ?? null
  if (!cursor.value) return
  mode.value = 'compare'
  compareFrom.value = cursor.value
  help.value = false
  // Suggest the release before as the other end; j/k move it.
  const at = indexOf.value.get(cursor.value) ?? 0
  const next = order.value[at + 1] ?? order.value[at - 1]
  if (next) cursor.value = next.version
  showDetail.value = false
  listbox.value?.focus({ preventScroll: true })
}
function exitCompare() {
  mode.value = 'browse'
  compareFrom.value = null
  if (cursor.value) publishSelection(cursor.value)
  listbox.value?.focus({ preventScroll: true })
}
function swap() { if (compareTo.value && compareFrom.value) { const a = compareFrom.value; compareFrom.value = compareTo.value; cursor.value = a } }

// ---------- Search ----------
async function focusSearch() { showDetail.value = false; help.value = false; await nextTick(); searchInput.value?.focus(); searchInput.value?.select() }
function searchKeys(event: KeyboardEvent) {
  if (event.key === 'Escape') {
    event.preventDefault(); event.stopPropagation()
    searchInput.value?.blur()
    listbox.value?.focus({ preventScroll: true })
  } else if (event.key === 'Enter' || event.key === 'ArrowDown') {
    event.preventDefault()
    if (order.value[0] && (!cursor.value || !indexOf.value.has(cursor.value))) cursor.value = order.value[0].version
    listbox.value?.focus({ preventScroll: true })
  }
}
function clearFilters() { Object.assign(filter, { q: '', features: false, fixes: false, other: false }) }
function marked(text: string) {
  const q = filter.q.trim().toLowerCase()
  if (!q) return [{ text, hit: false }]
  const i = text.toLowerCase().indexOf(q)
  return i === -1 ? [{ text, hit: false }] : [{ text: text.slice(0, i), hit: false }, { text: text.slice(i, i + q.length), hit: true }, { text: text.slice(i + q.length), hit: false }]
}

// The compare pins the live source commit and main head. A newer person or
// live release invalidates both the count and every in-flight page.
const waiting = ref<PendingChanges | null>(null)
const waitingError = ref('')
const waitingLoading = ref(false)
const showWaiting = ref(false)
const waitingClose = ref<HTMLButtonElement>()
let waitingOpener: HTMLElement | null = null
async function openWaiting(event: MouseEvent) { waitingOpener = event.currentTarget as HTMLElement; showWaiting.value = true; await nextTick(); waitingClose.value?.focus() }
function closeWaiting() { showWaiting.value = false; void nextTick(() => { waitingOpener?.focus({ preventScroll: true }); waitingOpener = null }) }
function waitingKeys(event: KeyboardEvent) {
  event.stopPropagation()
  if (event.key === 'Escape') keydown(event)
}
const waitingLabel = computed(() => pendingLine(waiting.value, waitingLoading.value, waitingError.value))
let waitingRequest: AbortController | undefined
let waitingEpoch = 0
async function loadWaiting(more = false) {
  if (waitingLoading.value || !current.value || !session.identity || permissionsRevoked()) return
  if (more && (!waiting.value?.next_cursor || waiting.value.changes.length >= 250)) return
  const focused = document.activeElement instanceof HTMLElement && document.activeElement.closest('.pending-controls') ? document.activeElement : null
  const before = waiting.value
  const cursor = more ? before?.next_cursor ?? undefined : undefined
  waitingRequest?.abort()
  const controller = waitingRequest = new AbortController()
  const epoch = ++waitingEpoch
  const live = current.value
  waitingLoading.value = true
  waitingError.value = ''
  try {
    const answer = await getPendingChanges(cursor, controller.signal)
    if (epoch !== waitingEpoch || current.value !== live) return
    if (answer.live_version !== live || (more && (answer.head_commit !== before?.head_commit || answer.base_commit !== before.base_commit))) throw new Error('The live release or main branch changed. Refresh waiting changes.')
    waiting.value = more && before ? appendPendingChanges(before, answer) : answer
  } catch (error) {
    if (epoch === waitingEpoch && !controller.signal.aborted) waitingError.value = error instanceof Error ? error.message : 'Waiting changes could not be loaded.'
  } finally {
    if (epoch === waitingEpoch) {
      waitingLoading.value = false
      await nextTick()
      // The final page removes Show more; keep keyboard focus inside the panel.
      if (epoch === waitingEpoch && showWaiting.value && focused && !focused.isConnected && document.activeElement === document.body) waitingClose.value?.focus({ preventScroll: true })
    }
  }
}
function resetWaiting(keepOpen = false) {
  waitingRequest?.abort(); waitingEpoch++
  waiting.value = null; waitingError.value = ''; waitingLoading.value = false
  if (!keepOpen) showWaiting.value = false
  if (current.value && session.identity) void loadWaiting()
}
watch(() => [current.value, session.identity?.tenant.id, session.identity?.principal.id], () => resetWaiting(), { immediate: true })
const stopWaitingAccess = onAccessChange(change => {
  resetWaiting(change === 'refresh')
  if (change === 'reset') peekKey.value = null
})

// ---------- A ticket beside the history ----------
const peekKey = ref<string | null>(null)
const peekPanel = ref<InstanceType<typeof ReleaseTicketPanel>>()
const peekPane = ref<HTMLElement>()
let peekOpener: HTMLElement | null = null
async function openPeek(key: string, from?: HTMLElement | null) {
  peekOpener = from ?? null
  help.value = false
  peekKey.value = normalKey(key)
  await nextTick()
  void peekPanel.value?.focus()
}
// Focus goes back to the key that opened the panel (or the list when it is gone).
function closePeek() {
  peekKey.value = null
  void nextTick(() => {
    if (peekOpener?.isConnected && peekOpener.getClientRects().length) peekOpener.focus({ preventScroll: true })
    else listbox.value?.focus({ preventScroll: true })
    peekOpener = null
  })
}
provide(TICKET_PEEK, { open: (key, from) => void openPeek(key, from), openKey: peekKey })
// On phones the ticket is a full-screen sheet: the history under it is inert.
const covered = computed(() => phone.value && (!!peekKey.value || showWaiting.value))
const inPeek = (target: EventTarget | null) => target instanceof Node && !!peekPane.value?.contains(target)
// The side panel's own keys, as beside a project list.
function peekKeys(event: KeyboardEvent) {
  const panel = peekPanel.value
  if (!panel) return
  const run: Record<string, () => void> = { e: panel.startEdit, s: panel.openStatus, p: panel.openPriority, a: panel.openAssignee, r: panel.openLink, c: panel.focusComposer }
  const action = run[event.key]
  if (action) { event.preventDefault(); action() }
}

// ---------- Keys ----------
const typing = (target: EventTarget | null) => target instanceof HTMLElement && (target.tagName === 'INPUT' || target.tagName === 'TEXTAREA' || target.isContentEditable)
function keydown(event: KeyboardEvent) {
  if (event.key === 'Escape') {
    event.preventDefault()
    if (typing(event.target)) { (event.target as HTMLElement).blur(); return }
    if (help.value) help.value = false
    else if (peekKey.value) void peekPanel.value?.requestClose()
    else if (showWaiting.value) closeWaiting()
    else if (mode.value === 'compare') exitCompare()
    else if (showDetail.value && phone.value) { showDetail.value = false; void nextTick(() => listbox.value?.focus({ preventScroll: true })) }
    else emit('close')
    return
  }
  if (!keysReady.value) return
  if (typing(event.target) || event.metaKey || event.ctrlKey || event.altKey) return
  if (inPeek(event.target)) { peekKeys(event); return }
  if (event.target instanceof Element && event.target.closest('.switches') && ['ArrowDown', 'ArrowUp', 'ArrowLeft', 'ArrowRight', 'Home', 'End'].includes(event.key)) return
  const onControl = event.target instanceof HTMLElement && !!event.target.closest('button, a, summary') && event.target !== listbox.value
  switch (event.key) {
    case 'j': case 'ArrowDown': event.preventDefault(); step(1); break
    case 'k': case 'ArrowUp': event.preventDefault(); step(-1); break
    case 'Home': event.preventDefault(); if (order.value[0]) cursor.value = order.value[0].version; break
    case 'End': event.preventDefault(); if (order.value.length) cursor.value = order.value[order.value.length - 1].version; break
    case 'Enter': case ' ':
      if (onControl) return
      event.preventDefault(); void open(); break
    case '/': event.preventDefault(); void focusSearch(); break
    case 'c': case 'C': event.preventDefault(); if (mode.value === 'compare') exitCompare(); else startCompare(); break
    case 'e': case 'E': if (mode.value === 'browse' && selected.value?.tag) { event.preventDefault(); evidence.value = !evidence.value } break
    case '?': event.preventDefault(); help.value = !help.value; break
    default: return
  }
}

// ---------- Lifecycle ----------
let opener: HTMLElement | null = null
let clock: ReturnType<typeof setInterval> | undefined
onMounted(async () => {
  opener = document.activeElement as HTMLElement | null
  setOverlayTitle('Releases')
  phoneQuery.addEventListener('change', onPhone)
  scrollStatsQuery.addEventListener('change', onScrollStats)
  clock = setInterval(() => { now.value = Date.now() }, 30_000)
  dialog.value?.showModal()
  dialog.value?.focus()
  // Always refetch: the server may run a newer build than when the page loaded.
  // Keys wait until that history is the one on screen, so a press during the
  // refetch cannot move a row the replacement then drops.
  await store.load(true)
  await store.markSeen()
  initialSelection()
  keysReady.value = true
  await nextTick()
  if (document.activeElement === dialog.value || !dialog.value?.contains(document.activeElement)) listbox.value?.focus({ preventScroll: true })
})
onBeforeUnmount(() => {
  phoneQuery.removeEventListener('change', onPhone)
  scrollStatsQuery.removeEventListener('change', onScrollStats)
  clearInterval(clock)
  waitingRequest?.abort(); waitingEpoch++; stopWaitingAccess()
  setOverlayTitle('')
  // Leave the top layer first: the page is inert while the modal is open.
  if (dialog.value?.open) dialog.value.close()
  if (opener?.isConnected) opener.focus({ preventScroll: true })
})

const timeOf = (r: Release) => { const at = releasedAt(r); return at ? new Date(at).toLocaleTimeString('en-GB', { hour: '2-digit', minute: '2-digit' }) : '—' }
const ageOf = (r: Release) => { const at = releasedAt(r); return at ? relativeTime(at, { now: now.value }) : '' }
// Per-row counts follow the detail's blocks (features and fixes count tickets, other
// counts commits), worked out once per history rather than on every render.
const summaries = computed(() => new Map(releases.value.map(r => {
  const g = presentRelease(r, locale.value)
  return [r.version, { features: g.features.length, fixes: g.fixes.length, other: g.other.length, tickets: ticketsOf(r) }]
})))
const countsOf = (r: Release) => summaries.value.get(r.version) ?? { features: 0, fixes: 0, other: 0, tickets: [] as string[] }
function reload() { window.location.reload() }
// Change counts in a row: one glyph per kind, named in full for tooltips and screen readers.
const plural = (n: number, one: string, many: string) => `${n} ${n === 1 ? one : many}`
const KINDS = [
  { key: 'features', icon: 'sparkle', label: (n: number) => plural(n, 'feature', 'features') },
  { key: 'fixes', icon: 'bug', label: (n: number) => plural(n, 'fix', 'fixes') },
  { key: 'other', icon: 'gear', label: (n: number) => plural(n, 'other change', 'other changes') },
] as const
</script>

<template>
  <dialog ref="dialog" class="releases" :aria-label="`${brand.wordmark} releases`" tabindex="-1" @cancel.prevent @keydown="keydown">
    <div class="shell" :class="{ 'show-detail': showDetail, compare: mode === 'compare', peeking: !!peekKey }">
      <header class="head" :inert="covered">
        <div class="title-row">
          <!-- The mark leaves the release history for the home page, like the app header's mark. -->
          <a class="mark-backing" href="/" :aria-label="`${brand.wordmark} home`" data-tip="Home" @click.prevent="emit('home')"><img :src="mark" width="26" height="26" alt="" /></a>
          <div class="titles">
            <p id="release-header-line" class="eyebrow">{{ eyebrow }}</p>
            <h1 id="releases-title" class="hero" :class="{ named: !!hero }">
              <ReleaseCodename v-if="hero" :version="hero" :name="heroName" plain />
              <template v-else>{{ brand.wordmark }} releases</template>
            </h1>
            <div v-if="hero" class="status-dock">
              <p v-if="notice === 'update'" class="status-line outdated" role="status">
                <span class="status-dot" aria-hidden="true" />
                <span>Live on the server · this page still runs <ReleaseName v-if="pageRuns" :version="pageRuns" :name="byVersion.get(pageRuns)?.codename" class="status-version" /></span>
                <button type="button" class="reload" @click="reload"><AppIcon name="refresh" :size="13" />Reload</button>
              </p>
              <p v-else class="status-line" :data-tip="runningSince ? `Live on this server since ${absoluteTime(runningSince)}` : undefined">
                <span class="status-dot live" aria-hidden="true" /><span>{{ liveLine }}</span>
              </p>
              <ReleaseVersionCopy :value="hero" class="dock-version" />
            </div>
          </div>
          <span class="spacer" />
          <div class="switches">
            <div class="seg" role="radiogroup" aria-label="Language" @keydown="switchKeys($event, lang, LANGS, value => chooseLang(value as ReleaseLang))">
              <button type="button" role="radio" :aria-checked="lang === 'en'" :tabindex="lang === 'en' ? 0 : -1" @click="chooseLang('en')">EN</button>
              <button type="button" role="radio" :aria-checked="lang === 'de'" :tabindex="lang === 'de' ? 0 : -1" @click="chooseLang('de')">DE</button>
            </div>
            <div class="seg" role="radiogroup" aria-label="View" @keydown="switchKeys($event, view, VIEWS, value => chooseView(value as ReleaseView))">
              <button type="button" role="radio" :aria-checked="view === 'highlights'" :tabindex="view === 'highlights' ? 0 : -1" @click="chooseView('highlights')">Highlights</button>
              <button type="button" role="radio" :aria-checked="view === 'details'" :tabindex="view === 'details' ? 0 : -1" @click="chooseView('details')">Details</button>
            </div>
          </div>
          <label class="search-field search">
            <AppIcon name="search" :size="14" />
            <input ref="searchInput" v-model="filter.q" class="field" type="search" maxlength="240" placeholder="Search names, changes, tickets" aria-label="Search releases" aria-keyshortcuts="/" @keydown="searchKeys" />
            <kbd v-if="!filter.q" class="keycap slash" aria-hidden="true">/</kbd>
          </label>
          <button type="button" class="btn compare-btn" aria-label="Compare" :aria-pressed="mode === 'compare'" aria-keyshortcuts="c" :disabled="mode !== 'compare' && !visible.length" @click="mode === 'compare' ? exitCompare() : startCompare()">
            <AppIcon name="compare" :size="14" /><span class="btn-text">Compare</span><kbd class="keycap" aria-hidden="true">C</kbd>
          </button>
          <button type="button" class="icon-btn flat help-btn" aria-label="Keyboard shortcuts" aria-keyshortcuts="?" :aria-expanded="help" @click="help = !help"><AppIcon name="keyboard" :size="15" /></button>
          <button type="button" class="icon-btn close-btn" aria-label="Close release history" aria-keyshortcuts="Escape" @click="emit('close')"><AppIcon name="close" :size="15" /></button>
        </div>
        <p v-if="notice === 'missing'" class="notice" role="status">
          <AppIcon name="info" :size="14" />
          <span><CalendarVersion :value="missing" class="notice-version" /> is not in this build’s release history.</span>
          <button type="button" class="icon-btn flat sm" aria-label="Dismiss" @click="missing = ''"><AppIcon name="close" :size="12" /></button>
        </p>
        <ReleaseStats v-if="releases.length && !scrollStats" class="stats" :releases="releases" :now="now" />
        <div v-else-if="!scrollStats && !history && !store.error" class="stats-placeholder skeleton-body" aria-hidden="true"><span v-for="i in 2" :key="i" class="skeleton" /></div>
      </header>

      <div class="body">
        <section class="list-pane" aria-label="Releases" :inert="covered">
          <!-- Stacked cards scroll away with the list, inside its gutter. -->
          <ReleaseStats v-if="releases.length && scrollStats" compact class="stats" :releases="releases" :now="now" />
          <div class="filters">
            <span class="filter-label">Show</span>
            <div class="toggles" role="group" aria-label="Show only releases with">
              <button type="button" class="toggle" :aria-pressed="filter.features" @click="filter.features = !filter.features"><AppIcon name="sparkle" :size="13" />Features</button>
              <button type="button" class="toggle" :aria-pressed="filter.fixes" @click="filter.fixes = !filter.fixes"><AppIcon name="bug" :size="13" />Fixes</button>
              <button type="button" class="toggle" :aria-pressed="filter.other" @click="filter.other = !filter.other"><AppIcon name="gear" :size="12" />Other</button>
            </div>
            <p class="sr" aria-live="polite"><template v-if="filtering">{{ visible.length }} matching releases</template></p>
          </div>

          <p v-if="mode === 'compare'" class="compare-hint" role="status">
            <span :lang="lang">{{ copyText.comparingFrom }}</span><ReleaseName v-if="compareFrom" :version="compareFrom" :name="byVersion.get(compareFrom)?.codename" class="hint-version" />
            <span v-if="phone" :lang="lang">{{ copyText.compareTap }}</span>
            <span v-else :lang="lang">{{ copyText.compareOther[0] }} <kbd class="keycap">j</kbd> <kbd class="keycap">k</kbd> {{ copyText.compareOther[1] }}</span>
          </p>

          <div v-if="!history && store.loading" class="loading" role="status" aria-label="Loading the release history">
            <div v-for="i in 6" :key="i" class="row-skeleton"><span class="skeleton" /><span class="skeleton wide" /></div>
          </div>
          <div v-else-if="!history && store.error" class="empty" role="alert">
            <h2>The release history could not be loaded.</h2>
            <p>{{ store.error }}</p>
            <button type="button" class="btn sm" @click="store.load(true).then(initialSelection)"><AppIcon name="refresh" :size="12" />Try again</button>
          </div>
          <div v-else-if="history && !releases.length" class="empty" :lang="lang">
            <span class="empty-icon"><AppIcon name="history" :size="20" /></span>
            <h2>{{ copyText.noHistory }}</h2>
            <p>{{ copyText.noHistoryText(current === 'dev') }}</p>
          </div>
          <p v-else-if="history && matching.length && !visible.length" class="empty hidden-history">Hidden in the history. Show reserved versions in Developer settings.</p>
          <div v-else-if="history && !visible.length" class="empty" :lang="lang">
            <h2>{{ copyText.noMatch }}</h2>
            <p>{{ copyText.noMatchText(releases.length, filter.q.trim()) }}</p>
            <button type="button" class="btn sm" @click="clearFilters">{{ copyText.clear }}</button>
          </div>

          <div
            v-show="visible.length" ref="listbox" class="listbox" role="grid" tabindex="0" aria-label="Releases, newest first"
            :aria-activedescendant="cursor && indexOf.has(cursor) ? optionId(cursor) : undefined"
          >
            <div v-for="day in days" :key="day.key" class="day" role="rowgroup" :aria-labelledby="`day-${day.key}`">
              <p :id="`day-${day.key}`" class="day-h"><span>{{ day.label }}</span><span class="day-count">{{ day.releases.length }}</span></p>
              <div
                v-for="r in day.releases" :id="optionId(r.version)" :key="r.version" role="row" class="row"
                :aria-selected="cursor === r.version"
                :class="{ reserved: r.state === 'reserved', current: r.version === current, fresh: store.highlight.has(r.version), from: mode === 'compare' && r.version === compareFrom, to: r.version === compareTo }"
                :style="{ '--i': Math.min(indexOf.get(r.version) ?? 0, 16) }"
                @click="choose(r.version)"
              >
                <span class="time" role="gridcell">
                  <span class="mono clock">{{ timeOf(r) }}</span>
                  <span class="age">{{ ageOf(r) }}</span>
                  <span v-if="r.version === current" class="time-badge current-tag">Live</span>
                  <span v-if="r.version === rollbackTarget" class="time-badge rollback-tag">Rollback</span>
                  <span v-if="store.highlight.has(r.version)" class="time-badge new-tag">New</span>
                  <span v-if="mode === 'compare' && r.version === compareFrom" class="time-badge end-tag">From</span>
                  <span v-if="r.version === compareTo" class="time-badge end-tag">To</span>
                </span>
                <span class="main" role="gridcell">
                  <span class="line1">
                    <ReleaseName plain :version="r.version" :name="r.codename" class="row-name" :title="r.codename || codenameOf(r.version) || r.version" :data-tip="r.codename || codenameOf(r.version) || r.version"><template v-for="(p, i) in marked(r.codename || codenameOf(r.version) || r.version)" :key="i"><mark v-if="p.hit">{{ p.text }}</mark><template v-else>{{ p.text }}</template></template></ReleaseName>
                    <span class="counts">
                      <span v-for="k in KINDS" :key="k.key" class="count" role="img" :aria-label="k.label(countsOf(r)[k.key])" :data-tip="k.label(countsOf(r)[k.key])"><AppIcon :name="k.icon" :size="11" />{{ countsOf(r)[k.key] }}</span>
                    </span>
                  </span>
                  <span v-if="r.state === 'reserved'" class="headline">Reserved, never published</span>
                  <template v-else>
                    <span v-if="rails.get(r.version)?.text" class="rail-name"><span class="summary-size" aria-hidden="true">{{ railLine(r, lang === 'en' ? 'de' : 'en').text }}</span><span class="headline" :class="{ theme: rails.get(r.version)!.themed }" :lang="rails.get(r.version)!.lang"><template v-for="(p, i) in marked(rails.get(r.version)!.text)" :key="i"><mark v-if="p.hit">{{ p.text }}</mark><template v-else>{{ p.text }}</template></template></span><LangBadge v-if="rails.get(r.version)!.lang !== lang" :lang="rails.get(r.version)!.lang" /></span>

                  </template>
                  <span class="row-foot"><ReleaseVersionCopy :value="r.version" class="row-version" @click.stop /></span>
                </span>
                <span role="gridcell" class="row-chev"><AppIcon name="chevron-right" :size="14" /></span>
              </div>
            </div>
          </div>
        </section>

        <section class="detail-pane" :aria-label="mode === 'compare' ? 'Comparison' : 'Release'">
          <div class="detail-actions" :inert="covered">
            <div class="detail-left">
              <button v-if="selected?.version === current" type="button" class="next-up" :data-tip="waitingLabel" :aria-label="waitingLabel" @click="openWaiting"><span>{{ waitingLabel }}</span></button>
              <button v-if="selected && selected.version !== current && currentKnown" type="button" class="to-current" :data-tip="behindLine" @click="choose(current)"><AppIcon name="arrow-left" :size="11" /><span>{{ behindLine }}</span></button>
            </div>
            <div class="pager">
              <button type="button" class="icon-btn flat" aria-label="Previous release" :disabled="selectedIndex <= 0" @click="step(-1)"><AppIcon name="chevron-left" :size="14" /></button>
              <span class="detail-action-state">{{ selectedIndex < 0 ? '—' : selectedIndex + 1 }} of {{ order.length }}</span>
              <button type="button" class="icon-btn flat" aria-label="Next release" :disabled="selectedIndex < 0 || selectedIndex >= order.length - 1" @click="step(1)"><AppIcon name="chevron-right" :size="14" /></button>
            </div>
            <button type="button" class="evidence-control" :aria-pressed="evidence || view === 'details'" :disabled="!selected || (!selected.tag && selected.state !== 'candidate')" @click="evidence = !evidence"><AppIcon name="shield" :size="13" />Evidence<kbd class="keycap">E</kbd></button>
          </div>
          <div ref="detailScroll" class="detail-scroll" :inert="covered">
            <ReleaseCompare
              v-if="mode === 'compare' && compareFrom" key="compare" :releases="eligible" :from="compareFrom" :to="compareTo"
              :repository="history?.repository ?? ''" :query="filter.q" :lang="lang" :view="view" @swap="swap" @exit="exitCompare"
            />
            <ReleaseDetail
              v-else-if="selected" ref="detail" :key="selected.version" :release="selected" :repository="history?.repository ?? ''"
              :current="selected.version === current" :rollback="selected.version === rollbackTarget" :fresh="store.highlight.has(selected.version)"
              :hidden-in-history="hiddenReserved" :published-count="releases.filter(r => r.state === 'published').length"
              :live-since="history?.live_since ?? null" :now="now" :query="filter.q" :evidence="evidence" :lang="lang" :view="view"
            />
            <div v-else-if="store.loading" class="detail-loading skeleton-body" role="status" aria-label="Loading release"><span class="skeleton" /><span class="skeleton" /><span class="skeleton" /></div>
          </div>
          <section v-if="showWaiting" class="pending-pane" aria-label="Changes waiting for the next release" :inert="phone && !!peekKey" @keydown="waitingKeys">
            <header class="pending-head"><h2>Next release</h2><button type="button" ref="waitingClose" class="icon-btn flat" aria-label="Close waiting changes" @click="closeWaiting"><AppIcon name="close" :size="14" /></button></header>
            <div class="pending-controls">
              <button type="button" class="btn sm" :aria-disabled="waitingLoading" @click="loadWaiting()"><AppIcon name="refresh" :size="12" />Refresh</button>
              <button v-if="waiting?.next_cursor" type="button" class="btn sm" :aria-disabled="waitingLoading || waiting.changes.length >= 250" @click="loadWaiting(true)">Show more</button>
            </div>
            <div class="pending-scroll">
              <p class="pending-status" role="status">{{ waitingLabel }}</p>
              <p v-if="waitingError" role="alert">{{ waitingError }}</p>
              <p v-for="reason in waiting?.unavailable ?? []" :key="reason">{{ reason }}</p>
              <ul class="pending-list">
                <li v-for="change in waiting?.changes ?? []" :key="change.commit"><p>{{ plainSubject(change.subject, change.tickets) }}</p><div class="pending-meta"><TicketLink v-for="key in change.tickets" :key="key" :ticket-key="key" variant="inline" /><span class="mono">{{ shortCommit(change.commit) }}</span></div></li>
              </ul>
            </div>
          </section>
          <div v-if="peekKey" ref="peekPane" class="peek-pane">
            <ReleaseTicketPanel ref="peekPanel" :ticket-key="peekKey" :now="now" open-in-project @close="closePeek" @navigate="path => emit('navigate', path)" />
          </div>
        </section>
      </div>

      <footer v-if="phone" class="mobile-footer" :inert="covered">
        <button type="button" :aria-pressed="!showDetail" @click="showDetail = false"><AppIcon name="chevron-left" :size="13" />All releases</button>
        <button type="button" :aria-pressed="showDetail" :disabled="!selected" @click="showDetail = true">Release notes<AppIcon name="chevron-right" :size="13" /></button>
      </footer>

      <div v-if="help" class="help-scrim" @click.self="help = false">
        <section class="help" role="dialog" aria-labelledby="release-keys-title">
          <header><h2 id="release-keys-title">Release history keys</h2><button type="button" class="icon-btn flat sm" aria-label="Close the keys" @click="help = false"><AppIcon name="close" :size="13" /></button></header>
          <dl>
            <div><dt><kbd class="keycap">j</kbd><kbd class="keycap">k</kbd></dt><dd>Next and previous release</dd></div>
            <div><dt><kbd class="keycap">Enter</kbd></dt><dd>Open the release</dd></div>
            <div><dt><kbd class="keycap">/</kbd></dt><dd>Search names, changes and ticket keys</dd></div>
            <div><dt><kbd class="keycap">c</kbd></dt><dd>Compare two releases, then pick the other end with j and k</dd></div>
            <div><dt><kbd class="keycap">e</kbd></dt><dd>Show or hide the evidence</dd></div>
            <div><dt><kbd class="keycap">?</kbd></dt><dd>These keys</dd></div>
            <div><dt><kbd class="keycap">Esc</kbd></dt><dd>Step back, then close</dd></div>
          </dl>
        </section>
      </div>
    </div>
  </dialog>
</template>

<style scoped>
.releases { width: 100vw; height: 100dvh; max-width: none; max-height: none; margin: 0; padding: 0; border: 0; color: var(--ink); outline: none; overflow: hidden; background-color: var(--canvas); --aurora-1: color-mix(in srgb, var(--primary-tint) 55%, transparent); --aurora-2: color-mix(in srgb, var(--primary-line) 22%, transparent); --aurora-3: color-mix(in srgb, var(--secondary-tint) 32%, transparent); background-image: radial-gradient(ellipse 520px 230px at 25% 0%,var(--aurora-1),transparent),radial-gradient(ellipse 420px 190px at 45% 0%,var(--aurora-2),transparent),radial-gradient(ellipse 350px 200px at 60% 0%,var(--aurora-3),transparent); }
.releases[open] { display: block; }
.releases::backdrop { background: var(--scrim); }
:root[data-theme="dark"] .releases { --aurora-1: color-mix(in srgb, var(--primary-tint) 20%, transparent); --aurora-2: color-mix(in srgb, var(--primary-tint) 8%, transparent); --aurora-3: color-mix(in srgb, var(--secondary-tint) 12%, transparent); }
@media(prefers-color-scheme:dark) { :root:not([data-theme="light"]) .releases { --aurora-1: color-mix(in srgb, var(--primary-tint) 20%, transparent); --aurora-2: color-mix(in srgb, var(--primary-tint) 8%, transparent); --aurora-3: color-mix(in srgb, var(--secondary-tint) 12%, transparent); } }
/* Toolbar labels may wrap; their intrinsic width must never enlarge the sheet. */
.shell { position:relative; display:grid; grid-template-columns:minmax(0,1fr); grid-template-rows:auto minmax(0,1fr); height:100%; max-width:1640px; margin:0 auto; padding:0 var(--gutter); }
.head { display:grid; min-width:0; gap:18px; padding:18px 0 20px; }
.title-row { display:flex; flex-wrap:wrap; align-items:flex-start; gap:12px; min-width:0; }
.mark-backing { display:grid; place-items:center; flex:none; width:52px; height:52px; border-radius:15px; background:var(--glass); box-shadow:0 0 0 1px var(--glass-rim),0 6px 16px -10px var(--line-2); }
.titles { flex:0 0 auto; width:max-content; max-width:100%; display:flex; flex-direction:column; align-items:center; gap:12px; min-width:0; padding:4px 0; }
.titles .eyebrow { display:flex; align-items:center; gap:12px; width:100%; margin:0; font:600 10.5px/1.4 var(--mono); letter-spacing:.18em; white-space:nowrap; text-align:center; }
.titles .eyebrow::before,.titles .eyebrow::after { content:''; flex:1; min-width:0; height:1px; background:var(--line-2); }
.hero { max-width:100%; margin:0; font:300 clamp(28px,3.4vw,48px)/1.1 var(--serif); text-align:center; overflow-wrap:anywhere; }
.status-dock { display:flex; flex-wrap:wrap; align-items:center; justify-content:center; gap:4px 14px; min-width:0; max-width:100%; min-height:44px; padding:6px 16px; border-radius:24px; background:linear-gradient(135deg,var(--glass),var(--glass-2)); box-shadow:inset 0 1px 0 var(--glass-edge),0 0 0 1px var(--glass-rim); }
.dock-version { padding-left:14px; border-left:1px solid var(--line-2); }
.status-line { display:flex; align-items:center; gap:8px; min-width:0; font-size:12px; line-height:1.45; color:var(--ink-2); }
.status-line > span:not(.status-dot) { min-width:0; overflow-wrap:anywhere; }
@media(min-width:600px) { .status-dock:not(:has(.outdated)) { flex-wrap:nowrap; } .status-line:not(.outdated),.status-dock:not(:has(.outdated)) .dock-version { flex:none; white-space:nowrap; } }
.status-dot { flex:none; width:7px; height:7px; border-radius:50%; background:var(--gold); }
.status-dot.live { background:var(--ok); }
.reload { display:inline-flex; align-items:center; gap:6px; min-height:30px; padding:0 12px; border-radius:999px; color:var(--teal-ink); background:var(--surface); }
.spacer { display:none; }
.switches { display:flex; flex-wrap:wrap; flex:none; align-items:center; gap:8px; min-width:0; }
.switches .seg button { height:36px; padding:0 12px; }
.search { flex:1 1 220px; min-width:140px; }
.search .field { height:36px; border-radius:999px; padding-right:34px; font-size:12px; }
.search .field::-webkit-search-cancel-button { display:none; }
.slash { position:absolute; right:10px; }
.compare-btn { height:36px; }
.icon-btn { flex:none; width:36px; height:36px; }
.notice { display:flex; align-items:center; gap:10px; padding:8px 14px; background:var(--glass); border-radius:12px; color:var(--ink); font-size:13px; }
.notice > span { flex:1; min-width:0; }
.stats-placeholder { display:grid; grid-template-columns:minmax(0,360px) minmax(0,1fr); gap:14px; height:300px; }
.stats-placeholder .skeleton { border-radius:20px; }
.body { display:grid; grid-template-columns:minmax(300px,410px) minmax(0,1fr); gap:28px; min-height:0; padding-bottom:8px; }
.list-pane { display:flex; flex-direction:column; gap:10px; min-width:0; min-height:0; overflow:auto; padding:2px 1px; scrollbar-gutter:stable; overscroll-behavior:contain; }
.filters { flex:none; position:sticky; top:0; z-index:2; display:flex; flex-wrap:wrap; align-items:center; gap:8px; min-width:0; min-height:36px; padding-bottom:2px; background:var(--canvas); }
.filter-label { font:600 10px/1 var(--mono); letter-spacing:.14em; text-transform:uppercase; color:var(--ink-3); }
.toggles { display:flex; flex-wrap:wrap; gap:6px; min-width:0; }
.toggle { display:inline-flex; align-items:center; gap:6px; height:30px; padding:0 11px; border:1px solid var(--glass-edge); border-radius:999px; background:var(--btn-bg); box-shadow:var(--shadow-btn); color:var(--ink-2); font-size:12px; font-weight:600; }
.toggle[aria-pressed="true"] { background:var(--seg-on); color:var(--teal-ink); box-shadow:0 0 0 1px var(--chip-teal-line); }
.compare-hint { font-size:12px; padding:8px; }
.listbox { display:grid; gap:0; padding:4px 6px; border-radius:16px; outline:none; background:var(--surface-raised); box-shadow:0 0 0 1px var(--line),0 10px 26px -20px var(--line-2); }
.day { display:grid; }
.day-h { display:flex; align-items:baseline; gap:8px; padding:12px 10px 4px; font:600 10px/1.5 var(--mono); letter-spacing:.16em; text-transform:uppercase; color:var(--ink-3); }
.day:first-child .day-h { padding-top:8px; }
.day-count { letter-spacing:0; font-weight:500; }
.row { position:relative; display:grid; grid-template-columns:60px minmax(0,1fr) 12px; align-items:start; gap:10px; padding:12px 10px 13px; border-top:1px solid var(--line); cursor:pointer; outline-offset:-1px; }
.day-h + .row { border-top:0; }
.row.fresh { background:var(--secondary-tint-3); }
.row[aria-selected="true"],.row.from { background:var(--row-selected); box-shadow:inset 0 0 0 1px color-mix(in srgb,var(--teal) 55%,transparent); border-radius:10px; border-top-color:transparent; }
.row[aria-selected="true"] + .row { border-top-color:transparent; }
.row.reserved .clock,.row.reserved .row-name { color:var(--ink-2); }
.time { display:grid; gap:2px; justify-items:start; }
.clock { font-size:12px; font-weight:600; color:var(--ink); }
.age { font-size:10px; color:var(--ink); white-space:nowrap; }
.time-badge.rollback-tag { color:var(--ink); }
.time-badge { display:block; margin-top:6px; padding:4px 0; width:100%; text-align:center; font:650 10px/1.2 var(--mono); letter-spacing:.07em; text-transform:uppercase; color:var(--ink-2); background:var(--surface-2); border-radius:7px; }
.current-tag { color:var(--teal-ink); background:var(--row-selected); box-shadow:inset 0 0 0 1px var(--chip-teal-line); }
.new-tag { color:color-mix(in oklab,var(--secondary-ink),var(--ink) 35%); background:var(--secondary-tint-3); }
.main { display:grid; gap:3px; min-width:0; }
.line1 { display:grid; grid-template-columns:minmax(0,1fr) max-content; align-items:start; gap:10px; min-width:0; }
.row-name { font-size:14.5px; line-height:20px; font-weight:600; color:var(--ink); }
.row-name :deep(.rn-name) { display:-webkit-box; -webkit-line-clamp:2; -webkit-box-orient:vertical; overflow:hidden; white-space:normal; overflow-wrap:anywhere; }
.counts { display:flex; flex:none; align-items:center; gap:9px; margin-top:3px; font-size:10px; color:var(--ink-3); }
.count { display:inline-flex; align-items:center; gap:4px; font-variant-numeric:tabular-nums; }
.rail-name { display:grid; margin-top:3px; min-width:0; }
.rail-name .headline,.rail-name .summary-size { grid-area:1/1; }
.headline,.summary-size { color:var(--ink-2); font-size:12.5px; line-height:1.4; overflow:hidden; display:-webkit-box; -webkit-line-clamp:2; -webkit-box-orient:vertical; overflow-wrap:anywhere; }
.summary-size { visibility:hidden; pointer-events:none; }
.rail-name > .lang-badge { justify-self:end; }
.row-foot { display:flex; justify-content:flex-end; min-width:0; margin-top:4px; }
.row-version { font-size:11.5px; color:var(--ink-3); }
.row-version :deep(.version-copy) { gap:4px; padding:0; margin:0; }
/* Compact mouse rows; touch keeps the shared 44px button inside its row. */
@media (pointer:fine) { .row-version :deep(.version-copy) { min-height:24px; } }
.row-version :deep(.version-pretty) { font-size:11.5px; }
.row-chev { align-self:center; opacity:.55; color:var(--ink-3); }
.row[aria-selected="true"] .row-chev { opacity:1; color:var(--teal-ink); }
.detail-pane { position:relative; display:grid; grid-template-rows:36px minmax(0,1fr); gap:10px; min-width:0; min-height:0; }
.detail-actions { display:grid; grid-template-columns:minmax(0,1fr) auto minmax(0,1fr); align-items:center; gap:10px; min-width:0; }
.to-current,.next-up,.evidence-control,.mobile-footer button { border:0; background:transparent; font-family:var(--font); cursor:pointer; }
.detail-left { justify-self:stretch; display:flex; align-items:center; min-width:0; max-width:100%; }
.to-current,.next-up { display:inline-flex; align-items:center; gap:6px; min-height:30px; padding:0 10px; border-radius:999px; font-size:12px; font-weight:600; color:var(--teal-ink); max-width:100%; }
.to-current > span,.next-up > span { overflow:hidden; text-overflow:ellipsis; white-space:nowrap; }
.to-current > svg { flex:none; }
.next-up { background:var(--row-selected); box-shadow:inset 0 0 0 1px var(--chip-teal-line); }
.pager { display:flex; align-items:center; gap:6px; justify-self:center; }
.detail-action-state { min-width:8ch; text-align:center; font:600 12px/1 var(--mono); color:var(--ink-2); font-variant-numeric:tabular-nums; }
.evidence-control { justify-self:end; display:inline-flex; align-items:center; gap:6px; min-height:32px; padding:0 6px; font-size:12px; color:var(--ink); }
.evidence-control .keycap { display:none; }
.detail-scroll { overflow:auto; overscroll-behavior:contain; scrollbar-gutter:stable; padding:6px 8px 24px; min-height:0; }
.detail-loading { display:grid; align-content:start; gap:14px; padding:16px; }
.detail-loading .skeleton { height:22px; }
.loading { display:grid; gap:10px; padding:8px; }
.row-skeleton { display:grid; grid-template-columns:58px 1fr; gap:12px; height:54px; }
.empty { padding:28px 12px; display:grid; gap:8px; justify-items:start; }
.empty p,.hidden-history { font-size:13px; color:var(--ink-3); }
.peek-pane,.pending-pane { position:absolute; top:46px; right:0; bottom:8px; width:min(400px,calc(100% - 16px)); z-index:4; display:flex; flex-direction:column; min-height:0; background:var(--surface-raised); border-radius:16px; box-shadow:0 0 0 1px var(--line-2),var(--shadow-pop); }
.peek-pane > * { flex:1; }
.peek-pane :deep(.peek.inline) { padding:0; }
.pending-pane { padding:16px; gap:12px; }
.pending-controls { display:flex; gap:8px; }
.pending-controls button[aria-disabled="true"] { cursor:default; opacity:.55; }
.pending-scroll { overflow:auto; min-height:0; }
.pending-status { font-size:12px; margin-bottom:12px; color:var(--ink-2); }
.pending-head { display:flex; align-items:center; gap:12px; }
.pending-head h2 { flex:1; font-size:17px; }
.pending-list { display:grid; gap:0; list-style:none; padding:0; margin:0; }
.pending-list li { padding:10px 0; border-top:1px solid var(--line); font-size:13px; overflow-wrap:anywhere; }
.pending-meta { display:flex; gap:8px; flex-wrap:wrap; color:var(--ink-3); font-size:11px; margin-top:4px; }
.help-scrim { position:absolute; inset:0; z-index:5; display:grid; place-items:center; padding:24px; background:var(--palette-scrim); }
.help { width:min(460px,100%); padding:18px 22px 16px; border-radius:var(--radius); background:var(--surface-raised); box-shadow:var(--shadow-pop),var(--shadow); }
.help header { display:flex; align-items:center; justify-content:space-between; }
.help dl > div { display:grid; grid-template-columns:96px 1fr; align-items:center; gap:12px; min-height:36px; border-bottom:1px solid var(--line); }
.help dt { display:flex; gap:4px; }
.help dd { font-size:13px; color:var(--ink-2); }
.sr { position:absolute; width:1px; height:1px; overflow:hidden; clip-path:inset(50%); white-space:nowrap; }
.toggle:focus-visible,.to-current:focus-visible,.next-up:focus-visible,.evidence-control:focus-visible { outline:none; box-shadow:var(--focus-ring); }
@media(hover:hover) { .row:hover:not([aria-selected="true"]) { background:var(--row-hover); border-radius:10px; } .toggle:hover { background:var(--btn-bg-hover); } }
@media(max-width:1180px) and (min-width:761px) { .title-row { flex-wrap:wrap; } .titles { flex-basis:calc(100% - 164px); } .switches { order:4; } .search { order:5; } .compare-btn { order:6; } .help-btn { margin-left:auto; } .close-btn { order:1; } .body { grid-template-columns:minmax(300px,380px) minmax(0,1fr); gap:20px; } }
@media(max-width:760px) {
 .shell { padding:0 16px; grid-template-rows:auto minmax(0,1fr) auto; }
 .head { gap:10px; padding:12px 0 10px; }
 .title-row { flex-wrap:wrap; gap:10px; }
 .mark-backing { display:none; }
 .titles { flex:1; max-width:calc(100% - 54px); padding:4px 0; gap:10px; }
 .titles .eyebrow { font-size:9px; letter-spacing:.12em; gap:8px; }
 .hero { font-size:clamp(26px,7.4vw,34px); }
 .status-dock { gap:2px 10px; padding:4px 12px; }
 .dock-version { padding-left:0; border-left:0; }
 .close-btn { order:1; width:44px; height:44px; }
 .search { order:2; flex:1 1 calc(100% - 54px); min-width:0; }
 .search .field { height:44px; }
 .compare-btn { order:3; width:44px; height:44px; padding:0; }
 .compare-btn .btn-text,.compare-btn .keycap,.slash { display:none; }
 .switches { order:4; flex:1 1 auto; gap:6px; }
 .switches .seg button { height:44px; padding:0 12px; }
 .help-btn { display:flex; order:5; width:44px; height:44px; }
 .body { display:block; min-height:0; }
 .list-pane { height:100%; padding:2px 1px; }
 .filters { min-height:44px; padding-bottom:8px; }
 .toggles { gap:4px; }
 .toggle { height:44px; padding:0 11px; }
 .row { grid-template-columns:56px minmax(0,1fr) 10px; gap:8px; padding:12px 8px; }
 .line1 { gap:5px; }
 .row-name { font-size:14px; }
 .counts { gap:5px; font-size:9px; }
 .count { gap:2px; }
 .detail-pane { display:none; height:100%; grid-template-rows:44px minmax(0,1fr); gap:8px; }
 .show-detail .detail-pane { display:grid; }
 .show-detail .list-pane,.show-detail .head > .stats { display:none; }
 .detail-actions { gap:2px; }
 .pager { gap:0; }
 .detail-action-state { min-width:7ch; font-size:10px; }
 .detail-actions .icon-btn { width:32px; height:44px; }
 .evidence-control { height:44px; gap:4px; padding:0 2px; font-size:11px; }
 .to-current,.next-up { min-height:44px; padding:0 6px; font-size:11px; }
 .detail-scroll { padding:6px 2px 24px; }
 .peek-pane,.pending-pane { position:fixed; inset:0; z-index:10; width:auto; border-radius:0; }
 .pending-pane { padding:16px; }
 .mobile-footer { display:flex; align-items:center; justify-content:space-between; gap:8px; min-height:60px; border-top:1px solid var(--line); padding:8px 0 max(8px,env(safe-area-inset-bottom)); background:var(--canvas); }
 .mobile-footer button { display:flex; align-items:center; gap:6px; min-height:44px; padding:0 12px; border-radius:999px; font-size:12px; font-weight:600; }
 .mobile-footer button[aria-pressed="true"] { background:var(--seg-on); }
}
@media(pointer:coarse) { .toggle { min-height:44px; } .icon-btn { width:44px; height:44px; } }
</style>
