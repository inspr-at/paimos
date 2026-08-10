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
import { api, errMsg } from "@/api/client";

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
  /** PAI-715: true while nothing was selected yet — the first pick is threshold-free. */
  first_detection?: boolean;
}

export interface IntakeImpactEntry {
  issue_id?: number;
  issue_key: string;
  title: string;
  category: "touches" | "conflicts" | "extends" | "related";
  mapped_relation?: string;
  via: "retrieval" | "graph";
}

export interface IntakeSummaries {
  eli5: string;
  eli10: string;
  eli15: string;
  language?: IntakeLanguage;
}

export interface IntakeImpacts {
  project_id: number;
  impacted: IntakeImpactEntry[];
  related: IntakeImpactEntry[];
  graph_hits: { entity_type: string; title: string }[];
}

const session = ref<IntakeSession | null>(null);
const connection = ref<IntakeConnection>("disconnected");
const transcript = ref("");
const spec = ref<IntakeSpecPayload | null>(null);
const specSeq = ref(0);
const ticketPreview = ref<IntakeTicketPreview | null>(null);
const stage = ref<IntakeStage | null>(null);
const projectMatch = ref<IntakeProjectMatch | null>(null);
const createdIssue = ref<{ id: number; issue_key: string; project_id: number | null } | null>(null);
const impacts = ref<IntakeImpacts | null>(null);
const summaries = ref<IntakeSummaries | null>(null);
const summariesSeq = ref(0);
const revIndex = ref<IntakeRevision[]>([]);
const checkpoints = ref<IntakeCheckpoint[]>([]);
const viewSeq = ref<number | null>(null); // null = live/HEAD
const viewState = ref<{ transcript: string; spec: IntakeSpecPayload | null } | null>(null);
const error = ref<string | null>(null);
const lastError = error; // alias matching the useAiAction naming convention

let source: EventSource | null = null;
let lastSeq = 0;
let clientSeqCounter = 0;

// PAI-734: per-language artifact caches. The displayed spec/summaries/
// ticket refs always point at the active language's cache (falling back
// to the newest cached language while a first generation is pending),
// so an EN/DE toggle is a view switch — never a regeneration wait.
interface CachedArtifact<T> {
  payload: T;
  seq: number;
}
let specCache: Partial<Record<IntakeLanguage, CachedArtifact<IntakeSpecPayload>>> = {};
let summariesCache: Partial<Record<IntakeLanguage, CachedArtifact<IntakeSummaries>>> = {};
let previewCache: Partial<Record<IntakeLanguage, CachedArtifact<IntakeTicketPreview>>> = {};

function activeLanguage(): IntakeLanguage {
  return session.value?.language ?? "en";
}

function newestCached<T>(
  cache: Partial<Record<IntakeLanguage, CachedArtifact<T>>>,
): CachedArtifact<T> | null {
  let best: CachedArtifact<T> | null = null;
  for (const entry of Object.values(cache) as CachedArtifact<T>[]) {
    if (entry && (!best || entry.seq > best.seq)) best = entry;
  }
  return best;
}

function clearArtifactCaches() {
  specCache = {};
  summariesCache = {};
  previewCache = {};
}

function refreshDisplayedArtifacts() {
  const lang = activeLanguage();
  const sp = specCache[lang] ?? newestCached(specCache);
  spec.value = sp?.payload ?? null;
  specSeq.value = sp?.seq ?? 0;
  const su = summariesCache[lang] ?? newestCached(summariesCache);
  summaries.value = su?.payload ?? null;
  summariesSeq.value = su?.seq ?? 0;
  const tp = previewCache[lang] ?? newestCached(previewCache);
  ticketPreview.value = tp?.payload ?? null;
}

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
        specCache[payload.language ?? activeLanguage()] = { payload, seq };
        refreshDisplayedArtifacts();
      }
      break;
    }
    case "ticket_preview": {
      const payload = ev.payload as IntakeTicketPreview | undefined;
      if (payload?.title !== undefined) {
        previewCache[payload.language ?? activeLanguage()] = { payload, seq };
        refreshDisplayedArtifacts();
      }
      break;
    }
    case "summaries": {
      const payload = ev.payload as IntakeSummaries | undefined;
      if (payload && (payload.eli5 || payload.eli10 || payload.eli15)) {
        summariesCache[payload.language ?? activeLanguage()] = { payload, seq };
        refreshDisplayedArtifacts();
      }
      break;
    }
    case "impacts": {
      const payload = ev.payload as IntakeImpacts | undefined;
      if (payload?.impacted !== undefined) impacts.value = payload;
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
      if (lang && session.value) {
        session.value.language = lang;
        refreshDisplayedArtifacts();
      }
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
  // State artifacts arrive language-preferred (server-side PAI-734
  // selection); seed the caches under each payload's own language.
  clearArtifactCaches();
  seedArtifactCaches(head.state.artifacts, head.state.at_seq);
  refreshDisplayedArtifacts();
  projectMatch.value =
    (head.state.artifacts?.project_match as IntakeProjectMatch | undefined) ?? null;
  impacts.value = (head.state.artifacts?.impacts as IntakeImpacts | undefined) ?? null;
  checkpoints.value = head.checkpoints ?? [];
  lastSeq = head.session.rev;
  // The metadata timeline is rebuilt lazily; SSE replay from 0 would
  // duplicate hydration, so the stream resumes from the hydrated rev.
  revIndex.value = [];
}

