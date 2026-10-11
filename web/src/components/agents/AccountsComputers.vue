<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { useRoute } from 'vue-router'
import { api } from '../../lib/api'
import { can } from '../../lib/authz'
import type { PairingPermissions } from '../../lib/agentPairing'
import { overviewAccounts, type SignInReference } from '../../lib/accountsOverview'
import { DEFAULT_QUOTA_THRESHOLDS, glanceAvailability, glanceChipText, glanceFlag, glanceItems, glanceSummary, glanceTiles, isProblemSignin, onlineComputers, type GlanceAvailability, type GlanceItem, type GlancePart } from '../../lib/accountsGlance'
import { pacingSummary } from '../../lib/computerAccounts'
import { when, whenFull } from '../../lib/capacity'
import { validQuotaThresholds, type QuotaWarningSettings } from '../../lib/quotaWarnings'
import { toast } from '../../lib/toast'
import { initialRefreshStatus, usePoller } from '../../lib/usePolledData'
import { useAgents } from '../../stores/agents'
import { useCapacity } from '../../stores/capacity'
import { useSession } from '../../stores/session'
import AppIcon, { type IconName } from '../AppIcon.vue'
import FloatingPanel from '../work/FloatingPanel.vue'
import VerificationButton from './VerificationButton.vue'
import { signinKey, useVerification } from '../../lib/useVerification'

// Accounts and computers on the Agents page (AEON-782), now the accounts chip in the
// capacity row (AEON-1209, design AEON-1196; the tiles are AEON-1176 P6). The chip names
// what there is and flags what needs a person; its popover keeps the status line, Away
// and Verify again, then three tiles that open Settings › Accounts, Computers and Capacity.
const route = useRoute()
const props = defineProps<{ permissions: PairingPermissions; showAccounts: boolean }>()
const agents = useAgents(), capacity = useCapacity(), session = useSession()
const mayManage = computed(() => session.identity?.principal.kind === 'person' && can('account.manage'))
const identityKey = computed(() => `${session.identity?.tenant.id ?? ''}/${session.identity?.principal.id ?? ''}`)
const now = computed(() => agents.now)

const busy = ref(false)
const chip = ref<HTMLButtonElement>()
const open = ref(false)
function close(restore: boolean) {
  open.value = false
  if (restore) chip.value?.focus({ preventScroll: true })
}
watch(identityKey, () => { open.value = false; linked = '' })

