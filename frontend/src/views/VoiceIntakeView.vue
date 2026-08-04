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
import { computed, onBeforeUnmount, ref, watch } from "vue";

import ProjectConfidenceChip from "@/components/intake/ProjectConfidenceChip.vue";
import TranscriptInput from "@/components/intake/TranscriptInput.vue";
import { useIntakeSession } from "@/composables/useIntakeSession";
import { useMarkdown } from "@/composables/useMarkdown";

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
  checkpoints,
  viewSeq,
  viewState,
  isViewingHistory,
  error,
  start,
  sendTranscript,
  saveSpec,
  setLanguage,
  saveCheckpoint,
  scrub,
  restore,
  reset,
  disconnect,
} = useIntakeSession();

const talking = ref(false);
const previewEnabled = ref(true);
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
        return "Session AI budget exhausted — spec frozen, capture still works.";
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

async function onStartTalking() {
  if (!session.value) await start();
  talking.value = true;
}

function onStopTalking() {
  talking.value = false;
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

watch(scrubPos, async (pos) => {
  if (!session.value) return;
  await scrub(pos >= headRev.value ? null : pos);
});

watch(headRev, (rev) => {
  if (!isViewingHistory.value) scrubPos.value = rev;
});

async function onRestore() {
  if (viewSeq.value === null) return;
  await restore(viewSeq.value);
  scrubPos.value = headRev.value;
}

async function onNewSession() {
  talking.value = false;
  draftDirty.value = false;
  specDraft.value = "";
  reset();
  await start();
}

onBeforeUnmount(() => {
  disconnect();
});
</script>

<template>
  <div class="vi-page">
    <header class="vi-topbar">
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
      <div class="vi-talk">
        <button
          v-if="!talking"
          class="vi-talk-btn vi-talk-start"
          type="button"
          @click="onStartTalking"
        >
          ● Start Talking
        </button>
        <button v-else class="vi-talk-btn vi-talk-stop" type="button" @click="onStopTalking">
          ■ Stop Talking
        </button>
        <span class="vi-conn" :class="`vi-conn--${connection}`">{{ connectionLabel }}</span>
      </div>
    </header>

    <p v-if="error" class="vi-error">{{ error }}</p>

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
          or
          <button class="vi-link" type="button" @click="onRestore">restore this state</button>
        </div>
        <textarea
          v-if="!previewOn"
          v-model="specDraft"
          v-auto-grow
          class="vi-spec-editor"
          rows="14"
          placeholder="The specification builds here while you talk — or edit it directly."
          @input="onSpecInput"
          @blur="draftDirty && onSaveSpec()"
        />
        <div v-else class="vi-spec-preview md-rendered" v-html="previewHtml" />
        <div v-if="draftDirty && !isViewingHistory" class="vi-draft-note">
          Unsaved edits — <button class="vi-link" type="button" @click="onSaveSpec">save</button>
        </div>
        <footer class="vi-spec-foot">
          <TranscriptInput v-if="talking" :disabled="isViewingHistory" @chunk="onChunk" />
          <p v-else class="vi-hint">Start talking (dev mode: typed input) to build the spec.</p>
        </footer>
      </section>

      <aside class="vi-side">
        <section class="vi-card">
          <header class="vi-card-head"><h2>Impact Analysis</h2></header>
          <p class="vi-empty">Impacted and related issues appear here once project detection lands (PAI-706/708).</p>
        </section>
        <section class="vi-card">
          <header class="vi-card-head"><h2>Understanding Check</h2></header>
          <p class="vi-empty">ELI5 / ELI10 / ELI15 summaries arrive with the AI loop (PAI-709).</p>
        </section>
        <section class="vi-card">
          <header class="vi-card-head"><h2>Ticket Preview</h2></header>
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
        </section>
        <section class="vi-card">
          <header class="vi-card-head"><h2>Transcript</h2></header>
          <p class="vi-transcript">{{ displayedTranscript || "Nothing captured yet." }}</p>
        </section>
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
        <button class="vi-create" type="button" disabled title="Arrives with PAI-707">
          Create Issue →
        </button>
      </div>
    </footer>
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
.vi-topbar {
  display: flex;
  align-items: center;
  gap: 16px;
  flex-wrap: wrap;
}
.vi-title {
  font-size: 15px;
  color: var(--text-muted);
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
  margin-left: auto;
  display: flex;
  align-items: center;
  gap: 10px;
}
.vi-talk-btn {
  padding: 10px 22px;
  border-radius: 999px;
  border: none;
  font-size: 14px;
  font-weight: 700;
  cursor: pointer;
  color: #fff;
}
.vi-talk-start {
  background: #b91c1c;
}
.vi-talk-stop {
  background: #b91c1c;
  animation: vi-pulse 1.6s ease-in-out infinite;
}
@keyframes vi-pulse {
  50% {
    box-shadow: 0 0 0 6px rgba(185, 28, 28, 0.15);
  }
}
.vi-conn {
  font-size: 12px;
  color: var(--text-muted);
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
  gap: 16px;
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
  max-height: 180px;
  overflow-y: auto;
}
.vi-timeline-card {
  display: flex;
  align-items: center;
  gap: 16px;
  flex-wrap: wrap;
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
