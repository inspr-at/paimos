<!--
  PAIMOS — Your Professional & Personal AI Project OS
  Copyright (C) 2026 Markus Barta <markus@barta.com>
  Licensed under AGPL-3.0-only; see LICENSE.
-->
<script setup lang="ts">
// Voice Intake — Spec Workbench (PAI-704, epic PAI-703). Slice 1: session
// core with the dev text transcript source, live-editable spec, checkpoints
// and time-travel. AI generation, project detection, impact analysis, ELIs
// and one-click create land in later slices (PAI-705…709); their cards are
// scaffolded here so the layout already matches the approved mockup.
import { computed, nextTick, onBeforeUnmount, onMounted, ref, watch } from "vue";
import { useRouter } from "vue-router";

import AiSurfaceFeedback from "@/components/ai/AiSurfaceFeedback.vue";
import AppIcon from "@/components/AppIcon.vue";
import ProjectConfidenceChip from "@/components/intake/ProjectConfidenceChip.vue";
import { LS_INTAKE_ELI_LEVEL, LS_INTAKE_TTS_MUTED } from "@/constants/storage";
import TranscriptInput from "@/components/intake/TranscriptInput.vue";
import { useIntakeSession } from "@/composables/useIntakeSession";
import { useMarkdown } from "@/composables/useMarkdown";
import IntakeCard from "@/components/intake/IntakeCard.vue";
import { useIssuePreview } from "@/composables/useIssuePreview";
import { createIntakeTtsPlayback } from "@/composables/useIntakeTtsPlayback";
import { useMicPermission } from "@/composables/useMicPermission";
import { useMicTranscript } from "@/composables/useMicTranscript";
import { postIntakeAudio, voiceAvailable } from "@/api/intake";
import { lineDiff } from "@/components/ai/lineDiff";

const {
  session,
  connection,
  transcript,
  spec,
  specSeq,
  ticketPreview,
  stage,
  projectMatch,
  pinProject,
  impacts,
  summaries,
  summariesSeq,
  createdIssue,
  createIssue,
  activeProjectId,
  checkpoints,
  viewSeq,
  viewState,
  isViewingHistory,
  error,
  start,
  resume,
  sendTranscript,
  saveSpec,
  setLanguage,
  saveCheckpoint,
  scheduleScrub,
  restore,
  reset,
  disconnect,
} = useIntakeSession();

const talking = ref(false);
const previewEnabled = ref(true);
const eliLevel = ref<"eli5" | "eli10" | "eli15">(
  (localStorage.getItem(LS_INTAKE_ELI_LEVEL) as "eli5" | "eli10" | "eli15") || "eli10",
);
watch(eliLevel, (lvl) => localStorage.setItem(LS_INTAKE_ELI_LEVEL, lvl));
const specDraft = ref("");
const draftDirty = ref(false);
const draftBaseSeq = ref(0);
const checkpointLabel = ref("");
const showCheckpointInput = ref(false);
const scrubPos = ref(0);

// The editor mirrors the live spec until the user diverges; then the draft
// wins: incoming generations advance HEAD but never touch the textarea.
// A banner offers "Apply latest" / the draft is saved on blur ("keep mine").
watch(spec, (next) => {
  if (!draftDirty.value) specDraft.value = next?.markdown ?? "";
});

const pendingGeneration = computed(
  () => draftDirty.value && specSeq.value > draftBaseSeq.value,
);

const stageLabel = computed(() => {
  const st = stage.value;
  if (!st) return "";
  if (st.state === "running") return "Generating specification…";
  if (st.state === "degraded") {
    switch (st.reason) {
      case "unconfigured":
        return "AI assist is not configured — capture and manual editing still work.";
      case "daily_cap":
        return "Daily AI budget exhausted — spec frozen, capture still works.";
      case "session_budget":
        return "This session hit its AI budget — capture and editing still work. Raise the budget under Settings → System, or start a new session.";
      default:
        return `AI degraded (${st.reason ?? "unknown"}) — capture still works.`;
    }
  }
  if (st.state === "error") return `Generation failed (${st.reason ?? "error"}) — retrying on next input.`;
  return "";
});

async function onToggleLanguage(lang: "en" | "de") {
  if (!session.value || session.value.language === lang) return;
  await setLanguage(lang);
}

function applyLatestGeneration() {
  specDraft.value = spec.value?.markdown ?? "";
  draftDirty.value = false;
  draftBaseSeq.value = specSeq.value;
}

const displayedMarkdown = computed(() =>
  isViewingHistory.value ? (viewState.value?.spec?.markdown ?? "") : specDraft.value,
);
const displayedTranscript = computed(() =>
  isViewingHistory.value ? (viewState.value?.transcript ?? "") : transcript.value,
);
const mdSource = computed(() => displayedMarkdown.value);
const previewOn = computed(() => previewEnabled.value || isViewingHistory.value);
const { html: previewHtml } = useMarkdown(mdSource, previewOn);

const headRev = computed(() => session.value?.rev ?? 0);
const connectionLabel = computed(() => {
  if (talking.value && !textMode.value) {
    if (mic.state.value === "transcribing") return "Transcribing…";
    if (mic.state.value === "listening") return "Listening";
    if (mic.state.value === "starting") return "Mic starting…";
  }
  switch (connection.value) {
    case "open":
      return talking.value ? "Listening" : "Connected";
    case "connecting":
      return "Connecting…";
    case "retrying":
      return "Reconnecting…";
    default:
      return "Offline";
  }
});

// PAI-710: continuous-listening microphone (ElevenLabs STT server-side).
// Preference order: mic when the instance has voice configured AND the
// browser supports capture; the typed input is always one click away and
// is the automatic fallback on any mic failure.
const mic = useMicTranscript();
const voiceReady = ref(false);
const textMode = ref(false);
void voiceAvailable().then((ok) => {
  voiceReady.value = ok;
  if (!ok || !mic.micSupported()) textMode.value = true;
});

