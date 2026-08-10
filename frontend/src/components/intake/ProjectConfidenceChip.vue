<!--
  PAIMOS — Your Professional & Personal AI Project OS
  Copyright (C) 2026 Markus Barta <markus@barta.com>
  Licensed under AGPL-3.0-only; see LICENSE.
-->
<script setup lang="ts">
// PAI-706: the persistent project chip for the Voice Intake workbench.
// States: searching (no match yet) → suggested (< threshold, popover with
// candidates + rationale) → auto-matched (≥ threshold, session follows the
// detection) → pinned (manual override; better matches surface as a badge
// but never displace the pin). Switching is non-destructive — it only
// re-targets downstream stages, never the spec or the timeline.
import { computed, nextTick, onBeforeUnmount, onMounted, ref, watch } from "vue";

import { api } from "@/api/client";
import type { IntakeSession } from "@/api/intake";
import type { IntakeProjectMatch } from "@/composables/useIntakeSession";
import type { Project } from "@/types";

const props = defineProps<{
  session: IntakeSession | null;
  match: IntakeProjectMatch | null;
}>();

const emit = defineEmits<{
  (e: "pin", projectId: number): void;
  (e: "unpin"): void;
}>();

const popoverOpen = ref(false);
const switchedFlash = ref<string | null>(null);
const projectSearch = ref("");
const accessibleProjects = ref<Project[]>([]);
const projectsLoading = ref(false);
const projectsLoaded = ref(false);
const projectsError = ref<string | null>(null);
const projectSearchRef = ref<HTMLInputElement | null>(null);
let projectLoadGeneration = 0;

// PAI-735 moved this chip into the app header via Teleport, and the header
// is deliberately hard chrome: `height: 52px; overflow: hidden`, with
// `.ah-left` adding its own overflow clip plus a mask-image. An absolutely
// positioned popover opens *below* that 52px row, so it was painted and
// then clipped away — the button toggled, nothing appeared, and the chip
// read as dead. Teleport to body and position from the trigger's rect, the
// same escape hatch MetaSelect uses for table-cell dropdowns.
const chipRef = ref<HTMLButtonElement | null>(null);
const popRef = ref<HTMLElement | null>(null);
const popPos = ref({ top: "0px", left: "0px", minWidth: "320px" });

const POP_MAX_WIDTH = 420;

function positionPopover() {
  const el = chipRef.value;
  if (!el) return;
  const r = el.getBoundingClientRect();
  const margin = 8;
  popPos.value = {
    top: `${r.bottom + 6}px`,
    // Keep the panel on screen when the chip sits near the right edge.
    left: `${Math.max(margin, Math.min(r.left, window.innerWidth - POP_MAX_WIDTH - margin))}px`,
    minWidth: `${Math.max(r.width, 320)}px`,
  };
}

function togglePopover() {
  popoverOpen.value = !popoverOpen.value;
  if (popoverOpen.value) {
    void nextTick(positionPopover);
    void loadAccessibleProjects();
  }
}

function closePopover() {
  popoverOpen.value = false;
}

async function loadAccessibleProjects() {
  const generation = ++projectLoadGeneration;
  projectSearch.value = "";
  accessibleProjects.value = [];
  projectsLoading.value = true;
  projectsLoaded.value = false;
  projectsError.value = null;
  void nextTick(() => projectSearchRef.value?.focus());

  try {
    const projects = await api.get<Project[]>("/projects?status=active");
    if (generation !== projectLoadGeneration) return;
    // The server applies the caller's project visibility filter. Keep the
    // client-side status guard too so the picker fails closed on stale data.
    accessibleProjects.value = projects.filter((project) => project.status === "active");
    projectsLoaded.value = true;
  } catch {
    if (generation !== projectLoadGeneration) return;
    projectsError.value = "Projects could not be loaded. Close and try again.";
  } finally {
    if (generation === projectLoadGeneration) projectsLoading.value = false;
  }
}

function selectProject(projectId: number) {
  emit("pin", projectId);
  closePopover();
}

