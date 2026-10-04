<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import KeyCap from '../KeyCap.vue'
defineProps<{ editable: boolean; closable: boolean; editing: boolean; busy: boolean; reloadRequired: boolean; undoable: boolean; submitKey: string }>()
defineEmits<{ edit: []; save: []; cancel: []; undo: []; reload: [] }>()
</script>
<template>
  <div class="actions">
    <button type="button" class="btn" :aria-disabled="!editable || busy || reloadRequired" data-testid="policy-edit" @click="editable && !busy && !reloadRequired && $emit('edit')">Edit <kbd>E</kbd></button>
    <button type="button" class="btn" data-policy-save data-testid="policy-save" :aria-disabled="!editing || busy || reloadRequired" @click="editing && !busy && !reloadRequired && $emit('save')">Save <span class="keys" :aria-label="submitKey"><KeyCap k="mod" /><KeyCap k="enter" /></span></button>
    <button type="button" class="btn" data-testid="policy-cancel" :aria-disabled="!closable || busy" @click="closable && !busy && $emit('cancel')">Cancel <kbd>Esc</kbd></button>
    <button type="button" class="btn" data-testid="policy-undo" :aria-disabled="!undoable || busy || reloadRequired" @click="undoable && !busy && !reloadRequired && $emit('undo')">Undo <kbd>U</kbd></button>
    <button type="button" class="btn" data-testid="policy-reload" :aria-disabled="busy" @click="!busy && $emit('reload')">Reload</button>
  </div>
</template>
<style scoped>
.actions { display: grid; grid-template-columns: repeat(5,minmax(0,1fr)); gap: 6px; inline-size: 100%; }
.actions button { min-inline-size: 0; padding: 6px; min-block-size: 36px; gap: 4px; display: flex; justify-content: center; align-items: center; flex-wrap: wrap; font-size: 12px; }
.actions button { border-color:transparent; border-radius:3px; background:transparent; box-shadow:none; transform:none; }
.actions button[data-policy-save] { border-color:var(--line-2); }
.actions button:hover { background:var(--row-selected); box-shadow:none; }
.actions button[aria-disabled="true"] { opacity: .5; cursor: default; }
.keys { display:inline-flex; align-items:center; gap:2px; }
kbd { font-size: 10px; color: var(--ink-2); }
@media (pointer:coarse) { .actions button { min-block-size: 44px; } }
@media (max-width:600px) { .actions { grid-template-columns: repeat(3,minmax(0,1fr)); } .actions button { min-block-size: 44px; } }
</style>
