// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>
// Licensed under AGPL-3.0-only; see LICENSE.

import { beforeEach, describe, expect, it, vi } from "vitest";

// PAI-736: the sidebar brand was the literal string "Project Management",
// so every white-labelled instance advertised the wrong name on every
// page (pma should read PMA, ppm PPM). brandName is the single source
// for that label. These drive the REAL fetch+merge path — the same one
// that produced the bug — rather than poking the readonly ref, and cover
// the partially-specified branding.json documents where a stale default
// must not win.

/** Serve one branding document from /api/branding, then re-init. */
async function withBrandingDoc(doc: Record<string, unknown> | null) {
  vi.resetModules();
  vi.stubGlobal(
    "fetch",
    vi.fn(async () =>
      doc === null
        ? ({ ok: false } as Response)
        : ({ ok: true, json: async () => doc } as unknown as Response),
    ),
  );
  const { useBranding } = await import("./useBranding");
  const b = useBranding();
  await b.init();
  return b;
}

describe("brandName (PAI-736)", () => {
  beforeEach(() => {
    localStorage.clear();
    vi.unstubAllGlobals();
  });

  it("uses branding.name — the field operators set (pma reads PMA)", async () => {
    const { brandName } = await withBrandingDoc({ name: "PMA", product: "PMA" });
    expect(brandName.value).toBe("PMA");
  });

  it("reads PPM on the personal instance", async () => {
    const { brandName } = await withBrandingDoc({ name: "PPM", product: "PPM" });
    expect(brandName.value).toBe("PPM");
  });

  it("falls back to product when the document carries only BRAND_PRODUCT_NAME", async () => {
    const { brandName } = await withBrandingDoc({ product: "PPM" });
    expect(brandName.value).toBe("PPM");
  });

  it("ignores a whitespace-only name rather than rendering a blank brand", async () => {
    const { brandName } = await withBrandingDoc({ name: "   ", product: "PMA" });
    expect(brandName.value).toBe("PMA");
  });

  it("trims stray whitespace from the configured name", async () => {
    const { brandName } = await withBrandingDoc({ name: "  PMA  " });
    expect(brandName.value).toBe("PMA");
  });

  it("never renders empty when both fields are blank", async () => {
    const { brandName } = await withBrandingDoc({ name: "", product: "" });
    expect(brandName.value).toBe("PAIMOS");
  });

  it("falls back to the default when the branding endpoint is unavailable", async () => {
    const { brandName } = await withBrandingDoc(null);
    expect(brandName.value).toBe("PAIMOS");
  });

  it("never renders the old hardcoded label for a branded instance", async () => {
    const { brandName } = await withBrandingDoc({ name: "PMA", product: "PMA" });
    expect(brandName.value).not.toBe("Project Management");
  });
});

// `name` and `product` are one identity the server derives from a single
// BRAND_PRODUCT_NAME. A hand-written branding.json usually declares only
// one; the shallow merge used to fill the other from defaults, so other
// consumers (the login page reads branding.product) rendered "PAIMOS" on
// a branded instance.
describe("branding identity reconciliation (PAI-736)", () => {
  beforeEach(() => {
    localStorage.clear();
    vi.unstubAllGlobals();
  });

  it("a document declaring only name also answers for product", async () => {
    const { branding } = await withBrandingDoc({ name: "PMA" });
    expect(branding.value.product).toBe("PMA");
    expect(branding.value.name).toBe("PMA");
  });

  it("a document declaring only product also answers for name", async () => {
    const { branding } = await withBrandingDoc({ product: "PPM" });
    expect(branding.value.name).toBe("PPM");
    expect(branding.value.product).toBe("PPM");
  });

  it("leaves an explicitly divergent pair alone", async () => {
    const { branding } = await withBrandingDoc({ name: "Acme PM", product: "Acme" });
    expect(branding.value.name).toBe("Acme PM");
    expect(branding.value.product).toBe("Acme");
  });
});

describe("neutral default palette (PAI-737)", () => {
  beforeEach(() => {
    localStorage.clear();
    vi.unstubAllGlobals();
  });

  it("uses neutral gray defaults when the branding endpoint is unavailable", async () => {
    const { branding } = await withBrandingDoc(null);
    expect(branding.value.colors).toMatchObject({
      primary: "#52525b",
      primaryDark: "#3f3f46",
      primaryLight: "#a1a1aa",
      primaryPale: "#f4f4f5",
      sidebarBg: "#18181b",
      sidebarText: "#e4e4e7",
      loginBg: "#18181b",
      loginPattern: "#27272a",
    });
  });

  it("keeps instance-specific color overrides", async () => {
    const { branding } = await withBrandingDoc({
      colors: { primary: "#ff0066", sidebarBg: "#123456" },
    });
    expect(branding.value.colors.primary).toBe("#ff0066");
    expect(branding.value.colors.sidebarBg).toBe("#123456");
    expect(branding.value.colors.primaryDark).toBe("#3f3f46");
  });
});
