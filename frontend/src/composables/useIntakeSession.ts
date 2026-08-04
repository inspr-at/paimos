/*
 * PAIMOS — Your Professional & Personal AI Project OS
 * Copyright (C) 2026 Markus Barta <markus@barta.com>
 *
 * This program is free software: you can redistribute it and/or modify
 * it under the terms of the GNU Affero General Public License as
 * published by the Free Software Foundation, version 3.
 *
 * This program is distributed in the hope that it will be useful,
 * but WITHOUT ANY WARRANTY; without even the implied warranty of
 * MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
 * GNU Affero General Public License for more details.
 *
 * You should have received a copy of the GNU Affero General Public
 * License along with this program. If not, see <https://www.gnu.org/licenses/>.
 */

// Voice-intake session state + SSE lifecycle (PAI-704). Module-singleton
// refs, same pattern as useAiAction: all mounted intake surfaces share one
// session. NOTE the useAiOptimize gotcha applies — destructure the returned
// refs in setup; the returned object is not a reactive() proxy.
//
// Event application is rev-idempotent: every persisted SSE event carries a
// per-session seq, and any event with seq <= the local head is dropped, so
// reconnect replays and local echoes can never double-apply. Unknown event
// kinds are ignored (forward compatibility with later slices).

import { computed, ref } from "vue";

import {
  abandonIntakeSession,
  createIntakeCheckpoint,
  createIntakeSession,
  getIntakeSession,
  getIntakeState,
  intakeStreamURL,
  patchIntakeSession,
  postIntakeTranscript,
  restoreIntakeSession,
  type IntakeCheckpoint,
  type IntakeEventMeta,
  type IntakeLanguage,
  type IntakeSession,
  type IntakeSpecPayload,
  type IntakeStreamEvent,
} from "@/api/intake";
import { errMsg } from "@/api/client";

export type IntakeConnection = "disconnected" | "connecting" | "open" | "retrying";

export interface IntakeRevision {
  seq: number;
  kind: IntakeEventMeta["kind"];
  source: string;
  label?: string;
  at: string;
}

export interface IntakeTicketPreview {
  title: string;
  issue_type: "ticket" | "epic" | "task";
  description: string;
  acceptance_criteria?: string;
  language?: IntakeLanguage;
}

export interface IntakeStage {
  stage: string;
  state: "running" | "ok" | "degraded" | "error";
  reason?: string;
}

export interface IntakeMatchCandidate {
  project_id: number;
  key: string;
  name: string;
  score: number;
  confidence: "high" | "med" | "low";
  rationale?: string;
}

export interface IntakeProjectMatch {
  matches: IntakeMatchCandidate[];
  threshold: number;
  detected_project_id: number | null;
  detected_score: number;
}

const session = ref<IntakeSession | null>(null);
const connection = ref<IntakeConnection>("disconnected");
const transcript = ref("");
const spec = ref<IntakeSpecPayload | null>(null);
const specSeq = ref(0);
const ticketPreview = ref<IntakeTicketPreview | null>(null);
const stage = ref<IntakeStage | null>(null);
const projectMatch = ref<IntakeProjectMatch | null>(null);
const revIndex = ref<IntakeRevision[]>([]);
const checkpoints = ref<IntakeCheckpoint[]>([]);
const viewSeq = ref<number | null>(null); // null = live/HEAD
const viewState = ref<{ transcript: string; spec: IntakeSpecPayload | null } | null>(null);
const error = ref<string | null>(null);
const lastError = error; // alias matching the useAiAction naming convention

let source: EventSource | null = null;
let lastSeq = 0;
let clientSeqCounter = 0;

function headRev(): number {
  return session.value?.rev ?? 0;
}

