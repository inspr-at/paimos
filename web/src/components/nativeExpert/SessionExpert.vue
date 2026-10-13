<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, shallowRef, watch } from 'vue'
import AppIcon from '../AppIcon.vue'
import FloatingPanel from '../work/FloatingPanel.vue'
import NativeEventList from './NativeEventList.vue'
import { nativeSessionBounds as bounds, type NativeApproval, type NativeSessionView, type NativeStreamEvent } from '../../lib/nativeSessionContract'
import { boundedEvent, boundedPage, ExpertBuffer } from '../../lib/nativeExpert/buffer'
import { bindingKey, expertRequest, liveTerminalLink, type ExpertConsent, type ExpertViewer, type NativeExpertTransport } from '../../lib/nativeExpert/ports'
import { eventRows, eventTypes, type EventType } from '../../lib/nativeExpert/rows'
import { fullName, type RegistryProfile } from '../../lib/modelsSimple'
import { preferenceOwner, usePreference } from '../../lib/preferences'

const props = withDefaults(defineProps<{
  session: NativeSessionView; viewer: ExpertViewer; consent: ExpertConsent | null
  transport: NativeExpertTransport; active: boolean; name: string; host: string; modelProfile?: RegistryProfile | null
}>(), { modelProfile: null })
const emit = defineEmits<{ review: [approval: NativeApproval] }>()
const modelName = computed(() => props.modelProfile ? fullName(props.modelProfile) : '')
const request = computed(() => expertRequest(props.session, props.viewer, props.consent))
const link = computed(() => liveTerminalLink(props.session, props.viewer))
const readable = computed(() => props.viewer.canRead && (!props.session.current_binding || props.viewer.tenantId === props.session.current_binding.tenant_id))
const principalKey = computed(() => `${props.viewer.tenantId}/${props.viewer.principalId}/${props.viewer.kind}`)
const panelKey = computed(() => `${principalKey.value}/${props.session.owned_session_id}/${props.session.owner_person_id}`)
const captureKey = computed(() => JSON.stringify([panelKey.value, bindingKey(props.session.current_binding), props.session.generation,
  props.viewer.canRead, props.viewer.canWatch, props.viewer.canReadStructured, request.value?.view, request.value?.consentId,
]))
const segment = ref<'events' | 'terminal'>('events'), terminalAtOpen = ref(false), viewerAtOpen = ref(false)
const openedName = ref(''), openedHost = ref('')
const hidden = ref(false), following = ref(true), hiddenTypes = ref<EventType[]>([]), typeAnchor = ref<HTMLElement | null>(null)
const preference = usePreference<{ hidden?: string[] }>('agents:expert.types')
const ownPreference = computed(() => props.viewer.kind === 'person' && preferenceOwner.value === `${props.viewer.tenantId}/${props.viewer.principalId}`)
watch(() => ownPreference.value ? preference.value.value : null, value => {
  hiddenTypes.value = value?.hidden?.filter((key): key is EventType => eventTypes.some(type => type.key === key)) ?? []
}, { immediate: true })
function toggleType(key: EventType) {
  hiddenTypes.value = hiddenTypes.value.includes(key) ? hiddenTypes.value.filter(value => value !== key) : [...hiddenTypes.value, key]
  if (ownPreference.value) preference.save({ hidden: [...hiddenTypes.value] })
}
function closeTypes(restoreFocus: boolean) {
  const anchor = typeAnchor.value
  typeAnchor.value = null
  if (restoreFocus) anchor?.focus({ preventScroll: true })
}

