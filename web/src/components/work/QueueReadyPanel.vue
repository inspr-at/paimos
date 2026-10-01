<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, ref } from 'vue'
import type { ListItem } from '../../lib/api'
import { rowStore } from '../../lib/rowStore'
import { fixQueueReady, readyGaps, type ReadyGap } from '../../lib/workQueue'
import { useWorkQueue } from '../../stores/workQueue'
import { toast } from '../../lib/toast'
import AppIcon from '../AppIcon.vue'
import FloatingPanel from './FloatingPanel.vue'
const props = defineProps<{ row: ListItem; projectId: string; anchor: HTMLElement }>()
const emit = defineEmits<{ close: [restore: boolean] }>()
const queue = useWorkQueue()
const current = ref(props.row)
const gaps = computed(() => readyGaps(current.value))
const busy = ref(false), error = ref(''), naming = ref(false), blocker = ref('')
const lines: { id: ReadyGap; label: string; detail: string; action: string }[] = [
  { id: 'estimate', label: 'Estimate', detail: 'Nothing is dispatched without one', action: 'Suggest estimate' },
  { id: 'criteria', label: 'Acceptance criteria', detail: 'What must be true when it is done', action: 'Draft criteria' },
  { id: 'blocker', label: 'No unnamed blocker', detail: 'Blocked, but by what?', action: 'Name it' },
]
async function fix(kind: ReadyGap) {
  if (busy.value) return
  if (kind === 'blocker' && !naming.value) { naming.value = true; return }
  if (kind === 'blocker' && !blocker.value.trim()) { error.value = 'Name the ticket or reason blocking this work.'; return }
  busy.value = true; error.value = ''
  const sent = rowStore.mark()
  try { const updated = await fixQueueReady(props.row.id, kind, blocker.value.trim()); rowStore.wrote(updated, sent); current.value = { ...current.value, ...updated }; toast(`${props.row.key}: ${kind === 'estimate' ? 'estimate suggested' : kind === 'criteria' ? 'criteria drafted' : 'blocker named'}`) }
  catch (e) { error.value = e instanceof Error ? e.message : 'The fix could not be saved.' }
  finally { busy.value = false }
}
async function add() {
  if (gaps.value.length || busy.value) return
  busy.value = true; error.value = ''
  try { await queue.add(props.projectId, props.row.id); toast(`${props.row.key} queued`); emit('close', true) }
  catch (e) { error.value = e instanceof Error ? e.message : 'The ticket could not be queued.' }
  finally { busy.value = false }
}
</script>
<template>
  <FloatingPanel :anchor="anchor" :width="380" :label="`${row.key}: what is missing to queue it`" cycle @close="restore => emit('close', restore)">
    <div class="nr">
      <div class="nr-head"><p class="eyebrow">{{ gaps.length ? 'Not ready to queue' : 'Ready to queue' }}</p><span class="mono">{{ row.key }}</span></div>
      <p class="nr-lede">Queued work meets the definition of ready.</p>
      <ul class="nr-list"><li v-for="line in lines" :key="line.id" class="nr-item" :class="{ ok: !gaps.includes(line.id) }">
        <span class="nr-mark"><AppIcon :name="gaps.includes(line.id) ? 'alert' : 'check'" :size="13" /></span>
        <span class="nr-label">{{ line.label }}<small v-if="gaps.includes(line.id)">{{ line.detail }}</small></span>
        <button v-if="gaps.includes(line.id)" type="button" class="btn sm" :disabled="busy" @click="fix(line.id)"><AppIcon v-if="line.id !== 'blocker'" name="sparkle" :size="13" />{{ naming && line.id === 'blocker' ? 'Save blocker' : line.action }}</button>
        <input v-if="line.id === 'blocker' && naming" v-model="blocker" class="field" aria-label="Name the blocker" placeholder="Ticket key or blocking reason" :disabled="busy" @keydown.enter.prevent="fix('blocker')" />
      </li></ul>
      <p v-if="error" role="alert" class="error">{{ error }}</p>
      <div class="nr-foot"><span>{{ gaps.length ? 'Fix the lines above; Queue then works.' : 'All set: it goes in by priority.' }}</span><button type="button" class="btn sm primary" :disabled="busy || gaps.length > 0" @click="add"><AppIcon name="queue-add" :size="13" />Queue</button></div>
    </div>
  </FloatingPanel>
</template>
<style scoped>
.nr { display: grid; gap: 8px; padding: 4px; }
.nr-head, .nr-foot { display: flex; align-items: center; justify-content: space-between; gap: 8px; padding: 2px 6px; }
.nr-head .mono, .nr-foot { font-size: 11.5px; color: var(--ink-3); }
.nr-lede { padding: 0 6px; font-size: 12.5px; color: var(--ink-2); }
.nr-list { display: grid; gap: 2px; margin: 0; padding: 0; list-style: none; }
.nr-item { display: flex; flex-wrap: wrap; align-items: center; gap: 6px 9px; min-height: 38px; padding: 4px 6px; border-radius: 8px; font-size: 13px; }
.nr-item:not(.ok) { background: var(--surface-sunken); }
.nr-mark { display: grid; place-items: center; color: var(--warn-ink); }
.ok .nr-mark { color: var(--ok); }
.nr-label { flex: 1 1 120px; }
.nr-label small { display: block; font-size: 11.5px; color: var(--ink-3); }
.nr-foot { border-top: 1px solid var(--line); padding-top: 8px; }
.error { color: var(--danger); font-size: 12px; }
</style>
