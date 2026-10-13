<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, nextTick, ref, watch } from 'vue'
import AppIcon from '../../AppIcon.vue'
import HarnessMark from '../../agents/HarnessMark.vue'
import { harnessLabel } from '../../../lib/agentState'
import { effortLevel, groupByProvider, nearestEffort, providerLabel, retiringText, type ModelEntry, type ProviderGroup, type RowView } from '../../../lib/modelsSimple'

// One combined picker per row (approved Models v2, AEON-1144): providers, then each model line in its newest
// version with its own levels in the route's native names; a provider's older versions fold behind one line.
// ↑↓ model, ←→ level, Enter, type to filter; clicking a level picks both. No model is filtered out by what it can
// do; a quiet line says what it cannot, and the row says what runs instead. Orders store lines, so an older version
// is shown for what it is and is never picked: its line always runs the newest version.
const props = defineProps<{ entries: ModelEntry[]; row: RowView; name: string; cant: (line: string) => string; fresh: Set<string>; lock?: boolean; locked?: boolean }>()
const emit = defineEmits<{ choose: [entry: ModelEntry, effort: string | null]; lock: [on: boolean]; grew: [] }>()
const query = ref(''), active = ref(props.row.line ?? ''), pending = ref<Record<string, string>>({}), live = ref('')
const olderOpen = ref(new Set<string>())
const coarse = typeof matchMedia === 'function' && matchMedia('(pointer: coarse)').matches
const list = ref<HTMLElement>()

const needle = computed(() => query.value.trim().toLowerCase())
const matches = (entry: ModelEntry) => !needle.value || [entry.name, entry.line, providerLabel(entry.provider), ...entry.routes.flatMap(route => [harnessLabel(route.harness), route.model, route.via])].join(' ').toLowerCase().includes(needle.value)
const groups = computed(() => groupByProvider(props.entries, matches))
// Typing searches older versions too; otherwise they wait behind "Older versions · N".
const olderShown = (group: ProviderGroup) => !!needle.value || olderOpen.value.has(group.provider)
type Item = { key: string; entry?: ModelEntry; older?: string }
const flat = computed<Item[]>(() => groups.value.flatMap(group => [
  ...group.entries.map(entry => ({ key: entry.key, entry })),
  ...(!group.older.length ? [] : olderShown(group) ? group.older.map(entry => ({ key: entry.key, entry })) : [{ key: `older:${group.provider}`, older: group.provider }]),
]))
const effortOf = (entry: ModelEntry) => pending.value[entry.key] ?? (entry.line === props.row.line ? props.row.effort : nearestEffort(entry, props.row.effort, effortLevel(props.row.entry, props.row.effort)))
const id = (key: string) => `pm-o-${key.replace(/[^a-z0-9]+/gi, '-')}`
const activeItem = computed(() => flat.value.find(item => item.key === active.value) ?? flat.value[0] ?? null)
// Keys and typing bring the active option into view; a hover never scrolls the list under the pointer (AEON-541).
const reveal = () => void nextTick(() => list.value?.querySelector('.opt.on')?.scrollIntoView({ block: 'nearest' }))
watch(flat, () => { if (!flat.value.some(item => item.key === active.value)) { active.value = (flat.value.find(item => item.key === props.row.line) ?? flat.value[0])?.key ?? ''; reveal() } }, { flush: 'sync' })

function move(step: number) {
  if (!flat.value.length) return
  const index = flat.value.findIndex(item => item.key === activeItem.value?.key)
  active.value = flat.value[(index + step + flat.value.length) % flat.value.length]!.key
  reveal()
}
function stepLevel(step: number) {
  const entry = activeItem.value?.entry
  if (!entry || entry.replacedBy || entry.efforts.length < 2) return
  const current = entry.efforts.findIndex(effort => effort.name === effortOf(entry)), next = Math.max(0, Math.min(entry.efforts.length - 1, current + step))
  if (next === current) return
  pending.value = { ...pending.value, [entry.key]: entry.efforts[next]!.name }; live.value = entry.efforts[next]!.name
}
function showOlder(provider: string) {
  olderOpen.value = new Set([...olderOpen.value, provider])
  const first = groups.value.find(group => group.provider === provider)?.older[0]
  if (first) { active.value = first.key; reveal() }
  live.value = `${groups.value.find(group => group.provider === provider)?.older.length ?? 0} older versions shown`
  // An opened fold may need more room than is left: the menu keeps its side and lets its list scroll, so the field stays put.
  void nextTick(() => emit('grew'))
}
const oldWords = (entry: ModelEntry) => `${entry.name} is an older version. Its line runs ${entry.replacedBy}; pick that to use it.`
function choose(entry: ModelEntry, effort?: string) {
  if (entry.replacedBy) { live.value = ''; void nextTick(() => { live.value = oldWords(entry) }); return }
  emit('choose', entry, effort ?? effortOf(entry))
}
function activate(item: Item | null) {
  if (item?.older) showOlder(item.older)
  else if (item?.entry) choose(item.entry)
}
function keys(event: KeyboardEvent) {
  if (event.isComposing) return
  if (event.key === 'ArrowDown' || event.key === 'ArrowUp') { event.preventDefault(); move(event.key === 'ArrowDown' ? 1 : -1) }
  else if ((event.key === 'ArrowLeft' || event.key === 'ArrowRight') && !query.value) { event.preventDefault(); stepLevel(event.key === 'ArrowRight' ? 1 : -1) }
  else if (event.key === 'Enter' && !event.metaKey && !event.ctrlKey) { event.preventDefault(); activate(activeItem.value) }
}
// An older version's name carries the levels it shows, which its row hides from screen readers (AEON-1240).
const levels = (entry: ModelEntry) => entry.efforts.length ? `, ${entry.efforts.length > 1 ? 'levels' : 'level'} ${entry.efforts.map(effort => effort.name).join(', ')}` : ''
const label = (entry: ModelEntry) => entry.replacedBy
  ? `${entry.name}, older version${levels(entry)}, replaced by ${entry.replacedBy}. Can’t be picked: its line runs ${entry.replacedBy}.`
  : `${entry.name}${entry.efforts.length > 1 ? `, level ${effortOf(entry)}` : ''}${props.cant(entry.line) ? `. ${props.cant(entry.line)}` : ''}`
