<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import type { SessionControl } from '../../lib/agents'
import { GROUPS, controlBlocked, elapsed, sessionForest, type SessionBranch, type SessionGroup } from '../../lib/agentState'
import { relativeTime } from '../../lib/work'
import type { Availability, SessionView } from '../../stores/agents'
import AppIcon from '../AppIcon.vue'
import TicketPeekLink from '../TicketPeekLink.vue'
import FloatingPanel from '../work/FloatingPanel.vue'
import ConnectHint from './ConnectHint.vue'
import { isStale, useSessionRemoval } from './sessionRemoval'
import AgentStateLabel from './AgentStateLabel.vue'
import { useAgentAppearance } from '../../lib/agentAppearance'
const { appearance } = useAgentAppearance()
import AgentGlyph from './AgentGlyph.vue'
import HarnessBadge from './HarnessBadge.vue'
import ProviderMark from './ProviderMark.vue'
import { intendedResult, sessionContext, sessionExecution } from './sessionRow'
import EtaCell from '../work/EtaCell.vue'
import { etaFromSession } from '../../lib/eta'

// Session families stay together across status groups. Each lead's history is
// opt-in for this mounted list only; refreshes never open it or persist it.
const props = defineProps<{
  removed?: SessionView[]; groups: Record<SessionGroup, SessionView[]>; now: number; cursor: string; selected: string; state: Availability; error: string
  loaded: boolean; controls: Record<string, SessionControl>; canControl: boolean; canStart: boolean
}>()
const emit = defineEmits<{ open: [id: string]; control: [view: SessionView, kind: SessionControl['kind']]; focusRow: [id: string]; retry: []; start: [] }>()
const showStopped = ref(false)
// Removed sessions are a separate, opt-in list; they keep their full history.
const showRemoved = ref(false)
const removedCount = computed(() => props.removed?.length ?? 0)
watch(removedCount, n => { if (!n) showRemoved.value = false })
const removal = useSessionRemoval()
const current = computed(() => GROUPS.flatMap(g => props.groups[g.id]))
const stale = computed(() => current.value.map(v => v.session).filter(s => isStale(s, props.now) && removal.canRemove(s)))
const total = computed(() => GROUPS.reduce((sum, g) => sum + props.groups[g.id].length, 0))
type Branch = SessionBranch<SessionView>
const forest = computed(() => sessionForest(showRemoved.value ? props.removed ?? [] : current.value, props.now))
// Three calm buckets in urgency order: what needs a look, what runs, what ended.
// Each row still names its exact state; a family sits with its most urgent member.
type Bucket = 'attention' | 'live' | 'stopped'
const BUCKETS: { id: Bucket; label: string }[] = [{ id: 'attention', label: 'Needs attention' }, { id: 'live', label: 'Live' }, { id: 'stopped', label: 'Stopped' }]
const bucketOf = (group: SessionGroup): Bucket => group === 'stopped' ? 'stopped' : group === 'working' || group === 'idle' ? 'live' : 'attention'
const rank = (group: SessionGroup) => GROUPS.findIndex(g => g.id === group)
const roots = (bucket: Bucket) => showRemoved.value
  ? (bucket === 'stopped' ? forest.value : [])
  : forest.value.filter(branch => bucketOf(branch.group) === bucket).sort((a, b) => rank(a.group) - rank(b.group))
