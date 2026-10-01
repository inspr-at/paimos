<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { can, onAccessChange } from '../../lib/authz'
import { useIdentityScope } from '../../lib/useIdentityScope'
import { addPhonePasskey, decodeBytes, minuteTime, phoneError, phoneRequest, phoneSettings, timeMinute, type PhoneSettings } from '../../lib/phoneApprovals'

const settings = ref<PhoneSettings | null>(null)
const busy = ref(false), error = ref(''), message = ref(''), platform = ref(false)
const start = ref('22:00'), end = ref('07:00'), quiet = ref(false), zone = ref('UTC'), escalation = ref(15)
const scope = useIdentityScope(() => can('profile.read')), reads = scope.lane()
const pushSupported = typeof window !== 'undefined' && 'Notification' in window && 'PushManager' in window && 'serviceWorker' in navigator
function applySettings(value: PhoneSettings) {
    settings.value = value
    const prefs = value.preferences
    quiet.value = prefs.quiet_start !== prefs.quiet_end
    start.value = minuteTime(prefs.quiet_start); end.value = minuteTime(prefs.quiet_end)
    zone.value = prefs.time_zone; escalation.value = prefs.escalation_minutes
    if (!value.subscriptions.length) zone.value = Intl.DateTimeFormat().resolvedOptions().timeZone
}
function load() {
  error.value = ''
  return reads.run(({ after, signal }) => after(phoneSettings(signal), applySettings), { failed: e => { error.value = phoneError(e) } })
}
function act(work: (signal: AbortSignal) => Promise<unknown>, result: string) {
  if (busy.value || !can('profile.write')) return
  reads.cancel()
  busy.value = true; error.value = ''; message.value = ''
  return scope.run(({ after, signal }) => after(work(signal), () => after(phoneSettings(signal), value => {
    applySettings(value); message.value = result
  })), { failed: e => { error.value = phoneError(e) }, settled: () => { busy.value = false } })
}
function preferences(enabled = settings.value?.preferences.enabled ?? false) {
  return { enabled, time_zone: zone.value, quiet_start: quiet.value ? timeMinute(start.value) : 0,
    quiet_end: quiet.value ? timeMinute(end.value) : 0, escalation_minutes: Number(escalation.value) }
}
function save() { return act(signal => phoneRequest('/me/phone-approvals/settings', 'PUT', preferences(), signal), 'Notification settings saved.') }
function enable() {
  if (busy.value || !settings.value?.push_available || !settings.value.passkeys.length || !can('profile.write') || !pushSupported) return
  // The permission request starts synchronously inside the person's tap.
  const permission = Notification.requestPermission()
  const publicKey = settings.value.vapid_public_key
  return act(async signal => {
    if (await permission !== 'granted') throw new Error('Notifications are blocked. Allow them in your browser or phone settings, then try again.')
    signal.throwIfAborted()
    const registration = await navigator.serviceWorker.register('/phone-approvals-sw.js', { scope: '/' })
    await navigator.serviceWorker.ready
    signal.throwIfAborted()
    let subscription = await registration.pushManager.getSubscription()
    signal.throwIfAborted()
    if (!subscription) subscription = await registration.pushManager.subscribe({ userVisibleOnly: true, applicationServerKey: decodeBytes(publicKey) })
    // expirationTime is browser metadata, not part of the subscription contract.
    const serialized = subscription.toJSON()
    await phoneRequest('/me/phone-approvals/subscriptions', 'POST', { endpoint: serialized.endpoint, keys: serialized.keys }, signal)
    await phoneRequest('/me/phone-approvals/settings', 'PUT', preferences(true), signal)
  }, 'Phone notifications enabled.')
}
const revoke = (group: 'passkeys' | 'subscriptions', id: string) => act(signal => phoneRequest(`/me/phone-approvals/${group}/${encodeURIComponent(id)}`, 'DELETE', undefined, signal), 'Access revoked.')
const stopAccess = onAccessChange(() => {
  scope.reset(); settings.value = null; message.value = ''; busy.value = false
  if (can('profile.read')) void load()
})
watch(scope.owner, () => { settings.value = null; scope.reset(); busy.value = false; void load() }, { flush: 'sync' })
onMounted(() => {
  void load()
  if ('PublicKeyCredential' in window) void PublicKeyCredential.isUserVerifyingPlatformAuthenticatorAvailable().then(value => { platform.value = value }).catch(() => {})
})
onBeforeUnmount(stopAccess)
</script>

