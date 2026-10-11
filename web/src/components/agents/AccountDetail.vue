<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, nextTick, onMounted, ref, watch } from 'vue'
import { accountName } from '../../lib/accountCascade'
import {
  amountText, dateRange, listReadings, noWindowLine, readingWho, recentReadings, removeWindow, renameAccount, renameProblem, setByYou, setByYouText,
  sortWindows, sourceText, spendLine, windowFacts, windowLabel,
} from '../../lib/accountLimits'
import { putQuotaPool, type AgentAccount, type AllowanceWindow } from '../../lib/agents'
import { pct, when, type AccountCapacity, type AccountRow, type CapacityReading } from '../../lib/capacity'
import { learnedUseText } from '../../lib/computerAccounts'
import { confirmAction, type ConfirmRequest } from '../../lib/confirm'
import AppIcon from '../AppIcon.vue'
import PiAccountModel from '../settings/PiAccountModel.vue'
import ClaudeStatuslineToggle from '../settings/ClaudeStatuslineToggle.vue'
import UsageProbeToggle from '../settings/UsageProbeToggle.vue'
import { useSession } from '../../stores/session'

// The inline detail of one account in Settings → Accounts (AEON-384): its
// name, each window with source and freshness, the last three readings and
// money for an API key. Limits are no longer set by hand (AEON-1149): the
// Advanced sentence and Make this repeat are gone; windows set by hand before
// stay as "Set by you" with Remove until they end, and an older rule shows in
// the panel's "Limits on this account".
const props = defineProps<{
  account: AgentAccount; accounts?: AgentAccount[]; row?: AccountRow; cap?: AccountCapacity; now: number; timezone: string; mayManage: boolean; rename?: number; renameTrigger?: HTMLElement | null
}>()
const emit = defineEmits<{ changed: [] }>()

const session = useSession()
const mayProbe = computed(() => props.mayManage && session.identity?.principal.kind === 'person' && props.account.owner_person_id === session.identity?.principal.id && (['grok', 'claude', 'codex'].includes(props.account.harness) || props.account.harness === 'pi' && props.account.provider === 'openrouter'))
const budget = computed(() => props.cap?.budget ?? props.account.usage_budget)
const name = computed(() => accountName(props.account))
const host = computed(() => props.row?.host || props.account.host_label || '')
const windows = computed(() => sortWindows(props.cap?.windows ?? []))
// Spend already observed when this vendor reported no window (AEON-792). The Agents page no longer draws that capacity cell.
const learnedSpend = computed(() => props.row ? learnedUseText(props.row) : '')
const primary = computed(() => windows.value[0] ?? null)
const offline = computed(() => props.row?.state === 'offline')
const mine = computed(() => setByYou(props.account.windows, props.now))

const poolBusy = ref(false)
const poolError = ref('')
const poolCandidates = computed(() => (props.accounts ?? []).filter(a => a.id !== props.account.id && a.harness === props.account.harness && !!props.account.quota_fingerprint && a.quota_fingerprint === props.account.quota_fingerprint && (!props.account.quota_pool_fingerprint || a.quota_pool_fingerprint !== props.account.quota_pool_fingerprint)))
const poolMembers = computed(() => (props.accounts ?? []).filter(a => a.id !== props.account.id && a.harness === props.account.harness && !!props.account.quota_pool_fingerprint && a.quota_pool_fingerprint === props.account.quota_pool_fingerprint))
const loginName = (a: AgentAccount) => `${accountName(a)}${a.host_label ? ` · ${a.host_label}` : ''}`
async function shareQuota(other: AgentAccount) {
  const account = props.account
  const fingerprint = account.quota_fingerprint
  if (!fingerprint) return
  // Confirmation replaces the entire pool. Include existing confirmed members
  // even when the open account is joining a pool from outside it.
  const current = (props.accounts ?? []).filter(a => a.harness === account.harness && a.quota_pool_fingerprint === fingerprint)
  const members = [account, ...current, other].filter((a, i, all) => all.findIndex(b => b.id === a.id) === i)
  await changeQuotaPool(members, fingerprint, true, {
    title: 'Same login — pool them?', body: 'Confirm all these accounts use the same vendor login before they share a quota.', points: members.map(loginName), confirmLabel: 'Pool accounts',
  })
}
async function stopSharingQuota(removed = props.account) {
  const fingerprint = props.account.quota_pool_fingerprint
  if (!fingerprint) return
  const members = [props.account, ...poolMembers.value]
  const remaining = members.filter(a => a.id !== removed.id)
  const keepPool = remaining.length >= 2
  const self = removed.id === props.account.id
  await changeQuotaPool(keepPool ? remaining : members, fingerprint, keepPool, {
    title: self ? 'Stop sharing quota?' : `Remove ${loginName(removed)} from pool?`,
    body: `${loginName(removed)} will use its own readings and limits.${remaining.length ? keepPool ? ' These accounts will keep sharing quota:' : ' The remaining account will also use its own readings and limits:' : ''}`,
    points: remaining.map(loginName), confirmLabel: self ? 'Stop sharing' : 'Remove from pool',
  })
}
async function changeQuotaPool(members: AgentAccount[], fingerprint: string, confirmed: boolean, request: ConfirmRequest) {
  if (!props.mayManage || poolBusy.value) return
  const ok = await confirmAction(request)
  if (!ok) return
  poolBusy.value = true; poolError.value = ''
  try {
    await putQuotaPool(members.map(a => a.id), fingerprint, confirmed)
    emit('changed')
  } catch (e) { poolError.value = e instanceof Error ? e.message : 'The quota did not change. Please try again.' }
  finally { poolBusy.value = false }
}

