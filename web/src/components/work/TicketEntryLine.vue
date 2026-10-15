<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import type { ListItem } from '../../lib/api'
import { entryLabel, entryLine } from '../../lib/entryChecks'
import { useEntryChecks, useEntryDefinitionLayout } from '../../lib/useEntryChecks'
import { absoluteTime } from '../../lib/work'
import { useSession } from '../../stores/session'
import AppIcon from '../AppIcon.vue'
import QueueReadyPanel from './QueueReadyPanel.vue'
const props = defineProps<{ row: ListItem; projectId: string }>()
const session = useSession(), checks = useEntryChecks(() => props.row), layout = useEntryDefinitionLayout()
// Unknown projects reserve the normal row from the first render. A successful
// empty result removes it and informs the next ticket's layout hint.
const reserve = computed(() => checks.error.value || checks.defined.value || (!checks.data.value && !layout.isEmpty(props.projectId)))
watch(checks.data, result => { if (result && props.row.is_leaf !== false) layout.remember(props.projectId, !!result.definition) }, { flush: 'sync' })
const line = computed(() => entryLine(checks.data.value, props.row.state))
const anchor = ref<HTMLElement | null>(null)
const waiting = computed(() => line.value?.block?.checks.filter(check => check.applicable && !check.passed && check.owner) ?? [])
const ownerText = computed(() => {
  const owner = waiting.value[0]?.owner
  return owner ? owner.id === session.identity?.principal.id ? 'waiting for you' : `waiting for ${owner.name}` : ''
})
const tip = computed(() => {
  const record = line.value?.record
  const missing = line.value?.block?.checks.filter(check => check.applicable && !check.passed).map(check => `${check.label}${check.owner ? ` · waiting for ${check.owner.name}` : ''}`).join(', ')
  const overridden = record?.override_reason ? `${record.by.name} · ${absoluteTime(record.at)} · ${record.override_reason}` : ''
  return [missing, overridden].filter(Boolean).join('\n')
})
watch(() => [props.row.id, session.identity?.principal.id], () => { anchor.value = null })
</script>
<template>
  <div v-if="row.is_leaf !== false && reserve" class="entry-slot" data-entry-slot>
    <button v-if="line?.block" type="button" class="entry-line" :class="{ passing: line.block.passed === line.block.total && !line.readonly }" :data-tip="tip" :aria-label="`${line.text}${ownerText ? ` · ${ownerText}` : ''}`" @click="anchor = $event.currentTarget as HTMLElement">
      <AppIcon :name="line.readonly && line.record?.override_reason ? 'skip' : line.block.passed === line.block.total ? 'circle-check' : 'list-checks'" :size="14" />
      <span>{{ line.text }}</span><span v-if="ownerText" class="waiting">· {{ ownerText }}</span>
      <span v-if="!line.readonly && line.record?.override_reason" class="entry-override"><span>·</span><AppIcon name="skip" :size="12" />{{ entryLabel(line.record.to_status) }} overridden</span>
      <AppIcon name="chevron-right" :size="12" />
    </button>
    <button v-else-if="checks.error.value" class="entry-line" type="button" :data-tip="checks.error.value" @click="checks.refresh()"><AppIcon name="list-checks" :size="14" />Checks unavailable · Retry</button>
    <QueueReadyPanel v-if="anchor && line?.block" :key="row.id" :row="row" :project-id="projectId" :anchor="anchor" :to-status="line.block.to_status" :mode="line.readonly ? 'record' : 'entry'" :entry-record="line.readonly ? line.record ?? undefined : undefined" @changed="checks.refresh()" @close="restore => { const target = anchor; anchor = null; if (restore) target?.focus() }" />
  </div>
</template>
<style scoped>
.entry-slot { height: 28px; min-width: 0; display: flex; align-items: center; }
.entry-line { display: inline-flex; align-items: center; gap: 6px; max-width: 100%; height: 28px; padding: 0; border: 0; background: transparent; color: var(--ink-2); font-size: 12px; white-space: nowrap; }
.entry-line > svg { flex: none; }
.waiting, .entry-override { min-width: 0; overflow: hidden; text-overflow: ellipsis; }
.entry-line:hover { color: var(--ink); }
.entry-line.passing > svg:first-child { color: var(--ok); }
.entry-override { display: inline-flex; align-items: center; gap: 4px; color: var(--ink-3); }
@media (pointer: coarse) { .entry-slot, .entry-line { height: 44px; } }
@media (max-width: 600px) { .entry-line { font-size: 11.5px; } }
</style>
