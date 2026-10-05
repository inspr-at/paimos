<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import type { ListItem } from '../../lib/api'
import { can } from '../../lib/authz'
import { queueable, queueTargets, type QueueTarget } from '../../lib/workQueue'
import { toast } from '../../lib/toast'
import { useSession } from '../../stores/session'
import { useWorkQueue } from '../../stores/workQueue'
import AppIcon from '../AppIcon.vue'
import FloatingPanel from './FloatingPanel.vue'
import PersonAvatar from './PersonAvatar.vue'
import QueueModel from './QueueModel.vue'
import QueueReadyPanel from './QueueReadyPanel.vue'
const props = defineProps<{ row: ListItem; projectId: string; anchor: HTMLElement; people: { value: string; label: string }[]; canAssign: boolean }>()
const emit = defineEmits<{ close: [restore: boolean]; choose: [value: string]; changed: [] }>()
const queue = useWorkQueue(), session = useSession()
const targets = ref<QueueTarget[]>([]), loading = ref(true), error = ref(''), fixing = ref(false)
const query = ref('')
const people = computed(() => props.people.filter(person => person.label.toLowerCase().includes(query.value.trim().toLowerCase())))
const allowed = computed(() => session.identity?.principal.kind === 'person' && can('run.create', props.projectId))
const eligible = computed(() => queueable(props.row)), gaps = computed(() => queue.gaps(props.row))
const entry = computed(() => queue.entry(props.projectId, props.row.id))
onMounted(async () => {
  if (!allowed.value || !eligible.value) { loading.value = false; return }
  try {
    const [catalog] = await Promise.all([queueTargets(props.row.id), queue.checkReadiness(props.row)])
    targets.value = catalog.items.sort((a, b) => Number(b.available) - Number(a.available) || Number(!!b.matches_preference) - Number(!!a.matches_preference))
  }
  catch (e) { error.value = e instanceof Error ? e.message : 'Targets could not be loaded.' }
  finally { loading.value = false }
})
async function add(target?: QueueTarget) {
  if (!allowed.value || !eligible.value || queue.busy) return
  try {
    if (!(await queue.checkReadiness(props.row)).ready) { fixing.value = true; return }
    await queue.add(props.projectId, props.row.id, target); toast(target ? `${props.row.key}: Start now on ${target.name} requested` : `${props.row.key} queued`); emit('changed'); emit('close', true)
  }
  catch (e) { toast(e instanceof Error ? e.message : 'The work could not be started.', { tone: 'error' }) }
}
function keys(event: KeyboardEvent) {
  const target = event.target as HTMLElement | null
  if (target?.isContentEditable || target?.closest('input, textarea, select')) return
  if (!['ArrowDown', 'ArrowUp', 'j', 'k'].includes(event.key)) return
  event.preventDefault(); event.stopPropagation()
  const items = [...(event.currentTarget as HTMLElement).querySelectorAll<HTMLButtonElement>('button:not(:disabled)')]
  const at = items.indexOf(document.activeElement as HTMLButtonElement)
  items[Math.max(0, Math.min(items.length - 1, at + (['ArrowDown', 'j'].includes(event.key) ? 1 : -1)))]?.focus()
}
</script>
<template>
  <QueueReadyPanel v-if="fixing" :row="row" :project-id="projectId" :anchor="anchor" @close="restore => emit('close', restore)" />
  <FloatingPanel v-else :anchor="anchor" :width="372" :tallest="580" :label="`Assignee of ${row.key}`" @close="restore => emit('close', restore)">
    <p class="menu-title eyebrow">Assignee · {{ row.key }}</p>
    <div class="menu" role="menu" :aria-label="`Assignee of ${row.key}`" @keydown="keys">
      <template v-if="row.is_leaf !== false">
      <button v-if="allowed && eligible && gaps.length" type="button" class="am-notready" @click="fixing = true"><AppIcon name="alert" :size="13" /><span>Not ready: {{ gaps.join(', ') }}</span><b>Fix</b></button>
      <button type="button" role="menuitemradio" class="menu-item am-item am-queue" :aria-checked="!!entry && !entry.target_agent_id" :disabled="!allowed || !eligible || queue.busy" @click="add()"><AppIcon name="queue" :size="14" /><span class="am-body"><b>Queue: next free agent</b><small>{{ !eligible ? 'Takes New, Open, Backlog or Blocked tickets' : 'Default: priority first, then first come' }}</small></span><AppIcon v-if="entry && !entry.target_agent_id" name="check" :size="14" /></button>
      <p v-if="entry" class="am-note">Choosing another route replaces its current queue place.</p>
      <p class="am-sec mono-label" role="presentation">Start now on…</p>
      <p v-if="loading" class="am-note" role="status">Loading agents…</p><p v-else-if="error" class="am-note" role="alert">{{ error }}</p><p v-else-if="!targets.length" class="am-note">No available target is configured.</p>
      <button v-for="target in targets" :key="`${target.agent_id}:${target.account_id}:${target.profile_id}`" type="button" role="menuitemradio" class="menu-item am-item" :aria-checked="entry?.target_agent_id === target.agent_id && entry?.model_profile_id === target.profile_id" :disabled="!allowed || !eligible || loading || gaps.length > 0 || queue.busy" @click="add(target)">
        <AppIcon name="agent" :size="15" /><span class="am-body"><span class="am-l1"><b>{{ target.name }}</b><QueueModel :model="target.model" :effort="target.effort" /></span><small>{{ target.account }} · <span :class="{ 'am-free': target.available }">{{ target.available ? 'Free now' : 'Waiting for capacity' }}</span> · {{ target.matches_preference ? 'Matches the preference' : 'Differs from the model preference' }}</small></span><span :class="target.available ? 'am-go' : 'am-wait'">{{ target.available ? 'Start now' : `#1 for ${target.name}` }}</span>
      </button>
      </template>
      <p class="am-sec mono-label" role="presentation">People</p>
      <input v-model="query" type="search" class="field am-search" aria-label="Find assignee" placeholder="Find a person…" :data-autofocus="!allowed ? '' : undefined" :disabled="!canAssign" @keydown.enter.stop.prevent="() => { if (canAssign && people.length === 1) emit('choose', people[0]!.value) }" />
      <button v-for="person in people" :key="person.value" type="button" role="menuitemradio" class="menu-item" :disabled="!canAssign" :aria-checked="(row.assignee?.id ?? '') === person.value" @click="emit('choose', person.value)"><PersonAvatar v-if="person.value" :id="person.value" :name="person.label" :size="18" /><AppIcon v-else name="user" :size="14" /><span class="label">{{ person.label }}</span><AppIcon v-if="(row.assignee?.id ?? '') === person.value" name="check" :size="14" /></button>
    </div>
  </FloatingPanel>
