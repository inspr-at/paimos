<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, watch } from 'vue'
import type { ListItem } from '../../lib/api'
import { releaseCell, type NativeReleaseView } from '../../lib/releaseMembership'
import { absoluteTime, kindLabel, priorityLabel, relativeTime, statusMeta } from '../../lib/work'
import AppIcon from '../AppIcon.vue'
import PersonAvatar from './PersonAvatar.vue'
import PriorityIcon from './PriorityIcon.vue'
import StatusIcon from './StatusIcon.vue'
import LiveDot from '../agents/LiveDot.vue'
import TicketHours from '../business/TicketHours.vue'
import { useAgents } from '../../stores/agents'
import { usePoller } from '../../lib/usePolledData'

// Status, priority, assignee and release (editable popovers), type (read-only),
// the parent epic, and estimate and dates only when they have values.
const props = defineProps<{ item: ListItem; editable: boolean; layout: 'row' | 'column'; now: number; releaseView?: NativeReleaseView; releaseEditable?: boolean }>()
const emit = defineEmits<{ status: [anchor: HTMLElement]; priority: [anchor: HTMLElement]; assignee: [anchor: HTMLElement]; epic: [anchor: HTMLElement]; release: [anchor: HTMLElement]; openParent: [key: string] }>()

function text(value: unknown): string { return typeof value === 'string' ? value.trim() : '' }
function day(value: unknown): string {
  const raw = text(value)
  if (!raw) return ''
  const time = Date.parse(raw.length === 10 ? `${raw}T12:00:00` : raw)
  return Number.isNaN(time) ? raw : new Date(time).toLocaleDateString('en-GB', { day: 'numeric', month: 'short', year: 'numeric' })
}
const estimate = computed(() => {
  const hours = props.item.fields.estimate_hours
  const points = props.item.fields.estimate_lp
  if (typeof hours === 'number' && hours > 0) return `${hours} h`
  if (typeof points === 'number' && points > 0) return `${points} pt`
  return ''
})
const start = computed(() => day(props.item.fields.start_date))
const due = computed(() => day(props.item.fields.end_date))
const releaseInfo = computed(() => releaseCell(props.releaseView))
const release = computed(() => releaseInfo.value.kind === 'member' ? releaseInfo.value.text : '')
// Agent sessions bound to this ticket, with their live state; refreshed while shown.
const agents = useAgents()
const bound = computed(() => agents.forTicket(props.item.id))
const refresh = usePoller(() => agents.ensureTicket(props.item.id), 20_000)
watch(() => props.item.id, id => { void agents.ensureTicket(id) }, { immediate: true })
onMounted(() => refresh.start())
onBeforeUnmount(() => refresh.stop())
const epicParent = computed(() => props.item.parent && props.item.parent.kind_slug !== 'project' ? props.item.parent : null)
const target = (event: Event) => event.currentTarget as HTMLElement
</script>