function unpinAndClose() {
  emit("unpin");
  closePopover();
}

function onDocumentMouseDown(e: MouseEvent) {
  if (!popoverOpen.value) return;
  const t = e.target as Node;
  // The popover is teleported, so it is not a DOM descendant of the chip.
  if (chipRef.value?.contains(t) || popRef.value?.contains(t)) return;
  closePopover();
}

function onDocumentKeydown(e: KeyboardEvent) {
  if (e.key === "Escape" && popoverOpen.value) {
    closePopover();
    chipRef.value?.focus();
  }
}

function onViewportChange() {
  if (popoverOpen.value) positionPopover();
}

onMounted(() => {
  document.addEventListener("mousedown", onDocumentMouseDown);
  document.addEventListener("keydown", onDocumentKeydown);
  window.addEventListener("resize", onViewportChange);
});

onBeforeUnmount(() => {
  projectLoadGeneration += 1;
  document.removeEventListener("mousedown", onDocumentMouseDown);
  document.removeEventListener("keydown", onDocumentKeydown);
  window.removeEventListener("resize", onViewportChange);
});

const pinned = computed(() => props.session?.pinned_project_id != null);
const threshold = computed(() => props.match?.threshold ?? 90);
const top = computed(() => props.match?.matches?.[0] ?? null);
const normalizedSearch = computed(() => projectSearch.value.trim().toLocaleLowerCase());

function projectMatchesSearch(project: { key: string; name: string; description?: string }) {
  const query = normalizedSearch.value;
  if (!query) return true;
  return [project.key, project.name, project.description ?? ""].some((value) =>
    value.toLocaleLowerCase().includes(query),
  );
}

const accessibleProjectIDs = computed(() => new Set(accessibleProjects.value.map((project) => project.id)));
const accessibleCandidateIDs = computed(
  () => new Set((props.match?.matches ?? []).filter((candidate) => accessibleProjectIDs.value.has(candidate.project_id)).map((candidate) => candidate.project_id)),
);
const visibleCandidates = computed(() => {
  if (!projectsLoaded.value) return [];
  return (props.match?.matches ?? []).filter(
    (candidate) => accessibleCandidateIDs.value.has(candidate.project_id) && projectMatchesSearch(candidate),
  );
});
const visibleProjects = computed(() => {
  if (!projectsLoaded.value) return [];
  return accessibleProjects.value
    .filter((project) => !accessibleCandidateIDs.value.has(project.id) && projectMatchesSearch(project))
    .sort((a, b) => a.key.localeCompare(b.key));
});
const noProjectResults = computed(
  () => projectsLoaded.value && visibleCandidates.value.length === 0 && visibleProjects.value.length === 0,
);

const activeProject = computed(() => {
  const m = props.match?.matches ?? [];
  const pinId = props.session?.pinned_project_id;
  if (pinId != null) {
    const c = m.find((x) => x.project_id === pinId);
    if (c) return { label: `${c.key} — ${c.name}`, score: null as number | null };
    const project = accessibleProjects.value.find((candidate) => candidate.id === pinId);
    return project ? { label: `${project.key} — ${project.name}`, score: null } : { label: `Pinned project #${pinId}`, score: null };
  }
  const detId = props.session?.detected_project_id;
  if (detId != null) {
    const c = m.find((x) => x.project_id === detId);
    if (c) return { label: `${c.key} — ${c.name}`, score: c.score };
  }
  return null;
});

const state = computed(() => {
  if (!props.session) return "idle";
  if (pinned.value) return "pinned";
  if (!activeProject.value) return "searching";
  // PAI-715: the FIRST selection is threshold-free — with no incumbent,
  // any signal beats no project. The threshold only gates switching AWAY
  // from something already selected.
  if (props.match?.first_detection) return "auto";
  return (activeProject.value.score ?? 0) >= threshold.value ? "auto" : "suggested";
});

// "AI suggests Y" while pinned: the best non-pinned candidate over threshold.
const pinnedSuggestion = computed(() => {
  if (!pinned.value || !top.value) return null;
  if (top.value.project_id === props.session?.pinned_project_id) return null;
  return top.value.score >= threshold.value ? top.value : null;
});

