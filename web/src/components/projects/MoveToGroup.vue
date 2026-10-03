<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, ref, useId, watch } from 'vue'
import { MAX_GROUP_NAME, type GroupDef } from '../../lib/projectGroups'
import { highlight } from '../../lib/work'
import AppIcon from '../AppIcon.vue'
import FloatingPanel from '../work/FloatingPanel.vue'
import GroupMarker from './GroupMarker.vue'

export interface MoveOption { group: GroupDef; count: number; current: boolean; reason?: string }

// "Move to group": type to narrow the groups, arrows to pick, Enter to move.
// A name no group has yet offers to create that group and move there.
const props = defineProps<{ anchor: HTMLElement | null; subject: string; options: MoveOption[] }>()
const emit = defineEmits<{ choose: [id: string]; create: [name: string]; close: [restoreFocus: boolean] }>()
const id = useId()
const term = ref('')
const shown = computed(() => {
  const needle = term.value.trim().toLowerCase()
  return needle ? props.options.filter(option => option.group.name.toLowerCase().includes(needle)) : props.options
})
const newName = computed(() => {
  const name = term.value.trim()
  if (!name || [...name].length > MAX_GROUP_NAME) return ''
  return props.options.some(option => option.group.name.toLowerCase() === name.toLowerCase()) ? '' : name
})
const count = computed(() => shown.value.length + (newName.value ? 1 : 0))
const firstFree = () => Math.max(0, shown.value.findIndex(option => !option.current && !option.reason))
const active = ref(firstFree())
watch(term, () => { active.value = shown.value.length ? firstFree() : 0 })
function choose(index: number) {
  active.value = index
  if (index === shown.value.length && newName.value) { emit('create', newName.value); return }
  const option = shown.value[index]
  if (!option || option.current || option.reason) return
  emit('choose', option.group.id)
}
function keydown(event: KeyboardEvent) {
  if (event.key === 'ArrowDown' || event.key === 'ArrowUp') {
    event.preventDefault()
    if (count.value) active.value = (active.value + (event.key === 'ArrowDown' ? 1 : -1) + count.value) % count.value
  } else if (event.key === 'Enter') {
    event.preventDefault()
    choose(active.value)
  }
}
const optionId = (index: number) => `${id}-option-${index}`
</script>

<template>
  <FloatingPanel :anchor="anchor" :width="320" :label="`Move ${subject} to a group`" @close="restore => emit('close', restore)">
    <p class="menu-title eyebrow">Move {{ subject }} to</p>
    <input
      v-model="term" class="field picker-search" type="text" placeholder="Group name" aria-label="Find or name a group" role="combobox" autocomplete="off" spellcheck="false"
      :aria-controls="`${id}-options`" aria-expanded="true" aria-autocomplete="list" :aria-activedescendant="count ? optionId(active) : undefined" data-autofocus @keydown="keydown"
    />
    <div :id="`${id}-options`" class="options" role="listbox" :aria-label="`Groups for ${subject}`">
      <button
        v-for="(option, index) in shown" :id="optionId(index)" :key="option.group.id" type="button" role="option" class="option" tabindex="-1"
        :class="{ off: option.current || !!option.reason }" :aria-selected="active === index" :aria-disabled="option.current || !!option.reason ? 'true' : undefined"
        :aria-description="option.reason"
        @click="choose(index)" @pointermove="active = index"
      >
        <GroupMarker :group="option.group" />
        <span class="text">
          <span v-clip-tip="option.group.name" class="name"><template v-for="(part, i) in highlight(option.group.name, term)" :key="i"><mark v-if="part.match">{{ part.text }}</mark><template v-else>{{ part.text }}</template></template></span>
        </span>
        <AppIcon v-if="option.group.kind === 'shared'" name="users" :size="13" class="shared" />
        <span v-if="option.current" class="current">Here now</span>
        <span v-else class="count mono">{{ option.count }}</span>
      </button>
      <button
        v-if="newName" :id="optionId(shown.length)" type="button" role="option" class="option create" tabindex="-1" :aria-selected="active === shown.length"
        @click="choose(shown.length)" @pointermove="active = shown.length"
      >
        <AppIcon name="plus" :size="13" /><span class="text"><span v-clip-tip="`Create group “${newName}”`" class="name">Create group “{{ newName }}”</span></span>
      </button>
      <p v-if="!count" class="note">{{ term.trim() ? 'A group name can be up to 60 characters.' : 'No groups yet.' }}</p>
    </div>
    <p class="option-note" aria-live="polite">{{ shown[active]?.reason }}</p>
    <p class="fine"><kbd class="keycap">Enter</kbd> moves · type a new name to create a group</p>
  </FloatingPanel>
</template>

<style scoped>
.menu-title { padding: 6px 10px 4px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.picker-search { height: 34px; margin: 0 0 6px; font-size: 13.5px; }
.options { display: grid; grid-template-columns: minmax(0, 1fr); gap: 1px; max-height: 320px; overflow: auto; }
.option { display: flex; align-items: center; gap: 10px; height: 40px; padding: 0 10px; border: 0; border-radius: 8px; background: transparent; color: var(--ink); font-size: 13.5px; text-align: left; }
.option[aria-selected="true"] { background: var(--row-selected); }
.option.off { color: var(--ink-2); cursor: default; }
.text { flex: 1; display: grid; min-width: 0; }
.name { min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.option-note { height: 4.05em; overflow: auto; margin: 4px 10px; font-size: 12px; color: var(--ink-3); line-height: 1.35; overflow-wrap: anywhere; }
.shared, .count, .current, .option > svg { flex: none; }
.shared { color: var(--ink-3); }
.count { min-width: 2ch; font-size: 11.5px; color: var(--ink-3); text-align: right; }
.current { font-size: 11.5px; color: var(--ink-3); white-space: nowrap; }
.create { color: var(--teal-ink); font-weight: 600; }
.create svg { color: var(--teal-ink); }
.note { padding: 8px 10px; font-size: 13px; color: var(--ink-3); }
.fine { display: flex; align-items: center; gap: 6px; margin-top: 4px; padding: 8px 10px 4px; border-top: 1px solid var(--line); font-size: 11.5px; color: var(--ink-3); }
@media (pointer: coarse), (max-width: 600px) { .fine { display: none; } .option { min-height: 44px; } .picker-search { height: 44px; font-size: 16px; } }
@media (max-width: 720px) {
  .option { height: 60px; }
  .name { height: 2.8em; line-height: 1.4; white-space: normal; overflow-wrap: anywhere; display: -webkit-box; -webkit-box-orient: vertical; -webkit-line-clamp: 2; }
}
</style>