// ---------- What the page knows ----------
const computers = computed(() => props.permissions.canListComputers && capacity.computersState !== 'forbidden' ? capacity.computers.filter(c => c.computer_id && !c.archived_at) : [])
const accounts = computed(() => props.showAccounts && agents.accountsState !== 'forbidden' ? overviewAccounts(agents.accounts, capacity.rows, computers.value) : [])
const thresholds = ref<QuotaWarningSettings>({ ...DEFAULT_QUOTA_THRESHOLDS })
const requested = computed(() => typeof route.query.verify_account === 'string' ? route.query.verify_account : '')
const baseItems = computed(() => glanceItems(accounts.value, computers.value, thresholds.value, now.value, requested.value))
const allSignins = computed(() => accounts.value.flatMap(a => a.signins))
const verification = useVerification(identityKey, allSignins, canVerify, () => capacity.refreshComputers())
const attemptedItems = ref<{ item: GlanceItem; index: number }[]>([])
watch(identityKey, () => { attemptedItems.value = [] })
// A sign-in verified here keeps its row until the person leaves, so Verify again's answer stays where it was asked.
const items = computed(() => {
  const retained = new Map(verification.retained.value.map(s => [signinKey(s), s]))
  const remembered = attemptedItems.value.filter(x => x.item.signin && retained.has(signinKey(x.item.signin)))
  const keys = new Set(remembered.map(x => signinKey(x.item.signin!)))
  const out = baseItems.value.filter(i => !i.signin || !keys.has(signinKey(i.signin)))
  for (const { item, index } of remembered) out.splice(Math.min(index, out.length), 0, { ...item, signin: retained.get(signinKey(item.signin!))! })
  return out
})
const openItems = computed(() => items.value.filter(i => !i.signin || isProblemSignin(i.signin) || i.signin.enrollment.account_id === requested.value))
const summary = computed(() => glanceSummary(accounts.value, computers.value, baseItems.value, now.value))
// Each value names the reads that support it. A successful capacity response cannot
// stand in for a missing accounts/computers response, nor supply an unread schedule.
const sources = computed(() => ({
  accounts: props.showAccounts ? agents.accountsStatus : { ...initialRefreshStatus(), state: 'forbidden' as const, error: 'Owners and admins see accounts here.' },
  capacity: capacity.capacityStatus,
  computers: props.permissions.canListComputers ? capacity.computersStatus : { ...initialRefreshStatus(), state: 'forbidden' as const, error: 'Owners and admins see computers here.' },
  schedules: capacity.schedulesStatus,
}))
const chipState = computed(() => glanceAvailability(Object.values(sources.value), !accounts.value.length && !computers.value.length, now.value))
const tileStates = computed<Record<GlancePart, GlanceAvailability>>(() => ({
  accounts: glanceAvailability(agents.accountsState === 'ready' && !accounts.value.length
    ? [sources.value.accounts] : [sources.value.accounts, sources.value.capacity, sources.value.computers], !accounts.value.length, now.value),
  // This tile claims connectivity; account readiness and verification have their own line below.
  computers: glanceAvailability([sources.value.computers], !computers.value.length, now.value),
  capacity: glanceAvailability([sources.value.schedules], false, now.value),
}))
const hasValues = (state: GlanceAvailability) => ['ready', 'empty', 'stale'].includes(state.state)
const asOf = (state: GlanceAvailability) => state.updatedAt === null ? '' : `as of ${new Date(state.updatedAt).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit', hour12: false })}`
const forbiddenWords = (part: GlancePart) => `Owners and admins see ${part} here`
const online = computed(() => onlineComputers(computers.value))
const signins = computed(() => props.showAccounts ? capacity.signins.length : 0)
const statusText = computed(() => accounts.value.length ? summary.value.text : `No accounts yet · ${online.value} ${online.value === 1 ? 'computer' : 'computers'} online`)
const tone = computed(() => chipState.value.state === 'ready' && accounts.value.length ? summary.value.tone : 'warn')
const chipText = computed(() => chipState.value.state === 'error' ? 'Accounts unreadable'
  : chipState.value.state === 'forbidden' ? 'Accounts'
  : accounts.value.length ? glanceChipText(accounts.value.length, computers.value.length, props.showAccounts) : `No accounts yet${computers.value.length ? ` · ${computers.value.length} computers` : ''}`)
const flag = computed(() => hasValues(chipState.value) ? glanceFlag(signins.value, openItems.value) : '')
const chipTip = computed(() => chipState.value.state === 'error' ? chipState.value.reason
  : chipState.value.state === 'forbidden' ? `${forbiddenWords('accounts')}. ${chipState.value.reason}`
  : chipState.value.state === 'stale' ? `${chipText.value}${flag.value ? ` · ${flag.value}` : ''} · ${asOf(chipState.value)}${chipState.value.reason ? ` · ${chipState.value.reason}` : ''}`
  : 'Accounts, computers and capacity')
const awayWords = computed(() => capacity.away ? `until ${when(capacity.away, now.value)}` : '')
const shownAway = computed(() => hasValues(chipState.value) && capacity.away)
const tiles = computed(() => glanceTiles({
  accounts: accounts.value, computers: computers.value,
  // Only computer-owned updates and cleanup affect the connectivity tile. Account
  // verification/usage claims require the joined sources and appear below instead.
  items: glanceItems([], computers.value, thresholds.value, now.value), summary: summary.value, signins: signins.value,
  pacing: capacity.schedulesLoaded ? pacingSummary(capacity.schedule) : '', away: awayWords.value, showAccounts: true,
}).map(tile => {
  const state = tileStates.value[tile.part]
  return { ...tile, state,
    line: state.state === 'error' ? `Couldn't read ${tile.part}` : state.state === 'forbidden' ? forbiddenWords(tile.part) : tile.line,
    detail: state.state === 'stale' ? asOf(state) : ['error', 'forbidden', 'empty'].includes(state.state) ? '' : tile.detail,
  }
}))
const ICON: Record<GlancePart, IconName> = { accounts: 'key', computers: 'monitor', capacity: 'gauge' }
async function retry(part?: GlancePart) {
  if (part === 'computers') await capacity.refreshComputers()
  else if (part === 'capacity') await capacity.refreshSchedules()
  else if (part === 'accounts') await Promise.all([agents.refreshAccounts(), capacity.refreshCapacity(), capacity.refreshComputers()])
  else await Promise.all([agents.refreshAccounts(), capacity.load()])
}

