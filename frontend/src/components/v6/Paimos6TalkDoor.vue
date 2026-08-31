<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, ref, watch } from 'vue'
import { Mic, MicOff, Plus, Sparkles, X } from 'lucide-vue-next'

const props = defineProps<{
  open: boolean
  targetAgent: string | null
  orchestratorLabel: string
  orchestratorStatus: string
  voiceState: string
  voiceMessage: string
  voiceSupported: boolean
  voiceCanRetry: boolean
}>()

const emit = defineEmits<{
  'update:open': [open: boolean]
  status: [message: string]
  'voice-start': []
  'voice-finish': []
  'voice-cancel': []
  'voice-retry': []
}>()

const micMode = ref<'idle' | 'tap' | 'hold'>('idle')
const nodeTitle = ref('')
const localStatus = ref('')
const triggerRef = ref<HTMLButtonElement | null>(null)
const doorRef = ref<HTMLElement | null>(null)
const micRef = ref<HTMLButtonElement | null>(null)
let holdTimer: ReturnType<typeof setTimeout> | null = null
let holdEngaged = false
let suppressClick = false

const routeCopy = computed(() =>
  props.targetAgent
    ? `Preview target · ${props.targetAgent} (selected session)`
    : `Preview target · ${props.orchestratorLabel} (no session selected)`,
)
const voiceActive = computed(() => props.voiceState === 'permission' || props.voiceState === 'listening')
const voiceBusy = computed(() => props.voiceState === 'transcribing' || props.voiceState === 'sending')
const doorStatus = computed(() => localStatus.value || props.voiceMessage)

watch(
  () => props.open,
  async (open) => {
    await nextTick()
    if (open) micRef.value?.focus()
    else triggerRef.value?.focus()
  },
  { flush: 'post' },
)

function closeDoor() {
  emit('update:open', false)
}

function announce(message: string) {
  localStatus.value = message
  emit('status', message)
}

function trapFocus(event: KeyboardEvent) {
  const door = doorRef.value
  if (!door) return
  const focusable = [...door.querySelectorAll<HTMLElement>(
    'button:not([disabled]), input:not([disabled]), select:not([disabled]), summary, [href], [tabindex]:not([tabindex="-1"])',
  )].filter((element) => {
    if (element.closest('[hidden]')) return false
    const closedDetails = element.closest('details:not([open])')
    return !closedDetails || element.tagName === 'SUMMARY'
  })
  if (focusable.length === 0) return

  const first = focusable[0]
  const last = focusable[focusable.length - 1]
  const active = document.activeElement
  if (event.shiftKey && (active === first || !door.contains(active))) {
    event.preventDefault()
    last.focus()
  } else if (!event.shiftKey && active === last) {
    event.preventDefault()
    first.focus()
  }
}

function toggleMic() {
  if (suppressClick) {
    suppressClick = false
    return
  }
  if (!props.voiceSupported || voiceBusy.value) {
    announce('Microphone capture is unavailable in this browser.')
    return
  }
  localStatus.value = ''
  if (voiceActive.value) {
    micMode.value = 'idle'
    emit('voice-finish')
  } else {
    micMode.value = 'tap'
    emit('voice-start')
  }
}

function beginHold(event: PointerEvent) {
  if (!props.voiceSupported || voiceBusy.value) return
  const button = event.currentTarget as HTMLButtonElement
  suppressClick = false
  button.setPointerCapture?.(event.pointerId)
  if (holdTimer) clearTimeout(holdTimer)
  holdTimer = setTimeout(() => {
    holdEngaged = true
    micMode.value = 'hold'
    localStatus.value = ''
    if (!voiceActive.value) emit('voice-start')
  }, 450)
}

function endHold() {
  if (holdTimer) {
    clearTimeout(holdTimer)
    holdTimer = null
  }
  if (!holdEngaged) return
  holdEngaged = false
  suppressClick = true
  micMode.value = 'idle'
  emit('voice-finish')
}

function cancelVoice() {
  localStatus.value = ''
  emit('voice-cancel')
}

function retryVoice() {
  localStatus.value = ''
  emit('voice-retry')
}

function stageNode() {
  const title = nodeTitle.value.trim()
  if (!title) {
    announce('Name the node before staging it in this development preview.')
    return
  }
  announce(`Node “${title}” staged in local preview state only. No mutation endpoint was called.`)
  nodeTitle.value = ''
}

onBeforeUnmount(() => {
  if (holdTimer) clearTimeout(holdTimer)
  if (voiceActive.value || voiceBusy.value) emit('voice-cancel')
})
</script>

