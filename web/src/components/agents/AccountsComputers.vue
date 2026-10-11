<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { api } from '../../lib/api'
import { can } from '../../lib/authz'
import type { PairingPermissions } from '../../lib/agentPairing'
import { overviewAccounts, type SignInReference } from '../../lib/accountsOverview'
import { computerCaptionParts, DEFAULT_QUOTA_THRESHOLDS, glanceItems, glanceSummary, isProblemSignin, onlineComputers, signinStatus, windowLabel, type GlanceItem } from '../../lib/accountsGlance'
import { pacingSummary } from '../../lib/computerAccounts'
import { HARNESS_NAME, when, whenFull } from '../../lib/capacity'
import { validQuotaThresholds, type QuotaWarningSettings } from '../../lib/quotaWarnings'
import { toast } from '../../lib/toast'
import { usePoller } from '../../lib/usePolledData'
import { vClipTip } from '../../directives/clipTip'
import { useAgents } from '../../stores/agents'
import { useCapacity } from '../../stores/capacity'
import { useSectionFold } from '../../stores/sectionPrefs'
import { useSession } from '../../stores/session'
import AppIcon from '../AppIcon.vue'
import NeedsYouList, { type NeedsYouItem } from '../settings/NeedsYouList.vue'
import FoldSection from './FoldSection.vue'
import HarnessMark from './HarnessMark.vue'
import VerificationButton from './VerificationButton.vue'
import { signinKey, useVerification } from '../../lib/useVerification'

// Accounts and computers on the Agents page (AEON-782): its state in one status
// line and what blocks agents in the body, as the approved design (AEON-721)
// has it. The lists, panels, capacity, pacing and the account menu live in
// Settings › Accounts and computers (AEON-686, AEON-786); every name and cell
// here opens the right panel there.
const route = useRoute(), router = useRouter()
const props = defineProps<{ permissions: PairingPermissions; showAccounts: boolean }>()
const agents = useAgents(), capacity = useCapacity(), session = useSession()
const { open, toggle, reveal } = useSectionFold('accounts')
const mayManage = computed(() => session.identity?.principal.kind === 'person' && can('account.manage'))
const identityKey = computed(() => `${session.identity?.tenant.id ?? ''}/${session.identity?.principal.id ?? ''}`)
const now = computed(() => agents.now)

// An approval link (?verify_account=…) shows the section once; an explicit fold wins afterwards.
watch(() => route.query.verify_account, id => { if (id) reveal() }, { immediate: true })

const busy = ref(false)

// ---------- What the page knows ----------
const computers = computed(() => props.permissions.canListComputers ? capacity.computers.filter(c => c.computer_id && !c.archived_at) : [])
const accounts = computed(() => props.showAccounts ? overviewAccounts(agents.accounts, capacity.rows, computers.value) : [])
const thresholds = ref<QuotaWarningSettings>({ ...DEFAULT_QUOTA_THRESHOLDS })
const baseItems = computed(() => glanceItems(accounts.value, computers.value, thresholds.value, now.value, typeof route.query.verify_account === 'string' ? route.query.verify_account : ''))
const allSignins = computed(() => accounts.value.flatMap(a => a.signins))
const verification = useVerification(identityKey, allSignins, canVerify, () => capacity.refreshComputers())
const attemptedItems = ref<{ item: GlanceItem; index: number }[]>([])
watch(identityKey, () => { attemptedItems.value = [] })
const items = computed(() => {
  const retained = new Map(verification.retained.value.map(s => [signinKey(s), s]))
  const remembered = attemptedItems.value.filter(x => x.item.signin && retained.has(signinKey(x.item.signin)))
  const keys = new Set(remembered.map(x => signinKey(x.item.signin!)))
  const out = baseItems.value.filter(i => !i.signin || !keys.has(signinKey(i.signin)))
  for (const { item, index } of remembered) out.splice(Math.min(index, out.length), 0, { ...item, signin: retained.get(signinKey(item.signin!))! })
  return out
})
const summary = computed(() => glanceSummary(accounts.value, computers.value, baseItems.value, now.value))
const pacingLine = computed(() => pacingSummary(capacity.schedule))
const loaded = computed(() => capacity.loaded || !props.showAccounts)
const failed = computed(() => !loaded.value && capacity.state === 'error')
const empty = computed(() => loaded.value && !accounts.value.length && !computers.value.length)
const stale = computed(() => capacity.stale || capacity.computersStale)
const allReady = computed(() => accounts.value.every(a => a.signins.every(s => signinStatus(s.computer, s.enrollment.account_id) === 'Ready')))
const online = computed(() => onlineComputers(computers.value))
const statusText = computed(() => !props.showAccounts
  ? `${online.value} of ${computers.value.length} ${computers.value.length === 1 ? 'computer' : 'computers'} online`
  : accounts.value.length ? summary.value.text : `No accounts yet · ${computers.value.length} ${computers.value.length === 1 ? 'computer' : 'computers'} online`)
