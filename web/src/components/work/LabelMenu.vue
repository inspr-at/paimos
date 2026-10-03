<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, ref } from 'vue'
import AppIcon from '../AppIcon.vue'
import FloatingPanel from './FloatingPanel.vue'

// Add and remove labels on the selected tickets. Each label shows whether all,
// some or none of them carry it; changes collect until Apply, then go as one
// change with one Undo. A new label is typed into the field.
export interface LabelChoice { name: string; color: string; on: number }
const props = defineProps<{ anchor: HTMLElement | null; labels: LabelChoice[]; count: number; busy?: boolean }>()
const emit = defineEmits<{ apply: [change: { add: { name: string; color?: string }[]; remove: string[] }]; close: [restoreFocus: boolean] }>()
const term = ref('')
const pending = ref(new Map<string, 'add' | 'remove'>())
const created = ref<LabelChoice[]>([])
const all = computed(() => [...created.value, ...props.labels])
const shown = computed(() => {
  const needle = term.value.trim().toLowerCase()
  return needle ? all.value.filter(label => label.name.toLowerCase().includes(needle)) : all.value
})
const exact = computed(() => all.value.some(label => label.name.toLowerCase() === term.value.trim().toLowerCase()))
const changes = computed(() => pending.value.size)
function state(label: LabelChoice): 'all' | 'some' | 'none' {
  const change = pending.value.get(label.name.toLowerCase())
  if (change === 'add') return 'all'
  if (change === 'remove') return 'none'
  return label.on >= props.count ? 'all' : label.on > 0 ? 'some' : 'none'
}
function toggle(label: LabelChoice) {
  const key = label.name.toLowerCase()
  const next = new Map(pending.value)
  const now = state(label)
  const original: 'all' | 'some' | 'none' = label.on >= props.count ? 'all' : label.on > 0 ? 'some' : 'none'
  const target = now === 'all' ? 'remove' : 'add'
  // Back to how it was: no change for this label.
  if ((target === 'add' && original === 'all') || (target === 'remove' && original === 'none')) next.delete(key)
  else next.set(key, target)
  pending.value = next
}
function create() {
  const name = term.value.trim()
  if (!name || exact.value) return
  const label = { name, color: '', on: 0 }
  created.value = [label, ...created.value]
  toggle(label)
  term.value = ''
}
function apply() {
  if (!changes.value || props.busy) return
  const add: { name: string; color?: string }[] = [], remove: string[] = []
  for (const label of all.value) {
    const change = pending.value.get(label.name.toLowerCase())
    if (change === 'add') add.push(label.color ? { name: label.name, color: label.color } : { name: label.name })
    else if (change === 'remove') remove.push(label.name)
  }
  emit('apply', { add, remove })
}
function keydown(event: KeyboardEvent) {
  if (event.key === 'Enter' && document.activeElement?.tagName === 'INPUT' && (event.target as HTMLElement).classList.contains('label-search')) {
    event.preventDefault()
    if (term.value.trim() && !exact.value) create()
    else if (shown.value[0] && term.value.trim()) { toggle(shown.value[0]); term.value = '' }
    else apply()
  }
}
</script>

<template>
  <FloatingPanel :anchor="anchor" :width="272" :tallest="520" :label="`Labels of ${count} tickets`" @close="restore => emit('close', restore)">
    <div class="label-menu" @keydown="keydown">
      <p class="eyebrow">Labels</p>
      <input v-model="term" class="field label-search" placeholder="Find or add a label…" aria-label="Find or add a label" data-autofocus />
      <div class="options" role="group" aria-label="Labels">
        <button v-if="term.trim() && !exact" type="button" class="option create" @click="create"><AppIcon name="plus" :size="13" />Add “{{ term.trim() }}”</button>
        <button
          v-for="label in shown" :key="label.name" type="button" role="checkbox" class="option" :aria-checked="state(label) === 'all' ? 'true' : state(label) === 'some' ? 'mixed' : 'false'"
          :class="{ changed: pending.has(label.name.toLowerCase()) }" @click="toggle(label)"
        >
          <span class="box" :class="state(label)" aria-hidden="true"><AppIcon v-if="state(label) === 'all'" name="check" :size="11" /><AppIcon v-else-if="state(label) === 'some'" name="minus" :size="11" /></span>
          <i class="tag-dot" :data-color="label.color || undefined" aria-hidden="true" />
          <span class="name">{{ label.name }}</span>
          <span class="on mono">{{ state(label) === 'some' ? `${label.on}/${count}` : '' }}</span>
        </button>
        <p v-if="!shown.length && !term.trim()" class="none">No labels in this project yet. Type one to add it.</p>
      </div>
      <div class="foot">
        <span class="summary">{{ changes ? `${changes} ${changes === 1 ? 'change' : 'changes'}` : `${count} selected` }}</span>
        <button type="button" class="btn sm primary" :disabled="!changes || busy" @click="apply"><AppIcon name="check" :size="13" />Apply</button>
      </div>
    </div>
  </FloatingPanel>
</template>

<style scoped>
.label-menu { display: grid; gap: 6px; padding: 2px 2px 4px; }
.label-menu .eyebrow { padding: 4px 8px 0; }
.label-search { height: 30px; font-size: 13px; }
.options { display: grid; gap: 1px; max-height: 300px; overflow: auto; }
.option { display: flex; align-items: center; gap: 9px; height: 32px; padding: 0 10px; border: 0; border-radius: 8px; background: transparent; color: var(--ink); font-size: 13.5px; text-align: left; }
@media (hover: hover) { .option:hover { background: var(--row-hover); } }
.option:focus-visible { background: var(--row-selected); box-shadow: inset 0 0 0 1px var(--glass-rim); }
.option.create { color: var(--teal-ink); font-weight: 600; }
.option.changed .name { font-weight: 600; }
.box { display: grid; place-items: center; flex-shrink: 0; width: 16px; height: 16px; border-radius: 5px; box-shadow: inset 0 0 0 1.5px var(--line-2); color: var(--button-ink); }
.box.all, .box.some { background: var(--teal); box-shadow: none; }
.name { flex: 1; min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.on { font-size: 11px; color: var(--ink-3); }
.none { padding: 8px 10px; font-size: 12.5px; color: var(--ink-3); }
.foot { display: flex; align-items: center; justify-content: space-between; gap: 8px; margin-top: 2px; padding: 8px 6px 0; border-top: 1px solid var(--line); }
.summary { font-size: 12px; color: var(--ink-2); }
.foot .btn { gap: 6px; }
.tag-dot { flex-shrink: 0; width: 8px; height: 8px; border-radius: 50%; background: var(--ink-3); }
.tag-dot[data-color="blue"] { background: #4f86c6; }
.tag-dot[data-color="red"] { background: #d0625b; }
.tag-dot[data-color="green"] { background: #4f9e6f; }
.tag-dot[data-color="yellow"], .tag-dot[data-color="orange"] { background: var(--gold); }
.tag-dot[data-color="purple"] { background: #8a6cc2; }
.tag-dot[data-color="teal"], .tag-dot[data-color="cyan"] { background: var(--teal); }
.tag-dot[data-color="pink"] { background: #c7679a; }
</style>