function applyPersistedEvent(ev: IntakeStreamEvent) {
  const seq = ev.seq ?? 0;
  if (seq <= 0 || seq <= lastSeq) return;
  lastSeq = seq;
  if (session.value && seq > session.value.rev) session.value.rev = seq;

  revIndex.value.push({
    seq,
    kind: ev.kind as IntakeEventMeta["kind"],
    source: ev.source ?? "ai",
    label: ev.label || undefined,
    at: ev.created_at ?? "",
  });

  switch (ev.kind) {
    case "transcript_chunk": {
      const text = (ev.payload as { text?: string } | undefined)?.text ?? "";
      if (text) transcript.value = transcript.value ? `${transcript.value}\n${text}` : text;
      break;
    }
    case "spec": {
      const payload = ev.payload as IntakeSpecPayload | undefined;
      if (payload?.markdown !== undefined) {
        spec.value = payload;
        specSeq.value = seq;
      }
      break;
    }
    case "ticket_preview": {
      const payload = ev.payload as IntakeTicketPreview | undefined;
      if (payload?.title !== undefined) ticketPreview.value = payload;
      break;
    }
    case "project_match": {
      const payload = ev.payload as IntakeProjectMatch | undefined;
      if (payload?.matches !== undefined) {
        projectMatch.value = payload;
        if (session.value) {
          session.value.detected_project_id = payload.detected_project_id;
          session.value.detected_score = payload.detected_score;
        }
      }
      break;
    }
    case "checkpoint": {
      if (ev.label) checkpoints.value.push({ seq, label: ev.label, created_at: ev.created_at });
      break;
    }
    case "language": {
      const lang = (ev.payload as { language?: IntakeLanguage } | undefined)?.language;
      if (lang && session.value) session.value.language = lang;
      break;
    }
    default:
      // summaries / ticket_preview / project_match / impacts / restore /
      // status arrive in later slices; restore's follow-up snapshots are
      // plain spec/… events and apply above. Unknown kinds are ignored.
      break;
  }
}

async function hydrate(id: number) {
  const head = await getIntakeSession(id);
  session.value = head.session;
  transcript.value = head.state.transcript;
  const specArtifact = head.state.artifacts?.spec as IntakeSpecPayload | undefined;
  spec.value = specArtifact ?? null;
  specSeq.value = specArtifact ? head.state.at_seq : 0;
  ticketPreview.value =
    (head.state.artifacts?.ticket_preview as IntakeTicketPreview | undefined) ?? null;
  projectMatch.value =
    (head.state.artifacts?.project_match as IntakeProjectMatch | undefined) ?? null;
  checkpoints.value = head.checkpoints ?? [];
  lastSeq = head.session.rev;
  // The metadata timeline is rebuilt lazily; SSE replay from 0 would
  // duplicate hydration, so the stream resumes from the hydrated rev.
  revIndex.value = [];
}

function connect(id: number) {
  disconnect();
  connection.value = "connecting";
  const es = new EventSource(intakeStreamURL(id, lastSeq));
  source = es;
  let opened = false;
  es.onopen = () => {
    const firstOpen = !opened;
    opened = true;
    connection.value = "open";
    if (!firstOpen) {
      // Native EventSource reconnected: re-hydrate to heal any dropped
      // (buffer-full) events, then continue streaming from the new head.
      void hydrate(id).catch(() => {});
    }
  };
  es.onmessage = () => {}; // events arrive with explicit types below
  for (const kind of [
    "transcript_chunk",
    "spec",
    "summaries",
    "ticket_preview",
    "project_match",
    "impacts",
    "checkpoint",
    "restore",
    "language",
    "status",
  ]) {
    es.addEventListener(kind, (event) => {
      const msg = event as MessageEvent<string>;
      let ev: IntakeStreamEvent;
      try {
        ev = JSON.parse(msg.data) as IntakeStreamEvent;
      } catch {
        return;
      }
      ev.kind = ev.kind || kind;
      applyPersistedEvent(ev);
    });
  }
  es.addEventListener("stage", (event) => {
    const msg = event as MessageEvent<string>;
    try {
      const ev = JSON.parse(msg.data) as IntakeStreamEvent;
      const payload = ev.payload as IntakeStage | undefined;
      if (payload?.stage) stage.value = payload;
    } catch {
      /* ignore */
    }
  });
  es.addEventListener("session", (event) => {
    const msg = event as MessageEvent<string>;
    try {
      const s = JSON.parse(msg.data) as IntakeSession;
      if (session.value && s.id === session.value.id) session.value = s;
    } catch {
      /* ignore */
    }
  });
  es.onerror = () => {
    if (!opened) {
      es.close();
      if (source === es) source = null;
      connection.value = "disconnected";
    } else {
      connection.value = "retrying";
    }
  };
}

function disconnect() {
  source?.close();
  source = null;
  connection.value = "disconnected";
}

