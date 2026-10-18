<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
// AEON-1171: the docked panel of Settings › Accounts, Computers and Capacity. Each
// part puts its page into the default slot; the panel (an account or a computer),
// its menus, Rename and confirmations are the same in all three, bound to the
// part's useAccountsSettings.
import { computed, nextTick, ref, toRefs, watch } from 'vue'
import { useRouter } from 'vue-router'
import { displayLanguage } from '../../lib/displayLanguage'
import { accountName, accountPlan } from '../../lib/accountCascade'
import { removeLimit, ruleWords } from '../../lib/accountLimits'
import { sharedByComputers, type OverviewAccount, type SignInReference } from '../../lib/accountsOverview'
import type { AgentAccount } from '../../lib/agents'
import { quotaSentence, widestSentences } from '../../lib/quotaBar'
import { toast } from '../../lib/toast'
import { platformCaption } from '../../lib/agentPairing'
import { hostCapacityReason } from '../../lib/hostCapacity'
import { when, type CapacityWindow } from '../../lib/capacity'
import { verificationWords } from '../../lib/useVerification'
import type { AccountsSettings } from '../../lib/useAccountsSettings'
import { vClipTip } from '../../directives/clipTip'
import AppIcon from '../AppIcon.vue'
import AccountsCard from '../agents/AccountsCard.vue'
import HarnessMark from '../agents/HarnessMark.vue'
import VerificationButton from '../agents/VerificationButton.vue'
import AccountUsagePosture from './AccountUsagePosture.vue'
import QuotaBar from './QuotaBar.vue'
import QuotaLegend from './QuotaLegend.vue'
import HostCapacitySettings from './HostCapacitySettings.vue'
import SettingsDockedPanel from './SettingsDockedPanel.vue'
import SettingsPopover from './SettingsPopover.vue'

const props = defineProps<{ ctx: AccountsSettings }>()
const {
  agents, capacity, accounts, owner, manage, open, opener, computer, account, selectedKey, title, busy, focusSignIn, bindRoot, computerState, status, mayVerify,
  verificationBusy, verificationMessage, vendorName, windowLabel, show, refresh, verify, goPacing, cleanupRemoval, renameOpen, renameName, menuOpen,
  menuAnchor, menuItems, menuContext, menuError, openMenu, renameComputer, menuAction, setAccount, primarySignin, quota, paceWords, planFailed, heldOf, endHeld,
} = toRefs(props.ctx)
const fact = computed(() => computer.value ? `${computerState.value(computer.value)} · ${platformCaption(computer.value.platform, computer.value.arch)}` : account.value?.identity)
const cleanupText = (value?: string) => ({ confirmed: 'confirmed', pending: 'pending', settled: 'settled', unconfirmed: 'awaiting confirmation' }[value ?? ''] ?? 'not reported')
const spark = computed(() => {
  const points = computer.value?.host_capacity?.history ?? []
  if (points.length < 2) return ''
  const maximum = Math.max(1, ...points.map(p => p.load), computer.value?.host_capacity?.load_limit ?? 0)
  return points.map((p, i) => `${i ? 'L' : 'M'}${i * 240 / (points.length - 1)} ${48 - p.load / maximum * 44}`).join(' ')
})
const sparkLimit = computed(() => { const v = computer.value?.host_capacity; if (!v?.load_limit) return null; return 48 - v.load_limit / Math.max(1, v.load_limit, ...v.history.map(p => p.load)) * 44 })
function quotaSource(a: OverviewAccount, w: CapacityWindow) { const row = a.rows.find(r => [r.primary, r.five].some(candidate => candidate?.reading.read_at === w.reading.read_at)); return row?.host || 'reported reader' }