// PAI-715: permanent, live-updating mic-permission status. The chip is
// always visible while voice is configured, so "can speech detection
// work right now?" is answered at a glance.
const micPerm = useMicPermission();
void micPerm.init();
const permHint = ref(false);
const micPermMeta = computed(() => {
  switch (micPerm.permission.value) {
    case "granted":
      return { cls: "vi-perm--ok", label: "Mic allowed", action: null as string | null };
    case "prompt":
      return { cls: "vi-perm--ask", label: "Mic not enabled", action: "Enable" };
    case "denied":
      return { cls: "vi-perm--denied", label: "Mic blocked", action: "Re-check" };
    default:
      return { cls: "vi-perm--unknown", label: "Mic status unknown", action: "Check" };
  }
});
watch(micPerm.permission, (state) => {
  if (state === "granted") permHint.value = false;
});

async function onPermAction() {
  const state = micPerm.permission.value;
  if (state === "prompt" || state === "unknown") {
    const result = await micPerm.requestAccess();
    permHint.value = result === "denied";
    // Permission just granted mid-session: leave text mode if we only
    // fell back because of a denial.
    if (result === "granted" && mic.errorMessage.value) {
      mic.errorMessage.value = null;
      textMode.value = false;
    }
  } else {
    const result = await micPerm.recheck();
    permHint.value = result === "denied";
    if (result === "granted") {
      mic.errorMessage.value = null;
      textMode.value = false;
    }
  }
}
const micActive = mic.isActive;
const micLevel = mic.level;

// PAI-719: the talk button must never lie. If the mic module leaves the
// active states while voice mode is on (real device error, permission
// revoked mid-session), the button follows immediately.
watch(mic.state, (st) => {
  if (talking.value && !textMode.value && (st === "idle" || st === "error")) {
    talking.value = false;
  }
});

async function onStartTalking() {
  stopSpeaking(false); // barge-in: this explicit start owns mic resumption
  // PAI-719: a completed/abandoned session cannot take input — Start
  // Talking begins a fresh one, carrying the project target forward.
  if (session.value && session.value.status !== "active") {
    await onNewSession();
  } else if (!session.value) {
    await start();
  } else if (connection.value === "disconnected") {
    // Returning to a live session whose stream was closed on unmount.
    await resume(session.value.id);
  }
  talking.value = true;
  if (!textMode.value && voiceReady.value && mic.micSupported()) {
    const ok = await startMicCapture();
    if (!ok) textMode.value = true; // permission denied → typed input
  }
}

async function startMicCapture(): Promise<boolean> {
  return mic.start(async (blob) => {
      const s = session.value;
      if (!s || s.status !== "active") {
        mic.stop();
        talking.value = false;
        return;
      }
      try {
        await postIntakeAudio(s.id, blob);
      } catch (e) {
        const msg = e instanceof Error ? e.message : "voice transcription failed";
        if (msg.includes("not active")) {
          // Session ended under us (issue created elsewhere / swept):
          // stop listening cleanly; the next Start Talking begins fresh.
          mic.stop();
          talking.value = false;
          error.value = "This session has ended — Start Talking begins a new one.";
        } else {
          error.value = msg;
        }
      }
    });
}

function onStopTalking() {
  talking.value = false;
  mic.stop();
}

function onToggleTextMode() {
  textMode.value = !textMode.value;
  if (textMode.value) mic.stop();
  else if (talking.value) void onStartTalking();
}

async function onChunk(text: string) {
  if (!session.value) await start();
  await sendTranscript(text);
}

function onSpecInput() {
  if (!draftDirty.value) draftBaseSeq.value = specSeq.value;
  draftDirty.value = true;
}

async function onSaveSpec() {
  await saveSpec(specDraft.value);
  draftDirty.value = false;
  draftBaseSeq.value = specSeq.value;
}

async function onSaveCheckpoint() {
  const label = checkpointLabel.value.trim() || `Checkpoint ${checkpoints.value.length + 1}`;
  await saveCheckpoint(label);
  checkpointLabel.value = "";
  showCheckpointInput.value = false;
}

watch(scrubPos, (pos) => {
  if (!session.value) return;
  scheduleScrub(pos >= headRev.value ? null : pos);
});

watch(headRev, (rev) => {
  if (!isViewingHistory.value) scrubPos.value = rev;
});

async function onRestore() {
  if (viewSeq.value === null) return;
  await restore(viewSeq.value);
  scrubPos.value = headRev.value;
}

// PAI-715: collapsed-card summaries + hover previews + transcript autoscroll.
const preview = useIssuePreview();
const topImpactChips = computed(() => {
  const im = impacts.value;
  if (!im) return [];
  const pool = im.impacted.length ? im.impacted : im.related;
  return pool.slice(0, 3);
});
const eliSummary = computed(() => {
  const text = summaries.value?.[eliLevel.value] ?? "";
  return text.length > 90 ? text.slice(0, 90) + "…" : text;
});
const transcriptTail = computed(() => {
  const t = displayedTranscript.value;
  return t.length > 60 ? "…" + t.slice(-60) : t;
});
const transcriptEl = ref<HTMLElement | null>(null);
watch(displayedTranscript, async () => {
  await nextTick();
  const el = transcriptEl.value;
  if (el) el.scrollTop = el.scrollHeight; // transcript grows at the bottom
});

