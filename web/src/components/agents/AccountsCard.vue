<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, ref } from 'vue'
import type { AgentAccount } from '../../lib/agents'
import { accountName, accountPlan, allowanceWindowLabel } from '../../lib/accountCascade'
import { PACE_LABEL, UNIT_LABEL, bindingWindow, duration, harnessLabel } from '../../lib/agentState'
import type { Availability } from '../../stores/agents'
import AppIcon from '../AppIcon.vue'

// Per account: the window that binds first, how much of it is left and whether use
// is running ahead of the pace the window allows. Admins can drain or resume.
const props = defineProps<{ accounts: AgentAccount[]; state: Availability; now: number; admin: boolean; set: (account: AgentAccount, state: AgentAccount['state']) => Promise<void> }>()
const busy = ref('')
const error = ref('')
const rows = computed(() => [...props.accounts]
  .map(account => ({ account, window: bindingWindow(account.windows, props.now) }))
  .sort((a, b) => (a.account.state === 'available' ? 0 : 1) - (b.account.state === 'available' ? 0 : 1) || allowanceRank(a.window) - allowanceRank(b.window)))
function allowanceRank(window: ReturnType<typeof bindingWindow>) {
  if (!window) return 2
  if (window.window.provisional) return 1.5
  return window.left
}
async function toggle(account: AgentAccount) {
  busy.value = account.id; error.value = ''
  try { await props.set(account, account.state === 'draining' ? 'available' : 'draining') }
  catch (e) { error.value = e instanceof Error ? e.message : 'The account did not change. Please try again.' }
  finally { busy.value = '' }
}
const stateLabel: Record<AgentAccount['state'], string> = { available: 'Available', draining: 'Draining', unavailable: 'Unavailable' }
</script>

<template>
  <section class="accounts glass-card" aria-labelledby="accounts-title">
    <header class="card-head">
      <h2 id="accounts-title"><AppIcon name="gauge" :size="15" />Accounts and pacing</h2>
    </header>
    <p v-if="state === 'forbidden'" class="note">Accounts and their allowances are visible to workspace admins.</p>
    <p v-else-if="state === 'error'" class="note" role="alert">Accounts could not be loaded right now.</p>
    <div v-else-if="state === 'idle'" class="sk"><span class="skeleton" /><span class="skeleton short" /><span class="skeleton" /></div>
    <p v-else-if="!accounts.length" class="note">No accounts are enrolled. The local agent daemon enrolls one per harness sign-in.</p>
    <ul v-else class="list">
      <li v-for="{ account, window } in rows" :key="account.id" class="account" :class="account.state">
        <div class="top">
          <span class="dot" :class="account.state" aria-hidden="true" />
          <span class="label">{{ accountName(account) }}</span>
          <span v-if="accountPlan(account)" class="plan">{{ accountPlan(account) }}</span>
          <span class="harness">{{ harnessLabel(account.harness) }}</span>
          <span class="spacer" />
          <span v-if="account.state !== 'available'" class="state-text">{{ stateLabel[account.state] }}</span>
          <button v-if="admin && account.state !== 'unavailable'" type="button" class="btn sm ghost toggle" :disabled="busy === account.id" @click="toggle(account)">{{ account.state === 'draining' ? 'Resume' : 'Drain' }}</button>
        </div>
        <template v-if="window?.window.provisional">
          <p class="facts">
            <span class="left">{{ allowanceWindowLabel(window.window) }} {{ UNIT_LABEL[window.window.unit] }} · allowance unknown</span>
            <span class="provisional">Unmeasured</span>
            <span class="reset">resets in {{ duration(window.resetsIn) }}</span>
          </p>
        </template>
        <template v-else-if="window">
          <div class="meter" role="meter" :aria-valuenow="Math.round(window.left * 100)" aria-valuemin="0" aria-valuemax="100" :aria-label="`${accountName(account)}: ${Math.round(window.left * 100)}% of ${allowanceWindowLabel(window.window)} ${UNIT_LABEL[window.window.unit]} left`">
            <span class="fill" :class="window.pace" :style="{ width: `${Math.max(2, window.left * 100)}%` }" />
            <span class="pace-mark" :style="{ left: `${Math.min(100, (1 - window.expected) * 100)}%` }" :data-tip="`Pace allows ${Math.round(window.expected * 100)}% used by now`" />
          </div>
          <p class="facts">
            <span class="left"><b>{{ Math.round(window.left * 100) }}%</b> {{ UNIT_LABEL[window.window.unit] }} left · {{ allowanceWindowLabel(window.window) }}</span>
            <span class="pace" :class="window.pace">{{ PACE_LABEL[window.pace] }}</span>
            <span class="reset">resets in {{ duration(window.resetsIn) }}</span>
          </p>
        </template>
        <p v-else class="facts muted">No active allowance window</p>
      </li>
    </ul>
    <p v-if="error" class="note error" role="alert"><AppIcon name="alert" :size="13" />{{ error }}</p>
  </section>
