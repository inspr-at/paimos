<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { useRoute } from 'vue-router'
import { api } from '../../lib/api'
import { openModelPrefs } from '../../lib/modelPrefsCommand'
import { computed, nextTick, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { can } from '../../lib/authz'
import type { PairingPermissions, PairingView } from '../../lib/agentPairing'
import { describeComputerStatus, touchIDConfirmation } from '../../lib/agentPairing'
import {
  clone, daysLabel, nightLabel, putSchedule, reserveLabel, reserveLevel, daysSummary, timeLabel, unreportedCapacity, when, whenFull, workStart,
  type AccountRow, type CapacitySchedule, type Override, type PoolView,
} from '../../lib/capacity'
import { buildComputerCards, middleEllipsis, pacingSummary, readySummary, type AccountLine, type ComputerCard } from '../../lib/computerAccounts'
import { confirmAction } from '../../lib/confirm'
import { usePreference } from '../../lib/preferences'
import { toast } from '../../lib/toast'
import { useAgents } from '../../stores/agents'
import { useCapacity } from '../../stores/capacity'
import { useSession } from '../../stores/session'
import AppIcon from '../AppIcon.vue'
import CapacityGauge from './CapacityGauge.vue'
import CapacityLearning from './CapacityLearning.vue'
import CapacityLegend from './CapacityLegend.vue'
import ConnectedComputers from './ConnectedComputers.vue'
import HarnessMark from './HarnessMark.vue'
import KeepEditor, { type KeepDraft } from './KeepEditor.vue'
import ScheduleEditor from './ScheduleEditor.vue'

// Accounts and computers (AEON-499): one card per computer with its status
// (online, or offline since a time with the reason and the one fix) and its
// accounts nested inside, each with one readiness state and capacity only as
// measured. Pacing collapses into one summary button with the existing
// editors; destructive actions live in the row menus.
const route = useRoute()
const props = defineProps<{ permissions: PairingPermissions; showAccounts: boolean }>()
const capacity = useCapacity()
const agents = useAgents()
const session = useSession()
const mayManage = computed(() => session.identity?.principal.kind === 'person' && can('account.manage'))
const manageTip = 'Changing pacing needs permission to manage accounts'
const now = computed(() => agents.now)
const schedule = computed(() => capacity.schedule)
const days = computed(() => daysLabel(schedule.value.week))
const root = ref<HTMLElement>()
const computersRef = ref<InstanceType<typeof ConnectedComputers>>()

// ---------- Fold, remembered per person (AEON-402) ----------
const layoutPref = usePreference<{ setupFolded?: boolean }>('agents-page')
// The stored choice decides before the cards first show: no flash of a folded panel.
const layoutReady = ref(false)
void layoutPref.ready.then(() => { layoutReady.value = true })
// Reveal each new approval link once; an explicit Fold wins while it stays open.
const revealVerification = ref(!!route.query.verify_account)
watch(() => route.query.verify_account, account => { revealVerification.value = !!account })
const folded = computed(() => !!layoutPref.value.value?.setupFolded && !revealVerification.value)
function toggleFold() {
  const setupFolded = !folded.value
  revealVerification.value = false
  layoutPref.save({ ...(layoutPref.value.value ?? {}), setupFolded }, 0)
}

// ---------- Cards ----------
const rows = computed(() => (props.showAccounts ? capacity.rows : []))
// Start with attention first, then keep controls where the person saw them.
// A successful retry changes readiness; it must not reorder the clicked card.
const cardSnapshot = computed<{ identity: string; cards: ComputerCard[] }>((previous) => {
  const identity = `${session.identity?.tenant.id ?? ''}/${session.identity?.principal.id ?? ''}`
  const next = buildComputerCards({ computers: props.permissions.canListComputers ? capacity.computers : [], rows: rows.value, now: now.value })
  if (previous?.identity === identity) {
    const rank = new Map(previous.cards.map((card, index) => [card.key, index]))
    next.sort((a, b) => (rank.get(a.key) ?? rank.size) - (rank.get(b.key) ?? rank.size))
  }
  return { identity, cards: next }
})
const cards = computed(() => cardSnapshot.value.cards)
const summary = computed(() => readySummary(cards.value))
const pacingLine = computed(() => pacingSummary(schedule.value))
const empty = computed(() => capacity.loaded && !cards.value.length)
const shown = (line: AccountLine) => middleEllipsis(line.identity, 48)

// Owner-approved checks retain the row identity through both the write and refresh.
const verifying = ref('')
const identityKey = computed(() => `${session.identity?.tenant.id ?? ''}/${session.identity?.principal.id ?? ''}`)
watch(identityKey, () => { verifying.value = ''; revealVerification.value = !!route.query.verify_account })
const enrollmentOf = (line: AccountLine, card: ComputerCard) => card.computer?.enrollments.find(e => e.account_id === line.id)
function canVerify(line: AccountLine, card: ComputerCard) {
  const e = enrollmentOf(line, card)
  return mayManage.value && card.computer?.computer_state === 'connected' && e?.state === 'connected' && e.can_verify === true
    && card.computer.verification_capabilities?.[line.harness]?.supported === true
}
function verifyBusy(line: AccountLine, card: ComputerCard) {
  const e = enrollmentOf(line, card)
  return !!verifying.value || !e?.verification_stalled && ['starting', 'running', 'waiting', 'ownership_lost'].includes(e?.verification_state ?? '')
}
async function verifyAgain(line: AccountLine, card: ComputerCard) {
  if (!canVerify(line, card) || verifyBusy(line, card)) return
  const c = card.computer!, e = enrollmentOf(line, card)!
  const owner = identityKey.value, account = line.id, computer = c.computer_id, revision = c.revision, priorRun = e.verification_run_id
  if (!computer || !revision) return
  verifying.value = account
  try {
    const response = await api(`/agent-pairing/computers/${computer}/enrollments/${account}/verify`, { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ expected_revision: revision, expected_verification_run_id: priorRun }) })
    if (!response.ok) {
      const problem = await response.json().catch(() => ({})) as { code?: string }
      throw new Error(response.status === 403 ? 'Only the account owner may verify it again.' : response.status === 409 && problem.code === 'verification_active' ? 'The helper must confirm that the previous check stopped and settle its reports before another can start.' : response.status === 409 ? 'The account or verification changed. Refresh before verifying again.' : 'Verification could not be requested. Please try again.')
    }
    const created = await response.json() as { account_id?: string; run_id?: string }
    if (created.account_id !== account || typeof created.run_id !== 'string') throw new Error('Verification response could not be confirmed. Refresh the account list.')
    const current = cards.value.find(card => card.computer?.computer_id === computer)?.computer
    const currentEnrollment = current?.enrollments.find(e => e.account_id === account)
    if (identityKey.value !== owner || current?.revision !== revision || !currentEnrollment?.can_verify || ![priorRun, created.run_id].includes(currentEnrollment.verification_run_id)) return
    toast(`Verification requested for ${line.vendor} on ${card.name}.`)
    const result = await capacity.refreshComputers()
    if (identityKey.value === owner && !result.ok) toast('Verification was queued, but the account list could not refresh.', { tone: 'error' })
  } catch (error) {
    if (identityKey.value === owner) toast(error instanceof Error ? error.message : 'Verification could not be requested.', { tone: 'error' })
  } finally { if (identityKey.value === owner) verifying.value = '' }
}

