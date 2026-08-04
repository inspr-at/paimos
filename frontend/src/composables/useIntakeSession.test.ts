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

import { useIntakeSession } from "./useIntakeSession";

function lastSource(): FakeEventSource {
  const src = FakeEventSource.instances[FakeEventSource.instances.length - 1];
  if (!src) throw new Error("no EventSource opened");
  return src;
}

describe("useIntakeSession", () => {
  beforeEach(() => {
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
});
