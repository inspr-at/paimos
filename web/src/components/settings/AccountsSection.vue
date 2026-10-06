<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { api } from '../../lib/api'
import { accountName } from '../../lib/accountCascade'
import { approveAccountCapacity, type AgentAccount } from '../../lib/agents'
import { confirmAction } from '../../lib/confirm'
import { can, myPermissions } from '../../lib/authz'
import { overviewAccounts, type OverviewAccount, type SignInReference } from '../../lib/accountsOverview'
import { computerRemoval, describeEnrollmentStatus, describeComputerStatus, disconnectComputer, disconnectEnrollment, pairingPermissions, platformCaption, removeComputer, type PairingView } from '../../lib/agentPairing'
import { machinesForAdd } from '../../lib/addAccount'
import { hostCapacityReason } from '../../lib/hostCapacity'
import { daysSummary, holdOptions, ownOverride, overrideDone, poolOfRow, reserveLevel, timeLabel, when, type CapacityWindow, type HoldOption, type Override, type PoolView } from '../../lib/capacity'
import { toast } from '../../lib/toast'
import { usePoller } from '../../lib/usePolledData'
import { useAgents } from '../../stores/agents'
import { useCapacity } from '../../stores/capacity'
import { useSession } from '../../stores/session'
import { vClipTip } from '../../directives/clipTip'
import AppIcon from '../AppIcon.vue'
import { settingsNeeds } from '../../lib/footerProviders'
import AccountsCard from '../agents/AccountsCard.vue'
import HarnessMark from '../agents/HarnessMark.vue'
import AddAccountPanel from './AddAccountPanel.vue'
import HostCapacitySettings from './HostCapacitySettings.vue'
import PacingSettings from './PacingSettings.vue'
import NeedsYouList, { type NeedsYouItem } from './NeedsYouList.vue'
import QuotaWarningCard from './QuotaWarningCard.vue'
import SettingsDockedPanel from './SettingsDockedPanel.vue'
import SettingsPopover, { type SettingsMenuItem } from './SettingsPopover.vue'