// PAI-714: speak the selected ELI summary after each update. Muted by
// default (nobody gets surprise audio); persisted. The mic/TTS interlock
// suspends capture while audio plays so the workbench never transcribes
// its own voice, then resumes listening.
const ttsMuted = ref(localStorage.getItem(LS_INTAKE_TTS_MUTED) !== "0");
watch(ttsMuted, (m) => localStorage.setItem(LS_INTAKE_TTS_MUTED, m ? "1" : "0"));
let lastSpokenText = "";
const ttsPlayback = createIntakeTtsPlayback({
  micActive: mic.isActive,
  stopMic: mic.stop,
  canResumeMic: () => talking.value && !textMode.value,
  resumeMic: () => void startMicCapture(),
});

function stopSpeaking(resumeMic = true) {
  ttsPlayback.cancel(resumeMic);
}

async function speakSelectedEli() {
  const s = session.value;
  const text = summaries.value?.[eliLevel.value]?.trim() ?? "";
  if (!s || ttsMuted.value || !voiceReady.value || text === "" || text === lastSpokenText) return;
  lastSpokenText = text;
  await ttsPlayback.play(async () => {
    const res = await fetch(`/api/intake/sessions/${s.id}/tts`, {
      method: "POST",
      credentials: "include",
      headers: {
        "Content-Type": "application/json",
        "X-CSRF-Token":
          document.cookie.split("; ").find((c) => c.startsWith("csrf_token="))?.split("=")[1] ?? "",
      },
      body: JSON.stringify({ level: eliLevel.value }),
    });
    if (!res.ok) throw new Error(`tts ${res.status}`);
    return res.blob();
  }); // failures degrade silently — speech is a bonus, never a blocker
}

watch([summaries, eliLevel, ttsMuted], () => {
  void speakSelectedEli();
});

// PAI-721: empty-state hero — the card teaches the interaction model
// before any content exists, and clicking it starts talking.
const showHero = computed(
  () => !isViewingHistory.value && displayedMarkdown.value.trim() === "" && !draftDirty.value,
);
async function onHeroClick() {
  if (!talking.value) await onStartTalking();
}

const showDiff = ref(false);
// Flattened line diff of the scrubbed revision vs the live spec, capped so
// a pathological diff can't lock the UI.
const historyDiff = computed(() => {
  if (!isViewingHistory.value || !showDiff.value) return [];
  const result = lineDiff(viewState.value?.spec?.markdown ?? "", spec.value?.markdown ?? "");
  const lines: { type: string; text: string }[] = [];
  for (const row of result.left) {
    if (row.type === "del") lines.push({ type: "del", text: row.text ?? "" });
  }
  for (const row of result.right) {
    if (row.type === "add") lines.push({ type: "add", text: row.text ?? "" });
    else if (row.type === "eq") lines.push({ type: "eq", text: row.text ?? "" });
  }
  return lines.slice(0, 400);
});

const creating = ref(false);
const canCreate = computed(
  () =>
    !!session.value &&
    session.value.status === "active" &&
    activeProjectId() != null &&
    (specDraft.value.trim() !== "" || !!ticketPreview.value),
);

async function onCreateIssue() {
  if (creating.value || !canCreate.value) return;
  creating.value = true;
  try {
    if (draftDirty.value) await onSaveSpec();
    await createIssue();
    // PAI-719: the session is completed now — release the microphone.
    mic.stop();
    talking.value = false;
  } finally {
    creating.value = false;
  }
}

const router = useRouter();

function onJumpToIssue() {
  const issue = createdIssue.value;
  if (!issue) return;
  const pid = issue.project_id;
  router.push(pid != null ? `/projects/${pid}/issues/${issue.id}` : `/issues/${issue.id}`);
}

async function onNewSession() {
  const carryProject = activeProjectId();
  talking.value = false;
  draftDirty.value = false;
  specDraft.value = "";
  reset();
  await start();
  // Non-destructive continuity: the next idea usually belongs to the same
  // project — carry the previous target forward as a pin the user can lift.
  if (carryProject != null) await pinProject(carryProject);
}

// PAI-719: mount/unmount lifecycle. The composable state is a module
// singleton so the session survives navigation — the STREAM and the MIC
// must not. On return, reconnect + rehydrate so updates flow again (the
// missing piece behind "it listens but the spec stops updating"); on
// leave, hard-stop the microphone (never leave it hot) and close the
// stream.
onMounted(async () => {
  if (mic.isActive.value && !talking.value) mic.stop(); // stray from a previous visit
  const s = session.value;
  if (s && connection.value === "disconnected") {
    try {
      await resume(s.id);
    } catch {
      /* session may have been swept — start() will make a new one */
    }
  }
});

onBeforeUnmount(() => {
  talking.value = false;
  stopSpeaking(false);
  mic.stop();
  disconnect();
});
</script>

