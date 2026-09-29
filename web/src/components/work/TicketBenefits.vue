<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, useId } from 'vue'
import { benefitIssues, pillWords } from '../../lib/ticketBenefits'
import AppIcon from '../AppIcon.vue'

// The plain-language user benefit (AEON-256): a 2–4 word pill and one or two
// sentences, in English and German. Reading shows what is there, one line when
// something is missing and, when the viewer may edit, the one action that fixes it.
const props = defineProps<{ fields: Record<string, unknown>; editing?: boolean; disabled?: boolean; done?: boolean; editable?: boolean; notice?: string; invalidKey?: string }>()
const emit = defineEmits<{ change: [key: string, value: string | boolean]; edit: [] }>()
const uid = useId()
const issues = computed(() => benefitIssues(props.fields))
const languages = [
  { key: 'en', label: 'English', short: 'EN', pill: 'e.g. Hosts in minutes', benefit: 'What people gain, in one or two sentences.' },
  { key: 'de', label: 'Deutsch', short: 'DE', pill: 'z. B. Hosts in Minuten', benefit: 'Was Menschen gewinnen, neutral formuliert, ohne direkte Anrede.' },
] as const
const text = (key: string) => typeof props.fields[key] === 'string' ? (props.fields[key] as string).trim() : ''
const filled = computed(() => languages.filter(language => text(`pill_${language.key}`) || text(`benefit_${language.key}`)))
const guidance = computed(() => {
  if (!issues.value.length) return ''
  return props.done
    ? 'This completed ticket has incomplete benefit fields. Completing it again needs both languages.'
    : 'Before Done: a 2–4 word pill and a plain benefit, in both languages.'
})
const raw = (key: string) => typeof props.fields[key] === 'string' ? props.fields[key] as string : ''
const countLabel = (value: string) => { const n = pillWords(value); return n ? `${n} ${n === 1 ? 'word' : 'words'} of 2–4` : '2–4 words' }
</script>

<template>
  <section class="benefits" :class="{ editing }" :aria-labelledby="`${uid}-title`">
    <header class="section-head">
      <h3 :id="`${uid}-title`" class="eyebrow">User benefit</h3>
      <span v-if="!editing && fields.hide_from_release_notes === true" class="hidden-note"><AppIcon name="eye-off" :size="12" />Hidden from release notes</span>
    </header>

    <template v-if="editing">
      <p v-if="notice" :id="`${uid}-notice`" class="guidance" role="status">{{ notice }}</p>
      <p v-else-if="guidance" class="guidance" role="status">{{ guidance }}</p>
      <div class="languages">
        <div v-for="language in languages" :key="language.key" :lang="language.key" class="language-edit">
          <div class="label-row">
            <label :for="`${uid}-pill-${language.key}`">Pill · {{ language.label }}</label>
            <span class="count" :class="{ off: pillWords(text(`pill_${language.key}`)) > 4 || pillWords(text(`pill_${language.key}`)) === 1 }" aria-hidden="true">{{ countLabel(text(`pill_${language.key}`)) }}</span>
          </div>
          <input :id="`${uid}-pill-${language.key}`" class="field" :value="raw(`pill_${language.key}`)" :placeholder="language.pill" :disabled="disabled" :aria-invalid="invalidKey === `pill_${language.key}` ? 'true' : undefined" :aria-describedby="invalidKey === `pill_${language.key}` && notice ? `${uid}-notice` : undefined" @input="emit('change', `pill_${language.key}`, ($event.target as HTMLInputElement).value)" />
          <label :for="`${uid}-benefit-${language.key}`">Benefit · {{ language.label }}</label>
          <textarea :id="`${uid}-benefit-${language.key}`" class="field" rows="3" :value="raw(`benefit_${language.key}`)" :placeholder="language.benefit" :disabled="disabled" :aria-invalid="invalidKey === `benefit_${language.key}` ? 'true' : undefined" :aria-describedby="invalidKey === `benefit_${language.key}` && notice ? `${uid}-notice` : undefined" @input="emit('change', `benefit_${language.key}`, ($event.target as HTMLTextAreaElement).value)" />
        </div>
      </div>
      <label class="hide" data-tip="Hidden tickets still need both languages."><input type="checkbox" :checked="fields.hide_from_release_notes === true" :disabled="disabled" @change="emit('change', 'hide_from_release_notes', ($event.target as HTMLInputElement).checked)" />Hide from release notes</label>
    </template>

    <template v-else>
      <dl v-if="filled.length" class="languages">
        <div v-for="language in filled" :key="language.key" :lang="language.key" class="language">
          <dt class="lang" :data-tip="language.label" :aria-label="language.label">{{ language.short }}</dt>
          <dd>
            <span v-if="text(`pill_${language.key}`)" class="pill">{{ text(`pill_${language.key}`) }}</span>
            <p v-if="text(`benefit_${language.key}`)" class="benefit-text">{{ text(`benefit_${language.key}`) }}</p>
          </dd>
        </div>
      </dl>
      <p v-if="guidance" class="guidance" role="status">
        <span>{{ guidance }}</span>
        <button v-if="editable" type="button" class="add-benefit" @click="emit('edit')"><AppIcon name="plus" :size="12" />{{ filled.length ? 'Complete benefit' : 'Add benefit' }}</button>
      </p>
    </template>
  </section>