const buffer = new ExpertBuffer(), events = shallowRef<NativeStreamEvent[]>([]), evicted = ref(0)
const notice = ref(''), status = ref('Live events appear after Watch live. Only the bounded live buffer is available.')
const watching = ref(false), loading = ref(false), before = ref<string | null>(null), blocked = ref(false)
const rows = computed(() => eventRows(events.value).filter(row => !hiddenTypes.value.includes(row.type)))
const available = computed(() => !!request.value && props.active && !hidden.value && segment.value === 'events' && !blocked.value)
const inspectionPermit = computed(() => {
  const key = captureKey.value
  return () => key === captureKey.value && available.value
})
let controller: AbortController | null = null, epoch = 0, lastSeq: bigint | null = null
function publish() { events.value = [...buffer.events]; evicted.value = buffer.evicted }
function stop(clear = true) {
  epoch++; controller?.abort(); controller = null; watching.value = false; loading.value = false
  if (clear) { buffer.clear(); publish(); before.value = null; notice.value = ''; lastSeq = null }
}
function errorText(error: unknown) {
  // Never render adapter/native error bodies: they can contain paths or private payloads.
  const reason = error && typeof error === 'object' && 'reason' in error ? error.reason : null
  if (reason === 'cursor_expired') return 'The live cursor expired. Some events are unavailable. Watch live to reload the current snapshot.'
  if (reason === 'feature_disabled' || reason === 'qualification_missing') return 'Live events are not available for this session yet.'
  return 'Live events could not be read. The view is incomplete. Watch live to try again.'
}
function revoked(error: unknown) {
  if (!error || typeof error !== 'object') return false
  const reason = 'reason' in error ? error.reason : null, status = 'status' in error ? error.status : null
  return ['permission_revoked', 'grant_expired', 'wrong_principal'].includes(String(reason)) || [401, 403, 404].includes(Number(status))
}
function accessLost() { stop(); blocked.value = true; status.value = 'Viewing ended because access or the owner’s consent was revoked.' }
function receive(event: NativeStreamEvent, current: () => boolean) {
  if (!current()) return
  const bound = request.value
  if (!bound || bindingKey(event.binding) !== bindingKey(bound.binding)) return
  if (event.generation !== bound.generation) {
    stop(); blocked.value = true; status.value = 'The session generation changed. Reopen this view to choose the current session.'; return
  }
  let safe: NativeStreamEvent | null
  try { safe = boundedEvent(event, bound) } catch { safe = null }
  if (!safe) { notice.value = 'A frame was omitted because its view, shape or size was not permitted.'; return }
  if (safe.kind === 'end' && safe.payload.reason === 'revoked') {
    accessLost(); return
  }
  const seq = BigInt(safe.seq)
  if (lastSeq !== null && seq > lastSeq + 1n) notice.value = `${seq - lastSeq - 1n} frames are missing from this live view.`
  if (BigInt(safe.dropped_frames) > 0n) notice.value = `${safe.dropped_frames} frames were dropped by the live transport.`
  if (lastSeq === null || seq > lastSeq) lastSeq = seq
  buffer.add([safe]); publish()
  if (safe.kind === 'gap') {
    notice.value = `Live gap · ${safe.payload.reason} · ${safe.payload.dropped_frames} frames unavailable.`
    if (safe.payload.reload_snapshot) { stop(false); status.value = 'Watch live to reload the current snapshot. Earlier live frames may be unavailable.' }
  } else if (safe.kind === 'end') { stop(false); status.value = `Viewing ended · ${safe.payload.reason}.` }
  else if (safe.kind === 'stream.paused') status.value = 'Structured events pause while a person has control. Events resume when they hand back.'
  else status.value = 'Live · new events land here'
}
async function start() {
  if (!available.value) return
  stop(); following.value = true
  const bound = request.value!, frozen = Object.freeze({ ...bound, binding: Object.freeze({ ...bound.binding }) }), key = captureKey.value, turn = epoch
  const abort = controller = new AbortController()
  const current = () => turn === epoch && !abort.signal.aborted && key === captureKey.value && available.value
  watching.value = true; loading.value = true; status.value = 'Connecting to the bounded live buffer.'
  try {
    const page = await props.transport.events(frozen, null, bounds.event_page_default_items, abort.signal)
    if (!current()) return
    if (props.session.revision !== frozen.revision) { stop(); status.value = 'The session changed while events were loading. Watch live to read the current view.'; return }
    const accepted = boundedPage(page, frozen, null)
    if (accepted === null) { stop(); status.value = 'The replay page was rejected. Its generation, binding, ordering or bounds did not match this view.'; return }
    buffer.add(accepted); publish(); before.value = page.next_before
    if (page.truncated) notice.value = 'Earlier live frames are unavailable. This replay is incomplete.'
    lastSeq = accepted.length ? BigInt(accepted.at(-1)!.seq) : null
    loading.value = false; status.value = 'Live · new events land here'
    await props.transport.stream(Object.freeze({ ...frozen, lastEventId: lastSeq === null ? null : `${frozen.generation}:${lastSeq}` }), event => receive(event, current), abort.signal)
    if (current()) { stop(false); status.value = 'The live connection ended. Watch live to reconnect; events may be missing.' }
  } catch (error) {
    if (!current()) return
    if (revoked(error)) accessLost()
    else { stop(false); notice.value = errorText(error); status.value = 'Live connection unavailable.' }
  }
}
async function earlier() {
  const bound = request.value, abort = controller, cursor = before.value, turn = epoch, key = captureKey.value, revision = props.session.revision
  if (!bound || !abort || !cursor || !watching.value || loading.value) return
  loading.value = true
  const current = () => epoch === turn && captureKey.value === key && !abort.signal.aborted && available.value && props.session.revision === revision
  try {
    const page = await props.transport.events({ ...bound, lastEventId: lastSeq === null ? null : `${bound.generation}:${lastSeq}` }, cursor, bounds.event_page_default_items, abort.signal)
    if (!current()) return
    const accepted = boundedPage(page, bound, cursor)
    if (accepted === null) { notice.value = 'The earlier page was rejected. Its cursor, generation, binding, ordering or bounds did not match this view.'; return }
    following.value = false; buffer.add(accepted); publish(); before.value = page.next_before
    if (page.truncated) notice.value = 'Earlier live frames are unavailable. This replay is incomplete.'
  } catch (error) { if (current()) { if (revoked(error)) accessLost(); else notice.value = errorText(error) } }
  finally { if (epoch === turn) loading.value = false }
}

