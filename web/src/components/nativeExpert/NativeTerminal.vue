<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import AppIcon from '../AppIcon.vue'
import { bindingKey, type ExpertConsent, type ExpertViewer } from '../../lib/nativeExpert/ports'
import type { NativeSessionView, NativeStreamEvent } from '../../lib/nativeSessionContract'
import { preferenceOwner, preferenceSaves, usePreference } from '../../lib/preferences'
import { holderKey, terminalRequest, browserClock, type HolderMeasurement, type NativeTerminalTransport, type TerminalActionRequest,
  type TerminalActionView, type TerminalClock, type TerminalControl } from '../../lib/nativeTerminal/ports'
import { terminalBytes, validEnvelope } from '../../lib/nativeTerminal/frames'
import { viewerGeometry } from '../../lib/nativeTerminal/geometry'
import { HolderSizeObserver } from '../../lib/nativeTerminal/holderSize'
import type { TerminalRenderer, TerminalRendererFactory } from '../../lib/nativeTerminal/renderer'

const props = withDefaults(defineProps<{
  session: NativeSessionView
  viewer: ExpertViewer
  consent: ExpertConsent | null
  active: boolean
  transport: NativeTerminalTransport
  rendererFactory: TerminalRendererFactory | null
  modelName?: string
  host?: string
  clientId?: string
  control?: TerminalControl | null
  action?: TerminalActionView | null
  /** All localised action labels, measured together before any state change. */
  actionLabels?: string[]
  /** Panel layout belongs to I2. No layout control lives in this view. */
  layout?: 'side' | 'wide' | 'full'
  /** I2 supplies the phone Full screen header's control slot. */
  controlTarget?: HTMLElement | null
  clock?: TerminalClock
}>(), { modelName: '', host: '', clientId: '', control: null, action: null,
  actionLabels: () => ['Take over', 'Hand back', 'End control…', 'Cancel'], layout: 'side', controlTarget: null, clock: () => browserClock })
const emit = defineEmits<{ action: [request: TerminalActionRequest]; measure: [measurement: HolderMeasurement] }>()
const root = ref<HTMLElement | null>(null), box = ref<HTMLElement | null>(null), screen = ref<HTMLElement | null>(null), scroller = ref<HTMLElement | null>(null), probe = ref<HTMLElement | null>(null)
const hidden = ref(document.hidden), phone = ref(window.innerWidth <= 600), fontsReady = ref(false), cell13 = ref(0)
const short = ref(window.innerHeight <= 480), viewportSpace = ref<number | null>(null)
const width = ref(0), height = ref(0), cols = ref(120), rows = ref(36), hasGrid = ref(false), watching = ref(false), blocked = ref(false)
const notice = ref(''), status = ref('Watch live to see the current terminal. Earlier output is unavailable.')
const request = computed(() => terminalRequest(props.session, props.viewer, props.consent))
const readable = computed(() => props.viewer.kind === 'person' && props.viewer.canRead && !!props.viewer.principalId &&
  (!props.session.current_binding || props.viewer.tenantId === props.session.current_binding.tenant_id))
const ownerKey = computed(() => `${props.viewer.tenantId}/${props.viewer.principalId}`)
const scopeKey = computed(() => JSON.stringify([ownerKey.value, props.viewer.kind, props.viewer.canRead, props.viewer.canWatch,
  props.session.owned_session_id, props.session.owner_person_id, bindingKey(props.session.current_binding), props.session.generation,
  props.session.mode, props.session.state, props.session.capabilities.terminal_view, props.session.capabilities.qualification_digest,
  props.session.controller?.controller_id, props.session.controller?.epoch,
  props.consent?.id, props.consent?.ownerPersonId, props.consent?.active, bindingKey(props.consent?.binding ?? null)]))
const available = computed(() => !!request.value && props.active && !hidden.value && !!props.rendererFactory && !blocked.value)
const control = computed(() => readable.value && props.control && props.session.controller &&
  bindingKey(props.control.binding) === bindingKey(props.session.current_binding) &&
  props.control.controllerEpoch === props.session.controller.epoch ? props.control : null)
