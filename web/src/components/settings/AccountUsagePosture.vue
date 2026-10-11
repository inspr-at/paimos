<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
// Per-account overrides (AEON-1037), as rows of "Limits on this account" (AEON-1172, design
// AEON-1149): the floor saves on change (or ⌘↵ / Ctrl+↵) with Undo, the Daily limit row
// says the harness's pace and links Capacity, the default slot holds older limits, then
// the Resets card. Pace and Boost today are per harness, never per account (AEON-1030).
import { computed, onBeforeUnmount, ref, useId, watch } from 'vue'
import { can, onAccessChange } from '../../lib/authz'
import { createScope, scopeOwner } from '../../lib/identityScope'
import { getUsageOverview, putAccountUsage, putResetPolicy, type AccountUsagePolicy } from '../../lib/accountUsage'
import type { ResetCredits, ResetPlan, ResetPolicy } from '../../lib/accountResets'
import { toast } from '../../lib/toast'
import { useSession } from '../../stores/session'
import AppIcon from '../AppIcon.vue'
import AccountResetsCard from './AccountResetsCard.vue'
interface ResetsView { harness: string; provider?: string; credits: ResetCredits; policy: ResetPolicy; plan: ResetPlan | null }
const props = defineProps<{ accountId: string; german?: boolean; pace?: string; who?: string }>()
const emit = defineEmits<{ changed: [] }>()
const session = useSession(), policy = ref<AccountUsagePolicy | null>(null), resets = ref<ResetsView | null>(null), busy = ref(false), error = ref(''), floor = ref(0), readAt = ref(Date.now())
const text = (en: string, de: string) => props.german ? de : en
const mac = /Mac|iPhone|iPad/.test(navigator.platform || navigator.userAgent)
const floorId = `floor-${useId()}`
const key = computed(() => `${scopeOwner(session.identity)}/${props.accountId}`)
const scope = createScope(() => session.authenticationCurrent() && can('account.read') ? key.value : '')
// Reset policy belongs to the account's owner, like the former posture (can_set_posture = owns).
const mayResets = computed(() => !!policy.value?.can_set_posture && session.identity?.principal.kind === 'person' && can('account.manage'))
const mayFloor = computed(() => !!policy.value?.can_set_floor && session.identity?.principal.kind === 'person' && can('account.manage') && can('model_prefs.manage'))
function load() {
  policy.value = null; resets.value = null; busy.value = true; error.value = ''
  void scope.run(async ({ after, signal }) => {
    let cursor: string | undefined
    // The server bounds the tenant to 1024 accounts; never loop on an invalid cursor.
    for (let page = 0; page < 11; page++) {
      const next = await after(getUsageOverview(cursor, signal), value => {
        const row = value.accounts.find(account => account.account_id === props.accountId)
        if (row) {
          policy.value = row.usage_policy ?? null; floor.value = row.usage_policy?.own_floor_percent ?? 0; readAt.value = Date.now()
          resets.value = row.usage_policy && row.resets ? { harness: row.harness ?? '', provider: row.provider, credits: row.resets, policy: row.reset_policy ?? 'suggest', plan: row.reset_plan ?? null } : null
          return undefined
        }
        if (value.has_more && !value.next_cursor) throw new Error('Missing account cursor')
        return value.has_more ? value.next_cursor : undefined
      })
      if (!next) return
      if (next === cursor) throw new Error(text('The account list is incomplete.', 'Die Kontenliste ist unvollständig.'))
      cursor = next
    }
    throw new Error(text('The account list is incomplete.', 'Die Kontenliste ist unvollständig.'))
  }, { failed: () => { error.value = text('Usage settings could not be loaded.', 'Nutzungseinstellungen konnten nicht geladen werden.') }, settled: () => { busy.value = false } })
}
// Esc puts the saved floor back; the docked panel then leaves the field. ⌘↵ / Ctrl+↵ saves at once.
function floorKey(event: KeyboardEvent) {
  if (event.key === 'Escape') { floor.value = policy.value?.own_floor_percent ?? 0; return }
  if (event.key !== 'Enter') return
  event.preventDefault()
  if (!event.altKey && (mac ? event.metaKey && !event.ctrlKey : event.ctrlKey && !event.metaKey)) commitFloor()
}
const failed = () => toast(text('The change could not be confirmed. Reload before trying again.', 'Die Änderung konnte nicht bestätigt werden. Vor dem nächsten Versuch neu laden.'), { tone: 'error', action: { label: text('Reload', 'Neu laden'), run: load } })
/** A whole percentage from 0 to 80, the server's range; anything else is said and the saved value returns. */
function commitFloor() {
  const before = policy.value, next = Number(floor.value)
  if (!before || busy.value || !mayFloor.value) return
  if (next === before.own_floor_percent) return
  if (!Number.isInteger(next) || next < 0 || next > 80) { floor.value = before.own_floor_percent; toast(text('Use a whole percentage from 0 to 80.', 'Eine ganze Prozentzahl von 0 bis 80 verwenden.'), { tone: 'error' }); return }
  write(before, next, previous => toast(text(`Floor saved: ${next} %.`, `Untergrenze gespeichert: ${next} %.`), { action: { label: text('Undo', 'Rückgängig'), run: () => undoFloor(previous) } }))
}
function undoFloor(previous: { key: string; value: number; revision: number }) {
  const now = policy.value
  // Undo restores only the record it was made on, as long as nothing changed it since.
  if (!now || previous.key !== key.value || now.revision !== previous.revision) { toast(text('The floor changed since. Undo is no longer possible.', 'Die Untergrenze hat sich seither geändert. Rückgängig ist nicht mehr möglich.'), { tone: 'error' }); return }
  write(now, previous.value, () => toast(text(`Floor restored: ${previous.value} %.`, `Untergrenze wiederhergestellt: ${previous.value} %.`)))
}
function write(before: AccountUsagePolicy, value: number, done: (previous: { key: string; value: number; revision: number }) => void) {
  const owner = key.value
  busy.value = true
  void scope.run(({ after, signal }) => after(putAccountUsage(before, { floor_percent: value }, signal), result => {
    if (result.account_id !== before.account_id || result.binding_revision !== before.binding_revision || result.revision !== before.revision + 1) throw new Error('Save could not be confirmed')
    policy.value = result; floor.value = result.own_floor_percent; emit('changed')
    done({ key: owner, value: before.own_floor_percent, revision: result.revision })
  }), { failed: () => { floor.value = before.own_floor_percent; failed() }, settled: () => { busy.value = false } })
}
// The floor and the reset policy share one account revision, so either write moves both forward.
function setResets(next: ResetPolicy) {
  const before = policy.value, shown = resets.value
  if (!before || !shown || busy.value || !mayResets.value || shown.policy === next) return
  busy.value = true
  void scope.run(({ after, signal }) => after(putResetPolicy(before, next, signal), result => {
    if (result.account_id !== before.account_id || result.binding_revision !== before.binding_revision || result.revision !== before.revision + 1 || result.reset_policy !== next) throw new Error('Save could not be confirmed')
    policy.value = { ...before, revision: result.revision }; readAt.value = Date.now()
    resets.value = result.resets ? { ...shown, credits: result.resets, policy: result.reset_policy, plan: result.reset_plan } : null
    emit('changed')
  }), { failed, settled: () => { busy.value = false } })
}
watch(key, () => { scope.reset(); load() }, { immediate: true, flush: 'sync' })
const unsubscribe = onAccessChange(() => { scope.reset(); policy.value = null; resets.value = null; busy.value = false; if (can('account.read')) load() })
onBeforeUnmount(() => { scope.dispose(); unsubscribe() })
</script>
<template>
  <div class="usage-policy" data-account-usage :aria-busy="busy">
    <p v-if="who" class="who">{{ who }}</p>
    <template v-if="policy">
      <div class="lim" data-limit="floor">
        <span class="lbl"><b :id="floorId">{{ text('Floor', 'Untergrenze') }}</b><small>{{ text(`Agents never use the last ${policy.floor_percent} % of this account, not even with Use everything or Boost today.`, `Agenten nutzen die letzten ${policy.floor_percent} % dieses Kontos nie, auch nicht mit „Alles nutzen“ oder Boost heute.`) }}</small></span>
        <span v-if="mayFloor" class="pct"><input v-model.number="floor" class="field n" type="number" min="0" max="80" step="1" inputmode="numeric" :aria-labelledby="floorId" :aria-keyshortcuts="mac ? 'Meta+Enter' : 'Control+Enter'" :readonly="busy" @change="commitFloor" @keydown="floorKey" />%</span>
        <span v-else class="pct"><b>{{ policy.floor_percent }} %</b></span>
      </div>
      <div v-if="pace" class="lim" data-limit="daily">
        <span class="lbl"><b>{{ text('Daily limit', 'Tageslimit') }}</b><small>{{ pace }}</small></span>
        <RouterLink class="go" to="/settings/capacity" :aria-label="text('Daily limit, in Capacity', 'Tageslimit, unter Kapazität')">{{ text('Capacity', 'Kapazität') }}<AppIcon name="chevron-right" :size="12" /></RouterLink>
      </div>
      <slot />
      <AccountResetsCard v-if="resets" class="rs" :account-id="policy.account_id" :harness="resets.harness" :provider="resets.provider" :credits="resets.credits" :policy="resets.policy" :plan="resets.plan" :now="readAt" :can-set="mayResets" :busy="busy" @set="setResets" />
    </template>
    <p v-else-if="error" class="policy-feedback" role="alert">{{ error }} <button type="button" class="btn sm" @click="load">{{ text('Reload', 'Neu laden') }}</button></p>
    <p v-else class="policy-feedback quiet" role="status">{{ busy ? text('Loading usage settings…', 'Nutzungseinstellungen werden geladen…') : text('Usage settings are private.', 'Nutzungseinstellungen sind privat.') }}</p>
  </div>
