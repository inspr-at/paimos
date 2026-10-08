<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import { can, onAccessChange } from '../../lib/authz'
import { createScope, scopeOwner } from '../../lib/identityScope'
import { getUsageOverview, putAccountUsage, type AccountUsagePolicy, type UsagePosture } from '../../lib/accountUsage'
import { useSession } from '../../stores/session'
import KeyCap from '../KeyCap.vue'
const props = defineProps<{ accountId: string; german?: boolean }>()
const emit = defineEmits<{ changed: [] }>()
const session = useSession(), policy = ref<AccountUsagePolicy | null>(null), busy = ref(false), error = ref(''), floor = ref(0)
const text = (en: string, de: string) => props.german ? de : en
const mac = /Mac|iPhone|iPad/.test(navigator.platform || navigator.userAgent)
const key = computed(() => `${scopeOwner(session.identity)}/${props.accountId}`)
const scope = createScope(() => session.authenticationCurrent() && can('account.read') ? key.value : '')
const options = [['careful', 'Careful', 'Schonend'], ['balanced', 'Balanced', 'Ausgewogen'], ['maxout', 'Max out', 'Ausschöpfen']] as const
function load() {
  policy.value = null; busy.value = true; error.value = ''
  void scope.run(async ({ after, signal }) => {
    let cursor: string | undefined
    // The server bounds the tenant to 1024 accounts; never loop on an invalid cursor.
    for (let page = 0; page < 11; page++) {
      const next = await after(getUsageOverview(cursor, signal), value => {
        const row = value.accounts.find(account => account.account_id === props.accountId)
        if (row) { policy.value = row.usage_policy ?? null; floor.value = row.usage_policy?.own_floor_percent ?? 0; return undefined }
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
const sourceNote = computed(() => policy.value?.source === 'account' ? text('Set on this account by its owner.', 'Von der besitzenden Person am Konto gesetzt.') : policy.value?.source === 'workspace' ? text('Follows the workspace usage default.', 'Folgt der Nutzungsvorgabe des Arbeitsbereichs.') : policy.value?.source === 'schedule' ? text('Follows the existing account pacing.', 'Folgt der bestehenden Kontotaktung.') : text('Follows the account owner’s setting.', 'Folgt der Einstellung der besitzenden Person.'))
function save(value: { posture: UsagePosture | null } | { floor_percent: number }) {
  const before = policy.value
  if (!before || busy.value || session.identity?.principal.kind !== 'person' || !can('account.manage') || ('posture' in value ? !before.can_set_posture : !before.can_set_floor || !can('model_prefs.manage'))) return
  busy.value = true; error.value = ''
  void scope.run(({ after, signal }) => after(putAccountUsage(before, value, signal), result => {
    if (result.account_id !== before.account_id || result.binding_revision !== before.binding_revision || result.revision !== before.revision + 1) throw new Error('Save could not be confirmed')
    policy.value = result; floor.value = result.own_floor_percent; emit('changed')
  }), { failed: () => { error.value = text('The change could not be confirmed. Reload before trying again.', 'Die Änderung konnte nicht bestätigt werden. Vor dem nächsten Versuch neu laden.') }, settled: () => { busy.value = false } })
}
watch(key, () => { scope.reset(); load() }, { immediate: true, flush: 'sync' })
const unsubscribe = onAccessChange(() => { scope.reset(); policy.value = null; busy.value = false; if (can('account.read')) load() })
onBeforeUnmount(() => { scope.dispose(); unsubscribe() })
</script>
<template>
  <section class="usage-policy" data-account-usage>
    <h3>{{ text('Usage', 'Nutzung') }}</h3>
    <div v-if="policy" class="usage-controls">
      <div class="usage-options" role="group" :aria-label="text('Account usage', 'Kontonutzung')"><template v-for="[value, en, de] in options" :key="value"><button v-if="policy.can_set_posture && can('account.manage')" type="button" :data-account-posture="value" :aria-pressed="policy.posture === value" :disabled="busy" @click="save({ posture: value })">{{ text(en, de) }}</button><span v-else :class="{ selected: policy.posture === value }">{{ text(en, de) }}</span></template></div>
      <button v-if="policy.can_set_posture && can('account.manage')" type="button" class="btn sm" :disabled="busy" @click="save({ posture: null })">{{ text('Follow my Models setting', 'Meiner Modelleinstellung folgen') }}</button>
      <form v-if="policy.can_set_floor && can('account.manage') && can('model_prefs.manage')" class="floor-controls" @submit.prevent="save({ floor_percent: floor })"><label>{{ text('Floor (%)', 'Untergrenze (%)') }}<input v-model.number="floor" type="number" min="0" max="80" step="1" required :disabled="busy" @keydown="floorKey" /></label><button type="submit" class="btn sm" :aria-keyshortcuts="mac ? 'Meta+Enter' : 'Control+Enter'" :disabled="busy">{{ text('Save floor', 'Untergrenze speichern') }}<KeyCap k="mod" /><KeyCap k="enter" /></button></form>
      <p class="policy-note">{{ sourceNote }} {{ text(`Floor: ${policy.floor_percent}%. It also applies to Max out and Run now.`, `Untergrenze: ${policy.floor_percent} %. Sie gilt auch für Ausschöpfen und Jetzt starten.`) }}</p>
    </div>
    <div class="policy-feedback" aria-live="polite"><p v-if="error" role="alert">{{ error }} <button type="button" class="btn sm" @click="load">{{ text('Reload', 'Neu laden') }}</button></p><p v-else-if="!policy" role="status">{{ busy ? text('Loading usage settings…', 'Nutzungseinstellungen werden geladen…') : text('Usage settings are private.', 'Nutzungseinstellungen sind privat.') }}</p></div>
  </section>
</template>
<style scoped>
.usage-policy { display: grid; gap: 10px; padding-block: 16px; border-block-end: 1px solid var(--line); }.usage-policy h3 { font-size: 14px; }.usage-controls { display: grid; gap: 10px; justify-items: start; }.usage-options { display: grid; grid-template-columns: repeat(3, minmax(0, 1fr)); border: 1px solid var(--line); border-radius: 8px; overflow: hidden; width: 100%; }.usage-options > * { min-height: 44px; padding: 8px; display: grid; place-items: center; border: 0; background: transparent; color: var(--ink-2); font: inherit; font-size: 13px; }.usage-options [aria-pressed="true"], .usage-options .selected { background: var(--row-selected); color: var(--ink); font-weight: 650; }.usage-options button:focus-visible { outline: 2px solid var(--teal); outline-offset: -2px; }.floor-controls { display: flex; flex-wrap: wrap; gap: 12px; align-items: end; }.floor-controls label { display: grid; gap: 6px; font-size: 13px; }.floor-controls input { width: 7ch; min-height: 44px; background: var(--surface-raised); color: var(--ink); border: 1px solid var(--line); border-radius: 6px; padding: 8px; }.policy-note { min-height: 4.5em; font-size: 12px; line-height: 1.5; color: var(--ink-2); }.policy-feedback { height: 6em; overflow: auto; font-size: 12px; color: var(--danger); }
</style>
<style scoped src="../../styles/settingsButtons.css"></style>