// ---------- The account: Quota, then "Limits on this account" (AEON-1172, design AEON-1149) ----------
const router = useRouter()
/** A sign-in opens Computers at that sign-in; inside Computers its panel opens in place. */
function openSignin(s: SignInReference) {
  if (props.ctx.part === 'computers') { void show.value('computer', s.computer.computer_id!, undefined, s.enrollment.account_id); return }
  void router.push({ path: '/settings/computers', query: { computer: s.computer.computer_id!, signin: s.enrollment.account_id } })
}
const shown = computed(() => account.value ? quota.value(account.value) : null)
const said = computed(() => account.value && shown.value ? quotaSentence(shown.value.bar, { vendor: account.value.vendor, atLimit: shown.value.atLimit }) : [])
// The sentence keeps the height of the longest one its Limits can reach: the controls below never move.
const sizers = computed(() => account.value && shown.value ? widestSentences(account.value.vendor, shown.value.bar.known) : [])
// The legend names Keep for you whenever the person keeps something, so a hold ending or a Sprint never re-wraps it.
const keeps = computed(() => capacity.value.schedule.reserve !== 'off')
const readWindow = computed(() => shown.value?.window ?? account.value?.windows[0] ?? null)
const planLine = computed(() => account.value?.records.map(r => accountPlan(r)).find(Boolean) ?? account.value?.rows.find(r => r.plan)?.plan ?? '')
const held = computed(() => account.value ? heldOf.value(account.value) : null)
const allowed = (r: AgentAccount) => r.state === 'available' && r.ongoing_use_approved !== false
const ruleOf = (r: AgentAccount) => capacity.value.byAccount.get(r.id)?.limit
const ruleKey = (r: AgentAccount) => `${r.id}:${ruleOf(r)?.id ?? ''}`
// A removed rule or an ended Hold keeps its row, saying so, until the panel shows another
// account or closes: "Agents may use it" and everything below stay put after the write
// (AEON-541). The row is kept from the moment the person confirms, as it was, because a
// read can land before the write answers (the capacity read after it lands before the
// others). Each row holds the height of its tallest state and its action slot the width
// of its widest, so asking, confirming and the result never move a neighbour either.
interface Words { title: string; text: string }
// Removing an older rule asks in place, without Undo: the server refuses to create one again.
// Focus moves to Keep (the safe choice) and back to Remove when the question closes.
const confirmRule = ref(''), useBusy = ref('')
const removed = ref(new Map<string, { words: string; done: boolean }>()), ended = ref<(Words & { key: string; done: Words; settled: boolean }) | null>(null)
const RULE_TEXT = { ask: 'An older limit on this account only. It still applies until you remove it; the daily limit does this for every account now.', confirm: 'Remove it for good? This kind of limit can’t be set again; the daily limit paces every account.', done: 'Removed for good. The daily limit paces this account now.' }
function ruleRow(r: AgentAccount) {
  // A removal under way stays as confirmed; one the server confirmed shows at once, before the next read drops the rule.
  const rule = ruleOf(r), gone = removed.value.get(r.id), words = gone?.words ?? (rule ? ruleWords(rule) : '')
  if (!words) return null
  const state = gone?.done ? 'done' as const : gone || confirmRule.value === ruleKey(r) ? 'confirm' as const : 'ask' as const
  return { state, title: `Set by you · ${words}`, text: RULE_TEXT[state], sizers: Object.values(RULE_TEXT).filter(t => t !== RULE_TEXT[state]) }
}
const limitRows = computed(() => account.value?.records.map(record => ({ record, rule: ruleRow(record) })) ?? [])
const heldRow = computed(() => {
  const live = held.value, gone = ended.value
  // The hold being ended shows as it was, then as ended; a different one in force since shows as it is.
  if (gone && (!live || live.key === gone.key)) return gone.settled ? { state: 'done' as const, ...gone.done, sizers: [{ title: gone.title, text: gone.text }] } : { state: 'held' as const, title: gone.title, text: gone.text, sizers: [gone.done] }
  return live ? { state: 'held' as const, title: live.title, text: live.text, sizers: [heldDone(live.kind)] } : null
})
function heldDone(kind: string): Words { return kind === 'sprint' ? { title: 'Sprint ended', text: 'The daily limit paces it again.' } : { title: 'Hold ended', text: 'Agents may use it again.' } }
async function endHold(a: OverviewAccount) {
  const h = held.value, screen = selectedKey.value
  if (!h || busy.value) return
  const ending = { key: h.key, title: h.title, text: h.text, done: heldDone(h.kind), settled: false }
  ended.value = ending
  const ok = await endHeld.value(a, h.key)
  if (screen !== selectedKey.value) return
  // A failure says so in its toast and the row shows the hold as it is now.
  if (ok) ended.value = { ...ending, settled: true }
  else if (ended.value?.key === h.key && !ended.value.settled) ended.value = null
}
function forget() { confirmRule.value = ''; useBusy.value = ''; removed.value = new Map(); ended.value = null }
watch(selectedKey, forget)
watch(open, shown => { if (!shown) forget() })
async function askRule(r: AgentAccount, open: boolean) {
  confirmRule.value = open ? ruleKey(r) : ''
  await nextTick()
  document.querySelector<HTMLElement>(`section.pane [data-rule="${r.id}"] ${open ? '[data-keep]' : '[data-remove]'}`)?.focus({ preventScroll: true })
}
async function removeRule(r: AgentAccount, seen: string) {
  const identity = owner.value, screen = selectedKey.value, rule = ruleOf(r)
  if (!manage.value || busy.value) return
  if (!rule || ruleKey(r) !== seen) { confirmRule.value = ''; toast('This limit changed. The panel shows it as it is now.', { tone: 'error' }); return }
  busy.value = true
  const words = ruleWords(rule)
  removed.value = new Map(removed.value).set(r.id, { words, done: false })
  try {
    await removeLimit(r.id)
    if (identity !== owner.value || screen !== selectedKey.value) return
    confirmRule.value = ''; removed.value = new Map(removed.value).set(r.id, { words, done: true })
    toast(`Removed the older limit on ${accountName(r)}. The daily limit paces it now.`)
    await agents.value.afterWrite()
  } catch (error) {
    if (identity !== owner.value || screen !== selectedKey.value) return
    // Not removed: the row shows the rule as it is now, and the toast says why.
    if (removed.value.get(r.id)?.done === false) { const next = new Map(removed.value); next.delete(r.id); removed.value = next }
    toast(error instanceof Error ? error.message : 'The limit was not removed. Please try again.', { tone: 'error' })
  }
  finally { if (identity === owner.value) busy.value = false }
}
// The switch shows the saved state until the write is confirmed; turning it off asks first (Drain).
// A busy switch stays focusable (aria-disabled), so focus returns to it when its confirmation closes.
async function toggleUse(r: AgentAccount) {
  const want = !allowed(r)
  if (!manage.value || useBusy.value) return
  const identity = owner.value
  useBusy.value = r.id
  try { await setAccount.value(r, want ? 'available' : 'draining') }
  catch (error) { if (identity === owner.value) toast(error instanceof Error ? error.message : 'The account did not change. Please try again.', { tone: 'error' }) }
  finally { if (identity === owner.value) useBusy.value = '' }
}
</script>