<template>
  <button
    v-if="!open"
    ref="triggerRef"
    type="button"
    class="p6-door-trigger"
    aria-label="Open the talk-first door"
    @click="emit('update:open', true)"
  >
    <Plus :size="22" aria-hidden="true" />
  </button>

  <div v-else class="p6-door-layer">
    <button
      type="button"
      class="p6-door-backdrop"
      tabindex="-1"
      aria-label="Close the talk-first door"
      @click="closeDoor"
    ></button>
    <aside
      ref="doorRef"
      class="p6-talk-door"
      role="dialog"
      aria-modal="true"
      aria-labelledby="p6-talk-title"
      @keydown.esc.stop.prevent="closeDoor"
      @keydown.tab="trapFocus"
    >
    <header class="p6-talk-head">
      <div>
        <span class="p6-eyebrow">Talk-first door</span>
        <h2 id="p6-talk-title">What should {{ orchestratorLabel }} do?</h2>
      </div>
      <button type="button" class="p6-close" aria-label="Close the talk-first door" @click="closeDoor">
        <X :size="18" aria-hidden="true" />
      </button>
    </header>

    <section class="p6-orchestrator" aria-labelledby="p6-orchestrator-title">
      <div class="p6-orchestrator-identity">
        <span class="p6-orchestrator-orb" aria-hidden="true"><Sparkles :size="17" /></span>
        <div>
          <h3 id="p6-orchestrator-title">{{ orchestratorLabel }}</h3>
          <p>{{ routeCopy }}</p>
          <p class="p6-orchestrator-status">{{ orchestratorStatus }}</p>
        </div>
      </div>

      <button
        ref="micRef"
        type="button"
        class="p6-mic"
        :class="{ 'is-active': voiceActive || micMode === 'hold' }"
        :aria-pressed="voiceActive"
        :aria-disabled="!voiceSupported || voiceBusy"
        aria-describedby="p6-mic-help p6-mic-truth"
        @click="toggleMic"
        @pointerdown.left="beginHold"
        @pointerup.left="endHold"
        @pointercancel="endHold"
        @lostpointercapture="endHold"
      >
        <MicOff v-if="!voiceActive" :size="25" aria-hidden="true" />
        <Mic v-else :size="25" aria-hidden="true" />
        <span>{{ voiceBusy ? 'Finalizing…' : micMode === 'hold' ? 'Holding · release to send' : voiceActive ? 'Listening · tap to send' : `Talk to ${orchestratorLabel}` }}</span>
      </button>
      <p id="p6-mic-help" class="p6-mic-help"><strong>Tap</strong> to toggle · <strong>hold</strong> to talk, release to stop</p>
      <p id="p6-mic-truth" class="p6-mic-truth">
        Raw audio stays ephemeral and is sent only to transcription. The finalized transcript is delivered once to the selected authorized agent, or to Paimos when no session is selected.
      </p>
      <div class="p6-voice-actions">
        <button v-if="voiceActive" type="button" @click="cancelVoice">Cancel capture</button>
        <button v-if="voiceCanRetry" type="button" @click="retryVoice">Retry same utterance</button>
      </div>
    </section>

      <p class="p6-door-status" role="status" aria-live="polite" aria-atomic="true">{{ doorStatus }}</p>

      <details class="p6-node-door">
      <summary>
        <span>Human node form</span>
        <small>Secondary · the 1% door</small>
      </summary>
      <form @submit.prevent="stageNode">
        <label for="p6-node-title">Node title</label>
        <input id="p6-node-title" v-model="nodeTitle" type="text" autocomplete="off" placeholder="A small piece of work" />
        <label for="p6-node-parent">Parent</label>
        <select id="p6-node-parent" disabled>
          <option>Parent selection unavailable in this preview</option>
        </select>
        <button type="submit">Stage locally</button>
        <p>Local preview state only. No API request and no saved node.</p>
      </form>
      </details>
    </aside>
  </div>
</template>