async function start(language?: IntakeLanguage) {
  error.value = null;
  try {
    const s = await createIntakeSession(language);
    session.value = s;
    transcript.value = "";
    spec.value = null;
    specSeq.value = 0;
    ticketPreview.value = null;
    stage.value = null;
    projectMatch.value = null;
    revIndex.value = [];
    checkpoints.value = [];
    viewSeq.value = null;
    viewState.value = null;
    lastSeq = s.rev;
    clientSeqCounter = 0;
    connect(s.id);
  } catch (e) {
    error.value = errMsg(e, "Could not start an intake session");
    throw e;
  }
}

async function resume(id: number) {
  error.value = null;
  await hydrate(id);
  connect(id);
}

async function sendTranscript(text: string) {
  const s = session.value;
  if (!s) return;
  const trimmed = text.trim();
  if (!trimmed) return;
  clientSeqCounter += 1;
  try {
    await postIntakeTranscript(s.id, trimmed, clientSeqCounter);
  } catch (e) {
    error.value = errMsg(e, "Could not send transcript");
    throw e;
  }
}

async function saveSpec(markdown: string) {
  const s = session.value;
  if (!s) return;
  try {
    await patchIntakeSession(s.id, { spec_markdown: markdown });
  } catch (e) {
    error.value = errMsg(e, "Could not save the specification");
    throw e;
  }
}

async function setLanguage(language: IntakeLanguage) {
  const s = session.value;
  if (!s || s.language === language) return;
  await patchIntakeSession(s.id, { language });
}

/** Pin the session to a project (manual override); 0 releases the pin. */
async function pinProject(projectId: number) {
  const s = session.value;
  if (!s) return;
  const updated = await patchIntakeSession(s.id, { pinned_project_id: projectId });
  session.value = updated;
}

async function saveCheckpoint(label: string) {
  const s = session.value;
  if (!s) return;
  try {
    await createIntakeCheckpoint(s.id, label);
  } catch (e) {
    error.value = errMsg(e, "Could not save the checkpoint");
    throw e;
  }
}

/** Scrub to a seq (read-only preview); null returns to live. */
async function scrub(seq: number | null) {
  const s = session.value;
  if (!s) return;
  if (seq === null || seq >= headRev()) {
    viewSeq.value = null;
    viewState.value = null;
    return;
  }
  const state = await getIntakeState(s.id, seq);
  viewSeq.value = seq;
  viewState.value = {
    transcript: state.transcript,
    spec: (state.artifacts?.spec as IntakeSpecPayload | undefined) ?? null,
  };
}

async function restore(seq: number) {
  const s = session.value;
  if (!s) return;
  try {
    const res = await restoreIntakeSession(s.id, seq);
    session.value = res.session;
    transcript.value = res.state.transcript;
    const sp = res.state.artifacts?.spec as IntakeSpecPayload | undefined;
    spec.value = sp ?? null;
    specSeq.value = sp ? res.state.at_seq : 0;
    viewSeq.value = null;
    viewState.value = null;
    // SSE will deliver the appended restore events; lastSeq advances there.
  } catch (e) {
    error.value = errMsg(e, "Could not restore");
    throw e;
  }
}

async function abandon() {
  const s = session.value;
  if (!s) return;
  disconnect();
  try {
    await abandonIntakeSession(s.id);
  } finally {
    session.value = null;
  }
}

function reset() {
  disconnect();
  session.value = null;
  transcript.value = "";
  spec.value = null;
  specSeq.value = 0;
  ticketPreview.value = null;
  stage.value = null;
  projectMatch.value = null;
  revIndex.value = [];
  checkpoints.value = [];
  viewSeq.value = null;
  viewState.value = null;
  error.value = null;
  lastSeq = 0;
}

const isViewingHistory = computed(() => viewSeq.value !== null);

export function useIntakeSession() {
  return {
    session,
    connection,
    transcript,
    spec,
    specSeq,
    ticketPreview,
    stage,
    projectMatch,
    pinProject,
    revIndex,
    checkpoints,
    viewSeq,
    viewState,
    isViewingHistory,
    error,
    lastError,
    start,
    resume,
    sendTranscript,
    saveSpec,
    setLanguage,
    saveCheckpoint,
    scrub,
    restore,
    abandon,
    reset,
    disconnect,
  };
}
