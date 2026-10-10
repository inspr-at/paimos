<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
// Per-account overrides (AEON-1037): the floor and the Resets card. Pace and Boost today
// are per harness on the dial and replace the former account posture (AEON-1030 draft 6).
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import { can, onAccessChange } from '../../lib/authz'
import { createScope, scopeOwner } from '../../lib/identityScope'
import { getUsageOverview, putAccountUsage, putResetPolicy, type AccountUsagePolicy } from '../../lib/accountUsage'
import type { ResetCredits, ResetPlan, ResetPolicy } from '../../lib/accountResets'
import { useSession } from '../../stores/session'
import AccountResetsCard from './AccountResetsCard.vue'
import KeyCap from '../KeyCap.vue'
interface ResetsView { harness: string; provider?: string; credits: ResetCredits; policy: ResetPolicy; plan: ResetPlan | null }
const props = defineProps<{ accountId: string; german?: boolean }>()
const emit = defineEmits<{ changed: [] }>()
const session = useSession(), policy = ref<AccountUsagePolicy | null>(null), resets = ref<ResetsView | null>(null), busy = ref(false), error = ref(''), floor = ref(0), readAt = ref(Date.now())
const text = (en: string, de: string) => props.german ? de : en
const mac = /Mac|iPhone|iPad/.test(navigator.platform || navigator.userAgent)
const key = computed(() => `${scopeOwner(session.identity)}/${props.accountId}`)
const scope = createScope(() => session.authenticationCurrent() && can('account.read') ? key.value : '')
// Reset policy belongs to the account's owner, like the former posture (can_set_posture = owns).
const mayResets = computed(() => !!policy.value?.can_set_posture && session.identity?.principal.kind === 'person' && can('account.manage'))
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
function floorKey(event: KeyboardEvent) {
  if (event.key === 'Escape') { event.preventDefault(); event.stopPropagation(); (event.target as HTMLInputElement).form?.querySelector<HTMLButtonElement>('button[type="submit"]')?.focus(); return }
  if (event.key !== 'Enter') return
  event.preventDefault()
  if (!event.altKey && (mac ? event.metaKey && !event.ctrlKey : event.ctrlKey && !event.metaKey)) (event.target as HTMLInputElement).form?.requestSubmit()
}
const failed = () => { error.value = text('The change could not be confirmed. Reload before trying again.', 'Die Änderung konnte nicht bestätigt werden. Vor dem nächsten Versuch neu laden.') }
function save(value: { floor_percent: number }) {
  const before = policy.value
  if (!before || busy.value || session.identity?.principal.kind !== 'person' || !can('account.manage') || !before.can_set_floor || !can('model_prefs.manage')) return
  busy.value = true; error.value = ''
  void scope.run(({ after, signal }) => after(putAccountUsage(before, value, signal), result => {
    if (result.account_id !== before.account_id || result.binding_revision !== before.binding_revision || result.revision !== before.revision + 1) throw new Error('Save could not be confirmed')
    policy.value = result; floor.value = result.own_floor_percent; emit('changed')
  }), { failed, settled: () => { busy.value = false } })
}
// The floor and the reset policy share one account revision, so either write moves both forward.
function setResets(next: ResetPolicy) {
  const before = policy.value, shown = resets.value
  if (!before || !shown || busy.value || !mayResets.value || shown.policy === next) return
  busy.value = true; error.value = ''
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
  <section class="usage-policy" data-account-usage>
    <h3>{{ text('Usage', 'Nutzung') }}</h3>
    <div v-if="policy" class="usage-controls">
      <form v-if="policy.can_set_floor && can('account.manage') && can('model_prefs.manage')" class="floor-controls" @submit.prevent="save({ floor_percent: floor })"><label>{{ text('Floor (%)', 'Untergrenze (%)') }}<input v-model.number="floor" type="number" min="0" max="80" step="1" required :disabled="busy" @keydown="floorKey" /></label><button type="submit" class="btn sm" :aria-keyshortcuts="mac ? 'Meta+Enter' : 'Control+Enter'" :disabled="busy">{{ text('Save floor', 'Untergrenze speichern') }}<KeyCap k="mod" /><KeyCap k="enter" /></button></form>
      <p class="policy-note">{{ text(`Floor: ${policy.floor_percent}%. Agents never use it, not even with Use everything or Run now.`, `Untergrenze: ${policy.floor_percent} %. Agenten nutzen sie nie, auch nicht mit „Alles nutzen“ oder Jetzt starten.`) }} {{ text('Pace and Boost today are set per harness on the Agents dial and apply to every account.', 'Tempo und Boost heute werden je Harness am Agenten-Regler eingestellt und gelten für jedes Konto.') }}</p>
      <AccountResetsCard v-if="resets" :account-id="policy.account_id" :harness="resets.harness" :provider="resets.provider" :credits="resets.credits" :policy="resets.policy" :plan="resets.plan" :now="readAt" :can-set="mayResets" :busy="busy" @set="setResets" />
    </div>
    <div class="policy-feedback" aria-live="polite"><p v-if="error" role="alert">{{ error }} <button type="button" class="btn sm" @click="load">{{ text('Reload', 'Neu laden') }}</button></p><p v-else-if="!policy" role="status">{{ busy ? text('Loading usage settings…', 'Nutzungseinstellungen werden geladen…') : text('Usage settings are private.', 'Nutzungseinstellungen sind privat.') }}</p></div>
  </section>
</template>
<style scoped>
.usage-policy { display: grid; gap: 10px; padding-block: 16px; border-block-end: 1px solid var(--line); }.usage-policy h3 { font-size: 14px; }.usage-controls { display: grid; gap: 10px; }.floor-controls { display: flex; flex-wrap: wrap; gap: 12px; align-items: end; }.floor-controls label { display: grid; gap: 6px; font-size: 13px; }.floor-controls input { width: 7ch; min-height: 44px; background: var(--surface-raised); color: var(--ink); border: 1px solid var(--line); border-radius: 6px; padding: 8px; }.policy-note { min-height: 4.5em; font-size: 12px; line-height: 1.5; color: var(--ink-2); }.policy-feedback { height: 6em; overflow: auto; font-size: 12px; color: var(--danger); }
</style>
<style scoped src="../../styles/settingsButtons.css"></style>