const agents = useAgents(), capacity = useCapacity(), session = useSession(), route = useRoute(), router = useRouter()
const owner = computed(() => `${session.identity?.tenant.id ?? ''}/${session.identity?.principal.id ?? ''}`)
const manage = computed(() => session.identity?.principal.kind === 'person' && can('account.manage'))
const permissions = computed(() => pairingPermissions({ permissions: [...myPermissions()], principalKind: session.identity?.principal.kind }))
const computers = computed(() => capacity.computers.filter(c => c.computer_id && !c.archived_at))
const accounts = computed(() => overviewAccounts(agents.accounts, capacity.rows, computers.value))
const machines = computed(() => machinesForAdd(computers.value))
const add = ref(false), open = ref(false), opener = ref<HTMLElement | null>(null)
const selected = ref<{ kind: 'account' | 'computer'; id: string } | null>(null), focusSignIn = ref('')
const computer = computed(() => selected.value?.kind === 'computer' ? computers.value.find(c => c.computer_id === selected.value?.id) : undefined)
// An account is selected by one of its records, so the panel follows that
// login when confirming or stopping a shared quota regroups it.
function accountOf(id: string) { return accounts.value.find(a => a.id === id) ?? accounts.value.find(a => a.records.some(r => r.id === id)) }
const account = computed(() => selected.value?.kind === 'account' ? accountOf(selected.value.id) : undefined)
const selectedKey = computed(() => `${owner.value}/${selected.value?.kind}/${selected.value?.id}`)
const title = computed(() => computer.value?.computer_name ?? account.value?.vendor ?? 'Details')
const fact = computed(() => computer.value ? `${computerState(computer.value)} · ${platformCaption(computer.value.platform, computer.value.arch)}` : account.value?.identity)
const thresholds = ref({ early_percent: 10, urgent_percent: 3 })
const busy = ref(false)
const root = ref<HTMLElement>()
let generation = 0
function reset() { generation++; selected.value = null; open.value = false; add.value = false; menuOpen.value = false; renameOpen.value = false; busy.value = false; focusSignIn.value = ''; thresholds.value = { early_percent: 10, urgent_percent: 3 } }
watch(owner, reset)
watch(manage, allowed => { if (!allowed) { menuOpen.value = false; add.value = false } })
watch(selectedKey, () => { menuOpen.value = false; renameOpen.value = false; busy.value = false })
watch([computer, account], () => { if (selected.value && !computer.value && !account.value) open.value = false })
function computerState(c: PairingView) { return c.computer_state === 'revoked' ? `Removed${c.local_cleanup === 'pending' ? ' · cleanup pending' : ''}` : describeComputerStatus(c).stateLabel }
function status(c: PairingView, id: string) {
  const e = c.enrollments.find(e => e.account_id === id)
  if (!e) return ''
  if (c.computer_state === 'revoked' || e.state === 'revoked') return 'Blocked'
  if (c.computer_state === 'draining' || e.state === 'draining') return 'Draining'
  return describeEnrollmentStatus(c, e) || 'Not reported'
}
function references(a: OverviewAccount, c: PairingView) { return a.signins.filter(s => s.computer.computer_id === c.computer_id) }
function mayVerify(s: SignInReference) { return manage.value && s.enrollment.can_verify === true && s.computer.computer_state === 'connected' && s.enrollment.state === 'connected' && s.computer.verification_capabilities?.[s.enrollment.harness]?.supported === true }
function verificationBusy(s: SignInReference) { return ['queued', 'starting', 'running', 'waiting', 'ownership_lost'].includes(s.enrollment.verification_state ?? '') }
const expired = (s: SignInReference) => s.enrollment.verification_state === 'expired' && !s.enrollment.verification_expired_ready
const problemSignins = computed(() => accounts.value.flatMap(a => a.signins.filter(s => s.computer.computer_state === 'connected' && s.enrollment.state === 'connected' && (expired(s) || /failed|sign in|unavailable/i.test(status(s.computer, s.enrollment.account_id)))).map(s => ({ account: a, signin: s }))))
const attention = computed<NeedsYouItem[]>(() => {
  const items: NeedsYouItem[] = problemSignins.value.map(({ account: a, signin: s }) => ({ id: `verify:${s.enrollment.account_id}`, name: `${a.vendor} needs verifying on ${s.computer.computer_name}`, detail: expired(s) ? 'Verification expired. New agents wait until the sign-in passes again.' : status(s.computer, s.enrollment.account_id), count: 1, icon: 'alert', action: { label: manage.value && mayVerify(s) ? 'Verify again' : 'Details', fixesProblem: manage.value && mayVerify(s), disabled: verificationBusy(s) || busy.value } }))
  for (const c of computers.value.filter(c => c.computer_state === 'revoked' && c.local_cleanup === 'pending')) items.push({ id: `cleanup:${c.computer_id}`, name: `${c.computer_name} removed · cleanup pending`, detail: 'New work is blocked. Local sign-ins are deleted when the computer comes back online.', count: 1, icon: 'shield', tone: 'waiting', action: { label: manage.value ? 'Finish cleanup…' : 'Details' } })
  for (const a of accounts.value) {
    const low = a.windows.find(w => w.freshness === 'fresh' && w.remaining_percent <= thresholds.value.early_percent)
    if (low && !items.some(i => a.records.some(r => i.id === `verify:${r.id}`))) items.push({ id: `quota:${a.id}`, name: `${a.vendor} quota is low`, detail: `${Math.round(low.remaining_percent)}% left · ${windowLabel(low)}`, count: 1, icon: 'gauge', action: { label: 'Details' } })
  }
  return items
})
// The footer on Settings says how many of these need you while this section is open (AEON-785).
watch(() => attention.value.reduce((total, item) => total + Math.max(0, item.count), 0), total => { settingsNeeds.value = total }, { immediate: true })
function windowLabel(w: CapacityWindow) { return w.reading.window_kind === '5h' ? '5-hour window' : w.reading.window_kind === 'weekly' ? 'Weekly' : w.reading.window_kind === 'monthly' ? 'Monthly allowance' : w.reading.bucket || 'Quota window' }
function accountSummary(a: OverviewAccount) {
  const problem = problemSignins.value.find(p => p.account.id === a.id)
  if (problem) return `Needs verifying on ${problem.signin.computer.computer_name}`
  const w = a.windows[0]
  return w ? `${Math.round(w.remaining_percent)}% left · ${windowLabel(w).toLowerCase()}${w.freshness !== 'fresh' ? ' · may be out of date' : ''}` : 'Quota not reported'
}
function computerSummary(c: PairingView) {
  if (c.computer_state === 'revoked') return computerState(c)
  if (c.computer_state === 'draining') return 'New starts blocked · running agents finish'
  const p = problemSignins.value.find(p => p.signin.computer.computer_id === c.computer_id)
  return p ? `${p.account.vendor} needs verifying` : c.host_capacity?.reason ? `Waiting: ${hostCapacityReason(c.host_capacity.reason)}` : 'No sign-in needs attention'
}
async function show(kind: 'account' | 'computer', id: string, event?: Event, signin?: string) {
  if (event?.currentTarget instanceof HTMLElement && !open.value) opener.value = event.currentTarget
  selected.value = { kind, id: kind === 'account' ? accountOf(id)?.records[0]?.id ?? id : id }; focusSignIn.value = signin ?? ''; open.value = true
  await nextTick()
  if (signin) { const target = root.value?.querySelector<HTMLElement>(`[data-signin="${signin}"]`); target?.focus({ preventScroll: true }); target?.scrollIntoView({ block: 'nearest' }) }
}
async function actAttention(id: string) {
  const p = problemSignins.value.find(p => id === `verify:${p.signin.enrollment.account_id}`)
  if (p) { if (mayVerify(p.signin)) await verify(p.signin); else await show('computer', p.signin.computer.computer_id!, undefined, p.signin.enrollment.account_id); return }
  if (id.startsWith('cleanup:')) { await show('computer', id.slice(8)); return }
  if (id.startsWith('quota:')) await show('account', id.slice(6))
}
async function refresh() { await agents.refreshAccounts(); await capacity.load(); const result = await capacity.refreshComputers(); if (!result.ok && capacity.computersLoaded) toast('Computer status could not refresh. The previous report stays visible.', { tone: 'error' }) }
const poller = usePoller(refresh, 20_000)
// Returning to the window reads again at once (an account added in a terminal
// shows up); the poller coalesces it with a read already in flight.
function onFocus() { poller.tick(true) }
onMounted(() => { poller.start(true); window.addEventListener('focus', onFocus) })
onBeforeUnmount(() => { generation++; poller.stop(); window.removeEventListener('focus', onFocus); settingsNeeds.value = 0 })
watch(() => route.hash, async hash => {
  if (hash === '#add-account' && manage.value) { add.value = true; void router.replace({ path: route.path, query: route.query, hash: '' }) }
  // The Agents page's pacing line links here (AEON-721).
  if (hash === '#capacity-and-load') { await nextTick(); pacing.value?.reveal(); void router.replace({ path: route.path, query: route.query, hash: '' }) }
}, { immediate: true })
watch(() => route.query.verify_account, id => {
  if (typeof id !== 'string') return
  const s = accounts.value.flatMap(a => a.signins).find(s => s.enrollment.account_id === id)
  if (s) void show('computer', s.computer.computer_id!, undefined, id)
}, { immediate: true })
watch(accounts, () => { const id = route.query.verify_account; if (!open.value && typeof id === 'string') { const s = accounts.value.flatMap(a => a.signins).find(s => s.enrollment.account_id === id); if (s) void show('computer', s.computer.computer_id!, undefined, id) } })
async function verify(s: SignInReference) {
  if (!mayVerify(s) || verificationBusy(s) || busy.value) return
  const identity = owner.value, turn = generation, screen = selectedKey.value, c = s.computer, e = s.enrollment, revision = c.revision, prior = e.verification_run_id
  busy.value = true
  try {
    const response = await api(`/agent-pairing/computers/${c.computer_id}/enrollments/${e.account_id}/verify`, { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ expected_revision: revision, expected_verification_run_id: prior }) })
    if (!response.ok) throw new Error(response.status === 409 ? 'Verification changed. Refresh before verifying again.' : 'Verification could not be requested.')
    const result = await response.json()
    if (identity !== owner.value || turn !== generation || screen !== selectedKey.value) return
    if (result.account_id !== e.account_id || !result.run_id) throw new Error('Verification could not be confirmed.')
    toast(`Verification requested on ${c.computer_name}.`)
    await refresh()
  } catch (error) { if (identity === owner.value && turn === generation) toast(error instanceof Error ? error.message : 'Verification failed.', { tone: 'error' }) }
  finally { if (identity === owner.value && turn === generation) busy.value = false }
}