// An approval link (?verify_account=…) opens the popover once, at the sign-in it names, as soon
// as the chip is there (the first read may still be in flight); closing it is the person's choice.
let linked = ''
watch([requested, chip], ([id, button]) => {
  if (!id || !button || linked === id) return
  linked = id
  open.value = true
}, { immediate: true, flush: 'post' })

// ---------- Low-quota thresholds: read once per person, again when the window returns ----------
let generation = 0
async function readThresholds() {
  const turn = ++generation, owner = identityKey.value
  if (!session.identity || !can('account.read')) { thresholds.value = { ...DEFAULT_QUOTA_THRESHOLDS }; return }
  try {
    const response = await api('/settings/quota-warnings')
    if (!response.ok) return
    const body: QuotaWarningSettings = await response.json()
    if (turn === generation && owner === identityKey.value && validQuotaThresholds(body)) thresholds.value = body
  } catch { /* the defaults keep the early and urgent lines the server starts with */ }
}
watch(identityKey, () => { thresholds.value = { ...DEFAULT_QUOTA_THRESHOLDS }; busy.value = false; void readThresholds() }, { immediate: true })
const poller = usePoller(readThresholds, 120_000)
onMounted(() => { poller.start(false); window.addEventListener('focus', readThresholds) })
onBeforeUnmount(() => { generation++; poller.stop(); window.removeEventListener('focus', readThresholds) })

// ---------- Verify again, bound to the sign-in on screen ----------
function canVerify(s: SignInReference) {
  return chipState.value.state === 'ready' && mayManage.value && s.computer.computer_state === 'connected' && s.enrollment.state === 'connected' && s.enrollment.can_verify === true
    && s.computer.verification_capabilities?.[s.enrollment.harness]?.supported === true
}
const verifyBusy = (s: SignInReference) => verification.pending(s)
async function verifyAgain(item: GlanceItem) {
  if (!item.signin) return
  if (!attemptedItems.value.some(x => x.item.id === item.id)) attemptedItems.value.push({ item, index: items.value.findIndex(i => i.id === item.id) })
  await verification.request(item.signin)
}
// The one-click fix: the first sign-in a person may verify right now.
const firstFix = computed(() => items.value.find(i => i.kind === 'verify' && i.signin && canVerify(i.signin)))
// What the popover leads with: that fix, else the first thing that blocks agents, with its one
// fix command from the computer's own report (AEON-499; never a daemon-supplied command).
const lead = computed(() => chipState.value.state === 'ready' ? firstFix.value ?? openItems.value[0] : undefined)
const leadVerify = computed(() => !!lead.value?.signin && lead.value === firstFix.value)
function detailOf(i: GlanceItem): string {
  const readiness = (i.kind === 'attention' || i.kind === 'verify') && i.signinId ? capacity.accountLines.get(i.signinId)?.readiness : undefined
  const words = readiness?.hint ? `${i.detail}. ${readiness.hint}` : i.detail
  return readiness?.command ? `${words}${/[.!?]$/.test(words) ? '' : '.'} On ${i.signin?.computer.computer_name ?? 'its computer'}, run ${readiness.command}.` : words
}
const feedback = computed(() => lead.value?.signin ? verification.message(lead.value.signin) : '')