watch(panelKey, () => {
  stop(); segment.value = 'events'; following.value = true; typeAnchor.value = null; blocked.value = false
  hiddenTypes.value = ownPreference.value ? preference.value.value?.hidden?.filter((key): key is EventType => eventTypes.some(type => type.key === key)) ?? [] : []
  viewerAtOpen.value = !!link.value
  terminalAtOpen.value = props.viewer.kind === 'person' && readable.value &&
    (props.session.capabilities.take_supported || props.session.mode === 'terminal' || !!link.value)
  openedName.value = props.name; openedHost.value = props.host
}, { immediate: true, flush: 'sync' })
watch(captureKey, () => {
  stop(); blocked.value = false; typeAnchor.value = null
  status.value = !props.session.capabilities.events_supported ? 'Live events are not available for this session.' :
    request.value ? 'Viewing stopped. Watch live to view this session explicitly.' : 'Live diagnostics require current view permission and the owner’s active watch consent.'
}, { flush: 'sync' })
watch(() => props.active, active => { if (!active) { stop(); status.value = 'Viewing stopped while Expert is hidden.' } }, { flush: 'sync' })
watch(segment, () => { stop(); typeAnchor.value = null; status.value = 'Viewing stopped. Watch live to view Events again.' }, { flush: 'sync' })
function visibility() {
  hidden.value = document.hidden
  if (hidden.value) { stop(); status.value = 'Viewing stopped while this page is hidden.' }
}
onMounted(() => { visibility(); document.addEventListener('visibilitychange', visibility) })
onBeforeUnmount(() => { stop(); document.removeEventListener('visibilitychange', visibility) })
defineExpose({ stop, liveTerminalLink: link, modelName })
</script>