// Shared popover binds every confirmation to its original record and revision.
type MenuTarget = { kind: 'computer'; computer: PairingView } | { kind: 'signin'; signin: SignInReference } | { kind: 'account'; account: OverviewAccount } | { kind: 'cleanup'; computer: PairingView }
function cleanupRemoval(c: PairingView) {
  const removal = computerRemoval(c, permissions.value)
  if (!removal.allowed) return removal
  if (c.accounting_state !== 'settled' || c.enrollments.some(e => e.accounting_state !== 'settled')) return { allowed: false, reason: 'Run accounting is not confirmed yet.' }
  return removal
}
const renameOpen = ref(false), renameName = ref('')
const menuOpen = ref(false), menuAnchor = ref<HTMLElement | null>(null), menuTarget = ref<MenuTarget | null>(null), menuItems = ref<SettingsMenuItem[]>([]), menuContext = ref(''), menuError = ref('')
function currentTarget(t: MenuTarget): MenuTarget | undefined {
  if (t.kind === 'account') { const account = accounts.value.find(a => a.id === t.account.id); return account && { kind: 'account', account } }
  const computer = computers.value.find(c => c.computer_id === (t.kind === 'signin' ? t.signin.computer.computer_id : t.computer.computer_id))
  if (!computer) return undefined
  if (t.kind !== 'signin') return { kind: t.kind, computer }
  const enrollment = computer.enrollments.find(e => e.account_id === t.signin.enrollment.account_id)
  return enrollment && { kind: 'signin', signin: { computer, enrollment } }
}
function menuKey(t: MenuTarget) { return `${selectedKey.value}/${t.kind}/${t.kind === 'account' ? `${t.account.records.map(r => `${r.id}:${r.link_revision ?? ''}`).join(',')}|${paceKey(poolOf(t.account))}|${t.account.signins.map(s => `${s.computer.computer_id}:${s.computer.revision}:${s.enrollment.account_id}`).join(',')}` : t.kind === 'signin' ? `${t.signin.computer.computer_id}:${t.signin.computer.revision}:${t.signin.enrollment.account_id}` : `${t.computer.computer_id}:${t.computer.revision}`}` }
async function openMenu(t: MenuTarget, event: Event) {
  if (!manage.value) return
  menuTarget.value = t; menuAnchor.value = event.currentTarget as HTMLElement; menuContext.value = menuKey(t); menuError.value = ''
  if (t.kind === 'signin') {
    const s = t.signin, vendor = accounts.value.find(a => a.records.some(r => r.id === s.enrollment.account_id))?.vendor ?? s.enrollment.harness
    menuItems.value = [{ id: 'verify', label: 'Verify again', detail: `Run a short check on ${s.computer.computer_name}.`, icon: 'refresh', disabled: !mayVerify(s) || verificationBusy(s) }, { id: 'signout', label: 'Sign out here…', detail: `Only on ${s.computer.computer_name}.`, icon: 'logout', confirmation: { title: `Sign out of ${vendor} on ${s.computer.computer_name}?`, effect: 'New agents stop starting with this sign-in. This computer deletes its local sign-in when it receives the request.', keeps: 'Sign-ins on other computers and the vendor account stay untouched. Running work is not reported as stopped.', action: 'Sign out here' } }]
  } else if (t.kind === 'computer') {
    menuItems.value = [{ id: 'rename', label: 'Rename…', icon: 'edit', detail: 'Change the name shown in PAIMOS.' }, { id: 'remove', label: 'Remove computer…', detail: 'Blocks its sign-ins, even while offline.', icon: 'trash', confirmation: { title: `Remove ${t.computer.computer_name}?`, effect: 'New work stops and its sign-ins are blocked immediately. Local deletion stays pending until the computer confirms.', keeps: 'Accounts on other computers keep working. Pair this computer again to use it later.', action: 'Remove computer' } }]
  } else if (t.kind === 'cleanup') {
    menuItems.value = [{ id: 'cleanup', label: 'Finish cleanup…', disabled: !cleanupRemoval(t.computer).allowed, detail: cleanupRemoval(t.computer).reason || 'Use only after vendor sign-out or wiping the computer.', confirmation: { title: `Finish cleanup without ${t.computer.computer_name}?`, effect: 'Do this only after signing out of all devices at the vendors, or wiping the computer. PAIMOS then forgets it from this list.', keeps: 'Accounts on other computers are unaffected. Unsettled run accounting is kept and cannot be bypassed.', action: 'Finish cleanup' } }]
  } else {
    const reader = quotaReader(t.account)
    menuItems.value = [...paceItems(t.account), { id: 'read', label: 'Read quota now', icon: 'refresh', separated: !!menuPool.value, disabled: !reader, detail: reader ? `Ask ${reader.computer.computer_name} for a fresh reading.` : 'Needs a verified sign-in with a quota reader.' }, { id: 'everywhere', label: 'Sign out everywhere…', icon: 'logout', disabled: !t.account.signins.some(s => s.enrollment.state !== 'revoked'), confirmation: { title: `Sign out of ${t.account.vendor} everywhere?`, effect: 'PAIMOS blocks this account on every paired computer at once. Local sign-ins are deleted as the computers reconnect.', keeps: 'Other accounts, the vendor subscription and vendor sessions outside PAIMOS are untouched.', action: 'Sign out everywhere' } }, removeItem(t.account)]
  }
  // Publish the captured record before opening. The shared popover closes on
  // context changes; changing its context and opening in one render would
  // correctly invalidate that newly opened menu.
  const context = menuContext.value
  await nextTick()
  if (manage.value && menuContext.value === context && context === menuKey(t)) menuOpen.value = true
}
function quotaReader(a: OverviewAccount) { return a.signins.find(s => s.enrollment.state === 'connected' && s.computer.computer_state === 'connected' && s.computer.connectivity === 'online' && status(s.computer, s.enrollment.account_id) === 'Ready' && a.records.find(r => r.id === s.enrollment.account_id)?.reading_support !== 'none') }
async function renameComputer() {
  const t = menuTarget.value, screen = selectedKey.value, identity = owner.value
  if (t?.kind !== 'computer' || !manage.value || busy.value || menuContext.value !== menuKey(t)) return
  const name = renameName.value.trim()
  if (!name || name.length > 128) { menuError.value = 'Use a name of 1–128 characters.'; return }
  busy.value = true
  try {
    const response = await api(`/agent-pairing/computers/${t.computer.computer_id}/name`, { method: 'PUT', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ name, expected_revision: t.computer.revision }) })
    if (!response.ok) throw new Error('Computer changed or the name could not be saved. Reopen Rename.')
    if (identity !== owner.value || screen !== selectedKey.value) return
    renameOpen.value = false; toast('Computer name saved.'); await refresh()
  } catch (error) { if (identity === owner.value && screen === selectedKey.value) menuError.value = error instanceof Error ? error.message : 'Name could not be saved.' }
  finally { if (identity === owner.value && screen === selectedKey.value) busy.value = false }
}
async function menuAction(id: string, context: string | undefined) {
  const t = menuTarget.value, identity = owner.value, turn = generation, screen = selectedKey.value
  if (!t || busy.value || !manage.value) return
  // A record that changed since its menu opened (a poll, another tab) is said, never acted on.
  if (context !== menuContext.value || context !== menuKey(t)) { fail('This record changed. Reopen its menu.'); return }
  // The key of the record as it is now, not as captured: a regrouped account
  // (other logins), a newer computer revision or a gone sign-in all differ.
  const current = currentTarget(t)
  if (!current || menuKey(current) !== context) { fail('This record changed. Reopen its menu.'); return }
  if (t.kind === 'account' && (id === 'plan' || id === 'sprint' || id.startsWith('hold:'))) { await pace(t.account, id); return }
  if (t.kind === 'account' && id === 'remove-account') { await removeAccount(t.account); return }
  if (id === 'rename' && t.kind === 'computer') { renameName.value = t.computer.computer_name; await nextTick(); renameOpen.value = true; return }
  if (id === 'verify' && t.kind === 'signin') { menuOpen.value = false; await verify(t.signin); return }
  busy.value = true
  try {
    if (id === 'signout' && t.kind === 'signin') await disconnectEnrollment(t.signin.computer, t.signin.enrollment.account_id, 'revoke_now', permissions.value)
    else if (id === 'remove' && t.kind === 'computer') await disconnectComputer(t.computer, 'revoke_now', permissions.value)
    else if (id === 'cleanup' && t.kind === 'cleanup') await removeComputer(t.computer, permissions.value)
    else if (id === 'everywhere' && t.kind === 'account') {
      const targets = t.account.signins.filter(s => s.enrollment.state !== 'revoked' && s.computer.computer_state !== 'revoked').map(s => ({ computer_id: s.computer.computer_id, account_id: s.enrollment.account_id, expected_revision: s.computer.revision }))
      const response = await api('/agent-pairing/accounts/sign-out', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ targets }) })
      if (!response.ok) throw new Error('Sign-ins changed or sign-out failed. Refresh and review the account again.')
    } else if (id === 'read' && t.kind === 'account') {
      const s = quotaReader(t.account), r = s && t.account.records.find(r => r.id === s.enrollment.account_id)
      if (!r || r.link_revision === undefined) throw new Error('No verified quota reader is available.')
      const response = await api(`/agent-accounts/${r.id}/check`, { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ binding_revision: r.link_revision, idempotency_key: crypto.randomUUID() }) })
      if (!response.ok) throw new Error('Quota reading could not be requested. Try again later.')
    } else return
    if (identity !== owner.value || turn !== generation || screen !== selectedKey.value) return
    menuOpen.value = false
    toast(id === 'read' ? 'Quota reading requested. The computer will report the result.' : id === 'cleanup' ? 'Computer removed from the list. History stays.' : 'New work is blocked. Local cleanup remains pending until confirmed.')
    await refresh()
  } catch (error) { if (identity === owner.value && turn === generation && screen === selectedKey.value) fail(error instanceof Error ? error.message : 'The action failed.') }
  finally { if (identity === owner.value && turn === generation && screen === selectedKey.value) busy.value = false }
}
/** A plain menu item closes before it runs: its failure is a toast, a confirmation's stays in place. */
function fail(message: string) { if (menuOpen.value) menuError.value = message; else toast(message, { tone: 'error' }) }