const holding = computed(() => !!control.value && control.value.source === 'web' &&
  props.session.controller?.actor_id === props.viewer.principalId && props.session.controller?.client_id === props.clientId && !!props.clientId)
const measureKey = computed(() => available.value ? holderKey(props.session, props.viewer, control.value, props.clientId) : '')
const preference = usePreference<{ phone?: boolean; large?: boolean }>('agents:terminal.fit')
const ownPreference = computed(() => props.viewer.kind === 'person' && preferenceOwner.value === ownerKey.value)
const deviceClass = computed(() => phone.value || short.value ? 'phone' : 'large')
const fit = ref(false), fitMessage = ref('')
watch(() => [ownerKey.value, ownPreference.value, deviceClass.value, preference.value.value] as const, () => {
  fit.value = ownPreference.value ? preference.value.value?.[deviceClass.value] === true : false; fitMessage.value = ''
}, { immediate: true, flush: 'sync' })
async function toggleFit() {
  if (holding.value) return
  const key = ownerKey.value, device = deviceClass.value, next = !fit.value
  if (!ownPreference.value) { fit.value = next; fitMessage.value = 'Fit applies to this view only.'; return }
  if (!await preference.ensureLoaded()) { if (key === ownerKey.value) fitMessage.value = 'Fit could not be saved. Try again.'; return }
  if (key !== ownerKey.value || device !== deviceClass.value || !ownPreference.value || holding.value) return
  preference.save({ ...preference.value.value, [device]: next }, 0)
}
const geometry = computed(() => viewerGeometry(width.value, height.value, cols.value, rows.value, cell13.value || 7.8, fit.value && !holding.value, holding.value))
const edge = computed(() => [geometry.value.top ? 'top' : '', geometry.value.right ? 'right' : ''].filter(Boolean).join(' '))
const gridStyle = computed(() => ({ width: `${geometry.value.width}px`, height: `${geometry.value.height}px`,
  '--nt-cell': `${geometry.value.cell}px`, '--nt-font': `${geometry.value.font}px`, '--nt-line-height': `${geometry.value.line}px` }))
const stageStyle = computed(() => ({ width: `${Math.max(width.value, geometry.value.width)}px`, height: `${Math.max(height.value, geometry.value.height)}px` }))
const risk = computed(() => !!control.value?.atRisk)
const heading = computed(() => {
  if (!readable.value) return 'Terminal unavailable'
  if (risk.value) return props.host ? `Reconnecting to ${props.host}` : 'Reconnecting'
  if (!props.session.controller) return 'Nobody has control'
  if (props.session.controller.actor_id === props.viewer.principalId) {
    if (control.value?.source === 'cli') return 'You have control in a terminal (aeon take)'
    return holding.value ? 'You have control' : 'Control is in your other tab'
  }
  return control.value?.holderName ? `Read-only · ${control.value.holderName} has control` : 'Read-only · another person has control'
})
const detail = computed(() => {
  if (!readable.value) return ''
  if (risk.value) return 'input paused'
  if (!props.session.controller) return props.session.mode === 'structured' ? 'structured events are in Events' : 'no terminal is open'
  if (holding.value) return 'read-only here · use your local terminal'
  return `sized for ${props.session.controller.actor_id === props.viewer.principalId ? 'your terminal' : control.value?.holderName ? `${control.value.holderName.split(' ')[0]}’s window` : 'the holder’s window'}`
})
const tooltip = computed(() => [heading.value, detail.value, control.value?.since ? `Since ${control.value.since}.` : '',
  hasGrid.value ? `${cols.value} × ${rows.value}.` : '', risk.value && control.value?.remainingSeconds != null ? `Control grace: ${control.value.remainingSeconds} s; the server decides when it expires.` : '',
  'Live output only, read-only here. Messages in Chat wait until control is handed back.', notice.value, fitMessage.value,
  preferenceSaves.failed.has(`${ownerKey.value}/agents:terminal.fit`) ? 'Fit could not be saved. Try again.' : ''].filter(Boolean).join(' '))
const visibleStatus = computed(() => !readable.value ? 'You cannot read this session.' :
  props.session.mode !== 'terminal' || props.session.state !== 'taken' ? 'No terminal is open. Read structured output in Events.' :
  !request.value ? 'Terminal output needs current watch permission and the owner’s active consent.' :
  !props.rendererFactory ? 'Live terminal output is not available for this session yet.' : status.value)