</template>

<style scoped>
.accounts { overflow: clip; }
.card-head { padding: 14px 16px 8px; }
.card-head h2 { display: flex; align-items: center; gap: 8px; font-size: 14px; font-weight: 650; }
.card-head svg { color: var(--teal); }
.note { padding: 2px 16px 16px; font-size: 12.5px; color: var(--ink-2); }
.note.error { display: flex; align-items: center; gap: 6px; color: var(--danger); }
.sk { display: grid; gap: 10px; padding: 6px 16px 18px; }
.sk .short { width: 60%; }
.list { margin: 0; padding: 0 8px 8px; list-style: none; display: grid; grid-template-columns: minmax(0, 1fr); gap: 2px; }
.account { padding: 10px 8px 10px; border-radius: 10px; }
.account + .account { box-shadow: inset 0 1px 0 var(--line); border-radius: 0; }
.account.unavailable .label { color: var(--ink-2); }
.top { display: flex; align-items: center; flex-wrap: wrap; gap: 6px 8px; min-height: 24px; }
.dot { width: 8px; height: 8px; border-radius: 50%; background: var(--ok); flex-shrink: 0; }
.dot.draining { background: var(--gold); }
.dot.unavailable { background: var(--st-closed); }
.label { font-size: 13px; font-weight: 600; color: var(--ink); min-width: 0; overflow-wrap: anywhere; }
.harness, .plan { flex-shrink: 0; height: 18px; padding: 0 6px; border-radius: 5px; background: var(--chip-bg); box-shadow: inset 0 0 0 1px var(--chip-line); font: 500 10px/18px var(--mono); color: var(--ink-2); font-variant-ligatures: none; }
.plan { font-family: var(--font); letter-spacing: 0; }
.spacer { flex: 1; }
.state-text { font-size: 11.5px; color: var(--gold-ink); font-weight: 600; }
.account.unavailable .state-text { color: var(--ink-3); }
.toggle { height: 24px; padding: 0 8px; font-size: 12px; }
@media (hover: hover) { .account .toggle { opacity: 0; } .account:hover .toggle, .account:focus-within .toggle { opacity: 1; } }
.meter { position: relative; height: 6px; margin: 9px 0 7px; border-radius: 999px; background: var(--skeleton); }
.fill { position: absolute; inset: 0 auto 0 0; border-radius: inherit; background: linear-gradient(90deg, var(--teal), var(--st-qa-fill)); }
.fill.ahead { background: linear-gradient(90deg, var(--gold), var(--gold-2)); }
.pace-mark { position: absolute; top: -3px; width: 2px; height: 12px; margin-left: -1px; border-radius: 1px; background: var(--ink-2); opacity: .55; }
.facts { display: flex; align-items: center; flex-wrap: wrap; gap: 4px 10px; font-size: 12px; color: var(--ink-2); }
.facts b { font: 600 12.5px/1 var(--mono); color: var(--ink); font-variant-numeric: tabular-nums; }
.facts.muted { margin-top: 4px; color: var(--ink-3); }
.pace { font-weight: 600; color: var(--ok); }
.pace.ahead { color: var(--gold-ink); }
.pace.under { color: var(--ink-2); font-weight: 500; }
.provisional { font-weight: 600; color: var(--ink-2); }
.reset { margin-left: auto; color: var(--ink-3); }
</style>