const expanded = ref<Record<string, boolean>>({})
const history = ref<Record<string, boolean>>({})
const containsSelected = (branch: Branch): boolean => branch.view.session.id === props.selected || branch.children.some(containsSelected)
// A direct link may reveal its selected row, but never its stopped siblings.
const candidates = (branch: Branch) => branch.children.filter(child => showRemoved.value || history.value[branch.view.session.id] || child.liveCount > 0 || containsSelected(child))
const isExpanded = (branch: Branch): boolean => candidates(branch).length > 0 && (expanded.value[branch.view.session.id] ?? true)
function toggle(branch: Branch) {
  const id = branch.view.session.id
  expanded.value[id] = !isExpanded(branch)
}
function toggleHistory(branch: Branch) {
  const id = branch.view.session.id
  history.value[id] = !history.value[id]
  expanded.value[id] = true
}
const stoppedChildren = (branch: Branch) => branch.children.reduce((sum, child) => sum + child.count - child.liveCount, 0)
const workingChildren = (branch: Branch) => branch.children.reduce((sum, child) => sum + child.workingCount, 0)
const otherChildren = (branch: Branch) => branch.children.reduce((sum, child) => sum + child.liveCount - child.workingCount, 0)
const workerLabel = (branch: Branch) => `${branch.count - 1} ${branch.count === 2 ? 'worker' : 'workers'}`
const visible = (group: Bucket) => {
  const out: { view: SessionView; branch: Branch; depth: number; parent: string; open: boolean; guides: boolean[]; family: boolean; familyEnd: boolean; primary: string; context: string; exec: ReturnType<typeof sessionExecution> }[] = []
  function walk(branch: Branch, guides: boolean[], parent = '') {
    const open = isExpanded(branch)
    const primary = intendedResult(branch.view)
    out.push({ view: branch.view, branch, depth: guides.length, parent, open, guides, family: guides.length > 0 || open, familyEnd: false, primary, context: sessionContext(branch.view, primary), exec: sessionExecution(branch.view) })
    if (open) {
      const children = candidates(branch)
      children.forEach((child, index) => walk(child, [...guides, index < children.length - 1], branch.view.name))
    }
  }
  for (const branch of roots(group)) {
    if (group !== 'stopped' || showRemoved.value || showStopped.value || containsSelected(branch)) {
      walk(branch, [])
      out[out.length - 1]!.familyEnd = true
    }
  }
  return out
}
// Open the route's ancestors on navigation or when that session first arrives;
// subsequent ticks must not undo a person's explicit collapse.
const selectedPath = computed(() => {
  function find(branch: Branch): string[] | null {
    if (branch.view.session.id === props.selected) return [props.selected]
    for (const child of branch.children) {
      const path = find(child)
      if (path) return [branch.view.session.id, ...path]
    }
    return null
  }
  for (const branch of forest.value) {
    const path = find(branch)
    if (path) return path.join('/')
  }
  return ''
})
watch(selectedPath, path => {
  if (!path) return
  for (const id of path.split('/').slice(0, -1)) expanded.value[id] = true
  if (roots('stopped').some(containsSelected)) showStopped.value = true
}, { immediate: true })

const controlBlock = (view: SessionView, kind: SessionControl['kind']) => controlBlocked(view.session, kind, view.name, props.canControl, props.controls[view.session.id])
// A direct link to a removed session shows it in the Removed list.
watch(() => props.selected, id => {
  if (id && !current.value.some(v => v.session.id === id) && props.removed?.some(v => v.session.id === id)) showRemoved.value = true
}, { immediate: true })

// Every row has one quiet overflow button: process controls while it runs, and
// Remove from Agents for people. Wide rows also offer Interrupt on hover; Stop
// asks first anyway, so it lives in the menu.
const live = (view: SessionView) => view.session.phase !== 'stopped' && !view.session.archived_at
const hasMenu = (view: SessionView) => live(view) || removal.canRemove(view.session)
// The bound ticket's estimate sits under its key while the session runs; an ended
// session no longer speaks for the ticket.
const etaOf = (view: SessionView) => view.ticket && live(view) ? etaFromSession(view.session) : null
const hasEta = computed(() => current.value.some(view => !!etaOf(view)))
// Inline hover controls only where they can ever work; a session outside Aeon or
// a reader without write access finds the reason in the menu instead.
const inline = (view: SessionView, kind: SessionControl['kind']) => props.canControl && view.session.management_mode === 'managed' && view.session.advertised_capabilities.includes(kind)
// One reason for both controls is said once, under them.
const sharedBlock = (view: SessionView) => { const why = controlBlock(view, 'interrupt'); return why && why === controlBlock(view, 'stop') ? why : '' }
const menu = ref<{ view: SessionView; anchor: HTMLElement } | null>(null)
function openMenu(view: SessionView, event: MouseEvent) { menu.value = menu.value?.view.session.id === view.session.id ? null : { view, anchor: event.currentTarget as HTMLElement } }
function pick(kind: SessionControl['kind']) {
  const view = menu.value?.view
  menu.value = null
  if (view && !controlBlock(view, kind)) emit('control', view, kind)
}
function pickRemove() {
  const view = menu.value?.view
  menu.value = null
  if (view) void removal.removeOne(view.session, view.name)
}
function pendingLabel(view: SessionView) {
  const c = props.controls[view.session.id]
  if (!c || c.state === 'completed') return ''
  return c.kind === 'stop' ? (c.state === 'claimed' ? 'Stopping…' : 'Stop sent') : (c.state === 'claimed' ? 'Interrupting…' : 'Interrupt sent')
}
function rowClick(event: MouseEvent, id: string) {
  if ((event.target as HTMLElement).closest('a, button')) return
  emit('open', id)
}
</script>

