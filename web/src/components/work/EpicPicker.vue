<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { listNodes, type ListItem } from '../../lib/api'
import { highlight } from '../../lib/work'
import AppIcon from '../AppIcon.vue'
import FloatingPanel from './FloatingPanel.vue'
import StatusIcon from './StatusIcon.vue'

// Pick an epic of this project by searching key or title; never by typed IDs.
const props = defineProps<{ anchor: HTMLElement | null; projectId: string; current: string | null; subject: string; allowNone?: boolean }>()
const emit = defineEmits<{ choose: [epic: { id: string; key: string; title: string } | null]; close: [restoreFocus: boolean] }>()
const term = ref('')
const epics = ref<ListItem[]>([])
const loading = ref(true)
const failed = ref('')
const active = ref(0)
let timer: ReturnType<typeof setTimeout> | undefined
let generation = 0
async function search() {
  const request = ++generation
  loading.value = true; failed.value = ''
  try {
    const page = await listNodes({ within: props.projectId, kind: ['epic'], q: term.value.trim(), sort: 'state,-updated_at', limit: 50 })
    if (request !== generation) return
    epics.value = page.items; active.value = 0
  } catch (e) { if (request === generation) failed.value = e instanceof Error ? e.message : 'Epics could not be loaded' }
  finally { if (request === generation) loading.value = false }
}
watch(term, () => { clearTimeout(timer); timer = setTimeout(search, 160) })
onMounted(search)
onBeforeUnmount(() => { clearTimeout(timer); generation++ })
function keydown(event: KeyboardEvent) {
  const count = epics.value.length + (props.allowNone ? 1 : 0)
  if (event.key === 'ArrowDown' || event.key === 'ArrowUp') {
    event.preventDefault()
    active.value = Math.max(0, Math.min(count - 1, active.value + (event.key === 'ArrowDown' ? 1 : -1)))
  } else if (event.key === 'Enter') {
    event.preventDefault()
    const offset = props.allowNone ? 1 : 0
    if (props.allowNone && active.value === 0) emit('choose', null)
    else { const epic = epics.value[active.value - offset]; if (epic) emit('choose', { id: epic.id, key: epic.key, title: epic.title }) }
  }
}
</script>

<template>
  <FloatingPanel :anchor="anchor" :width="340" :label="`Epic for ${subject}`" @close="restore => emit('close', restore)">
    <p class="menu-title eyebrow">Epic</p>
    <input v-model="term" class="field picker-search" placeholder="Find an epic by key or title" aria-label="Find an epic" role="combobox" aria-controls="epic-options" :aria-expanded="true" :aria-activedescendant="`epic-option-${active}`" data-autofocus @keydown="keydown" />
    <div id="epic-options" class="options" role="listbox" aria-label="Epics">
      <button v-if="allowNone" id="epic-option-0" type="button" role="option" class="option" :aria-selected="active === 0" @click="emit('choose', null)" @pointermove="active = 0">
        <span class="none-mark" /><span class="title muted">No epic</span><AppIcon v-if="!current" name="check" :size="14" class="tick" />
      </button>
      <button
        v-for="(epic, index) in epics" :id="`epic-option-${index + (allowNone ? 1 : 0)}`" :key="epic.id" type="button" role="option" class="option"
        :aria-selected="active === index + (allowNone ? 1 : 0)" @click="emit('choose', { id: epic.id, key: epic.key, title: epic.title })" @pointermove="active = index + (allowNone ? 1 : 0)"
      >
        <StatusIcon :state="epic.state" :size="12" />
        <span class="key">{{ epic.key }}</span>
        <span class="title"><template v-for="(part, i) in highlight(epic.title, term)" :key="i"><mark v-if="part.match">{{ part.text }}</mark><template v-else>{{ part.text }}</template></template></span>
        <AppIcon v-if="epic.id === current" name="check" :size="14" class="tick" />
      </button>
      <p v-if="loading && !epics.length" class="note" role="status">Looking for epics…</p>
      <p v-else-if="failed" class="note error" role="alert">{{ failed }}</p>
      <p v-else-if="!epics.length" class="note">{{ term ? 'No epic matches.' : 'This project has no epics yet.' }}</p>
    </div>
  </FloatingPanel>
</template>

<style scoped>
.menu-title { padding: 6px 10px 4px; }
.picker-search { height: 32px; margin: 0 0 6px; font-size: 13px; }
.options { display: grid; grid-template-columns: minmax(0, 1fr); gap: 1px; }
.option { display: flex; align-items: center; gap: 9px; min-height: 34px; padding: 0 10px; border: 0; border-radius: 8px; background: transparent; color: var(--ink); font-size: 13px; text-align: left; }
.option[aria-selected="true"] { background: var(--row-selected); }
.key { flex-shrink: 0; font: 500 11px/1 var(--mono); color: var(--ink-2); font-variant-ligatures: none; }
.title { flex: 1; min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.muted { color: var(--ink-2); }
.tick { color: var(--teal); }
.none-mark { width: 12px; height: 2px; border-radius: 2px; background: var(--line-2); }
.note { padding: 8px 10px; font-size: 13px; color: var(--ink-3); }
.note.error { color: var(--danger); }
</style>