const tone = computed(() => !props.showAccounts ? (online.value === computers.value.length ? 'ok' : 'warn') : accounts.value.length ? summary.value.tone : 'warn')
async function retry() { await Promise.all([agents.refreshAccounts(), capacity.load()]) }

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
  return mayManage.value && s.computer.computer_state === 'connected' && s.enrollment.state === 'connected' && s.enrollment.can_verify === true
    && s.computer.verification_capabilities?.[s.enrollment.harness]?.supported === true
}
const verifyBusy = (s: SignInReference) => verification.pending(s)
async function verifyAgain(item: GlanceItem) {
  if (!item.signin) return
  if (!attemptedItems.value.some(x => x.item.id === item.id)) attemptedItems.value.push({ item, index: items.value.findIndex(i => i.id === item.id) })
  await verification.request(item.signin)
}
// The folded head's one-click fix: the first sign-in a person may verify right now.
const firstFix = computed(() => items.value.find(i => i.kind === 'verify' && i.signin && canVerify(i.signin)))

// ---------- Away ----------
async function endAway() {
  if (busy.value || !mayManage.value) return
  const owner = identityKey.value
  busy.value = true
  try { await capacity.endAway(); if (owner === identityKey.value) toast('Welcome back. Agents follow your plan again.') }
  catch (e) { if (owner === identityKey.value) toast(e instanceof Error ? e.message : 'That did not save. Please try again.', { tone: 'error' }) }
  finally { if (owner === identityKey.value) busy.value = false }
}

// ---------- Links into Settings, each with the right panel open ----------
const SETTINGS = '/settings/accounts'
const linkComputer = (id: string | null | undefined, signin?: string) => `${SETTINGS}?${new URLSearchParams({ computer: id ?? '', ...(signin ? { signin } : {}) })}`
const linkAccount = (id: string) => `${SETTINGS}?${new URLSearchParams({ account: id })}`
const linkOf = (i: GlanceItem) => i.kind === 'quota' ? linkAccount(i.accountId!) : linkComputer(i.computerId, i.signinId)
// The computer's own report says how to fix a blocked sign-in: its hint, and the one command to run there.
function detailOf(i: GlanceItem): string {
  const readiness = (i.kind === 'attention' || i.kind === 'verify') && i.signinId ? capacity.accountLines.get(i.signinId)?.readiness : undefined
  const feedback = i.signin && verification.message(i.signin)
  if (feedback) return feedback
  const words = readiness?.hint ? `${i.detail}. ${readiness.hint}` : i.detail
  return readiness?.command ? `${words}${/[.!?]$/.test(words) ? '' : '.'} On ${i.signin?.computer.computer_name ?? 'its computer'}, run ${readiness.command}.` : words
}
const needs = computed<NeedsYouItem[]>(() => items.value.map(i => {
  const resolved = !!i.signin && !isProblemSignin(i.signin) && verification.retained.value.some(s => signinKey(s) === signinKey(i.signin!))
  return {
    id: i.id,
    name: i.kind === 'verify' && i.signin ? `${HARNESS_NAME[i.signin.enrollment.harness] ?? 'Account'} verification on ${i.signin.computer.computer_name}` : i.name,
    detail: detailOf(i), count: resolved ? 0 : 1,
    icon: i.kind === 'quota' ? 'gauge' : i.kind === 'cleanup' ? 'shield' : 'alert', tone: i.kind === 'cleanup' ? 'waiting' : undefined,
    href: linkOf(i),
    action: i.kind === 'verify' && i.signin && canVerify(i.signin) ? { label: 'Verify again', fixesProblem: true, disabled: verifyBusy(i.signin), busy: verifyBusy(i.signin) } : { label: 'Details' },
  }
}))
function act(id: string) {
  const item = items.value.find(i => i.id === id)
  if (!item) return
  if (item.kind === 'verify' && item.signin && canVerify(item.signin)) void verifyAgain(item)
  else void router.push(linkOf(item))
}