const actionDisabled = computed(() => !readable.value || !props.action || props.action.disabled ||
  (risk.value && props.action.kind === 'return') || bindingKey(props.action.binding) !== bindingKey(props.session.current_binding) ||
  props.action.revision !== props.session.revision || props.action.controllerEpoch !== (props.session.controller?.epoch ?? null))

let abort: AbortController | null = null, renderer: TerminalRenderer | null = null, turn = 0, mounted = false
let envelopeSeq: bigint | null = null, terminalSeq: bigint | null = null, terminalEpoch = '', retiredEpochs = new Set<string>()
let sizeObserver: ResizeObserver | null = null, fontObserver: MutationObserver | null = null
function clearGrid() {
  renderer?.dispose(); renderer = null; screen.value?.replaceChildren(); hasGrid.value = false
  envelopeSeq = null; terminalSeq = null; terminalEpoch = ''; retiredEpochs = new Set()
}
function stop() { turn++; abort?.abort(); abort = null; watching.value = false; clearGrid() }
function unavailable(message: string) { stop(); blocked.value = true; status.value = message }
function receive(event: NativeStreamEvent, current: () => boolean) {
  if (!current() || !validEnvelope(event)) return
  const frozen = request.value
  if (!frozen || bindingKey(event.binding) !== bindingKey(frozen.binding)) return
  if (event.generation !== frozen.generation) { unavailable('The session generation changed. Reopen the current session to watch.'); return }
  const seq = BigInt(event.seq)
  if (envelopeSeq !== null && seq <= envelopeSeq) return
  envelopeSeq = seq
  if (event.kind === 'end') {
    unavailable(event.payload.reason === 'revoked' ? 'Viewing ended because access or the owner’s consent was revoked.' : 'The terminal connection ended. Earlier output is unavailable.'); return
  }
  if (event.kind === 'control.lease_expired' || event.kind === 'control.ended_by') {
    unavailable('Control ended. Read the current structured output in Events.'); return
  }
  if (event.kind === 'snapshot') {
    if (!validEnvelope({ ...event, binding: event.payload.current_binding! }) || event.payload.mode !== 'terminal' || event.payload.state !== 'taken' || !event.payload.capabilities?.terminal_view ||
        event.payload.owner_person_id !== props.session.owner_person_id || bindingKey(event.payload.current_binding) !== bindingKey(frozen.binding)) {
      unavailable('The current session no longer permits terminal output.');
    }
    return
  }
  if (event.kind === 'gap' || BigInt(event.dropped_frames) > 0n) {
    stop(); notice.value = 'Terminal output was lost. This view is incomplete; watch live for a fresh grid.'; status.value = notice.value; return
  }
  if (event.kind !== 'terminal') return
  const decoded = terminalBytes(event)
  if (!decoded) { stop(); notice.value = 'A terminal frame exceeded its permitted shape or bounds. Watch live for a fresh grid.'; status.value = notice.value; return }
  const { frame, bytes } = decoded
  if (retiredEpochs.has(frame.terminal_epoch)) return
  const value = BigInt(frame.sequence)
  if (frame.terminal_epoch === terminalEpoch && terminalSeq !== null && value <= terminalSeq) return
  const changedEpoch = terminalEpoch !== frame.terminal_epoch
  if (!frame.reset && (changedEpoch || terminalSeq === null || value !== terminalSeq + 1n || frame.cols !== cols.value || frame.rows !== rows.value)) {
    stop(); notice.value = 'The terminal grid changed or output is missing. Watch live for a fresh grid.'; status.value = notice.value; return
  }
  if (frame.reset) {
    renderer?.dispose(); renderer = null; screen.value?.replaceChildren()
    if (changedEpoch && terminalEpoch) {
      retiredEpochs.add(terminalEpoch)
      if (retiredEpochs.size > 16) { unavailable('The terminal restarted repeatedly. Reopen the current session to watch.'); return }
      notice.value = 'A new terminal epoch started. Earlier output is unavailable.'
    }
    terminalEpoch = frame.terminal_epoch; cols.value = frame.cols; rows.value = frame.rows
    try {
      renderer = props.rendererFactory!(screen.value!, frame.cols, frame.rows, (message, closed) => {
        if (!current()) return
        notice.value = message
        if (closed) { stop(); status.value = message }
      }, props.clock)
    } catch { unavailable('Live terminal rendering is unavailable.'); return }
  }
  terminalSeq = value; hasGrid.value = true
  if (!renderer?.append(bytes)) { stop(); status.value = notice.value || 'Live terminal rendering stopped. Watch live to reconnect.'; return }
  status.value = 'Live terminal · read-only'
  if (frame.reset) nextTick(() => { if (current() && scroller.value) scroller.value.scrollTop = scroller.value.scrollHeight })
}
async function start() {
  if (!available.value || !mounted) return
  stop(); notice.value = ''; status.value = 'Connecting to the current live terminal. Earlier output is unavailable.'
  const frozen = request.value!, scope = scopeKey.value, epoch = turn, factory = props.rendererFactory
  const controller = abort = new AbortController()
  const current = () => mounted && turn === epoch && !controller.signal.aborted && scopeKey.value === scope && available.value && factory === props.rendererFactory
  watching.value = true
  try {
    await props.transport.stream(frozen, event => receive(event, current), controller.signal)
    if (current()) { stop(); notice.value = 'The connection ended; terminal output may be missing.'; status.value = 'Watch live to reconnect to a fresh grid.' }
  } catch (error) {
    if (!current()) return
    const reason = error && typeof error === 'object' && 'reason' in error ? error.reason : null
    const code = error && typeof error === 'object' && 'status' in error ? Number(error.status) : null
    if (['permission_revoked', 'grant_expired', 'wrong_principal'].includes(String(reason)) || [401, 403, 404].includes(code ?? 0)) unavailable('Viewing ended because access or the owner’s consent was revoked.')
    else { stop(); notice.value = 'The live connection failed. Terminal output may be missing.'; status.value = 'Watch live to reconnect to a fresh grid.' }
  }
}
function invokeAction() {
  const action = props.action, binding = props.session.current_binding
  if (!readable.value || !action || action.disabled || (risk.value && action.kind === 'return') || !binding ||
      bindingKey(action.binding) !== bindingKey(binding) || action.revision !== props.session.revision ||
      action.controllerEpoch !== (props.session.controller?.epoch ?? null) ||
      props.viewer.tenantId !== binding.tenant_id || binding.owned_session_id !== props.session.owned_session_id) return
  emit('action', { kind: action.kind, binding: Object.freeze({ ...binding }), ownedSessionId: props.session.owned_session_id,
    revision: props.session.revision, controllerEpoch: props.session.controller?.epoch ?? null })
}
function protectEscape(event: KeyboardEvent) {
  if (holding.value && event.key === 'Escape') event.stopPropagation()
}
const holderSizer = new HolderSizeObserver(props.clock, () => ({ key: measureKey.value, visible: props.active && !hidden.value,
  fontsReady: fontsReady.value, scale: window.visualViewport?.scale ?? 1, width: width.value, height: height.value, cell13: cell13.value,
  request: request.value && measureKey.value ? { binding: request.value.binding, generation: request.value.generation,
    revision: props.session.revision, controllerEpoch: props.session.controller!.epoch, clientId: props.clientId } : null }), measurement => emit('measure', measurement))
