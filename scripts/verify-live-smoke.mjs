// SPDX-License-Identifier: AGPL-3.0-only
// Isolated unauthenticated Playwright smoke. Never use an operator profile,
// login, mutation, third-party request, WebSocket, or service worker.
import { createRequire } from "node:module";
import { createHash } from "node:crypto";
import { pathToFileURL } from "node:url";

const BASE = "https://aeon.barta.cm";
export function allowLiveRead(method, rawURL, base = BASE) {
  const url = new URL(rawURL);
  return ["GET", "HEAD"].includes(method) && url.origin === base &&
    (url.pathname === "/" || url.pathname === "/signin" ||
      ["/api/version", "/api/me", "/api/auth/config"].includes(url.pathname) ||
      /^\/(assets|brand)\/[A-Za-z0-9_./-]+$/.test(url.pathname));
}

export async function smoke(env = process.env, base = BASE) {
  const require = createRequire(new URL("../web/package.json", import.meta.url));
  const { chromium } = require("@playwright/test");
  const browser = await chromium.launch({ headless: true });
  const context = await browser.newContext({ serviceWorkers: "block", reducedMotion: "reduce" });
  let blocked = 0, errors = 0, bundleSeen = false;
  try {
    await context.route("**/*", async route => {
      if (!allowLiveRead(route.request().method(), route.request().url(), base)) { blocked++; await route.abort(); }
      else await route.continue();
    });
    await context.routeWebSocket("**/*", socket => { blocked++; socket.close(); });
    const page = await context.newPage();
    page.on("pageerror", () => errors++);
    page.on("requestfailed", () => errors++);
    page.on("response", response => {
      if (response.status() >= 400 && !(response.status() === 401 && new URL(response.url()).pathname === "/api/me")) errors++;
    });
    // Verify the actual entry bundle delivered to the browser, not an API label.
    const bundleResponse = page.waitForResponse(response => response.url() === `${base}${env.LIVE_BUNDLE}`, { timeout: 30_000 }).catch(() => null);
    const response = await page.goto(`${base}/signin`, { waitUntil: "networkidle", timeout: 30_000 });
    if (response?.status() !== 200 || page.url() !== `${base}/signin`) throw new Error("SPA smoke failed");
    await page.locator("#signin-title").waitFor({ state: "visible", timeout: 15_000 });
    await page.locator('a.login-button[href="/api/auth/login"]').waitFor({ state: "visible", timeout: 15_000 });
    if (await page.locator('[role="alert"]').count()) throw new Error("sign-in is unavailable");
    const bundle = await bundleResponse;
    bundleSeen = bundle?.status() === 200 && createHash("sha256").update(await bundle.body()).digest("hex") === env.LIVE_BUNDLE_SHA256;
    // APIRequestContext bypasses browser routing, so use only this fixed GET.
    const versionResponse = await context.request.get(`${base}/api/version`, { maxRedirects: 0, timeout: 10_000 });
    const version = await versionResponse.json();
    if (versionResponse.status() !== 200 || version.version !== env.LIVE_VERSION || version.scheme !== env.LIVE_SCHEME ||
        !bundleSeen || blocked || errors) throw new Error("read-only smoke failed");
  } finally {
    await context.close();
    await browser.close();
  }
}

if (process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href) {
  smoke().catch(() => { console.error("read-only live smoke failed"); process.exitCode = 1; });
}