<template>
  <dl class="props" :class="layout">
    <div class="prop">
      <dt>Status</dt>
      <dd><button type="button" class="prop-btn" :disabled="!editable" aria-haspopup="menu" aria-keyshortcuts="s" :aria-label="`Status: ${statusMeta(item.state).label}. Change status`" @click="emit('status', target($event))"><StatusIcon :state="item.state" />{{ statusMeta(item.state).label }}<AppIcon v-if="editable" name="chevron" :size="12" class="chev" /></button></dd>
    </div>
    <div v-if="bound.length" class="prop agents-prop">
      <dt>Agents</dt>
      <dd class="agent-chips">
        <RouterLink
          v-for="view in bound" :key="view.session.id" class="prop-btn agent-chip" :class="view.status.group" :to="`/agents/${view.session.id}`"
          :aria-label="`${view.harness} ${view.name}: ${view.status.label}. Open the session`" :data-tip="`${view.harness} · ${view.status.label}`"
        ><LiveDot :tone="view.status.tone" :size="8" /><span class="agent-name">{{ view.name }}</span></RouterLink>
      </dd>
    </div>
    <div class="prop">
      <dt>Priority</dt>
      <dd><button type="button" class="prop-btn" :disabled="!editable" aria-haspopup="menu" aria-keyshortcuts="p" :aria-label="`Priority: ${priorityLabel(item.priority)}. Change priority`" @click="emit('priority', target($event))"><PriorityIcon v-if="item.priority" :priority="item.priority" /><span v-else class="dash">—</span><span :class="{ unset: !item.priority }">{{ item.priority ? priorityLabel(item.priority) : 'No priority' }}</span><AppIcon v-if="editable" name="chevron" :size="12" class="chev" /></button></dd>
    </div>
    <div class="prop">
      <dt>Assignee</dt>
      <dd><button type="button" class="prop-btn" :disabled="!editable" aria-haspopup="menu" aria-keyshortcuts="a" :aria-label="`Assignee: ${item.assignee?.name ?? 'nobody'}. Change assignee`" @click="emit('assignee', target($event))"><PersonAvatar v-if="item.assignee" :id="item.assignee.id" :name="item.assignee.name" :size="18" /><AppIcon v-else name="user" :size="13" class="faint" /><span :class="{ unset: !item.assignee }">{{ item.assignee?.name ?? 'Unassigned' }}</span><AppIcon v-if="editable" name="chevron" :size="12" class="chev" /></button></dd>
    </div>
    <div class="prop">
      <dt>Type</dt>
      <dd><span class="prop-static"><AppIcon :name="item.kind_slug === 'epic' ? 'epic' : item.kind_slug === 'task' ? 'task' : 'ticket'" :size="13" :class="['kind', item.kind_slug]" />{{ kindLabel(item.kind_slug) }}</span></dd>
    </div>
    <div v-if="item.kind_slug !== 'epic'" class="prop">
      <dt>{{ epicParent && epicParent.kind_slug !== 'epic' ? 'Parent' : 'Epic' }}</dt>
      <dd class="epic-cell">
        <button v-if="epicParent" type="button" class="prop-btn epic-chip" :data-tip="`Open ${epicParent.key}\n${epicParent.title}`" @click="emit('openParent', epicParent.key)">
          <AppIcon :name="epicParent.kind_slug === 'epic' ? 'epic' : 'ticket'" :size="12" :class="['kind', epicParent.kind_slug]" /><span class="mono">{{ epicParent.key }}</span><span class="epic-title">{{ epicParent.title }}</span>
        </button>
        <button v-else-if="editable" type="button" class="prop-btn ghost" aria-label="No epic. Choose an epic" @click="emit('epic', target($event))"><AppIcon name="epic" :size="12" class="faint" /><span class="unset">No epic</span></button>
        <span v-else class="prop-static faint">No epic</span>
      </dd>
    </div>
    <div v-if="estimate" class="prop"><dt>Estimate</dt><dd><span class="prop-static"><span v-if="layout === 'row'" class="inline-label">Estimate</span><span class="mono">{{ estimate }}</span></span></dd></div>
    <div v-if="start" class="prop"><dt>Start</dt><dd><span class="prop-static"><span v-if="layout === 'row'" class="inline-label">Start</span>{{ start }}</span></dd></div>
    <div v-if="due" class="prop"><dt>Due</dt><dd><span class="prop-static"><span v-if="layout === 'row'" class="inline-label">Due</span>{{ due }}</span></dd></div>
    <div v-if="item.kind_slug !== 'epic'" class="prop">
      <dt>Release</dt>
      <dd>
        <button v-if="releaseEditable" type="button" class="prop-btn" :class="{ ghost: releaseInfo.kind !== 'member' }" aria-haspopup="menu" aria-keyshortcuts="g" :aria-label="releaseInfo.kind === 'unknown' ? 'Release unknown. Change release' : `Release: ${release || 'none'}. Change release`" @click="emit('release', target($event))"><AppIcon name="layers" :size="13" /><span :class="{ unset: releaseInfo.kind !== 'member' }">{{ releaseInfo.kind === 'unknown' ? '—' : (release || 'No release') }}</span><AppIcon name="chevron" :size="12" class="chev" /></button>
        <span v-else class="prop-static" :class="{ faint: releaseInfo.kind !== 'member' }"><span v-if="layout === 'row'" class="inline-label">Release</span><AppIcon name="layers" :size="13" class="faint" /><span class="mono">{{ releaseInfo.kind === 'unknown' ? '—' : (release || 'No release') }}</span></span>
      </dd>
    </div>
    <div v-if="layout === 'column'" class="prop"><dt>Updated</dt><dd><time class="prop-static" :datetime="item.updated_at" :data-tip="absoluteTime(item.updated_at)">{{ relativeTime(item.updated_at, { now, long: true }) }}</time></dd></div>
    <div v-if="layout === 'column'" class="prop"><dt>Created</dt><dd><time class="prop-static" :datetime="item.created_at" :data-tip="absoluteTime(item.created_at)">{{ relativeTime(item.created_at, { now, long: true }) }}</time></dd></div>
    <!-- Last: logged hours arrive after the ticket, and nothing moves when they do. -->
    <TicketHours :node-id="item.id" :kind="item.kind_slug" :layout="layout" />
  </dl>
