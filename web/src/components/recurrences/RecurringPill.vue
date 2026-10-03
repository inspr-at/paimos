<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed } from 'vue'
import type { NodeRecurrence } from '../../lib/api'
import { can } from '../../lib/authz'
import { recurrenceMarkerLabel, recurringWord } from '../../lib/recurrenceMarker'
import { useProfile } from '../../stores/profile'
import RecurrenceGlyph from './RecurrenceGlyph.vue'
const props = defineProps<{ recurrence: NodeRecurrence }>()
const profile = useProfile()
const locale = computed(() => profile.profile?.locale || navigator.language)
const label = computed(() => recurrenceMarkerLabel(props.recurrence, locale.value))
const manage = computed(() => !props.recurrence.retired && can('recurrences.manage', props.recurrence.project_id))
const destination = computed(() => ({ path: `/p/${encodeURIComponent(props.recurrence.project_key)}/settings`, query: { recurrence: props.recurrence.id } }))
</script>
<template>
  <RouterLink v-if="manage" class="recurring-pill" :to="destination" :aria-label="label" :title="label" :data-tip="label"><RecurrenceGlyph />{{ recurringWord(locale) }}</RouterLink>
  <span v-else class="recurring-pill" role="img" tabindex="0" :aria-label="label" :title="label" :data-tip="label"><RecurrenceGlyph />{{ recurringWord(locale) }}</span>
</template>
<style scoped>
.recurring-pill { display: inline-flex; align-items: center; justify-content: center; flex: none; gap: 6px; min-height: 26px; max-width: 100%; padding: 3px 9px; border-radius: 999px; box-shadow: inset 0 0 0 1px color-mix(in srgb, var(--gold) 35%, transparent); background: var(--gold-wash); color: var(--ink); font-size: 12px; font-weight: 600; text-decoration: none; }
.recurring-pill svg { flex: none; color: var(--gold); }
a.recurring-pill:hover { background: color-mix(in srgb, var(--gold) 15%, transparent); }
.recurring-pill:focus-visible { outline: 1px solid var(--gold); outline-offset: 3px; }
@media (pointer: coarse) { a.recurring-pill { min-height: 44px; } }
</style>
