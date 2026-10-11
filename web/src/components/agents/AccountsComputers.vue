<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { useRoute } from 'vue-router'
import { api } from '../../lib/api'
import { can } from '../../lib/authz'
import type { PairingPermissions } from '../../lib/agentPairing'
import { overviewAccounts, type SignInReference } from '../../lib/accountsOverview'
import { DEFAULT_QUOTA_THRESHOLDS, glanceChipText, glanceFlag, glanceItems, glanceSummary, glanceTiles, isProblemSignin, onlineComputers, type GlanceItem, type GlancePart } from '../../lib/accountsGlance'
import { pacingSummary } from '../../lib/computerAccounts'
import { when, whenFull } from '../../lib/capacity'
import { validQuotaThresholds, type QuotaWarningSettings } from '../../lib/quotaWarnings'
import { toast } from '../../lib/toast'
import { usePoller } from '../../lib/usePolledData'
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
watch(identityKey, () => { open.value = false })

// ---------- What the page knows ----------
const computers = computed(() => props.permissions.canListComputers ? capacity.computers.filter(c => c.computer_id && !c.archived_at) : [])
const accounts = computed(() => props.showAccounts ? overviewAccounts(agents.accounts, capacity.rows, computers.value) : [])
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
const loaded = computed(() => capacity.loaded || !props.showAccounts)
const failed = computed(() => !loaded.value && capacity.state === 'error')
const empty = computed(() => loaded.value && !accounts.value.length && !computers.value.length)
const stale = computed(() => capacity.stale || capacity.computersStale)
const online = computed(() => onlineComputers(computers.value))
const signins = computed(() => props.showAccounts ? capacity.signins.length : 0)
const statusText = computed(() => !props.showAccounts
  ? `${online.value} of ${computers.value.length} ${computers.value.length === 1 ? 'computer' : 'computers'} online`
  : accounts.value.length ? summary.value.text : `No accounts yet · ${computers.value.length} ${computers.value.length === 1 ? 'computer' : 'computers'} online`)
const tone = computed(() => !props.showAccounts ? (online.value === computers.value.length ? 'ok' : 'warn') : accounts.value.length ? summary.value.tone : 'warn')
const chipText = computed(() => failed.value ? 'Accounts could not be read' : glanceChipText(accounts.value.length, computers.value.length, props.showAccounts))
const flag = computed(() => failed.value ? '' : glanceFlag(signins.value, openItems.value))
const awayWords = computed(() => capacity.away ? `until ${when(capacity.away, now.value)}` : '')
const tiles = computed(() => glanceTiles({
  accounts: accounts.value, computers: computers.value, items: openItems.value, summary: summary.value, signins: signins.value,
  pacing: capacity.loaded ? pacingSummary(capacity.schedule) : '', away: awayWords.value, showAccounts: props.showAccounts,
}))
const ICON: Record<GlancePart, IconName> = { accounts: 'key', computers: 'monitor', capacity: 'gauge' }
async function retry() { await Promise.all([agents.refreshAccounts(), capacity.load()]) }

// An approval link (?verify_account=…) opens the popover once, at the sign-in it names.
watch(requested, async id => {
  if (!id) return
  await nextTick()
  if (chip.value) open.value = true
}, { immediate: true })

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
onMounted(() => {
  poller.start(false); window.addEventListener('focus', readThresholds)
  if (requested.value && chip.value) open.value = true
})
onBeforeUnmount(() => { generation++; poller.stop(); window.removeEventListener('focus', readThresholds) })

