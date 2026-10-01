<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { ref } from 'vue'
import { permissionLabel } from '../../lib/access'
import { scopeCodeSelection } from '../../lib/scopeCode'

const props = defineProps<{ unavailable: (key: string) => string | undefined; disabled?: boolean }>()
const emit = defineEmits<{ applied: [scopes: Set<string>] }>()
const code = ref('')
const error = ref('')
const applied = ref<number | null>(null)
const skipped = ref<{ key: string; reason: string }[]>([])
function apply(event: Event) {
  code.value = (event.target as HTMLInputElement).value
  error.value = ''
  applied.value = null
  skipped.value = []
  if (!code.value.trim() || props.disabled) return
  try {
    const result = scopeCodeSelection(code.value, props.unavailable)
    skipped.value = result.skipped
    applied.value = result.selected.size
    emit('applied', result.selected)
  } catch (e) { error.value = e instanceof Error ? e.message : 'The scope code could not be read.' }
}
</script>

<template>
  <div class="scope-code">
    <label for="scope-code-input">Scope code</label>
    <input id="scope-code-input" class="field mono" type="text" :value="code" :disabled="disabled" placeholder="aeon-scopes:v1:…" autocomplete="off" spellcheck="false" :maxlength="32768" :aria-invalid="!!error" aria-describedby="scope-code-note scope-code-result" @input="apply" />
    <p id="scope-code-note">Paste a proposal to replace the selection. Review the boxes before confirming.</p>
    <div id="scope-code-result" aria-live="polite">
      <p v-if="error" class="failure" role="alert">{{ error }} Your selection was kept.</p>
      <template v-else-if="applied !== null">
        <p>{{ applied }} {{ applied === 1 ? 'scope selected' : 'scopes selected' }}.</p>
        <template v-if="skipped.length">
          <p>Left unticked:</p>
          <ul><li v-for="scope in skipped" :key="scope.key">{{ permissionLabel(scope.key) }} <span class="mono">({{ scope.key }})</span> — {{ scope.reason }}</li></ul>
        </template>
      </template>
    </div>
  </div>
</template>

<style scoped>
.scope-code { display: grid; gap: 6px; min-width: 0; }
.scope-code label { font-size: 13px; font-weight: 600; }
.scope-code p, .scope-code li { font-size: 12.5px; line-height: 1.5; color: var(--ink-2); overflow-wrap: anywhere; }
.scope-code .field { font-size: 12px; min-width: 0; width: 100%; }
.scope-code ul { margin: 4px 0 0; padding-left: 18px; }
.scope-code .failure { color: var(--danger); }
</style>