// ---------- Pacing per account: Back to the plan, Sprint, Hold (AEON-786) ----------
// The same choices and words as the Agents page menu; they act on the pool the
// account paces with (its harness, or its group).
const menuPool = ref<PoolView>(), menuHolds = ref<HoldOption[]>([])
function poolOf(a: OverviewAccount) {
  const row = a.rows[0] ?? (a.records[0] ? { id: a.records[0].id, groupId: a.records[0].group_id ?? '', harness: a.records[0].harness } : undefined)
  return row ? poolOfRow(capacity.pools, row) : undefined
}
const paceKey = (pool: PoolView | undefined) => (pool ? `${pool.id}:${ownOverride(pool)}:${pool.overrideUntil}` : '')
function paceItems(a: OverviewAccount): SettingsMenuItem[] {
  const pool = poolOf(a), own: Override = ownOverride(pool)
  menuPool.value = pool; menuHolds.value = []
  if (!pool) return []
  const items: SettingsMenuItem[] = []
  if (own) items.push({ id: 'plan', label: 'Back to the plan', icon: 'play', detail: `${pool.name} paces by your work week again.` })
  if (own !== 'sprint') items.push({ id: 'sprint', label: `Sprint ${pool.name} until reset`, icon: 'bolt', detail: pool.sprintEnd ? `Agents may use everything left until ${when(pool.sprintEnd, agents.now)}.` : 'Agents may use everything left until the next reset.' })
  if (own !== 'hold') {
    // The times shown are the times saved: the choices are fixed when the menu opens.
    menuHolds.value = holdOptions(capacity.schedule, agents.now)
    items.push({ id: 'hold', label: `Hold ${pool.name}`, icon: 'pause', detail: `Agents leave ${pool.name} alone. Running steps finish.`, options: menuHolds.value.map((h, i) => ({ id: `hold:${i}`, label: h.label, hint: h.hint, name: h.name })) })
  }
  return items
}
async function pace(a: OverviewAccount, id: string) {
  const pool = menuPool.value, identity = owner.value, turn = generation
  if (!pool || paceKey(pool) !== paceKey(poolOf(a)) || busy.value) { fail('This account changed. Reopen its menu.'); return }
  const hold = id.startsWith('hold:') ? menuHolds.value[Number(id.slice(5))] : undefined
  if (id.startsWith('hold:') && !hold) return
  const value: Override = id === 'sprint' ? 'sprint' : hold ? 'hold' : ''
  busy.value = true
  try {
    await capacity.setPoolOverride(pool.id, value, hold?.until)
    if (identity === owner.value && turn === generation) toast(overrideDone(pool, value, hold?.until, agents.now))
  } catch (error) { if (identity === owner.value && turn === generation) toast(error instanceof Error ? error.message : 'That did not save. Please try again.', { tone: 'error' }) }
  finally { if (identity === owner.value && turn === generation) busy.value = false }
}

// ---------- Remove an account (AEON-402, moved here in AEON-786): named, history stays ----------
function removeItem(a: OverviewAccount): SettingsMenuItem {
  const ids = new Set(a.records.map(r => r.id))
  const queued = Object.values(agents.runs).filter(r => r.status === 'queued' && (ids.has(r.account_id ?? '') || ids.has(r.requested_account_id ?? ''))).length
  const hosts = [...new Set(a.signins.filter(s => s.enrollment.state !== 'revoked').map(s => s.computer.computer_name))]
  const effect = [
    'It leaves Accounts and agents stop using it.',
    a.records.length > 1 ? `All ${a.records.length} logins that share this quota are removed.` : '',
    hosts.length ? `Its binding on ${hosts.join(', ')} is disconnected.` : '',
    queued ? `${queued === 1 ? 'One queued run' : `${queued} queued runs`} for it ${queued === 1 ? 'is' : 'are'} cancelled.` : '',
  ].filter(Boolean).join(' ')
  return { id: 'remove-account', label: 'Remove account…', icon: 'trash', separated: true, detail: 'Agents stop using it. Its runs and history stay.', confirmation: { title: `Remove ${a.vendor} · ${a.identity}?`, effect, keeps: 'Its runs and history stay. Other accounts and the vendor subscription are untouched.', action: 'Remove account' } }
}
async function removeAccount(a: OverviewAccount) {
  const identity = owner.value, turn = generation, screen = selectedKey.value
  if (busy.value || !manage.value) return
  busy.value = true
  let removed = 0
  try {
    for (const record of a.records) { await agents.removeAccount(record); removed++ }
    if (identity !== owner.value || turn !== generation) return
    menuOpen.value = false
    toast(`Removed ${a.vendor} · ${a.identity}. Its runs and history stay.`)
  } catch (error) {
    if (identity !== owner.value || turn !== generation) return
    const reason = error instanceof Error && /still working/i.test(error.message) ? `A run is still working on ${a.identity}. Remove it once that run ends.` : error instanceof Error ? error.message : 'The account was not removed. Please try again.'
    // Partial removals are said as such; the list refreshes to what the server has.
    fail(removed ? `Removed ${removed} of ${a.records.length} logins. ${reason}` : reason)
  } finally { if (identity === owner.value && turn === generation && screen === selectedKey.value) busy.value = false }
}

