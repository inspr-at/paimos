<!--
  PAIMOS — Your Professional & Personal AI Project OS
  Copyright (C) 2026 Markus Barta <markus@barta.com>
  AGPL-3.0-only — see LICENSE.

  PAI-809 — compact sibling to AgentModeVoiceConsole. The challenge is an
  inline, non-modal dialog: no teleport, scrim, canvas layer, or layout shell.
-->
<script setup lang="ts">
import { computed, nextTick, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'

import type {
  AgentModeControlState,
  BoundControlCommand,
  ControlActivation,
} from '@/composables/agent-mode/useAgentModeControls'
import type {
  ControlChallengeTemplate,
  ControlPriority,
  ControlTarget,
} from '@/services/agentModeControls'

const props = defineProps<{
  state: AgentModeControlState
  targets: readonly ControlTarget[]
  command: BoundControlCommand | null
  busy: boolean
  available: boolean
  transitionAvailable: boolean
  selectedIssueKey: string
  voicePhrase: string
  focusToken: number
  focusReturnToken: number
}>()

const emit = defineEmits<{
  activate: [activation: ControlActivation]
  confirm: []
  withdraw: []
  retry: []
  dismiss: []
}>()

const { t } = useI18n()
const priority = ref<ControlPriority>('high')
const challenge = ref<HTMLElement | null>(null)
const backButton = ref<HTMLButtonElement | null>(null)
let returnTarget: HTMLElement | null = null

const pending = computed(() => props.command?.command.status === 'pending_confirmation')
const accepted = computed(() => props.command?.command.status === 'accepted')
const terminal = computed(() => {
  const status = props.command?.command.status
  return status === 'applied' || status === 'rejected' || status === 'expired'
})
const lifecycle = computed(() => {
  const command = props.command?.command
  if (!command) return ''
  if (command.outcome === 'outcome_unknown') return t('agentMode.controls.lifecycle.unknown')
  if (command.status === 'pending_confirmation') return t('agentMode.controls.lifecycle.pending')
  if (command.status === 'accepted') return t('agentMode.controls.lifecycle.accepted')
  if (command.status === 'applied') return t('agentMode.controls.lifecycle.applied')
  return t('agentMode.controls.lifecycle.rejected')
})

function effectLabel(template: ControlChallengeTemplate): string {
  const display = props.command?.command.display
  switch (template) {
    case 'issue_priority_set': {
      const value = display && 'priority' in display ? display.priority : 'high'
      return t('agentMode.controls.effect.priority', { priority: t(`agentMode.controls.priority.${value}`) })
    }
    case 'run_cancel_queued': return t('agentMode.controls.effect.cancelQueued')
    case 'run_cancel_running': return t('agentMode.controls.effect.cancelRunning')
    case 'input_approve': return t('agentMode.controls.effect.approve')
    case 'input_reject': return t('agentMode.controls.effect.reject')
    case 'input_choice': {
      const ordinal = display && 'choiceOrdinal' in display ? display.choiceOrdinal : ''
      return t('agentMode.controls.effect.choice', { n: ordinal })
    }
    case 'run_pause': return t('agentMode.controls.effect.pause')
    case 'run_resume': return t('agentMode.controls.effect.resume')
  }
}

const effect = computed(() => props.command ? effectLabel(props.command.command.challengeTemplate) : '')
const reason = computed(() => props.command?.command.reason
  ? t(`agentMode.controls.reason.${props.command.command.reason}`)
  : '')
const stateMessage = computed(() => {
  if (accepted.value && props.state === 'offline') return t('agentMode.controls.offlineAccepted')
  if (props.state === 'offline') return t('agentMode.controls.unavailableOffline')
  if (props.state === 'forbidden' || props.state === 'not-found') return t('agentMode.controls.unavailable')
  if (props.state === 'conflict') return t('agentMode.controls.conflict')
  if (props.state === 'error') return t('agentMode.controls.error')
  return ''
})

const priorityTarget = computed(() => props.targets.find((target): target is Extract<ControlTarget, { action: 'issue.priority.set' }> => (
  target.action === 'issue.priority.set'
)) ?? null)
const directTargets = computed(() => props.targets.filter((target) => target.action !== 'issue.priority.set'))

function targetSignature(target: ControlTarget): string {
  return JSON.stringify(target)
}

function rememberActivator(event: Event) {
  const target = event.currentTarget
  if (target instanceof HTMLElement) returnTarget = target
}

function activatePriority(event: Event) {
  const target = priorityTarget.value
  if (!target) return
  rememberActivator(event)
  emit('activate', { target, priority: priority.value })
}

function activateTarget(target: Exclude<ControlTarget, { action: 'issue.priority.set' }>, event: Event, value?: string) {
  rememberActivator(event)
  if (target.action === 'input.respond') {
    if (target.inputKind === 'approval' && (value === 'approve' || value === 'reject')) {
      emit('activate', { target, response: value })
    } else if (target.inputKind === 'choice') {
      const ordinal = Number(value)
      if (Number.isSafeInteger(ordinal) && ordinal > 0) emit('activate', { target, response: 'choice', choiceOrdinal: ordinal })
    }
    return
  }
  emit('activate', { target } as ControlActivation)
}

function directLabel(target: Exclude<ControlTarget, { action: 'issue.priority.set' }>): string {
  switch (target.action) {
    case 'run.cancel.queued': return t('agentMode.controls.action.cancelQueued')
    case 'run.cancel.running': return t('agentMode.controls.action.cancelRunning')
    case 'run.pause': return t('agentMode.controls.action.pause')
    case 'run.resume': return t('agentMode.controls.action.resume')
    case 'input.respond': return target.inputKind === 'approval'
      ? t('agentMode.controls.action.input')
      : t('agentMode.controls.action.choose')
  }
}

function focusables(): HTMLElement[] {
  if (!challenge.value) return []
  return [...challenge.value.querySelectorAll<HTMLElement>(
    'button:not([disabled]), select:not([disabled]), input:not([disabled]), [tabindex]:not([tabindex="-1"])',
  )].filter((element) => element.getAttribute('aria-hidden') !== 'true')
}

function onChallengeKeydown(event: KeyboardEvent) {
  if (event.key === 'Escape') {
    event.preventDefault()
    if (!props.busy && props.transitionAvailable) emit('withdraw')
    return
  }
  if (event.key !== 'Tab') return
  const items = focusables()
  if (items.length === 0) return
  const first = items[0]
  const last = items[items.length - 1]
  if (event.shiftKey && document.activeElement === first) {
    event.preventDefault()
    last.focus()
  } else if (!event.shiftKey && document.activeElement === last) {
    event.preventDefault()
    first.focus()
  }
}

watch(() => props.focusToken, async () => {
  await nextTick()
  backButton.value?.focus()
})
watch(pending, async (value, previous) => {
  if (!value || previous) return
  await nextTick()
  backButton.value?.focus()
}, { immediate: true })
watch(() => props.focusReturnToken, async () => {
  await nextTick()
  if (returnTarget?.isConnected) returnTarget.focus()
})
watch(() => props.targets, () => {
  // A grant response atomically replaces ephemeral target authority. Do not
  // carry an unsubmitted parameter choice into that replacement.
  priority.value = 'high'
})
</script>

<template>
  <section class="am-controls" :aria-label="t('agentMode.controls.label')">
    <header class="am-controls-head">
      <strong>{{ t('agentMode.controls.title') }}</strong>
      <span>{{ command?.issueKey || selectedIssueKey }}</span>
    </header>

    <div class="am-controls-live" role="status" aria-live="polite" aria-atomic="true">
      <strong v-if="lifecycle">{{ lifecycle }}</strong>
      <span v-if="stateMessage">{{ stateMessage }}</span>
      <span v-else-if="reason">{{ reason }}</span>
      <span v-else-if="state === 'loading' || busy">{{ t('agentMode.controls.loading') }}</span>
      <span v-else-if="!command && targets.length === 0">{{ t('agentMode.controls.none') }}</span>
    </div>

    <section
      v-if="pending && command"
      ref="challenge"
      class="am-controls-challenge"
      role="dialog"
      aria-modal="false"
      :aria-label="t('agentMode.controls.challengeLabel', { delivery: command.deliveryKey, issue: command.issueKey, effect })"
      @keydown="onChallengeKeydown"
    >
      <strong>{{ t('agentMode.controls.challengeTitle') }}</strong>
      <p>{{ t('agentMode.controls.challengeBody', { issue: command.issueKey, effect }) }}</p>
      <p class="am-controls-voice">{{ t('agentMode.controls.voicePhrase', { phrase: voicePhrase }) }}</p>
      <div class="am-controls-challenge-actions">
        <button ref="backButton" type="button" :disabled="busy || !transitionAvailable" @click="emit('withdraw')">
          {{ t('agentMode.controls.back') }}
        </button>
        <button type="button" class="is-consequential" :disabled="busy || !transitionAvailable" @click="emit('confirm')">
          {{ effect }}
        </button>
      </div>
    </section>

    <template v-else-if="!command">
      <div v-if="priorityTarget" class="am-controls-priority">
        <label for="am-control-priority">{{ t('agentMode.controls.action.priority') }}</label>
        <div>
          <select id="am-control-priority" v-model="priority" :disabled="!available">
            <option value="high">{{ t('agentMode.controls.priority.high') }}</option>
            <option value="medium">{{ t('agentMode.controls.priority.medium') }}</option>
            <option value="low">{{ t('agentMode.controls.priority.low') }}</option>
          </select>
          <button type="button" :disabled="!available" @click="activatePriority">
            {{ t('agentMode.controls.action.setPriority') }}
          </button>
        </div>
      </div>

      <div v-for="target in directTargets" :key="targetSignature(target)" class="am-controls-target">
        <template v-if="target.action === 'input.respond' && target.inputKind === 'approval'">
          <span>{{ directLabel(target) }}</span>
          <div>
            <button type="button" :disabled="!available" @click="activateTarget(target, $event, 'approve')">
              {{ t('agentMode.controls.action.approve') }}
            </button>
            <button type="button" :disabled="!available" @click="activateTarget(target, $event, 'reject')">
              {{ t('agentMode.controls.action.reject') }}
            </button>
          </div>
        </template>
        <template v-else-if="target.action === 'input.respond'">
          <span>{{ directLabel(target) }}</span>
          <div class="am-controls-choices">
            <button
              v-for="(code, index) in target.optionCodes"
              :key="code"
              type="button"
              :disabled="!available"
              @click="activateTarget(target, $event, String(index + 1))"
            >{{ index + 1 }}</button>
          </div>
        </template>
        <button v-else type="button" :disabled="!available" @click="activateTarget(target, $event)">
          {{ directLabel(target) }}
        </button>
      </div>
    </template>

    <button v-if="terminal" type="button" class="am-controls-dismiss" @click="emit('dismiss')">
      {{ t('agentMode.controls.dismiss') }}
    </button>
    <button
      v-else-if="!pending && !accepted && ['offline', 'conflict', 'error'].includes(state)"
      type="button"
      class="am-controls-retry"
      :disabled="busy"
      @click="emit('retry')"
    >{{ t('agentMode.controls.retry') }}</button>
  </section>
</template>

<style scoped>
.am-controls {
  display: grid;
  gap: 8px;
  min-width: 0;
  padding: 10px;
  border: 1px solid var(--am-line);
  border-radius: 12px;
  background: var(--am-surface);
  box-shadow: 0 1px 2px color-mix(in srgb, var(--am-ink) 7%, transparent);
  font-size: 11px;
}
.am-controls-head { display: flex; align-items: center; justify-content: space-between; gap: 8px; }
.am-controls-head strong { font-weight: 600; }
.am-controls-head span { color: var(--am-muted); font-family: 'JetBrains Mono', ui-monospace, monospace; font-size: 10px; }
.am-controls-live { display: grid; gap: 2px; min-height: 15px; color: var(--am-muted); line-height: 1.35; }
.am-controls-live strong { color: var(--am-ink); font-weight: 600; }
.am-controls button,
.am-controls select { min-height: 30px; border: 1px solid var(--am-line); border-radius: 8px; background: var(--am-shell); color: var(--am-ink); font: inherit; }
.am-controls button { padding: 5px 8px; cursor: pointer; }
.am-controls button:disabled,
.am-controls select:disabled { cursor: default; opacity: 0.5; }
.am-controls button:focus-visible,
.am-controls select:focus-visible,
.am-controls-challenge:focus-visible { outline: 2px solid var(--am-focus); outline-offset: 2px; }
.am-controls-priority { display: grid; gap: 5px; }
.am-controls-priority > label,
.am-controls-target > span { color: var(--am-muted); }
.am-controls-priority > div { display: grid; grid-template-columns: minmax(0, 1fr) auto; gap: 5px; }
.am-controls-priority select { min-width: 0; padding: 5px 8px; }
.am-controls-target { display: grid; gap: 5px; }
.am-controls-target > div { display: flex; gap: 5px; }
.am-controls-target > button { width: 100%; }
.am-controls-choices { flex-wrap: wrap; }
.am-controls-choices button { min-width: 30px; padding-inline: 7px; }
.am-controls-challenge {
  display: grid;
  gap: 6px;
  padding: 8px;
  border: 1px solid color-mix(in srgb, var(--am-amber) 42%, var(--am-line));
  border-radius: 8px;
  background: color-mix(in srgb, var(--am-amber) 5%, var(--am-shell));
}
.am-controls-challenge > strong { color: var(--am-amber); font-weight: 600; }
.am-controls-challenge p { margin: 0; line-height: 1.4; }
.am-controls-voice { color: var(--am-muted); font-size: 10px; }
.am-controls-challenge-actions { display: flex; justify-content: flex-end; gap: 5px; }
.am-controls-challenge-actions .is-consequential { border-color: var(--am-ink); background: var(--am-ink); color: var(--am-surface); }
.am-controls-dismiss,
.am-controls-retry { justify-self: end; }
@media (prefers-reduced-motion: reduce) {
  .am-controls *, .am-controls *::before, .am-controls *::after { scroll-behavior: auto !important; transition: none !important; }
}
</style>
