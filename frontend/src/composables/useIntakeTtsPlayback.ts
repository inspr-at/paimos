// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>
// Licensed under AGPL-3.0-only; see LICENSE.

import type { Ref } from "vue";

export interface IntakePlaybackAudio {
  src: string;
  onended: ((event: Event) => void) | null;
  onerror: ((event: Event) => void) | null;
  pause(): void;
  play(): Promise<void>;
}

interface IntakeTtsPlaybackOptions {
  micActive: Readonly<Ref<boolean>>;
  stopMic: () => void;
  canResumeMic: () => boolean;
  resumeMic: () => void;
  createObjectURL?: (blob: Blob) => string;
  revokeObjectURL?: (url: string) => void;
  createAudio?: (url: string) => IntakePlaybackAudio;
}

/** Owns the mic interlock and every browser resource used by TTS playback. */
export function createIntakeTtsPlayback(options: IntakeTtsPlaybackOptions) {
  const createObjectURL = options.createObjectURL ?? URL.createObjectURL.bind(URL);
  const revokeObjectURL = options.revokeObjectURL ?? URL.revokeObjectURL.bind(URL);
  const createAudio = options.createAudio ?? ((url: string) => new Audio(url));

  let audio: IntakePlaybackAudio | null = null;
  let objectURL: string | null = null;
  let resumeMicAfterPlayback = false;
  let generation = 0;

  function releaseAudio() {
    if (audio) {
      audio.onended = null;
      audio.onerror = null;
      audio.pause();
      audio.src = "";
      audio = null;
    }
    if (objectURL) {
      revokeObjectURL(objectURL);
      objectURL = null;
    }
  }

  function finish(myGeneration: number) {
    if (myGeneration !== generation) return;
    generation += 1;
    const shouldResumeMic = resumeMicAfterPlayback;
    resumeMicAfterPlayback = false;
    releaseAudio();
    if (shouldResumeMic && options.canResumeMic()) options.resumeMic();
  }

  /** Cancel current/pending speech. Returns whether its mic-resume intent was preserved. */
  function cancel(shouldResumeMic = true): boolean {
    generation += 1;
    const inheritedResume = resumeMicAfterPlayback;
    resumeMicAfterPlayback = false;
    releaseAudio();
    if (shouldResumeMic && inheritedResume && options.canResumeMic()) options.resumeMic();
    return inheritedResume;
  }

  async function play(loadBlob: () => Promise<Blob>): Promise<boolean> {
    // Replacement inherits the old clip's resume intent but never restarts
    // capture between clips, where the new audio could transcribe itself.
    const inheritedResume = cancel(false);
    const wasMicActive = options.micActive.value;
    if (wasMicActive) options.stopMic();
    resumeMicAfterPlayback = inheritedResume || wasMicActive;
    const myGeneration = generation;

    try {
      const blob = await loadBlob();
      if (myGeneration !== generation) return false;

      objectURL = createObjectURL(blob);
      const nextAudio = createAudio(objectURL);
      audio = nextAudio;
      nextAudio.onended = () => finish(myGeneration);
      nextAudio.onerror = () => finish(myGeneration);
      await nextAudio.play();
      return myGeneration === generation;
    } catch {
      finish(myGeneration);
      return false;
    }
  }

  return { cancel, play };
}
