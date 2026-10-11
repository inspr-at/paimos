<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed } from 'vue'
import { ROUTINE_STATE_WORDS, recurrenceName, recurrenceZone, routineState, when, type Recurrence } from '../../lib/recurrences'
import { STATE_LOOK, nextWords, rowNote, scopeOf, triggerShort, whoWords } from '../../lib/routineRuns'
import AppIcon from '../AppIcon.vue'

// One row per routine on Agents › Routines (AEON-1192): the kind glyph, name and scope,
// the next run and the derived state with its reason. The whole row opens the logbook.
const props = defineProps<{ item: Recurrence; selected: boolean; me: string; ownerName?: string; projectKey: string }>()
const emit = defineEmits<{ select: [] }>()
const agent = computed(() => !!props.item.definition?.assignment)
const scope = computed(() => scopeOf(props.item))
const zone = computed(() => recurrenceZone(props.item.trigger))
const state = computed(() => routineState(props.item))
const look = computed(() => STATE_LOOK[state.value])
const at = (value: string) => when(value, zone.value)
const note = computed(() => rowNote(props.item, { me: props.me, ownerName: props.ownerName, project: props.projectKey, when: at }))
</script>

<template>
  <button type="button" class="rt-row" :data-routine="item.id" :aria-current="selected ? 'true' : undefined" @click="emit('select')">
    <span class="glyph" :class="agent ? 'agent' : 'ticket'" aria-hidden="true"><AppIcon :name="agent ? 'agent' : 'repeat'" :size="agent ? 15 : 14" /></span>
    <span class="rt-n">
      <span class="l1"><b v-clip-tip>{{ recurrenceName(item) }}</b><span class="sc" :class="scope === 'project' ? 'proj' : scope === 'personal' ? 'you' : 'ws'">{{ scope === 'project' ? projectKey || 'Project' : scope === 'personal' ? 'Yours' : 'Workspace' }}</span></span>
      <small v-clip-tip>{{ triggerShort(item.trigger) }} · {{ whoWords(item) }}</small>
    </span>
    <span v-clip-tip class="rt-next">{{ nextWords(item, at) }}</span>
    <span class="rt-st">
      <span class="stc" :class="look.tone"><span v-if="look.icon === 'live'" class="pulse" aria-hidden="true" /><AppIcon v-else :name="look.icon" :size="12" /><span>{{ ROUTINE_STATE_WORDS[state] }}</span></span>
      <small v-clip-tip>{{ note }}</small>
    </span>
    <AppIcon class="chev" name="chevron-right" :size="14" />
  </button>
</template>

<style scoped>
.rt-row { display: grid; grid-template-columns: 32px minmax(0, 1fr) 124px 180px 14px; align-items: center; gap: 12px; width: 100%; min-height: 64px; margin-top: 2px; padding: 8px 10px; border: 0; border-radius: 12px; background: transparent; color: var(--ink); text-align: left; font: inherit; cursor: pointer; }
@media (hover: hover) { .rt-row:hover { background: var(--row-hover); } }
.rt-row[aria-current="true"] { background: var(--row-selected); box-shadow: inset 0 0 0 1px var(--chip-teal-line); }
.rt-row:focus-visible { outline: none; box-shadow: var(--focus-ring); }
.glyph { display: grid; place-items: center; width: 32px; height: 32px; border-radius: 10px; }
.glyph.agent { color: var(--teal-ink); background: var(--chip-teal-bg); box-shadow: inset 0 0 0 1px var(--chip-teal-line); }
.glyph.ticket { color: var(--marker-on); background: var(--marker); }
.rt-n { display: grid; gap: 2px; min-width: 0; }
.l1 { display: flex; align-items: center; gap: 8px; min-width: 0; }
.l1 b { min-width: 0; font-size: 13.5px; font-weight: 600; white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
.rt-n small, .rt-st small { min-width: 0; max-width: 100%; font-size: 12px; color: var(--ink-3); white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
.rt-st small { font-size: 11.5px; }
.sc { flex: none; display: inline-flex; align-items: center; height: 18px; padding: 0 6px; border-radius: 5px; font: 600 9.5px/1 var(--mono); letter-spacing: .08em; text-transform: uppercase; color: var(--ink-2); background: var(--surface-sunken); box-shadow: inset 0 0 0 1px var(--line); }
.sc.proj { color: color-mix(in srgb, var(--level-project) 60%, var(--ink)); background: color-mix(in srgb, var(--level-project) 12%, transparent); box-shadow: inset 0 0 0 1px color-mix(in srgb, var(--level-project) 35%, transparent); }
.sc.you { color: var(--teal-ink); background: var(--chip-teal-bg); box-shadow: inset 0 0 0 1px var(--chip-teal-line); }
.rt-next { min-width: 0; font-size: 12.5px; color: var(--ink-2); font-variant-numeric: tabular-nums; white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
.rt-st { display: grid; gap: 3px; justify-items: start; min-width: 0; }
.stc { display: inline-flex; align-items: center; gap: 6px; height: 22px; padding: 0 8px; border-radius: 999px; font-size: 12px; font-weight: 600; white-space: nowrap; color: var(--ink-2); background: var(--surface-sunken); box-shadow: inset 0 0 0 1px var(--line); }
.stc svg { flex: none; }
.stc.wait { color: var(--queue-wait-ink); background: var(--queue-wait-bg); box-shadow: inset 0 0 0 1px var(--queue-wait-line); }
.stc.bad { color: var(--danger); background: var(--danger-bg); box-shadow: inset 0 0 0 1px var(--danger-line); }
.stc.live { color: var(--teal-ink); background: var(--chip-teal-bg); box-shadow: inset 0 0 0 1px var(--chip-teal-line); }
.chev { color: var(--ink-3); }
.pulse { position: relative; display: inline-block; flex: none; width: 8px; height: 8px; border-radius: 50%; background: var(--teal); }
@media (prefers-reduced-motion: no-preference) {
  .pulse::after { content: ''; position: absolute; inset: -3px; border-radius: 50%; box-shadow: 0 0 0 1.5px var(--teal); opacity: 0; animation: ring 1.8s ease-out infinite; }
  @keyframes ring { 0% { transform: scale(.6); opacity: .8; } 100% { transform: scale(1.5); opacity: 0; } }
}
/* Phones: the next run moves out; the state sits under the name. */
@container routines (max-width: 600px) {
  .rt-row { grid-template-columns: 32px minmax(0, 1fr) 14px; }
  .rt-next { display: none; }
  .rt-st { grid-column: 2; }
  .chev { grid-row: 1; grid-column: 3; }
}
</style>
