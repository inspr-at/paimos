<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue'
import { RouterLink } from 'vue-router'
import { accountName, accountPlan } from '../../lib/accountCascade'
import { limitSummary, nameClashes, readingSupport, setByYou } from '../../lib/accountLimits'
import type { AgentAccount } from '../../lib/agents'
import { can } from '../../lib/authz'
import { HARNESS_NAME, POOL_ORDER, type AccountState } from '../../lib/capacity'
import { useAgents, type Availability } from '../../stores/agents'
import { useCapacity } from '../../stores/capacity'
import { useSession } from '../../stores/session'
import AppIcon from '../AppIcon.vue'
import AccountDetail from './AccountDetail.vue'
import HarnessMark from './HarnessMark.vue'

// Settings → Accounts (AEON-384): every vendor account, grouped by vendor,
// with how Aeon reads it and whether agents may use it. A row opens its
// detail inline: name, windows, last readings and the Advanced limit. There is
// no allowance form: limits are observed, and a limit by hand is one sentence.
// The card's header (SettingsCard) keeps its aside slot for "Add an account".
const props = defineProps<{ accounts: AgentAccount[]; state: Availability; now: number; admin: boolean; set: (account: AgentAccount, state: AgentAccount['state']) => Promise<void> }>()
const session = useSession()
const agents = useAgents()
const capacity = useCapacity()
onMounted(() => { void capacity.load() })
const mayManage = computed(() => session.identity?.principal.kind === 'person' && can('account.manage'))
const busy = ref('')
const error = ref('')
const open = ref('')
const renameAt = ref('')

const rowOf = computed(() => new Map(capacity.rows.map(r => [r.id, r])))
const clashes = computed(() => nameClashes(props.accounts))
const groups = computed(() => {
  const byHarness = new Map<string, AgentAccount[]>()
  for (const a of props.accounts) byHarness.set(a.harness, [...(byHarness.get(a.harness) ?? []), a])
  const order = (h: string) => { const i = POOL_ORDER.indexOf(h); return i < 0 ? 99 : i }
  return [...byHarness.entries()].sort(([a], [b]) => order(a) - order(b) || a.localeCompare(b)).map(([harness, list]) => ({
    harness, name: HARNESS_NAME[harness] ?? harness,
    accounts: [...list].sort((x, y) => accountName(x).localeCompare(accountName(y)) || x.id.localeCompare(y.id)),
  }))
})
const host = (a: AgentAccount) => rowOf.value.get(a.id)?.host || a.host_label || ''
const agentsAllowed = (a: AgentAccount) => a.state === 'available' && a.ongoing_use_approved !== false
// Only what needs a word: a normal, usable account shows none.
const STATE_TEXT: Partial<Record<AccountState, string>> = { signin: 'Sign in again', offline: 'Offline', unavailable: "Couldn't check", paused: 'Paused' }
const stateOf = (a: AgentAccount): AccountState => rowOf.value.get(a.id)?.state ?? (a.state === 'draining' ? 'paused' : 'live')
function limitChip(a: AgentAccount): string {
  const rule = capacity.byAccount.get(a.id)?.limit
  if (rule) return `Limit · ${limitSummary(rule)}`
  return setByYou(a.windows, props.now).length ? 'Set by you' : ''
}

function toggleOpen(id: string) {
  open.value = open.value === id ? '' : id
  renameAt.value = ''
}
function nameIt(id: string) { open.value = id; renameAt.value = id }
// A click on the row's quiet parts opens it; its buttons act on their own.
function onHead(event: MouseEvent, id: string) {
  if ((event.target as HTMLElement).closest('button, a, input, select')) return
  toggleOpen(id)
}
async function toggleUse(account: AgentAccount) {
  busy.value = account.id; error.value = ''
  try { await props.set(account, agentsAllowed(account) ? 'draining' : 'available') }
  catch (e) { error.value = e instanceof Error ? e.message : 'The account did not change. Please try again.' }
  finally { busy.value = '' }
}
async function changed() { await Promise.all([agents.refreshAccounts(), capacity.refreshCapacity()]) }
watch(() => props.accounts.map(a => a.id).join(), () => { if (open.value && !props.accounts.some(a => a.id === open.value)) open.value = '' })
</script>