const lowerFirst = (text: string) => text.charAt(0).toLowerCase() + text.slice(1)
const routes = (entry: ModelEntry) => entry.routes.map(route => harnessLabel(route.harness)).join(', ')
const second = (entry: ModelEntry) => entry.replacedBy
  ? [routes(entry), `replaced by ${entry.replacedBy}`].join(' · ')
  : [routes(entry), entry.route, entry.note, lowerFirst(retiringText(entry))].filter(Boolean).join(' · ')
</script>
<template>
  <input v-model="query" class="pm-q" type="text" role="combobox" aria-expanded="true" aria-controls="pm-list" aria-autocomplete="list" autocomplete="off" spellcheck="false" data-autofocus :aria-activedescendant="activeItem ? id(activeItem.key) : undefined" :placeholder="coarse ? 'Type to filter' : 'Type to filter · ←→ level'" :aria-label="`Model for ${name}`" @keydown="keys" />
  <div id="pm-list" ref="list" class="pm-l" role="listbox" aria-label="Models">
    <div v-for="group in groups" :key="group.provider" class="pm-g" role="group" :aria-label="group.label" :data-provider="group.provider">
      <p class="pm-gh" aria-hidden="true">{{ group.label }}</p>
      <template v-for="entry in [...group.entries, ...(olderShown(group) ? group.older : [])]" :key="entry.key">
        <p v-if="entry.replacedBy && entry.key === group.older[0]?.key" class="pm-oh" aria-hidden="true">Older versions</p>
        <div :id="id(entry.key)" class="opt" :class="{ on: activeItem?.key === entry.key, 'is-old': !!entry.replacedBy }" role="option" :data-line="entry.replacedBy ? undefined : entry.line" :data-old="entry.replacedBy ? entry.key : undefined" :aria-selected="!entry.replacedBy && entry.line === row.line" :aria-disabled="entry.replacedBy ? 'true' : undefined" :aria-label="label(entry)" @click="choose(entry)" @mousemove="active = entry.key">
          <span class="hg" aria-hidden="true"><HarnessMark :harness="entry.harness" :provider="entry.provider" :size="13" /></span>
          <span class="ot">
            <span class="t">{{ entry.name }}<span v-if="!entry.replacedBy && fresh.has(entry.line)" class="newt">New</span></span>
            <span v-if="!entry.replacedBy && cant(entry.line)" class="d cant"><AppIcon name="info" :size="12" />{{ routes(entry) }} · {{ cant(entry.line) }}</span>
            <span v-else class="d">{{ second(entry) }}</span>
          </span>
          <!-- An older version shows its levels as the current rows do (AEON-1240), only to read: it is never picked. -->
          <span v-if="entry.replacedBy && entry.efforts.length > 1" class="lvp" data-old-levels><span v-for="effort in entry.efforts" :key="effort.name" class="lvc" :data-eff="effort.name" aria-hidden="true">{{ effort.name }}</span></span>
          <span v-else-if="entry.replacedBy && entry.efforts.length" class="lvp one" data-old-levels>{{ entry.efforts[0]!.name }}</span>
          <span v-else-if="entry.replacedBy" />
          <span v-else-if="entry.efforts.length > 1" class="lvp"><span v-for="effort in entry.efforts" :key="effort.name" class="lvc" :data-eff="effort.name" aria-hidden="true" :data-on="effort.name === effortOf(entry) ? '' : undefined" @click.stop="choose(entry, effort.name)">{{ effort.name }}</span></span>
          <span v-else-if="entry.efforts.length" class="lvp one">{{ entry.efforts[0]!.name }}</span>
          <span v-else />
          <AppIcon class="ok" name="check" :size="15" />
        </div>
      </template>
      <div v-if="group.older.length && !olderShown(group)" :id="id(`older:${group.provider}`)" class="opt more" :class="{ on: activeItem?.key === `older:${group.provider}` }" role="option" aria-selected="false" :data-older="group.provider" :aria-label="`Show ${group.older.length} older ${group.label} ${group.older.length === 1 ? 'version' : 'versions'}`" @click="showOlder(group.provider)" @mousemove="active = `older:${group.provider}`">
        <span />
        <span class="t">Older versions · {{ group.older.length }}</span>
        <AppIcon name="chevron" :size="13" />
      </div>
    </div>
    <p v-if="!flat.length" class="pm-none">No model matches “{{ query }}”.</p>
  </div>
  <p class="sr-only" aria-live="polite">{{ live }}</p>
  <div v-if="lock" class="pm-lock"><span>Members can’t change this</span><label class="switch"><input type="checkbox" role="switch" :checked="locked" @change="emit('lock', ($event.target as HTMLInputElement).checked)" /><span class="sr-only">Lock for everyone</span></label></div>
</template>
<style scoped>
/* An older version's levels are only read (AEON-1240): no pointer, no hover fill. */
.opt.is-old .lvc, .opt.is-old .lvc:hover { cursor: default; color: var(--ink-3); background: transparent; }
</style>
