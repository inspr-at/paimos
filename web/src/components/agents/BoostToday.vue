<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { useRoute } from 'vue-router'
import { can, onAccessChange } from '../../lib/authz'
import { getUsageOverview, putAccountBoost, type AccountUsagePolicy } from '../../lib/accountUsage'
import { createScope, scopeOwner } from '../../lib/identityScope'
import { usePoller } from '../../lib/usePolledData'
import { useSession } from '../../stores/session'
const emit = defineEmits<{ changed: [] }>()
const session = useSession(), route = useRoute()
const german = computed(() => route.query.lang === 'de' || document.documentElement.lang.startsWith('de'))
const text = (en: string, de: string) => german.value ? de : en
const allowed = () => session.authenticationCurrent() && session.identity?.principal.kind === 'person' && can('account.read') && can('account.manage')
const owner = () => allowed() ? scopeOwner(session.identity) : ''
const scope = createScope(owner), lane = scope.lane()
const policies = ref<AccountUsagePolicy[]>([]), busy = ref(false), ready = ref(false), error = ref(''), now = ref(Date.now())
const options = [0, 10, 20, 30] as const
const active = (p: AccountUsagePolicy) => p.boost_until && Date.parse(p.boost_until) > now.value ? p.boost_percent ?? 0 : 0
const selected = computed(() => policies.value.length && policies.value.every(p => active(p) === active(policies.value[0]!)) ? active(policies.value[0]!) : null)
const visible = computed(() => !!owner() && (policies.value.length > 0 || !!error.value))
let expiryTimer: ReturnType<typeof setTimeout> | undefined
watch(policies, value => {
  clearTimeout(expiryTimer)
  const expires = value.map(p => p.boost_until ? Date.parse(p.boost_until) : 0).filter(at => at > Date.now())
  if (expires.length) expiryTimer = setTimeout(() => { now.value = Date.now() }, Math.min(...expires) - Date.now())
}, { flush: 'sync' })
const note = computed(() => error.value || (busy.value ? text('Saving…', 'Speichern…') : !ready.value ? text('Reload before changing.', 'Vor der Änderung neu laden.') : selected.value === null ? text('Accounts have different boosts.', 'Konten haben unterschiedliche Erhöhungen.') : text('Your accounts · ends 23:59 local', 'Eigene Konten · endet 23:59 Ortszeit')))
function load(clear = false) {
  if (!owner() || busy.value) return Promise.resolve()
  if (clear) error.value = ''
  ready.value = false
  return lane.run(async ({ after, signal }) => {
    const all: AccountUsagePolicy[] = [], cursors = new Set<string>()
    let cursor: string | undefined
    for (let page = 0; page < 11; page++) {
      const next = await after(getUsageOverview(cursor, signal), value => {
        all.push(...value.accounts.flatMap(a => a.usage_policy?.can_set_posture ? [a.usage_policy] : []))
        if (all.length > 1024 || value.has_more && (!value.next_cursor || cursors.has(value.next_cursor))) throw new Error('Incomplete account list')
        if (!value.has_more) { policies.value = all; now.value = Date.now(); ready.value = true; return undefined }
        cursors.add(value.next_cursor!); return value.next_cursor
      })
      if (!next) return
      cursor = next
    }
    throw new Error('Incomplete account list')
  }, { failed: () => { error.value = text('Could not read Boost today. Reload.', 'Heute mehr nutzen konnte nicht gelesen werden. Neu laden.') } })
}
function save(percent: number) {
  if (!visible.value || !policies.value.length || !ready.value || busy.value || !options.includes(percent as typeof options[number])) return
  const before = policies.value.map(p => ({ ...p }))
  lane.cancel(); busy.value = true; error.value = ''
  const timezone = Intl.DateTimeFormat().resolvedOptions().timeZone
  void scope.run(({ after, signal }) => after(putAccountBoost(before, percent, timezone, signal), result => {
    if (result.accounts.length !== before.length || result.accounts.some(p => {
      const old = before.find(a => a.account_id === p.account_id)
      return !old || p.revision !== old.revision + 1 || p.binding_revision !== old.binding_revision || p.boost_percent !== percent || !p.can_set_posture
    }) || new Set(result.accounts.map(p => p.account_id)).size !== before.length) throw new Error('Save could not be confirmed')
    policies.value = result.accounts; now.value = Date.now(); emit('changed')
  }), { failed: () => { ready.value = false; error.value = text('Could not confirm the change. Reload.', 'Änderung konnte nicht bestätigt werden. Neu laden.') }, settled: () => { busy.value = false } })
}
function reset() { scope.reset(); policies.value = []; busy.value = false; ready.value = false; error.value = ''; void load() }
watch(owner, reset, { immediate: true, flush: 'sync' })
const unsubscribe = onAccessChange(reset)
const poller = usePoller(() => { now.value = Date.now(); return load() }, 20_000, { enabled: () => !!owner() && !busy.value })
onMounted(() => poller.start())
onBeforeUnmount(() => { clearTimeout(expiryTimer); poller.stop(); scope.dispose(); unsubscribe() })
</script>
<template>
  <div v-if="visible" class="boost-today" data-boost-today>
    <div class="boost-heading">{{ text('Boost today', 'Heute mehr nutzen') }}</div>
    <div class="boost-controls">
      <div v-if="policies.length" class="seg boost-options" role="group" :aria-label="text('Boost today', 'Heute mehr nutzen')">
        <button v-for="percent in options" :key="percent" type="button" :data-boost="percent" :aria-pressed="selected === percent" :disabled="busy || !ready" @click="save(percent)">{{ percent ? `+${percent}%` : text('Off', 'Aus') }}</button>
      </div>
      <button type="button" class="boost-reload link-btn" :class="{ hidden: !error && ready || busy }" :disabled="busy" @click="load(true)">{{ text('Reload', 'Neu laden') }}</button>
    </div>
    <p class="boost-note" :class="{ failed: error }" :data-tip="note" tabindex="0" aria-live="polite">{{ note }}</p>
  </div>
</template>
<style scoped>
.boost-today { flex: 0 1 auto; display: grid; gap: 4px; min-width: 0; max-width: 100%; align-self: flex-start; }
.boost-heading { color: var(--ink); font-weight: 650; font-size: 12px; line-height: 18px; }
.boost-controls { display: flex; gap: 8px; }
.boost-note { width: 0; min-width: 100%; height: 18px; margin: 0; font-size: 11px; line-height: 18px; color: var(--ink-3); overflow: hidden; white-space: nowrap; text-overflow: ellipsis; }
.boost-note.failed { color: var(--danger); }
.boost-options { display: flex; }
.boost-options button { min-width: 44px; min-height: 44px; flex: 1; padding-inline: 10px; }
.boost-reload { min-height: 44px; padding: 4px 8px; }.boost-reload.hidden { visibility: hidden; }
@media (max-width: 600px) { .boost-today { flex-basis: 100%; }.boost-controls { justify-self: start; } }
</style>
