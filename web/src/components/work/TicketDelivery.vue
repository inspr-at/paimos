<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import AppIcon from '../AppIcon.vue'
import DeliveryRow from './DeliveryRow.vue'
import { can, onAccessChange } from '../../lib/authz'
import { createScope, scopeOwner } from '../../lib/identityScope'
import { deliveryLanguage, liftDeliveryHold, minutesSince, readDelivery, sortDelivery, type DeliveryItem, type DeliveryPage } from '../../lib/delivery'
import { useSession } from '../../stores/session'
import { useProfile } from '../../stores/profile'

const props = defineProps<{ nodeId: string; projectId: string; ticketKey?: string }>()
const session = useSession(), profile = useProfile()
const lang = computed(() => deliveryLanguage(profile.profile?.locale))
const de = computed(() => lang.value === 'de')
const read = computed(() => can('delivery.read', props.projectId)), manage = computed(() => can('delivery.manage', props.projectId))
const owner = () => scopeOwner(session.identity) ? `${scopeOwner(session.identity)}/${props.projectId}/${props.nodeId}` : ''
const scope = createScope(owner), reader = scope.lane()
const result = ref<DeliveryPage | null>(null), loading = ref(true), error = ref(''), mutationError = ref(''), busy = ref(''), now = ref(Date.now())
const order = ref<string[]>([]), lifted = ref<string[]>([]), liftedAt = ref<Record<string, string>>({})
const items = computed(() => {
  const list = sortDelivery(result.value?.items ?? [], now.value)
  return list.sort((a, b) => order.value.indexOf(a.id) - order.value.indexOf(b.id))
})
const showSlot = computed(() => items.value.length === 0)
const updated = computed(() => items.value.reduce((latest, item) => item.updated_at > latest ? item.updated_at : latest, ''))
function reset() { scope.reset(); result.value = null; error.value = ''; mutationError.value = ''; loading.value = true; busy.value = ''; order.value = []; lifted.value = []; liftedAt.value = {} }
function load() {
  if (!read.value || busy.value) return Promise.resolve()
  const id = props.nodeId
  loading.value = true
  return reader.run(({ after, signal }) => after(readDelivery(id, signal), value => {
    result.value = value; error.value = ''
    const sorted = sortDelivery(value.items, now.value)
// Keep existing rows in place while refreshing or lifting a hold. New rows append; reopening applies the priority order.
    if (!order.value.length) order.value = sorted.map(item => item.id)
    else order.value = [...order.value, ...sorted.map(item => item.id).filter(id => !order.value.includes(id))]
    const nextAt = { ...liftedAt.value }
    lifted.value = lifted.value.filter(rowId => {
      const row = value.items.find(item => item.id === rowId)
      const at = nextAt[rowId]
      if (row?.state === 'held' && at && Date.parse(row.updated_at) > Date.parse(at)) { delete nextAt[rowId]; return false }
      return true
    })
    liftedAt.value = nextAt
  }), { failed: cause => { error.value = cause instanceof Error ? cause.message : 'Delivery unavailable' }, settled: () => { loading.value = false } })
}
function lift(item: DeliveryItem) {
  if (!manage.value || busy.value || item.state !== 'held') return
  const snapshot = { ...item }, id = item.id
  reader.cancel(); busy.value = id; mutationError.value = ''
  return scope.run(({ after, signal }) => after(liftDeliveryHold(snapshot, signal), value => {
    if (!manage.value || !result.value || value.id !== id || value.ticket_node_id !== props.nodeId) return
    result.value.items = result.value.items.map(item => item.id === id ? value : item)
    if (!lifted.value.includes(id)) lifted.value = [...lifted.value, id]
    liftedAt.value = { ...liftedAt.value, [id]: value.updated_at }
  }), { failed: cause => { mutationError.value = cause instanceof Error ? cause.message : 'The hold could not be lifted.' }, settled: () => { busy.value = '' } })
}
watch([() => props.nodeId, () => props.projectId, () => scopeOwner(session.identity), read], () => { reset(); void load() }, { immediate:true, flush:'sync' })
const stopAccess = onAccessChange(change => { if (change === 'reset') reset(); else void load() })
const poll = setInterval(() => { now.value = Date.now(); void load() }, 30_000)
onBeforeUnmount(() => { scope.dispose(); clearInterval(poll); stopAccess() })
</script>
<template>
  <section v-if="read" class="delivery" :lang="lang" :aria-label="de ? 'Lieferstatus' : 'Delivery'">
    <header class="head"><h3 class="eyebrow">{{ de ? 'Lieferstatus' : 'Delivery' }}</h3><div class="head-aside"><span class="updated">{{ updated ? de ? `Vor ${minutesSince(updated, now)} Min. aktualisiert` : `Updated ${minutesSince(updated, now)} min ago` : '' }}</span><button class="icon-btn flat sm" type="button" :aria-label="de ? 'Lieferstatus neu laden' : 'Reload delivery status'" :disabled="loading || !!busy" @click="load"><AppIcon name="refresh" :size="13" /></button></div></header>
    <div v-if="showSlot" class="slot" :aria-busy="loading">
      <span class="slot-icon"><AppIcon :name="error ? 'alert' : !result ? 'clock' : !result.available ? 'link' : 'merge'" :size="18" /></span>
      <div>
        <p class="slot-title">{{ error ? de ? 'Lieferstatus konnte nicht geladen werden' : 'Delivery status could not be loaded' : !result ? de ? 'Lieferstatus wird geladen…' : 'Loading delivery status…' : !result.available ? de ? 'Lieferstatus nicht verfügbar' : 'Delivery status is not available' : de ? 'Noch kein Pull Request' : 'No pull request yet' }}</p>
        <p class="slot-text">{{ error ? de ? 'Der Rest des Tickets ist aktuell.' : 'The rest of the ticket is up to date.' : result && !result.available ? de ? 'Mit diesem Arbeitsbereich ist keine GitHub-App verbunden, daher erhält PAIMOS keine Pull-Request-Ereignisse. Sie wird auf dem Server eingerichtet, nicht in den Einstellungen.' : 'No GitHub App is connected to this workspace, so PAIMOS receives no pull request events. It is set up on the server, not in Settings.' : result ? de ? `Der Lieferstatus erscheint hier, sobald ein Pull Request für ${ticketKey || 'dieses Ticket'} offen ist.` : `Delivery status shows here once a pull request for ${ticketKey || 'this ticket'} is open.` : '' }}</p>
      </div>
    </div>
    <ol v-else class="list"><DeliveryRow v-for="item in items" :key="item.id" :item="item" :can-manage="manage" :now="now" :lang="lang" :busy="busy === item.id" :lifted="lifted.includes(item.id)" @lift="lift" /></ol>
    <p v-if="error && !showSlot" class="problem" role="status">{{ de ? 'Lieferstatus konnte nicht geladen werden. Der Rest des Tickets ist aktuell.' : 'Delivery status could not be loaded. The rest of the ticket is up to date.' }}</p>
    <p v-if="mutationError" class="problem" role="alert">{{ mutationError }}</p>
    <p v-if="result?.next_cursor" class="problem" role="status">{{ de ? 'Es werden die ersten 100 Pull Requests angezeigt. Weitere Lieferdaten sind vorhanden.' : 'Showing the first 100 pull requests. More delivery records are available.' }}</p>
    <p class="foot"><AppIcon name="eye" :size="13" /><span>{{ de ? 'PAIMOS meldet nur, was GitHub und die eigenen Läufe zeigen; es pusht, reiht ein und mergt nie selbst.' : 'PAIMOS only reports what GitHub and its own runs show; it never pushes, queues or merges.' }}</span></p>
  </section>
