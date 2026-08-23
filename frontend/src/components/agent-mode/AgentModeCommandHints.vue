<!--
  PAI-818 — contextual deterministic-command carousel. Presentation only:
  clicking a suggestion emits its exact grammar string back through the same
  typed-command reducer used by keyboard and microphone transcripts.
-->
<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'

import AppIcon from '@/components/AppIcon.vue'
import type { AgentModeCommandHint } from '@/composables/agent-mode/agentModeCommandHints'

const props = defineProps<{
  hints: readonly AgentModeCommandHint[]
  disabled?: boolean
  compact?: boolean
}>()
const emit = defineEmits<{ execute: [command: string] }>()
const { t } = useI18n()

const index = ref(0)
const paused = ref(false)
const reducedMotion = ref(false)
let timer: ReturnType<typeof setInterval> | null = null
let motionQuery: MediaQueryList | null = null

const current = computed(() => props.hints[index.value] ?? null)
const showAll = computed(() => props.hints.find((hint) => hint.id === 'show-all') ?? null)
const identities = computed(() => props.hints.map((hint) => hint.id).join('\u0000'))

function stopTimer() {
  if (timer != null) clearInterval(timer)
  timer = null
}

function startTimer() {
  stopTimer()
  if (props.hints.length < 2 || paused.value || reducedMotion.value) return
  timer = setInterval(() => {
    index.value = (index.value + 1) % props.hints.length
  }, 7000)
}

function move(delta: number) {
  if (props.hints.length < 2) return
  index.value = (index.value + delta + props.hints.length) % props.hints.length
  startTimer()
}

function setPaused(value: boolean) {
  paused.value = value
  startTimer()
}

function syncMotion() {
  reducedMotion.value = motionQuery?.matches ?? false
  startTimer()
}

function execute(hint: AgentModeCommandHint | null) {
  if (!hint || props.disabled) return
  emit('execute', hint.command)
}

watch(
  identities,
  () => {
    index.value = 0
    startTimer()
  },
  { flush: 'sync' },
)

onMounted(() => {
  if (typeof window !== 'undefined' && typeof window.matchMedia === 'function') {
    motionQuery = window.matchMedia('(prefers-reduced-motion: reduce)')
    reducedMotion.value = motionQuery.matches
    motionQuery.addEventListener?.('change', syncMotion)
  }
  startTimer()
})

onBeforeUnmount(() => {
  stopTimer()
  motionQuery?.removeEventListener?.('change', syncMotion)
  motionQuery = null
})
</script>

<template>
  <section
    v-if="current"
    class="am-hints"
    :class="{ 'am-hints--compact': compact }"
    :aria-label="t('agentMode.hints.label')"
    @pointerenter="setPaused(true)"
    @pointerleave="setPaused(false)"
    @focusin="setPaused(true)"
    @focusout="setPaused(false)"
  >
    <div class="am-hints-head">
      <span>{{ t('agentMode.hints.eyebrow') }}</span>
      <span class="am-hints-count" aria-live="off">{{ index + 1 }}/{{ hints.length }}</span>
    </div>
    <div class="am-hints-row">
      <button
        type="button"
        class="am-hints-step"
        :disabled="disabled || hints.length < 2"
        :aria-label="t('agentMode.hints.previous')"
        @click="move(-1)"
      >
        <AppIcon name="chevron-left" :size="12" aria-hidden="true" />
      </button>
      <button type="button" class="am-hints-current" :disabled="disabled" @click="execute(current)">
        <AppIcon name="sparkles" :size="12" aria-hidden="true" />
        <span>{{ current.label }}</span>
      </button>
      <button
        type="button"
        class="am-hints-step"
        :disabled="disabled || hints.length < 2"
        :aria-label="t('agentMode.hints.next')"
        @click="move(1)"
      >
        <AppIcon name="chevron-right" :size="12" aria-hidden="true" />
      </button>
    </div>
    <button
      v-if="showAll && showAll.id !== current.id"
      type="button"
      class="am-hints-clear"
      :disabled="disabled"
      @click="execute(showAll)"
    >
      {{ showAll.label }}
    </button>
  </section>
</template>

<style scoped>
.am-hints {
  position: relative;
  display: grid;
  gap: 7px;
  padding: 9px;
  border: 1px solid var(--am-line);
  border-radius: 11px;
  background: color-mix(in srgb, var(--am-select) 3%, var(--am-surface));
}
.am-hints-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 8px;
}
.am-hints-head > span:first-child {
  color: var(--am-muted);
  font-size: 10px;
  font-weight: 600;
  letter-spacing: 0.06em;
  text-transform: uppercase;
}
.am-hints-count {
  color: var(--am-muted);
  font-family: 'JetBrains Mono', ui-monospace, monospace;
  font-size: 9.5px;
}
.am-hints-row {
  display: grid;
  grid-template-columns: 28px minmax(0, 1fr) 28px;
  gap: 6px;
}
.am-hints-row button,
.am-hints-clear {
  min-height: 28px;
  border: 1px solid var(--am-line);
  border-radius: 8px;
  background: var(--am-surface);
  color: var(--am-ink);
  cursor: pointer;
}
.am-hints-row button:focus-visible,
.am-hints-clear:focus-visible {
  outline: 2px solid var(--am-focus);
  outline-offset: 2px;
}
.am-hints-row button:disabled,
.am-hints-clear:disabled {
  cursor: default;
  opacity: 0.45;
}
.am-hints-step {
  display: grid;
  place-items: center;
  padding: 0;
}
.am-hints-current {
  display: flex;
  min-width: 0;
  align-items: center;
  justify-content: center;
  gap: 6px;
  padding: 4px 8px;
  color: var(--am-select) !important;
  font-size: 11px;
  font-weight: 600;
}
.am-hints-current span {
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.am-hints-clear {
  position: absolute;
  right: 9px;
  top: 6px;
  min-height: auto;
  padding: 2px 5px;
  border-color: transparent;
  background: transparent;
  color: var(--am-select);
  font-size: 9.5px;
}
.am-hints--compact {
  gap: 0;
  padding: 0;
  border: 0;
  border-radius: 0;
  background: transparent;
}
.am-hints--compact .am-hints-head,
.am-hints--compact .am-hints-clear {
  display: none;
}
.am-hints--compact .am-hints-row {
  grid-template-columns: 25px minmax(0, 1fr) 25px;
  gap: 4px;
}
.am-hints--compact .am-hints-row button {
  min-height: 24px;
}
.am-hints--compact .am-hints-current {
  padding-block: 2px;
}
@media (prefers-reduced-motion: reduce) {
  .am-hints * {
    scroll-behavior: auto !important;
  }
}
</style>
