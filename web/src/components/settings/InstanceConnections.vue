<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import { useDeskConnections } from '../../stores/deskConnections'
import { useSession } from '../../stores/session'
import { issuedConnections, revokeIssuedConnection, type IssuedConnection } from '../../lib/instanceConnect'
import { displayLanguage } from '../../lib/displayLanguage'
import { toast } from '../../lib/toast'
import SettingsCard from './SettingsCard.vue'
import AppIcon from '../AppIcon.vue'

const store = useDeskConnections(), session = useSession()
const text = (en: string, de: string) => displayLanguage() === 'de' ? de : en
const issued = ref<IssuedConnection[]>([]), failed = ref(false), busy = ref('')
const owner = computed(() => session.identity ? `${session.identity.tenant.id}:${session.identity.principal.id}` : '')
let generation = 0, abort = new AbortController()
const date = (value: string) => new Date(value).toLocaleString([], { dateStyle: 'medium', timeStyle: 'short' })
async function load() {
  const who = owner.value, turn = generation
  try { const rows = await issuedConnections(abort.signal); if (turn === generation && who === owner.value) { issued.value = rows; failed.value = false } }
  catch { if (turn === generation && who === owner.value) failed.value = true }
}
watch(owner, () => { generation++; abort.abort(); abort = new AbortController(); issued.value = []; busy.value = ''; failed.value = false; if (owner.value) void load() }, { immediate: true, flush: 'sync' })
function disconnect(tag: string) {
  const undo = store.disconnect(tag)
  if (undo) toast(text('Disconnected in this browser.', 'In diesem Browser getrennt.'), { action: { label: text('Undo', 'Rückgängig'), run: undo } })
}
async function revoke(id: string) {
  const who = owner.value, turn = generation
  if (!who || busy.value || !session.authenticationCurrent()) return
  busy.value = id
  try {
    await revokeIssuedConnection(id, abort.signal)
    if (who === owner.value && turn === generation) { issued.value = issued.value.filter(row => row.id !== id); failed.value = false }
  } catch { if (who === owner.value && turn === generation) failed.value = true }
  finally { if (who === owner.value && turn === generation) busy.value = '' }
}
onBeforeUnmount(() => { generation++; abort.abort() })
</script>

<template>
  <SettingsCard :title="text('Other instances', 'Andere Instanzen')" icon="link" anchor="other-instances">
    <template #lead>{{ text('Read your waiting decisions here. Each decision opens on its home instance.', 'Wartende Entscheidungen werden hier angezeigt. Jede Entscheidung öffnet sich auf ihrer eigenen Instanz.') }}</template>
    <div v-for="row in store.reads" :key="row.tag" class="connection-row">
      <span><b>{{ row.tag }}</b> · {{ new URL(row.origin).host }}<small v-if="row.account">{{ text('connected as', 'verbunden als') }} {{ row.account }} · {{ text('since', 'seit') }} {{ date(row.connectedAt) }}<template v-if="row.summary"> · {{ text('last read', 'zuletzt gelesen') }} {{ date(row.summary.asOf) }}</template></small><small>{{ row.state }}</small></span>
      <button v-if="row.state === 'connecting'" type="button" class="btn sm" @click="store.cancel(row.tag)">{{ text('Cancel', 'Abbrechen') }}</button>
      <button v-else-if="row.state === 'not connected' || row.state === 'ended'" type="button" class="btn sm" @click="store.connect(row.tag)">{{ row.state === 'ended' ? text('Reconnect', 'Neu verbinden') : text('Connect', 'Verbinden') }}</button>
      <button v-else type="button" class="btn sm" @click="disconnect(row.tag)">{{ text('Disconnect', 'Trennen') }}</button>
    </div>
    <p v-if="!store.reads.length">{{ text('Not connected to another instance.', 'Keine Verbindung zu einer anderen Instanz.') }}</p>
    <button v-if="store.discovery === 'unknown'" type="button" class="btn sm" @click="store.refresh(true)">{{ text('Try again', 'Erneut versuchen') }}</button>
  </SettingsCard>
  <SettingsCard :title="text('Connected instances (read your waiting items)', 'Verbundene Instanzen (Wartendes lesen)')" icon="shield" anchor="connected-instances">
    <template #lead>{{ text('Disconnect here to end access from that browser.', 'Durch Trennen endet der Zugriff aus dem verbundenen Browser.') }}</template>
    <div v-for="row in issued" :key="row.id" class="connection-row">
      <span><b>{{ row.instance }}</b> · {{ new URL(row.origin).host }}<small>{{ text('this browser · connected', 'dieser Browser · verbunden') }} {{ date(row.created_at) }}<template v-if="row.last_used_at"> · {{ text('last used', 'zuletzt verwendet') }} {{ date(row.last_used_at) }}</template></small></span>
      <button type="button" class="btn sm" :disabled="!!busy" @click="revoke(row.id)">{{ text('Disconnect', 'Trennen') }}</button>
    </div>
    <p v-if="!issued.length && !failed">{{ text('No active connections.', 'Keine aktiven Verbindungen.') }}</p>
    <p v-if="failed" role="alert"><AppIcon name="alert" :size="14" />{{ text('Connections could not be updated.', 'Verbindungen konnten nicht aktualisiert werden.') }} <button type="button" class="btn sm" @click="load">{{ text('Try again', 'Erneut versuchen') }}</button></p>
  </SettingsCard>
</template>

<style scoped>
.connection-row { display: flex; align-items: center; justify-content: space-between; gap: 16px; padding: 10px 0; border-top: 1px solid var(--line); font-size: 13px; }
.connection-row > span { min-width: 0; overflow-wrap: anywhere; }
small { display: block; margin-top: 3px; color: var(--ink-3); font-size: 12px; }
button { flex: none; min-height: 44px; }
p { font-size: 13px; color: var(--ink-2); }
</style>
