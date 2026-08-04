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

// Voice-intake workbench API (PAI-704, epic PAI-703). Thin typed wrappers
// over the session endpoints plus the SSE stream URL builder. The session
// event log is append-only; `seq` is the per-session cursor used both for
// time-travel and SSE resume.

import { api } from "@/api/client";

export type IntakeLanguage = "en" | "de";
export type IntakeSessionStatus = "active" | "completed" | "abandoned";

export type IntakeEventKind =
  | "transcript_chunk"
  | "spec"
  | "summaries"
  | "ticket_preview"
  | "project_match"
  | "impacts"
  | "checkpoint"
  | "restore"
  | "language"
  | "status";

export interface IntakeSession {
  id: number;
  user_id: number;
  status: IntakeSessionStatus;
  language: IntakeLanguage;
  detected_project_id: number | null;
  detected_score: number;
  pinned_project_id: number | null;
  created_issue_id: number | null;
  transcript_bytes: number;
  rev: number;
  created_at: string;
  updated_at: string;
  completed_at: string | null;
}

export interface IntakeEventMeta {
  seq: number;
  kind: IntakeEventKind;
  source: "ai" | "user" | "system";
  label?: string;
  bytes: number;
  created_at: string;
}

export interface IntakeCheckpoint {
  seq: number;
  label: string;
  created_at?: string;
}

export interface IntakeSpecPayload {
  markdown: string;
  language: IntakeLanguage;
}

export interface IntakeState {
  at_seq: number;
  transcript: string;
  artifacts: Partial<Record<string, unknown>> & { spec?: IntakeSpecPayload };
}

export interface IntakeHead {
  session: IntakeSession;
  state: IntakeState;
  checkpoints: IntakeCheckpoint[];
}

/** One SSE message; persisted events carry seq > 0, ephemeral ones seq 0. */
export interface IntakeStreamEvent {
  seq?: number;
  kind: string;
  source?: string;
  label?: string;
  payload?: unknown;
  created_at?: string;
}

export function createIntakeSession(language?: IntakeLanguage) {
  return api.post<IntakeSession>("/intake/sessions", language ? { language } : {});
}

export function listIntakeSessions() {
  return api.get<IntakeSession[]>("/intake/sessions");
}

export function getIntakeSession(id: number) {
  return api.get<IntakeHead>(`/intake/sessions/${id}`);
}

export function abandonIntakeSession(id: number) {
  return api.delete<void>(`/intake/sessions/${id}`);
}

export function postIntakeTranscript(id: number, text: string, clientSeq?: number) {
  return api.post<{ seq: number; transcript_bytes: number; deduped?: boolean }>(
    `/intake/sessions/${id}/transcript`,
    clientSeq === undefined ? { text } : { text, client_seq: clientSeq },
  );
}

export function patchIntakeSession(
  id: number,
  patch: { language?: IntakeLanguage; pinned_project_id?: number; spec_markdown?: string },
) {
  return api.patch<IntakeSession>(`/intake/sessions/${id}`, patch);
}

export function createIntakeCheckpoint(id: number, label: string) {
  return api.post<IntakeCheckpoint>(`/intake/sessions/${id}/checkpoints`, { label });
}

export function listIntakeEvents(id: number, sinceSeq = 0) {
  return api.get<IntakeEventMeta[]>(`/intake/sessions/${id}/events?since_seq=${sinceSeq}`);
}

export function getIntakeState(id: number, atSeq?: number) {
  const q = atSeq === undefined ? "" : `?at_seq=${atSeq}`;
  return api.get<IntakeState>(`/intake/sessions/${id}/state${q}`);
}

export function restoreIntakeSession(id: number, seq: number) {
  return api.post<{ session: IntakeSession; state: IntakeState }>(
    `/intake/sessions/${id}/restore`,
    { seq },
  );
}

export function intakeStreamURL(id: number, sinceSeq = 0): string {
  return `/api/intake/sessions/${id}/stream?since=${encodeURIComponent(String(sinceSeq))}`;
}

/**
 * PAI-710: ship one spoken utterance for server-side transcription. Raw
 * audio body (not multipart), so it bypasses the JSON client; CSRF is
 * echoed manually the same way api.upload does.
 */
export async function postIntakeAudio(
  id: number,
  blob: Blob,
): Promise<{ seq: number; text: string }> {
  const csrf =
    document.cookie
      .split("; ")
      .find((c) => c.startsWith("csrf_token="))
      ?.split("=")[1] ?? "";
  const res = await fetch(`/api/intake/sessions/${id}/audio`, {
    method: "POST",
    credentials: "include",
    headers: {
      "Content-Type": blob.type || "audio/webm",
      "X-CSRF-Token": csrf,
    },
    body: blob,
  });
  if (!res.ok) {
    const data = await res.json().catch(() => null);
    throw new Error(
      (data && (data.detail || data.error)) || `voice transcription failed (${res.status})`,
    );
  }
  return (await res.json()) as { seq: number; text: string };
}

/** Whether speech input is configured on this instance (from /ai/status). */
export async function voiceAvailable(): Promise<boolean> {
  try {
    const res = await fetch("/api/ai/status", { credentials: "include" });
    if (!res.ok) return false;
    const data = (await res.json()) as { voice_available?: boolean };
    return data.voice_available === true;
  } catch {
    return false;
  }
}