const confClass = computed(() => {
  const score = activeProject.value?.score ?? 0;
  if (pinned.value) return "vi-chip--pinned";
  if (score >= threshold.value) return "vi-chip--high";
  if (score >= 50) return "vi-chip--med";
  return "vi-chip--low";
});

function onAutoSwitchToggle() {
  if (pinned.value) {
    emit("unpin"); // switch ON → detection owns the selection again
    return;
  }
  // Switch OFF → pin whatever is active right now so it can't move.
  const detId = props.session?.detected_project_id;
  if (detId != null) emit("pin", detId);
}

// Flash "Switched to X" when the detected project changes while unpinned.
watch(
  () => props.session?.detected_project_id,
  (next, prev) => {
    if (pinned.value || next == null || prev == null || next === prev) return;
    const c = props.match?.matches?.find((x) => x.project_id === next);
    if (c && c.score >= threshold.value) {
      switchedFlash.value = `Switched to ${c.key}`;
      setTimeout(() => (switchedFlash.value = null), 4000);
    }
  },
);
</script>

<template>
  <div class="vi-chip-wrap">
    <button
      ref="chipRef"
      class="vi-chip"
      :class="confClass"
      type="button"
      :disabled="!session"
      aria-haspopup="dialog"
      :aria-expanded="popoverOpen"
      @click="togglePopover"
    >
      <span v-if="state === 'idle'">No session</span>
      <span v-else-if="state === 'searching'" class="vi-chip-searching">Detecting project…</span>
      <template v-else>
        <span v-if="pinned" class="vi-chip-pin" title="Pinned — auto-switch off">📌</span>
        <span class="vi-chip-label">{{ activeProject?.label ?? "—" }}</span>
        <span v-if="activeProject?.score != null" class="vi-chip-score">
          {{ activeProject.score }}%
        </span>
      </template>
    </button>
    <span v-if="switchedFlash" class="vi-chip-flash">{{ switchedFlash }}</span>
    <!-- PAI-717 (per Markus): auto-switch is a real switch. Off maps to
         pinning the active project (the existing non-destructive manual
         mode); on releases the pin and detection takes over again. -->
    <label
      v-if="state !== 'idle'"
      class="vi-chip-autoswitch"
      :class="{ 'vi-chip-autoswitch--off': pinned }"
      :title="pinned ? 'Auto-switch is off — the project is pinned' : `Switches projects automatically at ≥ ${threshold}% confidence`"
    >
      <span class="vi-switch" :class="{ on: !pinned }" role="switch" :aria-checked="!pinned">
        <input
          type="checkbox"
          :checked="!pinned"
          :disabled="!pinned && !activeProject"
          @change="onAutoSwitchToggle"
        />
        <span class="vi-switch-knob" />
      </span>
      auto-switch ≥ {{ threshold }}%
    </label>
    <button
      v-if="pinnedSuggestion"
      class="vi-chip-suggest"
      type="button"
      :title="pinnedSuggestion.rationale ?? ''"
      @click="emit('pin', pinnedSuggestion.project_id)"
    >
      AI suggests: {{ pinnedSuggestion.key }} · {{ pinnedSuggestion.score }}%
    </button>

    <!-- Teleported to body so the app header's overflow clip cannot eat it.
         Positioned from the chip's rect, so it still tracks the trigger. -->
    <Teleport to="body">
      <div
        v-if="popoverOpen"
        ref="popRef"
        class="vi-chip-pop"
        role="dialog"
        aria-label="Choose project"
        :style="popPos"
      >
        <label class="vi-chip-search-label" for="vi-project-search">Search accessible projects</label>
        <input
          id="vi-project-search"
          ref="projectSearchRef"
          v-model="projectSearch"
          class="vi-chip-project-search"
          type="search"
          placeholder="Key, name, or description"
          autocomplete="off"
        />
        <p v-if="projectsLoading" class="vi-chip-pop-empty">Loading projects…</p>
        <p v-else-if="projectsError" class="vi-chip-pop-error" role="alert">{{ projectsError }}</p>
        <div v-else class="vi-chip-results">
          <template v-if="visibleCandidates.length">
            <p class="vi-chip-pop-title">Candidate projects</p>
            <button
              v-for="c in visibleCandidates"
              :key="c.project_id"
              class="vi-chip-cand"
              type="button"
              @click="selectProject(c.project_id)"
            >
              <span class="vi-cand-head">
                <strong>{{ c.key }}</strong> — {{ c.name }}
                <span class="ar-conf" :class="`ar-conf--${c.confidence === 'med' ? 'medium' : c.confidence}`">
                  {{ c.score }}%
                </span>
              </span>
              <span v-if="c.rationale" class="vi-cand-rationale">{{ c.rationale }}</span>
            </button>
          </template>
          <template v-if="visibleProjects.length">
            <p class="vi-chip-pop-title" :class="{ 'vi-chip-pop-title--spaced': visibleCandidates.length }">All projects</p>
            <button
              v-for="project in visibleProjects"
              :key="project.id"
              class="vi-chip-project"
              type="button"
              @click="selectProject(project.id)"
            >
              <span><strong>{{ project.key }}</strong> — {{ project.name }}</span>
              <span v-if="project.description" class="vi-project-description">{{ project.description }}</span>
            </button>
          </template>
          <p v-if="noProjectResults" class="vi-chip-pop-empty">
            {{ projectSearch ? "No accessible projects match that search." : "No accessible active projects." }}
          </p>
        </div>
        <button v-if="pinned" class="vi-chip-unpin" type="button" @click="unpinAndClose">
          Unpin — resume auto-switch
        </button>
      </div>
    </Teleport>
  </div>