<template>
  <section class="sessions glass-card" aria-labelledby="sessions-title">
    <header class="card-head">
      <h2 id="sessions-title">{{ showRemoved ? 'Removed sessions' : 'Sessions' }}</h2>
      <span v-if="loaded && state === 'ready'" class="head-tools">
        <button
          v-if="!showRemoved && stale.length" type="button" class="btn sm ghost quiet-btn" :disabled="removal.busy.value"
          :data-tip="`${stale.length} without a heartbeat for 15 minutes`" @click="removal.clearStale(stale)"
        >Clear stale</button>
        <button
          v-if="removedCount || showRemoved" type="button" class="btn sm ghost quiet-btn" :aria-pressed="showRemoved"
          :aria-label="showRemoved ? 'Back to sessions' : `Show ${removedCount} removed sessions`" @click="showRemoved = !showRemoved"
        ><template v-if="showRemoved"><AppIcon name="arrow-left" :size="13" />Sessions</template><template v-else>Removed<span class="count">{{ removedCount }}</span></template></button>
      </span>
    </header>

    <div v-if="state === 'forbidden'" class="state">
      <AppIcon name="agent" :size="20" />
      <h3>Sessions are visible to workspace members with agent access</h3>
      <p>Ask a workspace admin to give your account access to agent sessions.</p>
    </div>
    <div v-else-if="state === 'error'" class="state" role="alert">
      <AppIcon name="alert" :size="20" />
      <h3>Sessions could not be loaded</h3>
      <p>{{ error }}</p>
      <button type="button" class="btn" @click="emit('retry')"><AppIcon name="refresh" :size="14" />Try again</button>
    </div>
    <div v-else-if="!loaded" class="skeleton-rows" role="status" aria-label="Loading sessions">
      <div v-for="i in 5" :key="i" class="sk-row"><span class="skeleton dot" /><span class="skeleton" :style="{ width: `${18 + (i * 7) % 16}%` }" /><span class="skeleton key" /><span class="skeleton" style="width: 12%" /></div>
    </div>
    <p v-else-if="showRemoved && !removedCount" class="state">No removed sessions.</p>
    <ConnectHint v-else-if="!total && !showRemoved" :can-start="canStart" @start="emit('start')" />

    <div v-else class="table" :class="{ 'has-eta': hasEta }" role="table" aria-label="Agent sessions">
      <div class="thead" role="row">
        <span role="columnheader">State</span><span role="columnheader">Intended result</span><span role="columnheader">Ticket</span>
        <span role="columnheader" class="c-exec">Execution</span>
        <span role="columnheader" class="right c-beat">Heartbeat</span><span role="columnheader" class="right c-elapsed">Running</span><span role="columnheader"><span class="sr-only">Actions</span></span>
      </div>
      <template v-for="group in BUCKETS" :key="group.id">
        <div v-if="roots(group.id).length" class="group-row" :class="group.id" role="row">
          <span role="rowheader" class="group-label">
            <template v-if="showRemoved">Removed<span class="mono">{{ roots(group.id).length }}</span></template>
            <button v-else-if="group.id === 'stopped'" type="button" class="group-toggle" :aria-expanded="showStopped" @click="showStopped = !showStopped">
              <AppIcon name="chevron-right" :size="12" class="chev" :class="{ turned: showStopped }" />{{ group.label }}<span class="mono">{{ roots(group.id).length }}</span>
            </button>
            <template v-else>{{ group.label }}<span class="mono">{{ roots(group.id).length }}</span></template>
          </span>
        </div>
        <div
          v-for="{ view, branch, depth, parent, open, guides, family, familyEnd, primary, context, exec } in visible(group.id)" :key="view.session.id" class="row agent-state-surface" :data-state="view.status.state" role="row" :data-row="`s:${view.session.id}`" :data-parent="view.session.parent_harness_session_id || undefined" :data-depth="depth" :style="{ '--depth': depth, ...appearance(view.status.state) }" tabindex="-1" @focusin="emit('focusRow', `s:${view.session.id}`)"
          :class="[view.status.group, { worker: depth > 0, family, 'family-start': family && !depth, 'family-end': family && familyEnd, active: cursor === `s:${view.session.id}`, selected: selected === view.session.id }]" @click="rowClick($event, view.session.id)"
        >
          <span v-if="depth || open" class="tree-lines" aria-hidden="true">
            <span v-for="(continues, level) in guides" :key="level" class="tree-guide" :class="{ continues, elbow: level === depth - 1, last: level === depth - 1 && !continues }" :style="{ '--level': level }" />
            <span v-if="open" class="tree-stem" :style="{ '--level': depth }" />
          </span>
          <span role="cell" class="c-state"><AgentStateLabel :state="view.status.state" :label="view.status.label" :detail="pendingLabel(view)" /></span>
          <span role="cell" class="c-agent">
            <span v-if="depth" class="sr-only">Worker of {{ parent }}. </span>
            <RouterLink class="agent-link" :to="`/agents/${view.session.id}`" :aria-label="`${view.harness} ${view.name}, ${view.status.label}${view.session.role === 'coordinator' ? ', lead' : ''}. ${primary}. ${context}`">
              <span class="bot">
                <AgentGlyph :view="view" :size="30" />
                <HarnessBadge :harness="view.session.harness" />
              </span>
              <span class="who">
                <span class="result" :title="primary">{{ primary }}</span>
                <span class="session-context">
                  <span class="session-name" :title="context">{{ context }}</span>
                  <span v-if="view.session.role === 'coordinator'" class="role" data-tip="Coordinates other sessions">Lead</span>
                </span>
              </span>
            </RouterLink>
            <span v-if="branch.children.length" class="worker-tools">
              <button type="button" class="worker-toggle" :disabled="!candidates(branch).length" :aria-expanded="open" :aria-label="`${open ? 'Collapse' : 'Expand'} ${workerLabel(branch)} of ${view.name}: ${workingChildren(branch)} working`" @click="toggle(branch)">
                <AppIcon name="chevron-right" :size="12" class="chev" :class="{ turned: open }" />{{ workingChildren(branch) }} working
              </button>
              <template v-if="otherChildren(branch)"><span aria-hidden="true"> · </span><span class="idle-count">{{ otherChildren(branch) }} other active</span></template>
              <span aria-hidden="true"> · </span>
              <button type="button" class="worker-toggle history-toggle" :disabled="!stoppedChildren(branch)" :aria-expanded="!!history[view.session.id]" :aria-label="`${history[view.session.id] ? 'Hide' : 'Show'} stopped workers of ${view.name}: ${stoppedChildren(branch)} stopped`" @click="toggleHistory(branch)">{{ stoppedChildren(branch) }} stopped</button>
            </span>
          </span>
          <span role="cell" class="c-ticket">
            <TicketPeekLink v-if="view.ticket" class="ticket-chip" :ticket-key="view.ticket.key" :href="view.ticket.href" :tip="view.ticket.title">{{ view.ticket.key }}</TicketPeekLink>
            <span v-else class="faint">{{ view.projectKey || '—' }}</span>
            <EtaCell v-if="etaOf(view)" class="row-eta" align="start" :eta="etaOf(view)" :now="now" />
          </span>
          <span role="cell" class="c-exec" :aria-label="[exec.model ? exec.providerLabel : '', exec.modelLine, exec.accountLine].filter(Boolean).join('. ')">
            <span class="exec-icon"><ProviderMark :provider="exec.provider" /></span>
            <span class="exec-copy">
              <span v-if="exec.model" class="exec-model" :title="exec.modelLine">{{ exec.modelLine }}</span>
              <span class="exec-account" :title="exec.accountLine">{{ exec.accountLine }}</span>
            </span>
          </span>
          <span role="cell" class="right c-beat">
            <time v-if="view.session.heartbeat_at" :datetime="view.session.heartbeat_at">{{ relativeTime(view.session.heartbeat_at, { now }) }}</time>
            <span v-else class="faint">never</span>
          </span>
          <span role="cell" class="right c-elapsed mono-cell">{{ elapsed(view.session, now) }}</span>
          <span role="cell" class="c-actions">
            <template v-if="live(view)">
              <button
                v-if="inline(view, 'interrupt')" type="button" class="icon-btn sm flat act" :aria-label="`Interrupt ${view.name}`" :data-tip="controlBlock(view, 'interrupt') || 'Interrupt: stop the current turn, keep the session'"
                :aria-disabled="!!controlBlock(view, 'interrupt')" @click="!controlBlock(view, 'interrupt') && emit('control', view, 'interrupt')"
              ><AppIcon name="interrupt" :size="16" /></button>
            </template>
            <button
              v-if="hasMenu(view)" type="button" class="icon-btn sm flat more" :aria-label="`Actions for ${view.name}`" aria-haspopup="menu"
              :aria-expanded="menu?.view.session.id === view.session.id" @click="openMenu(view, $event)"
            ><AppIcon name="more" :size="16" /></button>
          </span>
        </div>
      </template>
    </div>
    <FloatingPanel v-if="menu" :anchor="menu.anchor" align="end" :width="248" :label="`Actions for ${menu.view.name}`" @close="menu = null">
      <div role="menu" :aria-label="`Actions for ${menu.view.name}`">
        <template v-if="live(menu.view)">
          <button type="button" role="menuitem" class="menu-item" :aria-disabled="!!controlBlock(menu.view, 'interrupt')" data-autofocus @click="pick('interrupt')">
            <AppIcon name="interrupt" :size="16" /><span class="mi-text"><span>Interrupt</span><small v-if="!sharedBlock(menu.view)">{{ controlBlock(menu.view, 'interrupt') || 'Stop the current turn, keep the session' }}</small></span>
          </button>
          <button type="button" role="menuitem" class="menu-item danger" :aria-disabled="!!controlBlock(menu.view, 'stop')" @click="pick('stop')">
            <AppIcon name="halt" :size="16" /><span class="mi-text"><span>Stop session…</span><small v-if="!sharedBlock(menu.view)">{{ controlBlock(menu.view, 'stop') || 'End this session; asks first' }}</small></span>
          </button>
          <p v-if="sharedBlock(menu.view)" class="menu-note">{{ sharedBlock(menu.view) }}</p>
        </template>
        <template v-if="removal.canRemove(menu.view.session)">
          <hr v-if="live(menu.view)" class="menu-sep">
          <button type="button" role="menuitem" class="menu-item" :data-autofocus="live(menu.view) ? undefined : ''" @click="pickRemove">
            <AppIcon name="archive" :size="16" /><span class="mi-text"><span>Remove from Agents…</span><small>Hides the record; does not stop the process</small></span>
          </button>
        </template>
      </div>
    </FloatingPanel>
  </section>
