<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { can, onAccessRevalidate } from '../../lib/authz'
import { displayLanguage } from '../../lib/displayLanguage'
import { deskPushBrowser, deviceSubscriptionHash, phoneRequest, phoneSettings, setDeskSubscription, type PhoneSettings } from '../../lib/phoneApprovals'
import { useIdentityScope } from '../../lib/useIdentityScope'
import { useSession } from '../../stores/session'

const session = useSession()
const de = computed(() => displayLanguage() === 'de')
const text = (en: string, german: string) => de.value ? german : en
const settings = ref<PhoneSettings | null>(null), hash = ref(''), busy = ref(false), failed = ref(false)
const browser = ref(deskPushBrowser())
const person = () => session.identity?.principal.kind === 'person'
const scope = useIdentityScope(() => person() && can('profile.read')), reads = scope.lane()
const writes = useIdentityScope(() => person() && can('profile.read') && can('profile.write'))
const enabled = computed(() => !!hash.value && !!settings.value?.subscriptions.some(sub => sub.endpoint_hash === hash.value && sub.desk_enabled))
const devices = computed(() => [...(settings.value?.subscriptions.filter(sub => sub.desk_enabled) ?? [])].sort((a, b) => Number(b.endpoint_hash === hash.value) - Number(a.endpoint_hash === hash.value)))
// Prose names the Share menu's item; no glyph stands in for the step (UG1).
const words = computed(() => ({
  loading: text('Loading notifications…', 'Benachrichtigungen werden geladen…'),
  loadFailed: text('Notifications could not be loaded. Please try again.', 'Benachrichtigungen konnten nicht geladen werden. Bitte erneut versuchen.'),
  unavailable: text("Browser notifications aren't available on this instance.", 'Browser-Benachrichtigungen sind auf dieser Instanz nicht verfügbar.'),
  homeScreen: text('On iPhone, add PAIMOS to the Home Screen first (in the Share menu: Add to Home Screen), then turn this on there.', 'Auf dem iPhone PAIMOS zuerst zum Home-Bildschirm hinzufügen (im Teilen-Menü: Zum Home-Bildschirm), dann dort einschalten.'),
  blocked: text("Notifications are blocked for this site. Allow them in the browser's site settings.", 'Benachrichtigungen sind für diese Seite blockiert. In den Website-Einstellungen des Browsers erlauben.'),
  unsupported: text('This browser does not support push notifications.', 'Dieser Browser unterstützt keine Push-Benachrichtigungen.'),
  updateFailed: text('Notifications could not be updated. Please try again.', 'Benachrichtigungen konnten nicht aktualisiert werden. Bitte erneut versuchen.'),
  on: text('Notifications on for this browser', 'Benachrichtigungen für diesen Browser an'),
  off: text('Off for this browser', 'Für diesen Browser aus'),
}))
const state = computed(() => {
  const w = words.value
  if (!settings.value) return failed.value ? w.loadFailed : w.loading
  if (!settings.value.push_available) return w.unavailable
  if (browser.value.homeScreenRequired) return w.homeScreen
  if (browser.value.permission === 'denied') return w.blocked
  if (!browser.value.supported) return w.unsupported
  if (failed.value) return w.updateFailed
  return enabled.value ? w.on : w.off
})
// The slot holds the longest line this browser can still reach from its current kind of state, so switching
// between off, on, a failed update and a refused prompt moves nothing below it (AEON-541).
const reachable = computed(() => {
  const w = words.value
  if (!settings.value) return [w.loading, w.loadFailed]
  if (!settings.value.push_available) return [w.unavailable]
  if (browser.value.homeScreenRequired) return [w.homeScreen]
  if (!browser.value.supported) return [w.unsupported]
  return [w.off, w.on, w.updateFailed, w.blocked]
})
const added = (value: string) => new Date(value).toLocaleDateString(de.value ? 'de' : 'en-GB', { day: 'numeric', month: 'short', year: 'numeric' })
function load() {
  return reads.run(({ after, signal }) => after(Promise.all([phoneSettings(signal), deviceSubscriptionHash()]), ([value, device]) => {
    settings.value = value; hash.value = device; browser.value = deskPushBrowser(); failed.value = false
  }), { failed: () => { failed.value = true } })
}
function act(work: (signal: AbortSignal) => Promise<unknown>) {
  reads.cancel(); busy.value = true; failed.value = false
  return writes.run(({ after, signal }) => after(work(signal), () => after(Promise.all([phoneSettings(signal), deviceSubscriptionHash()]), ([value, device]) => {
    settings.value = value; hash.value = device
    window.dispatchEvent(new Event('aeon:desk-notifications'))
  })), { failed: () => { failed.value = true }, settled: () => { busy.value = false; browser.value = deskPushBrowser() } })
}
function toggle() {
  if (!writes.owner.value || !session.authenticationCurrent() || busy.value || !settings.value?.push_available || !can('profile.write') || !browser.value.supported || browser.value.homeScreenRequired || browser.value.permission === 'denied') return
  const on = !enabled.value
  // This is the only permission request; it runs directly inside the click.
  const permission = on ? Notification.requestPermission() : undefined
  void act(signal => setDeskSubscription(on, settings.value!.vapid_public_key, permission, signal))
}
function remove(id: string) {
  if (!writes.owner.value || !session.authenticationCurrent() || busy.value || !can('profile.write')) return
  void act(signal => phoneRequest(`/me/phone-approvals/subscriptions/${encodeURIComponent(id)}`, 'DELETE', undefined, signal))
}
watch(scope.owner, () => { settings.value = null; hash.value = ''; failed.value = false; busy.value = false; void load() }, { flush: 'sync' })
watch(writes.owner, () => { busy.value = false }, { flush: 'sync' })
const stopAccess = onAccessRevalidate({ reset: () => { settings.value = null; hash.value = ''; busy.value = false; failed.value = false }, refresh: () => { void load() } })
onMounted(() => { void load() })
onBeforeUnmount(stopAccess)
</script>