<template>
  <div class="accounts">
    <p v-if="state === 'forbidden'" class="note">Accounts are visible to workspace admins.</p>
    <p v-else-if="state === 'error'" class="note" role="alert">Accounts could not be loaded right now.</p>
    <div v-else-if="state === 'idle'" class="sk"><span class="skeleton" /><span class="skeleton short" /><span class="skeleton" /></div>
    <p v-else-if="!accounts.length" class="note empty">
      No accounts yet. Connect a computer; accounts appear as you sign in to Codex, Claude, Grok or Cursor there.
      <RouterLink v-if="mayManage" class="btn sm" to="/agents/register-agent"><AppIcon name="monitor" :size="14" />Connect computer</RouterLink>
    </p>
    <template v-else>
      <p class="use-head" aria-hidden="true">Agents may use it</p>
      <section v-for="group in groups" :key="group.harness" class="group" :aria-labelledby="`accounts-${group.harness}`">
        <h3 :id="`accounts-${group.harness}`" class="group-head">
          <HarnessMark :harness="group.harness" :size="16" />
          <span>{{ group.name }}</span>
          <span class="count">{{ group.accounts.length }} {{ group.accounts.length === 1 ? 'account' : 'accounts' }}</span>
        </h3>
        <ul class="list">
          <li v-for="a in group.accounts" :key="a.id" class="account" :class="[stateOf(a), { open: open === a.id }]" :data-account="a.id">
            <div class="head" @click="onHead($event, a.id)">
              <span class="dot" :class="stateOf(a)" aria-hidden="true" />
              <div class="ident">
                <span class="name" :title="accountName(a)">{{ accountName(a) }}</span>
                <button v-if="clashes.has(a.id) && mayManage" type="button" class="name-it" :aria-label="`Name it: another ${group.name} account is also called ${accountName(a)}`" :data-tip="`Another ${group.name} account is also called ${accountName(a)}`" @click="nameIt(a.id)">Name it</button>
                <span v-if="host(a)" class="chip host" :title="host(a)">{{ host(a) }}</span>
                <span v-if="limitChip(a)" class="chip mine">{{ limitChip(a) }}</span>
                <span class="meta">
                  <template v-if="STATE_TEXT[stateOf(a)]"><span class="state">{{ STATE_TEXT[stateOf(a)] }}</span><span class="sep"> · </span></template>
                  <template v-if="accountPlan(a)"><span class="plan">{{ accountPlan(a) }}</span><span class="sep"> · </span></template>
                  <span class="reads">{{ readingSupport(a.harness) }}</span>
                </span>
              </div>
              <button
                type="button" role="switch" class="use" :aria-checked="agentsAllowed(a)" :aria-label="`Agents may use it · ${accountName(a)}`"
                :disabled="!mayManage || busy === a.id" :data-tip="mayManage ? undefined : 'People who can manage accounts change this'" @click="toggleUse(a)"
              ><span class="track" aria-hidden="true"><span class="thumb" /></span></button>
              <button type="button" class="icon-btn flat more" :aria-expanded="open === a.id" :aria-controls="`account-detail-${a.id}`" :aria-label="`Details for ${accountName(a)}`" @click="toggleOpen(a.id)"><AppIcon name="chevron" :size="16" /></button>
            </div>
            <AccountDetail
              v-if="open === a.id" :id="`account-detail-${a.id}`" :account="a" :row="rowOf.get(a.id)" :cap="capacity.byAccount.get(a.id)" :now="now"
              :timezone="capacity.timezone" :may-manage="mayManage" :rename="renameAt === a.id" @changed="changed"
            />
          </li>
        </ul>
      </section>
    </template>
    <p v-if="error" class="note error" role="alert"><AppIcon name="alert" :size="13" />{{ error }}</p>
  </div>
</template>

