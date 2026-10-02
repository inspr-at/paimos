<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script lang="ts">
export interface WindDownHost { id: string; label: string; online: boolean; meta: string }
</script>
<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import type { HarnessSession } from '../../lib/agents'
import { hostTick, liveSession, selectedAgent, toggleScope, type WindDownScope } from '../../lib/agentPause'
import { readPreference, writePreference } from '../../lib/preferences'
import { useSession } from '../../stores/session'
import { HARNESS_LABEL } from '../../lib/agentState'
import { assessAgentState, STATE_LABEL } from '../../lib/agentSignals'
import AppIcon from '../AppIcon.vue'
import KeyCap from '../KeyCap.vue'
const props = defineProps<{ hosts: WindDownHost[]; sessions: HarnessSession[]; scope: WindDownScope; anchor: HTMLElement; readonly: boolean }>()
const emit = defineEmits<{ change: [scope: WindDownScope]; close: [] }>()
const dialog = ref<HTMLDialogElement>(), left = ref(8), top = ref(60), height = ref(520)
const session = useSession(), group = ref<'host' | 'state'>('host')
let groupEpoch = 0
watch(() => `${session.identity?.tenant.id}:${session.identity?.principal.id}`, async () => {
  const turn = ++groupEpoch; group.value = 'host'
  if (!session.identity) return
  const saved = await readPreference('agents.wind-down-picker')
  if (turn === groupEpoch) group.value = saved?.group === 'state' ? 'state' : 'host'
}, { immediate: true, flush: 'sync' })
const running = computed(() => props.sessions.filter(liveSession))
const mac = /Mac|iPhone|iPad/.test(navigator.platform || navigator.userAgent)
const chosen = computed(() => running.value.filter(s => selectedAgent(s, props.scope)).length)
const hostCount = computed(() => props.scope.hosts === 'all' ? props.hosts.length : props.scope.hosts.length)
const hostRows = (id: string) => running.value.filter(s => s.host === id)
const hostName = (id: string) => props.hosts.find(h => h.id === id)?.label || id
const name = (s: HarnessSession) => s.display_label || s.agent?.name || s.host
const openedAt = Date.now()
const state = (s: HarnessSession) => assessAgentState(s, openedAt).state
const stateLabel = (s: HarnessSession) => STATE_LABEL[state(s)].toLowerCase()
const stateRows = computed(() => (['problem', 'unresponsive', 'waiting', 'awaiting', 'throttled', 'pausing', 'working', 'idle', 'stale'] as const).map(word => ({ word: STATE_LABEL[word], sessions: running.value.filter(s => state(s) === word) })).filter(g => g.sessions.length))
function toggle(kind: 'host' | 'agent', id: string) { if (!props.readonly) emit('change', toggleScope(props.scope, kind, id, props.sessions, props.hosts.map(h => h.id))) }
let groupWrite = Promise.resolve()
function setGroup(value: 'host' | 'state') {
  const turn = ++groupEpoch; group.value = value
  groupWrite = groupWrite.then(async () => { if (turn === groupEpoch) await writePreference('agents.wind-down-picker', { group: value }) })
}
function place() {
  const box = props.anchor.getBoundingClientRect()
  left.value = Math.max(8, Math.min(box.left, innerWidth - 438))
  top.value = Math.max(12, Math.min(box.bottom + 8, innerHeight - 330))
  height.value = Math.min(520, Math.max(300, innerHeight - top.value - 12))
}
function close() { emit('close'); props.anchor.focus({ preventScroll: true }) }
function keys(event: KeyboardEvent) {
  if (event.key === 'Escape') { event.preventDefault(); event.stopPropagation(); close(); return }
  if (event.key === 'Enter' && !event.altKey && !event.shiftKey && (mac ? event.metaKey && !event.ctrlKey : event.ctrlKey && !event.metaKey)) { event.preventDefault(); close(); return }
  if (event.metaKey || event.ctrlKey || event.altKey) return
  const target = event.target as HTMLElement
  if (target.closest('[role="radiogroup"]') && ['ArrowLeft', 'ArrowRight'].includes(event.key)) { event.preventDefault(); setGroup(group.value === 'host' ? 'state' : 'host'); void nextTick(() => dialog.value?.querySelector<HTMLElement>('[role="radio"][aria-checked="true"]')?.focus()); return }
  const rows = [...(dialog.value?.querySelectorAll<HTMLElement>('[role="checkbox"]') ?? [])], index = rows.indexOf(target)
  if (index < 0) return
  if (['ArrowDown', 'ArrowUp', 'Home', 'End'].includes(event.key)) {
    event.preventDefault(); const next = event.key === 'Home' ? 0 : event.key === 'End' ? rows.length - 1 : (index + (event.key === 'ArrowDown' ? 1 : -1) + rows.length) % rows.length
    rows[next]?.focus(); return
  }
  if (/^[a-z0-9]$/i.test(event.key)) { event.preventDefault(); const ordered = [...rows.slice(index + 1), ...rows.slice(0, index + 1)]; ordered.find(row => row.dataset.name?.toLowerCase().startsWith(event.key.toLowerCase()))?.focus() }
}
onMounted(async () => { place(); await nextTick(); dialog.value?.showModal(); dialog.value?.querySelector<HTMLElement>('[role="checkbox"]')?.focus({ preventScroll: true }); window.addEventListener('resize', place) })
onBeforeUnmount(() => { groupEpoch++; dialog.value?.close(); window.removeEventListener('resize', place) })
</script>
<template>
  <Teleport to="body"><dialog ref="dialog" class="wind-picker" aria-label="Hosts to wind down" :style="{ '--picker-left': `${left}px`, '--picker-top': `${top}px`, '--picker-height': `${height}px` }" @keydown="keys" @cancel.prevent="close" @click="event => { if (event.target === dialog) close() }">
    <div class="picker-frame">
      <header class="picker-head"><div class="picker-title"><h2>Hosts and agents</h2><button class="icon-btn flat" type="button" aria-label="Close host picker" @click="close"><AppIcon name="close" :size="14" /></button></div><div class="group-line"><span>Group by</span><div class="seg" role="radiogroup" aria-label="Group by"><button type="button" role="radio" :aria-checked="group === 'host'" @click="setGroup('host')">Host</button><button type="button" role="radio" :aria-checked="group === 'state'" @click="setGroup('state')">State</button></div><div class="quick"><button type="button" :disabled="readonly" :aria-pressed="scope.hosts === 'all'" @click="emit('change', { hosts: 'all' })">All</button><span>·</span><button type="button" :disabled="readonly" :aria-pressed="scope.hosts !== 'all' && !scope.hosts.length && !scope.agents?.length" @click="emit('change', { hosts: [], agents: [] })">None</button></div></div></header>
      <div class="picker-list">
        <template v-if="group === 'host'">
          <section v-for="host in hosts" :key="host.id" :aria-label="host.label">
            <button type="button" class="picker-row host" role="checkbox" :aria-checked="hostTick(host.id, scope, sessions)" :aria-disabled="readonly" :data-name="host.label" @click="toggle('host', host.id)"><span class="tickbox"><AppIcon v-if="hostTick(host.id, scope, sessions) !== 'false'" :name="hostTick(host.id, scope, sessions) === 'mixed' ? 'minus' : 'check'" :size="12" /></span><span class="row-copy"><strong>{{ host.label }}</strong><small>{{ host.meta }}</small></span><span class="row-state">{{ hostRows(host.id).length ? `${hostRows(host.id).length} running` : host.online ? 'idle' : 'offline' }}</span></button>
            <button v-for="s in hostRows(host.id)" :key="s.id" type="button" class="picker-row agent" role="checkbox" :aria-checked="selectedAgent(s, scope)" :aria-disabled="readonly" :data-name="name(s)" @click="toggle('agent', s.id)"><span class="tickbox"><AppIcon v-if="selectedAgent(s, scope)" name="check" :size="12" /></span><span class="row-copy"><strong>{{ name(s) }}</strong><small>{{ HARNESS_LABEL[s.harness] || s.harness }}<template v-if="s.supported_pause_levels?.length === 1"> · can only be stopped</template></small></span><span class="row-state" :title="stateLabel(s)">{{ stateLabel(s) }}</span></button>
          </section>
        </template>
        <template v-else>
          <section v-for="g in stateRows" :key="g.word"><h3>{{ g.word }}</h3><button v-for="s in g.sessions" :key="s.id" type="button" class="picker-row" role="checkbox" :aria-checked="selectedAgent(s, scope)" :aria-disabled="readonly" :data-name="name(s)" @click="toggle('agent', s.id)"><span class="tickbox"><AppIcon v-if="selectedAgent(s, scope)" name="check" :size="12" /></span><span class="row-copy"><strong>{{ name(s) }}</strong><small>{{ hostName(s.host) }} · {{ HARNESS_LABEL[s.harness] || s.harness }}</small></span></button></section>
          <section v-for="g in ['idle', 'offline']" :key="g"><h3>{{ g }} hosts</h3><button v-for="host in hosts.filter(h => !hostRows(h.id).length && (g === 'idle' ? h.online : !h.online))" :key="host.id" type="button" class="picker-row" role="checkbox" :aria-checked="hostTick(host.id, scope, sessions)" :aria-disabled="readonly" :data-name="host.label" @click="toggle('host', host.id)"><span class="tickbox"><AppIcon v-if="hostTick(host.id, scope, sessions) === 'true'" name="check" :size="12" /></span><span class="row-copy"><strong>{{ host.label }}</strong><small>{{ host.meta }}</small></span></button></section>
        </template>
      </div>
      <footer class="picker-foot"><span>{{ readonly ? 'Turn off wind-down to change this.' : `${chosen} of ${running.length} agents · ${hostCount} of ${hosts.length} hosts` }}</span><button type="button" class="btn sm primary" @click="close">Done<span class="submit-keys"><KeyCap k="mod" /><KeyCap k="enter" /></span></button></footer>
    </div>
  </dialog></Teleport>