// ---------- Check now: read capacity again; say what came back, a failure as a failure ----------
const checking = ref('')
async function checkNow(line: AccountLine, host: string) {
  if (checking.value) return
  checking.value = line.id
  try {
    const outcome = await capacity.refreshCapacity()
    if (!outcome.ok) { toast(`Capacity could not be read${'error' in outcome ? `: ${outcome.error}` : '.'} Please try again.`, { tone: 'error' }); return }
    const fresh = capacity.rows.find(r => r.id === line.id)
    if (!fresh) return
    toast(fresh.primary ? `${line.vendor} on ${host}: reading updated.` : `${unreportedCapacity(line.harness)}.`)
  } catch { toast('Capacity could not be read. Please try again.', { tone: 'error' }) }
  finally { checking.value = '' }
}
async function copy(command: string, host: string) {
  try { await navigator.clipboard?.writeText(command) } catch { /* the toast still names it */ }
  toast(`Copied: ${command} — run it on ${host}.`)
}

// ---------- Row menus: one open at a time ----------
type MenuTarget = { kind: 'account'; line: AccountLine; card: ComputerCard } | { kind: 'computer'; card: ComputerCard }
type Menu = MenuTarget & { style: Record<string, string> }
const menu = ref<Menu | null>(null)
const menuEl = ref<HTMLElement>()
let menuButton: HTMLElement | null = null
const poolOf = (row: AccountRow): PoolView | undefined => capacity.pools.find(p => p.rows.some(r => r.id === row.id)) ?? capacity.pools.find(p => p.id === (row.groupId ? `group:${row.groupId}` : row.harness))
async function openMenu(next: MenuTarget, event: Event) {
  const button = event.currentTarget as HTMLElement
  const same = menu.value && menuButton === button
  closeMenu()
  if (same) { button.focus(); return }
  closePacing(false)
  if (editor.value && !editorRef.value?.dirty()) closeEditor(false)
  menuButton = button
  menu.value = { ...next, style: { visibility: 'hidden' } }
  await nextTick()
  if (!root.value || !menuEl.value || !menu.value) return
  const box = root.value.getBoundingClientRect(), r = button.getBoundingClientRect(), w = menuEl.value.offsetWidth
  menu.value.style = { top: `${r.bottom - box.top + 4}px`, left: `${Math.max(8, Math.min(r.right - box.left - w, box.width - w - 8))}px` }
  await nextTick()
  menuEl.value?.querySelector<HTMLElement>('button, a')?.focus({ preventScroll: true })
  menuEl.value?.scrollIntoView({ block: 'nearest' })
}
function closeMenu(focus = false) {
  const button = menuButton
  menu.value = null
  menuButton = null
  if (focus) button?.focus({ preventScroll: true })
}
function menuKeys(event: KeyboardEvent) {
  if (event.key === 'Escape') { event.preventDefault(); event.stopPropagation(); closeMenu(true); return }
  if (event.key !== 'ArrowDown' && event.key !== 'ArrowUp') return
  event.preventDefault()
  const items = [...(menuEl.value?.querySelectorAll<HTMLElement>('button, a') ?? [])]
  const i = items.indexOf(document.activeElement as HTMLElement)
  items[(i + (event.key === 'ArrowDown' ? 1 : -1) + items.length) % items.length]?.focus()
}
/** The pool's own override; Away comes from your own schedule and ends in the header. */
const ownOverride = (pool: PoolView | undefined): Override => (!pool || pool.override === 'away' ? '' : pool.override)
function holdOptions() {
  const later = new Date(Math.floor((now.value + 2 * 3600e3) / 60_000) * 60_000).toISOString()
  const tomorrow = new Date(workStart(schedule.value, now.value, 1)).toISOString()
  const [day, time] = when(tomorrow, now.value).split(' ')
  return [
    { label: 'For 2 hours', hint: `until ${when(later, now.value)}`, name: 'Hold for 2 hours', until: later },
    { label: `Until ${day}`, hint: time ?? '', name: `Hold until ${when(tomorrow, now.value)}`, until: tomorrow },
    { label: 'Until I resume', hint: '', name: 'Hold until I resume', until: undefined },
  ]
}
function setOverride(pool: PoolView, value: Override, until?: string) {
  closeMenu(true)
  const done = value === 'sprint' ? `Sprint: agents may use everything left on ${pool.name} until it resets.`
    : value === 'hold' ? `Holding ${pool.name}${until ? ` until ${when(until, now.value)}` : ''}. Running steps finish.` : `${pool.name} follows your work week again.`
  void run(() => capacity.setPoolOverride(pool.id, value, until), done)
}
const canChange = (view: PairingView) => !!computersRef.value?.canChange(view)
const removal = (view: PairingView) => computersRef.value?.removal(view) ?? { allowed: false, reason: '' }
function disconnect(card: ComputerCard) { closeMenu(); if (card.computer) computersRef.value?.openDialog(card.computer, 'computer') }
function removeComputer(card: ComputerCard) { closeMenu(); if (card.computer) void computersRef.value?.remove(card.computer) }
const details = ref('')
function toggleDetails(card: ComputerCard) { closeMenu(true); details.value = details.value === card.key ? '' : card.key }

// ---------- Remove an account (AEON-402): person-only, named, history stays ----------
const removing = ref('')
async function removeAccount(line: AccountLine, host: string) {
  closeMenu()
  if (removing.value || !mayManage.value) return
  const queued = Object.values(agents.runs).filter(r => r.status === 'queued' && (r.account_id === line.id || r.requested_account_id === line.id)).length
  const ok = await confirmAction({
    title: `Remove ${line.identity}?`,
    body: 'It leaves Accounts and agents stop using it. Its runs and history stay.',
    points: [
      host ? `Its binding on ${host} is disconnected.` : '',
      queued ? `${queued === 1 ? 'One queued run' : `${queued} queued runs`} for it ${queued === 1 ? 'is' : 'are'} cancelled.` : '',
    ].filter(Boolean),
    confirmLabel: 'Remove account', danger: true,
  })
  if (!ok) return
  const menus = () => [...(root.value?.querySelectorAll<HTMLElement>('.acct .row-more button') ?? [])]
  const index = menus().findIndex(el => el.closest<HTMLElement>('.acct')?.dataset.account === line.id)
  removing.value = line.id
  try {
    await agents.removeAccount(line.row)
    toast(`Removed ${line.identity}. Its runs and history stay.`)
    // A disabled button takes no focus: settle busy before moving focus to the next row's menu.
    removing.value = ''
    await nextTick()
    const left = menus()
    ;(left[Math.min(index, left.length - 1)] ?? root.value?.querySelector<HTMLElement>('#ac-title'))?.focus()
  } catch (e) {
    const busyRun = e instanceof Error && /still working/i.test(e.message)
    toast(busyRun ? `A run is still working on ${line.identity}. Remove it once that run ends.` : e instanceof Error ? e.message : 'The account was not removed. Please try again.', { tone: 'error' })
  } finally { removing.value = '' }
}