<template>
  <div class="vi-page">
    <!-- PAI-735: the workbench toolbar lives in the app header — one
         chrome row instead of a stacked page toolbar. Scoped styles
         still apply to teleported nodes (data-v attributes travel). -->
    <Teleport defer to="#app-header-left">
      <div class="vi-title">
        <span class="vi-crumb">Voice Intake</span>
        <span class="vi-crumb-sep">/</span>
        <strong>Spec Workbench</strong>
      </div>
      <ProjectConfidenceChip
        :session="session"
        :match="projectMatch"
        @pin="(id: number) => pinProject(id)"
        @unpin="pinProject(0)"
      />
    </Teleport>
    <Teleport defer to="#app-header-right">
      <div class="vi-talk">
        <button
          v-if="!talking"
          class="vi-talk-btn vi-talk-start"
          type="button"
          @click="onStartTalking"
        >
          ● Start Talking
        </button>
        <button
          v-else
          class="vi-talk-btn vi-talk-stop"
          type="button"
          :style="micActive ? { boxShadow: `0 0 0 ${4 + micLevel * 14}px rgba(185,28,28,0.18)` } : undefined"
          @click="onStopTalking"
        >
          ■ Stop Talking
        </button>
        <span class="vi-conn" :class="`vi-conn--${connection}`">{{ connectionLabel }}</span>
        <span v-if="voiceReady" class="vi-perm" :class="micPermMeta.cls" :title="micPermMeta.label">
          <AppIcon :name="micPerm.permission.value === 'granted' ? 'mic' : 'mic-off'" :size="13" />
          <button
            v-if="micPermMeta.action"
            class="vi-perm-action"
            type="button"
            @click="onPermAction"
          >
            {{ micPermMeta.action }}
          </button>
        </span>
        <button v-if="voiceReady" class="vi-link vi-mode-toggle" type="button" @click="onToggleTextMode">
          {{ textMode ? "🎤 use microphone" : "⌨ type instead" }}
        </button>
      </div>
    </Teleport>

    <p v-if="error" class="vi-error">{{ error }}</p>
    <!-- Shared AI feedback host for any useAiAction-backed control on this
         page; generation activity itself streams via the session SSE. -->
    <AiSurfaceFeedback host-key="intake:workbench" />

    <div class="vi-grid">
      <section class="vi-card vi-spec">
        <header class="vi-card-head">
          <h2>Live Specification</h2>
          <div class="vi-spec-tools">
            <div class="vi-tabs vi-lang" role="tablist" aria-label="Specification language">
              <button
                :class="{ active: (session?.language ?? 'en') === 'en' }"
                :disabled="!session"
                @click="onToggleLanguage('en')"
              >
                EN
              </button>
              <button
                :class="{ active: session?.language === 'de' }"
                :disabled="!session"
                @click="onToggleLanguage('de')"
              >
                DE
              </button>
            </div>
            <div class="vi-tabs" role="tablist">
              <button
                role="tab"
                :aria-selected="!previewOn"
                :class="{ active: !previewOn }"
                :disabled="isViewingHistory"
                @click="previewEnabled = false"
              >
                Edit
              </button>
              <button
                role="tab"
                :aria-selected="previewOn"
                :class="{ active: previewOn }"
                @click="previewEnabled = true"
              >
                Preview
              </button>
            </div>
          </div>
        </header>
        <p v-if="stageLabel" class="vi-stage" :class="`vi-stage--${stage?.state}`">{{ stageLabel }}</p>
        <div v-if="pendingGeneration && !isViewingHistory" class="vi-history-banner">
          A newer generated specification is available (rev {{ specSeq }}) —
          <button class="vi-link" type="button" @click="applyLatestGeneration">apply latest</button>
          or keep editing (your text is saved on blur and becomes the basis for the next generation).
        </div>
        <div v-if="isViewingHistory" class="vi-history-banner">
          Viewing rev {{ viewSeq }} of {{ headRev }} —
          <button class="vi-link" type="button" @click="scrubPos = headRev">back to live</button>
          ·
          <button class="vi-link" type="button" @click="onRestore">restore this state</button>
          ·
          <button class="vi-link" type="button" @click="showDiff = !showDiff">
            {{ showDiff ? "hide diff" : "diff vs live" }}
          </button>
        </div>
        <div v-if="isViewingHistory && showDiff" class="vi-diff">
          <template v-for="(line, i) in historyDiff" :key="i">
            <div class="vi-diff-line" :class="`vi-diff--${line.type}`">
              <span class="vi-diff-sign">{{ line.type === "add" ? "+" : line.type === "del" ? "−" : " " }}</span>
              <span>{{ line.text }}</span>
            </div>
          </template>
        </div>
        <div
          v-else-if="showHero && previewOn"
          class="vi-hero"
          role="button"
          tabindex="0"
          @click="onHeroClick"
          @keydown.enter="onHeroClick"
        >
          <div class="vi-hero-mark" :class="{ 'vi-hero-mark--live': talking }">
            <svg viewBox="0 0 120 48" class="vi-hero-wave" aria-hidden="true">
              <path
                d="M4 24 q6 -14 12 0 t12 0 q4 -22 8 0 t8 0 q6 -10 12 0 t12 0 q4 -18 8 0 t8 0 q6 -8 12 0 t12 0"
                fill="none"
                stroke="currentColor"
                stroke-width="2.5"
                stroke-linecap="round"
              />
            </svg>
            <span class="vi-hero-mic"><AppIcon name="mic" :size="15" /></span>
          </div>
          <h3 class="vi-hero-title">{{ talking ? "Listening…" : "Ready when you are" }}</h3>
          <p class="vi-hero-sub">
            {{ talking
              ? "Speak naturally — each pause sends an utterance and the specification builds here."
              : voiceReady && !textMode
                ? "Start talking to capture requirements. The microphone listens continuously."
                : "Start talking (typed input) to capture requirements." }}
          </p>
          <p v-if="permHint" class="vi-hero-hint">
            💡 The browser has the microphone blocked for this site and will not ask again by
            itself — allow it in the site settings (icon left of the address bar) and it
            re-enables here automatically.
          </p>
          <p v-else-if="mic.errorMessage.value" class="vi-hero-hint">
            💡 {{ mic.errorMessage.value }}
          </p>
        </div>
        <textarea
          v-else-if="!previewOn"
          v-model="specDraft"
          v-auto-grow
          class="vi-spec-editor"
          rows="14"
          placeholder="The specification builds here while you talk — or edit it directly."
          @input="onSpecInput"
          @blur="draftDirty && onSaveSpec()"
        />
        <div v-else-if="!(isViewingHistory && showDiff)" class="vi-spec-preview md-rendered" v-html="previewHtml" />
        <div v-if="draftDirty && !isViewingHistory" class="vi-draft-note">
          Unsaved edits — <button class="vi-link" type="button" @click="onSaveSpec">save</button>
        </div>
        <footer class="vi-spec-foot">
          <TranscriptInput v-if="talking && (textMode || !voiceReady)" :disabled="isViewingHistory" @chunk="onChunk" />
          <p v-else-if="talking" class="vi-hint">
            🎤 Speak naturally — each pause sends an utterance for transcription.
          </p>
          <p v-else-if="!showHero" class="vi-hint">
            {{ voiceReady ? "Start talking to build the spec — the mic listens continuously." : "Start talking (typed input) to build the spec." }}
          </p>
        </footer>
      </section>

      <aside class="vi-side">
        <IntakeCard id="impact" title="Impact Analysis" icon="bar-chart-2">
          <template #summary>
            <template v-if="topImpactChips.length">
              <button
                v-for="e in topImpactChips"
                :key="e.issue_key"
                class="vi-impact-key vi-chipbtn"
                :class="`vi-impact--${e.category}`"
                type="button"
                @mouseenter="e.issue_id && preview.showPreview(e.issue_id, $event)"
                @mouseleave="preview.hidePreview()"
                @click="router.push(`/issues/${e.issue_key}`)"
              >
                {{ e.issue_key }}
              </button>
            </template>
            <span v-else>No hits yet</span>
          </template>
          <template v-if="impacts && (impacts.impacted.length || impacts.related.length || impacts.graph_hits.length)">
            <div v-for="cat in ['touches', 'conflicts', 'extends']" :key="cat">
              <template v-if="impacts.impacted.some((e) => e.category === cat)">
                <p class="vi-impact-group">{{ cat }}</p>
                <div class="vi-impact-rows">
                  <RouterLink
                    v-for="e in impacts.impacted.filter((x) => x.category === cat)"
                    :key="e.issue_key"
                    :to="`/issues/${e.issue_key}`"
                    class="vi-impact-row"
                    :title="`filed as '${e.mapped_relation}' on create · via ${e.via}`"
                    @mouseenter="e.issue_id && preview.showPreview(e.issue_id, $event)"
                    @mouseleave="preview.hidePreview()"
                  >
                    <span class="vi-impact-key" :class="`vi-impact--${cat}`">{{ e.issue_key }}</span>
                    <span class="vi-impact-title">{{ e.title }}</span>
                  </RouterLink>
                </div>
              </template>
            </div>
            <template v-if="impacts.related.length">
              <p class="vi-impact-group">related context</p>
              <div class="vi-impact-rows">
                <RouterLink
                  v-for="e in impacts.related.slice(0, 6)"
                  :key="e.issue_key"
                  :to="`/issues/${e.issue_key}`"
                  class="vi-impact-row"
                  @mouseenter="e.issue_id && preview.showPreview(e.issue_id, $event)"
                  @mouseleave="preview.hidePreview()"
                >
                  <span class="vi-impact-key">{{ e.issue_key }}</span>
                  <span class="vi-impact-title">{{ e.title }}</span>
                </RouterLink>
              </div>
            </template>
            <template v-if="impacts.graph_hits.length">
              <p class="vi-impact-group">knowledge-graph hits</p>
              <p v-for="(g, i) in impacts.graph_hits" :key="i" class="vi-impact-graphhit">
                {{ g.entity_type }} · {{ g.title }}
              </p>
            </template>
          </template>
          <p v-else class="vi-empty">
            Impacted and related issues appear here once a project is detected or pinned.
          </p>
        </IntakeCard>

        <IntakeCard id="understanding" title="Understanding Check" icon="shield-check">
          <template #summary>
            <span class="vi-sum-text">{{ eliSummary || "Appears after the first generation" }}</span>
          </template>
          <template #headerExtra>
            <button
              v-if="voiceReady"
              class="vi-tts-toggle"
              type="button"
              :title="ttsMuted ? 'Unmute — speak the summary after updates' : 'Mute speak-back'"
              @click="ttsMuted = !ttsMuted"
            >
              {{ ttsMuted ? "🔇" : "🔊" }}
            </button>
            <div v-if="summaries" class="vi-tabs">
              <button
                v-for="lvl in ['eli5', 'eli10', 'eli15'] as const"
                :key="lvl"
                :class="{ active: eliLevel === lvl }"
                @click="eliLevel = lvl"
              >
                {{ lvl.toUpperCase() }}
              </button>
            </div>
          </template>
          <template v-if="summaries">
            <p class="vi-eli-text">{{ summaries[eliLevel] || "—" }}</p>
            <p v-if="summariesSeq < specSeq" class="vi-eli-stale">
              ⏳ refers to an earlier revision — refreshes with the next generation
            </p>
          </template>
          <p v-else class="vi-empty">ELI5 / ELI10 / ELI15 summaries appear after the first generations.</p>
        </IntakeCard>

        <IntakeCard id="preview" title="Ticket Preview" icon="ticket">
          <template #summary>
            <template v-if="ticketPreview">
              <span class="vi-type-badge">{{ ticketPreview.issue_type }}</span>
              <span class="vi-sum-text">{{ ticketPreview.title }}</span>
            </template>
            <span v-else>Appears after the first generation</span>
          </template>
          <template v-if="ticketPreview">
            <div class="vi-preview-row">
              <span class="vi-preview-label">Title</span>
              <strong>{{ ticketPreview.title }}</strong>
            </div>
            <div class="vi-preview-row">
              <span class="vi-preview-label">Type</span>
              <span class="vi-type-badge">{{ ticketPreview.issue_type }}</span>
            </div>
            <div class="vi-preview-row vi-preview-block">
              <span class="vi-preview-label">Description</span>
              <p class="vi-preview-text">{{ ticketPreview.description }}</p>
            </div>
            <div
              v-if="ticketPreview.acceptance_criteria"
              class="vi-preview-row vi-preview-block"
            >
              <span class="vi-preview-label">Acceptance criteria</span>
              <p class="vi-preview-text vi-preview-mono">{{ ticketPreview.acceptance_criteria }}</p>
            </div>
          </template>
          <p v-else class="vi-empty">
            The ticket that will be created is previewed here once the first generation lands.
          </p>
        </IntakeCard>

        <IntakeCard id="transcript" title="Transcript" icon="file-text">
          <template #summary>
            <span class="vi-sum-text">{{ transcriptTail || "Nothing captured yet" }}</span>
          </template>
          <p ref="transcriptEl" class="vi-transcript">{{ displayedTranscript || "Nothing captured yet." }}</p>
        </IntakeCard>
      </aside>
    </div>

    <footer class="vi-timeline-card vi-card">
      <div class="vi-timeline-left">
        <button
          v-if="!showCheckpointInput"
          class="vi-btn"
          type="button"
          :disabled="!session || headRev === 0"
          @click="showCheckpointInput = true"
        >
          ⚑ Save checkpoint
        </button>
        <template v-else>
          <input
            v-model="checkpointLabel"
            class="vi-cp-input"
            :placeholder="`Checkpoint ${checkpoints.length + 1}`"
            @keydown.enter.prevent="onSaveCheckpoint"
            @keydown.escape="showCheckpointInput = false"
          />
          <button class="vi-btn" type="button" @click="onSaveCheckpoint">Save</button>
        </template>
      </div>
      <div class="vi-timeline-track">
        <input
          v-model.number="scrubPos"
          type="range"
          class="vi-scrubber"
          :min="0"
          :max="headRev"
          :disabled="!session || headRev === 0"
          aria-label="History scrubber"
        />
        <div class="vi-flags">
          <button
            v-for="cp in checkpoints"
            :key="cp.seq"
            class="vi-flag"
            type="button"
            :style="{ left: headRev ? `${(cp.seq / headRev) * 100}%` : '0%' }"
            :title="`${cp.label} (rev ${cp.seq})`"
            @click="scrubPos = cp.seq"
          >
            ⚑
          </button>
        </div>
        <span class="vi-rev">rev {{ viewSeq ?? headRev }} / {{ headRev }}</span>
      </div>
      <div class="vi-timeline-right">
        <button class="vi-btn" type="button" :disabled="!session" @click="onNewSession">
          New session
        </button>
        <button
          class="vi-create"
          type="button"
          :disabled="!canCreate || creating"
          :title="canCreate ? '' : 'Needs an active session, a target project, and some content'"
          @click="onCreateIssue"
        >
          {{ creating ? "Creating…" : "Create Issue →" }}
        </button>
      </div>
    </footer>

    <div v-if="createdIssue" class="vi-postcreate-backdrop">
      <div class="vi-postcreate vi-card">
        <h2>✓ {{ createdIssue.issue_key }} created</h2>
        <p class="vi-hint">What next?</p>
        <div class="vi-postcreate-actions">
          <button class="vi-create" type="button" autofocus @click="onNewSession">
            New session (default)
          </button>
          <button class="vi-btn" type="button" @click="createdIssue = null">
            Stay and refine
          </button>
          <button class="vi-btn" type="button" @click="onJumpToIssue">
            Open {{ createdIssue.issue_key }}
          </button>
        </div>
      </div>
    </div>
  </div>
