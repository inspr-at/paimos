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
  static instances: FakeMediaRecorder[] = [];
  state: RecordingState = "inactive";
  mimeType = "audio/webm";
  ondataavailable: ((event: BlobEvent) => void) | null = null;
  onerror: ((event: Event) => void) | null = null;
  onstop: (() => void) | null = null;

  constructor() {
    FakeMediaRecorder.instances.push(this);
  }

  start() {
    this.state = "recording";
  }

  stop() {
    this.state = "inactive";
  }
}

class FakeTrack {
  readonly stop = vi.fn();
  private readonly ended = new Set<() => void>();

  addEventListener(type: string, handler: EventListenerOrEventListenerObject) {
    if (type !== "ended") return;
    this.ended.add(typeof handler === "function" ? handler as () => void : () => handler.handleEvent(new Event("ended")));
  }

  removeEventListener(type: string, handler: EventListenerOrEventListenerObject) {
    if (type !== "ended" || typeof handler !== "function") return;
    this.ended.delete(handler as () => void);
  }

  end() {
    for (const handler of [...this.ended]) handler();
  }
}

describe("useMicTranscript start lifecycle", () => {
  const mic = useMicTranscript();

  beforeEach(() => {
    mic.stop();
    FakeMediaRecorder.instances = [];
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

  it("releases the stopped sink and lets only a newly armed recorder deliver", async () => {
    const stopTrack = vi.fn();
    vi.stubGlobal("navigator", {
      mediaDevices: {
        getUserMedia: vi.fn(async () => ({ getTracks: () => [{ stop: stopTrack }] } as unknown as MediaStream)),
      },
    });
    const oldSink = vi.fn(async () => {});
    const newSink = vi.fn(async () => {});

    await expect(mic.start(oldSink)).resolves.toBe(true);
    const oldRecorder = FakeMediaRecorder.instances[FakeMediaRecorder.instances.length - 1]!;
    mic.stop();
    await expect(mic.start(newSink)).resolves.toBe(true);
    const newRecorder = FakeMediaRecorder.instances[FakeMediaRecorder.instances.length - 1]!;

    oldRecorder.ondataavailable?.({ data: new Blob(["x".repeat(2_100)]) } as BlobEvent);
    oldRecorder.onstop?.();
    expect(oldSink).not.toHaveBeenCalled();

    newRecorder.ondataavailable?.({ data: new Blob(["y".repeat(2_100)]) } as BlobEvent);
    newRecorder.onstop?.();
    for (let index = 0; index < 4; index += 1) await Promise.resolve();
    expect(oldSink).not.toHaveBeenCalled();
    expect(newSink).toHaveBeenCalledOnce();
  });

  it("fails closed and releases the sink when the microphone track ends", async () => {
    const track = new FakeTrack();
    vi.stubGlobal("navigator", {
      mediaDevices: {
        getUserMedia: vi.fn(async () => ({ getTracks: () => [track] } as unknown as MediaStream)),
      },
    });
    const sink = vi.fn(async () => {});

    await expect(mic.start(sink)).resolves.toBe(true);
    const recorder = FakeMediaRecorder.instances[FakeMediaRecorder.instances.length - 1]!;
    track.end();

    expect(mic.state.value).toBe("error");
    expect(mic.errorMessage.value).toBe("Microphone stream ended.");
    expect(track.stop).toHaveBeenCalledOnce();
    recorder.ondataavailable?.({ data: new Blob(["x".repeat(2_100)]) } as BlobEvent);
    recorder.onstop?.();
    for (let index = 0; index < 4; index += 1) await Promise.resolve();
    expect(sink).not.toHaveBeenCalled();
  });

  it("fails closed and releases the sink when MediaRecorder reports an error", async () => {
    const track = new FakeTrack();
    vi.stubGlobal("navigator", {
      mediaDevices: {
        getUserMedia: vi.fn(async () => ({ getTracks: () => [track] } as unknown as MediaStream)),
      },
    });
    const sink = vi.fn(async () => {});

    await expect(mic.start(sink)).resolves.toBe(true);
    const recorder = FakeMediaRecorder.instances[FakeMediaRecorder.instances.length - 1]!;
    recorder.onerror?.(new Event("error"));

    expect(mic.state.value).toBe("error");
    expect(mic.errorMessage.value).toBe("Microphone recording failed.");
    expect(track.stop).toHaveBeenCalledOnce();
    recorder.ondataavailable?.({ data: new Blob(["x".repeat(2_100)]) } as BlobEvent);
    recorder.onstop?.();
    for (let index = 0; index < 4; index += 1) await Promise.resolve();
    expect(sink).not.toHaveBeenCalled();
  });

  it("fails closed when AudioContext construction throws after stream acquisition", async () => {
    const track = new FakeTrack();
    vi.stubGlobal("navigator", {
      mediaDevices: {
        getUserMedia: vi.fn(async () => ({ getTracks: () => [track] } as unknown as MediaStream)),
      },
    });
    vi.stubGlobal(
      "AudioContext",
      class {
        constructor() {
          throw new Error("audio context unavailable");
        }
      },
    );
    const sink = vi.fn(async () => {});

    await expect(mic.start(sink)).resolves.toBe(false);

    expect(mic.state.value).toBe("error");
    expect(mic.errorMessage.value).toBe("Microphone initialization failed.");
    expect(track.stop).toHaveBeenCalledOnce();
    expect(sink).not.toHaveBeenCalled();
  });

  it("fails closed when MediaRecorder construction throws", async () => {
    const track = new FakeTrack();
    const close = vi.fn(async () => {});
    vi.stubGlobal("navigator", {
      mediaDevices: {
        getUserMedia: vi.fn(async () => ({ getTracks: () => [track] } as unknown as MediaStream)),
      },
    });
    vi.stubGlobal(
      "AudioContext",
      class extends FakeAudioContext {
        override close() {
          return close();
        }
      },
    );
    vi.stubGlobal(
      "MediaRecorder",
      class {
        constructor() {
          throw new Error("recorder unavailable");
        }
      },
    );
    const sink = vi.fn(async () => {});

    await expect(mic.start(sink)).resolves.toBe(false);

    expect(mic.state.value).toBe("error");
    expect(mic.errorMessage.value).toBe("Microphone recording failed.");
    expect(track.stop).toHaveBeenCalledOnce();
    expect(close).toHaveBeenCalledOnce();
    expect(sink).not.toHaveBeenCalled();
  });

  it("fails closed when MediaRecorder.start throws and leaves stale callbacks inert", async () => {
    const track = new FakeTrack();
    const close = vi.fn(async () => {});
    class StartThrowingMediaRecorder extends FakeMediaRecorder {
      override start() {
        throw new Error("capture could not start");
      }
    }
    vi.stubGlobal("navigator", {
      mediaDevices: {
        getUserMedia: vi.fn(async () => ({ getTracks: () => [track] } as unknown as MediaStream)),
      },
    });
    vi.stubGlobal(
      "AudioContext",
      class extends FakeAudioContext {
        override close() {
          return close();
        }
      },
    );
    vi.stubGlobal("MediaRecorder", StartThrowingMediaRecorder);
    const sink = vi.fn(async () => {});

    await expect(mic.start(sink)).resolves.toBe(false);
    const failedRecorder = FakeMediaRecorder.instances[FakeMediaRecorder.instances.length - 1]!;

    expect(mic.state.value).toBe("error");
    expect(mic.errorMessage.value).toBe("Microphone recording failed.");
    expect(track.stop).toHaveBeenCalledOnce();
    expect(close).toHaveBeenCalledOnce();
    failedRecorder.ondataavailable?.({ data: new Blob(["x".repeat(2_100)]) } as BlobEvent);
    failedRecorder.onstop?.();
    for (let index = 0; index < 4; index += 1) await Promise.resolve();
    expect(sink).not.toHaveBeenCalled();
  });
});