</template>

<style scoped>
.sessions { overflow: clip; container: sessions / inline-size; }
.card-head { display: flex; align-items: baseline; gap: 10px; padding: 14px 18px 10px; }
.card-head h2 { font-size: 15px; font-weight: 650; }
.head-tools { display: inline-flex; align-items: center; gap: 2px; margin-left: auto; margin-right: -8px; }
.quiet-btn { color: var(--ink-2); font-weight: 550; }
.quiet-btn:hover { color: var(--ink); }
.quiet-btn[aria-pressed="true"] { background: transparent; box-shadow: none; color: var(--ink-2); }
.quiet-btn[aria-pressed="true"]:hover { background: var(--row-selected); color: var(--ink); }
.quiet-btn .count { margin-left: 2px; font: 500 11.5px/1 var(--mono); color: var(--ink-3); font-variant-numeric: tabular-nums; }
.table { --state-width: 164px; --tree-step: 28px; display: grid; grid-template-columns: var(--state-width) minmax(140px, 1.45fr) minmax(72px, .48fr) minmax(128px, .82fr) 80px 68px 76px; padding: 0 0 8px; }
.thead, .row, .group-row { display: grid; grid-template-columns: subgrid; grid-column: 1 / -1; align-items: center; column-gap: 0; }
.thead { height: 32px; padding: 0 12px; border-top: 1px solid var(--line); border-bottom: 1px solid var(--line); font: 500 10.5px/1 var(--mono); letter-spacing: .14em; text-transform: uppercase; color: var(--ink-3); font-variant-ligatures: none; white-space: nowrap; }
.thead > span, .row > span { padding: 0 8px; min-width: 0; }
.right { text-align: right; justify-content: flex-end; }
.group-row { margin: 10px 6px 2px; padding: 0 12px; }
.group-label { grid-column: 1 / -1; display: inline-flex; align-items: center; gap: 8px; height: 26px; font: 500 10.5px/1 var(--mono); letter-spacing: .16em; text-transform: uppercase; color: var(--ink-3); font-variant-ligatures: none; }
.group-row.attention .group-label { color: var(--gold-ink); }
.group-label .mono { letter-spacing: 0; color: var(--ink-3); }
.group-toggle { display: inline-flex; align-items: center; gap: 8px; height: 26px; margin-left: -6px; padding: 0 8px 0 6px; border: 0; border-radius: 8px; background: transparent; font: inherit; letter-spacing: inherit; text-transform: inherit; color: inherit; }
.group-toggle:hover { background: var(--row-hover); color: var(--ink); }
.group-toggle:focus-visible { box-shadow: var(--focus-ring); }
.chev.turned { transform: rotate(90deg); }
/* Two label lines keep the glyph centre near the tree joint. Phones add padding. */
.row { --tree-joint: 22px; position: relative; min-height: 48px; margin: 0 6px; padding: 0 4px; border-radius: 10px; outline: none; cursor: pointer; font-size: 13px; }
.row.family { border-radius: 0; }
.row.family-start { border-radius: 10px 10px 0 0; }
.row.family-end { border-radius: 0 0 10px 10px; }
@media (hover: hover) { .row:hover { background: var(--row-hover); } }
.row.active { background: var(--row-selected); box-shadow: inset 0 0 0 1px var(--chip-teal-line); }
.row.selected { background: var(--row-selected); }
.row { transition: background-color .3s ease, color .3s ease; }
.row.stopped { color: var(--ink-2); }
.row.stopped .agent-name { font-weight: 450; color: var(--ink-2); }
.row.worker .c-agent { padding-left: calc(8px + var(--depth) * var(--tree-step)); }
/* The track is anchored to the lead glyph's centre. Each visible descendant
   carries its ancestors' tracks across row boundaries; the last child closes
   its track at the badge. The toggle remains in the lead's text column. */