</template>

<style scoped>
.vi-page {
  display: flex;
  flex-direction: column;
  gap: 16px;
  padding: 20px 24px;
  max-width: 1500px;
}
/* PAI-735: toolbar nodes teleport into the 52px app header — sizes and
   wrapping follow the header's chrome rules (32px controls, nowrap). */
.vi-title {
  font-size: 14px;
  color: var(--text-muted);
  white-space: nowrap;
}
.vi-title strong {
  color: var(--text);
}
.vi-crumb-sep {
  margin: 0 6px;
}
.vi-project-chip {
  padding: 6px 14px;
  border: 1px solid var(--border);
  border-radius: 999px;
  background: var(--bg-card);
  font-size: 13px;
  color: var(--text-muted);
}
.vi-talk {
  display: flex;
  align-items: center;
  gap: 10px;
  white-space: nowrap;
}
.vi-talk-btn {
  height: 32px;
  display: inline-flex;
  align-items: center;
  padding: 0 16px;
  border-radius: 999px;
  font-size: 13px;
  font-weight: 700;
  cursor: pointer;
  transition:
    background-color 0.18s ease,
    color 0.18s ease,
    box-shadow 0.18s ease;
}

/* Header tiers (container appchrome, matching AppHeader's breakpoints):
   shed the informational extras before the talk button ever truncates. */
