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

// PAI-710: continuous-listening microphone TranscriptSource for the Voice
// Intake workbench. Ported from the amt-start (start.augmentoring.com)
// voice pipeline research — the battle-tested batch pattern:
//
//   getUserMedia → MediaRecorder per utterance, cut by a client-side RMS
//   energy gate (threshold 0.035, 950 ms trailing silence → utterance
//   ends, 15 s with no speech at all → stop listening), each utterance
//   blob POSTed to the session's audio endpoint where the backend
//   transcribes it (ElevenLabs Scribe) and appends a transcript chunk.
//   The chunk then arrives through the normal session SSE — this
//   composable never touches transcript state.
//
// Audio never persists anywhere: not here, not on the server
// (INV-INTAKE-06 — transcribed and dropped).

import { computed, ref } from "vue";

export type MicState = "idle" | "starting" | "listening" | "transcribing" | "error";

const RMS_SPEECH_THRESHOLD = 0.035;
const TRAILING_SILENCE_MS = 950;
const NO_SPEECH_GIVEUP_MS = 15_000;
const MIN_UTTERANCE_BYTES = 2_000; // below this the blob is silence/click noise

const state = ref<MicState>("idle");
const level = ref(0); // smoothed 0..1 for the pulse indicator
const errorMessage = ref<string | null>(null);

let stream: MediaStream | null = null;
let audioCtx: AudioContext | null = null;
let analyser: AnalyserNode | null = null;
let recorder: MediaRecorder | null = null;
let chunks: Blob[] = [];
let rafId = 0;
let stopping = false;
let onUtterance: ((blob: Blob) => Promise<void>) | null = null;

export function micSupported(): boolean {
  return (
    typeof navigator !== "undefined" &&
    !!navigator.mediaDevices?.getUserMedia &&
    typeof MediaRecorder !== "undefined" &&
    window.isSecureContext
  );
}

function cleanup() {
  cancelAnimationFrame(rafId);
  rafId = 0;
  try {
    if (recorder && recorder.state !== "inactive") recorder.stop();
  } catch {
    /* already stopped */
  }
  recorder = null;
  chunks = [];
  stream?.getTracks().forEach((t) => t.stop());
  stream = null;
  void audioCtx?.close().catch(() => {});
  audioCtx = null;
  analyser = null;
  level.value = 0;
}

/** One utterance cycle: record until the silence gate closes. */
function armRecorder() {
  if (!stream || stopping) return;
  chunks = [];
  const rec = new MediaRecorder(stream);
  recorder = rec;
  rec.ondataavailable = (e) => {
    if (e.data.size > 0) chunks.push(e.data);
  };
  rec.onstop = () => {
    const blob = new Blob(chunks, { type: rec.mimeType || "audio/webm" });
    chunks = [];
    if (stopping) return;
    if (blob.size >= MIN_UTTERANCE_BYTES && onUtterance) {
      state.value = "transcribing";
      void onUtterance(blob)
        .catch(() => {
          /* surfaced via the session error ref by the caller */
        })
        .finally(() => {
          if (!stopping) {
            state.value = "listening";
            armRecorder();
          }
        });
    } else {
      armRecorder(); // too short — keep listening
    }
  };
  rec.start(200); // 200 ms timeslice, per the amt-start tuning

  // RMS gate on requestAnimationFrame.
  const data = new Uint8Array(analyser!.fftSize);
  let speechHeard = false;
  let lastSpeechAt = performance.now();
  const startedAt = performance.now();
  const tick = () => {
    if (!analyser || recorder !== rec || stopping) return;
    analyser.getByteTimeDomainData(data);
    let sum = 0;
    for (let i = 0; i < data.length; i++) {
      const v = (data[i] - 128) / 128;
      sum += v * v;
    }
    const rms = Math.sqrt(sum / data.length);
    level.value = level.value + (Math.min(1, rms * 6) - level.value) * 0.25;
    const now = performance.now();
    if (rms > RMS_SPEECH_THRESHOLD) {
      speechHeard = true;
      lastSpeechAt = now;
    }
    if (speechHeard && now - lastSpeechAt > TRAILING_SILENCE_MS) {
      rec.stop(); // utterance complete → onstop ships it
      return;
    }
    if (!speechHeard && now - startedAt > NO_SPEECH_GIVEUP_MS) {
      stop(); // nobody is talking — stop listening entirely
      return;
    }
    rafId = requestAnimationFrame(tick);
  };
  rafId = requestAnimationFrame(tick);
}

/** Start continuous listening; utterances flow to `handleUtterance`. */
async function start(handleUtterance: (blob: Blob) => Promise<void>): Promise<boolean> {
  if (state.value !== "idle" && state.value !== "error") return true;
  errorMessage.value = null;
  state.value = "starting";
  stopping = false;
  onUtterance = handleUtterance;
  try {
    stream = await navigator.mediaDevices.getUserMedia({
      audio: { echoCancellation: true, noiseSuppression: true, autoGainControl: true },
    });
  } catch {
    state.value = "error";
    errorMessage.value = "Microphone unavailable or permission denied — you can type instead.";
    return false;
  }
  audioCtx = new AudioContext();
  analyser = audioCtx.createAnalyser();
  analyser.fftSize = 512;
  audioCtx.createMediaStreamSource(stream).connect(analyser);
  state.value = "listening";
  armRecorder();
  return true;
}

function stop() {
  stopping = true;
  cleanup();
  state.value = "idle";
}

const isActive = computed(() => state.value === "listening" || state.value === "transcribing");

export function useMicTranscript() {
  return { state, level, errorMessage, isActive, start, stop, micSupported };
}