// ---------- Away ----------
async function endAway() {
  if (busy.value || !mayManage.value || tileStates.value.capacity.state !== 'ready') return
  const owner = identityKey.value
  busy.value = true
  try { await capacity.endAway(); if (owner === identityKey.value) toast('Welcome back. Agents follow your plan again.') }
  catch (e) { if (owner === identityKey.value) toast(e instanceof Error ? e.message : 'That did not save. Please try again.', { tone: 'error' }) }
  finally { if (owner === identityKey.value) busy.value = false }
}
</script>

<template>
  <span class="acc-slot">
    <span v-if="chipState.state === 'loading'" class="skeleton acc-sk" role="status" aria-label="Reading accounts and computers…" />
    <button
      v-else ref="chip" type="button" class="schip acc-chip" :class="{ warn: chipState.state === 'error', stale: chipState.state === 'stale' }" :data-state="chipState.state" aria-haspopup="dialog" :aria-expanded="open"
      :aria-label="`Accounts and computers: ${chipText}${flag ? `, ${flag}` : ''}${shownAway ? `, away ${awayWords}` : ''}${chipState.state === 'stale' ? `, ${asOf(chipState)}` : ''}`"
      :data-tip="open ? undefined : chipTip" @click="open = !open"
    >
      <AppIcon :name="chipState.state === 'error' ? 'alert' : chipState.state === 'forbidden' ? 'lock' : 'monitor'" :size="15" />
      <span class="sc-t">{{ chipText }}</span>
      <span v-if="chipState.state === 'stale'" class="chip-age"><AppIcon name="clock" :size="13" />{{ asOf(chipState) }}</span>
      <span v-else-if="flag" class="flag">{{ flag }}</span>
      <span v-else-if="shownAway" class="flag calm">Away</span>
      <AppIcon name="chevron" :size="13" class="chev" />
    </button>
  </span>
  <FloatingPanel v-if="open && chip" :anchor="chip" :width="440" label="Accounts and computers" cycle @close="close">
    <div class="acc-pop">
      <!-- The tiles come first and never move; what changes (Away, the first fix and its answer, the state line) grows below them (AEON-541). -->
      <nav class="tiles" aria-label="Accounts, computers and capacity in Settings">
        <div v-for="tile in tiles" :key="tile.part" class="tile-shell" :data-part="tile.part" :data-state="tile.state.state">
          <RouterLink class="tile" :data-part="tile.part" :to="tile.to" :data-tip="tile.state.reason || `${tile.line}. ${tile.detail}`" @click="open = false">
            <AppIcon :name="ICON[tile.part]" :size="16" class="ti" />
            <span class="tt">
              <b>{{ tile.title }}</b>
              <span v-if="tile.state.state === 'loading'" class="tile-line"><span class="skeleton tile-sk" role="status" :aria-label="`Reading ${tile.part}…`" /></span>
              <span v-else class="tile-line" :class="{ warn: tile.state.state === 'error' || tile.warn && tile.state.state === 'ready' }">
                <AppIcon v-if="tile.state.state === 'error'" name="alert" :size="14" />
                <AppIcon v-else-if="tile.state.state === 'forbidden'" name="lock" :size="14" />
                <span>{{ tile.line }}</span>
              </span>
              <small class="tile-note"><AppIcon v-if="tile.state.state === 'stale'" name="clock" :size="12" />{{ tile.state.state === 'loading' ? '' : tile.detail }}</small>
            </span>
            <AppIcon name="chevron-right" :size="14" class="go" />
          </RouterLink>
          <button v-if="tile.state.state === 'error'" type="button" class="link-btn tile-retry" @click="retry(tile.part)">Try again</button>
          <RouterLink v-else-if="tile.state.state === 'empty' && mayManage" class="link-btn tile-retry" to="/agents/register-agent" @click="open = false">Connect</RouterLink>
        </div>
      </nav>
      <p v-if="shownAway" class="away" :data-tip="`Away: agents use everything left until ${whenFull(capacity.away)}`">
        <span>Away {{ awayWords }}</span>
        <button v-if="mayManage" type="button" class="link-btn" :disabled="busy || tileStates.capacity.state !== 'ready'" aria-label="Back now: end Away" @click="endAway">Back now</button>
      </p>
      <div v-if="lead" class="acc-fix" :data-need="lead.id">
        <span class="fix-t"><b>{{ lead.name }}</b><small v-if="feedback" class="verification-feedback" role="status">{{ feedback }}</small><small v-else class="fix-d">{{ detailOf(lead) }}</small></span>
        <VerificationButton v-if="leadVerify && lead.signin" class="sm" :busy="verifyBusy(lead.signin)" @click="verifyAgain(lead)" />
      </div>
      <p class="acc-state" role="status">
        <template v-if="chipState.state === 'error'"><span class="st warn"><AppIcon name="alert" :size="14" /><span class="t">Accounts unreadable</span></span><button type="button" class="link-btn" @click="retry()">Try again</button></template>
        <span v-else-if="chipState.state === 'forbidden'" class="st"><AppIcon name="lock" :size="14" /><span class="t">{{ forbiddenWords('accounts') }}</span></span>
        <template v-else-if="chipState.state === 'stale'"><span class="st stale"><AppIcon name="clock" :size="14" /><span class="t">{{ statusText }} · {{ asOf(chipState) }}</span></span><button type="button" class="link-btn" @click="retry()">Refresh</button></template>
        <template v-else-if="chipState.state === 'empty'"><span class="st"><AppIcon name="info" :size="14" /><span class="t">No accounts yet</span></span><RouterLink v-if="mayManage" class="link-btn" to="/agents/register-agent" @click="open = false">Connect</RouterLink></template>
        <span v-else class="st" :class="{ warn: tone === 'warn' }"><span class="dot" :class="tone" aria-hidden="true" /><span class="t">{{ statusText }}</span></span>
      </p>
    </div>
  </FloatingPanel>
