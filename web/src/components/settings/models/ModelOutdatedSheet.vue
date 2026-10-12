<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import AppIcon from '../../AppIcon.vue'
import HarnessMark from '../../agents/HarnessMark.vue'
import AccessSheet from '../../access/AccessSheet.vue'
import { getMembers } from '../../../lib/access'
import { can } from '../../../lib/authz'
import { listWorkKinds } from '../../../lib/workKinds'
import { useSession } from '../../../stores/session'
import { SOURCE_NAME, SOURCE_TIP, atLevel, outdatedUseText, peopleInUses, type FactSource, type OutdatedEntry } from '../../../lib/modelRegistry'

// Settings › Models › Model registry › Review… (AEON-1164). A sheet over the page:
// every old version with why it is old, a box each, every use that moves with it,
// and one button. Nothing is written until that button; the page under the sheet
// never moves. Focus stays inside and Esc closes (the shared sheet does both).
const props = defineProps<{ entries: OutdatedEntry[]; autoOn: boolean; incomplete: boolean; more: boolean; busy: boolean }>()
const emit = defineEmits<{ close: []; remove: [versions: string[]] }>()
const session = useSession()
const me = computed(() => session.identity?.principal.id ?? '')

// Every old version starts selected. When the list is read again, choices stay for the versions still listed.
const selected = ref(new Set(props.entries.map(entry => entry.version)))
watch(() => props.entries.map(entry => entry.version).join('\u0000'), () => {
  const listed = new Set(props.entries.map(entry => entry.version))
  selected.value = new Set([...selected.value].filter(version => listed.has(version)))
})
function toggle(version: string, event: Event) {
  const next = new Set(selected.value)
  if ((event.target as HTMLInputElement).checked) next.add(version); else next.delete(version)
  selected.value = next
}
const chosen = computed(() => props.entries.filter(entry => selected.value.has(entry.version)))
const hidden = computed(() => props.incomplete || chosen.value.some(entry => entry.incomplete))

// Kind labels and people's names are a courtesy: without them a use still says what moves.
const kinds = ref<Record<string, string>>({}), names = ref<ReadonlyMap<string, string>>(new Map())
const controller = new AbortController()
onMounted(() => {
  void listWorkKinds(controller.signal).then(found => { if (!controller.signal.aborted) kinds.value = Object.fromEntries(found.items.map(kind => [kind.slug, kind.label])) }).catch(() => {})
  const people = peopleInUses(props.entries.flatMap(entry => entry.uses)).filter(id => id !== me.value)
  if (people.length && can('members.read')) {
    void getMembers().then(members => { if (!controller.signal.aborted && Array.isArray(members?.people)) names.value = new Map(members.people.map(person => [person.principal_id, person.name] as const)) }).catch(() => {})
  }
})
onBeforeUnmount(() => controller.abort())
// Each use moves from the old version to its successor, at the level it runs now and the nearest one it lands on
// ("Opus 5 · xhigh → Opus 5.5 · xhigh"). The server names both by identity and sends the levels (AEON-1163).
const moving = computed(() => chosen.value.flatMap(entry => entry.uses.map((use, index) => ({
  key: `${entry.version}\u0000${index}`, from: atLevel(entry.name, use.from_effort), to: atLevel(use.to && use.to !== entry.successor ? use.to : entry.successorName, use.to_effort),
  ...outdatedUseText(use, kinds.value, names.value, me.value),
}))))

const SOURCE_ICON: Record<FactSource, 'box' | 'server' | 'terminal' | 'edit'> = { shipped: 'box', provider: 'server', harness: 'terminal', manual: 'edit' }
const none = computed(() => {
  const n = chosen.value.length
  if (!n) return 'Select an old version to see what moves.'
  return hidden.value ? 'Nothing you can see uses them.' : `Nothing uses ${n === 1 ? 'it' : 'them'}.`
})
const note = computed(() => {
  const parts = ['Daily caps belong to accounts, not models, so none move.']
  const used = props.entries.some(entry => entry.uses.length > 0)
  if (!used && props.autoOn) parts.push('Auto-update is on, so uses already moved by themselves; this only removes the old versions.')
  else if (used && !props.autoOn) parts.push('Auto-update is off, which is why these are still in use.')
  if (hidden.value) parts.push('Other people’s choices are not shown; they move too.')
  if (props.more) parts.push(`This shows the first ${props.entries.length}; the rest appear once these are removed.`)
  return parts.join(' ')
})
// The button keeps one size for every count, so the actions beside it never move.
const goLabel = computed(() => chosen.value.length ? `Remove ${chosen.value.length} old version${chosen.value.length === 1 ? '' : 's'}` : 'Nothing selected')
const widest = computed(() => `Remove ${props.entries.length} old versions`)
function go() {
  if (props.busy || !chosen.value.length) return
  emit('remove', chosen.value.map(entry => entry.version))
}
</script>