</template>
<style scoped>
.delivery { min-width:0; container:delivery/inline-size; }.head { display:flex; align-items:center; justify-content:space-between; gap:12px; min-height:28px; margin-bottom:8px; }.eyebrow { margin:0; }.head-aside { display:flex; align-items:center; gap:8px; }.updated { font-size:12px; color:var(--ink-3); font-variant-numeric:tabular-nums; text-align:right; }
.list { display:grid; gap:8px; margin:0; padding:0; list-style:none; }
.slot { display:grid; grid-template-columns:auto minmax(0,1fr); align-items:center; gap:12px; min-height:118px; padding:12px 14px; border-radius:10px; box-shadow:inset 0 0 0 1px var(--line); box-sizing:border-box; }.slot-icon { display:grid; place-items:center; width:32px; height:32px; border-radius:50%; background:var(--chip-teal-bg); box-shadow:inset 0 0 0 1px var(--chip-teal-line); color:var(--teal-ink); }.slot-title { margin:0; font-size:13.5px; font-weight:600; color:var(--ink); }.slot-text { margin:2px 0 0; font-size:12.5px; line-height:1.45; color:var(--ink-2); overflow-wrap:anywhere; }
.foot { display:grid; grid-template-columns:auto minmax(0,1fr); gap:7px; margin:8px 0 0; font-size:12px; line-height:1.45; color:var(--ink-3); }.foot svg { margin-top:2px; }.problem { font-size:12.5px; color:var(--danger); overflow-wrap:anywhere; }
@container delivery (max-width:479px) { .slot { min-height:150px; } }
@media (pointer:coarse) { .head .icon-btn { min-width:44px; min-height:44px; } }
</style>
