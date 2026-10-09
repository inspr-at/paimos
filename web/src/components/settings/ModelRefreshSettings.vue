<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import { api } from '../../lib/api'
import { can, onAccessChange } from '../../lib/authz'
import { useSession } from '../../stores/session'
import { scopeOwner } from '../../lib/identityScope'
import SettingsCard from './SettingsCard.vue'

interface Settings { agent_reports_enabled: boolean; auto_add_profiles: boolean; api_enabled: boolean; interval_minutes: number }
interface Observation { harness: string; model: string; effort: string; pending: boolean; failures: number; suppressed_until: string | null; last_working_at: string | null }
interface Source { account_id: string; vendor: string }
interface Status { settings: Settings; last_run_at: string | null; last_result: { added?: number; sources?: { vendor: string; state: string }[] }; observations: Observation[]; sources: Source[] }
interface Account { id: string; label: string; harness: string }
const props = withDefaults(defineProps<{ german?: boolean }>(), { german: false })
const text = (en: string, de: string) => props.german ? de : en
const session = useSession()
const owner = computed(() => scopeOwner(session.identity))
let generation = 0, alive = true
const capture = () => { const identity = owner.value, turn = generation; return () => alive && identity === owner.value && turn === generation && session.authenticationCurrent() }
const status = ref<Status | null>(null)
const busy = ref(false), error = ref(''), message = ref('')
const accounts = ref<Account[]>([]), accountId = ref(''), apiKey = ref('')
const manageable = computed(() => session.authenticationCurrent() && session.identity?.principal.kind === 'person' && can('models.manage'))
const pending = computed(() => status.value?.observations.filter(o => o.pending) ?? [])
const vendorFor: Record<string, string> = { codex: 'openai', claude: 'anthropic', grok: 'xai', pi: 'openrouter' }
const vendor = computed(() => vendorFor[accounts.value.find(a => a.id === accountId.value)?.harness ?? ''] ?? '')
const configured = computed(() => status.value?.sources.some(s => s.account_id === accountId.value))
async function request(path: string, method = 'GET', body?: unknown) {
  const response = await api(path, { method, ...(body !== undefined ? { headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body) } : {}) })
  if (!response.ok) throw new Error(response.status === 429 ? 'The configured refresh interval has not elapsed. Try again after it expires.' : 'The model settings could not be updated. Please try again.')
  return response.json()
}
async function load() { const current = capture(); const result = await request('/models/refresh') as Status; if (current()) status.value = result }
async function action(fn: () => Promise<void>) {
  if (busy.value || !owner.value || !session.authenticationCurrent() || session.identity?.principal.kind !== 'person') return
  const current = capture(); busy.value = true; error.value = ''; message.value = ''
  let saved = false
  try { await fn(); saved = true; if (current()) await load() } catch (e) { if (current()) { message.value = ''; error.value = saved ? 'Saved, but the refreshed status could not be loaded. Reload before making another change.' : e instanceof Error ? e.message : 'The request failed.' } } finally { if (current()) busy.value = false }
}
async function save() {
  if (!status.value || !manageable.value) return
  const settings = { ...status.value.settings }
  const current = capture()
  await action(async () => { await request('/models/refresh/settings', 'PUT', settings); if (current()) message.value = 'Model refresh settings saved.' })
}
async function refresh() { if (!can('models.refresh')) return; const current = capture(); await action(async () => { await request('/models/refresh', 'POST'); if (current()) message.value = 'Model refresh completed.' }) }
async function accept(o: Observation) {
  if (!manageable.value) return
  const current = capture()
  await action(async () => { await request('/models/proposals/accept', 'POST', { harness: o.harness, model: o.model, effort: o.effort }); if (current()) message.value = 'Profile enabled. Your role order is preserved.' })
}
async function credential(revoke = false) {
  if (!manageable.value) return
  const key = revoke ? '' : apiKey.value
  apiKey.value = ''
  if (!vendor.value || !accountId.value) return
  const current = capture()
  await action(async () => { await request(`/models/refresh/credentials/${accountId.value}`, 'PUT', { vendor: vendor.value, api_key: key }); if (current()) message.value = revoke ? 'Discovery key removed.' : 'Discovery key saved.' })
}
async function initialise() {
  const current = capture()
  try { await load() } catch { if (current()) error.value = 'Model refresh status could not be loaded.' }
  if (!current()) return
  if (manageable.value && can('account.read')) {
    try { const result = await request('/agent-accounts') as Account[]; if (current()) { accounts.value = result.filter(account => vendorFor[account.harness]); accountId.value = accounts.value[0]?.id ?? '' } } catch { /* Key setup needs an enrolled account the person can read. */ }
  }
}
function reset() { generation++; status.value = null; accounts.value = []; accountId.value = ''; apiKey.value = ''; error.value = ''; message.value = ''; busy.value = false; if (owner.value && can('models.read')) void initialise() }
watch(owner, reset, { immediate: true, flush: 'sync' })
const stopAccess = onAccessChange(reset)
onBeforeUnmount(() => { alive = false; generation++; apiKey.value = ''; stopAccess() })

