// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>
// Licensed under AGPL-3.0-only; see LICENSE.

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { useMicTranscript } from "./useMicTranscript";

function deferred<T>() {
  let resolve!: (value: T) => void;
  const promise = new Promise<T>((done) => {
    resolve = done;
  });
  return { promise, resolve };
}

class FakeAudioContext {
  createAnalyser() {
    return {
      fftSize: 0,
      getByteTimeDomainData: vi.fn(),
    };
  }

  createMediaStreamSource() {
    return { connect: vi.fn() };
  }

  close() {
    return Promise.resolve();
  }
}

class FakeMediaRecorder {
  state: RecordingState = "inactive";
  mimeType = "audio/webm";
  ondataavailable: ((event: BlobEvent) => void) | null = null;
  onstop: (() => void) | null = null;

  start() {
    this.state = "recording";
  }

  stop() {
    this.state = "inactive";
  }
}

describe("useMicTranscript start lifecycle", () => {
  const mic = useMicTranscript();

  beforeEach(() => {
    mic.stop();
    vi.stubGlobal("AudioContext", FakeAudioContext);
    vi.stubGlobal("MediaRecorder", FakeMediaRecorder);
  });

  afterEach(() => {
    mic.stop();
    vi.unstubAllGlobals();
  });

  it("treats a pending microphone permission request as active", async () => {
    const request = deferred<MediaStream>();
    vi.stubGlobal("navigator", {
      mediaDevices: { getUserMedia: vi.fn(() => request.promise) },
    });

    const starting = mic.start(async () => {});

    expect(mic.state.value).toBe("starting");
    expect(mic.isActive.value).toBe(true);

    mic.stop();
    const stopTrack = vi.fn();
    request.resolve({ getTracks: () => [{ stop: stopTrack }] } as unknown as MediaStream);
    await starting;
  });

  it("stops a stream that arrives after capture was cancelled", async () => {
    const request = deferred<MediaStream>();
    vi.stubGlobal("navigator", {
      mediaDevices: { getUserMedia: vi.fn(() => request.promise) },
    });
    const stopTrack = vi.fn();

    const starting = mic.start(async () => {});
    mic.stop();
    request.resolve({ getTracks: () => [{ stop: stopTrack }] } as unknown as MediaStream);

    await expect(starting).resolves.toBe(false);
    expect(stopTrack).toHaveBeenCalledOnce();
    expect(mic.state.value).toBe("idle");
  });
});
