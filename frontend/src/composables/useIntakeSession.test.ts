// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>
// Licensed under AGPL-3.0-only; see LICENSE.

import { beforeEach, describe, expect, it, vi } from "vitest";

// Mock the API layer before importing the composable under test.
vi.mock("@/api/intake", async () => {
  const actual = await vi.importActual<typeof import("@/api/intake")>("@/api/intake");
  return {
    ...actual,
    createIntakeSession: vi.fn(async () => ({
      id: 1,
      user_id: 1,
      status: "active",
      language: "en",
      detected_project_id: null,
      detected_score: 0,
      pinned_project_id: null,
      created_issue_id: null,
      transcript_bytes: 0,
      rev: 0,
      created_at: "",
      updated_at: "",
      completed_at: null,
    })),
    postIntakeTranscript: vi.fn(async () => ({ seq: 1, transcript_bytes: 5 })),
    patchIntakeSession: vi.fn(async () => ({})),
    createIntakeCheckpoint: vi.fn(async () => ({ seq: 2, label: "cp" })),
    getIntakeSession: vi.fn(async () => ({
      session: { id: 1, rev: 3, status: "active", language: "en" },
      state: { at_seq: 3, transcript: "a\nb", artifacts: {} },
      checkpoints: [],
    })),
    getIntakeState: vi.fn(async () => ({
      at_seq: 1,
      transcript: "a",
      artifacts: { spec: { markdown: "# v1", language: "en" } },
    })),
    restoreIntakeSession: vi.fn(async () => ({
      session: { id: 1, rev: 5, status: "active", language: "en" },
      state: { at_seq: 5, transcript: "a", artifacts: { spec: { markdown: "# v1", language: "en" } } },
    })),
  };
});

// Minimal EventSource stub: records instances, lets tests push events.
class FakeEventSource {
  static instances: FakeEventSource[] = [];
  url: string;
  listeners: Record<string, ((ev: MessageEvent<string>) => void)[]> = {};
  onopen: (() => void) | null = null;
  onerror: (() => void) | null = null;
  onmessage: (() => void) | null = null;
  closed = false;

  constructor(url: string) {
    this.url = url;
    FakeEventSource.instances.push(this);
  }
  addEventListener(kind: string, cb: (ev: MessageEvent<string>) => void) {
    (this.listeners[kind] ??= []).push(cb);
  }
  emit(kind: string, data: unknown, lastEventId = "") {
    for (const cb of this.listeners[kind] ?? []) {
      cb({ data: JSON.stringify(data), lastEventId } as MessageEvent<string>);
    }
  }
  close() {
    this.closed = true;
  }
}

vi.stubGlobal("EventSource", FakeEventSource as unknown as typeof EventSource);

import { getIntakeSession, getIntakeState, type IntakeHead, type IntakeState } from "@/api/intake";
import { useIntakeSession } from "./useIntakeSession";

function lastSource(): FakeEventSource {
  const src = FakeEventSource.instances[FakeEventSource.instances.length - 1];
  if (!src) throw new Error("no EventSource opened");
  return src;
}

function intakeHead(rev: number, transcript: string): IntakeHead {
  return {
    session: {
      id: 1,
      user_id: 1,
      status: "active",
      language: "en",
      detected_project_id: null,
      detected_score: 0,
      pinned_project_id: null,
      created_issue_id: null,
      transcript_bytes: transcript.length,
      rev,
      created_at: "",
      updated_at: "",
      completed_at: null,
    },
    state: { at_seq: rev, transcript, artifacts: {} },
    checkpoints: [],
  };
}

function deferred<T>() {
  let resolve!: (value: T) => void;
  let reject!: (reason?: unknown) => void;
  const promise = new Promise<T>((done, fail) => {
    resolve = done;
    reject = fail;
  });
  return { promise, resolve, reject };
}