</script>

<template>
  <SettingsCard :title="text('Models', 'Modelle')" icon="gear" anchor="catalog-settings">
    <template #lead>{{ text('Agents keep model availability current as they work. Your role order stays under your control.', 'Agenten halten die Modellverfügbarkeit bei der Arbeit aktuell. Die Reihenfolge bleibt selbst bestimmt.') }}<span class="accounts-link">{{ text('Vendor logins and their quota (Claude, Codex, Cursor) are in', 'Anbieter-Zugänge und ihr Kontingent (Claude, Codex, Cursor) stehen unter') }} <RouterLink to="/settings/accounts">{{ text('Accounts and computers', 'Konten und Computer') }}</RouterLink>.</span></template>
    <div class="feedback"><p v-if="error" role="alert" class="error-line">{{ error }}</p><p v-else-if="message" role="status" class="hint">{{ message }}</p></div>
    <template v-if="status">
      <p class="hint">{{ text('Last refresh:', 'Letzte Aktualisierung:') }} {{ status.last_run_at ? new Date(status.last_run_at).toLocaleString() : text('Waiting for the first refresh', 'Erste Aktualisierung steht aus') }}.</p>
      <p v-for="source in status.last_result.sources ?? []" :key="source.vendor" class="hint">{{ source.vendor }}: {{ source.state === 'stale' ? text('Unavailable; previous catalog kept', 'Nicht verfügbar; bisheriger Katalog bleibt') : source.state === 'limited' ? text('Discovery limit reached', 'Abrufgrenze erreicht') : text('Up to date', 'Aktuell') }}</p>
      <form v-if="manageable" class="controls" @submit.prevent="save">
        <label><input v-model="status.settings.agent_reports_enabled" type="checkbox" :disabled="busy"> {{ text('Accept agent model reports', 'Modellmeldungen von Agenten annehmen') }}</label>
        <label><input v-model="status.settings.auto_add_profiles" type="checkbox" :disabled="busy"> {{ text('Automatically record discovered profiles', 'Entdeckte Profile automatisch erfassen') }}</label>
        <label><input v-model="status.settings.api_enabled" type="checkbox" :disabled="busy"> {{ text('Enable vendor API discovery', 'Anbieter-API für Modellsuche aktivieren') }}</label>
        <p class="hint">{{ text('Discovered profiles need your acceptance before they can run.', 'Entdeckte Profile benötigen eine Freigabe, bevor sie laufen können.') }}</p>
        <p class="hint">{{ text('Optional discovery sends a model-list request to the configured vendor using its saved key. Off by default.', 'Die optionale Suche ruft mit dem gespeicherten Schlüssel die Modellliste des Anbieters ab. Standardmäßig aus.') }}</p>
        <label for="model-refresh-interval">{{ text('Refresh every (minutes)', 'Aktualisieren alle (Minuten)') }}</label>
        <input id="model-refresh-interval" v-model.number="status.settings.interval_minutes" type="number" min="60" max="43200" required :disabled="busy">
        <div class="actions"><button class="btn" type="submit" :disabled="busy">{{ text('Save settings', 'Einstellungen speichern') }}</button><button v-if="can('models.refresh')" class="btn" type="button" :disabled="busy" @click="refresh">{{ text('Refresh now', 'Jetzt aktualisieren') }}</button></div>
      </form>
      <form v-if="manageable && accounts.length" class="controls" @submit.prevent="credential()">
        <label for="model-discovery-account">{{ text('Vendor discovery key for account', 'Suchschlüssel des Anbieters für das Konto') }}</label>
        <select id="model-discovery-account" v-model="accountId" :disabled="busy"><option v-for="account in accounts" :key="account.id" :value="account.id">{{ account.label }} · {{ account.harness }}</option></select>
        <p class="hint">{{ configured ? text('A discovery key is saved for this account.', 'Für dieses Konto ist ein Suchschlüssel gespeichert.') : text('No discovery key is saved for this account.', 'Für dieses Konto ist kein Suchschlüssel gespeichert.') }}</p>
        <label for="model-discovery-key">{{ vendor }} API key</label>
        <input id="model-discovery-key" v-model="apiKey" type="password" autocomplete="off" maxlength="4096" :disabled="busy">
        <div class="actions"><button class="btn" type="submit" :disabled="busy || !apiKey">{{ text('Save key', 'Schlüssel speichern') }}</button><button v-if="configured" class="btn" type="button" :disabled="busy" @click="credential(true)">{{ text('Remove key', 'Schlüssel entfernen') }}</button></div>
      </form>
      <div v-if="pending.length" class="proposals">
        <h3>{{ text('Discovered models awaiting acceptance', 'Entdeckte Modelle warten auf Freigabe') }}</h3>
        <div v-for="o in pending" :key="`${o.harness}/${o.model}/${o.effort}`" class="proposal"><span>{{ o.harness }} · {{ o.model }} · {{ o.effort }}</span><button v-if="manageable" class="btn" :disabled="busy" @click="accept(o)">{{ text('Accept profile', 'Profil freigeben') }}</button></div>
      </div>
      <p v-if="status.observations.some(o => o.failures >= 2)" class="hint">{{ text('Repeated invalid-model reports pause the affected profiles for 24 hours. A successful run clears that pause.', 'Wiederholte Meldungen ungültiger Modelle pausieren betroffene Profile für 24 Stunden. Ein erfolgreicher Lauf beendet die Pause.') }}</p>
    </template>
  </SettingsCard>
</template>

<style scoped>
.feedback { min-height: 3em; font-size: 12.5px; line-height: 1.5; }
.accounts-link { display: block; margin-top: 4px; }
.accounts-link a { color: var(--teal-ink); text-underline-offset: 3px; }
.controls { display: grid; gap: 10px; margin-top: 18px; }
label { display: flex; align-items: center; gap: 9px; font-size: 13px; color: var(--ink); }
input[type="number"], input[type="password"], select { width: 100%; max-width: 440px; min-height: 40px; padding: 8px 10px; border: 1px solid var(--line); border-radius: 8px; background: var(--surface); color: var(--ink); font: inherit; }
input[type="checkbox"] { accent-color: var(--teal); width: 17px; height: 17px; }
.actions { display: flex; flex-wrap: wrap; gap: 8px; }
.hint { color: var(--ink-2); font-size: 12.5px; line-height: 1.5; }
.error-line { color: var(--danger); font-size: 13px; }
.proposals { display: grid; gap: 8px; margin-top: 18px; }
h3 { font-size: 13px; font-weight: 600; }
.proposal { display: flex; align-items: center; justify-content: space-between; gap: 12px; font-size: 13px; overflow-wrap: anywhere; }
</style>