.row > .tree-lines { position: absolute; inset: 0 0 0 calc(var(--state-width) + 17px); padding: 0; pointer-events: none; color: var(--ink-3); }
.tree-guide, .tree-stem { position: absolute; left: calc(var(--level) * var(--tree-step)); top: 0; bottom: 0; width: var(--tree-step); }
.tree-guide.continues::before, .tree-guide.elbow::before { content: ''; position: absolute; top: 0; bottom: 0; width: 1px; background: currentColor; }
.tree-guide.last::before { bottom: auto; height: calc(var(--tree-joint) - 4px); }
.tree-guide.elbow::after { content: ''; position: absolute; top: calc(var(--tree-joint) - 4px); left: 0; width: calc(var(--tree-step) - 15px); height: 5px; border: solid currentColor; border-width: 0 0 1px 1px; border-radius: 0 0 0 5px; }
.tree-stem { top: calc(var(--tree-joint) + 15px); bottom: 0; width: 1px; background: currentColor; }
.worker-tools { display: flex; align-items: center; flex-wrap: wrap; gap: 2px; flex-basis: 100%; padding: 2px 0 6px 38px; color: var(--ink-2); font-size: 11.5px; }
.worker-toggle { display: inline-flex; align-items: center; justify-content: center; gap: 4px; min-height: 28px; padding: 2px 6px; border: 0; border-radius: 6px; background: transparent; color: var(--ink); font: inherit; font-weight: 550; white-space: nowrap; }
.worker-toggle:hover:not(:disabled) { background: var(--row-hover); }
.worker-toggle:disabled { cursor: default; }
.worker-toggle:focus-visible { box-shadow: var(--focus-ring); }
.history-toggle { color: var(--ink-2); font-weight: 450; }
.history-toggle[aria-expanded="true"] { background: var(--row-hover); color: var(--ink); }
.idle-count { padding-inline: 4px; white-space: nowrap; }
.chev { transition: transform .2s ease; }
@media (prefers-reduced-motion: reduce) { .row, .chev { transition: none; } }
.c-state { display: inline-flex; align-items: center; gap: 9px; min-width: 0; }
.c-state :deep(.state-word) { white-space: nowrap; }
.state-label { font-size: 12.5px; color: var(--ink-2); white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
.row.needs .state-label { color: var(--gold-ink); font-weight: 600; }
.row > .c-agent { display: inline-flex; align-items: center; flex-wrap: wrap; gap: 4px 8px; min-width: 0; padding-block: 6px; }
.agent-link { display: inline-flex; align-items: center; gap: 8px; min-width: 0; max-width: 100%; color: var(--ink); text-decoration: none; }
.agent-link:focus-visible { box-shadow: var(--focus-ring); border-radius: 6px; }
.bot { position: relative; display: inline-grid; width: 30px; height: 30px; flex: none; }
.who { display: grid; min-width: 0; line-height: 1.25; }
.result { font-weight: 600; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.session-context { display: flex; align-items: center; gap: 6px; min-width: 0; color: var(--ink-3); font-size: 12px; font-weight: 450; }
.session-name { min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.row:hover .result { color: var(--teal-ink); }
.role { flex: none; height: 16px; padding: 0 5px; border-radius: 999px; background: var(--gold-wash); color: var(--gold-ink); font: 600 9px/16px var(--mono); letter-spacing: .06em; text-transform: uppercase; font-variant-ligatures: none; }
.c-exec { display: inline-flex; align-items: center; gap: 8px; min-width: 0; }
/* Marks differ in width (Claude narrow, xAI wide): a fixed slot keeps every row's text on one left edge. */
.exec-icon { display: grid; place-items: center; flex: none; width: 28px; height: 16px; }
.exec-icon :deep(svg) { max-width: 28px; height: auto; max-height: 14px; }
.exec-copy { display: grid; min-width: 0; line-height: 1.25; }
.exec-model, .exec-account { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.exec-model { font-size: 12.5px; color: var(--ink); }
.exec-account { font-size: 11.5px; color: var(--ink-3); }
/* The estimate follows the key on its line and wraps below it only when the column is narrow. */
.row > .c-ticket { display: flex; flex-wrap: wrap; align-items: center; align-content: center; gap: 3px 8px; padding-block: 6px; }
.row-eta { font-size: 12px; }
.ticket-chip { display: inline-flex; align-items: center; height: 22px; padding: 0 8px; border-radius: 6px; background: var(--chip-teal-bg); box-shadow: inset 0 0 0 1px var(--chip-teal-line); color: var(--teal-ink); font: 600 11.5px/1 var(--mono); text-decoration: none; font-variant-ligatures: none; white-space: nowrap; }
.ticket-chip:hover { filter: brightness(1.04); text-decoration: underline; }
.ticket-chip:focus-visible { box-shadow: var(--focus-ring); }
.row .c-beat { font-size: 12.5px; color: var(--ink-2); white-space: nowrap; }
.row .c-elapsed { font-size: 12px; color: var(--ink-2); white-space: nowrap; font-variant-numeric: tabular-nums; }
.faint { color: var(--ink-3); }
.mono-cell { font-family: var(--mono); font-variant-ligatures: none; }
/* Controls appear on the row the pointer or keyboard is on; the layout never
   shifts. The row's inset leaves ~66px of the 76px track: two 28px controls. */
.row > .c-actions { display: inline-flex; align-items: center; justify-content: flex-end; gap: 2px; padding: 0 4px 0 0; }
.act, .more { opacity: 0; transition: opacity .15s ease; }
.row:hover :is(.act, .more), .row.active :is(.act, .more), .row.selected :is(.act, .more), .row:focus-within :is(.act, .more), .more[aria-expanded="true"] { opacity: 1; }
.act[aria-disabled="true"] { color: var(--ink-3); cursor: not-allowed; }
.row:hover .act[aria-disabled="true"], .row.active .act[aria-disabled="true"], .row.selected .act[aria-disabled="true"] { opacity: .45; }
@media (hover: none) { .act { display: none; } .more { opacity: 1; } .c-actions .icon-btn.more { width: 36px; height: 36px; } }
@media (prefers-reduced-motion: reduce) { .act, .more { transition: none; } }
.menu-note { margin: -2px 10px 6px 36px; font-size: 11.5px; line-height: 1.35; color: var(--ink-3); }
.menu-sep { height: 1px; margin: 4px 6px; border: 0; background: var(--line); }
.menu-item { display: flex; align-items: flex-start; gap: 10px; width: 100%; padding: 8px 10px; border: 0; border-radius: 8px; background: transparent; color: var(--ink); font-size: 13.5px; text-align: left; }
.menu-item > svg { margin-top: 2px; color: var(--ink-2); flex-shrink: 0; }
.menu-item:hover:not([aria-disabled="true"]) { background: var(--row-hover); }
.menu-item:focus-visible { background: var(--row-selected); box-shadow: inset 0 0 0 1px var(--glass-rim); }
.menu-item.danger:not([aria-disabled="true"]), .menu-item.danger:not([aria-disabled="true"]) > svg { color: var(--danger); }
.menu-item[aria-disabled="true"] { color: var(--ink-3); cursor: not-allowed; }
.mi-text { display: grid; gap: 2px; min-width: 0; }
.mi-text small { font-size: 11.5px; color: var(--ink-3); line-height: 1.35; }
.state { display: grid; justify-items: center; gap: 8px; padding: 48px 24px 56px; text-align: center; color: var(--ink-2); border-top: 1px solid var(--line); }
.state > svg { color: var(--teal); margin-bottom: 4px; }
.state h3 { color: var(--ink); font-size: 16px; }
.state p { max-width: 52ch; font-size: 13.5px; }
.state .btn { margin-top: 8px; }
.skeleton-rows { display: grid; gap: 4px; padding: 8px 18px 16px; border-top: 1px solid var(--line); }
.sk-row { display: flex; align-items: center; gap: 18px; height: 40px; }
.sk-row .dot { width: 10px; height: 10px; border-radius: 50%; }
.sk-row .key { width: 70px; height: 20px; border-radius: 6px; }
/* Estimates need a ticket track wide enough for "overdue 5 min". */
.table.has-eta { grid-template-columns: var(--state-width) minmax(140px, 1.45fr) minmax(112px, .48fr) minmax(128px, .82fr) 80px 68px 76px; }
@container sessions (max-width: 980px) {
  .table { --state-width: 156px; grid-template-columns: var(--state-width) minmax(120px, 1.35fr) minmax(68px, .42fr) minmax(116px, .75fr) 72px 76px; }
  .table.has-eta { grid-template-columns: var(--state-width) minmax(120px, 1.35fr) minmax(104px, .42fr) minmax(116px, .75fr) 72px 76px; }
  .c-elapsed { display: none; }
}
@container sessions (max-width: 760px) {
  .table { --state-width: 150px; grid-template-columns: var(--state-width) minmax(100px, 1.2fr) minmax(64px, auto) minmax(108px, .7fr) 68px; }
  .table.has-eta { grid-template-columns: var(--state-width) minmax(100px, 1.2fr) minmax(100px, auto) minmax(108px, .7fr) 68px; }
  .c-beat, .c-elapsed { display: none; }
  .act { display: none; }
}
/* Phones: identity, execution and state stack; actions stay in the overflow menu. */
@container sessions (max-width: 560px) {
  .table { --tree-step: 20px; display: block; }
  .thead { display: none; }
  .group-row { display: block; margin: 12px 8px 2px; padding: 0 8px; }
  .row { --tree-joint: 32px; display: grid; grid-template-columns: auto minmax(0, 1fr) auto 44px; grid-template-areas: "agent agent beat actions" "exec exec exec actions" "state ticket ticket actions"; row-gap: 4px; column-gap: 8px; align-items: center; min-height: 72px; margin: 0 6px; padding: 10px 4px 10px calc(10px + var(--depth) * var(--tree-step)); }
  .row > span { padding: 0; }
  .c-agent { grid-area: agent; min-width: 0; }
  .row.worker .c-agent { padding-left: 0; }
  .row > .tree-lines { left: 25px; }
  .worker-toggle { min-height: 36px; padding-inline: 6px; }
  .worker-tools { flex-wrap: nowrap; white-space: nowrap; }
  .worker-tools .idle-count, .worker-tools > span[aria-hidden]:has(+ .idle-count) { display: none; }
  .c-state { grid-area: state; min-width: 0; }
  .c-ticket { grid-area: ticket; justify-self: start; min-width: 0; overflow: hidden; }
  .row > .c-ticket { padding-block: 0; }
  .c-exec { grid-area: exec; min-width: 0; }
  .c-beat { display: block; grid-area: beat; }
  .c-elapsed { display: none; }
  .row > .c-actions { grid-area: actions; grid-row: 1 / span 3; align-self: center; justify-content: center; padding: 0; }
  .act { display: none; }
  .c-actions .icon-btn.more { width: 44px; height: 44px; opacity: 1; }
}
</style>