function measureBox() {
  if (!mounted || !box.value || !probe.value) return
  phone.value = window.innerWidth <= 600
  short.value = window.innerHeight <= 480
  if (phone.value && window.visualViewport && window.visualViewport.scale === 1 && root.value) {
    viewportSpace.value = Math.max(0, window.visualViewport.height + window.visualViewport.offsetTop - root.value.getBoundingClientRect().top)
  } else if (!phone.value) viewportSpace.value = null
  if (fontsReady.value) cell13.value = probe.value.getBoundingClientRect().width / 200
  width.value = box.value.clientWidth; height.value = box.value.clientHeight
  holderSizer.changed()
}
function visibility() {
  hidden.value = document.hidden
  if (hidden.value) { stop(); status.value = 'Stopped watching while this page is hidden. Watch live to continue.' }
  measureBox()
}
watch(scopeKey, () => { stop(); blocked.value = false; notice.value = ''; status.value = 'The session or viewing permission changed. Watch live to choose the current view.'; holderSizer.changed() }, { flush: 'sync' })
watch(() => props.active, active => { if (!active) { stop(); status.value = 'Stopped watching while Terminal is hidden.' }; holderSizer.changed() }, { flush: 'sync' })
watch(() => [props.transport, props.rendererFactory], () => { stop(); status.value = 'The terminal connection changed. Watch live to continue.' }, { flush: 'sync' })
watch(() => [measureKey.value, props.layout, props.control?.atRisk], () => { measureBox() }, { flush: 'post' })
onMounted(() => {
  mounted = true
  sizeObserver = new ResizeObserver(measureBox); if (box.value) sizeObserver.observe(box.value)
  fontObserver = new MutationObserver(measureBox); fontObserver.observe(document.documentElement, { attributes: true, attributeFilter: ['style', 'class'] })
  document.addEventListener('visibilitychange', visibility); window.addEventListener('resize', measureBox); window.addEventListener('orientationchange', measureBox)
  window.visualViewport?.addEventListener('resize', measureBox)
  document.fonts.addEventListener('loadingdone', measureBox)
  document.fonts.ready.then(() => { if (!mounted) return; fontsReady.value = true; measureBox() })
  measureBox(); start()
})
onBeforeUnmount(() => {
  mounted = false; stop(); holderSizer.dispose(); sizeObserver?.disconnect(); fontObserver?.disconnect()
  document.removeEventListener('visibilitychange', visibility); window.removeEventListener('resize', measureBox); window.removeEventListener('orientationchange', measureBox)
  window.visualViewport?.removeEventListener('resize', measureBox); document.fonts.removeEventListener('loadingdone', measureBox)
})
defineExpose({ stop, start, measureBox, holding })
</script>

