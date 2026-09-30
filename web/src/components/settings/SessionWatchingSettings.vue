<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
import { brand } from '../../lib/brand'
import { api } from '../../lib/api'
import { can, onAccessChange } from '../../lib/authz'
import AppIcon from '../AppIcon.vue'
import type { LocalAuthCapability, LocalAuthComputer, WatchConsentMode } from '../../lib/attachWatch'

const saved = ref<WatchConsentMode | null>(null)
const choice = ref<WatchConsentMode>('aeon')
const computers = ref<LocalAuthComputer[]>([])
const busy = ref(false)
const error = ref('')
const notice = ref('')
let operation: AbortController | undefined

const known: LocalAuthCapability[] = ['available', 'unsupported', 'unsigned', 'no_gui', 'policy', 'unreported']
function readComputers(value: unknown): LocalAuthComputer[] {
  if (!Array.isArray(value)) return []
  return value.flatMap(item => {
    if (!item || typeof item !== 'object') return []
    const row = item as Partial<LocalAuthComputer>
    const capability = known.find(code => code === row.capability) ?? 'unreported'
    const name = typeof row.name === 'string' && row.name.trim() ? row.name : 'Paired computer'
    return [{ computer_id: typeof row.computer_id === 'string' ? row.computer_id : name, name, capability, pairing_upgraded: row.pairing_upgraded === true }]
  })
}
function limitation(computer: LocalAuthComputer): string | null {
  const name = computer.name || 'Paired computer'
  if (!computer.pairing_upgraded && computer.capability !== 'unsupported') return `${name}: upgrade this computer’s pairing to enable Touch ID`
  switch (computer.capability) {
    case 'available': return null
    case 'unsupported': return `${name} cannot confirm on the Mac`
    case 'unsigned': return `${name} needs a signed daemon`
    case 'no_gui': return `${name} has no graphical session`
    case 'policy': return `${name} cannot use Touch ID`
    default: return `${name} has not reported Touch ID support`
  }
}
const canPickLocal = computed(() => computers.value.some(computer => computer.capability === 'available' && computer.pairing_upgraded))
const savedUnavailable = computed(() => saved.value === 'local_auth' && !canPickLocal.value && computers.value.some(computer => computer.pairing_upgraded))
// Watches stay off only while Mac confirmation is the selected or saved mode.
const macConfirmation = computed(() => choice.value === 'local_auth' || saved.value === 'local_auth')
const blocked = computed(() => computers.value.flatMap(computer => {
  const line = limitation(computer)
  if (!line) return []
  if (!computer.pairing_upgraded) return [`${line}. Approval stays in ${brand.value.short_name}.`]
  return [macConfirmation.value ? `${line}, so watches there stay off.` : `${line}.`]
}))

async function request(save = false) {
  if (save && (!can('profile.write') || (choice.value === 'local_auth' && !canPickLocal.value))) return
  operation?.abort()
  const controller = new AbortController(); operation = controller
  busy.value = true; error.value = ''; notice.value = ''
  try {
    const response = await api('/me/security/session-watching', {
      method: save ? 'PUT' : 'GET', signal: controller.signal,
      ...(save ? { headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ consent_mode: choice.value }) } : {}),
    })
    if (!response.ok) throw new Error('request failed')
    const result = await response.json() as { consent_mode?: WatchConsentMode; local_auth_computers?: unknown }
    if (result.consent_mode !== 'aeon' && result.consent_mode !== 'local_auth') throw new Error('invalid mode')
    if (controller.signal.aborted || operation !== controller) return
    computers.value = readComputers(result.local_auth_computers)
    saved.value = choice.value = result.consent_mode
    if (save) notice.value = 'Saved for future approvals.'
  } catch {
    if (!controller.signal.aborted) error.value = save ? 'Your setting was not saved. Try again.' : 'Your setting could not be loaded.'
  } finally { if (operation === controller) busy.value = false }
}
const stopAccess = onAccessChange(() => { if (!can('profile.read')) { operation?.abort(); saved.value = null; busy.value = false } })
onMounted(() => void request())
onBeforeUnmount(() => { operation?.abort(); stopAccess() })
</script>

