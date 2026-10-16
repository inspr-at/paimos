<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { nextTick, onMounted, ref } from 'vue'
import AppIcon from '../AppIcon.vue'
import FloatingPanel from './FloatingPanel.vue'

// Name a new view (with sharing), or rename one. Which view the project opens with is the
// view switch's default: right-click the saved view (AEON-1453).
const props = withDefaults(defineProps<{ anchor: HTMLElement | null; mode: 'create' | 'rename'; name: string; projectTitle: string; busy?: boolean; error?: string; canShare?: boolean }>(), { canShare: true })
const emit = defineEmits<{ submit: [value: { name: string; shared: boolean }]; close: [restoreFocus: boolean] }>()
const name = ref(props.name)
const shared = ref(false)
const input = ref<HTMLInputElement>()
onMounted(async () => { await nextTick(); input.value?.focus(); input.value?.select() })
function submit() {
  const value = name.value.trim()
  if (!value || props.busy) return
  emit('submit', { name: value, shared: shared.value })
}
</script>

<template>
  <FloatingPanel :anchor="anchor" :width="320" :label="mode === 'create' ? 'Save view' : 'Rename view'" cycle @close="restore => emit('close', restore)">
    <form class="save-view" @submit.prevent="submit">
      <p class="eyebrow">{{ mode === 'create' ? 'Save view' : 'Rename view' }}</p>
      <label class="name">
        <span class="sr-only">Name</span>
        <input ref="input" v-model="name" class="field" maxlength="80" placeholder="Name this view" aria-label="View name" data-autofocus />
      </label>
      <template v-if="mode === 'create'">
        <label v-if="canShare" class="check-row">
          <input v-model="shared" type="checkbox" class="check-box" />
          <span><b>Share with the project</b><small>Everyone in {{ projectTitle }} sees it; only you change it.</small></span>
        </label>
      </template>
      <p v-if="error" class="error" role="alert"><AppIcon name="alert" :size="13" />{{ error }}</p>
      <div class="actions">
        <button type="button" class="btn sm ghost" @click="emit('close', true)">Cancel</button>
        <button type="submit" class="btn sm primary" :disabled="!name.trim() || busy"><AppIcon name="check" :size="13" />{{ mode === 'create' ? 'Save view' : 'Rename' }}</button>
      </div>
    </form>
  </FloatingPanel>
</template>

<style scoped>
.save-view { display: grid; gap: 10px; padding: 4px 6px 6px; }
.name .field { height: 34px; font-size: 14px; }
.check-row { display: flex; align-items: flex-start; gap: 10px; padding: 6px 4px; border-radius: 8px; cursor: pointer; }
.check-row:hover { background: var(--row-hover); }
.check-row .check-box { margin-top: 2px; }
.check-row span { display: grid; gap: 2px; min-width: 0; }
.check-row b { font-size: 13px; font-weight: 600; color: var(--ink); }
.check-row small { font-size: 12px; color: var(--ink-2); line-height: 1.35; }
.error { display: flex; align-items: center; gap: 6px; font-size: 12.5px; color: var(--danger); }
.actions { display: flex; justify-content: flex-end; gap: 6px; }
.actions .btn { gap: 6px; }
</style>