// ---------- The last readings ----------
const readings = ref<CapacityReading[] | null>(null)
const recent = computed(() => recentReadings(readings.value ?? [], primary.value))
onMounted(async () => {
  try { readings.value = await listReadings(props.account.id) } catch { readings.value = [] }
})

// ---------- Name ----------
const renaming = ref(false)
const nameDraft = ref('')
const nameError = ref('')
const nameBusy = ref(false)
const nameInput = ref<HTMLInputElement | null>(null)
const nameButton = ref<HTMLButtonElement | null>(null)
let nameReturn: HTMLElement | null = null
watch(() => props.rename, activation => { if (activation) void startRename(props.renameTrigger) }, { immediate: true })
async function startRename(trigger?: HTMLElement | null) {
  nameReturn = trigger ?? nameButton.value
  nameDraft.value = name.value === 'Unlabeled account' ? '' : name.value
  nameError.value = ''
  renaming.value = true
  await nextTick()
  nameInput.value?.focus()
  nameInput.value?.select()
}
async function cancelRename() {
  renaming.value = false; nameError.value = ''
  await nextTick()
  const target = nameReturn?.isConnected ? nameReturn : nameButton.value
  target?.focus({ preventScroll: true })
}
async function saveName() {
  const problem = renameProblem(nameDraft.value)
  if (problem) { nameError.value = problem; return }
  if (nameDraft.value.trim() === props.account.label.trim()) { renaming.value = false; return }
  nameBusy.value = true
  try {
    await renameAccount(props.account.id, nameDraft.value.trim())
    renaming.value = false
    emit('changed')
  } catch (e) {
    nameError.value = e instanceof Error ? e.message : 'The name did not change. Please try again.'
  } finally { nameBusy.value = false }
}

// ---------- Set by you: windows set by hand before AEON-384, until they end ----------
const windowBusy = ref('')
const windowError = ref('')
const windowTip = (w: AllowanceWindow) => `${amountText(w.used, w.unit)} used of ${amountText(w.allowance, w.unit)}, ${dateRange(w.starts_at, w.ends_at, props.timezone)}`
async function drop(w: AllowanceWindow) {
  const ok = await confirmAction({ title: `Remove ${amountText(w.allowance, w.unit)} on ${name.value}?`, body: `Agents follow your plan and the vendor's readings on ${name.value}. Its history stays.`, confirmLabel: 'Remove limit' })
  if (!ok) return
  windowBusy.value = w.id; windowError.value = ''
  try {
    await removeWindow(props.account.id, w.id)
    emit('changed')
  } catch (e) {
    windowError.value = e instanceof Error ? e.message : 'The limit did not change. Please try again.'
  } finally { windowBusy.value = '' }
}
</script>