<template>
  <div class="desk-notifications">
    <div v-if="!browser.homeScreenRequired" class="switch-row">
      <button type="button" class="notification-switch" role="switch" :aria-checked="enabled" :disabled="busy || !settings?.push_available || !browser.supported || browser.permission === 'denied' || !can('profile.write')" @click="toggle">
        <span class="switch-track" aria-hidden="true"><span /></span>
        <span>{{ text('Notify me in this browser when a decision waits, including connected instances', 'In diesem Browser über wartende Entscheidungen benachrichtigen, auch auf verbundenen Instanzen') }}</span>
      </button>
    </div>
    <!-- All state copy shares a slot sized by the longest line this browser can reach. -->
    <div class="state-slot" aria-live="polite">
      <p v-for="line in reachable" :key="line" class="state-size" aria-hidden="true">{{ line }}</p>
      <p :role="failed ? 'alert' : 'status'">{{ state }}</p>
    </div>
    <button v-if="failed && !settings" type="button" class="btn sm" @click="load">{{ text('Try again', 'Erneut versuchen') }}</button>
    <ul v-if="devices.length" :aria-label="text('Desk notification devices', 'Geräte für Desk-Benachrichtigungen')">
      <li v-for="(device, i) in devices" :key="device.id">
        <span>{{ device.endpoint_hash === hash ? text('This browser', 'Dieser Browser') : `${text('Device', 'Gerät')} ${i + 1}` }} · {{ text('added', 'hinzugefügt am') }} {{ added(device.created_at) }}</span>
        <button type="button" class="btn sm" :disabled="busy || !can('profile.write')" :title="text('Removes all notifications for this device, phone approvals included', 'Entfernt alle Benachrichtigungen für dieses Gerät, auch Freigaben am Telefon')" @click="remove(device.id)">{{ text('Remove', 'Entfernen') }}</button>
      </li>
    </ul>
  </div>
</template>

<style scoped>
.desk-notifications { display: grid; gap: 14px; font-size: 13px; color: var(--ink-2); }
.notification-switch { display: flex; align-items: center; gap: 12px; min-height: 44px; padding: 0; border: 0; background: transparent; color: inherit; font: inherit; text-align: left; cursor: pointer; }
.notification-switch:disabled { cursor: default; opacity: .6; }
.notification-switch:focus-visible { outline: 2px solid var(--teal); outline-offset: 4px; }
.switch-track { display: flex; align-items: center; flex: 0 0 34px; height: 20px; padding: 3px; box-sizing: border-box; border-radius: 12px; background: var(--surface-2); box-shadow: inset 0 0 0 1px var(--line); }
.switch-track > span { width: 14px; height: 14px; border-radius: 50%; background: var(--ink-3); }
[aria-checked=true] .switch-track { background: var(--chip-teal-bg); }
[aria-checked=true] .switch-track > span { margin-left: auto; background: var(--teal-ink); }
p { margin: 0; line-height: 1.6; overflow-wrap: anywhere; }
.state-slot { display: grid; color: var(--ink-3); }
.state-slot > p { grid-area: 1 / 1; }
.state-size { visibility: hidden; pointer-events: none; }
ul { list-style: none; padding: 0; margin: 0; display: grid; gap: 8px; }
li { display: flex; flex-wrap: wrap; align-items: center; justify-content: space-between; gap: 8px; }
li > span { min-width: 0; overflow-wrap: anywhere; }
button.btn { min-height: 44px; }
</style>