</template>

<style scoped>
.props { margin: 0; }
/* The panel reserves this row's height (hours arrive late); the chips keep together at its top rather than spreading over it. */
.props.row { display: flex; flex-wrap: wrap; align-content: flex-start; gap: 6px; }
@media (max-width: 600px) { .props.row { gap: 12px; } }
.props.row dt { position: absolute; width: 1px; height: 1px; overflow: hidden; clip-path: inset(50%); }
.props.row dd { margin: 0; }
/* A long epic title ends in an ellipsis inside the row; the chip never runs off the edge. */
.props.row .prop, .props.row dd { min-width: 0; max-width: 100%; }
.props.column { display: grid; gap: 2px; }
.props.column .prop { display: grid; grid-template-columns: 92px minmax(0, 1fr); align-items: center; min-height: 34px; }
.props.column dt { font: 500 10.5px/1 var(--mono); letter-spacing: .12em; text-transform: uppercase; color: var(--ink-3); font-variant-ligatures: none; }
.props.column dd { margin: 0; min-width: 0; }
.prop-btn, .prop-static { display: inline-flex; align-items: center; gap: 7px; max-width: 100%; height: 28px; padding: 0 11px 0 9px; border: 0; border-radius: 999px; font-size: 12.5px; color: var(--ink); white-space: nowrap; }
.row .prop-btn, .row .prop-static { background: var(--chip-bg); box-shadow: inset 0 0 0 1px var(--chip-line); }
.column .prop-btn, .column .prop-static { margin-left: -9px; background: transparent; }
.prop-btn { cursor: pointer; }
@media (hover: hover) { .prop-btn:not(:disabled):hover { background: var(--row-hover); box-shadow: inset 0 0 0 1px var(--glass-rim); } }
.prop-btn:active:not(:disabled) { background: var(--row-selected); }
.prop-btn:focus-visible { box-shadow: var(--focus-ring); }
.prop-btn:disabled { cursor: default; opacity: 1; }
.prop-btn[aria-expanded="true"] { background: var(--row-selected); }
.prop-btn.ghost { color: var(--ink-3); }
.chev { color: var(--ink-3); margin-left: -2px; }
.dash, .faint, .unset { color: var(--ink-3); }
.kind { color: var(--ink-3); }
.kind.epic { color: var(--gold); }
.agent-chips { display: flex; flex-wrap: wrap; gap: 6px; }
.row .agent-chips { flex-wrap: nowrap; }
.agent-chip { text-decoration: none; }
.agent-chip.needs { box-shadow: inset 0 0 0 1px rgba(214, 155, 49, .45); }
.agent-name { max-width: 16ch; overflow: hidden; text-overflow: ellipsis; font-weight: 600; }
.column .agent-chips { gap: 2px 10px; }
.epic-chip { max-width: 100%; }
.epic-chip .mono { font-size: 11px; color: var(--ink-2); }
.epic-title { min-width: 0; overflow: hidden; text-overflow: ellipsis; }
.row .epic-chip { max-width: 280px; }
.mono { font-family: var(--mono); font-size: 11.5px; font-variant-numeric: tabular-nums; font-variant-ligatures: none; }
.inline-label { font: 500 9.5px/1 var(--mono); letter-spacing: .12em; text-transform: uppercase; color: var(--ink-3); font-variant-ligatures: none; }
/* Phones: the chips wrap onto more lines; nothing scrolls sideways or is cut. */
@media (max-width: 720px) {
  .props.row { row-gap: 14px; margin-top: 14px; }
  .row .prop-btn, .row .prop-static { height: 34px; }
  .row .epic-chip { max-width: 100%; }
  .row .agent-chips { flex-wrap: wrap; }
}
</style>