// ---------- Pacing: one summary button, the settings in a popover ----------
const busy = ref(false)
async function run(work: () => Promise<void>, done?: string) {
  if (busy.value) return
  busy.value = true
  try { await work(); if (done) toast(done) }
  catch (e) { toast(e instanceof Error ? e.message : 'That did not save. Please try again.', { tone: 'error' }) }
  finally { busy.value = false }
}
const pacingOpen = ref(false)
const pacingEl = ref<HTMLElement>()
const pacingButton = ref<HTMLButtonElement>()
const pacingStyle = ref<Record<string, string>>({})
async function togglePacing() {
  if (pacingOpen.value) { closePacing(true); return }
  closeMenu()
  if (editor.value) closeEditor(false)
  pacingOpen.value = true
  pacingStyle.value = { visibility: 'hidden' }
  await nextTick()
  placeUnder(pacingEl.value, pacingStyle)
  await nextTick()
  pacingEl.value?.querySelector<HTMLElement>('[aria-checked="true"], button:not(:disabled)')?.focus({ preventScroll: true })
}
function closePacing(focus: boolean) {
  if (!pacingOpen.value) return
  pacingOpen.value = false
  if (focus) pacingButton.value?.focus({ preventScroll: true })
}
function placeUnder(el: HTMLElement | undefined, style: typeof pacingStyle) {
  if (!el || !root.value || !pacingButton.value) return
  const box = root.value.getBoundingClientRect(), g = pacingButton.value.getBoundingClientRect(), w = el.offsetWidth
  style.value = { top: `${g.bottom - box.top + 8}px`, left: `${Math.max(12, Math.min(g.right - box.left - w, box.width - w - 12))}px`, maxHeight: `${Math.max(440, window.innerHeight - g.bottom - 64)}px` }
}
function pacingKeys(event: KeyboardEvent) {
  if (event.key === 'Escape') { event.preventDefault(); event.stopPropagation(); closePacing(true) }
}
const setPreset = (n: 5 | 6 | 7) => { if (mayManage.value && days.value.preset !== n) void run(() => capacity.setPreset(n)) }
const toggleNights = () => { if (mayManage.value) void run(() => capacity.setNights(!schedule.value.nights)) }
function daysKey(event: KeyboardEvent) {
  if (event.key !== 'ArrowLeft' && event.key !== 'ArrowRight') return
  event.preventDefault()
  const cur = days.value.preset ?? 5
  const next = Math.max(5, Math.min(7, cur + (event.key === 'ArrowRight' ? 1 : -1))) as 5 | 6 | 7
  setPreset(next)
  void nextTick(() => pacingEl.value?.querySelector<HTMLElement>(`.days [data-v="${next}"]`)?.focus())
}
const keepLabel = computed(() => {
  const auto = !schedule.value.reserve || schedule.value.reserve === 'auto'
  const levels = capacity.rows.flatMap(row => row.learning?.windows.map(w => w.auto_reserve_percent).filter((n): n is number => !!n) ?? [])
  return auto && levels.length ? 'Auto · learned' : reserveLabel(schedule.value)
})

// ---------- Editors: the existing week, night and Keep for you popovers ----------
type EditorKind = 'week' | 'night' | 'keep'
const editor = ref<{ kind: EditorKind } | null>(null)
const editorRef = ref<{ root?: HTMLElement | null; dirty: () => boolean; focusTitle: () => void }>()
const editorStyle = ref<Record<string, string>>({})
const sheet = ref(false)
const saving = ref(false)
const phoneQuery = typeof window !== 'undefined' ? window.matchMedia('(max-width: 720px)') : null
async function openEditor(kind: EditorKind) {
  if (editor.value?.kind === kind) { closeEditor(); return }
  closeMenu()
  closePacing(false)
  sheet.value = !!phoneQuery?.matches
  editor.value = { kind }
  editorStyle.value = { visibility: 'hidden' }
  await nextTick()
  if (!sheet.value) {
    placeUnder(editorRef.value?.root ?? undefined, editorStyle)
    await nextTick()
    editorRef.value?.root?.scrollIntoView({ block: 'nearest', behavior: window.matchMedia('(prefers-reduced-motion: reduce)').matches ? 'auto' : 'smooth' })
    editorRef.value?.focusTitle()
  }
}
let sheetReturn: HTMLElement | null = null
function closeEditor(focus = true) {
  const onSheet = sheet.value && !!editor.value
  editor.value = null
  editorStyle.value = {}
  if (onSheet) { sheetReturn = focus ? pacingButton.value ?? null : null; return }
  if (focus) pacingButton.value?.focus({ preventScroll: true })
}
async function saveEditor(next: CapacitySchedule) {
  saving.value = true
  try { await capacity.saveSchedule(next); closeEditor(); toast("Saved. Today's plan follows the new schedule.") }
  catch (e) { toast(e instanceof Error ? e.message : 'The schedule did not save. Please try again.', { tone: 'error' }) }
  finally { saving.value = false }
}
async function saveKeep(draft: KeepDraft) {
  saving.value = true
  try {
    await capacity.saveKeep(draft)
    closeEditor()
    toast(draft.away ? `Saved. Agents use everything until ${when(draft.away, now.value)}.` : draft.reserve === 'off' ? 'Saved. Agents may use everything.' : 'Saved. Agents leave you room while you work.')
  } catch (e) { toast(e instanceof Error ? e.message : 'Keep for you did not save. Please try again.', { tone: 'error' }) }
  finally { saving.value = false }
}
const endAway = () => void run(() => capacity.endAway(), 'Welcome back. Agents follow your plan again.')
/** The learned hours as this account's own work week. */
async function useHours(row: AccountRow) {
  const hours = row.learning?.suggested_hours
  if (!mayManage.value || !hours || !row.schedule) return
  const next = clone(row.schedule)
  next.week = next.week.map(d => d.on ? { ...d, start: hours.start, end: hours.end } : d)
  await run(async () => { await putSchedule({ scope: 'account', account_id: row.id, schedule: next }); await agents.afterWrite() }, `Saved ${row.name}'s work hours.`)
}

// ---------- The one-time plan card ----------
const cardClosed = ref(false)
const planCard = computed(() => {
  if (!mayManage.value || cardClosed.value || !capacity.loaded || !capacity.schedulesLoaded || !capacity.pools.length || capacity.reserveConfirmed) return null
  const s = schedule.value
  const on = s.week.filter(d => d.on)
  const hours = on.every(d => d.start === on[0].start && d.end === on[0].end) ? `${timeLabel(on[0].start)}–${timeLabel(on[0].end)}` : 'in your hours'
  const level = reserveLevel(s)
  return capacity.hasUserSchedule
    ? { title: 'New:', text: `agents now leave you room while you work, about ${level}% of every limit.`, secondary: 'Turn off', primary: 'Keep' }
    : { title: "Here's the plan.", text: `Agents work alongside you ${daysSummary(s.week)}, ${hours}, pace each account to its reset, and leave you about ${level}% of every limit while you work.`, secondary: 'Change', primary: 'Looks right' }
})
function cardPrimary() { void run(() => capacity.confirmReserve('auto'), 'Saved. Agents leave you room while you work.') }
function cardSecondary() {
  if (capacity.hasUserSchedule) { void run(() => capacity.confirmReserve('off'), 'Turned off. Agents may use everything.'); return }
  cardClosed.value = true
  void togglePacing()
}

// ---------- Phone sheet: the page behind it is inert ----------
const inerted: { el: HTMLElement; inert: boolean; hidden: string | null }[] = []
function setBackground(off: boolean) {
  if (off) {
    const host = document.querySelector('.sheet-host')
    for (const el of [...document.body.children] as HTMLElement[]) {
      if (host && el.contains(host)) continue
      inerted.push({ el, inert: el.inert, hidden: el.getAttribute('aria-hidden') })
      el.inert = true
      el.setAttribute('aria-hidden', 'true')
    }
  } else {
    for (const { el, inert, hidden } of inerted.splice(0)) {
      el.inert = inert
      if (hidden === null) el.removeAttribute('aria-hidden'); else el.setAttribute('aria-hidden', hidden)
    }
  }
}
watch(() => !!editor.value && sheet.value, async open => {
  await nextTick()
  setBackground(false)
  const back = sheetReturn
  sheetReturn = null
  if (open) setBackground(true)
  else if (back?.isConnected) back.focus({ preventScroll: true })
})