</template>
<style scoped>
.usage-policy { display: grid; container: usage / inline-size; }
.who { margin: 4px 0 2px; font-size: 12px; font-weight: 600; color: var(--ink-2); overflow-wrap: anywhere; }
.lim { display: grid; grid-template-columns: minmax(0, 1fr) auto; align-items: center; column-gap: 16px; min-height: 60px; padding: 8px 0; }
.lim + .lim { border-top: 1px solid var(--line); }
.lbl { display: grid; gap: 2px; min-width: 0; }
.lbl b { font-size: 13.5px; font-weight: 600; color: var(--ink); }
.lbl small { font-size: 12.5px; line-height: 1.45; color: var(--ink-3); }
.pct { display: inline-flex; align-items: center; gap: 4px; font-size: 13px; color: var(--ink-2); }
.pct .field.n { width: 58px; height: 36px; padding: 0 4px; text-align: center; font: 600 14px/1 var(--font); }
.pct b { color: var(--ink); font-weight: 600; }
.go { display: inline-flex; align-items: center; justify-content: center; gap: 4px; min-height: 32px; padding: 0 6px; margin-right: -6px; border-radius: 8px; color: var(--ink-2); font-size: 13px; font-weight: 600; text-decoration: none; white-space: nowrap; }
.go:hover { color: var(--teal-ink); background: var(--row-hover); }
.go:focus-visible { outline: none; box-shadow: var(--focus-ring); }
.rs { margin: 4px 0 8px; }
.policy-feedback { padding: 8px 0; font-size: 12.5px; color: var(--danger); }
.policy-feedback.quiet { color: var(--ink-3); }
@container usage (max-width: 420px) {
  .lim { grid-template-columns: minmax(0, 1fr); row-gap: 8px; }
  .lim > .pct, .lim > .go { justify-self: start; }
  .go { margin-left: -6px; }
}
@media (pointer: coarse) {
  .pct .field.n { height: 44px; min-height: 44px; }
  .go { min-height: 44px; min-width: 44px; }
}
</style>
<style scoped src="../../styles/settingsButtons.css"></style>