// ---------- The grid ----------
type Cell = { signinId: string; text: string; tone: 'ok' | 'warn' | 'wait' }
const problems = computed(() => new Set(items.value.filter(i => i.signin && isProblemSignin(i.signin)).map(i => `${i.computerId}/${i.signinId}`)))
/** One row per account, one cell list per computer: an account signed in twice on a computer shows both. */
const grid = computed(() => accounts.value.map(a => ({
  account: a,
  quota: a.windows[0] ? { percent: Math.max(0, Math.min(100, Math.round(a.windows[0].remaining_percent))), label: windowLabel(a.windows[0]).toLowerCase().replace(' window', '').replace(' allowance', '') } : null,
  cells: computers.value.map(c => a.signins.filter(s => s.computer.computer_id === c.computer_id).map<Cell>(s => {
    const text = signinStatus(c, s.enrollment.account_id, a.rows.find(row => row.id === s.enrollment.account_id), now.value)
    return { signinId: s.enrollment.account_id, text, tone: text === 'Ready' ? 'ok' : problems.value.has(`${c.computer_id}/${s.enrollment.account_id}`) ? 'warn' : 'wait' }
  })),
})))
</script>

<template>
  <FoldSection class="acc-section" label="Accounts and computers" :open="open" :tip="open ? 'Fold accounts and computers' : 'Unfold accounts and computers'" @toggle="toggle">
    <template #title><span class="acc-ico" aria-hidden="true"><AppIcon name="monitor" :size="14" /></span>Accounts and computers</template>
    <template #head>
      <span class="fs-sum" role="status">
        <span v-if="!loaded && !failed" class="stat-skel" aria-hidden="true"></span><span v-if="!loaded && !failed" class="sr-only">Reading accounts and computers…</span>
        <span v-else-if="failed" class="st warn"><span class="dot warn" aria-hidden="true"></span><span class="t">Accounts could not be read · <button type="button" class="link-btn" @click="retry">Try again</button></span></span>
        <span v-else-if="empty" class="st warn"><span class="dot wait" aria-hidden="true"></span><span class="t">No accounts yet · <RouterLink class="link-btn" to="/agents/register-agent">Connect a computer</RouterLink></span></span>
        <span v-else class="st" :class="{ warn: tone === 'warn' }"><span class="dot" :class="tone" aria-hidden="true"></span><span class="t" v-clip-tip>{{ statusText }}</span></span>
      </span>
      <span v-if="capacity.away" class="away" :data-tip="`Away: agents use everything left until ${whenFull(capacity.away)}`">
        <span>Away until {{ when(capacity.away, now) }}</span>
        <button v-if="mayManage" type="button" class="link-btn" :disabled="busy" aria-label="Back now: end Away" @click="endAway">Back now</button>
      </span>
      <span v-if="!open && firstFix?.signin" class="fs-act"><VerificationButton class="sm" :busy="verifyBusy(firstFix.signin)" @click="verifyAgain(firstFix)" /></span>
      <span class="fs-tools"><RouterLink class="btn sm ghost" :to="SETTINGS" aria-label="Manage accounts and computers" title="Accounts, computers, capacity and warnings in Settings"><AppIcon name="gear" :size="14" /><span class="tl">Manage</span></RouterLink></span>
    </template>

    <template #feedback><p v-if="!open && firstFix?.signin && verification.message(firstFix.signin)" class="verification-feedback" role="status">{{ verification.message(firstFix.signin) }}</p></template>
    <div class="acc-pad acc">
      <p v-if="failed" class="empty" role="alert">Accounts could not be read. <button type="button" class="btn sm" @click="retry">Try again</button></p>
      <p v-else-if="empty" class="empty">No accounts or computers yet. <RouterLink to="/agents/register-agent">Connect a computer</RouterLink>, sign in to a harness there, and it appears here.</p>
      <template v-else-if="loaded">
        <NeedsYouList v-if="props.showAccounts" :items="needs" :show-calm="verification.retained.value.length > 0" :aside="!stale && allReady ? 'Everything else is working.' : 'Based on the last available reports.'" @action="act" />
        <p v-if="stale" class="stale" role="status">Some readings may be out of date. <button type="button" class="btn sm" @click="retry">Refresh</button></p>
        <section v-if="accounts.length && computers.length" class="grid-wrap" aria-labelledby="acc-grid-title">
          <div class="zone-head"><h3 id="acc-grid-title" class="eyebrow">Sign-ins at a glance</h3><p>Each cell is one account on one computer</p></div>
          <div class="matrix-scroll" tabindex="0" aria-label="Sign-ins table; scroll horizontally for more computers">
            <table class="matrix">
              <thead><tr>
                <th scope="col" class="corner">Account · quota left</th>
                <th v-for="c in computers" :key="c.computer_id!" scope="col">
                  <RouterLink class="mx-head" :to="linkComputer(c.computer_id)" :data-tip="`Open ${c.computer_name} in Settings`">
                    <span class="glyph sm" aria-hidden="true"><AppIcon name="monitor" :size="14" /></span>
                    <b class="mx-name" v-clip-tip>{{ c.computer_name }}</b>
                    <span class="mx-sub"><span class="dot" :class="computerCaptionParts(c).tone" aria-hidden="true"></span><span class="long">{{ computerCaptionParts(c).long }}</span><span class="short">{{ computerCaptionParts(c).short }}</span></span>
                  </RouterLink>
                </th>
              </tr></thead>
              <tbody>
                <tr v-for="row in grid" :key="row.account.id">
                  <th scope="row">
                    <RouterLink class="mx-head" :to="linkAccount(row.account.records[0]?.id ?? row.account.id)" :data-tip="`Open ${row.account.vendor} in Settings`">
                      <span class="vendor" aria-hidden="true"><HarnessMark :harness="row.account.harness" :size="15" /></span>
                      <b class="mx-name">{{ row.account.vendor }}</b>
                      <span class="mx-sub">
                        <template v-if="row.quota"><span class="mini" aria-hidden="true"><i :style="{ width: `${row.quota.percent}%` }"></i></span>{{ row.quota.percent }}% · {{ row.quota.label }}</template>
                        <template v-else>Quota not reported</template>
                      </span>
                    </RouterLink>
                  </th>
                  <td v-for="(cells, index) in row.cells" :key="computers[index].computer_id!">
                    <RouterLink v-for="cell in cells" :key="cell.signinId" class="mx-cell" :to="linkComputer(computers[index].computer_id, cell.signinId)" :aria-label="`${row.account.vendor} on ${computers[index].computer_name}: ${cell.text}`">
                      <span class="st" :class="{ warn: cell.tone === 'warn' }"><span class="dot" :class="cell.tone" aria-hidden="true"></span>{{ cell.text }}</span>
                    </RouterLink>
                    <span v-if="!cells.length" class="mx-none"><AppIcon name="minus" :size="16" /><span class="sr-only">{{ row.account.vendor }} is not signed in on {{ computers[index].computer_name }}</span></span>
                  </td>
                </tr>
              </tbody>
            </table>
          </div>
        </section>
        <p class="acc-foot">
          <span v-if="props.showAccounts"><b>Pacing:</b> {{ pacingLine }} · <RouterLink :to="`${SETTINGS}#capacity-and-load`">Change in Settings</RouterLink></span>
          <span>Lists, capacity and low-quota warnings live in <RouterLink :to="SETTINGS">Settings, under Accounts and computers</RouterLink>.</span>
        </p>
      </template>
    </div>
  </FoldSection>