</template>
<style scoped>
.menu-title { padding: 6px 10px 4px; }
.menu { display: grid; gap: 1px; }
.menu-item { display: flex; align-items: center; gap: 10px; min-width: 0; min-height: 32px; padding: 0 10px; border: 0; border-radius: 8px; background: transparent; color: var(--ink); font-size: 13.5px; text-align: left; }
.menu-item:hover:not(:disabled) { background: var(--row-hover); }
.menu-item:focus-visible { background: var(--row-selected); box-shadow: inset 0 0 0 1px var(--glass-rim); }
.label, .am-body { flex: 1; min-width: 0; }
.am-item { min-height: 44px; padding: 6px 8px 6px 10px; }
.am-queue > svg { color: var(--teal-ink); }
.am-body { display: grid; gap: 1px; }
.am-l1 { display: flex; align-items: center; gap: 8px; }
small, .am-note { font-size: 11.5px; color: var(--ink-3); }
.am-note { padding: 4px 10px; }
.am-sec { padding: 8px 10px 3px; font-size: 10px; }
.am-search { margin: 2px 6px 4px; width: calc(100% - 12px); min-height: 28px; }
.am-go { flex: none; padding: 2px 9px; border-radius: 999px; background: var(--chip-teal-bg); box-shadow: inset 0 0 0 1px var(--chip-teal-line); color: var(--teal-ink); font-size: 11.5px; font-weight: 650; }
.am-wait { max-width: 100px; font: 500 11px/1.4 var(--mono); }
.am-free { color: var(--ok); }
.am-notready { display: flex; align-items: center; gap: 8px; min-height: 34px; padding: 0 10px; border: 0; border-radius: 8px; background: var(--queue-wait-bg); color: var(--queue-wait-ink); text-align: left; }
.am-notready span { flex: 1; }
</style>
