<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, nextTick, ref, watch } from 'vue'
import AppIcon from '../../AppIcon.vue'
import HarnessMark from '../../agents/HarnessMark.vue'
import { harnessLabel } from '../../../lib/agentState'
import { effortLevel, groupByHarness, nearestEffort, retiringText, type ModelEntry, type RowView } from '../../../lib/modelsSimple'

// One combined picker per row: every model, grouped by harness, each with its own thinking levels in the model's
// native names. ↑↓ model, ←→ level, Enter, type to filter; clicking a level picks both. No model is filtered out by
// what it can do; a quiet line says what it cannot, and the row says what runs instead.
const props = defineProps<{ entries: ModelEntry[]; row: RowView; name: string; cant: (line: string) => string; fresh: Set<string>; lock?: boolean; locked?: boolean }>()
const emit = defineEmits<{ choose: [entry: ModelEntry, effort: string | null]; lock: [on: boolean] }>()
const query = ref(''), active = ref(props.row.line ?? ''), pending = ref<Record<string, string>>({}), live = ref('')
const coarse = typeof matchMedia === 'function' && matchMedia('(pointer: coarse)').matches
const list = ref<HTMLElement>()

const shown = computed(() => {
  const needle = query.value.trim().toLowerCase()
  return props.entries.filter(entry => !needle || `${entry.name} ${harnessLabel(entry.harness)} ${entry.route} ${entry.slug}`.toLowerCase().includes(needle))
})
const groups = computed(() => groupByHarness(shown.value))
const flat = computed(() => groups.value.flatMap(group => group.entries))
const effortOf = (entry: ModelEntry) => pending.value[entry.line] ?? (entry.line === props.row.line ? props.row.effort : nearestEffort(entry, props.row.effort, effortLevel(props.row.entry, props.row.effort)))
const id = (entry: ModelEntry) => `pm-o-${entry.line.replace(/[^a-z0-9]+/gi, '-')}`
const activeEntry = computed(() => flat.value.find(entry => entry.line === active.value) ?? flat.value[0] ?? null)
watch(flat, () => { if (!flat.value.some(entry => entry.line === active.value)) active.value = (flat.value.find(entry => entry.line === props.row.line) ?? flat.value[0])?.line ?? '' }, { flush: 'sync' })
watch(active, async () => { await nextTick(); list.value?.querySelector('.opt.on')?.scrollIntoView({ block: 'nearest' }) })

function move(step: number) {
  if (!flat.value.length) return
  const index = flat.value.findIndex(entry => entry.line === activeEntry.value?.line)
  active.value = flat.value[(index + step + flat.value.length) % flat.value.length]!.line
}
function stepLevel(step: number) {
  const entry = activeEntry.value
  if (!entry || entry.efforts.length < 2) return
  const current = entry.efforts.findIndex(effort => effort.name === effortOf(entry)), next = Math.max(0, Math.min(entry.efforts.length - 1, current + step))
  if (next === current) return
  pending.value = { ...pending.value, [entry.line]: entry.efforts[next]!.name }; live.value = entry.efforts[next]!.name
}
function choose(entry: ModelEntry, effort?: string) { emit('choose', entry, effort ?? effortOf(entry)) }
function keys(event: KeyboardEvent) {
  if (event.isComposing) return
  if (event.key === 'ArrowDown' || event.key === 'ArrowUp') { event.preventDefault(); move(event.key === 'ArrowDown' ? 1 : -1) }
  else if ((event.key === 'ArrowLeft' || event.key === 'ArrowRight') && !query.value) { event.preventDefault(); stepLevel(event.key === 'ArrowRight' ? 1 : -1) }
  else if (event.key === 'Enter' && !event.metaKey && !event.ctrlKey) { event.preventDefault(); if (activeEntry.value) choose(activeEntry.value) }
}
const label = (entry: ModelEntry) => `${entry.name}${entry.efforts.length > 1 ? `, thinking ${effortOf(entry)}` : ''}${props.cant(entry.line) ? `. ${props.cant(entry.line)}` : ''}`
const lowerFirst = (text: string) => text.charAt(0).toLowerCase() + text.slice(1)
const second = (entry: ModelEntry) => [harnessLabel(entry.harness), entry.route, entry.note, lowerFirst(retiringText(entry))].filter(Boolean).join(' · ')
</script>
<template>
  <input v-model="query" class="pm-q" type="text" role="combobox" aria-expanded="true" aria-controls="pm-list" aria-autocomplete="list" autocomplete="off" spellcheck="false" data-autofocus :aria-activedescendant="activeEntry ? id(activeEntry) : undefined" :placeholder="coarse ? 'Type to filter' : 'Type to filter · ←→ thinking'" :aria-label="`Model for ${name}`" @keydown="keys" />
  <div id="pm-list" ref="list" class="pm-l" role="listbox" aria-label="Models">
    <template v-for="(group, index) in groups" :key="group.harness">
      <div v-if="index" class="pm-sep" role="presentation" />
      <div role="group" :aria-label="group.label">
        <div v-for="entry in group.entries" :id="id(entry)" :key="entry.line" class="opt" :class="{ on: activeEntry?.line === entry.line }" role="option" :data-line="entry.line" :aria-selected="entry.line === row.line" :aria-label="label(entry)" @click="choose(entry)" @mousemove="active = entry.line">
          <span class="hg" aria-hidden="true"><HarnessMark :harness="entry.harness" :size="13" /></span>
          <span class="ot">
            <span class="t">{{ entry.name }}<span v-if="fresh.has(entry.line)" class="newt">New</span></span>
            <span v-if="cant(entry.line)" class="d cant"><AppIcon name="info" :size="12" />{{ harnessLabel(entry.harness) }} · {{ cant(entry.line) }}</span>
            <span v-else class="d">{{ second(entry) }}</span>
          </span>
          <span v-if="entry.efforts.length > 1" class="lvp"><span v-for="effort in entry.efforts" :key="effort.name" class="lvc" :data-eff="effort.name" aria-hidden="true" :data-on="effort.name === effortOf(entry) ? '' : undefined" @click.stop="choose(entry, effort.name)">{{ effort.name }}</span></span>
          <span v-else-if="entry.efforts.length" class="lvp one">{{ entry.efforts[0]!.name }}</span>
          <span v-else />
          <AppIcon class="ok" name="check" :size="15" />
        </div>
      </div>
    </template>
    <p v-if="!flat.length" class="pm-none">No model matches “{{ query }}”.</p>
  </div>
  <p class="sr-only" aria-live="polite">{{ live }}</p>
  <div v-if="lock" class="pm-lock"><span>Members can’t change this</span><label class="switch"><input type="checkbox" role="switch" :checked="locked" @change="emit('lock', ($event.target as HTMLInputElement).checked)" /><span class="sr-only">Lock for everyone</span></label></div>
</template>
