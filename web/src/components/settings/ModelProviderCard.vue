<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { api } from '../../lib/api'
import SettingsCard from './SettingsCard.vue'

interface ProviderSettings {
  enabled: boolean
  base_url: string
  chat_model: string
  embedding_model: string
  features: { crm_note_rewrite: boolean; embeddings: boolean }
  revision: number
  has_api_key: boolean
}
const settings = ref<ProviderSettings | null>(null)
const saved = ref('')
const apiKey = ref('')
const clearKey = ref(false)
const busy = ref(false)
const loading = ref(true)
const problem = ref('')
const notice = ref('')
const dirty = computed(() => JSON.stringify(settings.value) !== saved.value || !!apiKey.value || clearKey.value)

function accept(next: ProviderSettings) {
  settings.value = next
  saved.value = JSON.stringify(next)
  apiKey.value = ''; clearKey.value = false
}
async function load() {
  loading.value = true; problem.value = ''
  try {
    const response = await api('/settings/model-provider')
    if (!response.ok) throw new Error('read')
    accept(await response.json() as ProviderSettings)
  } catch { problem.value = 'Model provider settings could not be loaded. Try again.' }
  finally { loading.value = false }
}
onMounted(load)

async function save() {
  if (!settings.value || busy.value) return
  busy.value = true; problem.value = ''; notice.value = ''
  try {
    const current = settings.value
    const response = await api('/settings/model-provider', {
      method: 'PUT', headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ enabled: current.enabled, base_url: current.base_url.trim(), chat_model: current.chat_model.trim(), embedding_model: current.embedding_model.trim(), features: current.features, expected_revision: current.revision,
        ...(clearKey.value ? { api_key: '' } : apiKey.value ? { api_key: apiKey.value } : {}),
      }),
    })
    if (!response.ok) {
      if (response.status === 409) { problem.value = 'Another admin changed these settings. Reload before saving.'; return }
      const result = await response.json().catch(() => ({})) as { error?: string }
      problem.value = result.error || 'The settings could not be saved.'; return
    }
    accept(await response.json() as ProviderSettings)
    notice.value = 'Saved. Only the selected features may use this provider.'
  } catch { problem.value = 'The settings could not be saved. Check the connection and try again.' }
  finally { busy.value = false; apiKey.value = '' }
}
async function test() {
  if (!settings.value || busy.value || dirty.value) return
  busy.value = true; problem.value = ''; notice.value = ''
  try {
    const response = await api('/settings/model-provider/test', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ expected_revision: settings.value.revision }) }, 35_000)
    if (!response.ok) { problem.value = response.status === 409 ? 'Save or reload the provider settings before testing.' : 'Connection test failed. Check the server address, model and API key.'; return }
    notice.value = 'Connection succeeded. The chat model answered the test.'
  } catch { problem.value = 'Connection test failed. Check that the model server is reachable from Aeon.' }
  finally { busy.value = false }
}
</script>

<template>
  <SettingsCard title="In-app AI" icon="agent" anchor="model-provider">
    <template #lead>Use a model you control for selected workspace features. Off by default.</template>
    <p v-if="loading" role="status">Loading model provider settings…</p>
    <form v-else-if="settings" class="provider-form" @submit.prevent="save">
      <fieldset :disabled="busy">
        <label class="check"><input v-model="settings.enabled" type="checkbox" />Enable workspace AI</label>
        <label for="model-base">API base URL</label>
        <input id="model-base" v-model="settings.base_url" type="url" maxlength="2048" :required="settings.enabled" placeholder="http://localhost:11434/v1" autocomplete="off" spellcheck="false" aria-describedby="model-base-help" />
        <p id="model-base-help" class="hint">Ollama example: http://localhost:11434/v1. This address is reached from the Aeon server; localhost means that server.</p>
        <label for="chat-model">Chat model</label>
        <input id="chat-model" v-model="settings.chat_model" maxlength="200" :required="settings.enabled" placeholder="Your installed model name" autocomplete="off" spellcheck="false" />
        <label for="model-key">API key <span class="hint">optional</span></label>
        <input id="model-key" v-model="apiKey" type="password" maxlength="16384" autocomplete="new-password" :disabled="clearKey" :placeholder="settings.has_api_key ? 'Key stored — leave blank to keep it' : 'No key stored'" aria-describedby="model-key-help" />
        <p id="model-key-help" class="hint">Keys are encrypted. Changing the base URL clears the stored key unless you enter a replacement.</p>
        <label v-if="settings.has_api_key" class="check"><input v-model="clearKey" type="checkbox" />Remove stored API key</label>
        <h3>Features allowed to use this provider</h3>
        <label class="check"><input v-model="settings.features.crm_note_rewrite" type="checkbox" />Rewrite customer notes with AI</label>
        <p class="hint">Requires CRM and its AI tool grant. Suggestions remain drafts until a person applies them.</p>
        <label class="check"><input v-model="settings.features.embeddings" type="checkbox" />Semantic search and background indexing</label>
        <label for="embedding-model">Embedding model</label>
        <input id="embedding-model" v-model="settings.embedding_model" maxlength="200" :required="settings.features.embeddings" placeholder="Model returning 1536 dimensions" autocomplete="off" spellcheck="false" />
        <p class="hint">Uses the same server and API key. Selected features send their content to this server. Agent runs and Aithema use their own settings.</p>
      </fieldset>
      <div class="actions">
        <button type="submit" class="btn" :disabled="busy || !dirty">{{ busy ? 'Working…' : 'Save settings' }}</button>
        <button type="button" class="btn" :disabled="busy || dirty || !settings.base_url || !settings.chat_model" @click="test">Test connection</button>
        <span v-if="dirty" class="hint">Save before testing.</span>
      </div>
      <p class="hint">The test sends a small sample prompt, even when workspace AI is off.</p>
    </form>
    <p v-if="problem" class="problem" role="alert">{{ problem }} <button type="button" class="btn sm" :disabled="busy" @click="load">Reload</button></p>
    <p v-if="notice" role="status" class="notice">{{ notice }}</p>
  </SettingsCard>
</template>

<style scoped>
.provider-form { display: grid; gap: 12px; }
fieldset { display: grid; gap: 8px; margin: 0; padding: 0; border: 0; min-width: 0; }
label { color: var(--ink); font-size: 13px; }
input:not([type=checkbox]) { width: 100%; min-width: 0; padding: 9px 12px; border: 1px solid var(--line); border-radius: 8px; background: var(--surface-sunken); color: var(--ink); font: 14px var(--font); }
input:focus-visible { outline: 2px solid var(--teal-ink); outline-offset: 2px; }
.check { display: flex; align-items: center; gap: 8px; min-height: 32px; }
h3 { margin-top: 12px; font-size: 14px; }
.hint { color: var(--ink-2); font-size: 12px; line-height: 1.5; }
.actions { display: flex; flex-wrap: wrap; align-items: center; gap: 8px; }
.problem, .notice { margin-top: 12px; font-size: 13px; }
.problem { color: var(--red-ink); }
@media (max-width: 600px) { .check, .btn, input:not([type=checkbox]) { min-height: 44px; } }
</style>
