<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { onMounted, ref, watch } from 'vue'
import { tickTarget, type CheckState } from '../../lib/rules'

// One tick for a whole layer or set. Mixed means some movable rules are off.
const props = defineProps<{ state: CheckState; label: string; disabled?: boolean; tip?: string }>()
const emit = defineEmits<{ toggle: [enabled: boolean] }>()
const box = ref<HTMLInputElement | null>(null)

function sync() {
  const input = box.value
  if (!input) return
  // The browser flips the control before this runs. Paint the saved state;
  // a later prop change (optimistic, then rollback or commit) paints again.
  input.checked = props.state === 'on'
  input.indeterminate = props.state === 'mixed'
}
onMounted(sync)
watch(() => props.state, sync)

function change() {
  const next = tickTarget(props.state)
  sync()
  if (!props.disabled) emit('toggle', next)
}
</script>

<template>
  <label class="switch" :data-tip="tip || undefined">
    <input
      ref="box" type="checkbox" :checked="state === 'on'" :disabled="disabled"
      :aria-label="label" :aria-checked="state === 'mixed' ? 'mixed' : undefined"
      @change="change"
    >
  </label>
</template>