</template>

<style scoped>
/* The chip: one control at the shared height, its flag inside. */
.acc-slot { display: grid; flex: none; inline-size: clamp(28ch, 26vw, 40ch); max-inline-size: 100%; }
.schip { inline-size: 100%; display: inline-flex; align-items: center; flex: none; gap: 8px; min-width: 0; height: var(--ctl-h, 36px); padding: 0 10px 0 12px; border: 1px solid var(--glass-edge); border-radius: 999px; background: var(--btn-bg); color: var(--ink-2); box-shadow: var(--shadow-btn); font-size: 13px; white-space: nowrap; cursor: pointer; }
@media (hover: hover) { .schip:hover { background: var(--btn-bg-hover, var(--btn-bg)); color: var(--ink); } }
.schip[aria-expanded="true"] { background: var(--row-selected); color: var(--teal-ink); }
.schip:focus-visible { outline: none; box-shadow: var(--focus-ring); }
.schip > svg { flex: none; }
.sc-t { min-width: 0; overflow: hidden; text-overflow: ellipsis; color: var(--ink); font-weight: 550; text-align: left; }
.schip.warn .sc-t { color: var(--warn-ink); }
.schip.stale .flag { background: none; box-shadow: none; color: var(--ink-3); }
.chip-age { display: inline-flex; align-items: center; gap: 4px; flex: none; color: var(--ink-3); font-size: 11.5px; }
.chev { color: var(--ink-3); }
.flag { display: inline-flex; align-items: center; flex: none; height: 20px; padding: 0 7px; border-radius: 999px; background: var(--queue-wait-bg); box-shadow: inset 0 0 0 1px var(--queue-wait-line); color: var(--queue-wait-ink); font-size: 11.5px; font-weight: 650; white-space: nowrap; }
/* The flag keeps the room of its usual words ("1 needs you", "2 sign-ins"), so a count that changes moves nothing. */
.flag:not(.calm) { justify-content: center; min-inline-size: 12ch; }
.flag.calm { background: var(--chip-teal-bg); box-shadow: inset 0 0 0 1px var(--chip-teal-line); color: var(--teal-ink); }
.acc-sk { display: inline-block; width: 100%; height: var(--ctl-h, 36px); border-radius: 999px; }
/* A narrow row (phones): the chip takes the full width; its words shorten, the flag stays. */
@container cap (max-width: 640px) {
  .acc-slot { flex: 1 1 100%; inline-size: 100%; }
  .sc-t { flex: 1; }
}