</template>

<style scoped>
.benefits { display: grid; gap: 8px; min-width: 0; container-type: inline-size; }
.section-head { display: flex; align-items: center; justify-content: space-between; gap: 12px; min-height: 26px; }
.section-head .eyebrow { margin: 0; }
.hidden-note { display: inline-flex; align-items: center; gap: 5px; font-size: 12px; color: var(--ink-3); }
.languages { display: grid; gap: 12px; margin: 0; }
/* Reading: a quiet language tag in a narrow gutter, the pill above its sentence. */
.language { display: grid; grid-template-columns: 26px minmax(0, 1fr); gap: 10px; align-items: start; }
.lang { padding-top: 4px; font: 500 10px/1.5 var(--mono); letter-spacing: .12em; color: var(--ink-3); font-variant-ligatures: none; }
.language dd { display: grid; justify-items: start; gap: 5px; min-width: 0; margin: 0; }
.pill {
  display: inline-flex; align-items: center; max-width: 100%; height: 24px; padding: 0 10px; border-radius: 999px;
  background: var(--chip-teal-bg); box-shadow: inset 0 0 0 1px var(--chip-teal-line); color: var(--teal-ink);
  font-size: 12.5px; font-weight: 600; white-space: nowrap; overflow: hidden; text-overflow: ellipsis;
}
.benefit-text { margin: 0; font-size: 14px; line-height: 1.5; color: var(--ink); white-space: pre-wrap; overflow-wrap: anywhere; }
.guidance { display: flex; flex-wrap: wrap; align-items: center; gap: 4px 10px; margin: 0; font-size: 12.5px; line-height: 1.45; color: var(--ink-3); }
.add-benefit { display: inline-flex; align-items: center; gap: 5px; height: 26px; padding: 0 10px; border: 0; border-radius: 999px; background: transparent; box-shadow: inset 0 0 0 1px var(--line); color: var(--teal-ink); font-size: 12px; font-weight: 600; cursor: pointer; }
@media (hover: hover) { .add-benefit:hover { background: var(--row-hover); box-shadow: inset 0 0 0 1px var(--glass-rim); } }
.add-benefit:focus-visible { box-shadow: var(--focus-ring); }
/* Editing: English and German side by side when there is room. */
.editing .languages { gap: 14px 16px; }
@container (min-width: 520px) { .editing .languages { grid-template-columns: 1fr 1fr; } }
.language-edit { display: grid; gap: 5px; min-width: 0; align-content: start; }
.language-edit label:not(:first-child) { margin-top: 6px; }
.label-row { display: flex; align-items: baseline; justify-content: space-between; gap: 8px; }
label { font-size: 12px; color: var(--ink-2); }
.count { font: 500 10.5px/1 var(--mono); color: var(--ink-3); font-variant-numeric: tabular-nums; font-variant-ligatures: none; }
.count.off { color: var(--warn); }
.field { width: 100%; padding: 8px 10px; }
.field[aria-invalid="true"] { box-shadow: 0 0 0 1px var(--warn); }
textarea.field { height: auto; min-height: 68px; resize: vertical; line-height: 1.45; }
.hide { display: inline-flex; align-items: center; gap: 8px; justify-self: start; margin-top: 2px; }
</style>