// ---------- Outside clicks and resizes ----------
function outside(event: MouseEvent) {
  const target = event.target as HTMLElement
  if (!target.isConnected) return
  if (menu.value && !menuEl.value?.contains(target) && target !== menuButton && !menuButton?.contains(target)) closeMenu()
  if (pacingOpen.value && !pacingEl.value?.contains(target) && !pacingButton.value?.contains(target)) closePacing(false)
  if (editor.value && !sheet.value && !editorRef.value?.root?.contains(target) && !pacingButton.value?.contains(target) && !editorRef.value?.dirty()) closeEditor(false)
}
function reflow() {
  if (pacingOpen.value) placeUnder(pacingEl.value, pacingStyle)
  if (editor.value && !sheet.value) placeUnder(editorRef.value?.root ?? undefined, editorStyle)
}
onMounted(() => { document.addEventListener('click', outside, true); window.addEventListener('resize', reflow) })
onBeforeUnmount(() => { document.removeEventListener('click', outside, true); window.removeEventListener('resize', reflow); setBackground(false) })
const statusOf = (card: ComputerCard) => (card.computer ? describeComputerStatus(card.computer) : null)
</script>

<template>
  <section v-if="layoutReady" ref="root" class="ac" :class="{ folded }" aria-labelledby="ac-title">
    <div class="ac-head">
      <div class="ac-title">
        <button type="button" class="fold" :aria-expanded="!folded" aria-controls="ac-body" aria-label="Accounts and computers" :data-tip="folded ? 'Show' : 'Fold'" @click="toggleFold">
          <AppIcon name="chevron-right" :size="14" class="chev" />
        </button>
        <h2 id="ac-title" tabindex="-1">Accounts and computers</h2>
        <button type="button" class="icon-btn sm flat" aria-label="Account model preferences" data-tip="Which models do which work" @click="openModelPrefs()"><AppIcon name="gear" :size="15" /></button>
        <span v-if="summary" class="pill" :class="summary.tone">{{ summary.text }}</span>
        <span v-if="capacity.away" class="away-chip" :data-tip="`Away: agents use everything left until ${whenFull(capacity.away)}`">
          Away until {{ when(capacity.away, now) }}
          <button v-if="mayManage" type="button" aria-label="Back now: end Away" data-tip="Back now" :disabled="busy" @click="endAway"><AppIcon name="close" :size="12" /></button>
        </span>
      </div>
      <div v-if="!folded" class="ac-actions">
        <button
          v-if="showAccounts" ref="pacingButton" type="button" class="pacing-btn" aria-haspopup="dialog" :aria-expanded="pacingOpen || !!editor"
          :data-tip="mayManage ? 'Work days, Keep for you and nights' : manageTip" @click="togglePacing"
        ><AppIcon name="sliders" :size="15" /><span class="pacing-line">{{ pacingLine }}</span><AppIcon name="chevron" :size="14" /></button>
        <RouterLink v-if="mayManage" class="ghost-link add-account" to="/settings/accounts#add-account" aria-label="Add an account" data-tip="Add an account on a paired machine"><AppIcon name="plus" :size="15" /><span>Add<span class="long"> an account</span></span></RouterLink>
        <RouterLink class="ghost-link manage" to="/settings/accounts" aria-label="Manage accounts" data-tip="Accounts in Settings: sign-ins, names, which accounts agents may use"><AppIcon name="gear" :size="15" class="phone-only" /><span class="wide-only">Manage</span></RouterLink>
      </div>
    </div>

    <div v-if="!folded" id="ac-body" class="ac-body">
      <div v-if="planCard" class="plan-card" role="region" aria-label="The plan">
        <p><b>{{ planCard.title }}</b> {{ planCard.text }}</p>
        <span class="pc-actions">
          <button class="btn" type="button" :disabled="busy" @click="cardSecondary">{{ planCard.secondary }}</button>
          <button class="btn primary" type="button" :disabled="busy" @click="cardPrimary">{{ planCard.primary }}</button>
        </span>
      </div>

      <p v-if="empty" class="empty">No accounts or computers yet. <RouterLink to="/agents/register-agent">Connect a computer</RouterLink>, sign in to a harness there, and it appears here.</p>
      <p v-else-if="!capacity.loaded && capacity.state === 'error'" class="empty">Accounts could not be loaded. <button type="button" class="btn sm" @click="capacity.load()">Try again</button></p>

      <section v-for="card in cards" :key="card.key" class="computer glass-card" :class="{ offline: card.offline }" :aria-label="`Computer ${card.name}`" :data-computer="card.key">
        <div class="c-head">
          <span class="c-glyph"><AppIcon name="monitor" :size="18" /></span>
          <div class="c-id">
            <p class="c-name-line"><span class="c-name">{{ card.name }}</span><span v-if="card.agent" class="c-agent">{{ card.agent }}</span></p>
            <p v-if="card.caption" class="c-caption">{{ card.caption }}</p>
            <p v-if="card.advice" class="c-advice" role="status">{{ card.advice }}</p>
          </div>
          <div class="c-side">
            <span v-if="card.status" class="pill" :class="card.status.tone"><span class="dot" :class="{ live: card.status.live }" aria-hidden="true" />{{ card.status.text }}</span>
            <button
              v-if="card.computer" type="button" class="icon-btn flat more" aria-haspopup="menu" :aria-expanded="menu?.kind === 'computer' && menu.card.key === card.key"
              :aria-label="`More for ${card.name}`" data-tip="More" @click="openMenu({ kind: 'computer', card }, $event)"
            ><AppIcon name="more" :size="16" /></button>
          </div>
        </div>
        <p v-if="card.computer" class="computer-confirmation">{{ touchIDConfirmation(card.computer) }}</p>
        <div v-if="card.notice" class="notice" role="status">
          <AppIcon name="alert" :size="16" class="n-icon" />
          <div class="n-text">
            <p class="n-title">{{ card.notice.title }}</p>
            <p class="n-body">{{ card.notice.body }}</p>
            <p class="n-fix"><code>{{ card.notice.command }}</code><button type="button" class="n-copy" :aria-label="`Copy ${card.notice.command}`" @click="copy(card.notice.command, card.name)"><AppIcon name="copy" :size="14" />Copy</button></p>
          </div>
        </div>
        <p v-if="details === card.key && statusOf(card)" class="c-details">{{ statusOf(card)!.detail }} {{ statusOf(card)!.next }}</p>

        <div v-if="card.accounts.length" role="table" :aria-label="`Accounts on ${card.name}`" class="accts">
          <div v-for="line in card.accounts" :key="line.id" role="row" class="acct" :data-account="line.id" :data-ready="line.readiness.kind">
            <div role="cell" class="acct-who">
              <span class="vendor"><HarnessMark :harness="line.harness" :size="16" /></span>
              <div class="who-text">
                <p class="vendor-name">{{ line.vendor }}<span v-if="line.group" class="group" :title="`Kept separate: ${line.group}`"> · {{ line.group }}</span></p>
                <p class="identity" :title="line.identity">{{ shown(line) }}</p>
              </div>
            </div>
            <div role="cell" class="acct-ready">
              <div class="readiness-actions">
                <button v-if="canVerify(line, card)" type="button" class="verify-again" :disabled="verifyBusy(line, card)" :aria-busy="verifying === line.id" :data-requested="route.query.verify_account === line.id || undefined" @click="verifyAgain(line, card)">Verify again</button>
                <span class="pill" :class="line.readiness.tone" :data-tip="line.readiness.tip"><span class="dot" aria-hidden="true" />{{ line.readiness.text }}</span>
              </div>
              <button v-if="line.readiness.command" type="button" class="fix" :data-tip="`Copies ${line.readiness.command} to run on ${card.name}`" @click="copy(line.readiness.command, card.name)"><AppIcon name="copy" :size="13" />{{ line.readiness.command }}</button>
              <p v-if="line.readiness.hint" class="r-hint">{{ line.readiness.hint }}</p>
            </div>
            <div role="cell" class="acct-cap">
              <div v-if="line.capacity.kind === 'bar'" class="meter">
                <p class="meter-head"><b>{{ Math.round(line.capacity.left) }}% left</b><span>{{ [line.capacity.window, `resets ${line.capacity.resets}`].filter(Boolean).join(' · ') }}</span></p>
                <CapacityGauge :gauge="line.capacity.gauge" :left="line.capacity.left" :value="line.capacity.left" :label="line.capacity.label" :dim="line.capacity.dim" />
                <p v-if="line.capacity.five !== null || line.capacity.source || line.capacity.note" class="meter-foot">{{ [line.capacity.note, line.capacity.five !== null ? `5-hour window: ${line.capacity.five}% left` : '', line.capacity.source].filter(Boolean).join(' · ') }}</p>
              </div>
              <span v-else-if="line.capacity.kind === 'none'" class="no-reading">
                <span class="quiet">No reading yet</span>
                <button type="button" class="check-now" :disabled="!!checking" @click="checkNow(line, card.name)"><AppIcon name="refresh" :size="14" />{{ checking === line.id ? 'Checking…' : 'Check now' }}</button>
              </span>
              <span v-else-if="line.capacity.kind === 'offline'" class="quiet">No reading while the computer is offline</span>
              <span v-else class="quiet">{{ line.capacity.text }}</span>
              <CapacityLearning :learning="line.row.learning" :host="card.name" :now="now" :may-manage="mayManage" :saving="busy" @hours="useHours(line.row)" @away="openEditor('keep')" />
            </div>
            <div role="cell" class="row-more">
              <button
                v-if="mayManage" type="button" class="icon-btn flat more" aria-haspopup="menu" :aria-expanded="menu?.kind === 'account' && menu.line.id === line.id"
                :aria-label="`More for ${line.vendor} ${line.identity}`" data-tip="More" :disabled="removing === line.id" @click="openMenu({ kind: 'account', line, card }, $event)"
              ><AppIcon name="more" :size="16" /></button>
            </div>
          </div>
        </div>
        <p v-else-if="!showAccounts" class="c-empty">You can't see the accounts on this computer.</p>
        <p v-else class="c-empty">No accounts on this computer yet.<RouterLink v-if="mayManage" to="/settings/accounts#add-account">Add an account</RouterLink></p>

        <div v-if="card.legend" class="legend-row">
          <CapacityLegend :yours="card.accounts.some(a => a.capacity.kind === 'bar' && a.capacity.gauge.yours >= 0.5)" />
          <span class="fine">Your own use counts toward today's share too.</span>
        </div>
      </section>

      <ConnectedComputers ref="computersRef" :permissions="permissions" embedded revoked-only />
    </div>

    <div v-if="pacingOpen" ref="pacingEl" class="pacing" role="dialog" aria-label="Pacing" :style="pacingStyle" @keydown="pacingKeys">
      <div class="setting days">
        <span id="days-lbl" class="lbl">Work days a week</span>
        <span class="seg" role="radiogroup" aria-labelledby="days-lbl" @keydown="daysKey">
          <button
            v-for="n in ([5, 6, 7] as const)" :key="n" type="button" role="radio" :data-v="n" :aria-checked="days.preset === n" :tabindex="(days.preset ?? 5) === n && days.preset !== null ? 0 : -1"
            :disabled="!mayManage" :aria-disabled="busy" :data-tip="mayManage ? undefined : manageTip" @click="setPreset(n)"
          >{{ n }}</button>
          <button v-if="days.preset === null" type="button" role="radio" data-v="custom" aria-checked="true" tabindex="0" :disabled="!mayManage" @click="openEditor('week')">{{ days.custom }}</button>
        </span>
        <span class="hint" :class="{ custom: days.preset === null }">{{ days.hint }}</span>
        <button class="gear" type="button" aria-haspopup="dialog" :aria-expanded="editor?.kind === 'week'" aria-label="Customize work week" :data-tip="mayManage ? 'Customize work week' : manageTip" :disabled="!mayManage" @click="openEditor('week')"><AppIcon name="gear" :size="15" /></button>
      </div>
      <div class="setting keep">
        <span id="keep-lbl" class="lbl">Keep for you</span>
        <button
          class="keep-btn" type="button" aria-haspopup="dialog" :aria-expanded="editor?.kind === 'keep'" aria-labelledby="keep-lbl keep-val" :disabled="!mayManage"
          :data-tip="mayManage ? 'How much of every limit agents leave you while you work' : manageTip" @click="openEditor('keep')"
        ><span id="keep-val">{{ keepLabel }}</span><AppIcon name="chevron" :size="14" /></button>
      </div>
      <div class="setting nights">
        <span id="nights-lbl" class="lbl" @click="toggleNights">Agents at night</span>
        <button class="tog" type="button" role="switch" :aria-checked="schedule.nights" aria-labelledby="nights-lbl" :disabled="!mayManage" :aria-disabled="busy" :data-tip="mayManage ? undefined : manageTip" @click="toggleNights" />
        <span class="hint" :class="{ off: !schedule.nights }">{{ nightLabel(schedule) }}</span>
        <button class="gear" type="button" aria-haspopup="dialog" :aria-expanded="editor?.kind === 'night'" aria-label="Customize night and shifts" :data-tip="mayManage ? 'Customize night and shifts' : manageTip" :disabled="!mayManage" @click="openEditor('night')"><AppIcon name="gear" :size="15" /></button>
      </div>
    </div>

    <div v-if="menu" ref="menuEl" class="menu" role="menu" :aria-label="menu.kind === 'account' ? `${menu.line.vendor} ${menu.line.identity}` : menu.card.name" :style="menu.style" @keydown="menuKeys">
      <template v-if="menu.kind === 'account'">
        <template v-for="pool in [poolOf(menu.line.row)]" :key="pool?.id ?? 'none'">
          <template v-if="pool">
            <button v-if="ownOverride(pool)" type="button" role="menuitem" @click="setOverride(pool, '')"><AppIcon name="play" :size="16" /><span class="t">Back to the plan</span><span class="d">{{ pool.name }} paces by your work week again.</span></button>
            <button v-if="ownOverride(pool) !== 'sprint'" type="button" role="menuitem" @click="setOverride(pool, 'sprint')"><AppIcon name="bolt" :size="16" /><span class="t">Sprint {{ pool.name }} until reset</span><span class="d">{{ pool.sprintEnd ? `Agents may use everything left until ${when(pool.sprintEnd, now)}.` : 'Agents may use everything left until the next reset.' }}</span></button>
            <div v-if="ownOverride(pool) !== 'hold'" class="hold" role="group" aria-labelledby="hold-t">
              <AppIcon name="pause" :size="16" /><span id="hold-t" class="t">Hold {{ pool.name }}</span><span class="d">Agents leave {{ pool.name }} alone. Running steps finish.</span>
              <span class="hold-opts">
                <button v-for="h in holdOptions()" :key="h.name" type="button" role="menuitem" :aria-label="h.name" @click="setOverride(pool, 'hold', h.until)"><span>{{ h.label }}</span><span class="hint">{{ h.hint }}</span></button>
              </span>
            </div>
          </template>
        </template>
        <span class="sep" role="separator" />
        <button type="button" role="menuitem" class="danger" @click="removeAccount(menu.line, menu.card.name)"><AppIcon name="trash" :size="16" /><span class="t">Remove account</span><span class="d">Agents stop using it. Its runs and history stay.</span></button>
      </template>
      <template v-else-if="menu.card.computer">
        <button type="button" role="menuitem" @click="toggleDetails(menu.card)"><AppIcon name="list" :size="16" /><span class="t">{{ details === menu.card.key ? 'Hide details' : 'Details' }}</span><span class="d">Setup, local processes and accounting.</span></button>
        <RouterLink v-if="menu.card.computer.computer_state === 'connected' && permissions.canApprove" role="menuitem" :to="`/agents/register-agent?computer=${menu.card.computer.computer_id}`" @click="closeMenu()"><AppIcon name="plus" :size="16" /><span class="t">Add harness</span><span class="d">Sign in to another harness on {{ menu.card.name }}.</span></RouterLink>
        <template v-if="canChange(menu.card.computer) || removal(menu.card.computer).allowed">
          <span class="sep" role="separator" />
          <button v-if="canChange(menu.card.computer)" type="button" role="menuitem" class="danger" @click="disconnect(menu.card)"><AppIcon name="logout" :size="16" /><span class="t">Disconnect</span><span class="d">New work stops here; running work may finish.</span></button>
          <button v-else type="button" role="menuitem" class="danger" @click="removeComputer(menu.card)"><AppIcon name="trash" :size="16" /><span class="t">Remove</span><span class="d">It leaves this list. Its history stays in the audit log.</span></button>
        </template>
      </template>
    </div>

    <template v-if="editor && !sheet">
      <KeepEditor
        v-if="editor.kind === 'keep'" ref="editorRef" :schedule="schedule" :away="capacity.away" :pool-reserves="capacity.poolReserves" :sheet="false" :now="now" :accounts="capacity.inputs" :pools="capacity.pools"
        :timezone="capacity.timezone" :saving="saving" :style="editorStyle" @close="closeEditor()" @save="saveKeep"
      />
      <ScheduleEditor
        v-else ref="editorRef" :key="editor.kind" :kind="editor.kind === 'night' ? 'night' : 'week'" :schedule="schedule" :sheet="false" :now="now" :accounts="capacity.inputs" :pools="capacity.pools" :timezone="capacity.timezone" :saving="saving"
        :style="editorStyle" @close="closeEditor()" @save="saveEditor"
      />
    </template>
    <Teleport to="body">
      <div v-if="editor && sheet" class="sheet-host">
        <div class="scrim" @click="closeEditor()" />
        <KeepEditor
          v-if="editor.kind === 'keep'" ref="editorRef" :schedule="schedule" :away="capacity.away" :pool-reserves="capacity.poolReserves" :sheet="true" :now="now" :accounts="capacity.inputs" :pools="capacity.pools"
          :timezone="capacity.timezone" :saving="saving" @close="closeEditor()" @save="saveKeep"
        />
        <ScheduleEditor
          v-else ref="editorRef" :key="editor.kind" :kind="editor.kind === 'night' ? 'night' : 'week'" :schedule="schedule" :sheet="true" :now="now" :accounts="capacity.inputs" :pools="capacity.pools" :timezone="capacity.timezone" :saving="saving"
          @close="closeEditor()" @save="saveEditor"
        />
      </div>
    </Teleport>
  </section>
