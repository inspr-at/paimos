<!--
  PAIMOS — Your Professional & Personal AI Project OS
  Copyright (C) 2026 Markus Barta <markus@barta.com>
  AGPL-3.0-only — see LICENSE.

  PAI-808 — compact, token-native Agent Mode voice controls. This component
  owns presentation only: no transcript/note persistence, routing, network,
  selection, or speech construction occurs here.
-->
<script setup lang="ts">
import { computed, nextTick, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'

import AppIcon from '@/components/AppIcon.vue'
import type { AgentModeVoiceCandidateView, AgentModeVoiceError, AgentModeVoiceNotice } from '@/composables/agent-mode/useAgentModeVoice'
import type { UnsupportedVoiceControl } from '@/composables/agent-mode/agentModeVoiceIntent'
import type { VoiceNoteState } from '@/composables/agent-mode/agentModeVoiceMachine'
import type { MicPermission } from '@/composables/useMicPermission'
import type { MicState } from '@/composables/useMicTranscript'

const props = defineProps<{
  micState: MicState
  micLevel: number
  micSupported: boolean
  permission: MicPermission
  wantsListening: boolean
  micStartPending: boolean
  speechActive: boolean
  authorized: boolean
  audioAvailable: boolean
  voiceRepliesEnabled: boolean
  replyState: 'off' | 'loading' | 'ready'
  draft: string
  candidates: readonly AgentModeVoiceCandidateView[]
  candidateMatchCount: number
  candidateTruncated: boolean
  note: VoiceNoteState | null
  noteTarget: string
  notice: AgentModeVoiceNotice | null
  unsupportedControl: UnsupportedVoiceControl | null
  error: AgentModeVoiceError | null
  busy: boolean
  noteFocusToken: number
  inputResetToken: number
  oneShotWarning: boolean
  compact: boolean
}>()

const emit = defineEmits<{
  toggleMic: []
  setReplies: [enabled: boolean]
  submit: [text: string]
  choose: [index: number]
  confirmNote: []
  cancelNote: []
}>()

const { t } = useI18n()
const typed = ref('')
const notePreview = ref<HTMLElement | null>(null)
const commandInput = ref<HTMLInputElement | null>(null)

const visibleCandidates = computed(() => props.candidates.slice(0, 3))
const micEngaged = computed(() => props.wantsListening
  || props.micState === 'starting'
  || props.micState === 'listening'
  || props.micState === 'transcribing')
const micCapturing = computed(() => props.audioAvailable
  && !props.speechActive
  && props.micState === 'listening')
const micLabel = computed(() => {
  if (!props.authorized) return t('agentMode.voice.mic.revalidating')
  if (!props.audioAvailable) return t('agentMode.voice.mic.offline')
  if (!props.micSupported) return t('agentMode.voice.mic.unsupported')
  if (props.permission === 'denied') return t('agentMode.voice.mic.denied')
  if (props.speechActive && props.wantsListening) return t('agentMode.voice.mic.pausedReply')
  if (props.micStartPending || props.micState === 'starting') return t('agentMode.voice.mic.starting')
  if (props.micState === 'transcribing') return t('agentMode.voice.mic.transcribing')
  if (props.micState === 'listening' || props.wantsListening) return t('agentMode.voice.mic.stop')
  if (props.micState === 'error') return t('agentMode.voice.mic.retry')
  return t('agentMode.voice.mic.start')
})
const replyLabel = computed(() => t(`agentMode.voice.replies.${props.audioAvailable ? props.replyState : 'unavailable'}`))
const statusText = computed(() => {
  if (props.error) return t(`agentMode.voice.error.${props.error}`)
  if (!props.notice) return ''
  if (props.notice === 'command_unsupported' && props.unsupportedControl) {
    return t('agentMode.voice.notice.command_unsupported', {
      control: t(`agentMode.voice.control.${props.unsupportedControl}`),
    })
  }
  return t(`agentMode.voice.notice.${props.notice}`)
})
const confirmable = computed(() => props.note?.status === 'preview' || props.note?.status === 'failed')

function submitTyped() {
  const value = typed.value.trim()
  if (value === '' || !props.authorized) return
  emit('submit', value)
  typed.value = ''
}

async function chooseCandidate(index: number) {
  emit('choose', index)
  await nextTick()
  commandInput.value?.focus()
}

async function confirmNote() {
  emit('confirmNote')
  await nextTick()
  commandInput.value?.focus()
}

async function cancelNote() {
  emit('cancelNote')
  await nextTick()
  commandInput.value?.focus()
}

watch(() => props.noteFocusToken, async () => {
  await nextTick()
  notePreview.value?.focus()
})
watch(() => props.inputResetToken, () => { typed.value = '' }, { flush: 'sync' })
</script>

<template>
  <section class="am-voice" :class="{ 'is-compact': compact }" :aria-label="t('agentMode.voice.label')">
    <div class="am-voice-actions">
      <button
        type="button"
        class="am-voice-mic"
        :class="{ 'is-active': micCapturing }"
        :disabled="!audioAvailable || !micSupported || permission === 'denied'"
        :aria-pressed="micEngaged"
        @click="emit('toggleMic')"
      >
        <AppIcon :name="micEngaged ? 'mic-off' : 'mic'" :size="13" />
        <span>{{ micLabel }}</span>
        <i v-if="micCapturing" class="am-voice-level" :style="{ opacity: String(Math.max(0.3, Math.min(1, micLevel))) }" aria-hidden="true"></i>
      </button>
      <button
        type="button"
        class="am-voice-replies"
        :class="{ 'is-on': voiceRepliesEnabled }"
        :disabled="!audioAvailable"
        :aria-pressed="voiceRepliesEnabled"
        @click="emit('setReplies', !voiceRepliesEnabled)"
      >
        <span>{{ t('agentMode.voice.replies.label') }}</span>
        <small>{{ replyLabel }}</small>
      </button>
    </div>

    <form class="am-voice-form" @submit.prevent="submitTyped">
      <label class="am-voice-sr" for="am-voice-command">{{ t('agentMode.voice.typed.label') }}</label>
      <input
        id="am-voice-command"
        ref="commandInput"
        v-model="typed"
        type="text"
        autocomplete="off"
        :disabled="!authorized"
        :placeholder="t('agentMode.voice.typed.placeholder')"
      />
      <button type="submit" :disabled="typed.trim() === '' || !authorized" :aria-label="t('agentMode.voice.typed.send')">
        <AppIcon name="arrow-right" :size="13" />
      </button>
    </form>

    <p v-if="draft" class="am-voice-draft">
      <span>{{ t('agentMode.voice.draft') }}</span>{{ draft }}
    </p>

    <div class="am-voice-status" role="status" aria-live="polite" aria-atomic="true">
      <span v-if="busy">{{ t('agentMode.voice.busy') }}</span>
      <span v-else>{{ statusText }}</span>
    </div>

    <div v-if="visibleCandidates.length" class="am-voice-candidates">
      <p>
        {{ t('agentMode.voice.candidates', { shown: visibleCandidates.length, total: candidateMatchCount }) }}
        <span v-if="candidateTruncated">{{ t('agentMode.voice.candidatesMore') }}</span>
      </p>
      <ol>
        <li v-for="candidate in visibleCandidates" :key="candidate.deliveryId">
          <button type="button" :disabled="!authorized" @click="chooseCandidate(candidate.index)">
            <strong>{{ candidate.index }}</strong>
            <span><b>{{ candidate.issueKey }}</b> · {{ candidate.title }}</span>
            <small>{{ candidate.lane }}</small>
          </button>
        </li>
      </ol>
    </div>

    <section
      v-if="note"
      ref="notePreview"
      class="am-voice-note"
      tabindex="-1"
      :aria-label="t('agentMode.voice.note.preview')"
    >
      <div class="am-voice-note-head">
        <span>{{ t('agentMode.voice.note.internal') }} · <b>{{ note.binding.issueKey }}</b></span>
        <small>{{ t(`agentMode.voice.note.status.${note.status}`) }}</small>
      </div>
      <p v-if="noteTarget" class="am-voice-note-target">{{ noteTarget }}</p>
      <p class="am-voice-note-body" :tabindex="compact ? 0 : undefined">{{ note.binding.body }}</p>
      <p v-if="oneShotWarning" class="am-voice-note-warning">{{ t('agentMode.detail.nextRunNote') }}</p>
      <div class="am-voice-note-actions">
        <button type="button" :disabled="!authorized || !confirmable" @click="confirmNote">
          {{ t('agentMode.voice.note.confirm') }}
        </button>
        <button type="button" :disabled="!authorized || note.status === 'submitting'" @click="cancelNote">
          {{ t('agentMode.voice.note.cancel') }}
        </button>
      </div>
    </section>
  </section>
</template>

<style scoped>
.am-voice {
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
.am-voice-actions { display: grid; grid-template-columns: minmax(0, 1fr); gap: 6px; }
.am-voice button,
.am-voice input { font: inherit; }
.am-voice button { cursor: pointer; }
.am-voice button:disabled { cursor: default; opacity: 0.5; }
.am-voice button:focus-visible,
.am-voice input:focus-visible,
.am-voice-note:focus-visible { outline: 2px solid var(--am-focus); outline-offset: 2px; }
.am-voice-mic,
.am-voice-replies {
  display: inline-flex;
  align-items: center;
  gap: 5px;
  min-width: 0;
  min-height: 30px;
  padding: 5px 8px;
  border: 1px solid var(--am-line);
  border-radius: 8px;
  background: var(--am-shell);
  color: var(--am-ink);
}
.am-voice-mic.is-active { border-color: color-mix(in srgb, var(--am-green) 48%, var(--am-line)); color: var(--am-green); }
.am-voice-level { width: 5px; height: 5px; margin-left: auto; border-radius: 50%; background: currentColor; }
.am-voice-replies { display: grid; gap: 0; text-align: left; }
.am-voice-replies small { color: var(--am-muted); font-size: 9px; }
.am-voice-replies.is-on { border-color: color-mix(in srgb, var(--am-blue) 45%, var(--am-line)); color: var(--am-blue); }
.am-voice-form { display: grid; grid-template-columns: minmax(0, 1fr) 30px; gap: 5px; }
.am-voice-form input {
  min-width: 0;
  height: 30px;
  padding: 5px 8px;
  border: 1px solid var(--am-line);
  border-radius: 8px;
  background: var(--am-shell);
  color: var(--am-ink);
}
.am-voice-form button {
  display: grid;
  place-items: center;
  border: 1px solid var(--am-line);
  border-radius: 8px;
  background: var(--am-ink);
  color: var(--am-surface);
}
.am-voice-draft { max-height: 54px; margin: 0; overflow: auto; color: var(--am-ink); line-height: 1.35; overflow-wrap: anywhere; }
.am-voice-draft > span { margin-right: 5px; color: var(--am-muted); }
.am-voice-status { min-height: 15px; color: var(--am-muted); line-height: 1.35; }
.am-voice-candidates > p { margin: 0 0 5px; color: var(--am-muted); }
.am-voice-candidates ol { display: grid; gap: 4px; margin: 0; padding: 0; list-style: none; }
.am-voice-candidates button {
  display: grid;
  grid-template-columns: 18px minmax(0, 1fr);
  width: 100%;
  padding: 5px 6px;
  border: 1px solid var(--am-line);
  border-radius: 7px;
  background: var(--am-shell);
  color: var(--am-ink);
  text-align: left;
}
.am-voice-candidates button > strong { grid-row: 1 / span 2; color: var(--am-blue); }
.am-voice-candidates button b,
.am-voice-note-head b { font-family: 'JetBrains Mono', ui-monospace, monospace; }
.am-voice-candidates button > span { min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.am-voice-candidates button > small { color: var(--am-muted); overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.am-voice-note {
  display: grid;
  gap: 6px;
  padding: 8px;
  border: 1px solid color-mix(in srgb, var(--am-amber) 42%, var(--am-line));
  border-radius: 8px;
  background: color-mix(in srgb, var(--am-amber) 5%, var(--am-shell));
}
.am-voice-note-head { display: flex; justify-content: space-between; gap: 8px; color: var(--am-amber); font-weight: 600; }
.am-voice-note-head small { color: var(--am-muted); font-weight: 400; }
.am-voice-note-target { margin: 0; color: var(--am-muted); font-size: 10px; line-height: 1.3; }
.am-voice-note-body { margin: 0; color: var(--am-ink); line-height: 1.4; overflow-wrap: anywhere; white-space: pre-wrap; }
.am-voice.is-compact .am-voice-note-body { max-height: 70px; overflow: auto; }
.am-voice.is-compact .am-voice-status { min-height: 0; }
.am-voice-note-warning { margin: 0; color: var(--am-amber); line-height: 1.35; }
.am-voice-note-actions { display: flex; gap: 5px; justify-content: flex-end; }
.am-voice-note-actions button { padding: 4px 7px; border: 1px solid var(--am-line); border-radius: 7px; background: var(--am-surface); color: var(--am-ink); }
.am-voice-note-actions button:first-child { border-color: var(--am-ink); background: var(--am-ink); color: var(--am-surface); }
.am-voice-sr { position: absolute; width: 1px; height: 1px; margin: -1px; overflow: hidden; clip: rect(0, 0, 0, 0); white-space: nowrap; border: 0; }

</style>
