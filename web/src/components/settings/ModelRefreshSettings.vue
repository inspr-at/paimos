<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { api } from '../../lib/api'
import { can } from '../../lib/authz'
import { useSession } from '../../stores/session'
import SettingsCard from './SettingsCard.vue'

interface Settings { agent_reports_enabled: boolean; auto_add_profiles: boolean; api_enabled: boolean; interval_minutes: number }
interface Observation { harness: string; model: string; effort: string; pending: boolean; failures: number; suppressed_until: string | null; last_working_at: string | null }
interface Source { account_id: string; vendor: string }
interface Status { settings: Settings; last_run_at: string | null; last_result: { added?: number; sources?: { vendor: string; state: string }[] }; observations: Observation[]; sources: Source[] }
interface Account { id: string; label: string; harness: string }
const session = useSession()
const status = ref<Status | null>(null)
const busy = ref(false), error = ref(''), message = ref('')
const accounts = ref<Account[]>([]), accountId = ref(''), apiKey = ref('')
const manageable = computed(() => session.identity?.principal.kind === 'person' && can('models.manage'))
const pending = computed(() => status.value?.observations.filter(o => o.pending) ?? [])
const vendorFor: Record<string, string> = { codex: 'openai', claude: 'anthropic', grok: 'xai', pi: 'openrouter' }
const vendor = computed(() => vendorFor[accounts.value.find(a => a.id === accountId.value)?.harness ?? ''] ?? '')
const configured = computed(() => status.value?.sources.some(s => s.account_id === accountId.value))
async function request(path: string, method = 'GET', body?: unknown) {
  const response = await api(path, { method, ...(body !== undefined ? { headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body) } : {}) })
  if (!response.ok) throw new Error(response.status === 429 ? 'The configured refresh interval has not elapsed. Try again after it expires.' : 'The model settings could not be updated. Please try again.')
  return response.json()
}
async function load() { status.value = await request('/models/refresh') as Status }
async function action(fn: () => Promise<void>) {
  busy.value = true; error.value = ''; message.value = ''
  try { await fn(); await load() } catch (e) { error.value = e instanceof Error ? e.message : 'The request failed.' } finally { busy.value = false }
}
async function save() {
  if (!status.value || !manageable.value) return
  const settings = { ...status.value.settings }
  await action(async () => { await request('/models/refresh/settings', 'PUT', settings); message.value = 'Model refresh settings saved.' })
}
async function refresh() { await action(async () => { await request('/models/refresh', 'POST'); message.value = 'Model refresh completed.' }) }
async function accept(o: Observation) {
  await action(async () => { await request('/models/proposals/accept', 'POST', { harness: o.harness, model: o.model, effort: o.effort }); message.value = 'Profile enabled. Your role order is preserved.' })
}
async function credential(revoke = false) {
  const key = revoke ? '' : apiKey.value
  apiKey.value = ''
  if (!vendor.value || !accountId.value) return
  await action(async () => { await request(`/models/refresh/credentials/${accountId.value}`, 'PUT', { vendor: vendor.value, api_key: key }); message.value = revoke ? 'Discovery key removed.' : 'Discovery key saved.' })
}
onMounted(async () => {
  try { await load() } catch { error.value = 'Model refresh status could not be loaded.' }
  if (manageable.value && can('account.read')) {
    try { accounts.value = (await request('/agent-accounts') as Account[]).filter(a => vendorFor[a.harness]); accountId.value = accounts.value[0]?.id ?? '' } catch { /* Key setup needs an enrolled account the person can read. */ }
  }
})
</script>

<template>
  <SettingsCard title="Models" icon="gear" anchor="models">
    <template #lead>Agents keep model availability current as they work. Your role order stays under your control.<span class="accounts-link">Vendor logins and their quota (Claude, Codex, Cursor) are in <RouterLink to="/settings/accounts">Accounts and computers</RouterLink>.</span></template>
    <p v-if="error" role="alert" class="error-line">{{ error }}</p>
    <p v-if="message" role="status" class="hint">{{ message }}</p>
    <template v-if="status">
      <p class="hint">Last refresh: {{ status.last_run_at ? new Date(status.last_run_at).toLocaleString() : 'Waiting for the first refresh' }}.</p>
      <p v-for="source in status.last_result.sources ?? []" :key="source.vendor" class="hint">{{ source.vendor }}: {{ source.state === 'stale' ? 'Unavailable; previous catalog kept' : source.state === 'limited' ? 'Discovery limit reached' : 'Up to date' }}</p>
      <form v-if="manageable" class="controls" @submit.prevent="save">
        <label><input v-model="status.settings.agent_reports_enabled" type="checkbox" :disabled="busy"> Accept agent model reports</label>
        <label><input v-model="status.settings.auto_add_profiles" type="checkbox" :disabled="busy"> Automatically record discovered profiles</label>
        <label><input v-model="status.settings.api_enabled" type="checkbox" :disabled="busy"> Enable vendor API discovery</label>
        <p class="hint">Discovered profiles need your acceptance before they can run.</p>
        <p class="hint">Optional discovery sends a model-list request to the configured vendor using its saved key. Off by default.</p>
        <label for="model-refresh-interval">Refresh every (minutes)</label>
        <input id="model-refresh-interval" v-model.number="status.settings.interval_minutes" type="number" min="60" max="43200" required :disabled="busy">
        <div class="actions"><button class="btn" type="submit" :disabled="busy">Save settings</button><button v-if="can('models.refresh')" class="btn" type="button" :disabled="busy" @click="refresh">Refresh now</button></div>
      </form>
      <form v-if="manageable && accounts.length" class="controls" @submit.prevent="credential()">
        <label for="model-discovery-account">Vendor discovery key for account</label>
        <select id="model-discovery-account" v-model="accountId" :disabled="busy"><option v-for="account in accounts" :key="account.id" :value="account.id">{{ account.label }} · {{ account.harness }}</option></select>
        <p class="hint">{{ configured ? 'A discovery key is saved for this account.' : 'No discovery key is saved for this account.' }}</p>
        <label for="model-discovery-key">{{ vendor }} API key</label>
        <input id="model-discovery-key" v-model="apiKey" type="password" autocomplete="off" maxlength="4096" :disabled="busy">
        <div class="actions"><button class="btn" type="submit" :disabled="busy || !apiKey">Save key</button><button v-if="configured" class="btn" type="button" :disabled="busy" @click="credential(true)">Remove key</button></div>
      </form>
      <div v-if="pending.length" class="proposals">
        <h3>Discovered models awaiting acceptance</h3>
        <div v-for="o in pending" :key="`${o.harness}/${o.model}/${o.effort}`" class="proposal"><span>{{ o.harness }} · {{ o.model }} · {{ o.effort }}</span><button v-if="manageable" class="btn" :disabled="busy" @click="accept(o)">Accept profile</button></div>
      </div>
      <p v-if="status.observations.some(o => o.failures >= 2)" class="hint">Repeated invalid-model reports pause the affected profiles for 24 hours. A successful run clears that pause.</p>
    </template>
  </SettingsCard>
</template>

<style scoped>
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
