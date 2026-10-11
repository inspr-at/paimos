<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, ref, useId, watch } from 'vue'
import AppIcon from '../../AppIcon.vue'
import { HARNESS_NAME, intervalLabel, type Harness, type Observation, type RefreshSettings, type RefreshStatus } from '../../../lib/modelRegistry'

// Settings › Models › Model registry › How new models are found (AEON-1163, merging the old Model catalog form per
// AEON-1138). A quiet fold under the auto-update strip: agent reports, vendor model lists, the check interval, the
// vendor keys those lists use, and models that wait for a person. Auto-update itself stays the strip's switch, the
// one writer of that rule (AEON-1148). The card owns every write; this only shows and asks.
export interface DiscoveryAccount { id: string; label: string; harness: string; vendor: string }
const props = defineProps<{ status: RefreshStatus; open: boolean; manage: boolean; busy: boolean; error: string; note: string; accounts: DiscoveryAccount[] | null }>()
const emit = defineEmits<{
  toggle: []; set: [change: Partial<Pick<RefreshSettings, 'agent_reports_enabled' | 'api_enabled' | 'interval_minutes'>>]
  saveKey: [account: DiscoveryAccount, key: string]; accept: [observation: Observation]
}>()
const uid = useId()
const settings = computed(() => props.status.settings)
const INTERVALS = [60, 180, 360, 720, 1440, 4320, 10080]
const intervals = computed(() => [...new Set([...INTERVALS, settings.value.interval_minutes])].sort((a, b) => a - b))
const every = (minutes: number) => minutes % 1440 === 0 ? `${minutes / 1440} day${minutes === 1440 ? '' : 's'}` : intervalLabel(minutes)
const summary = computed(() => [
  `agent reports ${settings.value.agent_reports_enabled ? 'on' : 'off'}`,
  `vendor lists ${settings.value.api_enabled ? 'on' : 'off'}`,
  `every ${every(settings.value.interval_minutes)}`,
].join(' · '))
const VENDOR_NAME: Record<string, string> = { openai: 'OpenAI', anthropic: 'Anthropic', xai: 'xAI', openrouter: 'OpenRouter', google: 'Google' }
const sources = computed(() => (props.status.last_result?.sources ?? []).filter(source => !!source?.vendor).map(source => ({
  vendor: VENDOR_NAME[source.vendor!] ?? source.vendor!,
  state: source.state === 'stale' ? 'could not be read; its last list is kept' : source.state === 'limited' ? 'read in part; the discovery limit was reached' : 'up to date',
})))
const waiting = computed(() => (props.status.observations ?? []).filter(observation => observation.pending))
const failing = computed(() => (props.status.observations ?? []).some(observation => observation.failures >= 2))
const harnessName = (harness: string) => HARNESS_NAME[harness as Harness] ?? harness

// The key field is cleared once it is sent; a saved key is never shown again.
const accountId = ref(''), key = ref('')
watch(() => props.accounts, list => { if (list && !list.some(account => account.id === accountId.value)) accountId.value = list[0]?.id ?? '' }, { immediate: true })
const account = computed(() => props.accounts?.find(candidate => candidate.id === accountId.value) ?? null)
const saved = computed(() => !!account.value && (props.status.sources ?? []).some(source => source.account_id === account.value!.id))
function sendKey(remove = false) {
  const chosen = account.value
  if (!chosen || props.busy || (!remove && !key.value)) return
  const value = remove ? '' : key.value
  key.value = ''
  emit('saveKey', chosen, value)
}
const typed = (event: Event) => (event.target as HTMLInputElement | HTMLSelectElement).value
const checked = (event: Event) => (event.target as HTMLInputElement).checked
</script>

