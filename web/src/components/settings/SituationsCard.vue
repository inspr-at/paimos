<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import SettingsCard from './SettingsCard.vue'
import type { SituationLimits } from '../../lib/workKinds'
import { validLimit } from '../../lib/workKinds'
import { situationDefinition, type KindsText } from '../../lib/workKindsCopy'
const props = defineProps<{ limits: SituationLimits | null; editable: boolean; busy: boolean; loading: boolean; error: string; german: boolean; t: KindsText }>()
const emit = defineEmits<{ change: [key: 'small_hours' | 'fix_rounds', value: number]; reload: [] }>()
const small = ref<number | string>(2), rounds = ref<number | string>(3), invalid = ref(false)
watch(() => props.limits, limits => { if (limits) { small.value = limits.small_hours; rounds.value = limits.fix_rounds; invalid.value = false } }, { immediate: true })
function change(key: 'small_hours' | 'fix_rounds', value: number | string) {
  const number = Number(value)
  invalid.value = !validLimit(number, key === 'small_hours' ? 8 : 6)
  if (!invalid.value && props.limits && !props.busy && props.editable && number !== props.limits[key]) emit('change', key, number)
}
const fixDefinition = computed(() => situationDefinition('fix', props.limits?.fix_rounds ?? 3, props.german))
const stuckDefinition = computed(() => situationDefinition('stuck', props.limits?.fix_rounds ?? 3, props.german))
</script>
<template>
  <SettingsCard :title="t('situations')" icon="tree" anchor="k-sits">
    <template #lead>{{ t('situationsLead') }}</template>
    <template #aside><button type="button" class="btn sm" :style="{ visibility: error ? 'visible' : 'hidden' }" :disabled="busy || loading || !error" @click="emit('reload')">{{ t('reload') }}</button></template>
    <div v-if="loading && !limits" role="status">{{ t('loading') }}</div>
    <p v-else-if="!limits" class="err" role="alert">{{ error || t('limitsError') }}</p>
    <ul v-else class="sits">
      <li id="sit-first" class="sit"><b>{{ t('first') }}</b><p>{{ t('firstDefinition') }}</p><label class="sit-lim">{{ t('small') }} <input v-if="editable" v-model="small" class="field num" type="number" min="1" max="8" step="1" :aria-label="t('hours')" :disabled="busy" :aria-invalid="invalid || undefined" @change="change('small_hours', small)"><b v-else>{{ limits.small_hours }}</b> {{ t('hours') }} <span>{{ t('lessThinking') }}</span></label></li>
      <li id="sit-fix" class="sit"><b>{{ t('fix') }}</b><p>{{ fixDefinition }}</p><p class="small">{{ t('fixHint') }}</p></li>
      <li id="sit-stuck" class="sit"><b>{{ t('stuck') }}</b><p>{{ stuckDefinition }}</p><label class="sit-lim">{{ t('stuckAfter') }} <input v-if="editable" v-model="rounds" class="field num" type="number" min="1" max="6" step="1" :aria-label="t('rounds')" :disabled="busy" :aria-invalid="invalid || undefined" @change="change('fix_rounds', rounds)"><b v-else>{{ limits.fix_rounds }}</b> {{ t('rounds') }}</label></li>
      <li id="sit-review" class="sit"><b>{{ t('review') }}</b><p>{{ t('reviewDefinition') }}</p><p class="small">{{ t('reviewHint') }}</p></li>
      <li id="sit-concept" class="sit"><b>{{ t('concept') }}</b><p>{{ t('conceptDefinition') }}</p><p class="small">{{ t('conceptHint') }}</p></li>
    </ul>
    <p class="feedback" :class="{ err: error || invalid }" role="status">{{ invalid ? t('invalidLimit') : error }}</p>
  </SettingsCard>
</template>
<style scoped>
.sits { display: grid; margin: 0; padding: 0; list-style: none; }
.sit { display: grid; gap: 4px; padding: 12px 0; border-top: 1px solid var(--line); }.sit:first-child { border-top: 0; padding-top: 4px; }
.sit b { font-size: 14px; }.sit p { margin: 0; font-size: 13.5px; line-height: 1.5; color: var(--ink-2); }.sit .small { font-size: 12px; color: var(--ink-3); }
.sit-lim { display: inline-flex; flex-wrap: wrap; align-items: center; gap: 6px; color: var(--ink-3); font-size: 12px; }
.field.num { width: 4.5em; height: 32px; text-align: right; }
.feedback { min-height: 2lh; margin-top: 12px; font-size: 12px; }.err { color: var(--danger); }
@media (max-width: 720px), (pointer: coarse) { .field.num, button { min-height: 44px; } }
</style>
<style scoped src="../../styles/settingsButtons.css"></style>
