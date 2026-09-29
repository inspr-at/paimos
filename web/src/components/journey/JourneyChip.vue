<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import { getIntake, STAGE_LABEL, STAGES } from '../../lib/journey'
import { useJourney } from '../../stores/journey'
import AppIcon from '../AppIcon.vue'

// The footer's compact journey line: where the project stands and the one next
// action. It leads to the Journey view. A project that has not really started a
// journey (Inspire, nothing recorded, stage not derived from its history) shows
// no pill: "Continue intake" would mislead on a mature project.
const props = defineProps<{ projectId: string; active: boolean }>()
const emit = defineEmits<{ go: []; shown: [value: boolean] }>()
const store = useJourney()
watch(() => props.projectId, id => { void store.load(id) }, { immediate: true })
const journey = computed(() => store.journeys[props.projectId] ?? null)
const index = computed(() => journey.value ? STAGES.indexOf(journey.value.stage) + 1 : 0)
const next = computed(() => journey.value?.next_action ?? null)
const quiet = computed(() => !next.value || next.value.key === 'wait_for_build' || !next.value.available)
const sources = ref<number | null>(null)
watch(() => [props.projectId, journey.value?.stage, journey.value?.stage_source] as const, async ([id, stage, origin]) => {
  sources.value = null
  if (stage !== 'inspire' || origin === 'derived') return
  try { const intake = await getIntake(id); if (id === props.projectId) sources.value = intake.sources.length } catch { if (id === props.projectId) sources.value = 0 }
}, { immediate: true })
const shown = computed(() => !!journey.value && !(journey.value.stage === 'inspire' && journey.value.stage_source !== 'derived' && !sources.value))
watch(shown, value => emit('shown', value), { immediate: true })
onBeforeUnmount(() => emit('shown', false))
</script>

<template>
  <button
    v-if="journey && next && shown" type="button" class="journey-chip" :class="{ active, quiet }" :aria-label="`Journey: ${STAGE_LABEL[journey.stage]}, stage ${index} of 8. Next: ${next.label}. Open the journey`"
    :data-tip="next.reason && !next.available ? `Next: ${next.label}\n${next.reason}` : `Next: ${next.label}`" @click="emit('go')"
  >
    <span class="face">
      <span class="n mono" aria-hidden="true">{{ index }}</span>
      <span class="stage">{{ STAGE_LABEL[journey.stage] }}</span>
      <span class="sep" aria-hidden="true" />
      <span class="led" aria-hidden="true" />
      <span class="next">{{ next.label }}</span>
      <AppIcon name="arrow" :size="12" class="go" />
    </span>
  </button>
</template>

<style scoped>
/* The button is the footer's full hit target. The face is the pill. */
.journey-chip {
  display: inline-flex; align-items: center; justify-content: center; box-sizing: border-box; width: max-content; max-width: 100%; height: 100%; min-width: 0; min-height: 0;
  margin: 0; padding: 0; border: 0; background: transparent; color: inherit; cursor: pointer;
}
.face {
  display: inline-flex; align-items: center; gap: 8px; max-width: 100%; min-width: 0; height: 30px; padding: 0 12px 0 4px; border-radius: 999px;
  background: var(--chip-bg); box-shadow: inset 0 0 0 1px var(--chip-line); color: var(--ink-2); font-size: 12.5px; white-space: nowrap; overflow: hidden;
}
.journey-chip:hover .face { box-shadow: inset 0 0 0 1px rgba(214, 155, 49, .6); color: var(--ink); }
.journey-chip:focus-visible .face { box-shadow: var(--focus-ring); }
.journey-chip.active .face { background: var(--chip-teal-bg); box-shadow: inset 0 0 0 1px var(--chip-teal-line); }
.n { display: inline-grid; place-items: center; flex-shrink: 0; width: 22px; height: 22px; border-radius: 7px; background: radial-gradient(circle at 40% 35%, var(--surface), var(--aqua)); box-shadow: 0 0 0 1px var(--aqua); color: var(--teal-ink); font-size: 10.5px; }
.stage { flex-shrink: 0; font-weight: 600; color: var(--ink); }
.sep { flex-shrink: 0; width: 1px; height: 14px; background: var(--line-2); }
.led { width: 6px; height: 6px; border-radius: 50%; background: var(--teal); box-shadow: 0 0 6px color-mix(in oklab, var(--aqua) 90%, transparent); flex-shrink: 0; }
.quiet .led { background: var(--ink-3); box-shadow: none; }
.next { min-width: 0; overflow: hidden; text-overflow: ellipsis; color: var(--teal-ink); font-weight: 600; }
.quiet .next { color: var(--ink-2); font-weight: 500; }
.go { color: var(--ink-3); flex-shrink: 0; }
@media (max-width: 600px) {
  .journey-chip { height: 44px; min-height: 44px; }
  .face { height: 32px; }
}
</style>
