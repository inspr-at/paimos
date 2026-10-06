<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed } from 'vue'
import { grantablePresetScopes, KEY_SCOPE_PRESETS, PERSON_ONLY_KEY_NOTE, presetScopes, type Permission } from '../../lib/access'
import AppIcon from '../AppIcon.vue'

const props = defineProps<{ held: Set<string>; registry: Permission[]; disabled?: boolean; preferred?: string }>()
const emit = defineEmits<{ apply: [scopes: string[]] }>()
const presets = computed(() => KEY_SCOPE_PRESETS.map(preset => ({ ...preset, scopes: presetScopes(preset.scopes, props.registry), allowed: grantablePresetScopes(preset.scopes, props.held, props.registry) })))
</script>

<template>
  <div class="presets" aria-label="Scope presets">
    <div class="preset-actions">
      <button v-for="preset in presets" :key="preset.id" type="button" class="btn sm" :aria-label="`Apply ${preset.label}`" :disabled="disabled || !preset.allowed.length" @click="emit('apply', grantablePresetScopes(preset.scopes, held, registry))">{{ preset.label }}</button>
    </div>
    <p class="person-only">{{ PERSON_ONLY_KEY_NOTE }}</p>
    <details v-for="preset in presets" :key="preset.id" class="preset" :open="preferred === preset.id">
      <summary><AppIcon name="chevron-right" :size="12" class="chevron" /><span>{{ preset.label }} <span class="count">{{ preset.allowed.length }}/{{ preset.scopes.length }} available</span></span></summary>
      <p>{{ preset.description }}</p>
      <ul>
        <li v-for="key in preset.scopes" :key="key"><code>{{ key }}</code><span v-if="!preset.allowed.includes(key)" class="unavailable">Unavailable under current permissions</span></li>
      </ul>
      <p v-if="preset.allowed.length < preset.scopes.length">Only available scopes will be selected. The agent’s role and project access stay as they are.</p>
    </details>
  </div>
</template>

<style scoped>
.presets { display: grid; gap: 8px; min-width: 0; }
/* Presets keep their labels' width and wrap as a group (AEON-730). */
.preset-actions { display: flex; flex-wrap: wrap; gap: 6px; }
.preset-actions .btn { white-space: nowrap; }
.person-only { font-size: 12px; line-height: 1.5; color: var(--ink-3); }
.preset { min-width: 0; padding-block: 6px; border-bottom: 1px solid var(--line); font-size: 12px; line-height: 1.5; overflow-wrap: anywhere; }
summary { display: flex; align-items: center; gap: 6px; cursor: pointer; font-size: 13px; font-weight: 600; list-style: none; }
summary::-webkit-details-marker { display: none; }
.chevron { flex-shrink: 0; }
.preset[open] .chevron { transform: rotate(90deg); }
summary:focus-visible { outline: 2px solid var(--teal); outline-offset: 3px; border-radius: 4px; }
.count { color: var(--ink-3); font-size: 11px; font-weight: 400; }
.preset p, .preset ul { margin-top: 8px; }
.preset p { color: var(--ink-2); }
ul { display: grid; gap: 4px; padding-left: 18px; }
li .unavailable { display: block; color: var(--ink-3); font-size: 11px; }
code { font: 11px var(--mono); }
@media (max-width: 600px) { summary, .preset-actions .btn { min-height: 44px; } summary { align-content: center; } }
</style>