</template>

<style scoped>
.acc-section { container: acc / inline-size; border-radius: 20px; min-width: 0; }
.acc-ico { display: grid; place-items: center; width: 26px; height: 26px; border-radius: 8px; background: var(--surface-sunken); color: var(--ink-2); }
/* The head: title, then the state; Verify again and Manage sit at the line end, so a part that appears moves nothing before it. Wide: one row, and the state shortens with an ellipsis instead of wrapping, so Verify again leaving never changes the head's height. Medium (below): two fixed rows, whatever is shown, so nothing jumps when Away or Verify again comes or goes. */
.acc-section :deep(.fs-head) { flex-wrap: nowrap; gap: 6px 12px; }
.fs-sum { display: flex; align-items: center; gap: 8px; min-width: 0; color: var(--ink-2); font-size: 13px; }
.fs-sum .t { min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.st { display: inline-flex; align-items: center; gap: 8px; min-width: 0; color: var(--ink-2); font-size: 13px; }
.st.warn { color: var(--warn-ink); font-weight: 600; }
.dot { display: inline-block; flex: none; width: 8px; height: 8px; border-radius: 50%; }
.dot.ok { background: var(--ok); }
.dot.warn { background: var(--gold); }
.dot.wait { box-shadow: inset 0 0 0 1.8px var(--gold); }
.dot.blocked { box-shadow: inset 0 0 0 1.8px var(--ink-3); }
.stat-skel { display: inline-block; width: 220px; max-width: 100%; height: 12px; border-radius: 6px; background: var(--skeleton); }
.link-btn { padding: 0; border: 0; background: none; color: var(--teal-ink); font: inherit; font-size: 12.5px; font-weight: 600; text-decoration: underline; text-underline-offset: 2px; cursor: pointer; }
.link-btn:disabled { opacity: .55; cursor: default; }
.link-btn:focus-visible { outline: none; box-shadow: var(--focus-ring); border-radius: 4px; }
.away { display: inline-flex; align-items: center; gap: 8px; height: 26px; padding: 0 10px; border-radius: 999px; background: var(--chip-teal-bg); box-shadow: inset 0 0 0 1px var(--chip-teal-line); color: var(--teal-ink); font-size: 12px; font-weight: 600; white-space: nowrap; }
.away .link-btn { font-size: 12px; }
.fs-act { display: flex; align-items: center; margin-left: auto; flex: none; }
.fs-tools { display: flex; align-items: center; gap: 6px; margin-left: auto; flex: none; }
.fs-act + .fs-tools { margin-left: 0; }
.fs-tools .btn.ghost { color: var(--ink-2); }
.fs-tools .btn.ghost svg { color: var(--ink-3); }
.fs-tools .btn.ghost:hover, .fs-tools .btn.ghost:hover svg { color: var(--teal-ink); }

/* The body: Needs you, then the grid, then the pacing line. */
.verification-feedback { margin: 0; padding: 0 20px 12px; color: var(--ink-2); font-size: 13px; }
.acc-pad { display: grid; gap: 18px; padding: 4px 20px 16px; border-top: 1px solid var(--line); padding-top: 16px; }
.acc :deep(.needs-head) { margin: 0 0 10px; }
.acc :deep(.needs-head h2) { font: 650 13.5px/1.3 var(--font); letter-spacing: 0; }
.acc :deep(.attention) { background: var(--surface-raised); }
.empty, .stale { margin: 0; color: var(--ink-3); font-size: 13px; }
.empty a, .acc-foot a { color: var(--teal-ink); }
.zone-head { display: flex; align-items: baseline; justify-content: space-between; flex-wrap: wrap; gap: 4px 16px; margin-bottom: 10px; }
.zone-head p { margin: 0; font-size: 12px; color: var(--ink-3); }
.eyebrow { margin: 0; font: 500 10.5px/1.5 var(--mono); letter-spacing: .18em; text-transform: uppercase; color: var(--ink-3); font-variant-ligatures: none; }
.matrix-scroll { max-width: 100%; overflow-x: auto; }
.matrix-scroll:focus-visible { outline: none; box-shadow: var(--focus-ring); border-radius: var(--radius-row); }
.matrix { width: 100%; min-width: 320px; border-collapse: collapse; table-layout: fixed; }
.matrix th, .matrix td { padding: 2px 0; border-bottom: 1px solid var(--line); text-align: left; vertical-align: middle; font-weight: inherit; }
.matrix thead th { border-bottom-color: var(--line-2); vertical-align: bottom; }
.matrix .corner { width: 38%; padding-bottom: 10px; font: 500 10.5px/1.4 var(--mono); letter-spacing: .12em; text-transform: uppercase; color: var(--ink-3); }
.mx-head { display: grid; grid-template-columns: auto minmax(0, 1fr); align-items: center; gap: 0 10px; width: 100%; min-height: 54px; padding: 6px 10px; border: 0; border-radius: var(--radius-row); background: transparent; color: inherit; text-align: left; text-decoration: none; }
.mx-head:hover, .mx-cell:hover { background: var(--row-hover); }
.mx-head:focus-visible, .mx-cell:focus-visible { outline: none; box-shadow: var(--focus-ring); }
.mx-head > .vendor, .mx-head > .glyph { grid-row: span 2; }
.mx-name { min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; color: var(--ink); font-size: 13.5px; font-weight: 650; }
.mx-sub { display: flex; align-items: center; flex-wrap: wrap; gap: 2px 8px; grid-column: 2; min-width: 0; color: var(--ink-3); font-size: 12px; font-variant-numeric: tabular-nums; }
.mx-sub .short { display: none; }
.vendor { display: grid; place-items: center; flex: none; width: 28px; height: 28px; border-radius: 9px; background: var(--surface-raised); box-shadow: inset 0 0 0 1px var(--line); color: var(--ink); }
.glyph { display: grid; place-items: center; flex: none; width: 28px; height: 28px; border-radius: 50%; background: var(--surface-sunken); color: var(--ink-2); }
.mini { display: inline-block; width: 40px; height: 4px; border-radius: 999px; background: var(--track); overflow: hidden; }
.mini i { display: block; height: 100%; border-radius: inherit; background: color-mix(in srgb, var(--teal) 60%, transparent); }
.mx-cell { display: flex; align-items: center; gap: 8px; width: 100%; height: 46px; padding: 0 10px; border: 0; border-radius: var(--radius-row); background: transparent; color: inherit; text-align: left; text-decoration: none; }
.mx-none { display: flex; align-items: center; height: 46px; padding: 0 10px; color: var(--ink-3); }
.acc-foot { display: flex; flex-wrap: wrap; align-items: center; gap: 4px 16px; margin: 0; font-size: 12.5px; color: var(--ink-3); }
.acc-foot b { color: var(--ink-2); font-weight: 600; }
@media (pointer: coarse) { .mx-head, .mx-cell { min-height: 44px; } }

/* Medium: Away and Verify again beside the status no longer fit one row with the title and Manage (about 780 px of controls in the widest case, Away plus Verify again, before the status gets any room). The head then always has two rows, chosen by the container's width alone and never by what is shown: row 1 chevron, title, Manage at the line end; row 2 the status with Away after it, and Verify again at the line end, as in the wide head, so neither moves the other. Away's margin box (26 + 1 + 5) is as tall as Verify again's (28 + 4), so a row they share keeps its height, and Away its place, when Verify again leaves. The state still shortens with an ellipsis, so no part ever leaves the card. */
@container acc (640px < width <= 63.75rem) {
  .acc-section :deep(.fs-head) { flex-wrap: wrap; row-gap: 0; }
  .acc-section :deep(.fs-head)::after { content: ''; order: 2; flex: 0 0 100%; height: 0; }
  .fs-tools, .fs-act + .fs-tools { order: 1; margin-left: auto; }
  .fs-sum { order: 3; flex: 0 1 auto; min-height: 28px; margin: 0 0 4px; padding-left: 40px; }
  .away { order: 4; margin: 1px 0 5px; }
  .fs-act { order: 5; margin: 0 0 4px auto; }
}

/* Phone: summary keeps one line with a full-text clip tip, above the fix. */
@container acc (width <= 640px) {
  .acc-section :deep(.fs-head) { flex-wrap: wrap; gap: 6px 8px; padding: 6px 10px 6px 4px; }
  .fs-sum { order: 3; flex: 1 1 100%; padding-left: 40px; }
  .fs-sum .st { min-height: 20px; }
  .fs-tools, .fs-act + .fs-tools { margin-left: auto; }
  .fs-tools .btn { min-height: 44px; width: 44px; padding: 0; }
  .fs-tools .tl { display: none; }
  .fs-act { order: 4; flex: 1 1 100%; margin: 4px 0 2px; padding-left: 40px; }
  .fs-act .btn { min-height: 44px; }
  .away { order: 4; height: 44px; }
  .acc-pad { padding: 12px 12px 14px; }
  .matrix .corner { width: 36%; }
  .mx-head { grid-template-columns: minmax(0, 1fr); min-height: 52px; padding: 6px; }
  .mx-head > .vendor, .mx-head > .glyph, .mx-sub .mini, .mx-sub .long { display: none; }
  .mx-head > .mx-sub { grid-column: 1; }
  .mx-sub .short { display: inline; }
  .mx-cell, .mx-none { min-height: 52px; padding: 0 6px; }
}
</style>