<template>
  <div class="watch-security">
    <h3>Session watching</h3>
    <p class="intro">Choose how you approve sharing a running session’s new turns.</p>
    <p v-if="busy && !saved" role="status">Loading your setting…</p>
    <form v-else-if="saved" @submit.prevent="request(true)">
      <fieldset :disabled="busy || !can('profile.write')">
        <legend class="sr-only">Session watching consent</legend>
        <label :class="{ selected: choice === 'aeon' }">
          <input v-model="choice" type="radio" name="watch-consent" value="aeon" />
          <span><strong>Approve in {{ brand.short_name }} <small>Default</small></strong><span>Review the process and allow each watch here.</span></span>
        </label>
        <label :class="{ selected: choice === 'local_auth', unavailable: !canPickLocal }">
          <input v-model="choice" type="radio" name="watch-consent" value="local_auth" :disabled="!canPickLocal" />
          <span>
            <strong>Also confirm on the Mac</strong>
            <span v-if="canPickLocal">After approval here, confirm with Touch ID.</span>
          </span>
        </label>
      </fieldset>
      <p v-if="computers.length === 0" class="reasons">No paired computer has reported Touch ID support.</p>
      <ul v-else-if="blocked.length" class="reasons">
        <li v-for="line in blocked" :key="line">{{ line }}</li>
      </ul>
      <p v-if="savedUnavailable" class="reasons" role="status">Mac confirmation is saved, but no paired computer can run it.</p>
      <p class="footnote">Applies to future approvals; active watches keep their current approval.</p>
      <footer><span role="status">{{ notice }}</span><button class="btn primary" type="submit" :disabled="busy || choice === saved || !can('profile.write') || (choice === 'local_auth' && !canPickLocal)">{{ busy ? 'Saving…' : 'Save setting' }}</button></footer>
    </form>
    <p v-if="error" class="error" role="alert">{{ error }} <button v-if="!saved" class="btn sm" type="button" @click="request()">Try again</button></p>
    <details><summary><AppIcon name="chevron-right" class="disclosure-chev" :size="12" />About local confirmation</summary><p>The terminal WATCH prompt is a best-effort check that same-user processes can imitate.</p><p>Touch ID needs an upgraded pairing, a signed daemon and an interactive login; an unavailable or cancelled prompt never activates a watch.</p></details>
  </div>
</template>

<style scoped>
h3 { font-size: 14px; font-weight: 600; margin: 0; }
p { font-size: 13px; line-height: 1.5; }
.watch-security { overflow-wrap: anywhere; }
.intro { color: var(--ink-2); margin: 4px 0 16px; }
fieldset { display: grid; gap: 8px; padding: 0; margin: 0; border: 0; min-width: 0; }
label { display: flex; align-items: flex-start; gap: 12px; padding: 14px; border: 1px solid var(--line); border-radius: 10px; cursor: pointer; }
label.selected { background: var(--surface-2); border-color: var(--line-2); }
label.unavailable { cursor: not-allowed; }
label.unavailable:not(.selected),
label.unavailable:not(.selected) strong { color: var(--ink-2); }
label:focus-within { box-shadow: var(--focus-ring); }
input { flex-shrink: 0; margin: 3px 0 0; accent-color: var(--teal-ink); }
label.unavailable input { cursor: not-allowed; }
label > span { display: grid; gap: 3px; min-width: 0; }
strong { font-size: 13px; font-weight: 600; }
strong small { color: var(--ink-2); font-size: 11px; font-weight: 400; margin-left: 8px; }
label span span { font-size: 13px; line-height: 1.5; color: var(--ink-2); }
.reasons { list-style: none; padding: 0; margin: 8px 0 0; display: grid; gap: 4px; color: var(--ink-2); font-size: 12px; line-height: 1.5; }
.reasons li { min-width: 0; }
.footnote { color: var(--ink-2); font-size: 12px; margin: 12px 0; }
footer { display: flex; align-items: center; justify-content: space-between; gap: 12px; }
footer span { font-size: 12px; color: var(--ink-2); min-width: 0; }
.error { margin-top: 12px; color: var(--danger); }
details { margin-top: 18px; color: var(--ink-2); font-size: 12px; }
details p { font-size: 12px; margin-top: 8px; }
summary { display: flex; align-items: center; gap: 6px; cursor: pointer; list-style: none; }
summary::marker { content: ""; }
summary::-webkit-details-marker { display: none; }
.disclosure-chev { flex-shrink: 0; }
details[open] .disclosure-chev { transform: rotate(90deg); }
.sr-only { position: absolute; width: 1px; height: 1px; overflow: hidden; clip-path: inset(50%); }
</style>