describe("useIntakeSession", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    FakeEventSource.instances = [];
    useIntakeSession().reset();
  });

  it("starts a session, opens the stream, and applies events in order", async () => {
    const s = useIntakeSession();
    await s.start();
    expect(s.session.value?.id).toBe(1);
    const src = lastSource();
    src.onopen?.();
    expect(s.connection.value).toBe("open");

    src.emit("transcript_chunk", { seq: 1, kind: "transcript_chunk", payload: { text: "hello" } });
    src.emit("transcript_chunk", { seq: 2, kind: "transcript_chunk", payload: { text: "world" } });
    expect(s.transcript.value).toBe("hello\nworld");
    expect(s.session.value?.rev).toBe(2);

    src.emit("spec", { seq: 3, kind: "spec", payload: { markdown: "# Spec", language: "en" } });
    expect(s.spec.value?.markdown).toBe("# Spec");
    expect(s.specSeq.value).toBe(3);
  });

  it("drops stale or duplicate events (rev-idempotent application)", async () => {
    const s = useIntakeSession();
    await s.start();
    const src = lastSource();
    src.onopen?.();

    src.emit("transcript_chunk", { seq: 1, kind: "transcript_chunk", payload: { text: "one" } });
    // Replayed duplicate and an out-of-order stale event must be ignored.
    src.emit("transcript_chunk", { seq: 1, kind: "transcript_chunk", payload: { text: "one" } });
    src.emit("spec", { seq: 1, kind: "spec", payload: { markdown: "stale", language: "en" } });
    expect(s.transcript.value).toBe("one");
    expect(s.spec.value).toBeNull();
    expect(s.revIndex.value.length).toBe(1);
  });

  it("replays persisted events above a reconnect snapshot after hydration", async () => {
    const pending = deferred<IntakeHead>();
    vi.mocked(getIntakeSession).mockImplementationOnce(() => pending.promise);

    const s = useIntakeSession();
    await s.start();
    const src = lastSource();
    src.onopen?.();
    src.onopen?.(); // reconnect starts the deferred hydration

    src.emit("transcript_chunk", {
      seq: 3,
      kind: "transcript_chunk",
      payload: { text: "third" },
    });
    src.emit("transcript_chunk", {
      seq: 2,
      kind: "transcript_chunk",
      payload: { text: "second" },
    });
    src.emit("transcript_chunk", {
      seq: 2,
      kind: "transcript_chunk",
      payload: { text: "duplicate second" },
    });
    pending.resolve(intakeHead(1, "snapshot"));

    await vi.waitFor(() => {
      expect(s.transcript.value).toBe("snapshot\nsecond\nthird");
    });
    expect(s.session.value?.rev).toBe(3);
    expect(s.revIndex.value.map((event) => event.seq)).toEqual([2, 3]);
  });

  it("ignores a stale reconnect hydration after a newer one finishes", async () => {
    const older = deferred<IntakeHead>();
    const newer = deferred<IntakeHead>();
    vi.mocked(getIntakeSession)
      .mockImplementationOnce(() => older.promise)
      .mockImplementationOnce(() => newer.promise);

    const s = useIntakeSession();
    await s.start();
    const src = lastSource();
    src.onopen?.();
    src.onopen?.(); // older hydration
    src.onopen?.(); // newer hydration supersedes it

    newer.resolve(intakeHead(4, "new head"));
    await vi.waitFor(() => expect(s.transcript.value).toBe("new head"));

    older.resolve(intakeHead(1, "stale head"));
    await Promise.resolve();
    expect(s.transcript.value).toBe("new head");
    expect(s.session.value?.rev).toBe(4);
  });

  it("does not install reconnect hydration after reset", async () => {
    const pending = deferred<IntakeHead>();
    vi.mocked(getIntakeSession).mockImplementationOnce(() => pending.promise);

    const s = useIntakeSession();
    await s.start();
    const src = lastSource();
    src.onopen?.();
    src.onopen?.();
    src.emit("transcript_chunk", {
      seq: 2,
      kind: "transcript_chunk",
      payload: { text: "buffered" },
    });

    s.reset();
    pending.resolve(intakeHead(1, "stale head"));
    await Promise.resolve();

    expect(s.session.value).toBeNull();
    expect(s.transcript.value).toBe("");
    expect(s.revIndex.value).toEqual([]);
  });

  it("keeps replayed events when reconnect hydration fails", async () => {
    const pending = deferred<IntakeHead>();
    vi.mocked(getIntakeSession).mockImplementationOnce(() => pending.promise);

    const s = useIntakeSession();
    await s.start();
    const src = lastSource();
    src.onopen?.();
    src.onopen?.();
    src.emit("transcript_chunk", {
      seq: 2,
      kind: "transcript_chunk",
      payload: { text: "replayed despite failed snapshot" },
    });

    pending.reject(new Error("snapshot unavailable"));
    await vi.waitFor(() => {
      expect(s.transcript.value).toBe("replayed despite failed snapshot");
    });
    expect(s.session.value?.rev).toBe(2);
  });

  it("collects checkpoints from events", async () => {
    const s = useIntakeSession();
    await s.start();
    const src = lastSource();
    src.emit("checkpoint", { seq: 4, kind: "checkpoint", label: "v1 frozen" });
    expect(s.checkpoints.value).toEqual([
      { seq: 4, label: "v1 frozen", created_at: undefined },
    ]);
  });

  it("scrub loads as-of state and restore returns to live", async () => {
    const s = useIntakeSession();
    await s.start();
    const src = lastSource();
    src.emit("transcript_chunk", { seq: 1, kind: "transcript_chunk", payload: { text: "a" } });
    src.emit("spec", { seq: 2, kind: "spec", payload: { markdown: "# v2", language: "en" } });

    await s.scrub(1);
    expect(s.isViewingHistory.value).toBe(true);
    expect(s.viewState.value?.spec?.markdown).toBe("# v1");

    await s.restore(1);
    expect(s.isViewingHistory.value).toBe(false);
    expect(s.session.value?.rev).toBe(5);
    expect(s.spec.value?.markdown).toBe("# v1");
  });

  it("debounces history scrubs and fetches only the latest revision", async () => {
    vi.useFakeTimers();
    try {
      const s = useIntakeSession();
      await s.start();
      const src = lastSource();
      src.emit("transcript_chunk", { seq: 1, kind: "transcript_chunk", payload: { text: "a" } });
      src.emit("transcript_chunk", { seq: 2, kind: "transcript_chunk", payload: { text: "b" } });
      src.emit("transcript_chunk", { seq: 3, kind: "transcript_chunk", payload: { text: "c" } });

      s.scheduleScrub(1);
      s.scheduleScrub(2);
      expect(getIntakeState).not.toHaveBeenCalled();

      await vi.advanceTimersByTimeAsync(149);
      expect(getIntakeState).not.toHaveBeenCalled();
      await vi.advanceTimersByTimeAsync(1);
      expect(getIntakeState).toHaveBeenCalledTimes(1);
      expect(getIntakeState).toHaveBeenCalledWith(1, 2);
    } finally {
      vi.useRealTimers();
    }
  });

  it("keeps the newest scrub when responses finish out of order", async () => {
    const older = deferred<IntakeState>();
    const newer = deferred<IntakeState>();
    vi.mocked(getIntakeState)
      .mockImplementationOnce(() => older.promise)
      .mockImplementationOnce(() => newer.promise);

    const s = useIntakeSession();
    await s.start();
    const src = lastSource();
    src.emit("transcript_chunk", { seq: 1, kind: "transcript_chunk", payload: { text: "a" } });
    src.emit("transcript_chunk", { seq: 2, kind: "transcript_chunk", payload: { text: "b" } });
    src.emit("transcript_chunk", { seq: 3, kind: "transcript_chunk", payload: { text: "c" } });

    const olderRequest = s.scrub(1);
    const newerRequest = s.scrub(2);
    newer.resolve({ at_seq: 2, transcript: "newer", artifacts: {} });
    await newerRequest;
    expect(s.viewSeq.value).toBe(2);
    expect(s.viewState.value?.transcript).toBe("newer");

    older.resolve({ at_seq: 1, transcript: "older", artifacts: {} });
    await olderRequest;
    expect(s.viewSeq.value).toBe(2);
    expect(s.viewState.value?.transcript).toBe("newer");
  });

  it("invalidates an in-flight response as soon as a newer scrub is scheduled", async () => {
    vi.useFakeTimers();
    try {
      const older = deferred<IntakeState>();
      vi.mocked(getIntakeState).mockImplementationOnce(() => older.promise);

      const s = useIntakeSession();
      await s.start();
      const src = lastSource();
      src.emit("transcript_chunk", { seq: 1, kind: "transcript_chunk", payload: { text: "a" } });
      src.emit("transcript_chunk", { seq: 2, kind: "transcript_chunk", payload: { text: "b" } });
      src.emit("transcript_chunk", { seq: 3, kind: "transcript_chunk", payload: { text: "c" } });

      const olderRequest = s.scrub(1);
      s.scheduleScrub(2);
      older.resolve({ at_seq: 1, transcript: "older", artifacts: {} });
      await olderRequest;

      expect(s.viewSeq.value).toBeNull();
      expect(s.viewState.value).toBeNull();
      await vi.advanceTimersByTimeAsync(150);
      expect(s.viewSeq.value).toBe(2);
    } finally {
      vi.useRealTimers();
    }
  });

  it("does not reinstall history after returning to live", async () => {
    const pending = deferred<IntakeState>();
    vi.mocked(getIntakeState).mockImplementationOnce(() => pending.promise);

    const s = useIntakeSession();
    await s.start();
    const src = lastSource();
    src.emit("transcript_chunk", { seq: 1, kind: "transcript_chunk", payload: { text: "a" } });
    src.emit("transcript_chunk", { seq: 2, kind: "transcript_chunk", payload: { text: "b" } });

    const request = s.scrub(1);
    await s.scrub(null);
    pending.resolve({ at_seq: 1, transcript: "stale", artifacts: {} });
    await request;

    expect(s.isViewingHistory.value).toBe(false);
    expect(s.viewState.value).toBeNull();
  });

  it("reset closes the stream and clears state", async () => {
    const s = useIntakeSession();
    await s.start();
    const src = lastSource();
    src.emit("transcript_chunk", { seq: 1, kind: "transcript_chunk", payload: { text: "x" } });
    s.reset();
    expect(src.closed).toBe(true);
    expect(s.session.value).toBeNull();
    expect(s.transcript.value).toBe("");
    expect(s.connection.value).toBe("disconnected");
  });

  it("caches artifacts per language and toggles as a view switch (PAI-734)", async () => {
    const s = useIntakeSession();
    await s.start();
    const src = lastSource();
    src.onopen?.();

    src.emit("transcript_chunk", { seq: 1, kind: "transcript_chunk", payload: { text: "idea" } });
    src.emit("spec", { seq: 2, kind: "spec", payload: { markdown: "# EN", language: "en" } });
    expect(s.spec.value?.markdown).toBe("# EN");

    // A DE generation arriving while EN is active must not clobber the view.
    src.emit("spec", { seq: 3, kind: "spec", payload: { markdown: "# DE", language: "de" } });
    expect(s.spec.value?.markdown).toBe("# EN");
    expect(s.specSeq.value).toBe(2);

    // The toggle echo swaps the displayed spec from the cache instantly.
    src.emit("language", { seq: 4, kind: "language", payload: { language: "de", from: "en" } });
    expect(s.session.value?.language).toBe("de");
    expect(s.spec.value?.markdown).toBe("# DE");

    // Toggling back re-displays the cached EN spec unchanged.
    src.emit("language", { seq: 5, kind: "language", payload: { language: "en", from: "de" } });
    expect(s.spec.value?.markdown).toBe("# EN");
  });

  it("falls back to the newest cached language while a first generation is pending", async () => {
    const s = useIntakeSession();
    await s.start();
    const src = lastSource();
    src.onopen?.();

    src.emit("spec", { seq: 1, kind: "spec", payload: { markdown: "# EN", language: "en" } });
    src.emit("language", { seq: 2, kind: "language", payload: { language: "de", from: "en" } });
    // No DE spec yet — the EN one stays visible instead of a blank card.
    expect(s.spec.value?.markdown).toBe("# EN");

    // When the DE generation lands, the active view picks it up.
    src.emit("spec", { seq: 3, kind: "spec", payload: { markdown: "# DE", language: "de" } });
    expect(s.spec.value?.markdown).toBe("# DE");
  });
});