<template>
  <div class="disc" data-reg-discovery>
    <button type="button" class="disc-fold" :aria-expanded="open" :aria-controls="`${uid}-body`" data-reg-discovery-fold @click="emit('toggle')">
      <AppIcon name="chevron-right" :size="14" class="disc-chev" /><b>How new models are found</b><span class="disc-sum">{{ summary }}</span>
    </button>
    <div v-if="open" :id="`${uid}-body`" class="disc-body" data-reg-discovery-body>
      <div class="disc-row">
        <span class="disc-l"><b :id="`${uid}-reports`">Models agents report</b><span>Harnesses tell PAIMOS which models and levels they offer.</span></span>
        <label class="switch"><input type="checkbox" role="switch" :aria-labelledby="`${uid}-reports`" data-reg-reports :checked="settings.agent_reports_enabled" :disabled="!manage || busy" @change="emit('set', { agent_reports_enabled: checked($event) })"><span class="sr-only">Models agents report</span></label>
      </div>
      <div class="disc-row">
        <span class="disc-l"><b :id="`${uid}-vendors`">Vendor model lists</b><span>Scheduled checks read public model lists, and a vendor’s own list with a key saved below. Off by default.</span></span>
        <label class="switch"><input type="checkbox" role="switch" :aria-labelledby="`${uid}-vendors`" data-reg-vendors :checked="settings.api_enabled" :disabled="!manage || busy" @change="emit('set', { api_enabled: checked($event) })"><span class="sr-only">Vendor model lists</span></label>
      </div>
      <div class="disc-row">
        <span class="disc-l"><label :for="`${uid}-every`"><b>Check every</b></label><span>Check now doesn’t wait for this.</span></span>
        <select :id="`${uid}-every`" class="field disc-select" data-reg-interval :value="settings.interval_minutes" :disabled="!manage || busy" @change="emit('set', { interval_minutes: Number(typed($event)) })">
          <option v-for="minutes in intervals" :key="minutes" :value="minutes">{{ every(minutes) }}</option>
        </select>
      </div>
      <p v-if="!manage" class="disc-hint">Admins change how models are found.</p>

      <div v-if="manage && accounts?.length" class="disc-keys" role="group" :aria-labelledby="`${uid}-keys`">
        <p :id="`${uid}-keys`" class="disc-h">Vendor keys for model lists</p>
        <div class="disc-key">
          <select class="field" aria-label="Account" data-reg-key-account :value="accountId" :disabled="busy" @change="accountId = typed($event)">
            <option v-for="item in accounts" :key="item.id" :value="item.id">{{ item.label }} · {{ harnessName(item.harness) }}</option>
          </select>
          <input v-model="key" class="field" type="password" autocomplete="off" maxlength="4096" spellcheck="false" :aria-label="`${account ? VENDOR_NAME[account.vendor] ?? account.vendor : 'Vendor'} API key`" placeholder="Paste a key" data-reg-key :disabled="busy" @keydown.enter.prevent="sendKey()">
          <button type="button" class="btn sm" data-reg-key-save :disabled="busy || !key" @click="sendKey()">Save key</button>
          <button v-if="saved" type="button" class="btn sm ghost danger-t" data-reg-key-remove :disabled="busy" @click="sendKey(true)">Remove key</button>
        </div>
        <p class="disc-hint">{{ saved ? 'A key is saved for this account. It is never shown again.' : 'No key is saved for this account.' }}</p>
      </div>

      <div v-if="waiting.length" class="disc-wait" role="group" :aria-labelledby="`${uid}-wait`">
        <p :id="`${uid}-wait`" class="disc-h">Found, waiting for you · {{ waiting.length }}</p>
        <ul>
          <li v-for="item in waiting" :key="`${item.harness}/${item.model}/${item.effort}`" class="disc-w" :data-reg-waiting="`${item.harness}/${item.model}/${item.effort}`">
            <span class="disc-w-t">{{ harnessName(item.harness) }} · <code>{{ item.model }}</code> · {{ item.effort }}</span>
            <button v-if="manage" type="button" class="btn sm" :disabled="busy" @click="emit('accept', item)">Accept</button>
          </li>
        </ul>
      </div>
      <ul v-if="sources.length" class="disc-src" aria-label="Last check per vendor">
        <li v-for="source in sources" :key="source.vendor"><b>{{ source.vendor }}</b>: {{ source.state }}</li>
      </ul>
      <p v-if="failing" class="disc-hint">A model that a harness reports as invalid twice is paused for 24 hours; a successful run ends that pause.</p>
      <p v-if="error" class="disc-msg err" role="alert" data-reg-discovery-error><AppIcon name="alert" :size="14" />{{ error }}</p>
      <p v-else-if="note" class="disc-msg" role="status">{{ note }}</p>
    </div>
  </div>
