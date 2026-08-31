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

// PAI-710 / PAI-719: continuous-listening microphone TranscriptSource for
// the Voice Intake workbench. Batch pattern from the amt-start research:
// MediaRecorder utterances cut by an RMS energy gate, each blob shipped to
// the session's audio endpoint; transcripts return via the session SSE.
//
// Lifecycle guarantees (PAI-719 — "the user can talk continuously"):
//   - Listening NEVER silently dies. There is no no-speech give-up; long
//     silence just recycles the recorder (discarding the silent blob) so
//     memory stays bounded while the gate keeps listening.
//   - Every recorder is generation-tagged. stop()/cleanup() bumps the
//     generation, so a stopped recorder's ASYNC onstop can never re-arm a
//     zombie loop after a quick stop→start (the race that froze capture).
//   - The silence gate runs on a 40 ms interval, not requestAnimationFrame
//     — RAF freezes in hidden tabs and would wedge utterances open.
//     Hiding the tab flushes the open utterance immediately.
//   - Audio never persists anywhere (INV-INTAKE-06).

import { computed, ref } from "vue";

export type MicState = "idle" | "starting" | "listening" | "transcribing" | "error";

const RMS_SPEECH_THRESHOLD = 0.035;
const TRAILING_SILENCE_MS = 950;
const SILENT_RECYCLE_MS = 15_000; // re-arm (not give up) after silent stretches
const MIN_UTTERANCE_BYTES = 2_000; // below this the blob is silence/click noise

const state = ref<MicState>("idle");
const level = ref(0); // smoothed 0..1 for the pulse indicator
const errorMessage = ref<string | null>(null);

let stream: MediaStream | null = null;
let audioCtx: AudioContext | null = null;
let analyser: AnalyserNode | null = null;
let recorder: MediaRecorder | null = null;
let chunks: Blob[] = [];
let gateTimer = 0;
let generation = 0; // bumped on every cleanup — stale callbacks self-discard
let onUtterance: ((blob: Blob) => Promise<void>) | null = null;
let trackEndHandlers: Array<{ track: MediaStreamTrack; handler: () => void }> = [];
let finishRequestedGeneration: number | null = null;

export function micSupported(): boolean {
  return (
    typeof navigator !== "undefined" &&
    !!navigator.mediaDevices?.getUserMedia &&
    typeof MediaRecorder !== "undefined" &&
    window.isSecureContext
  );
}

function onVisibilityChange() {
  // Tab hidden while an utterance is open: flush it now so the audio
  // ships instead of accumulating while backgrounded.
  if (document.hidden && recorder && recorder.state === "recording") {
    try {
      recorder.stop();
    } catch {
      /* already stopped */
    }
  }
}

function cleanup() {
  generation++; // invalidate every in-flight callback of the old loop
  finishRequestedGeneration = null;
  // A stopped singleton must not retain the disposed Voice instance and its
  // authorized context through the last callback closure.
  onUtterance = null;
  clearInterval(gateTimer);
  gateTimer = 0;
  document.removeEventListener("visibilitychange", onVisibilityChange);
  try {
    if (recorder) recorder.onerror = null;
    if (recorder && recorder.state !== "inactive") recorder.stop();
  } catch {
    /* already stopped */
  }
  recorder = null;
  chunks = [];
  for (const { track, handler } of trackEndHandlers) track.removeEventListener?.("ended", handler);
  trackEndHandlers = [];
  stream?.getTracks().forEach((track) => {
    try {
      track.stop();
    } catch {
      /* the capture is already unusable */
    }
  });
  stream = null;
  try {
    void audioCtx?.close().catch(() => {});
  } catch {
    /* a partially constructed context may reject close synchronously */
  }
  audioCtx = null;
  analyser = null;
  level.value = 0;
}

function failCapture(expectedGeneration: number, message: string) {
  if (expectedGeneration !== generation) return;
  cleanup();
  errorMessage.value = message;
  state.value = "error";
}