// ---------- The one-time plan card (moved here from the Agents page, AEON-786) ----------
const pacing = ref<InstanceType<typeof PacingSettings>>()
const cardClosed = ref(false), planBusy = ref(false)
watch(owner, () => { cardClosed.value = false; planBusy.value = false })
const planCard = computed(() => {
  if (!manage.value || cardClosed.value || !capacity.loaded || !capacity.schedulesLoaded || !capacity.pools.length || capacity.reserveConfirmed) return null
  const s = capacity.schedule
  const on = s.week.filter(d => d.on)
  const hours = on.length && on.every(d => d.start === on[0].start && d.end === on[0].end) ? `${timeLabel(on[0].start)}–${timeLabel(on[0].end)}` : 'in your hours'
  const level = reserveLevel(s)
  return capacity.hasUserSchedule
    ? { title: 'New:', text: `agents now leave you room while you work, about ${level}% of every limit.`, secondary: 'Turn off', primary: 'Keep' }
    : { title: "Here's the plan.", text: `Agents work alongside you ${daysSummary(s.week)}, ${hours}, pace each account to its reset, and leave you about ${level}% of every limit while you work.`, secondary: 'Change', primary: 'Looks right' }
})
async function confirmPlan(mode: 'auto' | 'off') {
  const identity = owner.value
  if (planBusy.value || !manage.value) return
  planBusy.value = true
  try {
    await capacity.confirmReserve(mode)
    if (identity === owner.value) toast(mode === 'auto' ? 'Saved. Agents leave you room while you work.' : 'Turned off. Agents may use everything.')
  } catch (e) { if (identity === owner.value) toast(e instanceof Error ? e.message : 'That did not save. Please try again.', { tone: 'error' }) }
  finally { if (identity === owner.value) planBusy.value = false }
}
function cardSecondary() {
  if (capacity.hasUserSchedule) { void confirmPlan('off'); return }
  cardClosed.value = true
  pacing.value?.reveal()
}
// Pause, resume and first approval stay with each login (AccountsCard). Resume
// approves ongoing use first, as the Agents desk does.
async function setAccount(record: AgentAccount, state: AgentAccount['state']) {
  const identity = owner.value
  if (!manage.value) return
  if (state === 'draining') {
    const ok = await confirmAction({ title: `Drain ${accountName(record)}?`, body: 'Running work finishes; no new runs start on this account until you resume it.', confirmLabel: 'Drain account' })
    if (!ok || identity !== owner.value || !manage.value) return
  }
  if (state === 'available') await approveAccountCapacity(record.id)
  await agents.setAccount(record, state)
}
const primarySignin = computed(() => computer.value ? problemSignins.value.find(p => p.signin.computer.computer_id === computer.value?.computer_id)?.signin : account.value ? problemSignins.value.find(p => p.account.id === account.value?.id)?.signin : undefined)
const spark = computed(() => {
  const points = computer.value?.host_capacity?.history ?? []
  if (points.length < 2) return ''
  const maximum = Math.max(1, ...points.map(p => p.load), computer.value?.host_capacity?.load_limit ?? 0)
  return points.map((p, i) => `${i ? 'L' : 'M'}${i * 240 / (points.length - 1)} ${48 - p.load / maximum * 44}`).join(' ')
})
const sparkLimit = computed(() => { const v = computer.value?.host_capacity; if (!v?.load_limit) return null; return 48 - v.load_limit / Math.max(1, v.load_limit, ...v.history.map(p => p.load)) * 44 })
function quotaSource(a: OverviewAccount, w: CapacityWindow) { const row = a.rows.find(r => [r.primary, r.five].some(candidate => candidate?.reading.read_at === w.reading.read_at)); return row?.host || 'reported reader' }
</script>

