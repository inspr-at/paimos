<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, useId } from 'vue'
import { benefitIssues } from '../../lib/ticketBenefits'
const props = defineProps<{ fields: Record<string, unknown>; editing?: boolean; disabled?: boolean; done?: boolean }>()
const emit = defineEmits<{ change: [key: string, value: string | boolean] }>()
const uid = useId()
const issues = computed(() => benefitIssues(props.fields))
const languages = [{ key: 'en', label: 'English' }, { key: 'de', label: 'Deutsch' }] as const
const text = (key: string) => typeof props.fields[key] === 'string' ? props.fields[key] as string : ''
</script>
<template>
  <section class="benefits" :aria-labelledby="`${uid}-title`">
    <h3 :id="`${uid}-title`" class="eyebrow">User benefit</h3>
    <p v-if="issues.length" class="guidance" role="status">{{ done ? 'This completed ticket has incomplete benefit fields. Add them when known; completing it again will require both languages.' : 'Before Done, add a 2–4 word pill and a plain-language benefit in both languages.' }}</p>
    <p v-if="editing" class="guidance">One or two positive sentences about what people gain. German: neutral, without direct address. Hidden tickets still need both languages.</p>
    <div v-for="language in languages" :key="language.key" :lang="language.key" class="language">
      <template v-if="editing">
        <label :for="`${uid}-pill-${language.key}`">Pill · {{ language.label }} <span class="hint">2–4 words</span></label>
        <input :id="`${uid}-pill-${language.key}`" class="field" :value="text(`pill_${language.key}`)" :disabled="disabled" @input="emit('change', `pill_${language.key}`, ($event.target as HTMLInputElement).value)" />
        <label :for="`${uid}-benefit-${language.key}`">Benefit · {{ language.label }}</label>
        <textarea :id="`${uid}-benefit-${language.key}`" class="field" rows="2" :value="text(`benefit_${language.key}`)" :disabled="disabled" @input="emit('change', `benefit_${language.key}`, ($event.target as HTMLTextAreaElement).value)" />
      </template>
      <template v-else-if="text(`pill_${language.key}`) || text(`benefit_${language.key}`)">
        <p class="pill-line"><span class="hint">{{ language.label }}</span><span v-if="text(`pill_${language.key}`)" class="chip">{{ text(`pill_${language.key}`) }}</span></p>
        <p class="benefit-text">{{ text(`benefit_${language.key}`) }}</p>
      </template>
    </div>
    <label v-if="editing" class="hide"><input type="checkbox" :checked="fields.hide_from_release_notes === true" :disabled="disabled" @change="emit('change', 'hide_from_release_notes', ($event.target as HTMLInputElement).checked)" />Hide from release notes</label>
    <p v-else-if="fields.hide_from_release_notes === true" class="guidance">Hidden from release notes</p>
  </section>
</template>
<style scoped>
.benefits { display: grid; gap: 10px; }
.eyebrow { margin: 0; }
.language { display: grid; gap: 6px; }
.language:empty { display: none; }
label { font-size: 12px; color: var(--ink-2); }
.field { width: 100%; padding: 8px 10px; }
textarea { resize: vertical; }
.guidance, .hint { color: var(--ink-3); font-size: 12px; }
.pill-line, .hide { display: flex; align-items: center; gap: 8px; }
.benefit-text { font-size: 14px; color: var(--ink); white-space: pre-wrap; }
</style>