<template>
  <section class="native-expert" :aria-label="modelName ? `Expert events · ${modelName}` : 'Expert events'">
    <div class="expert-bar" role="toolbar" aria-label="Expert view tools">
      <div v-if="terminalAtOpen && viewer.kind === 'person'" class="seg expert-segments" role="radiogroup" aria-label="Expert view">
        <button type="button" role="radio" :disabled="!readable" :aria-checked="segment === 'events'" @click="segment = 'events'"><AppIcon name="list" :size="13" />Events</button>
        <button type="button" role="radio" :disabled="!readable" :aria-checked="segment === 'terminal'" @click="segment = 'terminal'"><AppIcon name="terminal" :size="13" />Terminal</button>
      </div>
      <template v-if="segment === 'events'">
        <div class="event-types" role="group" aria-label="Event types">
          <button v-for="type in eventTypes" :key="type.key" class="type-toggle" type="button" :aria-pressed="!hiddenTypes.includes(type.key)" @click="toggleType(type.key)">
            <AppIcon :name="hiddenTypes.includes(type.key) ? 'square' : 'square-check-big'" :size="14" />{{ type.label }}
          </button>
        </div>
        <button class="btn types-button" type="button" aria-label="Event types" aria-haspopup="dialog" :aria-expanded="!!typeAnchor" @click="typeAnchor = $event.currentTarget as HTMLElement">
          <AppIcon name="filter" :size="14" /><span>Types</span><span class="type-count">{{ 6 - hiddenTypes.length }} of 6</span>
        </button>
        <span class="grow"></span>
        <label class="switch follow"><input type="checkbox" role="switch" aria-label="Follow live" :checked="following" @change="following = ($event.target as HTMLInputElement).checked"><span class="follow-long">Follow live</span><span class="follow-short" aria-hidden="true">Follow</span></label>
      </template>
    </div>
    <NativeEventList v-if="segment === 'events'" :key="captureKey" :rows="rows" :retained="events.length" :evicted="evicted" :notice="notice" :status="status" :before="before" :loading="loading" :watching="watching" :can-start="available" :can-inspect="inspectionPermit" :follow="following" @start="start" @stop="stop(); status = 'Viewing stopped. The transient buffer was cleared.'" @earlier="earlier" @follow="following = $event" @review="emit('review', $event)" />
    <div v-else-if="viewer.kind === 'person'" class="terminal-pane">
      <p v-if="!readable">You cannot read this session.</p>
      <template v-else-if="session.mode === 'terminal' || !viewerAtOpen">
        <slot v-if="request?.view === 'expert' && session.capabilities.terminal_view && active && !hidden" name="terminal" :session="session" :viewer="viewer" :consent="consent" :model-name="modelName"><p>Live terminal output is unavailable.</p></slot>
        <p v-else>Live terminal output requires current watch permission and the owner’s active consent.</p>
      </template>
      <div v-else-if="terminalAtOpen" class="terminal-viewer">
        <p class="terminal-description"><AppIcon name="terminal" :size="20" /><span>{{ openedName }} runs in Zellij on {{ openedHost }}.</span></p>
        <a class="btn terminal-link" role="link" :href="link?.href" target="_blank" rel="noopener noreferrer" :aria-disabled="!link" :tabindex="link ? 0 : -1" @click="!link && $event.preventDefault()"><AppIcon name="terminal" :size="16" />Open live terminal<AppIcon name="external" :size="14" /></a>
        <p class="terminal-hint"><span :aria-hidden="!link">Read-only, in a new tab, on the tailnet. The first visit asks for the viewer token.</span><span :aria-hidden="!!link">The live terminal is no longer available.</span></p>
      </div>
    </div>
    <FloatingPanel v-if="typeAnchor && segment === 'events'" class="expert-types-panel" :anchor="typeAnchor" label="Event types" :width="260" :sheet="true" cycle @close="closeTypes">
      <div class="types-menu" role="group" aria-label="Event types">
        <p>Show these events</p>
        <div class="types-options">
          <label v-for="type in eventTypes" :key="type.key" class="type-option"><input type="checkbox" :checked="!hiddenTypes.includes(type.key)" @change="toggleType(type.key)"><AppIcon :name="type.icon" :size="16" /><span>{{ type.label }}</span></label>
        </div>
        <div class="types-actions"><button class="btn" type="button" @click="closeTypes(true)">Done</button></div>
      </div>
    </FloatingPanel>
  </section>