</template>

<style scoped>
.readiness-actions { align-self: flex-start; display: flex; flex-direction: column; align-items: flex-start; gap: 4px; }
.verify-again { font: inherit; font-size: 12px; padding: .35em .6em; border: 1px solid var(--line); background: transparent; color: var(--ink); cursor: pointer; }
@media (pointer: coarse) { .verify-again { min-height: 44px; } }
.verify-again:disabled { cursor: default; opacity: .6; }
.verify-again[data-requested] { outline: 1px solid var(--ink-3); outline-offset: 2px; }
.computer-confirmation { margin: 0; padding: 0 16px 12px; color: var(--ink-2); font-size: 12px; }
.ac { position: relative; display: grid; gap: 14px; min-width: 0; z-index: 3; container: ac / inline-size; }
/* Folding removes the taller actions; keep the title controls anchored at the top. */
.ac-head { display: flex; align-items: flex-start; gap: 10px 12px; flex-wrap: wrap; min-width: 0; }
.ac-title { display: flex; align-items: center; gap: 8px 10px; flex-wrap: wrap; min-width: 0; }
.ac-title h2 { font: 700 21px/1.2 var(--font); letter-spacing: -.015em; color: var(--ink); }
.ac-title h2:focus { outline: none; }
.fold { display: grid; place-items: center; width: 28px; height: 28px; margin-right: -4px; padding: 0; border: 0; border-radius: 8px; background: none; color: var(--ink-3); cursor: pointer; }
@media (hover: hover) { .fold:hover { background: var(--row-hover); color: var(--ink); } }
.fold:focus-visible { box-shadow: var(--focus-ring); }
.fold .chev { transform: rotate(90deg); transition: transform .2s ease; }
.folded .fold .chev { transform: none; }
@media (prefers-reduced-motion: reduce) { .fold .chev { transition: none; } }
.ac-actions { display: flex; align-items: center; gap: 6px; margin-left: auto; min-width: 0; }
.pacing-btn { display: inline-flex; align-items: center; gap: 8px; height: 36px; min-width: 0; padding: 0 13px; border: 0; border-radius: 999px; background: var(--surface-raised); box-shadow: inset 0 0 0 1px var(--line-2); color: var(--ink); font: 600 13px/1 var(--font); white-space: nowrap; cursor: pointer; font-variant-numeric: tabular-nums; }
.pacing-btn svg { flex: none; color: var(--ink-2); }
.pacing-line { min-width: 0; overflow: hidden; text-overflow: ellipsis; }
@media (hover: hover) { .pacing-btn:hover { background: var(--row-hover); } }
.pacing-btn[aria-expanded="true"] { background: var(--row-selected); box-shadow: inset 0 0 0 1px var(--chip-teal-line); color: var(--teal-ink); }
.pacing-btn:focus-visible { outline: none; box-shadow: var(--focus-ring); }
.ghost-link { display: inline-flex; align-items: center; gap: 7px; height: 36px; padding: 0 13px; border-radius: 999px; color: var(--ink); font-size: 13.5px; font-weight: 600; white-space: nowrap; text-decoration: none; }
@media (hover: hover) { .ghost-link:hover { background: var(--row-hover); } }
.ghost-link:focus-visible { box-shadow: var(--focus-ring); }
.phone-only { display: none; }
@media (max-width: 720px) { .phone-only { display: block; } }

