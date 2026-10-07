<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed } from 'vue'
import type { NodeRecurrence } from '../../lib/api'
import { kindLabel } from '../../lib/work'
import { recurrenceMarkerLabel } from '../../lib/recurrenceMarker'
import { useProfile } from '../../stores/profile'
import AppIcon from '../AppIcon.vue'
import { workIcon } from '../../lib/workVocabulary'
import RecurrenceGlyph from '../recurrences/RecurrenceGlyph.vue'
const props = defineProps<{ kind: string; levelName?: string; levelIcon?: string; recurrence?: NodeRecurrence }>()
const profile = useProfile()
const label = computed(() => props.recurrence ? recurrenceMarkerLabel(props.recurrence, profile.profile?.locale || navigator.language) : props.levelName ?? kindLabel(props.kind))
</script>
<template>
  <span class="ticket-type-icon" role="img" :aria-label="label" :title="label" :data-tip="label" :tabindex="recurrence ? 0 : undefined">
    <AppIcon :name="workIcon({ kind_slug: kind, level_icon: levelIcon })" :size="16" class="kind-glyph" :class="kind" />
    <span v-if="recurrence" class="recurrence-dot" aria-hidden="true"><RecurrenceGlyph :size="7" /></span>
  </span>
</template>
<style scoped>
.ticket-type-icon { position: relative; display: grid; place-items: center; flex: 0 0 22px; width: 22px; height: 22px; }
.kind-glyph { color: var(--ink-3); }.kind-glyph.epic { color: var(--kind-parent); }
.recurrence-dot { position: absolute; top: -1px; right: -2px; display: grid; place-items: center; width: 11px; height: 11px; border-radius: 50%; background: var(--marker); color: var(--marker-on); box-shadow: 0 0 0 1.5px var(--recurrence-row-tint, transparent), 0 0 0 1.5px var(--surface-raised); }
.ticket-type-icon:focus-visible { outline: 1px solid var(--marker); outline-offset: 3px; border-radius: 3px; }
</style>
