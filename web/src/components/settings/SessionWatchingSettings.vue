<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { onBeforeUnmount, onMounted, ref } from 'vue'
import { brand } from '../../lib/brand'
import { api } from '../../lib/api'
import { can, onAccessChange } from '../../lib/authz'
import AppIcon from '../AppIcon.vue'
import type { WatchConsentMode } from '../../lib/attachWatch'

const saved = ref<WatchConsentMode | null>(null)
const choice = ref<WatchConsentMode>('aeon')
const busy = ref(false)
const error = ref('')
const notice = ref('')
let operation: AbortController | undefined
async function request(save = false) {
  if (save && !can('profile.write')) return
  operation?.abort()
  const controller = new AbortController(); operation = controller
  busy.value = true; error.value = ''; notice.value = ''
  try {
    const response = await api('/me/security/session-watching', {
      method: save ? 'PUT' : 'GET', signal: controller.signal,
      ...(save ? { headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ consent_mode: choice.value }) } : {}),
    })
    if (!response.ok) throw new Error('request failed')
    const result = await response.json() as { consent_mode: WatchConsentMode }
    if (result.consent_mode !== 'aeon' && result.consent_mode !== 'local_auth') throw new Error('invalid mode')
    if (controller.signal.aborted || operation !== controller) return
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
        <label :class="{ selected: choice === 'local_auth' }">
          <input v-model="choice" type="radio" name="watch-consent" value="local_auth" />
          <span><strong>Also confirm on the Mac</strong><span>After approval here, confirm with Touch ID or your Mac password.</span><span class="availability">Unavailable on Linux; if local confirmation cannot run, the watch stays off.</span></span>
        </label>
      </fieldset>
      <p class="footnote">Applies to your paired computers; active watches keep their current approval.</p>
      <footer><span role="status">{{ notice }}</span><button class="btn primary" type="submit" :disabled="busy || choice === saved || !can('profile.write')">{{ busy ? 'Saving…' : 'Save setting' }}</button></footer>
    </form>
    <p v-if="error" class="error" role="alert">{{ error }} <button v-if="!saved" class="btn sm" type="button" @click="request()">Try again</button></p>
    <details><summary><AppIcon name="chevron-right" class="disclosure-chev" :size="12" />About local confirmation</summary><p>The terminal WATCH prompt is a best-effort check that same-user processes can imitate.</p><p>Mac confirmation needs a signed daemon and an interactive login; an unavailable or cancelled prompt never activates a watch.</p></details>
  </div>
</template>

<style scoped>
h3 { font-size: 14px; font-weight: 600; margin: 0; }
p { font-size: 13px; line-height: 1.5; }
.intro { color: var(--ink-2); margin: 4px 0 16px; }
fieldset { display: grid; gap: 8px; padding: 0; margin: 0; border: 0; min-width: 0; }
label { display: flex; align-items: flex-start; gap: 12px; padding: 14px; border: 1px solid var(--line); border-radius: 10px; cursor: pointer; }
label.selected { background: var(--surface-2); border-color: var(--line-2); }
label:focus-within { box-shadow: var(--focus-ring); }
input { flex-shrink: 0; margin: 3px 0 0; accent-color: var(--teal-ink); }
label > span { display: grid; gap: 3px; min-width: 0; }
strong { font-size: 13px; font-weight: 600; }
strong small { color: var(--ink-2); font-size: 11px; font-weight: 400; margin-left: 8px; }
label span span { font-size: 13px; line-height: 1.5; color: var(--ink-2); }
label span .availability { font-size: 12px; margin-top: 4px; }
.footnote { color: var(--ink-2); font-size: 12px; margin: 12px 0; }
footer { display: flex; align-items: center; justify-content: space-between; gap: 12px; }
footer span { font-size: 12px; color: var(--ink-2); }
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
