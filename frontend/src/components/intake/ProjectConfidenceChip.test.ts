import { afterEach, describe, expect, it } from "vitest";
import { createApp, defineComponent, nextTick, ref } from "vue";
import ProjectConfidenceChip from "@/components/intake/ProjectConfidenceChip.vue";

// The chip lives inside the app header, which is deliberately hard chrome:
// a fixed 52px row with `overflow: hidden`. PAI-735 teleported the chip
// there and the candidate popover — absolutely positioned below the
// trigger — was clipped out of existence, so the chip read as unclickable.
// These tests mount it inside an equivalently clipped host.
function mountInClippedHeader(opts: {
  matches?: Array<{ project_id: number; key: string; name: string; score: number; confidence: string; rationale?: string }>;
  pinnedProjectId?: number | null;
  detectedProjectId?: number | null;
} = {}) {
  const host = document.createElement("div");
  host.className = "fake-app-header";
  host.style.height = "52px";
  host.style.overflow = "hidden";
  document.body.appendChild(host);

  const pinned: number[] = [];
  const unpinned: number[] = [];

  const Parent = defineComponent({
    components: { ProjectConfidenceChip },
    setup() {
      const session = ref({
        id: 1,
        pinned_project_id: opts.pinnedProjectId ?? null,
        detected_project_id: opts.detectedProjectId ?? 7,
      });
      const match = ref({
        threshold: 90,
        first_detection: false,
        matches: opts.matches ?? [
          { project_id: 7, key: "NIX", name: "NixCfg Infrastructure", score: 58, confidence: "med", rationale: "config and deploy wording" },
          { project_id: 9, key: "JANUS", name: "JANUS", score: 41, confidence: "low" },
        ],
      });
      return {
        session,
        match,
        onPin: (id: number) => pinned.push(id),
        onUnpin: () => unpinned.push(1),
      };
    },
    template: `
      <ProjectConfidenceChip :session="session" :match="match" @pin="onPin" @unpin="onUnpin" />
    `,
  });

  const app = createApp(Parent);
  app.mount(host);
  return {
    host,
    pinned,
    unpinned,
    chip: () => host.querySelector<HTMLButtonElement>("button.vi-chip")!,
    popover: () => document.querySelector<HTMLElement>(".vi-chip-pop"),
    unmount() {
      app.unmount();
      host.remove();
    },
  };
}

let mounted: ReturnType<typeof mountInClippedHeader> | null = null;
afterEach(() => {
  mounted?.unmount();
  mounted = null;
  document.querySelectorAll(".vi-chip-pop").forEach((n) => n.remove());
});

describe("ProjectConfidenceChip", () => {
  it("opens the candidate popover on click", async () => {
    mounted = mountInClippedHeader();
    expect(mounted.popover()).toBeNull();

    mounted.chip().click();
    await nextTick();

    expect(mounted.popover()).not.toBeNull();
  });

  it("escapes the clipped header instead of rendering inside it", async () => {
    // The regression. Rendering inside the 52px overflow:hidden header
    // means the panel is painted and immediately clipped away, which is
    // indistinguishable from a dead button.
    mounted = mountInClippedHeader();
    mounted.chip().click();
    await nextTick();

    const pop = mounted.popover();
    expect(pop).not.toBeNull();
    expect(mounted.host.contains(pop!)).toBe(false);
    expect(pop!.parentElement).toBe(document.body);

    // Escaping the clip is only half of it: once the panel is out of the
    // header it has to be placed from the trigger's rect rather than by
    // static CSS. jsdom does not apply scoped <style>, so `position:
    // fixed` itself is not observable here — the inline offsets are.
    expect(pop!.style.top).toMatch(/px$/);
    expect(pop!.style.left).toMatch(/px$/);
    expect(pop!.style.minWidth).toMatch(/px$/);
  });

  it("closes on a click outside and on Escape", async () => {
    mounted = mountInClippedHeader();

    mounted.chip().click();
    await nextTick();
    expect(mounted.popover()).not.toBeNull();

    document.body.dispatchEvent(new MouseEvent("mousedown", { bubbles: true }));
    await nextTick();
    expect(mounted.popover()).toBeNull();

    mounted.chip().click();
    await nextTick();
    document.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape", bubbles: true }));
    await nextTick();
    expect(mounted.popover()).toBeNull();
  });

  it("keeps the popover open when clicking inside it", async () => {
    mounted = mountInClippedHeader();
    mounted.chip().click();
    await nextTick();

    const pop = mounted.popover()!;
    pop.dispatchEvent(new MouseEvent("mousedown", { bubbles: true }));
    await nextTick();

    expect(mounted.popover()).not.toBeNull();
  });

  it("pins the chosen candidate and closes", async () => {
    mounted = mountInClippedHeader();
    mounted.chip().click();
    await nextTick();

    const candidates = document.querySelectorAll<HTMLButtonElement>(".vi-chip-cand");
    expect(candidates.length).toBe(2);
    candidates[1].click();
    await nextTick();

    expect(mounted.pinned).toEqual([9]);
    expect(mounted.popover()).toBeNull();
  });

  it("still opens with an explanation when detection returned nothing", async () => {
    // Previously `v-if="popoverOpen && match?.matches?.length"` meant a
    // click did nothing at all with no candidates — the same dead-button
    // symptom by a different route.
    mounted = mountInClippedHeader({ matches: [], detectedProjectId: null });
    mounted.chip().click();
    await nextTick();

    const pop = mounted.popover();
    expect(pop).not.toBeNull();
    expect(pop!.querySelector(".vi-chip-pop-empty")).not.toBeNull();
  });

  it("cleans up the teleported popover when the chip unmounts", async () => {
    mounted = mountInClippedHeader();
    mounted.chip().click();
    await nextTick();
    expect(document.querySelector(".vi-chip-pop")).not.toBeNull();

    mounted.unmount();
    mounted = null;
    await nextTick();

    expect(document.querySelector(".vi-chip-pop")).toBeNull();
  });
});