/* The popover: the status line, Away and Verify again, then the three tiles. */
.acc-pop { display: grid; gap: 10px; padding: 4px; }
.acc-state { display: flex; align-items: center; flex-wrap: wrap; gap: 4px 10px; margin: 0; padding: 2px 4px; }
.st { display: inline-flex; align-items: center; gap: 8px; min-width: 0; color: var(--ink-2); font-size: 13px; }
.st .t { min-width: 0; overflow-wrap: anywhere; }
.st.warn { color: var(--warn-ink); font-weight: 600; }
.dot { display: inline-block; flex: none; width: 8px; height: 8px; border-radius: 50%; }
.dot.ok { background: var(--ok); }
.dot.warn { background: var(--gold); }
.dot.wait { box-shadow: inset 0 0 0 1.8px var(--gold); }
.link-btn { padding: 0; border: 0; background: none; color: var(--teal-ink); font: inherit; font-size: 12.5px; font-weight: 600; text-decoration: underline; text-underline-offset: 2px; cursor: pointer; }
.link-btn:disabled { opacity: .55; cursor: default; }
.link-btn:focus-visible { outline: none; box-shadow: var(--focus-ring); border-radius: 4px; }
.away { display: flex; align-items: center; gap: 10px; margin: 0; padding: 0 4px; color: var(--teal-ink); font-size: 12.5px; font-weight: 600; }
/* The words grow downward beside Verify again, which keeps its place at the top right. */
.acc-fix { display: grid; grid-template-columns: minmax(0, 1fr) auto; align-items: start; gap: 6px 12px; padding: 0 4px; }
.fix-t { display: grid; gap: 2px; min-width: 0; color: var(--ink); font-size: 13px; overflow-wrap: anywhere; }
.fix-t b { font-weight: 600; }
.fix-t small { color: var(--ink-2); font-size: 12.5px; line-height: 1.45; }
.st.stale, .schip.stale, .tile-note { color: var(--ink-3); }
.tiles { display: grid; gap: 6px; }
.tile-shell { position: relative; }
.tile-retry { position: absolute; inset-inline-start: 42px; bottom: 8px; }
.tile { display: grid; grid-template-columns: 22px minmax(0, 1fr) 16px; align-items: center; gap: 10px; min-height: 56px; padding: 8px 10px; border-radius: 12px; background: var(--surface-sunken); color: var(--ink); text-decoration: none; }
@media (hover: hover) { .tile:hover { background: var(--row-hover); } }
.tile:focus-visible { outline: none; box-shadow: var(--focus-ring); }
.ti { justify-self: center; color: var(--ink-2); }
.go { color: var(--ink-3); }
.tt { display: grid; gap: 1px; min-width: 0; font-size: 13px; }
.tt b { font-weight: 650; }
.tt .tile-line { display: flex; align-items: flex-start; gap: 5px; min-height: 2.9em; }
.tile-line > svg { flex: none; margin-top: 2px; }
.tile-line > span { display: -webkit-box; -webkit-box-orient: vertical; -webkit-line-clamp: 2; overflow: hidden; }
.tile-sk { display: block; width: 80%; height: 1em; margin-top: 3px; }
.tt span { color: var(--ink-2); font-size: 12.5px; overflow-wrap: anywhere; }
.tile-line.warn > span { color: inherit; }
.tt span.warn { color: var(--warn-ink); font-weight: 600; }
.tt small { display: flex; align-items: flex-start; gap: 4px; min-height: 2.9em; max-height: 2.9em; overflow: hidden; color: var(--ink-3); font-size: 11.5px; overflow-wrap: anywhere; }
@media (pointer: coarse), (max-width: 720px) {
  .link-btn { display: inline-flex; align-items: center; min-height: 44px; }
  .acc-fix :deep(.btn) { min-height: 44px; }
  .tile { min-height: 64px; }
  .tt .tile-note { min-height: 44px; max-height: 44px; }
}
</style>