</template>
<style scoped>
.submit-keys{display:inline-flex;gap:2px;align-items:center;flex:none}

.wind-picker{position:fixed;inset:var(--picker-top) auto auto var(--picker-left);margin:0;padding:0;width:430px;max-width:calc(100vw - 16px);height:var(--picker-height);max-height:none;border:1px solid var(--glass-edge);border-radius:14px;background:var(--surface-raised);color:var(--ink);box-shadow:var(--shadow-pop);overflow:hidden}.wind-picker::backdrop{background:transparent}.picker-frame{display:grid;grid-template-rows:106px minmax(0,1fr) 64px;height:100%}.picker-head{padding:10px 16px;border-bottom:1px solid var(--line)}.picker-title{display:flex;justify-content:space-between;align-items:center}.picker-title h2{font-size:15px}.group-line{display:flex;align-items:center;gap:10px;margin-top:10px;font-size:12px;color:var(--ink-2)}.quick{margin-left:auto;display:flex;align-items:center;gap:4px}.quick button{padding:5px 4px;background:transparent;border:0;color:var(--teal-ink);font-size:12px}.quick button[aria-pressed=true]{font-weight:700;color:var(--ink)}.picker-list{overflow:auto;overscroll-behavior:contain;padding:8px}.picker-row{display:flex;align-items:center;gap:10px;width:100%;height:54px;border:0;border-radius:6px;background:transparent;color:var(--ink);padding:6px 8px;text-align:left}.picker-row:hover{background:var(--row-hover)}.picker-row[aria-checked=true]{background:var(--row-selected)}.picker-row.agent{padding-left:28px}.tickbox{display:grid;place-items:center;flex:none;width:17px;height:17px;border:1px solid var(--line-2);border-radius:4px;color:var(--teal-ink)}.row-copy{min-width:0;display:grid;gap:3px;flex:1}.row-copy strong{font-size:13px;font-weight:550;overflow:hidden;text-overflow:ellipsis;white-space:nowrap}.host strong{font-weight:650}.row-copy small{font-size:11px;color:var(--ink-3);overflow:hidden;text-overflow:ellipsis;white-space:nowrap}.row-state{white-space:nowrap;overflow:hidden;text-overflow:ellipsis;flex:none;width:70px;text-align:right;font-size:11px;color:var(--ink-2);font-variant-numeric:tabular-nums}.picker-list h3{font:500 10px var(--mono);text-transform:uppercase;letter-spacing:.1em;padding:12px 8px 4px}.picker-foot{display:flex;align-items:center;justify-content:space-between;gap:8px;padding:10px 14px;border-top:1px solid var(--line)}.picker-foot>span{font-size:11px;color:var(--ink-3);font-variant-numeric:tabular-nums}
@media(max-width:600px){.wind-picker{inset:auto 0 0;width:100%;max-width:none;height:85dvh;border-radius:18px 18px 0 0}.wind-picker::backdrop{background:var(--scrim)}.picker-frame{grid-template-rows:112px minmax(0,1fr) calc(70px + env(safe-area-inset-bottom))}.picker-foot{padding-bottom:calc(12px + env(safe-area-inset-bottom))}.picker-foot .btn{min-height:44px}.picker-title .icon-btn,.quick button{min-height:44px}.picker-head{padding-top:6px}.group-line{margin-top:2px}}
</style>
