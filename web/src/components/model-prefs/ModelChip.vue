<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed } from 'vue'
import AppIcon from '../AppIcon.vue'
import HarnessMark from '../agents/HarnessMark.vue'
import { modelCopy, type PrefChoice, type EffectiveModel } from '../../lib/modelPrefs'
const props = defineProps<{ model: EffectiveModel; label: string; review: boolean; disabled: boolean; choices?: PrefChoice[] }>()
defineEmits<{ pick: [] }>()
const copy = computed(() => modelCopy(props.model, props.review, props.choices))
</script>
<template>
  <button type="button" class="model-chip" :class="{ warning: model.unavailable_reason }" :disabled="disabled" :aria-label="`${label}: ${copy.name}`" :data-tip="copy.tip" aria-haspopup="dialog" @click="$emit('pick')">
    <span class="model-mark"><AppIcon v-if="model.selector.mode === 'auto'" :name="review ? 'compare' : 'sparkle'" :size="16" /><HarnessMark v-else :harness="model.profile?.harness || ''" :provider="model.profile?.provider || model.brand" :size="16" /></span>
    <span class="model-text"><b>{{ copy.name }}</b><small><AppIcon v-if="model.unavailable_reason" name="alert" :size="12" /><AppIcon v-else-if="model.follows_latest" name="refresh" :size="12" /><AppIcon v-else-if="model.pinned" name="pin" :size="12" /><span>{{ copy.detail }}</span></small></span>
  </button>
</template>
<style scoped>
.model-chip { display: flex; align-items: center; gap: 8px; width: 100%; height: 52px; min-width: 0; padding: 0 8px; border: 0; border-radius: 10px; background: var(--chip-bg); box-shadow: inset 0 0 0 1px var(--chip-line); text-align: left; color: var(--ink); }
.model-chip:hover:not(:disabled) { background: var(--row-hover); } .model-chip:disabled { opacity: 1; background: transparent; }
.model-chip.warning { background: var(--gold-wash); }
.model-mark { display: grid; place-items: center; width: 24px; flex: none; }
.model-text { display: grid; min-width: 0; gap: 3px; } b { font-size: 12px; line-height: 1.25; font-weight: 620; overflow-wrap: anywhere; }
small { display: flex; align-items: center; gap: 4px; color: var(--ink-3); font-size: 11px; line-height: 1.2; } small svg { flex: none; } small span { overflow-wrap: anywhere; }
</style>
