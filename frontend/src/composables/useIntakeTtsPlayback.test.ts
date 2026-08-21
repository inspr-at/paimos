// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>
// Licensed under AGPL-3.0-only; see LICENSE.

import { ref } from "vue";
import { describe, expect, it, vi } from "vitest";

import {
  createIntakeTtsPlayback,
  type IntakePlaybackAudio,
} from "./useIntakeTtsPlayback";

class FakeAudio implements IntakePlaybackAudio {
  src: string;
  onended: ((event: Event) => void) | null = null;
  onerror: ((event: Event) => void) | null = null;
  pause = vi.fn();
  play = vi.fn(async () => {});

  constructor(src: string) {
    this.src = src;
  }
}

function fixture() {
  const micActive = ref(true);
  const stopMic = vi.fn(() => {
    micActive.value = false;
  });
  const resumeMic = vi.fn(() => {
    micActive.value = true;
  });
  const revoked: string[] = [];
  const audios: FakeAudio[] = [];
  const activeChanges: boolean[] = [];
  let nextURL = 0;
  const playback = createIntakeTtsPlayback({
    micActive,
    stopMic,
    canResumeMic: () => true,
    resumeMic,
    onActiveChange: (active) => activeChanges.push(active),
    createObjectURL: () => `blob:voice-${++nextURL}`,
    revokeObjectURL: (url) => revoked.push(url),
    createAudio: (url) => {
      const audio = new FakeAudio(url);
      audios.push(audio);
      return audio;
    },
  });
  return { activeChanges, audios, micActive, playback, resumeMic, revoked, stopMic };
}

describe("createIntakeTtsPlayback", () => {
  it("replaces speech without resuming the microphone between clips", async () => {
    const f = fixture();

    await expect(f.playback.play(async () => new Blob(["first"]))).resolves.toBe(true);
    expect(f.activeChanges).toEqual([true]);
    expect(f.stopMic).toHaveBeenCalledOnce();
    expect(f.resumeMic).not.toHaveBeenCalled();

    const staleEnd = f.audios[0].onended;
    await expect(f.playback.play(async () => new Blob(["second"]))).resolves.toBe(true);
    expect(f.activeChanges).toEqual([true]);
    expect(f.audios[0].pause).toHaveBeenCalledOnce();
    expect(f.revoked).toEqual(["blob:voice-1"]);
    expect(f.resumeMic).not.toHaveBeenCalled();

    staleEnd?.(new Event("ended"));
    expect(f.resumeMic).not.toHaveBeenCalled();

    f.audios[1].onended?.(new Event("ended"));
    expect(f.activeChanges).toEqual([true, false]);
    expect(f.revoked).toEqual(["blob:voice-1", "blob:voice-2"]);
    expect(f.resumeMic).toHaveBeenCalledOnce();
  });

  it("revokes the current URL exactly once after a playback error", async () => {
    const f = fixture();
    await f.playback.play(async () => new Blob(["speech"]));

    const onerror = f.audios[0].onerror;
    onerror?.(new Event("error"));
    onerror?.(new Event("error"));

    expect(f.revoked).toEqual(["blob:voice-1"]);
    expect(f.activeChanges).toEqual([true, false]);
    expect(f.resumeMic).toHaveBeenCalledOnce();
  });

  it("revokes playback without restarting the microphone on teardown", async () => {
    const f = fixture();
    await f.playback.play(async () => new Blob(["speech"]));

    f.playback.cancel(false);

    expect(f.revoked).toEqual(["blob:voice-1"]);
    expect(f.activeChanges).toEqual([true, false]);
    expect(f.resumeMic).not.toHaveBeenCalled();
  });

  it("discards a response superseded by newer speech", async () => {
    const f = fixture();
    let resolveFirst!: (blob: Blob) => void;
    const firstBlob = new Promise<Blob>((resolve) => {
      resolveFirst = resolve;
    });

    const first = f.playback.play(() => firstBlob);
    const second = f.playback.play(async () => new Blob(["second"]));
    resolveFirst(new Blob(["stale"]));

    await expect(second).resolves.toBe(true);
    await expect(first).resolves.toBe(false);
    expect(f.audios).toHaveLength(1);
    expect(f.audios[0].src).toBe("blob:voice-1");
    expect(f.activeChanges).toEqual([true]);
  });

  it("is active through a pending paid load and settles once when cancelled", async () => {
    const f = fixture();
    let resolveBlob!: (blob: Blob) => void;
    const blob = new Promise<Blob>((resolve) => { resolveBlob = resolve; });

    const pending = f.playback.play(() => blob);
    expect(f.activeChanges).toEqual([true]);
    expect(f.audios).toEqual([]);

    f.playback.cancel(false);
    f.playback.cancel(false);
    resolveBlob(new Blob(["late"]));
    await expect(pending).resolves.toBe(false);
    expect(f.activeChanges).toEqual([true, false]);
    expect(f.audios).toEqual([]);
    expect(f.resumeMic).not.toHaveBeenCalled();
  });
});