<template>
  <section v-if="viewer.kind === 'person'" ref="root" class="native-terminal" :class="{ phone }" :style="viewportSpace === null ? undefined : { maxHeight: `${viewportSpace}px` }" :aria-label="modelName ? `Live terminal · ${modelName}` : 'Live terminal'" :data-layout="layout">
    <Teleport :to="controlTarget || 'body'" :disabled="!controlTarget">
      <div class="terminal-control" :class="{ risk }" data-terminal-control>
        <AppIcon :name="risk ? 'refresh' : session.controller ? holding ? 'hand' : 'eye' : 'terminal'" :size="15" />
        <span class="control-text" tabindex="0" :title="tooltip" :data-tip="tooltip" :aria-label="tooltip"><b>{{ heading }}</b><span v-if="detail"> · {{ detail }}</span></span>
        <div class="control-tools">
          <button v-if="readable && session.controller && !holding" type="button" class="fit-toggle" aria-label="Fit to window" :aria-pressed="fit" :title="fit ? 'Actual size' : 'Fit to window'" @click="toggleFit"><AppIcon :name="fit ? 'collapse' : 'expand'" :size="15" /></button>
          <button v-if="readable && action" type="button" class="terminal-action btn" :aria-disabled="actionDisabled" :aria-label="action.label" :title="[action.label, action.reason].filter(Boolean).join(' · ')" @click="invokeAction">
            <span class="action-size" aria-hidden="true"><span v-for="label in actionLabels" :key="label">{{ label }}</span></span>
            <span class="action-label"><AppIcon :name="action.kind === 'take' ? 'hand' : action.kind === 'cancel' ? 'close' : 'arrow-left'" :size="13" />{{ action.label }}</span>
          </button>
        </div>
      </div>
    </Teleport>
    <div ref="box" class="terminal-box" :class="{ dotted: hasGrid && !!edge, paused: risk }" data-terminal-box>
      <div v-show="hasGrid" ref="scroller" class="terminal-scroll">
        <div class="terminal-stage" :style="stageStyle">
          <div class="terminal-grid" :style="gridStyle" :data-edge="edge" :data-font="geometry.font" :data-cols="cols" :data-rows="rows" :data-native-terminal-holder="holding" tabindex="0" role="region" aria-label="Terminal output, read-only" aria-readonly="true" @keydown="protectEscape">
            <div ref="screen" class="terminal-screen"></div>
          </div>
        </div>
      </div>
      <div v-if="!hasGrid" class="terminal-empty">
        <AppIcon name="terminal" :size="24" /><p>{{ visibleStatus }}</p>
        <button v-if="available && !watching" type="button" class="btn" @click="start">Watch live</button>
      </div>
      <p v-if="hasGrid && (notice || fitMessage)" class="terminal-notice" role="status">{{ notice || fitMessage }}</p>
    </div>
    <div v-if="phone && holding" class="phone-keys" role="group" aria-label="Terminal keys, read-only here" title="Use these keys in the holder’s local terminal. This view sends no input.">
      <button type="button" disabled>Esc</button><button type="button" disabled>Tab</button><button type="button" disabled>Ctrl</button>
      <button v-for="direction in ['arrow-up', 'arrow-down', 'arrow-left', 'arrow'] as const" :key="direction" type="button" disabled :aria-label="direction"><AppIcon :name="direction" :size="15" /></button>
    </div>
    <span ref="probe" class="terminal-probe" aria-hidden="true">{{ 'M'.repeat(200) }}</span>
  </section>