<template>
  <div class="detail">
    <UsageProbeToggle v-if="mayProbe" :account="account" @changed="emit('changed')" />
    <PiAccountModel v-if="account.harness === 'pi'" :account="account" :editable="mayManage" @saved="emit('changed')" />
    <ClaudeStatuslineToggle v-if="mayManage && account.harness === 'claude' && account.statusline_opt_in" :account="account" @changed="emit('changed')" />
    <dl class="account-facts">
      <div class="fact">
        <dt>Name</dt>
        <dd v-if="!renaming" class="name-line">
          <span class="nm">{{ name }}</span>
          <button v-if="mayManage" ref="nameButton" type="button" class="icon-btn flat sm" :aria-label="`Rename ${name}`" data-tip="Rename" @click="startRename()"><AppIcon name="edit" :size="14" /></button>
        </dd>
        <dd v-else>
          <form class="rename" @submit.prevent="saveName" @keydown.esc.prevent="cancelRename">
            <input ref="nameInput" v-model="nameDraft" class="field" maxlength="128" :aria-label="`New name for ${name}`" :aria-invalid="!!nameError" :disabled="nameBusy">
            <button type="submit" class="btn sm" :disabled="nameBusy">Save</button>
            <button type="button" class="btn sm ghost" :disabled="nameBusy" @click="cancelRename">Cancel</button>
          </form>
          <p v-if="nameError" class="problem" role="alert">{{ nameError }}</p>
        </dd>
      </div>

      <div v-if="mayManage && (poolCandidates.length || account.quota_pool_fingerprint)" class="fact">
        <dt>Shared login</dt>
        <dd>
          <p v-if="poolMembers.length" class="quiet">Shared with {{ poolMembers.map(loginName).join(', ') }}</p>
          <button v-for="other in poolCandidates" :key="other.id" type="button" class="btn sm ghost" :disabled="poolBusy" @click="shareQuota(other)">Pool with {{ loginName(other) }}</button>
          <button v-for="member in poolMembers" :key="member.id" type="button" class="btn sm ghost" :disabled="poolBusy" @click="stopSharingQuota(member)">Remove {{ loginName(member) }} from pool</button>
          <button v-if="account.quota_pool_fingerprint" type="button" class="btn sm ghost" :disabled="poolBusy" @click="stopSharingQuota()">Stop sharing quota</button>
          <p v-if="poolError" class="problem" role="alert">{{ poolError }}</p>
        </dd>
      </div>

      <div v-if="budget" class="fact">
        <dt>Dollar budget</dt>
        <dd>
          <span class="num">${{ budget.key_usage_usd.toFixed(2) }} used</span>
          <span v-if="budget.key_limit_usd != null"> · ${{ budget.key_limit_usd.toFixed(2) }} key limit</span>
          <span v-if="budget.key_remaining_usd != null"> · ${{ budget.key_remaining_usd.toFixed(2) }} key remaining</span>
          <span> · {{ budget.balance_usd == null ? 'Account balance unknown' : `$${budget.balance_usd.toFixed(2)} account balance` }}</span>
          <span class="src"> · Read {{ when(budget.read_at, now, timezone) }}</span>
        </dd>
      </div>
      <template v-if="windows.length">
        <div v-for="w in windows" :key="`${w.reading.window_kind}/${w.reading.bucket}`" class="fact window" :class="{ frozen: windowFacts(w, now, timezone).stale }">
          <dt>{{ windowLabel(w, account.harness) }}</dt>
          <dd>
            <b class="num">{{ windowFacts(w, now, timezone).left }}</b><span class="sep"> · </span>{{ windowFacts(w, now, timezone).reset }}
            <span class="src"><span class="sep"> · </span>{{ sourceText(w.reading, account.harness, host, now) }}<template v-if="offline"> · offline</template><template v-else-if="windowFacts(w, now, timezone).stale"> · stale</template></span>
          </dd>
        </div>
      </template>
      <div v-else-if="!budget" class="fact">
        <dt>Limits</dt>
        <dd class="quiet">{{ noWindowLine(account.harness, host) }}<p v-if="learnedSpend" class="learned-use">{{ learnedSpend }}</p></dd>
      </div>

      <div v-if="recent.length" class="fact">
        <dt>Readings</dt>
        <dd>
          <ol class="readings">
            <li v-for="(r, i) in recent" :key="r.read_at"><time :datetime="r.read_at">{{ when(r.read_at, now, timezone) }}</time> <span class="num">{{ pct(r.used_percent) }}{{ i === 0 ? ' used' : '' }}</span> <span class="by">{{ readingWho(r, account.harness, host) }}</span></li>
          </ol>
        </dd>
      </div>

      <div v-if="cap?.spend_month_usd" class="fact">
        <dt>Spend</dt>
        <dd>{{ spendLine(cap.spend_month_usd, cap.limit, now, timezone) }} <span class="quiet">· list prices</span></dd>
      </div>

      <div v-if="mine.length" class="fact mine-fact">
        <dt>Set by you</dt>
        <dd>
          <ul class="mine">
            <li v-for="w in mine" :key="w.id">
              <span class="what" :data-tip="windowTip(w)">{{ setByYouText(w, timezone) }}</span>
              <span v-if="mayManage" class="actions">
                <button type="button" class="btn sm ghost" :disabled="!!windowBusy" @click="drop(w)"><AppIcon name="trash" :size="14" />Remove</button>
              </span>
            </li>
          </ul>
          <p v-if="windowError" class="problem" role="alert">{{ windowError }}</p>
        </dd>
      </div>
    </dl>
  </div>