/** One utterance cycle: record until the silence gate closes. */
function armRecorder(): boolean {
  if (!stream) return false;
  const myGen = generation;
  chunks = [];
  try {
    const rec = new MediaRecorder(stream);
    recorder = rec;
    rec.onerror = () => failCapture(myGen, "Microphone recording failed.");
    rec.ondataavailable = (e) => {
      if (myGen === generation && e.data.size > 0) chunks.push(e.data);
    };
    rec.onstop = () => {
      // A stopped recorder from a previous generation must never act:
      // its stream is dead and re-arming would fork a zombie loop.
      if (myGen !== generation) return;
      const blob = new Blob(chunks, { type: rec.mimeType || "audio/webm" });
      chunks = [];
      const finishing = finishRequestedGeneration === myGen;
      if (finishing) {
        const sink = onUtterance;
        finishRequestedGeneration = null;
        cleanup();
        if (blob.size >= MIN_UTTERANCE_BYTES && sink) {
          state.value = "transcribing";
          void sink(blob)
            .catch(() => {
              /* surfaced via the session error ref by the caller */
            })
            .finally(() => {
              if (state.value === "transcribing") state.value = "idle";
            });
        } else {
          state.value = "idle";
        }
        return;
      }
      if (blob.size >= MIN_UTTERANCE_BYTES && onUtterance) {
        state.value = "transcribing";
        void onUtterance(blob)
          .catch(() => {
            /* surfaced via the session error ref by the caller */
          })
          .finally(() => {
            if (myGen === generation) {
              state.value = "listening";
              armRecorder();
            }
          });
      } else {
        armRecorder(); // silent/too short — recycle and keep listening
      }
    };
    rec.start(200); // 200 ms timeslice, per the amt-start tuning

    // RMS silence gate — interval-based (see header).
    const data = new Uint8Array(analyser!.fftSize);
    let speechHeard = false;
    let lastSpeechAt = performance.now();
    const startedAt = performance.now();
    clearInterval(gateTimer);
    gateTimer = window.setInterval(() => {
      if (myGen !== generation || !analyser || recorder !== rec) {
        clearInterval(gateTimer);
        return;
      }
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
        clearInterval(gateTimer);
        rec.stop(); // utterance complete → onstop ships it
        return;
      }
      if (!speechHeard && now - startedAt > SILENT_RECYCLE_MS) {
        // Nothing said for a while: recycle the recorder so the silent
        // recording doesn't grow unbounded — but KEEP listening. The mic
        // only stops when the user says so (or the page is left).
        clearInterval(gateTimer);
        rec.stop(); // blob is silent → onstop discards it and re-arms
        return;
      }
    }, 40);
    return true;
  } catch {
    failCapture(myGen, "Microphone recording failed.");
    return false;
  }
}

/** Start continuous listening; utterances flow to `handleUtterance`. */
async function start(handleUtterance: (blob: Blob) => Promise<void>): Promise<boolean> {
  if (
    state.value === "starting" ||
    state.value === "listening" ||
    state.value === "transcribing"
  ) {
    onUtterance = handleUtterance; // idempotent restart: refresh the sink
    return true;
  }
  errorMessage.value = null;
  state.value = "starting";
  onUtterance = handleUtterance;
  const myGeneration = generation;
  let acquiredStream: MediaStream;
  try {
    acquiredStream = await navigator.mediaDevices.getUserMedia({
      audio: { echoCancellation: true, noiseSuppression: true, autoGainControl: true },
    });
  } catch {
    if (myGeneration !== generation) return false;
    cleanup();
    state.value = "error";
    errorMessage.value = "Microphone unavailable or permission denied — you can type instead.";
    return false;
  }
  if (myGeneration !== generation) {
    acquiredStream.getTracks().forEach((track) => track.stop());
    return false;
  }
  stream = acquiredStream;
  try {
    trackEndHandlers = acquiredStream.getTracks().map((track) => {
      const handler = () => failCapture(myGeneration, "Microphone stream ended.");
      track.addEventListener?.("ended", handler, { once: true });
      return { track, handler };
    });
    audioCtx = new AudioContext();
    analyser = audioCtx.createAnalyser();
    analyser.fftSize = 512;
    audioCtx.createMediaStreamSource(stream).connect(analyser);
    document.addEventListener("visibilitychange", onVisibilityChange);
    state.value = "listening";
    return armRecorder();
  } catch {
    failCapture(myGeneration, "Microphone initialization failed.");
    return false;
  }
}

function stop() {
  cleanup();
  state.value = "idle";
}

/** Finalize the current recorder once, release capture, and never re-arm.
 * Existing continuous-listening callers retain stop()'s discard semantics. */
function finish(): boolean {
  if (!recorder || recorder.state !== "recording") return false;
  finishRequestedGeneration = generation;
  clearInterval(gateTimer);
  gateTimer = 0;
  try {
    recorder.stop();
    return true;
  } catch {
    finishRequestedGeneration = null;
    return false;
  }
}

const isActive = computed(
  () =>
    state.value === "starting" || state.value === "listening" || state.value === "transcribing",
);

export interface MicTranscriptAdapter {
  state: typeof state;
  level: typeof level;
  errorMessage: typeof errorMessage;
  isActive: typeof isActive;
  start: typeof start;
  finish?: typeof finish;
  stop: typeof stop;
  micSupported: typeof micSupported;
}

export function useMicTranscript(): MicTranscriptAdapter {
  return { state, level, errorMessage, isActive, start, finish, stop, micSupported };
}