function seedArtifactCaches(
  artifacts: Partial<Record<string, unknown>> | undefined,
  atSeq: number,
) {
  const sp = artifacts?.spec as IntakeSpecPayload | undefined;
  if (sp?.markdown !== undefined) {
    const lang = sp.language ?? activeLanguage();
    if ((specCache[lang]?.seq ?? 0) <= atSeq) specCache[lang] = { payload: sp, seq: atSeq };
  }
  const su = artifacts?.summaries as IntakeSummaries | undefined;
  if (su && (su.eli5 || su.eli10 || su.eli15)) {
    const lang = su.language ?? activeLanguage();
    if ((summariesCache[lang]?.seq ?? 0) <= atSeq)
      summariesCache[lang] = { payload: su, seq: atSeq };
  }
  const tp = artifacts?.ticket_preview as IntakeTicketPreview | undefined;
  if (tp?.title !== undefined) {
    const lang = tp.language ?? activeLanguage();
    if ((previewCache[lang]?.seq ?? 0) <= atSeq) previewCache[lang] = { payload: tp, seq: atSeq };
  }
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
    clearArtifactCaches();
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
  // Optimistic view switch: the cached artifacts for the new language
  // display instantly (the SSE echo of the toggle is seq-guarded and
  // idempotent on top of this).
  s.language = language;
  refreshDisplayedArtifacts();
  if (!specCache[language]) {
    // Nothing cached locally — hydration only carried the previous
    // language. One cheap head-state fetch heals the cache when the
    // server has a generation for this language; when it has none, the
    // orchestrator is already regenerating and SSE delivers it.
    try {
      const state = await getIntakeState(s.id);
      seedArtifactCaches(state.artifacts, state.at_seq);
      refreshDisplayedArtifacts();
    } catch {
      /* non-fatal: SSE heals the view */
    }
  }
}

/** Pin the session to a project (manual override); 0 releases the pin. */
async function pinProject(projectId: number) {
  const s = session.value;
  if (!s) return;
  const updated = await patchIntakeSession(s.id, { pinned_project_id: projectId });
  session.value = updated;
}

/** The project the session currently targets: pin wins over detection. */
function activeProjectId(): number | null {
  const s = session.value;
  if (!s) return null;
  return s.pinned_project_id ?? s.detected_project_id ?? null;
}

/** File the issue from the session's current state (idempotent server-side). */
async function createIssue() {
  const s = session.value;
  const pid = activeProjectId();
  if (!s || pid == null) return null;
  try {
    const issue = await api.post<{ id: number; issue_key: string; project_id: number | null }>(
      `/projects/${pid}/intake-sessions/${s.id}/issue`,
      {},
    );
    createdIssue.value = issue;
    return issue;
  } catch (e) {
    error.value = errMsg(e, "Could not create the issue");
    throw e;
  }
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
    // Time travel invalidates the caches wholesale; reseed from the
    // restored state (SSE re-delivers the appended snapshots idempotently).
    clearArtifactCaches();
    seedArtifactCaches(res.state.artifacts, res.state.at_seq);
    refreshDisplayedArtifacts();
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
  clearArtifactCaches();
  spec.value = null;
  specSeq.value = 0;
  ticketPreview.value = null;
  stage.value = null;
  projectMatch.value = null;
  createdIssue.value = null;
  impacts.value = null;
  summaries.value = null;
  summariesSeq.value = 0;
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
    createdIssue,
    createIssue,
    impacts,
    summaries,
    summariesSeq,
    activeProjectId,
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
