<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, ref } from 'vue'
import BizIcon from '../business/BizIcon.vue'
import { LAYER_LABEL, LAYERS, putBudget, rulesMessage, type LayerName, type RuleBudgetView } from '../../lib/rules'

// The workspace budget for one session file (AEON-314): a total and optional
// caps per layer. Everyone sees it; people who manage the workspace change it.
const props = defineProps<{ view: RuleBudgetView; canManage: boolean }>()
const emit = defineEmits<{ saved: [view: RuleBudgetView] }>()
const editing = ref(false)
const busy = ref(false)
const error = ref('')
const total = ref('')
const caps = ref<Record<LayerName, string>>({ company: '', project: '', person: '', agent: '' })
const fmt = (n: number) => n.toLocaleString('en-US')

const line = computed(() => {
  const parts = [`${fmt(props.view.max_bytes)} bytes per session file`]
  for (const layer of LAYERS) {
    const cap = props.view.layer_max_bytes[layer]
    if (cap) parts.push(`${LAYER_LABEL[layer]} up to ${fmt(cap)}`)
  }
  return parts.join(' · ')
})

function start() {
  total.value = String(props.view.max_bytes)
  caps.value = { company: '', project: '', person: '', agent: '' }
  for (const layer of LAYERS) caps.value[layer] = props.view.layer_max_bytes[layer] ? String(props.view.layer_max_bytes[layer]) : ''
  error.value = ''
  editing.value = true
}
const whole = (value: string) => /^\d+$/.test(value.trim()) ? Number(value.trim()) : NaN
async function save() {
  const max = whole(total.value)
  if (!(max >= props.view.min_bytes && max <= props.view.ceiling_bytes)) { error.value = `The budget is between ${fmt(props.view.min_bytes)} and ${fmt(props.view.ceiling_bytes)} bytes.`; return }
  const layers: Partial<Record<LayerName, number>> = {}
  for (const layer of LAYERS) {
    const raw = caps.value[layer].trim()
    if (!raw) continue
    const cap = whole(raw)
    if (!(cap >= props.view.min_layer_bytes && cap <= max)) { error.value = `A ${LAYER_LABEL[layer].toLowerCase()} cap is between ${fmt(props.view.min_layer_bytes)} bytes and the total.`; return }
    layers[layer] = cap
  }
  busy.value = true
  error.value = ''
  try {
    emit('saved', await putBudget({ max_bytes: max, layer_max_bytes: layers }))
    editing.value = false
  } catch (cause) { error.value = rulesMessage(cause) } finally { busy.value = false }
}
</script>

<template>
  <section class="budget-section" aria-labelledby="rules-budget-title">
    <div class="head">
      <div class="titles">
        <h3 id="rules-budget-title">Budget</h3>
        <p v-if="!editing" class="value">{{ line }}</p>
        <p v-else class="lede">Always-on text one agent session may receive. Layer caps are optional.</p>
      </div>
      <button v-if="canManage && !editing" type="button" class="btn sm ghost" @click="start">Change</button>
    </div>
    <details v-if="canManage && view.blocking_clients?.length" class="compatibility">
      <summary><BizIcon name="chevron-right" :size="12" class="chev" /><span>Larger files need a client update <span class="opt">· {{ view.blocking_clients.length }}</span></span></summary>
      <p>Clients active in the last seven days limit new budgets to {{ fmt(view.ceiling_bytes) }} bytes.</p>
      <ul>
        <li v-for="(client, index) in view.blocking_clients" :key="index">
          <span class="client-host" :title="client.host">{{ client.host }}</span>
          <span class="client-detail" :title="[client.harness, client.version].filter(Boolean).join(' · ')">{{ client.harness }}<template v-if="client.version"> · {{ client.version }}</template></span>
          <span class="client-limit">{{ fmt(client.max_session_file_bytes) }} bytes</span>
        </li>
      </ul>
    </details>
    <p v-else-if="canManage && view.ceiling_bytes <= view.default_bytes" class="lede">Larger files unlock once active clients report support.</p>
    <form v-if="editing" class="form" @submit.prevent="save" @keydown.esc.prevent="editing = false">
      <label class="fld total"><span>Total <span class="opt">bytes</span></span>
        <input v-model="total" class="field" inputmode="numeric" autocomplete="off" :aria-describedby="'rules-budget-range'">
        <span id="rules-budget-range" class="range">{{ fmt(view.min_bytes) }} to {{ fmt(view.ceiling_bytes) }}<template v-if="view.default_bytes !== view.ceiling_bytes">; default {{ fmt(view.default_bytes) }}</template></span>
      </label>
      <label v-for="layer in LAYERS" :key="layer" class="fld"><span>{{ LAYER_LABEL[layer] }}</span>
        <input v-model="caps[layer]" class="field" inputmode="numeric" autocomplete="off" placeholder="No cap">
      </label>
      <p v-if="error" class="error" role="alert">{{ error }}</p>
      <div class="buttons">
        <button type="button" class="btn sm ghost" :disabled="busy" @click="editing = false">Cancel</button>
        <button type="submit" class="btn sm primary" :disabled="busy">{{ busy ? 'Saving…' : 'Save budget' }}</button>
      </div>
    </form>
  </section>
</template>

<style scoped>
.budget-section { display: flex; flex-direction: column; gap: 10px; padding: 14px 16px; border-radius: 14px; background: var(--surface); box-shadow: 0 0 0 1px var(--line); }
.compatibility { font-size: 12.5px; color: var(--ink-2); }
.compatibility summary { display: flex; align-items: center; gap: 6px; cursor: pointer; width: fit-content; max-width: 100%; }
.chev { flex: none; }
.compatibility[open] .chev { transform: rotate(90deg); }
.compatibility p { margin: 8px 0; color: var(--ink-3); }
.compatibility ul { list-style: none; padding: 0; margin: 0; display: grid; gap: 6px; }
.compatibility li { display: grid; grid-template-columns: minmax(0, 1fr) minmax(0, 1fr) auto; gap: 10px; }
.client-host, .client-detail { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.client-detail, .client-limit { color: var(--ink-3); }
.client-limit { font-variant-numeric: tabular-nums; }
@media (max-width: 480px) {
  .compatibility li { grid-template-columns: minmax(0, 1fr) auto; gap: 2px 8px; }
  .client-host { grid-column: 1 / -1; }
  .client-detail { grid-column: 1; grid-row: 2; }
  .client-limit { grid-column: 2; grid-row: 2; }
}
.head { display: flex; align-items: center; gap: 12px; }
.titles { flex: 1; min-width: 0; }
h3 { margin: 0; font-size: 14px; font-weight: 650; }
.value, .lede { margin: 2px 0 0; color: var(--ink-3); font-size: 13px; font-variant-numeric: tabular-nums; }
.form { display: grid; grid-template-columns: repeat(5, minmax(0, 1fr)); gap: 10px; align-items: start; }
.fld { display: grid; gap: 4px; min-width: 0; color: var(--ink-2); font-size: 12px; font-weight: 650; }
.fld .field { height: 34px; font-weight: 450; color: var(--ink); font-variant-numeric: tabular-nums; }
.opt { color: var(--ink-3); font-weight: 450; }
.range { color: var(--ink-3); font-size: 11.5px; font-weight: 450; }
.error { grid-column: 1 / -1; margin: 0; color: var(--danger); font-size: 12.5px; }
.buttons { grid-column: 1 / -1; display: flex; justify-content: flex-end; gap: 8px; }
@media (max-width: 760px) { .form { grid-template-columns: repeat(2, minmax(0, 1fr)); } .total { grid-column: 1 / -1; } }
</style>
