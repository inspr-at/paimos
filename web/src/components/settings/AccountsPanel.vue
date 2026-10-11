<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
// AEON-1171: the docked panel of Settings › Accounts, Computers and Capacity. Each
// part puts its page into the default slot; the panel (an account or a computer),
// its menus, Rename and confirmations are the same in all three, bound to the
// part's useAccountsSettings.
import { computed, toRefs } from 'vue'
import { displayLanguage } from '../../lib/displayLanguage'
import { sharedByComputers, type OverviewAccount } from '../../lib/accountsOverview'
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
import HostCapacitySettings from './HostCapacitySettings.vue'
import SettingsDockedPanel from './SettingsDockedPanel.vue'
import SettingsPopover from './SettingsPopover.vue'

const props = defineProps<{ ctx: AccountsSettings }>()
const {
  agents, capacity, accounts, owner, manage, open, opener, computer, account, selectedKey, title, busy, focusSignIn, bindRoot, computerState, status, mayVerify,
  verificationBusy, verificationMessage, vendorName, windowLabel, show, refresh, verify, goPacing, cleanupRemoval, renameOpen, renameName, menuOpen,
  menuAnchor, menuItems, menuContext, menuError, openMenu, renameComputer, menuAction, setAccount, primarySignin,
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
</script>

<template>
  <div :ref="bindRoot" id="agent-accounts" class="accounts-computers" :data-part="ctx.part">
    <SettingsDockedPanel v-model:open="open" :title="title" :fact="fact" :icon="computer ? 'monitor' : 'gauge'" :opener="opener" :context-key="owner" :record-key="selectedKey" layout-frame-selector=".settings-page">
      <slot name="head" />
      <p v-if="!manage" class="member-note">Only account owners with management permission can act. This view shows the status available to you.</p>
      <div class="loading-state" role="status"><template v-if="agents.accountsState === 'forbidden'">Accounts are visible to workspace admins.</template><template v-else-if="agents.accountsState === 'error' || capacity.computersState === 'error'">Accounts or computers could not be loaded. <button type="button" class="btn sm" @click="refresh">Try again</button></template><template v-else-if="!capacity.loaded">Loading accounts and computers…</template><template v-else-if="capacity.stale || capacity.computersStale">Some readings may be out of date. <button type="button" class="btn sm" @click="refresh">Refresh</button></template></div>
      <slot />
      <template #overflow><button v-if="manage && (computer || account)" class="icon-btn" type="button" :aria-label="`More actions for ${title}`" @click="openMenu(computer ? { kind: 'computer', computer } : { kind: 'account', account: account! }, $event)"><AppIcon name="more" /></button></template>
      <template #panel>
        <template v-if="computer">
          <div v-if="primarySignin" class="needs"><AppIcon name="alert" /><strong>{{ vendorName(primarySignin.enrollment.harness) }} needs verifying here</strong><p>Other ready sign-ins keep working on this computer.</p></div>
          <div v-if="computer.computer_state === 'revoked'" class="needs"><AppIcon name="shield" /><strong>Removed · {{ computer.local_cleanup === 'pending' ? 'cleanup pending' : 'cleanup confirmed' }}</strong><p>New work is blocked. Local deletion and open run accounting remain separate.</p></div>
          <section v-if="computer.computer_state !== 'revoked'" class="p-sec"><div class="p-head"><h3>Right now</h3><span>{{ computer.host_capacity?.reported_at ? `Reported ${when(computer.host_capacity.reported_at, agents.now)}` : 'No load report yet' }}</span></div><p class="now-line">{{ computer.computer_state === 'draining' ? 'Draining · new starts blocked' : computer.host_capacity?.reason ? `Busy · new starts are waiting: ${hostCapacityReason(computer.host_capacity.reason)}` : computer.host_capacity?.signals ? 'New starts allowed' : 'Host capacity not reported' }}</p><p class="now-copy">Running agents keep working. Only new starts wait for room.</p><figure class="spark"><svg v-if="spark" viewBox="0 0 240 48" preserveAspectRatio="none" role="img" aria-label="Reported one-minute host load over the last hour"><path :d="spark" /><line v-if="sparkLimit !== null" x1="0" x2="240" :y1="sparkLimit" :y2="sparkLimit" /></svg><p v-else>Load history not available yet</p><figcaption>One-minute load, last hour<template v-if="sparkLimit !== null"> · dashed line: limit {{ computer.host_capacity?.load_limit.toFixed(1) }}</template></figcaption></figure><dl class="facts"><div><dt>Agents</dt><dd>{{ computer.host_capacity?.running ?? 'Unknown' }}<small v-if="computer.host_capacity?.policy.maximum_agents"> of {{ computer.host_capacity.policy.maximum_agents }}</small></dd><p>{{ computer.host_capacity?.queued ?? 'Unknown' }} queued</p></div><div><dt>Load</dt><dd>{{ computer.host_capacity?.signals?.load?.toFixed(1) ?? 'Unknown' }}</dd><p>{{ computer.host_capacity?.signals?.cores ?? 'Unknown' }} cores</p></div><div><dt>Memory</dt><dd>{{ computer.host_capacity?.signals?.memory_used_gb?.toFixed(1) ?? 'Unknown' }}<small v-if="computer.host_capacity?.signals?.memory_total_gb"> / {{ computer.host_capacity.signals.memory_total_gb.toFixed(0) }} GB</small></dd><p>Pressure {{ computer.host_capacity?.signals?.memory_pressure ?? 'unknown' }}</p></div><div><dt>Power</dt><dd>{{ computer.host_capacity?.signals?.power === 'plugged_in' ? 'Plugged in' : computer.host_capacity?.signals?.power === 'battery' ? 'Battery' : 'Unknown' }}</dd><p>Temperature {{ computer.host_capacity?.signals?.thermal ?? 'unknown' }}</p></div></dl></section>
          <section v-else class="p-sec"><h3>Cleanup</h3><ul class="steps"><li><AppIcon name="check" />New work blocked</li><li><AppIcon :name="computer.local_cleanup === 'confirmed' ? 'check' : 'clock'" />Local sign-in deletion {{ cleanupText(computer.local_cleanup) }}</li><li><AppIcon :name="computer.accounting_state === 'settled' ? 'check' : 'clock'" />Run accounting {{ cleanupText(computer.accounting_state) }}</li></ul><h4>If it is lost or stolen</h4><p>Sign out of all devices at each vendor, or wipe the computer, before finishing cleanup.</p><ul class="vendor-steps"><li v-for="e in computer.enrollments" :key="e.account_id"><HarnessMark :harness="e.harness" /><span>{{ accounts.find(a => a.records.some(r => r.id === e.account_id))?.vendor ?? vendorName(e.harness) }} · {{ e.label }}</span></li></ul></section>
          <section class="p-sec"><div class="p-head"><h3>Sign-ins on this computer</h3><span>Quota is shared per account</span></div><div v-for="e in computer.enrollments" :key="e.account_id" class="si-row" :class="{ 'is-target': focusSignIn === e.account_id }" :data-signin="e.account_id" tabindex="-1"><HarnessMark :harness="e.harness" /><div class="si-who"><button type="button" class="name-link" v-clip-tip @click="show('account', accounts.find(a => a.records.some(r => r.id === e.account_id))?.id ?? e.account_id)">{{ accounts.find(a => a.records.some(r => r.id === e.account_id))?.vendor ?? vendorName(e.harness) }} · {{ e.label }}</button></div><span class="st si-status">{{ status(computer, e.account_id) }}</span><button v-if="manage && e.can_verify && e.state !== 'revoked'" type="button" class="icon-btn" :aria-label="`More for ${vendorName(e.harness)} on ${computer.computer_name}`" @click="openMenu({ kind: 'signin', signin: { computer, enrollment: e } }, $event)"><AppIcon name="more" /></button><p v-if="verificationMessage({ computer, enrollment: e })" class="si-feedback" role="status">{{ verificationMessage({ computer, enrollment: e }) }}</p><p class="si-meta">Local cleanup {{ cleanupText(e.local_cleanup) }}<template v-if="e.verified_at"> · verified {{ when(e.verified_at, agents.now) }}</template><template v-else-if="!verificationMessage({ computer, enrollment: e })"> · Verification: {{ verificationWords({ computer, enrollment: e }) }}</template><template v-if="e.verification_expires_at && e.verification_state !== 'not_selected'"> · expires {{ when(e.verification_expires_at, agents.now) }}</template><template v-if="e.last_used_at"> · used {{ when(e.last_used_at, agents.now) }}</template><template v-if="e.active_run_ids.length"> · {{ e.active_run_ids.length }} running</template></p></div></section>
          <HostCapacitySettings :computer="computer" :manage="manage && computer.enrollments.some(e => e.can_verify)" :owner-key="owner" @saved="refresh" />
        </template>
        <template v-else-if="account">
          <div v-if="primarySignin" class="needs"><AppIcon name="alert" /><strong>Verification on {{ primarySignin.computer.computer_name }}</strong><p role="status">{{ verificationMessage(primarySignin) || verificationWords(primarySignin) }}</p></div>
          <section class="p-sec"><div class="p-head"><h3>Shared quota</h3><span>{{ sharedByComputers(account, displayLanguage()) }}</span></div><div class="quota"><div v-for="w in account.windows" :key="w.reading.window_kind + w.reading.bucket" class="q-row"><span>{{ windowLabel(w) }}</span><b>{{ Math.round(w.remaining_percent) }}% left</b><div class="gauge" role="meter" :aria-label="`${account.vendor} ${windowLabel(w)} remaining`" aria-valuemin="0" aria-valuemax="100" :aria-valuenow="w.remaining_percent"><i :style="{ width: `${w.remaining_percent}%` }"></i></div><small>Resets {{ when(w.reading.resets_at, agents.now) }}</small><small :class="{ stale: w.freshness !== 'fresh' }">Read {{ when(w.reading.read_at, agents.now) }} on {{ quotaSource(account, w) }}<template v-if="w.freshness !== 'fresh'"> · may be out of date</template></small></div><p v-if="!account.windows.length">Quota has not been reported. No remaining allowance is inferred.</p></div><p class="q-note">Every computer signed in with this account draws from the same quota.</p></section>
          <section class="p-sec"><div class="p-head"><h3>Signed in on</h3><span>Details and actions live with each computer</span></div><button v-for="s in account.signins" :key="`${s.computer.computer_id}:${s.enrollment.account_id}`" type="button" class="ref-row" @click="show('computer', s.computer.computer_id!, undefined, s.enrollment.account_id)"><AppIcon name="monitor" /><span><b>{{ s.computer.computer_name }}</b><small>{{ computerState(s.computer) }}</small></span><span class="st">{{ status(s.computer, s.enrollment.account_id) }}</span><AppIcon name="chevron-right" /></button><p v-if="!account.signins.length">No paired computer is associated with this account.</p></section>
          <section class="p-sec use-sec"><div class="p-head"><h3>Use and limits</h3><span>{{ account.records.length === 1 ? 'One login' : `${account.records.length} logins share this quota` }}</span></div><AccountUsagePosture v-for="record in account.records" :key="record.id" :account-id="record.id" :german="displayLanguage() === 'de'" @changed="refresh" /><AccountsCard :accounts="account.records" :all="agents.accounts" state="ready" :now="agents.now" :admin="manage" :set="setAccount" @away="goPacing" /></section>
          <RouterLink class="models-link" to="/agents/models" aria-label="Agents, Models">Agents<AppIcon name="chevron-right" :size="12" />Models</RouterLink>
        </template>
      </template>
      <template v-if="manage && (primarySignin || computer?.computer_state === 'revoked')" #footer><template v-if="primarySignin"><p>{{ vendorName(primarySignin.enrollment.harness) }} on {{ primarySignin.computer.computer_name }}</p><VerificationButton class="primary" :disabled="busy || !mayVerify(primarySignin)" :busy="verificationBusy(primarySignin)" @click="verify(primarySignin)" /></template><template v-else-if="computer"><p>{{ cleanupRemoval(computer).reason || 'Only needed if it won’t come back' }}</p><button type="button" class="btn primary" :disabled="!cleanupRemoval(computer).allowed" @click="openMenu({ kind: 'cleanup', computer }, $event)">Finish cleanup…</button></template></template>
    </SettingsDockedPanel>
    <SettingsPopover v-model:open="renameOpen" :anchor="menuAnchor" label="Rename computer" mode="form" :context-key="selectedKey" :busy="busy" :error="menuError" @submit="renameComputer"><label class="rename-field">Name<input v-model="renameName" maxlength="128" :disabled="busy" /></label></SettingsPopover>
    <SettingsPopover v-model:open="menuOpen" :anchor="menuAnchor" :label="`Actions for ${title}`" :items="menuItems" :context-key="menuContext" :busy="busy" :error="menuError" @select="menuAction" />
  </div>
</template>

<style scoped src="../../styles/accountsSettings.css"></style>