</template>

<style scoped>
.detail { margin: 0 0 10px 30px; padding: 12px 16px; border-radius: 12px; background: var(--surface-sunken); box-shadow: inset 0 0 0 1px var(--line); }
.account-facts { display: grid; grid-template-columns: 96px minmax(0, 1fr); gap: 8px 18px; margin: 0; }
.fact { display: contents; }
dt { padding-top: 5px; font: 500 12px/1.5 var(--font); letter-spacing: 0; text-transform: none; color: var(--ink-3); }
dd { min-width: 0; margin: 0; padding-top: 4px; font-size: 13px; line-height: 1.5; color: var(--ink); overflow-wrap: anywhere; }
.num { font-variant-numeric: tabular-nums; }
b.num { font-weight: 650; }
.sep, .src, .by, .quiet { color: var(--ink-2); }
.learned-use { margin: 4px 0 0; color: var(--ink-2); font-size: 13px; line-height: 1.4; font-variant-numeric: tabular-nums; overflow-wrap: anywhere; }
.src { font-size: 12.5px; }
.window.frozen dd { color: var(--ink-2); }
.name-line { display: flex; align-items: flex-start; gap: 4px; min-height: 28px; padding-top: 0; }
.name-line .icon-btn { flex: none; }
.nm { flex: 1; min-width: 0; white-space: normal; overflow-wrap: anywhere; font-weight: 600; }
.rename { display: flex; flex-wrap: wrap; align-items: center; gap: 6px; }
.rename .field { flex: 1 1 200px; max-width: 320px; height: 30px; }
.readings { display: flex; flex-wrap: wrap; gap: 2px 14px; margin: 0; padding: 0; list-style: none; }
.readings time { font-variant-numeric: tabular-nums; color: var(--ink-2); }
.by { font-size: 12.5px; }
/* Labels sit on the text line of a 30 px control row. */
.mine-fact dt { padding-top: 10px; }
.mine .actions { display: inline-flex; gap: 4px; }
.problem { margin-top: 4px; font-size: 12.5px; color: var(--danger); }
.mine { display: grid; gap: 2px; margin: 0; padding: 0; list-style: none; }
.mine li { display: flex; flex-wrap: wrap; align-items: center; gap: 2px 16px; min-height: 30px; }
.mine .what { font-variant-numeric: tabular-nums; }
/* In Settings' docked pane (AEON-686) the card is phone-narrow at any viewport
   width: labels go above their values so a limit's actions never overflow. */
.detail { container: account-detail / inline-size; }
@container account-detail (max-width: 420px) {
  .account-facts { grid-template-columns: minmax(0, 1fr); gap: 2px; }
  dt, .mine-fact dt { padding-top: 10px; }
  .fact:first-child dt { padding-top: 0; }
  dd { padding-top: 0; }
  .mine .actions { margin-left: -10px; }
}
@media (max-width: 600px) {
  .detail { margin: 0 0 10px; padding: 12px; }
  .account-facts { grid-template-columns: minmax(0, 1fr); gap: 2px; }
  dt { padding-top: 10px; }
  .fact:first-child dt { padding-top: 0; }
  dd { padding-top: 0; }
  .src { display: block; }
  .src .sep { display: none; }
  .rename .field { height: 44px; font-size: 16px; }
  .rename .field { flex-basis: 100%; max-width: none; }
  .mine .actions { margin-left: -10px; }
  .btn.sm { min-height: 44px; }
  .icon-btn.sm { width: 44px; height: 44px; }
}
</style>