<style scoped>
.p6-door-trigger {
  position: fixed;
  top: 50%;
  right: 22px;
  z-index: 8;
  display: grid;
  width: 42px;
  height: 42px;
  place-items: center;
  border: 1px solid #c7d5cc;
  border-radius: 50%;
  color: #284f3d;
  background: rgba(248, 251, 248, 0.92);
  box-shadow: 0 10px 30px rgba(34, 61, 47, 0.13);
  transform: translateY(-50%);
}
.p6-door-trigger:hover { background: #fff; border-color: #89a795; }
.p6-door-trigger:focus-visible,
.p6-talk-door button:focus-visible,
.p6-node-door summary:focus-visible,
.p6-node-door input:focus-visible,
.p6-node-door select:focus-visible {
  outline: 3px solid rgba(47, 107, 82, 0.3);
  outline-offset: 3px;
}
.p6-door-layer {
  position: fixed;
  inset: 0;
  z-index: 9;
}
.p6-door-backdrop {
  position: absolute;
  inset: 0;
  width: 100%;
  height: 100%;
  padding: 0;
  border: 0;
  background: rgba(25, 38, 31, 0.2);
}
.p6-talk-door {
  position: absolute;
  inset: 78px 18px 18px auto;
  z-index: 1;
  width: min(410px, calc(100vw - 36px));
  overflow-y: auto;
  padding: 24px;
  border: 1px solid #d3ded7;
  border-radius: 22px;
  color: #1d2723;
  background: rgba(252, 253, 250, 0.97);
  box-shadow: 0 24px 70px rgba(28, 48, 38, 0.16);
}
.p6-talk-head { display: flex; align-items: flex-start; justify-content: space-between; gap: 18px; }
.p6-eyebrow { color: #59655e; font-size: 9px; font-weight: 750; letter-spacing: 0.11em; text-transform: uppercase; }
.p6-talk-head h2 { margin-top: 4px; font-family: "Bricolage Grotesque", "DM Sans", sans-serif; font-size: 23px; font-weight: 600; letter-spacing: -0.035em; }
.p6-close { display: grid; width: 34px; height: 34px; place-items: center; border: 1px solid #dce4df; border-radius: 10px; color: #637068; background: #fff; }
.p6-orchestrator { margin-top: 24px; padding: 18px; border: 1px solid #d8e3dc; border-radius: 17px; background: linear-gradient(145deg, #f0f7f2, #fbfcfa 72%); }
.p6-orchestrator-identity { display: flex; align-items: center; gap: 11px; }
.p6-orchestrator-orb { display: grid; width: 38px; height: 38px; place-items: center; border-radius: 50%; color: #eff8f2; background: #315e49; box-shadow: inset 0 0 0 5px rgba(255, 255, 255, 0.12); }
.p6-orchestrator h3 { font: 600 16px/1.1 "Bricolage Grotesque", "DM Sans", sans-serif; }
.p6-orchestrator-identity p { margin-top: 3px; color: #59655e; font: 500 10px/1.35 "JetBrains Mono", monospace; }
.p6-orchestrator-identity .p6-orchestrator-status { color: #42584c; font-family: "DM Sans", sans-serif; }
.p6-mic { display: flex; width: 100%; min-height: 76px; align-items: center; justify-content: center; gap: 10px; margin-top: 20px; border: 1px solid #aac2b3; border-radius: 15px; color: #284f3d; background: rgba(255, 255, 255, 0.83); font: 650 13px/1 "DM Sans", sans-serif; }
.p6-mic:hover { border-color: #6e9680; background: #fff; }
.p6-mic.is-active { color: #f4faf6; background: #315e49; box-shadow: 0 0 0 5px rgba(49, 94, 73, 0.1); }
.p6-mic[aria-disabled="true"] { cursor: not-allowed; opacity: 0.58; }
.p6-mic-help { margin-top: 10px; color: #536159; font-size: 11px; text-align: center; }
.p6-mic-truth { margin-top: 8px; color: #59655e; font-size: 10.5px; line-height: 1.55; text-align: center; }
.p6-voice-actions { display: flex; justify-content: center; gap: 8px; margin-top: 10px; }
.p6-voice-actions button { padding: 6px 9px; border: 1px solid #aac2b3; border-radius: 8px; color: #315e49; background: #fff; font-size: 10px; }
.p6-door-status { margin-top: 14px; padding: 9px 10px; border: 1px solid #d8e1db; border-radius: 9px; color: #59655e; background: #f7f9f7; font-size: 10.5px; line-height: 1.45; }
.p6-node-door { margin-top: 22px; border-top: 1px solid #e0e7e2; }
.p6-node-door summary { display: flex; align-items: center; justify-content: space-between; padding: 18px 2px 12px; color: #536159; cursor: pointer; font-size: 11.5px; font-weight: 650; }
.p6-node-door summary small { color: #59655e; font-size: 9.5px; font-weight: 600; }
.p6-node-door form { display: grid; gap: 7px; padding: 4px 2px 2px; }
.p6-node-door label { margin-top: 6px; color: #66736b; font-size: 10.5px; font-weight: 650; }
.p6-node-door input,
.p6-node-door select { min-height: 38px; border-color: #d8e1db; border-radius: 9px; background: #fff; font-size: 11.5px; }
.p6-node-door button[type="submit"] { min-height: 38px; margin-top: 7px; border: 1px solid #315e49; border-radius: 9px; color: #f6faf7; background: #315e49; font-weight: 650; }
.p6-node-door form p { color: #59655e; font-size: 10px; text-align: center; }

@media (max-width: 680px) {
  .p6-door-trigger { right: 14px; bottom: 20px; top: auto; transform: none; }
  .p6-talk-door { inset: 66px 8px 8px; width: auto; padding: 20px; border-radius: 19px; }
}

@media (prefers-reduced-motion: reduce) {
  .p6-door-trigger { transition: none; }
}
</style>
