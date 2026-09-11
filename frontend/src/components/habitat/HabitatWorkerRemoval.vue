<script setup lang="ts">
import { computed, nextTick, onScopeDispose, ref, watch } from 'vue'
import {
  FINISH_THEN_REMOVE_UNAVAILABLE,
  HANDOFF_UNAVAILABLE,
  RETIRE_AFTER_WORK_CONTROL,
  type RemovalMode,
} from './habitatRemoval'
import { humanize, type HabitatWorker } from './habitatModel'
import type { useHabitatRemoval } from '@/composables/habitat/useHabitatRemoval'

const props = defineProps<{
  worker: HabitatWorker
  removal: ReturnType<typeof useHabitatRemoval>
}>()

const dialogRef = ref<HTMLDivElement | null>(null)
const confirmRef = ref<HTMLButtonElement | null>(null)
let returnFocus: HTMLElement | null = null
let focusGeneration = 0
let disposed = false

const progressLabel = computed(() => {
  const trust = props.worker.delivery_trust
  if (trust.progress_trusted && trust.progress_percent !== null) return `${trust.progress_percent}%`
  return 'Unknown'
})
const etaLabel = computed(() =>
  props.worker.delivery_trust.eta_trusted && props.worker.delivery_trust.eta
    ? props.worker.delivery_trust.eta
    : 'Unknown',
)

const choices = computed(() => [
  {
    mode: 'finish_then_remove' as RemovalMode,
    title: 'Finish current work, then remove',
    detail:
      'Retire only after the owned runtime finishes the current turn and drains inbox work under worker-lease authority.',
    disabled: !props.removal.finishThenRemoveAvailable.value,
    unavailable: FINISH_THEN_REMOVE_UNAVAILABLE,
  },
  {
    mode: 'stop_now' as RemovalMode,
    title: 'Stop now and preserve history',
    detail:
      'Request an immediate owned stop control. Task progress, workspace, messages and account credentials stay on record; starting again needs a new generation.',
    disabled: !props.removal.stopNowAvailable.value,
    unavailable: 'Stop is not advertised for this generation or evidence is stale.',
  },
  {
    mode: 'handoff' as RemovalMode,
    title: 'Hand off unfinished work, then remove',
    detail:
      'Requires an authorized successor acceptance and settlement primitive. Binding transfer alone is insufficient.',
    disabled: !props.removal.handoffAvailable.value,
    unavailable: HANDOFF_UNAVAILABLE,
  },
  {
    mode: 'draft_only' as RemovalMode,
    title: 'Discard local lifecycle review draft',
    detail:
      'Clears only an unsubmitted lifecycle review saved in this browser for this worker. It does not change live assignment or stop the process.',
    disabled: !props.removal.draftOnlyAvailable.value,
    unavailable: 'No local lifecycle review draft exists for this worker in this browser.',
  },
])

const confirmEnabled = computed(() => {
  const selected = props.removal.mode.value
  if (props.removal.busy.value || !selected) return false
  if (selected === 'finish_then_remove') return props.removal.finishThenRemoveAvailable.value
  if (selected === 'stop_now') return props.removal.stopNowAvailable.value
  if (selected === 'draft_only') return props.removal.draftOnlyAvailable.value
  return false
})
const confirmLabel = computed(() => {
  if (props.removal.busy.value) return 'Submitting…'
  if (props.removal.mode.value === 'finish_then_remove') return 'Finish, then remove'
  if (props.removal.mode.value === 'draft_only') return 'Discard draft only'
  return 'Confirm removal'
})

watch(
  () => props.removal.open.value,
  async (value, previous) => {
    const generation = ++focusGeneration
    if (value) {
      const active = document.activeElement
      returnFocus = active instanceof HTMLElement ? active : null
      await nextTick()
      if (!disposed && generation === focusGeneration && props.removal.open.value)
        dialogRef.value?.focus()
      return
    }
    if (!previous) return
    const target = returnFocus
    returnFocus = null
    await nextTick()
    if (
      !disposed &&
      generation === focusGeneration &&
      target?.isConnected &&
      !target.matches(':disabled') &&
      !target.closest('[hidden], [inert]')
    )
      target.focus()
  },
  { flush: 'post' },
)
watch(
  () => props.removal.mode.value,
  async (value) => {
    if (!value || !confirmEnabled.value) return
    const generation = focusGeneration
    await nextTick()
    if (
      !disposed &&
      generation === focusGeneration &&
      props.removal.open.value &&
      confirmEnabled.value
    )
      confirmRef.value?.focus()
  },
)