// ---------- Verify again, bound to the sign-in on screen ----------
function canVerify(s: SignInReference) {
  return mayManage.value && s.computer.computer_state === 'connected' && s.enrollment.state === 'connected' && s.enrollment.can_verify === true
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
const lead = computed(() => firstFix.value ?? openItems.value[0])
const leadVerify = computed(() => !!lead.value?.signin && lead.value === firstFix.value)
function detailOf(i: GlanceItem): string {
  const readiness = (i.kind === 'attention' || i.kind === 'verify') && i.signinId ? capacity.accountLines.get(i.signinId)?.readiness : undefined
  const words = readiness?.hint ? `${i.detail}. ${readiness.hint}` : i.detail
  return readiness?.command ? `${words}${/[.!?]$/.test(words) ? '' : '.'} On ${i.signin?.computer.computer_name ?? 'its computer'}, run ${readiness.command}.` : words
}
const feedback = computed(() => lead.value?.signin ? verification.message(lead.value.signin) : '')

// ---------- Away ----------
async function endAway() {
  if (busy.value || !mayManage.value) return
  const owner = identityKey.value
  busy.value = true
  try { await capacity.endAway(); if (owner === identityKey.value) toast('Welcome back. Agents follow your plan again.') }
  catch (e) { if (owner === identityKey.value) toast(e instanceof Error ? e.message : 'That did not save. Please try again.', { tone: 'error' }) }
  finally { if (owner === identityKey.value) busy.value = false }
}
</script>

<template>
  <span v-if="!loaded && !failed" class="skeleton acc-sk" role="status" aria-label="Reading accounts and computers…" />
  <button
    v-else ref="chip" type="button" class="schip acc-chip" :class="{ warn: failed }" aria-haspopup="dialog" :aria-expanded="open"
    :aria-label="`Accounts and computers: ${chipText}${flag ? `, ${flag}` : ''}${capacity.away ? `, away ${awayWords}` : ''}`"
    :data-tip="open ? undefined : 'Accounts, computers and capacity'" @click="open = !open"
  >
    <AppIcon name="monitor" :size="15" />
    <span class="sc-t">{{ chipText }}</span>
    <span v-if="flag" class="flag">{{ flag }}</span>
    <span v-else-if="capacity.away" class="flag calm">Away</span>
    <AppIcon name="chevron" :size="13" class="chev" />
  </button>
  <FloatingPanel v-if="open && chip" :anchor="chip" :width="440" label="Accounts and computers" cycle @close="close">
    <div class="acc-pop">
      <p class="acc-state" role="status">
        <template v-if="failed"><span class="st warn"><span class="dot warn" aria-hidden="true" /><span class="t">Accounts could not be read</span></span><button type="button" class="link-btn" @click="retry">Try again</button></template>
        <template v-else-if="empty"><span class="st warn"><span class="dot wait" aria-hidden="true" /><span class="t">No accounts or computers yet</span></span><RouterLink class="link-btn" to="/agents/register-agent" @click="open = false">Connect a computer</RouterLink></template>
        <span v-else class="st" :class="{ warn: tone === 'warn' }"><span class="dot" :class="tone" aria-hidden="true" /><span class="t">{{ statusText }}</span></span>
      </p>
      <p v-if="capacity.away" class="away" :data-tip="`Away: agents use everything left until ${whenFull(capacity.away)}`">
        <span>Away {{ awayWords }}</span>
        <button v-if="mayManage" type="button" class="link-btn" :disabled="busy" aria-label="Back now: end Away" @click="endAway">Back now</button>
      </p>
      <div v-if="lead" class="acc-fix" :data-need="lead.id">
        <span class="fix-t"><b>{{ lead.name }}</b><small v-if="feedback" class="verification-feedback" role="status">{{ feedback }}</small><small v-else class="fix-d">{{ detailOf(lead) }}</small></span>
        <VerificationButton v-if="leadVerify && lead.signin" class="sm" :busy="verifyBusy(lead.signin)" @click="verifyAgain(lead)" />
      </div>
      <p v-if="stale" class="stale" role="status">Some readings may be out of date. <button type="button" class="link-btn" @click="retry">Refresh</button></p>
      <nav class="tiles" aria-label="Accounts, computers and capacity in Settings">
        <RouterLink v-for="tile in tiles" :key="tile.part" class="tile" :data-part="tile.part" :to="tile.to" @click="open = false">
          <AppIcon :name="ICON[tile.part]" :size="16" class="ti" />
          <span class="tt"><b>{{ tile.title }}</b><span :class="{ warn: tile.warn }">{{ tile.line }}</span><small>{{ tile.detail }}</small></span>
          <AppIcon name="chevron-right" :size="14" class="go" />
        </RouterLink>
      </nav>
    </div>
  </FloatingPanel>
</template>

<style scoped>
/* The chip: one control at the shared height, its flag inside. */
.schip { display: inline-flex; align-items: center; flex: none; gap: 8px; min-width: 0; height: var(--ctl-h, 36px); padding: 0 10px 0 12px; border: 1px solid var(--glass-edge); border-radius: 999px; background: var(--btn-bg); color: var(--ink-2); box-shadow: var(--shadow-btn); font-size: 13px; white-space: nowrap; cursor: pointer; }
@media (hover: hover) { .schip:hover { background: var(--btn-bg-hover, var(--btn-bg)); color: var(--ink); } }
.schip[aria-expanded="true"] { background: var(--row-selected); color: var(--teal-ink); }
.schip:focus-visible { outline: none; box-shadow: var(--focus-ring); }
.schip > svg { flex: none; }
.sc-t { min-width: 0; overflow: hidden; text-overflow: ellipsis; color: var(--ink); font-weight: 550; text-align: left; }
.schip.warn .sc-t { color: var(--warn-ink); }
.chev { color: var(--ink-3); }
.flag { display: inline-flex; align-items: center; flex: none; height: 20px; padding: 0 7px; border-radius: 999px; background: var(--queue-wait-bg); box-shadow: inset 0 0 0 1px var(--queue-wait-line); color: var(--queue-wait-ink); font-size: 11.5px; font-weight: 650; white-space: nowrap; }
/* The flag keeps the room of its usual words ("1 needs you", "2 sign-ins"), so a count that changes moves nothing. */
.flag:not(.calm) { justify-content: center; min-inline-size: 12ch; }
.flag.calm { background: var(--chip-teal-bg); box-shadow: inset 0 0 0 1px var(--chip-teal-line); color: var(--teal-ink); }
.acc-sk { display: inline-block; width: 250px; max-width: 60vw; height: var(--ctl-h, 36px); border-radius: 999px; }
/* A narrow row (phones): the chip takes the full width; its words shorten, the flag stays. */
@container cap (max-width: 640px) {
  .schip, .acc-sk { flex: 1 1 100%; }
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
.stale { margin: 0; padding: 0 4px; color: var(--ink-2); font-size: 12.5px; }
.tiles { display: grid; gap: 6px; }
.tile { display: grid; grid-template-columns: 22px minmax(0, 1fr) 16px; align-items: center; gap: 10px; min-height: 56px; padding: 8px 10px; border-radius: 12px; background: var(--surface-sunken); color: var(--ink); text-decoration: none; }
@media (hover: hover) { .tile:hover { background: var(--row-hover); } }
.tile:focus-visible { outline: none; box-shadow: var(--focus-ring); }
.ti { justify-self: center; color: var(--ink-2); }
.go { color: var(--ink-3); }
.tt { display: grid; gap: 1px; min-width: 0; font-size: 13px; }
.tt b { font-weight: 650; }
.tt span { color: var(--ink-2); font-size: 12.5px; overflow-wrap: anywhere; }
.tt span.warn { color: var(--warn-ink); font-weight: 600; }
.tt small { color: var(--ink-3); font-size: 11.5px; overflow-wrap: anywhere; }
@media (pointer: coarse), (max-width: 720px) {
  .link-btn { display: inline-flex; align-items: center; min-height: 44px; }
  .acc-fix :deep(.btn) { min-height: 44px; }
  .tile { min-height: 64px; }
}
</style>