/* One pill shape for every state: a subtle full tint, never an edge bar. */
.pill { display: inline-flex; align-items: center; gap: 7px; height: 26px; padding: 0 11px; border-radius: 999px; font-size: 12.5px; font-weight: 600; line-height: 1; white-space: nowrap; }
.pill.ok { background: color-mix(in srgb, var(--ok) 13%, transparent); color: color-mix(in srgb, var(--ok) 70%, var(--ink)); }
/* Warning text is --warn-ink: --gold-ink on this tint is about 4:1 in light, under 4.5:1. */
.pill.warn { background: color-mix(in srgb, var(--gold) 17%, transparent); color: var(--warn-ink); }
.pill.mute { background: var(--surface-sunken); color: var(--ink-2); }
.pill .dot { width: 7px; height: 7px; border-radius: 50%; flex: none; background: currentColor; }
.pill.ok .dot { background: var(--ok); }
.pill.warn .dot { background: var(--gold); }
.pill.mute .dot { background: var(--ink-3); }
.pill .dot.live { box-shadow: 0 0 0 3px color-mix(in srgb, var(--ok) 16%, transparent); animation: breathe 2.4s ease-in-out infinite; }
@keyframes breathe { 0%, 100% { box-shadow: 0 0 0 3px color-mix(in srgb, var(--ok) 16%, transparent); } 50% { box-shadow: 0 0 0 6px color-mix(in srgb, var(--ok) 6%, transparent); } }
@media (prefers-reduced-motion: reduce) { .pill .dot.live { animation: none; } }