</template>

<style scoped>
.vi-chip-wrap {
  position: relative;
  display: flex;
  align-items: center;
  gap: 8px;
  flex-wrap: wrap;
}
.vi-chip {
  display: inline-flex;
  align-items: center;
  gap: 8px;
  padding: 6px 14px;
  border: 1px solid var(--border);
  border-radius: 999px;
  background: var(--bg-card);
  font-size: 13px;
  color: var(--text);
  cursor: pointer;
}
.vi-chip:disabled {
  opacity: 0.6;
  cursor: default;
}
.vi-chip--high {
  border-color: #15803d;
}
.vi-chip--high .vi-chip-score {
  background: #dcfce7;
  color: #15803d;
}
.vi-chip--med .vi-chip-score {
  background: #fef9c3;
  color: #a16207;
}
.vi-chip--low .vi-chip-score {
  background: #fee2e2;
  color: #b91c1c;
}
.vi-chip--pinned {
  border-style: dashed;
}
.vi-chip-score {
  padding: 1px 8px;
  border-radius: 999px;
  font-weight: 700;
  font-size: 12px;
}
.vi-chip-searching {
  color: var(--text-muted);
  animation: vi-chip-pulse 1.6s ease-in-out infinite;
}
@keyframes vi-chip-pulse {
  50% {
    opacity: 0.5;
  }
}
.vi-chip-autoswitch {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  font-size: 11px;
  color: var(--text-muted);
  cursor: pointer;
  user-select: none;
}
.vi-chip-autoswitch--off {
  color: #a16207;
}
.vi-switch {
  position: relative;
  width: 28px;
  height: 16px;
  border-radius: 999px;
  background: #cbd5e1;
  transition: background-color 0.15s ease;
  flex-shrink: 0;
}
.vi-switch.on {
  background: #15803d;
}
.vi-switch input {
  position: absolute;
  inset: 0;
  opacity: 0;
  margin: 0;
  cursor: pointer;
}
.vi-switch input:disabled {
  cursor: default;
}
.vi-switch-knob {
  position: absolute;
  top: 2px;
  left: 2px;
  width: 12px;
  height: 12px;
  border-radius: 50%;
  background: #fff;
  transition: transform 0.15s ease;
  pointer-events: none;
}
.vi-switch.on .vi-switch-knob {
  transform: translateX(12px);
}
.vi-chip-flash {
  font-size: 12px;
  color: #15803d;
  font-weight: 600;
}
.vi-chip-suggest {
  padding: 4px 10px;
  border: 1px solid var(--brand-blue);
  border-radius: 999px;
  background: var(--brand-blue-pale, #eff6ff);
  color: var(--brand-blue);
  font-size: 12px;
  cursor: pointer;
}
/* Teleported to body: fixed to the viewport, with top/left supplied
   inline from the chip's bounding rect. Scoped styles still reach it —
   the data-v attribute travels with the teleported node. */
.vi-chip-pop {
  position: fixed;
  /* Same layer as MetaSelect's teleported dropdowns — both are panels
     anchored to a trigger, and both must clear modals at 1000. */
  z-index: 9000;
  min-width: 320px;
  max-width: 420px;
  padding: 10px;
  border: 1px solid var(--border);
  border-radius: 10px;
  background: var(--bg-card);
  box-shadow: 0 8px 24px rgba(15, 23, 42, 0.12);
  display: flex;
  flex-direction: column;
  gap: 6px;
}
.vi-chip-search-label {
  font-size: 10.5px;
  text-transform: uppercase;
  letter-spacing: 0.05em;
  color: var(--text-muted);
}
.vi-chip-project-search {
  width: 100%;
  padding: 7px 9px;
  border: 1px solid var(--border);
  border-radius: 7px;
  background: var(--bg);
  color: var(--text);
  font: inherit;
}
.vi-chip-project-search:focus {
  outline: 2px solid var(--brand-blue-pale, #dbeafe);
  border-color: var(--brand-blue);
}
.vi-chip-results {
  display: flex;
  flex-direction: column;
  gap: 6px;
  max-height: min(420px, calc(100vh - 180px));
  overflow-y: auto;
}
.vi-chip-pop-title {
  margin: 0 0 2px;
  font-size: 10.5px;
  text-transform: uppercase;
  letter-spacing: 0.05em;
  color: var(--text-muted);
}
.vi-chip-pop-title--spaced {
  margin-top: 6px;
}
.vi-chip-pop-error {
  margin: 0;
  padding: 6px 2px;
  font-size: 12.5px;
  color: #b91c1c;
}
.vi-chip-pop-empty {
  margin: 0;
  padding: 6px 2px;
  font-size: 12.5px;
  color: var(--text-muted);
}
.vi-chip-cand {
  text-align: left;
  padding: 8px 10px;
  border: 1px solid var(--border);
  border-radius: 8px;
  background: transparent;
  cursor: pointer;
  font-size: 12.5px;
  color: var(--text);
}
.vi-chip-cand:hover {
  border-color: var(--brand-blue);
}
.vi-chip-project {
  display: flex;
  flex-direction: column;
  gap: 3px;
  text-align: left;
  padding: 8px 10px;
  border: 1px solid var(--border);
  border-radius: 8px;
  background: transparent;
  cursor: pointer;
  font-size: 12.5px;
  color: var(--text);
}
.vi-chip-project:hover {
  border-color: var(--brand-blue);
}
.vi-project-description {
  color: var(--text-muted);
  font-size: 12px;
}
.vi-cand-head {
  display: flex;
  align-items: center;
  gap: 6px;
}
.vi-cand-rationale {
  display: block;
  margin-top: 3px;
  color: var(--text-muted);
  font-size: 12px;
}
.ar-conf {
  margin-left: auto;
  padding: 1px 8px;
  border-radius: 999px;
  font-size: 11.5px;
  font-weight: 700;
}
.ar-conf--high {
  background: #dcfce7;
  color: #15803d;
}
.ar-conf--medium {
  background: #fef9c3;
  color: #a16207;
}
.ar-conf--low {
  background: #fee2e2;
  color: #b91c1c;
}
.vi-chip-unpin {
  padding: 6px 10px;
  border: none;
  background: none;
  color: var(--brand-blue);
  text-decoration: underline;
  font-size: 12.5px;
  cursor: pointer;
  text-align: left;
}
</style>