<style scoped>
.accounts { position: relative; display: grid; gap: 14px; }
/* The switches' one column label, on the first vendor's line. */
.use-head { position: absolute; top: 0; right: 58px; margin: 0; font-size: 12px; line-height: 20px; color: var(--ink-3); }
.note { font-size: 13px; color: var(--ink-2); }
.note.empty { display: flex; flex-wrap: wrap; align-items: center; gap: 8px 12px; }
.note.error { display: flex; align-items: center; gap: 6px; color: var(--danger); }
.sk { display: grid; gap: 10px; padding: 6px 0 8px; }
.sk .short { width: 60%; }
.group-head { display: flex; align-items: center; gap: 8px; margin: 0 0 2px; padding: 0 8px; font: 600 12.5px/1.4 var(--font); color: var(--ink-2); }
.group-head .count { font-weight: 500; color: var(--ink-3); }
.list { margin: 0; padding: 0; list-style: none; }
.account + .account { box-shadow: inset 0 1px 0 var(--line); }
.head { display: grid; grid-template-columns: 10px minmax(0, 1fr) auto 34px; align-items: center; column-gap: 12px; min-height: 48px; padding: 7px 4px 7px 10px; border-radius: 10px; cursor: pointer; }
@media (hover: hover) { .head:hover { background: var(--row-hover); } }
.account.open > .head { background: transparent; }
.dot { width: 8px; height: 8px; border-radius: 50%; background: var(--ok); }
.dot.paused, .dot.unavailable { background: var(--ink-3); }
.dot.offline { background: transparent; box-shadow: inset 0 0 0 1.5px var(--ink-3); }
.dot.signin { background: var(--gold); }
.ident { display: flex; align-items: center; flex-wrap: wrap; gap: 4px 8px; min-width: 0; }
.name { max-width: 24ch; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; font-size: 13.5px; font-weight: 600; color: var(--ink); }
.account.paused .name, .account.unavailable .name { color: var(--ink-2); }
.chip { height: 20px; padding: 0 7px; font-size: 11px; letter-spacing: 0; }
.chip.mine { font-family: var(--font); font-size: 11.5px; }
.chip.host { max-width: 16ch; overflow: hidden; text-overflow: ellipsis; font-family: var(--mono); font-variant-ligatures: none; }
.chip.mine { background: var(--chip-teal-bg); box-shadow: inset 0 0 0 1px var(--chip-teal-line); color: var(--teal-ink); }
.meta { font-size: 12.5px; color: var(--ink-3); }
/* A long line breaks between its parts, never inside one. */
.meta > span:not(.sep) { white-space: nowrap; }
.plan { color: var(--ink-2); }
.state { color: var(--ink-2); font-weight: 600; }
.account.signin .state { color: var(--gold-ink); }
.sep { font-weight: 400; color: var(--ink-3); }
.name-it { height: 22px; padding: 0 8px; border: 0; border-radius: 999px; background: var(--gold-wash); color: var(--gold-ink); font-size: 11.5px; font-weight: 600; cursor: pointer; }
.name-it:focus-visible { box-shadow: var(--focus-ring); }
.use { display: inline-grid; place-items: center; width: 50px; height: 32px; padding: 0; border: 0; border-radius: 999px; background: transparent; cursor: pointer; }
@media (hover: hover) { .use:not(:disabled):hover { background: var(--row-selected); } }
.use:focus-visible { box-shadow: var(--focus-ring); }
.use:disabled { cursor: default; }
.track { position: relative; flex: none; width: 34px; height: 20px; border-radius: 999px; background: var(--track); box-shadow: inset 0 0 0 1px var(--line-2); transition: background-color .15s ease; }
.thumb { position: absolute; top: 3px; left: 3px; width: 14px; height: 14px; border-radius: 50%; background: var(--surface-raised); box-shadow: 0 1px 2px rgba(0, 0, 0, .25); transition: transform .15s ease; }
.use[aria-checked="true"] .track { background: linear-gradient(90deg, #0e6f6c, #1a8683); box-shadow: none; }
.use[aria-checked="true"] .thumb { transform: translateX(14px); background: #fff; }
.use:disabled .track { opacity: .55; }
.more { justify-self: end; color: var(--ink-3); }
.more :deep(svg) { transition: transform .15s ease; }
.more[aria-expanded="true"] :deep(svg) { transform: rotate(180deg); }
@media (prefers-reduced-motion: reduce) { .track, .thumb, .more :deep(svg) { transition: none; } }
@media (max-width: 600px) {
  .head { grid-template-columns: 10px minmax(0, 1fr) 50px 44px; column-gap: 8px; align-items: start; padding: 4px 0 6px 8px; }
  .dot { margin-top: 18px; }
  .ident { padding-top: 10px; }
  .meta { flex-basis: 100%; }
  .use { height: 44px; }
  .more { width: 44px; height: 44px; }
  .name { max-width: 100%; }
  .use-head { position: static; justify-self: end; margin: 0 60px -12px 0; }
}
</style>