<template>
  <AccessSheet title="Remove old versions" scale="m" @close="emit('close')">
    <div class="od-sheet" data-outdated-sheet>
      <p class="od-lead">Each has a newer version. Everything that uses one moves to its successor first, at the nearest level, then the old version is removed.</p>
      <h3 class="od-sub">Old versions · {{ entries.length }}</h3>
      <ul class="od-list" aria-label="Old versions">
        <li v-for="(entry, index) in entries" :key="entry.version" class="od" :data-od-row="entry.version">
          <label class="od-l">
            <input type="checkbox" class="check-box" :checked="selected.has(entry.version)" :data-autofocus="index === 0 ? '' : undefined" :data-od="entry.version" @change="toggle(entry.version, $event)">
            <span class="od-main">
              <span class="od-n">
                <span v-if="entry.harness" class="od-mark" aria-hidden="true"><HarnessMark :harness="entry.harness" :provider="entry.route" :size="13" /></span>
                <b>{{ entry.name }}</b><AppIcon name="arrow" :size="12" class="od-ar" /><span class="sr-only">replaced by</span><b>{{ entry.successorName }}</b>
              </span>
              <span v-if="entry.routes.length" class="od-r">{{ entry.routes.join(' · ') }}</span>
              <span class="od-why">{{ entry.why }}<span v-if="entry.source" class="srcb" :data-tip="SOURCE_TIP[entry.source]"><AppIcon :name="SOURCE_ICON[entry.source]" :size="11" />{{ SOURCE_NAME[entry.source] }}</span></span>
            </span>
          </label>
        </li>
      </ul>
      <h3 class="od-sub">What moves · {{ moving.length }}</h3>
      <ul class="rf-list" aria-label="What moves">
        <li v-for="item in moving" :key="item.key" class="rf">
          <span class="rf-w"><b>{{ item.title }}</b><small>{{ item.who }}</small></span>
          <span class="rf-c">{{ item.from }}<AppIcon name="arrow" :size="12" class="od-ar" /><span class="sr-only">moves to</span><b>{{ item.to }}</b></span>
        </li>
        <li v-if="!moving.length" class="rf none">{{ none }}</li>
      </ul>
      <p class="od-note"><AppIcon name="info" :size="13" /><span>{{ note }}</span></p>
    </div>
    <template #foot>
      <button type="button" class="btn ghost od-keep" data-od-keep @click="emit('close')">Keep them</button>
      <button type="button" class="btn primary" data-od-go :disabled="!chosen.length" :aria-busy="busy" @click="go">
        <span class="btn-label od-go-label"><span>{{ goLabel }}</span><span aria-hidden="true">{{ widest }}</span><span aria-hidden="true">Nothing selected</span></span>
      </button>
    </template>
  </AccessSheet>
</template>

<style scoped>
.od-sheet { container-type: inline-size; }
.od-lead { font-size: 13px; line-height: 1.55; color: var(--ink-2); }
.od-sub { margin: 20px 0 6px; font: 500 10.5px/1.5 var(--mono); letter-spacing: .14em; text-transform: uppercase; color: var(--ink-3); }
.od-list, .rf-list { display: grid; list-style: none; margin: 0; padding: 0; }
.od + .od, .rf + .rf { border-top: 1px solid var(--line); }
.od-l { display: grid; grid-template-columns: 20px minmax(0, 1fr); gap: 12px; padding: 12px 4px; cursor: pointer; }
.od-l .check-box { margin-top: 3px; }
.od-main { display: grid; gap: 3px; min-width: 0; }
.od-n { display: flex; flex-wrap: wrap; align-items: center; gap: 4px 8px; font-size: 13.5px; color: var(--ink); }
.od-n b { font-weight: 600; overflow-wrap: anywhere; }
.od-mark { display: grid; place-items: center; width: 22px; height: 22px; border-radius: 7px; background: var(--surface-raised); box-shadow: inset 0 0 0 1px var(--line); }
.od-ar { flex: none; color: var(--ink-3); }
.od-r { font: 500 11.5px/1.45 var(--mono); color: var(--ink-3); overflow-wrap: anywhere; }
.od-why { display: flex; flex-wrap: wrap; align-items: center; gap: 4px 8px; font-size: 12.5px; line-height: 1.5; color: var(--ink-2); }
.srcb { display: inline-flex; align-items: center; gap: 4px; flex: none; height: 20px; padding: 0 7px; border-radius: 999px; box-shadow: inset 0 0 0 1px var(--line-2); font-size: 11px; font-weight: 500; color: var(--ink-2); white-space: nowrap; cursor: help; }
.srcb :deep(svg) { color: var(--ink-3); }
.rf { display: grid; grid-template-columns: minmax(0, 1fr) minmax(0, 1.2fr); gap: 12px; padding: 10px 4px; font-size: 12.5px; line-height: 1.5; }
.rf-w { display: grid; min-width: 0; }
.rf-w b { font-weight: 600; color: var(--ink); overflow-wrap: anywhere; }
.rf-w small { font-size: 12px; color: var(--ink-3); }
.rf-c { min-width: 0; color: var(--ink-2); overflow-wrap: anywhere; }
.rf-c b { font-weight: 600; color: var(--ink); }
.rf-c .od-ar { display: inline-block; vertical-align: -1px; margin: 0 4px; }
.rf.none { display: block; color: var(--ink-3); }
.od-note { display: flex; gap: 8px; margin-top: 18px; padding: 10px 12px; border-radius: 10px; background: var(--surface-sunken); font-size: 12px; line-height: 1.5; color: var(--ink-2); }
.od-note > :first-child { flex: none; margin-top: 2px; color: var(--ink-3); }
.od-go-label { font-variant-numeric: tabular-nums; }
@container (max-width: 480px) { .rf { grid-template-columns: minmax(0, 1fr); gap: 2px; } }
@media (pointer: coarse) {
  .od-l { min-height: 44px; }
  .od-l .check-box { width: 20px; height: 20px; margin-top: 1px; }
}
/* Phones: the shared sheet pins its bar at the bottom in reverse; Keep them stays on the left, Remove on the right. */
@media (max-width: 600px) { .od-keep { order: 1; } }
</style>
<style scoped src="../../../styles/settingsButtons.css"></style>