</template>

<style scoped>
.native-expert { display: flex; flex-direction: column; flex: 1; min-height: 0; min-width: 0; container: native-expert / inline-size; background: var(--surface); }
.expert-bar { display: flex; align-items: center; gap: 10px; flex: none; height: 52px; padding: 8px 12px 8px 14px; border-bottom: 1px solid var(--line); }
.expert-segments { flex: none; }
.expert-segments button { display: inline-flex; align-items: center; justify-content: center; gap: 6px; }
.event-types { display: flex; align-items: center; gap: 2px; min-width: 0; }
.type-toggle { display: inline-flex; align-items: center; gap: 5px; height: 30px; padding: 0 8px 0 6px; border: 0; border-radius: var(--radius-row); background: transparent; color: var(--ink-3); font-size: 12.5px; font-weight: 600; white-space: nowrap; }
.type-toggle[aria-pressed="true"] { color: var(--ink); }
.type-toggle[aria-pressed="true"] svg { color: var(--teal-ink); }
.type-toggle:hover { background: var(--row-hover); }
.types-button { display: none; height: 36px; }
.type-count { display: inline-block; min-width: 5ch; font-variant-numeric: tabular-nums; }
.grow { flex: 1; }
.follow { flex: none; gap: 8px; font-size: 12.5px; font-weight: 600; color: var(--ink-2); }
.follow-short { display: none; }
.expert-types-panel { display: flex; flex-direction: column; }
.types-menu { display: flex; flex-direction: column; min-height: 0; padding: 12px; }
.types-menu > p { flex: none; margin-bottom: 6px; font-size: 12.5px; }
.types-options { min-height: 0; overflow: auto; overscroll-behavior: contain; }
.types-actions { display: flex; justify-content: flex-end; flex: none; padding-top: 12px; border-top: 1px solid var(--line); }
.types-actions .btn { min-height: 44px; }
.type-option { display: flex; align-items: center; justify-content: flex-start; gap: 12px; height: 44px; cursor: pointer; font-size: 13px; }
.type-option input { flex: none; width: 16px; height: 16px; margin: 0; accent-color: var(--teal); }
.terminal-pane { flex: 1; min-height: 0; overflow: auto; }
.terminal-pane > p { padding: 24px; font-size: 13px; }
.terminal-viewer { padding: 24px; }
.terminal-description { display: flex; align-items: flex-start; gap: 10px; color: var(--ink); font-size: 14px; line-height: 1.5; margin-bottom: 14px; overflow-wrap: anywhere; }
.terminal-description svg { color: var(--ink-3); flex: none; }
.terminal-link[aria-disabled="true"] { opacity: .55; cursor: default; }
.terminal-link { max-width: 100%; }
.terminal-hint { display: grid; margin-top: 12px; color: var(--ink-3); font-size: 13px; line-height: 1.5; }
.terminal-hint span { grid-area: 1 / 1; }
.terminal-hint [aria-hidden="true"] { visibility: hidden; }
@container native-expert (max-width: 760px) { .event-types { display: none; } .types-button { display: inline-flex; } }
@container native-expert (max-width: 430px) {
  .follow-long { display: none; } .follow-short { display: inline; }
  .types-button > span { display: none; }
  .types-button { width: 44px; padding: 0; }
  .expert-bar { gap: 6px; padding: 4px 8px 4px 10px; }
}
@media (pointer: coarse), (max-width: 600px) {
  .expert-bar { height: 56px; }
  .expert-segments button, .types-button, .follow, .terminal-link { min-height: 44px; }
  .native-expert { padding-bottom: env(safe-area-inset-bottom); }
}
@media (max-width: 600px) {
  .types-menu { flex: 1; padding-top: calc(12px + env(safe-area-inset-top)); padding-bottom: calc(12px + env(safe-area-inset-bottom)); }
  .types-options { flex: 1; }
}
</style>
