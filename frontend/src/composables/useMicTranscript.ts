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
  clearInterval(gateTimer);
  gateTimer = 0;
  document.removeEventListener("visibilitychange", onVisibilityChange);
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
  if (!stream) return;
  const myGen = generation;
  chunks = [];
  const rec = new MediaRecorder(stream);
  recorder = rec;
  rec.ondataavailable = (e) => {
    if (myGen === generation && e.data.size > 0) chunks.push(e.data);
  };
  rec.onstop = () => {
    // A stopped recorder from a previous generation must never act:
    // its stream is dead and re-arming would fork a zombie loop.
    if (myGen !== generation) return;
    const blob = new Blob(chunks, { type: rec.mimeType || "audio/webm" });
    chunks = [];
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
    state.value = "error";
    errorMessage.value = "Microphone unavailable or permission denied — you can type instead.";
    return false;
  }
  if (myGeneration !== generation) {
    acquiredStream.getTracks().forEach((track) => track.stop());
    return false;
  }
  stream = acquiredStream;
  audioCtx = new AudioContext();
  analyser = audioCtx.createAnalyser();
  analyser.fftSize = 512;
  audioCtx.createMediaStreamSource(stream).connect(analyser);
  document.addEventListener("visibilitychange", onVisibilityChange);
  state.value = "listening";
  armRecorder();
  return true;
}

function stop() {
  cleanup();
  state.value = "idle";
}

const isActive = computed(
  () =>
    state.value === "starting" || state.value === "listening" || state.value === "transcribing",
);

export function useMicTranscript() {
  return { state, level, errorMessage, isActive, start, stop, micSupported };
}
