<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useRoute } from 'vue-router'
import FoldSection from '../agents/FoldSection.vue'
import { scopeOwner } from '../../lib/identityScope'
import { useSession } from '../../stores/session'
import { validLimit, type SituationLimits, type WorkKind } from '../../lib/workKinds'
import type { KindsText } from '../../lib/workKindsCopy'
// The parts nobody changes weekly, folded: how PAIMOS recognises each kind and
// the two situation limits. The definitions themselves live on Models.
const props = defineProps<{ kinds: WorkKind[]; limits: SituationLimits | null; editable: boolean; busy: boolean; loading: boolean; error: string; german: boolean; t: KindsText }>()
const emit = defineEmits<{ change: [key: 'small_hours' | 'fix_rounds', value: number]; reload: [] }>()
const session = useSession(), route = useRoute()
const open = ref(false)
const storageKey = computed(() => `kinds-page/${scopeOwner(session.identity)}/advanced`)
// The fold is a per-person browser preference. A deep link to the limits opens it without saving that.
const named = () => route.hash === '#k-sits' || route.hash === '#k-adv'
watch(storageKey, () => {
  open.value = false
  if (scopeOwner(session.identity)) try { open.value = localStorage.getItem(storageKey.value) === '1' } catch { /* Optional browser preference. */ }
  if (named()) open.value = true
}, { immediate: true, flush: 'sync' })
watch(() => route.hash, () => { if (named()) open.value = true })
function toggle() {
  open.value = !open.value
  if (scopeOwner(session.identity)) try { localStorage.setItem(storageKey.value, open.value ? '1' : '0') } catch { /* Optional browser preference. */ }
}
const small = ref<number | string>(2), rounds = ref<number | string>(3), invalid = ref(false)
watch(() => props.limits, limits => { if (limits) { small.value = limits.small_hours; rounds.value = limits.fix_rounds; invalid.value = false } }, { immediate: true })
function change(key: 'small_hours' | 'fix_rounds', value: number | string) {
  const number = Number(value)
  invalid.value = !validLimit(number, key === 'small_hours' ? 8 : 6)
  if (!invalid.value && props.limits && !props.busy && props.editable && number !== props.limits[key]) emit('change', key, number)
}
</script>
<template>
  <FoldSection id="k-adv" :open="open" :label="t('advanced')" @toggle="toggle">
    <template #title>{{ t('advanced') }}</template>
    <template #head><span class="adv-sum">{{ t('advancedSummary') }}</span></template>
    <div class="adv">
      <h3 class="adv-h">{{ t('recognitionHeading') }}</h3>
      <p class="note">{{ t('recognitionNote') }}</p>
      <table class="kt">
        <thead><tr><th scope="col">{{ t('colKind') }}</th><th scope="col">{{ t('colArea') }}</th><th scope="col">{{ t('colLabels') }}</th></tr></thead>
        <tbody>
          <tr v-for="kind in kinds" :key="kind.id">
            <td>{{ kind.label }}</td>
            <td><code>{{ kind.system === 'other' ? (german ? '(keiner)' : '(none)') : kind.slug }}</code><template v-if="kind.system === 'other'"> {{ t('otherRecognition') }}</template></td>
            <td><template v-if="kind.labels.length"><code v-for="label in kind.labels" :key="label">{{ label }}</code></template><span v-else class="faint">{{ t('none') }}</span></td>
          </tr>
        </tbody>
      </table>
      <h3 id="k-sits" class="adv-h">{{ t('limitsHeading') }}</h3>
      <p v-if="loading && !limits" role="status">{{ t('loading') }}</p>
      <div v-else-if="!limits" class="adv-err"><p class="err" role="alert">{{ error || t('limitsError') }}</p><button type="button" class="btn sm" :disabled="busy || loading" @click="emit('reload')">{{ t('reload') }}</button></div>
      <template v-else>
        <ul class="sl">
          <li><b>{{ t('smallIs') }}</b> {{ t('smallMid') }} <input v-if="editable" v-model="small" class="field num" type="number" min="1" max="8" step="1" :aria-label="t('smallHoursLabel')" :disabled="busy" :aria-invalid="invalid || undefined" @change="change('small_hours', small)"><b v-else class="val">{{ limits.small_hours }}</b> {{ t('smallEnd') }}</li>
          <li><b>{{ t('stuckLead') }}</b> {{ t('stuckMid') }} <input v-if="editable" v-model="rounds" class="field num" type="number" min="1" max="6" step="1" :aria-label="t('fixRoundsLabel')" :disabled="busy" :aria-invalid="invalid || undefined" @change="change('fix_rounds', rounds)"><b v-else class="val">{{ limits.fix_rounds }}</b> {{ t('stuckEnd') }}</li>
        </ul>
        <p v-if="invalid || error" class="err" role="alert">{{ invalid ? t('invalidLimit') : error }}</p>
      </template>
      <p class="note">{{ t('definitionsNote') }}</p>
    </div>
  </FoldSection>
</template>
<style scoped>
.adv-sum { min-width: 0; margin-left: auto; font-size: 12.5px; color: var(--ink-2); text-align: right; text-wrap: balance; }
.adv { padding: 4px 18px 18px; }
.adv-h { margin: 16px 0 4px; font-size: 13.5px; font-weight: 650; }.adv-h:first-child { margin-top: 0; }
.note { margin: 4px 0 0; font-size: 12px; line-height: 1.5; color: var(--ink-3); }
.kt { width: 100%; margin: 8px 0 0; border-collapse: collapse; font-size: 12.5px; }
.kt th { padding: 6px 8px; text-align: left; font-size: 11.5px; font-weight: 600; color: var(--ink-3); border-bottom: 1px solid var(--line); }
.kt td { padding: 6px 8px; border-bottom: 1px solid var(--line); color: var(--ink-2); vertical-align: top; overflow-wrap: anywhere; }
.kt td:first-child { color: var(--ink); font-weight: 600; }
code { font: 500 11.5px/1.4 var(--mono); background: var(--code-bg); padding: 1px 4px; border-radius: 4px; color: var(--ink); margin-right: 3px; }
.faint { color: var(--ink-3); }
.sl { display: grid; gap: 8px; margin: 8px 0; padding: 0; list-style: none; font-size: 13.5px; line-height: 1.5; }
.sl .field.num { display: inline-block; width: 56px; height: 30px; margin: 0 4px; text-align: right; vertical-align: middle; }
.sl .val { margin: 0 4px; }
.adv-err { display: flex; flex-wrap: wrap; align-items: center; gap: 8px 12px; }.adv-err p { margin: 0; }
.err { margin: 4px 0 0; font-size: 12px; color: var(--danger); }
@container body (max-width: 640px) { .adv { padding: 4px 14px 16px; } }
@media (max-width: 720px), (pointer: coarse) { .sl .field.num, .adv-err button { min-height: 44px; } }
@media (max-width: 720px) { :deep(.fs-tog) { width: 44px; height: 44px; } :deep(.fs-title) { min-height: 44px; } }
</style>
<style scoped src="../../styles/settingsButtons.css"></style>