<template>
  <div ref="root" id="agent-accounts" class="accounts-computers">
    <SettingsDockedPanel v-model:open="open" :title="title" :fact="fact" :icon="computer ? 'monitor' : 'gauge'" :opener="opener" :context-key="owner" :record-key="selectedKey" layout-frame-selector=".settings-page">
      <header class="page-head"><h2>Accounts and computers</h2><p>Which vendor accounts your agents use, the computers they run on, and how much each one can still take on.</p></header>
      <div v-if="planCard" class="plan-card" role="region" aria-label="The plan">
        <p><b>{{ planCard.title }}</b> {{ planCard.text }}</p>
        <span class="pc-actions">
          <button class="btn" type="button" :disabled="planBusy" @click="cardSecondary">{{ planCard.secondary }}</button>
          <button class="btn primary" type="button" :disabled="planBusy" @click="confirmPlan('auto')">{{ planCard.primary }}</button>
        </span>
      </div>
      <p v-if="!manage" class="member-note">Only account owners with management permission can act. This view shows the status available to you.</p>
      <div class="loading-state" role="status"><template v-if="agents.accountsState === 'forbidden'">Accounts are visible to workspace admins.</template><template v-else-if="agents.accountsState === 'error' || capacity.computersState === 'error'">Accounts or computers could not be loaded. <button type="button" class="btn sm" @click="refresh">Try again</button></template><template v-else-if="!capacity.loaded">Loading accounts and computers…</template><template v-else-if="capacity.stale || capacity.computersStale">Some readings may be out of date. <button type="button" class="btn sm" @click="refresh">Refresh</button></template></div>
      <NeedsYouList :items="attention" :aside="capacity.loaded && capacity.computersLoaded && !capacity.stale && !capacity.computersStale && accounts.every(a => a.signins.every(s => status(s.computer, s.enrollment.account_id) === 'Ready')) ? 'Everything else is working.' : 'Based on the last available reports.'" @action="actAttention" />
      <section v-if="accounts.length && computers.length" class="zone grid-wrap" aria-labelledby="signin-grid-title">
        <div class="zone-head"><h3 id="signin-grid-title" class="eyebrow">Sign-ins at a glance</h3><p>Each cell is one account on one computer</p></div>
        <div class="matrix-scroll" tabindex="0" aria-label="Sign-ins table; scroll horizontally for more computers"><table class="matrix"><thead><tr><th scope="col" class="corner">Account · quota left</th><th v-for="c in computers" :key="c.computer_id!" scope="col"><button class="mx-head" type="button" @click="show('computer', c.computer_id!, $event)"><AppIcon name="monitor" /><b v-clip-tip>{{ c.computer_name }}</b><small>{{ c.computer_state === 'revoked' ? 'Removed' : c.host_capacity ? `${c.host_capacity.running} agents${c.host_capacity.policy.maximum_agents ? ` of ${c.host_capacity.policy.maximum_agents}` : ''}` : computerState(c) }}</small></button></th></tr></thead><tbody><tr v-for="a in accounts" :key="a.id"><th scope="row"><button class="mx-head" type="button" @click="show('account', a.id, $event)"><HarnessMark :harness="a.harness" /><b>{{ a.vendor }}</b><small>{{ a.windows[0] ? `${Math.round(a.windows[0].remaining_percent)}% · ${windowLabel(a.windows[0])}` : 'Quota not reported' }}</small></button></th><td v-for="c in computers" :key="c.computer_id!"><button v-for="s in references(a, c)" :key="s.enrollment.account_id" class="mx-cell" type="button" :aria-label="`${a.vendor} on ${c.computer_name}: ${status(c, s.enrollment.account_id)}`" @click="show('computer', c.computer_id!, $event, s.enrollment.account_id)"><span class="dot" :class="status(c, s.enrollment.account_id) === 'Ready' ? 'ok' : 'wait'"></span>{{ status(c, s.enrollment.account_id) }}</button><span v-if="!references(a, c).length" class="mx-none"><AppIcon name="minus" /><span class="sr-only">{{ a.vendor }} is not signed in on {{ c.computer_name }}</span></span></td></tr></tbody></table></div>
      </section>
      <section class="zone" aria-labelledby="accounts-list-title"><div class="zone-head"><h3 id="accounts-list-title">Accounts <span class="count">{{ accounts.length }}</span></h3><p>Quota belongs to the account and is shared by its computers</p><button v-if="manage && machines.length" type="button" class="btn sm" :aria-expanded="add" @click="add = !add"><AppIcon name="plus" />Add an account</button></div><AddAccountPanel v-if="add && machines.length" :machines="machines" /><ul class="list"><li v-for="a in accounts" :key="a.id"><button class="list-row" type="button" :data-account="a.records[0]?.id" :data-accounts="a.records.map(r => r.id).join(' ')" :aria-expanded="open && account?.id === a.id" :aria-current="open && account?.id === a.id" @click="show('account', a.id, $event)"><HarnessMark :harness="a.harness" /><span class="lr-who"><b>{{ a.vendor }}</b><small>{{ a.identity }}</small></span><span class="st"><span class="dot" :class="a.signins.some(expired) || a.windows.some(w => w.freshness === 'fresh' && w.remaining_percent <= thresholds.early_percent) ? 'wait' : a.signins.some(s => status(s.computer, s.enrollment.account_id) === 'Ready') ? 'ok' : 'blocked'"></span>{{ accountSummary(a) }}</span><AppIcon name="chevron-right" /></button></li></ul><p v-if="!accounts.length && capacity.loaded" class="empty">No vendor accounts are connected yet.</p><QuotaWarningCard @changed="thresholds = $event" /></section>
      <PacingSettings ref="pacing" :manage="manage" :owner-key="owner" />
      <section class="zone" aria-labelledby="computers-list-title"><div class="zone-head"><h3 id="computers-list-title">Computers <span class="count">{{ computers.length }}</span></h3><p>Sign-ins and capacity live with each computer</p><RouterLink v-if="manage" class="btn sm" to="/agents/register-agent"><AppIcon name="monitor" />Connect a computer</RouterLink></div><ul class="list"><li v-for="c in computers" :key="c.computer_id!"><button class="list-row" type="button" :data-computer="c.computer_id" :aria-expanded="open && computer?.computer_id === c.computer_id" :aria-current="open && computer?.computer_id === c.computer_id" @click="show('computer', c.computer_id!, $event)"><AppIcon name="monitor" /><span class="lr-who"><b v-clip-tip>{{ c.computer_name }}</b><small>{{ computerState(c) }}<template v-if="c.last_seen_at"> · last seen {{ when(c.last_seen_at, agents.now) }}</template></small></span><span class="st"><span class="dot" :class="c.computer_state === 'revoked' ? 'blocked' : c.computer_state === 'connected' && c.connectivity === 'online' && !c.host_capacity?.reason ? 'ok' : 'wait'"></span>{{ computerSummary(c) }}</span><AppIcon name="chevron-right" /></button></li></ul><p v-if="!computers.length && capacity.computersLoaded" class="empty">No paired computers yet.</p><p v-if="computers.length === 100" class="footnote">First 100 computers shown.</p></section>
      <p class="footnote"><AppIcon name="shield" /><span>Every sign-in stays on its computer. PAIMOS stores status and timestamps, never the vendor credential.</span></p>
      <template #overflow><button v-if="manage && (computer || account)" class="icon-btn" type="button" :aria-label="`More actions for ${title}`" @click="openMenu(computer ? { kind: 'computer', computer } : { kind: 'account', account: account! }, $event)"><AppIcon name="more" /></button></template>
      <template #panel>
        <template v-if="computer">
          <div v-if="primarySignin" class="needs"><AppIcon name="alert" /><strong>{{ primarySignin.enrollment.harness }} needs verifying here</strong><p>Other ready sign-ins keep working on this computer.</p></div>
          <div v-if="computer.computer_state === 'revoked'" class="needs"><AppIcon name="shield" /><strong>Removed · {{ computer.local_cleanup === 'pending' ? 'cleanup pending' : 'cleanup confirmed' }}</strong><p>New work is blocked. Local deletion and open run accounting remain separate.</p></div>
          <section v-if="computer.computer_state !== 'revoked'" class="p-sec"><div class="p-head"><h3>Right now</h3><span>{{ computer.host_capacity?.reported_at ? `Reported ${when(computer.host_capacity.reported_at, agents.now)}` : 'No load report yet' }}</span></div><p class="now-line">{{ computer.computer_state === 'draining' ? 'Draining · new starts blocked' : computer.host_capacity?.reason ? `Busy · new starts are waiting: ${hostCapacityReason(computer.host_capacity.reason)}` : computer.host_capacity?.signals ? 'New starts allowed' : 'Host capacity not reported' }}</p><p class="now-copy">Running agents keep working. Only new starts wait for room.</p><figure class="spark"><svg v-if="spark" viewBox="0 0 240 48" preserveAspectRatio="none" role="img" aria-label="Reported one-minute host load over the last hour"><path :d="spark" /><line v-if="sparkLimit !== null" x1="0" x2="240" :y1="sparkLimit" :y2="sparkLimit" /></svg><p v-else>Load history not available yet</p><figcaption>One-minute load, last hour<template v-if="sparkLimit !== null"> · dashed line: limit {{ computer.host_capacity?.load_limit.toFixed(1) }}</template></figcaption></figure><dl class="facts"><div><dt>Agents</dt><dd>{{ computer.host_capacity?.running ?? 'Unknown' }}<small v-if="computer.host_capacity?.policy.maximum_agents"> of {{ computer.host_capacity.policy.maximum_agents }}</small></dd><p>{{ computer.host_capacity?.queued ?? 'Unknown' }} queued</p></div><div><dt>Load</dt><dd>{{ computer.host_capacity?.signals?.load?.toFixed(1) ?? 'Unknown' }}</dd><p>{{ computer.host_capacity?.signals?.cores ?? 'Unknown' }} cores</p></div><div><dt>Memory</dt><dd>{{ computer.host_capacity?.signals?.memory_used_gb?.toFixed(1) ?? 'Unknown' }}<small v-if="computer.host_capacity?.signals?.memory_total_gb"> / {{ computer.host_capacity.signals.memory_total_gb.toFixed(0) }} GB</small></dd><p>Pressure {{ computer.host_capacity?.signals?.memory_pressure ?? 'unknown' }}</p></div><div><dt>Power</dt><dd>{{ computer.host_capacity?.signals?.power === 'plugged_in' ? 'Plugged in' : computer.host_capacity?.signals?.power === 'battery' ? 'Battery' : 'Unknown' }}</dd><p>Temperature {{ computer.host_capacity?.signals?.thermal ?? 'unknown' }}</p></div></dl></section>
          <section v-else class="p-sec"><h3>Cleanup</h3><ul class="steps"><li><AppIcon name="check" />New work blocked</li><li><AppIcon :name="computer.local_cleanup === 'confirmed' ? 'check' : 'clock'" />Local sign-in deletion {{ computer.local_cleanup }}</li><li><AppIcon :name="computer.accounting_state === 'settled' ? 'check' : 'clock'" />Run accounting {{ computer.accounting_state ?? 'not reported' }}</li></ul><h4>If it is lost or stolen</h4><p>Sign out of all devices at each vendor, or wipe the computer, before finishing cleanup.</p><ul class="vendor-steps"><li v-for="e in computer.enrollments" :key="e.account_id"><HarnessMark :harness="e.harness" /><span>{{ accounts.find(a => a.records.some(r => r.id === e.account_id))?.vendor ?? e.harness }} · {{ e.label }}</span></li></ul></section>
          <section class="p-sec"><div class="p-head"><h3>Sign-ins on this computer</h3><span>Quota is shared per account</span></div><div v-for="e in computer.enrollments" :key="e.account_id" class="si-row" :class="{ 'is-target': focusSignIn === e.account_id }" :data-signin="e.account_id" tabindex="-1"><HarnessMark :harness="e.harness" /><div class="si-who"><button type="button" class="name-link" @click="show('account', accounts.find(a => a.records.some(r => r.id === e.account_id))?.id ?? e.account_id)">{{ accounts.find(a => a.records.some(r => r.id === e.account_id))?.vendor ?? e.harness }}</button><small>{{ e.label }}</small></div><span class="st">{{ status(computer, e.account_id) }}</span><button v-if="manage && e.can_verify && e.state !== 'revoked'" type="button" class="icon-btn" :aria-label="`More for ${e.harness} on ${computer.computer_name}`" @click="openMenu({ kind: 'signin', signin: { computer, enrollment: e } }, $event)"><AppIcon name="more" /></button><p class="si-meta"><template v-if="e.verified_at">Verified {{ when(e.verified_at, agents.now) }}</template><template v-else>Verification: {{ e.verification_state ?? 'not reported' }}</template><template v-if="e.verification_expires_at && e.verification_state !== 'not_selected'"> · expires {{ when(e.verification_expires_at, agents.now) }}</template><template v-if="e.last_used_at"> · used {{ when(e.last_used_at, agents.now) }}</template> · local cleanup {{ e.local_cleanup }}<template v-if="e.active_run_ids.length"> · {{ e.active_run_ids.length }} running</template></p></div></section>
          <HostCapacitySettings :computer="computer" :manage="manage && computer.enrollments.some(e => e.can_verify)" :owner-key="owner" @saved="refresh" />
        </template>
        <template v-else-if="account">
          <div v-if="primarySignin" class="needs"><AppIcon name="alert" /><strong>Needs verifying on {{ primarySignin.computer.computer_name }}</strong><p>New starts with this sign-in wait until verification passes.</p></div>
          <section class="p-sec"><div class="p-head"><h3>Shared quota</h3><span>Shared by {{ new Set(account.signins.map(s => s.computer.computer_id)).size }} computers</span></div><div class="quota"><div v-for="w in account.windows" :key="w.reading.window_kind + w.reading.bucket" class="q-row"><span>{{ windowLabel(w) }}</span><b>{{ Math.round(w.remaining_percent) }}% left</b><div class="gauge" role="meter" :aria-label="`${account.vendor} ${windowLabel(w)} remaining`" aria-valuemin="0" aria-valuemax="100" :aria-valuenow="w.remaining_percent"><i :style="{ width: `${w.remaining_percent}%` }"></i></div><small>Resets {{ when(w.reading.resets_at, agents.now) }}</small><small :class="{ stale: w.freshness !== 'fresh' }">Read {{ when(w.reading.read_at, agents.now) }} on {{ quotaSource(account, w) }}<template v-if="w.freshness !== 'fresh'"> · may be out of date</template></small></div><p v-if="!account.windows.length">Quota has not been reported. No remaining allowance is inferred.</p></div><p class="q-note">Every computer signed in with this account draws from the same quota.</p></section>
          <section class="p-sec"><div class="p-head"><h3>Signed in on</h3><span>Details and actions live with each computer</span></div><button v-for="s in account.signins" :key="`${s.computer.computer_id}:${s.enrollment.account_id}`" type="button" class="ref-row" @click="show('computer', s.computer.computer_id!, undefined, s.enrollment.account_id)"><AppIcon name="monitor" /><span><b>{{ s.computer.computer_name }}</b><small>{{ computerState(s.computer) }}</small></span><span class="st">{{ status(s.computer, s.enrollment.account_id) }}</span><AppIcon name="chevron-right" /></button><p v-if="!account.signins.length">No paired computer is associated with this account.</p></section>
          <section class="p-sec use-sec"><div class="p-head"><h3>Use and limits</h3><span>{{ account.records.length === 1 ? 'One login' : `${account.records.length} logins share this quota` }}</span></div><AccountsCard :accounts="account.records" :all="agents.accounts" state="ready" :now="agents.now" :admin="manage" :set="setAccount" /></section>
          <RouterLink class="models-link" to="/agents/models" aria-label="Agents, Models">Agents<AppIcon name="chevron-right" :size="12" />Models</RouterLink>
        </template>
      </template>
      <template v-if="manage && (primarySignin || computer?.computer_state === 'revoked')" #footer><template v-if="primarySignin"><p>{{ primarySignin.enrollment.harness }} on {{ primarySignin.computer.computer_name }}</p><button type="button" class="btn primary" :disabled="busy || !mayVerify(primarySignin) || verificationBusy(primarySignin)" @click="verify(primarySignin)">Verify again</button></template><template v-else-if="computer"><p>{{ cleanupRemoval(computer).reason || 'Only needed if it won’t come back' }}</p><button type="button" class="btn primary" :disabled="!cleanupRemoval(computer).allowed" @click="openMenu({ kind: 'cleanup', computer }, $event)">Finish cleanup…</button></template></template>
    </SettingsDockedPanel>
    <SettingsPopover v-model:open="renameOpen" :anchor="menuAnchor" label="Rename computer" mode="form" :context-key="selectedKey" :busy="busy" :error="menuError" @submit="renameComputer"><label class="rename-field">Name<input v-model="renameName" maxlength="128" :disabled="busy" /></label></SettingsPopover>
    <SettingsPopover v-model:open="menuOpen" :anchor="menuAnchor" :label="`Actions for ${title}`" :items="menuItems" :context-key="menuContext" :busy="busy" :error="menuError" @select="menuAction" />
  </div>