.away-chip { display: inline-flex; align-items: center; gap: 4px; height: 24px; padding: 0 2px 0 10px; border-radius: 999px; background: var(--chip-teal-bg); box-shadow: inset 0 0 0 1px var(--chip-teal-line); color: var(--teal-ink); font-size: 12px; font-weight: 600; white-space: nowrap; }
.away-chip button { display: grid; place-items: center; width: 20px; height: 20px; padding: 0; border: 0; border-radius: 50%; background: transparent; color: inherit; }
.away-chip button:hover { background: var(--row-hover); }
.away-chip button:focus-visible { box-shadow: var(--focus-ring); }
.ac-body { display: grid; gap: 14px; min-width: 0; }
.plan-card { display: flex; align-items: center; gap: 12px 18px; flex-wrap: wrap; padding: 12px 14px 12px 16px; border-radius: 14px; background: color-mix(in srgb, var(--teal) 7%, var(--surface-raised)); box-shadow: inset 0 0 0 1px var(--chip-teal-line); }
.plan-card p { flex: 1 1 420px; margin: 0; color: var(--ink-2); font-size: 13.5px; line-height: 1.5; text-wrap: pretty; }
.plan-card b { color: var(--ink); font-weight: 650; }
.pc-actions { display: inline-flex; gap: 8px; margin-left: auto; }
.empty { padding: 16px 4px; color: var(--ink-2); font-size: 13.5px; }

/* ---------- A computer ---------- */
.computer { padding: 18px 22px 10px; border-radius: 22px; min-width: 0; }
.c-head { display: flex; align-items: center; gap: 14px; flex-wrap: wrap; min-width: 0; }
.c-glyph { display: grid; place-items: center; flex: none; width: 44px; height: 44px; border-radius: 13px; background: var(--surface-raised); box-shadow: inset 0 0 0 1px var(--line); color: var(--teal-ink); }
.c-id { flex: 1 1 220px; min-width: 0; }
.c-name-line { display: flex; align-items: center; gap: 6px 10px; flex-wrap: wrap; margin: 0; }
.c-name { color: var(--ink); font: 700 18px/1.2 var(--font); letter-spacing: -.01em; overflow-wrap: anywhere; }
.c-agent { padding: 5px 8px; border-radius: 7px; background: var(--surface-sunken); color: var(--ink-2); font: 500 11px/1 var(--mono); white-space: nowrap; }
.c-caption { margin: 3px 0 0; color: var(--ink-3); font-size: 13px; overflow-wrap: anywhere; }
.c-advice { margin: 4px 0 0; color: var(--warn-ink); font-size: 12.5px; line-height: 1.45; overflow-wrap: anywhere; }
.r-hint { margin: 0; max-width: 100%; color: var(--ink-2); font-size: 12px; line-height: 1.45; overflow-wrap: anywhere; }
.c-side { display: flex; align-items: center; gap: 6px; margin-left: auto; }
.more { width: 36px; height: 36px; color: var(--ink-2); }
.notice { display: flex; gap: 12px; align-items: flex-start; margin-top: 14px; padding: 12px 14px; border-radius: 14px; background: color-mix(in srgb, var(--gold) 11%, transparent); color: var(--ink); }
.n-icon { flex: none; margin-top: 2px; color: var(--gold-ink); }
.n-text { min-width: 0; flex: 1; }
.n-title { margin: 0; font-size: 14px; font-weight: 600; line-height: 1.45; }
.n-body { margin: 2px 0 0; color: var(--ink-2); font-size: 13.5px; line-height: 1.5; }
.n-fix { display: flex; align-items: center; gap: 8px; flex-wrap: wrap; margin: 8px 0 0; }
.n-fix code { padding: 7px 10px; border-radius: 8px; background: var(--surface-raised); box-shadow: inset 0 0 0 1px var(--line-2); color: var(--ink); font-size: 13px; line-height: 1; }
.n-copy, .fix, .check-now { display: inline-flex; align-items: center; gap: 6px; height: 30px; padding: 0 10px; border: 0; border-radius: 999px; background: transparent; color: var(--warn-ink); font: 600 12.5px/1 var(--font); white-space: nowrap; cursor: pointer; }
@media (hover: hover) { .n-copy:hover, .fix:hover { background: var(--row-hover); } }
.n-copy:focus-visible, .fix:focus-visible, .check-now:focus-visible { box-shadow: var(--focus-ring); }
.c-details { margin: 10px 0 0 58px; color: var(--ink-2); font-size: 12.5px; line-height: 1.5; }
.c-empty { display: flex; align-items: center; gap: 10px; flex-wrap: wrap; margin: 14px 0 6px; padding-top: 12px; border-top: 1px solid var(--line); color: var(--ink-2); font-size: 13px; }