<template>
  <div :ref="bindRoot" id="agent-accounts" class="accounts-computers" :data-part="ctx.part">
    <SettingsDockedPanel v-model:open="open" :title="title" :fact="fact" :icon="computer ? 'monitor' : 'gauge'" :opener="opener" :context-key="owner" :record-key="selectedKey" layout-frame-selector=".settings-page">
      <slot name="head" />
      <p v-if="!manage" class="member-note">Only account owners with management permission can act. This view shows the status available to you.</p>
      <div class="loading-state" role="status"><template v-if="agents.accountsState === 'forbidden'">Accounts are visible to workspace admins.</template><template v-else-if="agents.accountsState === 'error' || capacity.computersState === 'error'">Accounts or computers could not be loaded. <button type="button" class="btn sm" @click="refresh">Try again</button></template><template v-else-if="!capacity.loaded">Loading accounts and computers…</template><template v-else-if="capacity.stale || capacity.computersStale || planFailed">Some readings may be out of date. <button type="button" class="btn sm" @click="refresh">Refresh</button></template></div>
      <slot />
      <template #overflow><button v-if="manage && (computer || account)" class="icon-btn" type="button" :aria-label="`More actions for ${title}`" @click="openMenu(computer ? { kind: 'computer', computer } : { kind: 'account', account: account! }, $event)"><AppIcon name="more" /></button></template>
      <template #panel>
        <template v-if="computer">
          <div v-if="primarySignin" class="needs"><AppIcon name="alert" /><strong>{{ vendorName(primarySignin.enrollment.harness) }} needs verifying here</strong><p>Other ready sign-ins keep working on this computer.</p></div>
          <div v-if="computer.computer_state === 'revoked'" class="needs"><AppIcon name="shield" /><strong>Removed · {{ computer.local_cleanup === 'pending' ? 'cleanup pending' : 'cleanup confirmed' }}</strong><p>New work is blocked. Local deletion and open run accounting remain separate.</p></div>
          <section v-if="computer.computer_state !== 'revoked'" class="p-sec"><div class="p-head"><h3>Right now</h3><span>{{ computer.host_capacity?.reported_at ? `Reported ${when(computer.host_capacity.reported_at, agents.now)}` : 'No load report yet' }}</span></div><p class="now-line">{{ computer.computer_state === 'draining' ? 'Draining · new starts blocked' : computer.host_capacity?.reason ? `Busy · new starts are waiting: ${hostCapacityReason(computer.host_capacity.reason)}` : computer.host_capacity?.signals ? 'New starts allowed' : 'Host capacity not reported' }}</p><p class="now-copy">Running agents keep working. Only new starts wait for room.</p><figure class="spark"><svg v-if="spark" viewBox="0 0 240 48" preserveAspectRatio="none" role="img" aria-label="Reported one-minute host load over the last hour"><path :d="spark" /><line v-if="sparkLimit !== null" x1="0" x2="240" :y1="sparkLimit" :y2="sparkLimit" /></svg><p v-else>No load history available</p><figcaption>One-minute load, last hour<template v-if="sparkLimit !== null"> · dashed line: limit {{ computer.host_capacity?.load_limit.toFixed(1) }}</template></figcaption></figure><dl class="facts"><div><dt>Agents</dt><dd>{{ computer.host_capacity?.running ?? 'Unknown' }}<small v-if="computer.host_capacity?.policy.maximum_agents"> of {{ computer.host_capacity.policy.maximum_agents }}</small></dd><p>{{ computer.host_capacity?.queued ?? 'Unknown' }} queued</p></div><div><dt>Load</dt><dd>{{ computer.host_capacity?.signals?.load?.toFixed(1) ?? 'Unknown' }}</dd><p>{{ computer.host_capacity?.signals?.cores ?? 'Unknown' }} cores</p></div><div><dt>Memory</dt><dd>{{ computer.host_capacity?.signals?.memory_used_gb?.toFixed(1) ?? 'Unknown' }}<small v-if="computer.host_capacity?.signals?.memory_total_gb"> / {{ computer.host_capacity.signals.memory_total_gb.toFixed(0) }} GB</small></dd><p>Pressure {{ computer.host_capacity?.signals?.memory_pressure ?? 'unknown' }}</p></div><div><dt>Power</dt><dd>{{ computer.host_capacity?.signals?.power === 'plugged_in' ? 'Plugged in' : computer.host_capacity?.signals?.power === 'battery' ? 'Battery' : 'Unknown' }}</dd><p>Temperature {{ computer.host_capacity?.signals?.thermal ?? 'unknown' }}</p></div></dl></section>
          <section v-else class="p-sec"><h3>Cleanup</h3><ul class="steps"><li><AppIcon name="check" />New work blocked</li><li><AppIcon :name="computer.local_cleanup === 'confirmed' ? 'check' : 'clock'" />Local sign-in deletion {{ cleanupText(computer.local_cleanup) }}</li><li><AppIcon :name="computer.accounting_state === 'settled' ? 'check' : 'clock'" />Run accounting {{ cleanupText(computer.accounting_state) }}</li></ul><h4>If it is lost or stolen</h4><p>Sign out of all devices at each vendor, or wipe the computer, before finishing cleanup.</p><ul class="vendor-steps"><li v-for="e in computer.enrollments" :key="e.account_id"><HarnessMark :harness="e.harness" /><span>{{ accounts.find(a => a.records.some(r => r.id === e.account_id))?.vendor ?? vendorName(e.harness) }} · {{ e.label }}</span></li></ul></section>
          <section class="p-sec"><div class="p-head"><h3>Sign-ins on this computer</h3><span>Quota is shared per account</span></div><div v-for="e in computer.enrollments" :key="e.account_id" class="si-row" :class="{ 'is-target': focusSignIn === e.account_id }" :data-signin="e.account_id" tabindex="-1"><HarnessMark :harness="e.harness" /><div class="si-who"><button type="button" class="name-link" v-clip-tip @click="show('account', accounts.find(a => a.records.some(r => r.id === e.account_id))?.id ?? e.account_id)">{{ accounts.find(a => a.records.some(r => r.id === e.account_id))?.vendor ?? vendorName(e.harness) }} · {{ e.label }}</button></div><span class="st si-status">{{ status(computer, e.account_id) }}</span><button v-if="manage && e.can_verify && e.state !== 'revoked'" type="button" class="icon-btn" :aria-label="`More for ${vendorName(e.harness)} on ${computer.computer_name}`" @click="openMenu({ kind: 'signin', signin: { computer, enrollment: e } }, $event)"><AppIcon name="more" /></button><p v-if="verificationMessage({ computer, enrollment: e })" class="si-feedback" role="status">{{ verificationMessage({ computer, enrollment: e }) }}</p><p class="si-meta">Local cleanup {{ cleanupText(e.local_cleanup) }}<template v-if="e.verified_at"> · verified {{ when(e.verified_at, agents.now) }}</template><template v-else-if="!verificationMessage({ computer, enrollment: e })"> · Verification: {{ verificationWords({ computer, enrollment: e }) }}</template><template v-if="e.verification_expires_at && e.verification_state !== 'not_selected'"> · expires {{ when(e.verification_expires_at, agents.now) }}</template><template v-if="e.last_used_at"> · used {{ when(e.last_used_at, agents.now) }}</template><template v-if="e.active_run_ids.length"> · {{ e.active_run_ids.length }} running</template></p></div></section>
          <HostCapacitySettings :computer="computer" :manage="manage && computer.enrollments.some(e => e.can_verify)" :owner-key="owner" @saved="refresh" />
        </template>
        <template v-else-if="account">
          <div v-if="primarySignin" class="needs"><AppIcon name="alert" /><strong>Verification on {{ primarySignin.computer.computer_name }}</strong><p role="status">{{ verificationMessage(primarySignin) || verificationWords(primarySignin) }}</p></div>
          <section class="p-sec" data-quota><div class="p-head"><h3>Quota</h3><span>{{ sharedByComputers(account, displayLanguage()) }}</span></div><QuotaBar v-if="shown" size="lg" :bar="shown.bar" :label="shown.label" /><QuotaLegend v-if="shown?.bar.known" class="q-legend" :keep="keeps" /><div class="p-say-slot"><p class="p-say" data-quota-say><template v-for="(part, i) in said" :key="i"><b v-if="part.strong">{{ part.text }}</b><template v-else>{{ part.text }}</template></template></p><p v-for="(parts, i) in sizers" :key="`sizer-${i}`" class="p-say sizer" aria-hidden="true"><template v-for="(part, j) in parts" :key="j"><b v-if="part.strong">{{ part.text }}</b><template v-else>{{ part.text }}</template></template></p></div><dl class="p-win"><div v-for="w in account.windows" :key="w.reading.window_kind + w.reading.bucket"><dt>{{ windowLabel(w) }}</dt><dd><b>{{ Math.round(w.remaining_percent) }} % left</b> · resets {{ when(w.reading.resets_at, agents.now) }}</dd></div><div v-if="readWindow"><dt>Read</dt><dd :class="{ stale: account.windows.some(w => w.freshness !== 'fresh') }">{{ when(readWindow.reading.read_at, agents.now) }} on {{ quotaSource(account, readWindow) }}<template v-if="account.windows.some(w => w.freshness !== 'fresh')"> · may be out of date</template></dd></div><div v-if="planLine"><dt>Plan</dt><dd>{{ planLine }}</dd></div></dl></section>
          <section class="p-sec lim-sec" data-limits><div class="p-head"><h3>Limits on this account</h3><span>{{ manage ? 'For this account only' : 'Read only · set by its owner' }}</span></div>
            <AccountUsagePosture v-for="({ record, rule }, i) in limitRows" :key="record.id" :account-id="record.id" :who="account.records.length > 1 ? accountName(record) : ''" :pace="i === 0 ? paceWords(account) : ''" :german="displayLanguage() === 'de'" @changed="refresh">
              <div v-if="rule" class="lim legacy" :class="{ settled: rule.state === 'done' }" :data-rule="record.id"><span class="lbl stack"><span class="say" data-say><b>{{ rule.title }}</b><small>{{ rule.text }}</small></span><span v-for="(text, j) in rule.sizers" :key="j" class="say sizer" aria-hidden="true"><b>{{ rule.title }}</b><small>{{ text }}</small></span></span><span v-if="manage" class="act"><span v-if="rule.state === 'confirm'" class="two-btn"><button type="button" class="btn sm" data-keep :disabled="busy" @click="askRule(record, false)">Keep</button><button type="button" class="btn sm danger" :disabled="busy" @click="removeRule(record, ruleKey(record))">Remove</button></span><button v-else-if="rule.state === 'ask'" type="button" class="btn sm" data-remove :disabled="busy" @click="askRule(record, true)">Remove</button><span v-else class="done"><AppIcon name="check" :size="14" />Removed</span><span class="two-btn sizer" aria-hidden="true"><span class="btn sm">Keep</span><span class="btn sm">Remove</span></span></span></div>
            </AccountUsagePosture>
            <div v-if="heldRow" class="lim legacy" :class="{ settled: heldRow.state === 'done' }" data-held><span class="lbl stack"><span class="say" data-say><b>{{ heldRow.title }}</b><small>{{ heldRow.text }}</small></span><span v-for="(words, i) in heldRow.sizers" :key="i" class="say sizer" aria-hidden="true"><b>{{ words.title }}</b><small>{{ words.text }}</small></span></span><span v-if="manage" class="act"><button v-if="heldRow.state === 'held'" type="button" class="btn sm" :disabled="busy" @click="endHold(account)">End now</button><span v-else class="done"><AppIcon name="check" :size="14" />Ended</span><span class="btn sm sizer" aria-hidden="true">End now</span></span></div>
            <div v-for="record in account.records" :key="`use:${record.id}`" class="lim" :data-use="record.id"><span class="lbl"><b>Agents may use it</b><small>{{ account.records.length > 1 ? `${accountName(record)} · ` : '' }}Off: agents leave this account alone. Running work finishes.</small></span><button type="button" role="switch" class="use" :aria-checked="allowed(record)" :aria-label="`Agents may use it · ${accountName(record)}`" :disabled="!manage" :aria-disabled="useBusy === record.id || undefined" :data-tip="manage ? undefined : 'People who can manage accounts change this'" @click="toggleUse(record)"><span class="track" aria-hidden="true"><span class="thumb" /></span></button></div>
          </section>
          <section class="p-sec"><div class="p-head"><h3>Signed in on</h3><span>Details and actions live with each computer</span></div><button v-for="s in account.signins" :key="`${s.computer.computer_id}:${s.enrollment.account_id}`" type="button" class="ref-row" @click="openSignin(s)"><AppIcon name="monitor" /><span><b>{{ s.computer.computer_name }}</b><small>{{ computerState(s.computer) }}</small></span><span class="st">{{ status(s.computer, s.enrollment.account_id) }}</span><AppIcon name="chevron-right" /></button><p v-if="!account.signins.length">No paired computer is associated with this account.</p></section>
          <section class="p-sec use-sec"><div class="p-head"><h3>Logins</h3><span>{{ account.records.length === 1 ? 'One login' : `${account.records.length} logins share this quota` }}</span></div><AccountsCard :accounts="account.records" :all="agents.accounts" state="ready" :now="agents.now" :admin="manage" :preserve-readiness="!!heldRow" @away="goPacing" /></section>
        </template>
      </template>
      <template v-if="manage && (primarySignin || computer?.computer_state === 'revoked')" #footer><template v-if="primarySignin"><p>{{ vendorName(primarySignin.enrollment.harness) }} on {{ primarySignin.computer.computer_name }}</p><VerificationButton class="primary" :disabled="busy || !mayVerify(primarySignin)" :busy="verificationBusy(primarySignin)" @click="verify(primarySignin)" /></template><template v-else-if="computer"><p>{{ cleanupRemoval(computer).reason || 'Only needed if it won’t come back' }}</p><button type="button" class="btn primary" :disabled="!cleanupRemoval(computer).allowed" @click="openMenu({ kind: 'cleanup', computer }, $event)">Finish cleanup…</button></template></template>
    </SettingsDockedPanel>
    <SettingsPopover v-model:open="renameOpen" :anchor="menuAnchor" label="Rename computer" mode="form" :context-key="selectedKey" :busy="busy" :error="menuError" @submit="renameComputer"><label class="rename-field">Name<input v-model="renameName" maxlength="128" :disabled="busy" /></label></SettingsPopover>
    <SettingsPopover v-model:open="menuOpen" :anchor="menuAnchor" :label="`Actions for ${title}`" :items="menuItems" :context-key="menuContext" :busy="busy" :error="menuError" @select="menuAction" />
  </div>
</template>

<style scoped src="../../styles/accountsSettings.css"></style>
<style scoped>
/* An older rule or a hold keeps the docked panel's full width for its words, so "Set by you · at most 20 % a day"
   reads on one line, and its Remove or End now sits under them (AEON-1240); each still reserves its tallest state. */
@container lim (max-width: 600px) {
  .lim-sec .lim.legacy { grid-template-columns: minmax(0, 1fr); row-gap: 8px; }
  .lim-sec .lim.legacy > .act { justify-self: start; justify-items: start; }
}
</style>