@container appchrome (max-width: 1100px) {
  .vi-conn,
  .vi-mode-toggle {
    display: none;
  }
}
@container appchrome (max-width: 920px) {
  .vi-title {
    display: none;
  }
}
/* PAI-715: idle is quiet — outline only. Solid red is reserved for the
   moments the microphone is actually live. */
.vi-talk-start {
  background: transparent;
  border: 1.5px solid #b91c1c;
  color: #b91c1c;
}
.vi-talk-start:hover {
  background: rgba(185, 28, 28, 0.06);
}
.vi-talk-stop {
  background: #b91c1c;
  border: 1.5px solid #b91c1c;
  color: #fff;
  animation: vi-breathe 2.4s ease-in-out infinite;
}
/* Soft professional breathing: a gentle ring + barely-there opacity swell,
   slow enough to read as "live", never as an alarm. The level-reactive
   inline ring (mic RMS) layers on top while speech is heard. */
@keyframes vi-breathe {
  0%,
  100% {
    box-shadow: 0 0 0 0 rgba(185, 28, 28, 0.22);
  }
  50% {
    box-shadow: 0 0 0 7px rgba(185, 28, 28, 0.08);
  }
}
.vi-perm {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  padding: 4px 10px;
  border-radius: 999px;
  border: 1px solid var(--border);
  font-size: 12px;
}
.vi-perm--ok {
  color: #15803d;
  border-color: #bbf7d0;
  background: #f0fdf4;
}
.vi-perm--ask {
  color: #a16207;
  border-color: #fde68a;
  background: #fffbeb;
}
.vi-perm--denied {
  color: #b91c1c;
  border-color: #fecaca;
  background: #fef2f2;
}
.vi-perm--unknown {
  color: var(--text-muted);
}
.vi-perm-action {
  border: none;
  background: none;
  padding: 0;
  font-size: 12px;
  font-weight: 600;
  color: inherit;
  text-decoration: underline;
  cursor: pointer;
}
.vi-conn {
  font-size: 12px;
  color: var(--text-muted);
}
.vi-mode-toggle {
  font-size: 12px;
}
.vi-mic-error {
  color: #b45309;
  margin-bottom: 8px;
}
.vi-conn--open {
  color: #15803d;
}
.vi-conn--retrying {
  color: #b45309;
}
.vi-error {
  margin: 0;
  padding: 8px 12px;
  border: 1px solid #fecaca;
  border-radius: 8px;
  background: #fef2f2;
  color: #b91c1c;
  font-size: 13px;
}
.vi-grid {
  display: grid;
  grid-template-columns: minmax(0, 3fr) minmax(0, 2fr);
  gap: 16px;
  align-items: start;
}
@media (max-width: 1000px) {
  .vi-grid {
    grid-template-columns: minmax(0, 1fr);
  }
}
.vi-card {
  background: var(--bg-card);
  border: 1px solid var(--border);
  border-radius: 12px;
  padding: 16px;
}
.vi-card-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  margin-bottom: 10px;
}
.vi-card-head h2 {
  margin: 0;
  font-size: 15px;
}
.vi-side {
  display: flex;
  flex-direction: column;
  gap: 12px;
  /* PAI-715: the column scrolls on its own so the sticky action bar
     below never gets pushed out of the viewport. */
  max-height: calc(100vh - 330px);
  overflow-y: auto;
  position: sticky;
  top: 12px;
  padding-right: 2px;
}
.vi-tabs {
  display: inline-flex;
  border: 1px solid var(--border);
  border-radius: 8px;
  overflow: hidden;
}
.vi-tabs button {
  padding: 5px 14px;
  border: none;
  background: transparent;
  font-size: 12.5px;
  color: var(--text-muted);
  cursor: pointer;
}
.vi-tabs button.active {
  background: var(--brand-blue);
  color: #fff;
}
.vi-stage {
  margin: 0 0 10px;
  font-size: 12.5px;
  color: var(--text-muted);
}
.vi-stage--running {
  color: var(--brand-blue);
}
.vi-stage--error {
  color: #b91c1c;
}
.vi-stage--degraded {
  color: #b45309;
}
.vi-diff {
  font-family: ui-monospace, SFMono-Regular, Menlo, monospace;
  font-size: 12px;
  border: 1px solid var(--border);
  border-radius: 8px;
  padding: 8px 10px;
  max-height: 420px;
  overflow-y: auto;
}
.vi-diff-line {
  display: flex;
  gap: 8px;
  white-space: pre-wrap;
}
.vi-diff--add {
  background: #dcfce7;
}
.vi-diff--del {
  background: #fee2e2;
  text-decoration: line-through;
}
.vi-diff-sign {
  width: 12px;
  color: var(--text-muted);
}
.vi-tts-toggle {
  border: 1px solid var(--border);
  background: var(--bg-card);
  border-radius: 7px;
  padding: 2px 8px;
  font-size: 13px;
  cursor: pointer;
  margin-right: 8px;
}
.vi-eli-text {
  margin: 0;
  font-size: 13px;
  line-height: 1.55;
}
.vi-eli-stale {
  margin: 8px 0 0;
  font-size: 11.5px;
  color: #b45309;
}
.vi-impact-group {
  margin: 10px 0 4px;
  font-size: 10.5px;
  text-transform: uppercase;
  letter-spacing: 0.05em;
  color: var(--text-muted);
}
.vi-impact-rows {
  display: flex;
  flex-direction: column;
  gap: 4px;
}
.vi-impact-row {
  display: flex;
  gap: 8px;
  align-items: baseline;
  font-size: 12.5px;
  text-decoration: none;
  color: var(--text);
  padding: 3px 4px;
  border-radius: 6px;
}
.vi-impact-row:hover {
  background: var(--bg);
}
.vi-impact-key {
  padding: 1px 8px;
  border: 1px solid var(--border);
  border-radius: 999px;
  font-size: 11.5px;
  font-weight: 700;
  white-space: nowrap;
}
.vi-impact--touches {
  border-color: var(--brand-blue);
  color: var(--brand-blue);
}
.vi-impact--conflicts {
  border-color: #b45309;
  color: #b45309;
}
.vi-impact--extends {
  border-color: #15803d;
  color: #15803d;
}
.vi-impact-title {
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.vi-impact-graphhit {
  margin: 2px 0;
  font-size: 12px;
  color: var(--text-muted);
}
.vi-preview-row {
  display: flex;
  gap: 10px;
  align-items: baseline;
  margin-bottom: 8px;
  font-size: 13px;
}
.vi-preview-block {
  flex-direction: column;
  gap: 3px;
}
.vi-preview-label {
  font-size: 10.5px;
  text-transform: uppercase;
  letter-spacing: 0.05em;
  color: var(--text-muted);
  min-width: 70px;
}
.vi-preview-text {
  margin: 0;
  white-space: pre-wrap;
}
.vi-preview-mono {
  font-family: ui-monospace, SFMono-Regular, Menlo, monospace;
  font-size: 12px;
}
.vi-type-badge {
  padding: 2px 10px;
  border: 1px solid var(--border);
  border-radius: 999px;
  font-size: 12px;
  text-transform: capitalize;
}
.vi-lang {
  margin-right: 8px;
}
.vi-history-banner {
  margin-bottom: 10px;
  padding: 7px 10px;
  border: 1px solid #fde68a;
  border-radius: 8px;
  background: #fffbeb;
  color: #92400e;
  font-size: 12.5px;
}
.vi-link {
  border: none;
  background: none;
  padding: 0;
  color: var(--brand-blue);
  text-decoration: underline;
  font-size: inherit;
  cursor: pointer;
}
.vi-hero {
  display: flex;
  flex-direction: column;
  align-items: center;
  justify-content: center;
  gap: 6px;
  min-height: 320px;
  cursor: pointer;
  text-align: center;
  padding: 24px;
  border-radius: 8px;
}
.vi-hero:hover {
  background: var(--bg);
}
.vi-hero-mark {
  position: relative;
  width: 132px;
  height: 88px;
  display: flex;
  align-items: center;
  justify-content: center;
  color: #94a3b8;
  margin-bottom: 6px;
}
.vi-hero-mark--live {
  color: var(--brand-blue);
}
.vi-hero-wave {
  width: 120px;
  height: 48px;
}
.vi-hero-mic {
  position: absolute;
  right: 8px;
  bottom: 4px;
  display: flex;
  align-items: center;
  justify-content: center;
  width: 30px;
  height: 30px;
  border-radius: 50%;
  background: var(--brand-blue);
  color: #fff;
  box-shadow: 0 2px 8px rgba(15, 23, 42, 0.18);
}
.vi-hero-mark--live .vi-hero-mic {
  background: #b91c1c;
  animation: vi-breathe 2.4s ease-in-out infinite;
}
.vi-hero-title {
  margin: 0;
  font-size: 19px;
}
.vi-hero-sub {
  margin: 0;
  font-size: 13.5px;
  color: var(--text-muted);
  max-width: 420px;
}
.vi-hero-hint {
  margin: 12px 0 0;
  font-size: 12.5px;
  color: var(--text-muted);
  max-width: 460px;
  border-top: 1px solid var(--border);
  padding-top: 12px;
}
.vi-spec-editor {
  width: 100%;
  min-height: 280px;
  resize: vertical;
  border: 1px solid var(--border);
  border-radius: 8px;
  padding: 10px 12px;
  font-family: ui-monospace, SFMono-Regular, Menlo, monospace;
  font-size: 13px;
  color: var(--text);
  background: var(--bg-card);
}
.vi-spec-editor:focus {
  outline: none;
  border-color: var(--brand-blue);
}
.vi-spec-preview {
  min-height: 280px;
  padding: 4px 2px;
  font-size: 13.5px;
}
.vi-draft-note {
  margin-top: 6px;
  font-size: 12px;
  color: var(--text-muted);
}
.vi-spec-foot {
  margin-top: 12px;
  border-top: 1px solid var(--border);
  padding-top: 12px;
}
.vi-hint,
.vi-empty {
  margin: 0;
  font-size: 12.5px;
  color: var(--text-muted);
}
.vi-transcript {
  margin: 0;
  font-size: 12.5px;
  color: var(--text);
  white-space: pre-wrap;
  max-height: 220px;
  overflow-y: auto;
}
.vi-sum-text {
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.vi-chipbtn {
  border: 1px solid var(--border);
  background: var(--bg-card);
  cursor: pointer;
}
.vi-timeline-card {
  display: flex;
  align-items: center;
  gap: 16px;
  flex-wrap: wrap;
  /* PAI-715: scrubber + session actions stay pinned while content grows. */
  position: sticky;
  bottom: 10px;
  z-index: 25;
  box-shadow: 0 -6px 18px rgba(15, 23, 42, 0.06), 0 2px 8px rgba(15, 23, 42, 0.08);
}
.vi-timeline-left,
.vi-timeline-right {
  display: flex;
  align-items: center;
  gap: 8px;
}
.vi-timeline-track {
  flex: 1;
  min-width: 220px;
  position: relative;
  display: flex;
  align-items: center;
  gap: 10px;
}
.vi-scrubber {
  flex: 1;
}
.vi-flags {
  position: absolute;
  left: 0;
  right: 70px;
  top: -14px;
  height: 14px;
  pointer-events: none;
}
.vi-flag {
  position: absolute;
  transform: translateX(-50%);
  border: none;
  background: none;
  color: var(--brand-blue);
  font-size: 12px;
  cursor: pointer;
  pointer-events: auto;
}
.vi-rev {
  font-size: 12px;
  color: var(--text-muted);
  white-space: nowrap;
}
.vi-btn {
  padding: 7px 14px;
  border: 1px solid var(--border);
  border-radius: 8px;
  background: var(--bg-card);
  color: var(--text);
  font-size: 13px;
  cursor: pointer;
}
.vi-btn:disabled {
  opacity: 0.5;
  cursor: default;
}
.vi-cp-input {
  padding: 7px 10px;
  border: 1px solid var(--border);
  border-radius: 8px;
  font-size: 13px;
}
.vi-postcreate-backdrop {
  position: fixed;
  inset: 0;
  z-index: 50;
  background: rgba(15, 23, 42, 0.35);
  display: flex;
  align-items: center;
  justify-content: center;
}
.vi-postcreate {
  min-width: 340px;
  text-align: center;
}
.vi-postcreate h2 {
  margin: 0 0 6px;
  font-size: 18px;
}
.vi-postcreate-actions {
  display: flex;
  flex-direction: column;
  gap: 8px;
  margin-top: 14px;
}
.vi-create {
  padding: 10px 22px;
  border: none;
  border-radius: 8px;
  background: #b91c1c;
  color: #fff;
  font-size: 14px;
  font-weight: 700;
  cursor: pointer;
}
.vi-create:disabled {
  opacity: 0.55;
  cursor: default;
}
</style>