function focusableElements(): HTMLElement[] {
  const dialog = dialogRef.value
  if (!dialog) return []
  const candidates = [
    ...dialog.querySelectorAll<HTMLElement>(
      'button:not(:disabled), input:not(:disabled), select:not(:disabled), textarea:not(:disabled), a[href], summary, [tabindex]:not([tabindex="-1"])',
    ),
  ].filter((element) => {
    if (element.matches(':disabled') || element.closest('[hidden], [inert]')) return false
    const closedDetails = element.closest('details:not([open])')
    if (closedDetails && element.tagName !== 'SUMMARY') return false
    const style = window.getComputedStyle(element)
    return style.display !== 'none' && style.visibility !== 'hidden'
  })

  const radioForGroup = new Map<string, HTMLInputElement>()
  for (const element of candidates) {
    if (!(element instanceof HTMLInputElement) || element.type !== 'radio') continue
    const current = radioForGroup.get(element.name)
    if (!current || element.checked) radioForGroup.set(element.name, element)
  }
  return candidates.filter(
    (element) =>
      !(element instanceof HTMLInputElement) ||
      element.type !== 'radio' ||
      radioForGroup.get(element.name) === element,
  )
}

function onKeydown(event: KeyboardEvent) {
  if (event.key === 'Escape') {
    event.preventDefault()
    event.stopPropagation()
    if (!props.removal.busy.value) props.removal.cancel()
    return
  }
  if (event.key !== 'Tab' || event.ctrlKey || event.metaKey || event.altKey) return
  event.stopPropagation()
  const items = focusableElements()
  const first = items[0]
  const last = items[items.length - 1]
  if (!first || !last) {
    event.preventDefault()
    dialogRef.value?.focus()
    return
  }
  const active = document.activeElement
  if (
    event.shiftKey &&
    (active === first || active === dialogRef.value || !dialogRef.value?.contains(active))
  ) {
    event.preventDefault()
    last.focus()
  } else if (!event.shiftKey && (active === last || !dialogRef.value?.contains(active))) {
    event.preventDefault()
    first.focus()
  }
}

onScopeDispose(() => {
  disposed = true
  focusGeneration++
  returnFocus = null
})
</script>

<template>
  <div
    v-if="removal.open.value"
    class="habitat-removal-backdrop"
    data-testid="habitat-worker-removal"
    @keydown="onKeydown"
  >
    <div
      ref="dialogRef"
      class="habitat-card habitat-removal-dialog"
      role="dialog"
      aria-modal="true"
      aria-labelledby="habitat-removal-title"
      tabindex="-1"
    >
      <h3 id="habitat-removal-title">Remove {{ worker.agent.name }}</h3>
      <p class="habitat-removal-meta">
        {{ worker.project.key }} · generation {{ worker.harness_session_id }} · revision
        {{ worker.revision }}
      </p>
      <p class="habitat-removal-meta" role="status">
        Progress {{ progressLabel }} · ETA {{ etaLabel }} ·
        {{ humanize(worker.delivery_trust.reason) }}
      </p>
      <p>Choose how unfinished work should be handled. Canceling leaves the worker unchanged.</p>
      <fieldset class="habitat-removal-choices" :disabled="removal.busy.value">
        <legend class="habitat-removal-legend">Removal choice</legend>
        <label
          v-for="choice in choices"
          :key="choice.mode"
          class="habitat-removal-choice"
          :class="{ 'habitat-removal-choice-disabled': choice.disabled }"
        >
          <input
            type="radio"
            name="habitat-removal-mode"
            :value="choice.mode"
            :checked="removal.mode.value === choice.mode"
            :disabled="choice.disabled || removal.busy.value"
            :aria-describedby="`habitat-removal-${choice.mode}`"
            @change="removal.choose(choice.mode)"
          />
          <span>
            <strong>{{ choice.title }}</strong>
            <small :id="`habitat-removal-${choice.mode}`">{{ choice.detail }}</small>
            <small v-if="choice.disabled" class="habitat-removal-unavailable">{{
              choice.unavailable
            }}</small>
          </span>
        </label>
      </fieldset>
      <details class="habitat-removal-gap">
        <summary>How safe finish-then-remove is confirmed</summary>
        <p>
          {{ RETIRE_AFTER_WORK_CONTROL.method }}
          {{ RETIRE_AFTER_WORK_CONTROL.path }}
        </p>
        <p>{{ RETIRE_AFTER_WORK_CONTROL.semantics }}</p>
      </details>
      <p v-if="removal.feedback.value" class="habitat-status" role="status">
        {{ removal.feedback.value }}
      </p>
      <div class="habitat-actions">
        <button
          ref="confirmRef"
          type="button"
          class="habitat-danger"
          :disabled="!confirmEnabled"
          :aria-label="removal.mode.value ? `Confirm ${removal.mode.value}` : 'Confirm removal'"
          @click="removal.submit()"
        >
          {{ confirmLabel }}
        </button>
        <button type="button" :disabled="removal.busy.value" @click="removal.cancel()">
          Cancel
        </button>
      </div>
    </div>
  </div>
</template>