/* ---------- Its accounts ---------- */
.accts { margin-top: 14px; }
.acct { display: grid; grid-template-columns: minmax(0, 280px) 210px minmax(0, 1fr) 36px; align-items: start; gap: 20px; padding: 14px 4px; border-top: 1px solid var(--line); min-width: 0; }
.acct-who { display: flex; align-items: center; gap: 12px; min-width: 0; }
.vendor { display: grid; place-items: center; flex: none; width: 36px; height: 36px; border-radius: 11px; background: var(--surface-raised); box-shadow: inset 0 0 0 1px var(--line); color: var(--ink); }
.who-text { min-width: 0; }
.vendor-name { margin: 0; overflow: hidden; color: var(--ink); font: 650 15px/1.3 var(--font); text-overflow: ellipsis; white-space: nowrap; }
.vendor-name .group { color: var(--ink-2); font-weight: 550; }
.identity { margin: 2px 0 0; color: var(--ink-2); font-size: 13px; font-weight: 500; line-height: 1.35; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.acct-ready { display: flex; flex-direction: column; align-items: flex-start; gap: 4px; min-width: 0; }
.fix { min-height: 26px; height: auto; max-width: 100%; padding-block: 5px; margin-left: -4px; color: var(--ink-2); font: 500 12px/1.4 var(--mono); white-space: normal; overflow-wrap: anywhere; text-align: left; }
.acct-cap { min-width: 0; }
.quiet { color: var(--ink-3); font-size: 13px; }
.no-reading { display: inline-flex; align-items: center; gap: 10px; flex-wrap: wrap; }
.check-now { background: color-mix(in srgb, var(--teal) 8%, transparent); color: var(--teal-ink); }
@media (hover: hover) { .check-now:hover:not(:disabled) { background: color-mix(in srgb, var(--teal) 14%, transparent); } }
.check-now:disabled { opacity: .6; cursor: default; }
.meter { display: grid; gap: 4px; min-width: 0; }
.meter-head { display: flex; align-items: baseline; gap: 8px; flex-wrap: wrap; margin: 0; }
.meter-head b { color: var(--ink); font-size: 17px; font-weight: 700; letter-spacing: -.01em; font-variant-numeric: tabular-nums; }
.meter-head span, .meter-foot { color: var(--ink-3); font-size: 12.5px; font-variant-numeric: tabular-nums; }
.meter-foot { margin: 0; }
.row-more { display: flex; justify-content: flex-end; }
.legend-row { display: flex; align-items: center; gap: 8px 18px; flex-wrap: wrap; padding: 12px 4px 4px; border-top: 1px solid var(--line); color: var(--ink-3); font-size: 12px; }
.legend-row .fine { margin-left: auto; }

/* ---------- Popovers ---------- */
.pacing { position: absolute; z-index: 20; display: grid; width: min(460px, calc(100% - 24px)); padding: 6px 16px; overflow: auto; border-radius: 16px; background: var(--surface-raised); border: 1px solid var(--glass-edge); box-shadow: var(--shadow-pop); }
.setting { display: flex; align-items: center; gap: 10px; min-height: 52px; color: var(--ink-2); font-size: 13px; font-weight: 550; }
.setting + .setting { border-top: 1px solid var(--line); }
.setting .lbl { flex: 1; min-width: 0; color: var(--ink); }
.setting.nights .lbl { cursor: pointer; }
.hint { min-width: 52px; color: var(--ink-3); font-size: 12px; font-weight: 450; font-variant-numeric: tabular-nums; white-space: nowrap; }
.hint.custom { color: var(--ink-2); }
.hint.off { opacity: .55; text-decoration: line-through; text-decoration-color: var(--line-2); }
.seg button { min-width: 32px; padding: 0 10px; font-variant-numeric: tabular-nums; }
.seg button[data-v="custom"] { padding: 0 12px; white-space: nowrap; }
.seg button:disabled { cursor: default; }
.seg button:disabled:not([aria-checked="true"]) { opacity: .6; }
.tog { position: relative; display: inline-flex; align-items: center; flex: none; width: 38px; height: 22px; padding: 0; border: 0; border-radius: 999px; background: var(--line-2); box-shadow: inset 0 1px 2px rgba(0, 0, 0, .12); cursor: pointer; }
.tog::after { content: ''; position: absolute; left: 3px; width: 16px; height: 16px; border-radius: 50%; background: #fff; box-shadow: 0 1px 3px rgba(0, 0, 0, .28); transition: transform .18s ease; }
.tog[aria-checked="true"] { background: linear-gradient(180deg, #1a8683, #0e6f6c); }
.tog[aria-checked="true"]::after { transform: translateX(16px); }
.tog:focus-visible { outline: none; box-shadow: var(--focus-ring); }
.tog:disabled { cursor: default; opacity: .6; }
.gear { display: inline-grid; place-items: center; flex: none; width: 30px; height: 30px; padding: 0; border: 0; border-radius: 999px; background: transparent; color: var(--ink-3); }
@media (hover: hover) { .gear:hover:not(:disabled) { background: var(--row-hover); color: var(--teal-ink); } }
.gear[aria-expanded="true"] { background: var(--row-selected); color: var(--teal-ink); }
.gear:disabled { opacity: .45; cursor: default; }
.gear:focus-visible { box-shadow: var(--focus-ring); }
.keep-btn { display: inline-flex; align-items: center; gap: 6px; height: 30px; padding: 0 10px 0 12px; border: 0; border-radius: 999px; background: var(--surface-sunken); box-shadow: inset 0 0 0 1px var(--line); color: var(--ink); font-size: 12.5px; font-weight: 600; font-variant-numeric: tabular-nums; white-space: nowrap; }
.keep-btn svg { color: var(--ink-3); }
@media (hover: hover) { .keep-btn:hover:not(:disabled) { background: var(--row-hover); } }
.keep-btn[aria-expanded="true"] { background: var(--row-selected); box-shadow: inset 0 0 0 1px var(--chip-teal-line); color: var(--teal-ink); }
.keep-btn:disabled { opacity: .6; cursor: default; }
.keep-btn:focus-visible { outline: none; box-shadow: var(--focus-ring); }
@media (prefers-reduced-motion: reduce) { .tog::after { transition: none; } }

.menu { position: absolute; z-index: 20; width: 300px; padding: 6px; border-radius: 12px; background: var(--surface-raised); border: 1px solid var(--glass-edge); box-shadow: var(--shadow-pop); }
.menu > button, .menu > a { display: grid; grid-template-columns: 20px 1fr; gap: 2px 10px; width: 100%; padding: 9px 10px; border: 0; border-radius: 8px; background: transparent; color: var(--ink); text-align: left; text-decoration: none; cursor: pointer; }
.menu > button:hover, .menu > button:focus-visible, .menu > a:hover, .menu > a:focus-visible { background: var(--row-hover); box-shadow: none; outline: none; }
.menu svg { grid-row: span 2; margin-top: 2px; color: var(--ink-2); }
.menu .t { color: var(--ink); font-size: 13.5px; font-weight: 600; }
.menu .d { color: var(--ink-3); font-size: 12px; line-height: 1.4; }
.menu .danger .t, .menu .danger svg { color: var(--danger); }
.menu .sep { display: block; height: 1px; margin: 4px 8px; background: var(--line); }
.menu .hold { display: grid; grid-template-columns: 20px 1fr; gap: 2px 10px; padding: 9px 10px; }
.hold-opts { grid-column: 1 / -1; display: grid; gap: 1px; margin: 6px -4px 0 26px; }
.hold-opts button { display: flex; align-items: center; justify-content: space-between; gap: 10px; min-height: 32px; padding: 0 10px; border: 0; border-radius: 8px; background: transparent; color: var(--ink); font-size: 13px; font-weight: 550; cursor: pointer; }
.hold-opts button:hover, .hold-opts button:focus-visible { background: var(--row-hover); outline: none; }
.hold-opts .hint { min-width: 0; }
.sheet-host { position: fixed; inset: 0; z-index: 80; }
.scrim { position: absolute; inset: 0; background: var(--scrim); }

/* The panel's own width: the session panel narrows the page without crossing a viewport breakpoint. */
@container ac (max-width: 900px) {
  .acct { grid-template-columns: minmax(0, 1fr) auto 36px; grid-template-areas: "who ready more" "cap cap cap"; gap: 10px 14px; }
  .acct-who { grid-area: who; }
  .acct-ready { grid-area: ready; align-items: flex-end; }
  /* The auto column grows leftward with its status; anchor the action at its right edge. */
  .readiness-actions { align-self: flex-end; align-items: flex-end; }
  .acct-cap { grid-area: cap; padding-left: 48px; }
  .row-more { grid-area: more; }
}
/* Phone: the approved stacked rows: identity and menu, the state, the capacity. */
@media (max-width: 720px) {
  .ac-actions { width: 100%; margin-left: 0; }
  .ghost-link .long, .wide-only { display: none; }
  .ghost-link.manage { width: 44px; padding: 0; justify-content: center; }
  .ghost-link { padding: 0 10px; }
  .pacing-btn { flex: 0 1 auto; }
  .pacing-btn, .ghost-link { height: 44px; }
  .computer { padding: 16px 16px 6px; }
  .c-side { width: 100%; margin-left: 58px; }
  .notice { margin-top: 12px; }
  .acct { grid-template-columns: minmax(0, 1fr) 40px; grid-template-areas: "who more" "ready ready" "cap cap"; gap: 10px; padding: 14px 0; }
  .acct-ready { align-items: flex-start; }
  .readiness-actions { align-self: flex-start; align-items: flex-start; }
  .acct-cap { padding-left: 0; }
  .more { width: 44px; height: 44px; }
  .legend-row .fine { width: 100%; margin-left: 0; }
  .plan-card p { flex-basis: 100%; }
  .pc-actions { display: grid; grid-template-columns: 1fr 1fr; width: 100%; margin: 0; }
  .pc-actions .btn { min-height: 44px; }
  .setting { flex-wrap: wrap; padding: 8px 0; }
  .setting .seg button { height: 38px; min-width: 44px; }
  .gear { width: 44px; height: 44px; }
  .menu { width: min(300px, calc(100% - 24px)); }
  .menu > button { min-height: 52px; }
  .hold-opts button { min-height: 44px; }
}
</style>