<template>
  <div class="phone-settings">
    <p>Get a notification when an agent needs your decision. Review the request, then verify with Face ID, Touch ID or your device passkey.</p>
    <p v-if="error" role="alert" class="error">{{ error }} <button v-if="!settings" class="btn sm" @click="load">Try again</button></p>
    <p v-if="message" role="status">{{ message }}</p>
    <p v-if="!settings && !error" role="status">Loading phone approvals…</p>
    <template v-if="settings">
      <p v-if="!settings.available">Phone approval verification is unavailable. Ask your workspace administrator to configure the public HTTPS address.</p>
      <p v-else-if="!platform">This device does not offer a platform passkey. Open these settings on a phone with Face ID, Touch ID or a device authenticator.</p>
      <div class="controls">
        <button class="btn" :disabled="busy || !settings.available || !platform || !can('profile.write')" @click="act(addPhonePasskey, 'Passkey added.')">Add device passkey</button>
        <button class="btn primary" :disabled="busy || !pushSupported || !settings.push_available || !settings.passkeys.length || !can('profile.write')" @click="enable">Enable notifications on this device</button>
        <button v-if="settings.preferences.enabled" class="btn" :disabled="busy || !can('profile.write')" @click="act(signal => phoneRequest('/me/phone-approvals/settings', 'PUT', preferences(false), signal), 'Phone notifications paused.')">Pause notifications</button>
      </div>
      <p v-if="!settings.push_available">Push delivery is not configured for this workspace.</p>
      <p v-else-if="!pushSupported">On iPhone or iPad, add this site to your Home Screen, open it there and enable notifications. Your browser must support Web Push.</p>
      <p v-else>On iPhone or iPad, enable notifications from the app on your Home Screen. Your device receives a generic review link; request details stay inside the signed-in app.</p>
      <form @submit.prevent="save">
        <label class="quiet-toggle"><input v-model="quiet" type="checkbox" :disabled="busy" /> Quiet hours</label>
        <div v-if="quiet" class="hours">
          <label>From<input v-model="start" class="field" type="time" required :disabled="busy" /></label>
          <label>Until<input v-model="end" class="field" type="time" required :disabled="busy" /></label>
        </div>
        <label>Time zone<input v-model="zone" class="field" required maxlength="100" :disabled="busy" /></label>
        <label>Escalate to the next authorized person after (minutes)<input v-model="escalation" class="field" type="number" min="5" max="1440" required :disabled="busy" /></label>
        <button class="btn" type="submit" :disabled="busy || !settings.available || !can('profile.write')">Save notification settings</button>
      </form>
      <ul v-if="settings.passkeys.length" aria-label="Registered passkeys">
        <li v-for="(key, i) in settings.passkeys" :key="key.id"><span>Passkey {{ i + 1 }} · added {{ new Date(key.created_at).toLocaleDateString() }}</span><button class="btn sm" :disabled="busy || !can('profile.write')" @click="revoke('passkeys', key.id)">Revoke passkey {{ i + 1 }}</button></li>
      </ul>
      <ul v-if="settings.subscriptions.length" aria-label="Notification devices">
        <li v-for="(sub, i) in settings.subscriptions" :key="sub.id"><span>Device {{ i + 1 }} · added {{ new Date(sub.created_at).toLocaleDateString() }}</span><button class="btn sm" :disabled="busy || !can('profile.write')" @click="revoke('subscriptions', sub.id)">Revoke device {{ i + 1 }}</button></li>
      </ul>
    </template>
  </div>
</template>

<style scoped>
.phone-settings, form { display: grid; gap: 14px; font-size: 13px; color: var(--ink-2); }
p { margin: 0; line-height: 1.6; }
.controls, .hours { display: flex; flex-wrap: wrap; gap: 10px; }
label { display: grid; gap: 6px; }
.quiet-toggle { display: flex; align-items: center; gap: 8px; }
input { width: 100%; min-width: 0; }
input[type=checkbox] { width: auto; }
form { max-width: 460px; }
ul { list-style: none; padding: 0; margin: 0; display: grid; gap: 8px; }
li { display: flex; flex-wrap: wrap; align-items: center; justify-content: space-between; gap: 8px; }
.error { color: var(--danger); }
button { min-height: 44px; }
@media (max-width: 600px) { .controls { display: grid; } }
</style>