</template>

<style scoped>
.native-terminal { --nt-bg: #f4f7f7; --nt-fg: #1f2d2e; --nt-void: #e6ecec; --nt-dot: rgba(31, 45, 46, .16); --nt-line: rgba(31, 45, 46, .24); --nt-ansi-0: #203c3d; --nt-ansi-1: #b23a32; --nt-ansi-2: #1b7a4a; --nt-ansi-3: #8c630c; --nt-ansi-4: #356c97; --nt-ansi-5: #8a3f9e; --nt-ansi-6: #0b6e86; --nt-ansi-7: #4c6163; --nt-ansi-8: #6c7f80; --nt-ansi-9: #b24a44; --nt-ansi-10: #2f7a5a; --nt-ansi-11: #9a6b12; --nt-ansi-12: #317ab0; --nt-ansi-13: #945a9a; --nt-ansi-14: #0e6f6c; --nt-ansi-15: #203c3d; position: relative; display: flex; flex-direction: column; flex: 1; min-height: 0; min-width: 0; height: 100%; padding: 8px var(--session-content-inset, 12px) 12px; overflow: hidden; box-sizing: border-box; }
:global(:root[data-theme="dark"]) .native-terminal { --nt-bg: #0d1718; --nt-fg: #d3e1e0; --nt-void: #121e1f; --nt-dot: rgba(211, 225, 224, .12); --nt-line: rgba(211, 225, 224, .22); --nt-ansi-0: #334c50; --nt-ansi-1: #f0897f; --nt-ansi-2: #6fd39b; --nt-ansi-3: #dfbf79; --nt-ansi-4: #8fc5e6; --nt-ansi-5: #d39ce6; --nt-ansi-6: #6cc9df; --nt-ansi-7: #dbe8e3; --nt-ansi-8: #8aa1a0; --nt-ansi-9: #f3a99f; --nt-ansi-10: #b2dfbd; --nt-ansi-11: #f1cf87; --nt-ansi-12: #a6d5f0; --nt-ansi-13: #debee9; --nt-ansi-14: #a9e6df; --nt-ansi-15: #f0f5ef; }
.terminal-control { position: relative; display: flex; align-items: center; gap: 8px; flex: none; height: 36px; min-width: 0; color: var(--ink-2); }
.terminal-control > svg { flex: none; }
.terminal-control.risk::before { content: ''; position: absolute; inset: 0 -8px; border-radius: var(--radius-row); background: var(--queue-wait-bg); box-shadow: inset 0 0 0 1px var(--queue-wait-line); }
.terminal-control > * { position: relative; }
.control-text { flex: 1; min-width: 0; white-space: nowrap; overflow: hidden; text-overflow: ellipsis; font-size: 13px; line-height: 20px; }
.control-text b { color: var(--ink); font-weight: 650; }
.control-text > span { color: var(--ink-3); font-size: 12.5px; }
.risk .control-text b, .risk .control-text > span { color: var(--queue-wait-ink); }
.control-text:focus-visible { outline: 2px solid var(--teal); outline-offset: -2px; }
.control-tools { display: flex; align-items: center; gap: 16px; flex: none; }
.fit-toggle { display: inline-flex; align-items: center; justify-content: center; position: relative; width: 28px; height: 28px; flex: none; padding: 0; border: 0; background: transparent; color: var(--ink-2); }
.fit-toggle[aria-pressed="true"] { color: var(--teal-ink); }
.terminal-action { position: relative; display: grid; height: 28px; min-height: 28px; padding: 0 10px; font-size: 12.5px; }
.action-size, .action-label { grid-area: 1 / 1; display: inline-flex; align-items: center; justify-content: center; gap: 6px; }
.action-size { display: grid; visibility: hidden; padding-left: 19px; }
.action-size > span { grid-area: 1 / 1; }
.terminal-action[aria-disabled="true"] { opacity: .5; cursor: default; }
.terminal-box { position: relative; flex: 1; min-height: 0; min-width: 0; margin-top: 8px; overflow: hidden; border-radius: var(--radius-row); background: var(--nt-bg); box-shadow: inset 0 0 0 1px var(--line); }
.terminal-box.dotted { background: radial-gradient(circle at 1px 1px, var(--nt-dot) .9px, transparent 1.2px) 0 0 / 9px 9px, var(--nt-void); }
.terminal-scroll { position: absolute; inset: 0; overflow: auto; overscroll-behavior: contain; }
.terminal-stage { position: relative; }
.terminal-grid { position: absolute; left: 0; bottom: 0; padding: 8px 10px; box-sizing: border-box; background: var(--nt-bg); color: var(--nt-fg); }
.terminal-grid[data-edge~="top"]::before, .terminal-grid[data-edge~="right"]::after { content: ''; position: absolute; pointer-events: none; background: var(--nt-line); z-index: 1; }
.terminal-grid[data-edge~="top"]::before { top: 0; left: 0; right: 0; height: 1px; }
.terminal-grid[data-edge~="right"]::after { top: 0; right: 0; bottom: 0; width: 1px; }
.terminal-grid:focus-visible { outline: 2px solid var(--teal); outline-offset: -2px; }
.terminal-screen { font: var(--nt-font)/var(--nt-line-height) var(--mono); font-variant-ligatures: none; white-space: pre; }
.terminal-screen :deep(.nt-row) { height: var(--nt-line-height); }
.terminal-screen :deep(span) { display: inline-block; height: var(--nt-line-height); overflow: hidden; vertical-align: top; font-style: normal; }
.paused .terminal-screen { opacity: .55; }
.terminal-empty { display: flex; align-items: center; justify-content: center; flex-direction: column; gap: 12px; height: 100%; padding: 24px; text-align: center; font-size: 13px; color: var(--ink-2); }
.terminal-empty p { max-width: 48ch; }
.terminal-notice { position: absolute; left: 10px; right: 10px; bottom: 8px; padding: 4px 8px; background: var(--nt-bg); color: var(--ink-2); font-size: 12px; overflow-wrap: anywhere; }
.terminal-probe { position: absolute; visibility: hidden; width: max-content; max-width: none; white-space: pre; font: 13px/17px var(--mono); font-variant-ligatures: none; pointer-events: none; }
.phone { padding: 0 var(--session-content-inset, 8px) max(8px, env(safe-area-inset-bottom)); }
.phone .terminal-action { width: 28px; padding: 0; }
.phone .action-size { display: none; }
.phone .action-label { font-size: 0; }
.phone .terminal-box { margin-top: 0; }
.phone-keys { display: flex; flex: none; justify-content: space-between; min-height: 44px; padding-bottom: env(safe-area-inset-bottom); }
.phone-keys button { display: flex; align-items: center; justify-content: center; flex: 1; min-width: 44px; height: 44px; padding: 0; border: 0; background: transparent; color: var(--ink-3); font-size: 12px; }
@media (pointer: coarse) { .fit-toggle::after, .terminal-action::after { content: ''; position: absolute; top: -8px; bottom: -8px; left: 0; right: 0; } .fit-toggle::after, .phone .terminal-action::after { left: -8px; right: -8px; } .control-tools { gap: 16px; } }
</style>