</template>

<style scoped>
.disc { margin-top: 6px; }
.disc-fold { display: flex; align-items: center; flex-wrap: wrap; gap: 4px 8px; width: 100%; min-height: 40px; padding: 6px 16px; border: 0; border-radius: 10px; background: transparent; color: var(--ink-2); font: inherit; font-size: 13px; text-align: left; cursor: pointer; }
.disc-fold:hover { background: var(--row-hover); }
.disc-fold:focus-visible { outline: none; box-shadow: var(--focus-ring); }
.disc-fold b { font-weight: 600; color: var(--ink); }
.disc-chev { color: var(--ink-3); transition: transform .2s ease; }
.disc-fold[aria-expanded="true"] .disc-chev { transform: rotate(90deg); }
@media (prefers-reduced-motion: reduce) { .disc-chev { transition: none; } }
.disc-sum { color: var(--ink-3); }
.disc-body { display: grid; gap: 2px; padding: 4px 16px 12px 38px; }
.disc-row { display: flex; align-items: center; gap: 16px; min-height: 52px; padding: 6px 0; border-bottom: 1px solid var(--line); }
.disc-l { display: grid; flex: 1; min-width: 0; font-size: 13px; color: var(--ink-2); }
.disc-l b { font-weight: 600; color: var(--ink); }
.disc-l span { font-size: 12.5px; line-height: 1.45; color: var(--ink-3); }
.disc-select { width: auto; min-width: 120px; height: 36px; padding-right: 28px; }
/* The switch's whole box is its target: 44 px wide and 40 px tall, 44 px on touch screens. */
.disc-row .switch { flex: none; justify-content: center; min-width: 44px; min-height: 40px; }
.disc-h { margin: 14px 0 6px; font-size: 12.5px; font-weight: 600; color: var(--ink); }
.disc-key { display: flex; flex-wrap: wrap; align-items: center; gap: 8px; }
.disc-key .field { height: 36px; flex: 1 1 180px; min-width: 0; }
.disc-key select.field { flex: 0 1 auto; padding-right: 28px; }
.disc-hint { margin-top: 6px; font-size: 12.5px; line-height: 1.5; color: var(--ink-3); }
.disc-wait ul, .disc-src { list-style: none; margin: 0; padding: 0; }
.disc-w { display: flex; flex-wrap: wrap; align-items: center; justify-content: space-between; gap: 6px 12px; min-height: 44px; padding: 4px 0; border-bottom: 1px solid var(--line); font-size: 12.5px; color: var(--ink-2); }
.disc-w-t { min-width: 0; overflow-wrap: anywhere; }
.disc-w code { font: 500 11.5px/1.4 var(--mono); color: var(--ink); }
.disc-src { margin-top: 10px; display: grid; gap: 2px; font-size: 12.5px; color: var(--ink-2); }
.disc-src b { font-weight: 600; color: var(--ink); }
.disc-msg { display: flex; align-items: flex-start; gap: 8px; margin-top: 10px; font-size: 13px; line-height: 1.5; color: var(--ink-2); }
.disc-msg.err { color: var(--danger); }
.disc-msg :deep(svg) { flex: none; margin-top: 3px; }
.btn.ghost.danger-t { color: var(--danger); }
@container (max-width: 600px) {
  .disc-fold { padding: 6px 12px; }
  .disc-body { padding: 4px 12px 12px; }
}
@media (pointer: coarse) { .disc-fold, .disc-row .switch { min-height: 44px; } }
</style>
<style scoped src="../../../styles/settingsButtons.css"></style>