</template>

<style scoped>
.rename-field { display: grid; gap: 8px; font-size: 13px; }.rename-field input { min-width: 0; min-height: 36px; padding: 4px 8px; background: var(--field-bg); color: var(--ink); border: 1px solid var(--line-2); border-radius: var(--radius-s); }.accounts-computers { min-width: 0; }.page-head h2 { font-size: 26px; font-weight: 500; letter-spacing: -.025em; margin: 0 0 8px; }.page-head p { color: var(--ink-2); font-size: 13.5px; max-width: 66ch; }.member-note { margin: 14px 0; color: var(--ink-3); font-size: 12.5px; }.loading-state { min-height: 34px; font-size: 13px; color: var(--ink-3); }.zone { margin-top: 30px; }.zone-head { display: flex; align-items: baseline; justify-content: space-between; flex-wrap: wrap; gap: 4px 16px; margin-bottom: 10px; }.zone-head h3 { font-size: 18px; font-weight: 600; }.zone-head p, .count { font-size: 12px; color: var(--ink-3); }.count { margin-left: 6px; }.eyebrow { font: 500 11px var(--mono); text-transform: uppercase; letter-spacing: .1em; }.matrix-scroll { max-width: 100%; overflow-x: auto; }.matrix { width: 100%; border-collapse: collapse; table-layout: fixed; min-width: 320px; }.matrix th, .matrix td { padding: 2px 0; border-bottom: 1px solid var(--line); text-align: left; font-weight: inherit; vertical-align: middle; }.matrix .corner { width: 38%; font: 500 11px var(--mono); color: var(--ink-3); }.mx-head { display: grid; grid-template-columns: auto minmax(0,1fr); align-items: center; gap: 0 10px; width: 100%; min-height: 54px; padding: 6px 10px; border: 0; background: transparent; text-align: left; color: var(--ink); border-radius: var(--radius-row); }.mx-head > svg, .mx-head > :deep(.harness-mark) { grid-row: span 2; }.mx-head b { font-size: 13.5px; min-width: 0; overflow-wrap: anywhere; }.mx-head small { grid-column: 2; font-size: 12px; color: var(--ink-3); overflow-wrap: anywhere; }.mx-cell, .mx-none { display: flex; align-items: center; gap: 8px; width: 100%; min-height: 46px; padding: 6px 10px; border: 0; background: transparent; text-align: left; font-size: 12px; color: var(--ink-2); border-radius: var(--radius-row); }.mx-cell:hover,.mx-head:hover,.list-row:hover,.ref-row:hover { background: var(--row-hover); }.list { margin: 0; padding: 0; list-style: none; border-top: 1px solid var(--line); }.list-row { display: grid; grid-template-columns: 36px minmax(0,1fr) minmax(0,1fr) 16px; align-items: center; gap: 2px 14px; width: 100%; min-height: 64px; padding: 10px; border: 0; border-bottom: 1px solid var(--line); background: transparent; text-align: left; color: var(--ink); }.list-row[aria-current=true] { background: var(--row-selected); }.lr-who { display: grid; min-width: 0; }.lr-who b { font-size: 14px; font-weight: 650; }.lr-who small { color: var(--ink-2); font-size: 12.5px; overflow-wrap: anywhere; }.st { display: inline-flex; align-items: center; gap: 8px; font-size: 12.5px; color: var(--ink-2); }.dot { width: 8px; height: 8px; border-radius: 50%; flex: none; background: var(--track); }.dot.ok { background: var(--ok); }.dot.wait { box-shadow: inset 0 0 0 1.8px var(--gold); }.dot.blocked { box-shadow: inset 0 0 0 1.8px var(--ink-3); }.footnote { display: flex; gap: 8px; margin-top: 22px; font-size: 12.5px; color: var(--ink-3); }.footnote svg { flex: none; }.empty { color: var(--ink-3); font-size: 13px; padding: 14px 0; }.p-sec { padding: 18px 0 20px; border-top: 1px solid var(--line); }.p-sec:first-child { padding-top: 0; border: 0; }.p-head { display: flex; align-items: baseline; justify-content: space-between; flex-wrap: wrap; gap: 4px 12px; margin-bottom: 12px; }.p-head h3 { font-size: 14px; }.p-head span { color: var(--ink-3); font-size: 12px; }.now-line { font-size: 14px; font-weight: 600; }.now-copy,.q-note { margin-top: 6px; font-size: 12.5px; color: var(--ink-3); }.spark { margin: 14px 0 0; }.spark svg { width: 100%; height: 52px; }.spark path { fill: none; stroke: var(--teal); stroke-width: 1.6; vector-effect: non-scaling-stroke; }.spark line { stroke: var(--warn-ink); stroke-width: 1.2; stroke-dasharray: 4 4; }.spark figcaption,.spark p { font-size: 12px; color: var(--ink-3); }.facts { display: grid; grid-template-columns: repeat(4,minmax(0,1fr)); gap: 14px 12px; margin-top: 16px; }.facts dt { font: 500 10.5px var(--mono); text-transform: uppercase; color: var(--ink-3); }.facts dd { margin: 3px 0 0; font-size: 16px; font-weight: 600; }.facts small { font-size: 12px; font-weight: 400; }.facts p { font-size: 12px; color: var(--ink-3); }.si-row { display: grid; grid-template-columns: 28px minmax(0,1fr) auto 34px; gap: 2px 12px; align-items: center; padding: 10px 0; border-top: 1px solid var(--line); }.si-row.is-target { background: var(--row-selected); border-radius: var(--radius-row); }.si-who { display: grid; min-width: 0; }.si-who small { font-size: 12px; color: var(--ink-2); overflow-wrap: anywhere; }.name-link { padding: 0; border: 0; background: transparent; color: var(--ink); font-weight: 600; text-align: left; }.si-meta { grid-column: 2/5; font-size: 12px; color: var(--ink-3); }.icon-btn { display: inline-grid; place-items: center; width: 34px; height: 34px; border: 0; border-radius: 999px; background: transparent; color: var(--ink-2); flex: none; }.icon-btn:hover { background: var(--row-hover); }.quota { display: grid; gap: 14px; }.q-row { display: grid; grid-template-columns: minmax(0,1fr) auto; gap: 4px 12px; align-items: baseline; }.q-row > span { font-size: 13px; color: var(--ink-2); }.q-row b { font-size: 18px; }.q-row small { grid-column: 1/-1; font-size: 12px; color: var(--ink-3); }.q-row small.stale { color: var(--warn-ink); }.gauge { grid-column: 1/-1; height: 6px; background: var(--track); border-radius: 999px; overflow: hidden; }.gauge i { display: block; height: 100%; background: color-mix(in srgb,var(--teal) 42%,transparent); }.ref-row { display: grid; grid-template-columns: 28px minmax(0,1fr) auto 16px; align-items: center; gap: 12px; width: 100%; min-height: 52px; padding: 6px 0; border: 0; border-top: 1px solid var(--line); background: transparent; color: var(--ink); text-align: left; }.ref-row > span:nth-child(2) { display: grid; }.ref-row small { font-size: 12px; color: var(--ink-3); }.needs { display: grid; grid-template-columns: 18px 1fr; gap: 4px 10px; padding: 12px; margin-bottom: 20px; background: var(--surface-sunken); outline: 1px solid var(--line-2); border-radius: 12px; }.needs p { grid-column: 2; font-size: 12px; color: var(--ink-2); }.steps,.vendor-steps { list-style: none; padding: 0; }.steps li,.vendor-steps li { display: flex; align-items: center; gap: 10px; min-height: 40px; border-top: 1px solid var(--line); font-size: 13px; }.p-sec h4 { margin: 18px 0 6px; }.models-link { display: inline-flex; align-items: center; gap: 8px; color: var(--teal-ink); font-size: 13px; }
.plan-card { display: flex; align-items: center; gap: 12px 18px; flex-wrap: wrap; margin: 18px 0 4px; padding: 12px 14px 12px 16px; border-radius: 14px; background: color-mix(in srgb, var(--teal) 7%, var(--surface-raised)); box-shadow: inset 0 0 0 1px var(--chip-teal-line); }
.plan-card p { flex: 1 1 420px; margin: 0; color: var(--ink-2); font-size: 13.5px; line-height: 1.5; text-wrap: pretty; }
.plan-card b { color: var(--ink); font-weight: 650; }
.pc-actions { display: inline-flex; flex-wrap: wrap; gap: 8px; margin-left: auto; }
@media(max-width:720px) { .plan-card p { flex-basis: 100%; }.pc-actions { display: grid; grid-template-columns: 1fr 1fr; width: 100%; margin: 0; }.pc-actions .btn { min-height: 44px; }.list-row { grid-template-columns: 28px minmax(0,1fr) 16px; gap: 4px 10px; }.list-row > .st { grid-column: 2; }.list-row > svg:last-child { grid-column: 3; grid-row: 1/3; }.facts { grid-template-columns: repeat(2,minmax(0,1fr)); }.matrix .corner { width: 34%; }.mx-head { gap: 6px; padding: 6px; }.mx-cell { gap: 6px; padding: 6px; }.si-row { grid-template-columns: 24px minmax(0,1fr) auto 34px; gap: 4px 8px; } }
@media(pointer:coarse) { .btn,.icon-btn,.name-link { min-height: 44px; }.icon-btn { width: 44px; }.si-row { grid-template-columns: 24px minmax(0,1fr) auto 44px; } }
</style>
