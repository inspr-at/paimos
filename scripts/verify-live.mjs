// SPDX-License-Identifier: AGPL-3.0-only
// Read-only by default. Coordinator-supplied aeon.rollout.v1 evidence extends
// the existing timing record; it is never obtained through fleet SSH here.
import { createHash } from "node:crypto";
import { readFileSync, writeFileSync } from "node:fs";
import { spawnSync } from "node:child_process";
import { pathToFileURL } from "node:url";
import { validCalendarVersion, schemeError } from "./verify-release.mjs";
import { gh, installationToken, fetchPublishedChecksums, bumpHomebrewTap, mergeHomebrewTap, TapPendingError } from "./homebrew-tap-pr.mjs";
import { parseChecksums } from "./homebrew-formula.mjs";

const REPO = "inspr-at/paimos";
const BASE = "https://aeon.barta.cm";
const ASSETS = ["aeon-cli", "paimos-agentd"].flatMap(name =>
  ["darwin-arm64", "darwin-amd64", "linux-arm64", "linux-amd64"].map(platform => `${name}-${platform}`));
const sha256 = bytes => createHash("sha256").update(bytes).digest("hex");
const sha = value => /^[a-f0-9]{40}$/.test(value ?? "");
const digest = value => /^sha256:[a-f0-9]{64}$/.test(value ?? "");
const hash = value => /^[a-f0-9]{64}$/.test(value ?? "");
const sleep = ms => new Promise(resolve => setTimeout(resolve, ms));
class VerificationError extends Error {}
const requireOK = (ok, reason) => { if (!ok) throw new VerificationError(reason); };
const MANUAL = [
  "Native signed/notarized agent qualification, existing-item ACL and attach preview, and Touch ID: operator only; record evidence for the exact bytes.",
  "AEON-493: qualify, publish agent assets and finish tap distribution before the server switch; deployment and rollback remain coordinator controls.",
  "D7: lead announces through aeon tell and handles ticket closure; no PPM key or automatic inbox writes in Actions.",
];

function time(value) {
  requireOK(typeof value === "string" && /(?:Z|[+-]\d\d:\d\d)$/.test(value), "timestamp requires a timezone");
  const result = Date.parse(value);
  requireOK(Number.isFinite(result), "invalid timestamp");
  return result;
}

export function validateRollout(r, now = Date.now()) {
  requireOK(r?.schema === "aeon.rollout.v1" && r.direction === "forward" && r.outcome === "success", "successful forward rollout required");
  requireOK(validCalendarVersion(r.version) && !schemeError(r.version_scheme, r.version), "rollout version/scheme mismatch");
  requireOK(digest(r.image_digest) && r.running_digest === r.image_digest && sha(r.source_commit), "rollout image identity mismatch");
  requireOK(r.pin?.repository === "markus-barta/nixcfg" && Number.isSafeInteger(r.pin.number) && r.pin.number > 0 && sha(r.pin.merge_commit_sha), "pin identity required");
  const merged = time(r.pin.merged_at), live = time(r.live_at);
  const o = r.observation;
  const start = time(o?.started_at), end = time(o?.ended_at);
  requireOK(merged <= live && live <= start && end - start >= 60_000 && end <= now && now - end <= 15 * 60_000, "fresh post-switch observation of at least 60 seconds required");
  requireOK(o.restart_count === 0 && o.requests_5xx === 0 && Number.isSafeInteger(o.requests_total) && o.requests_total > 0, "rollout restarts/5xx measurement failed");
  requireOK(/^\/assets\/[A-Za-z0-9_-]+\.js$/.test(r.web?.entrypoint ?? "") && hash(r.web?.sha256), "pinned web entry bundle required");
  const q = r.qualification;
  requireOK(q?.version === r.version && hash(q.sha256sums) && ASSETS.includes(q.asset) && q.asset.startsWith("paimos-agentd-darwin-") && hash(q.sha256), "native qualification artifact binding required");
  requireOK(q.operator === "markus-barta" && ["spctl", "foreground_socket", "acl_fixture", "attach_preview", "touch_id"].every(k => q[k] === true) &&
    /^[A-Za-z0-9._/#-]{1,200}$/.test(q.evidence ?? ""), "manual native qualification evidence required");
  return r;
}

async function api(token, path, method = "GET", body) {
  const res = await gh(token, method, path, body);
  requireOK(res.status >= 200 && res.status < 300, "GitHub request failed");
  return res.data;
}

export function validateRelease(release, r) {
  requireOK(Number.isSafeInteger(release?.id) && release.id > 0 && release.tag_name === `v${r.version}` &&
    typeof release.draft === "boolean" && release.prerelease === false, "release identity mismatch");
  const names = release.assets?.map(a => a.name).sort();
  requireOK(JSON.stringify(names) === JSON.stringify([...ASSETS, "SHA256SUMS"].sort()), "complete immutable nine-asset release required");
  requireOK(release.assets.every(a => Number.isSafeInteger(a.id) && a.id > 0 && a.state === "uploaded" && a.size > 0 && a.size <= 200_000_000 && digest(a.digest)), "release asset metadata incomplete");
  const digests = String(release.body ?? "").split(/\r?\n/).filter(line => line.startsWith("Digest: "));
  requireOK(digests.length === 1 && digests[0] === `Digest: ${r.image_digest}`, "release image digest mismatch");
  requireOK(release.assets.find(a => a.name === "SHA256SUMS").digest === `sha256:${r.qualification.sha256sums}`, "qualification checksum file mismatch");
  liveBody(release.body, r); // Refuse a conflicting previous verification before any writes.
  return release;
}

async function assetBytes(token, asset) {
  const url = `https://api.github.com/repos/${REPO}/releases/assets/${asset.id}`;
  let response = await fetch(url, {
    redirect: "manual", signal: AbortSignal.timeout(120_000),
    headers: { Authorization: `Bearer ${token}`, Accept: "application/octet-stream", "X-GitHub-Api-Version": "2022-11-28" },
  });
  if (response.status === 302) {
    const location = new URL(response.headers.get("location"));
    requireOK(location.protocol === "https:" && !location.username && !location.password &&
      location.hostname === "release-assets.githubusercontent.com", "asset redirect refused");
    // Signed storage URL receives no GitHub authorization header and is never logged.
    response = await fetch(location, { redirect: "error", signal: AbortSignal.timeout(120_000) });
  }
  requireOK(response.ok, "release asset download failed");
  const chunks = [];
  let size = 0;
  for await (const chunk of response.body) {
    size += chunk.length;
    requireOK(size <= asset.size, "release asset size mismatch");
    chunks.push(chunk);
  }
  requireOK(size === asset.size, "release asset size mismatch");
  const bytes = Buffer.concat(chunks);
  requireOK(`sha256:${sha256(bytes)}` === asset.digest, "release asset digest mismatch");
  return bytes;
}

export async function verifyArtifacts(token, release, r, download = assetBytes) {
  validateRelease(release, r);
  const sumsBytes = await download(token, release.assets.find(a => a.name === "SHA256SUMS"));
  requireOK(sha256(sumsBytes) === r.qualification.sha256sums, "SHA256SUMS digest mismatch");
  const sums = parseChecksums(sumsBytes.toString("utf8"));
  requireOK(JSON.stringify([...sums.keys()].sort()) === JSON.stringify([...ASSETS].sort()), "SHA256SUMS asset set mismatch");
  requireOK(sums.get(r.qualification.asset) === r.qualification.sha256, "native qualification checksum mismatch");
  for (const name of ASSETS) {
    const asset = release.assets.find(a => a.name === name);
    requireOK(asset.digest === `sha256:${sums.get(name)}`, "asset checksum metadata mismatch");
    requireOK(sha256(await download(token, asset)) === sums.get(name), "asset checksum mismatch");
  }
  return sumsBytes.toString("utf8");
}

export async function verifySource(token, r) {
  const pr = await api("", `/repos/${r.pin.repository}/pulls/${r.pin.number}`);
  requireOK(pr.merged === true && pr.base?.repo?.full_name === r.pin.repository && pr.base?.ref === "main" &&
    pr.merge_commit_sha === r.pin.merge_commit_sha && pr.merged_at === r.pin.merged_at, "pin merge mismatch");
  const files = await api("", `/repos/${r.pin.repository}/pulls/${r.pin.number}/files?per_page=100`);
  requireOK(Array.isArray(files) && files.length === 1 && files[0].status === "modified" &&
    files[0].filename === "hosts/csb1/docker/compose-spec.nix" && typeof files[0].patch === "string", "pin diff evidence required");
  const added = files[0].patch.split("\n").filter(l => l.startsWith("+") && !l.startsWith("+++"));
  const removed = files[0].patch.split("\n").filter(l => l.startsWith("-") && !l.startsWith("---"));
  const imageLine = /^[+-]\s*image = "ghcr\.io\/inspr-at\/aeon:([0-9]{12}\.0\.0)@(sha256:[a-f0-9]{64})";\s*(?:#.*)?$/;
  const next = imageLine.exec(added[0] ?? ""), previous = imageLine.exec(removed[0] ?? "");
  requireOK(added.length === 1 && removed.length === 1 && next && previous && validCalendarVersion(previous[1]) &&
    next[1] === r.version && next[2] === r.image_digest, "exact pin image diff required");
  const ref = await api(token, `/repos/${REPO}/git/ref/tags/v${r.version}`);
  requireOK(ref.object?.type === "tag" && sha(ref.object.sha), "annotated release tag required");
  const tag = await api(token, `/repos/${REPO}/git/tags/${ref.object.sha}`);
  requireOK(tag.tag === `v${r.version}` && tag.object?.type === "commit" && tag.object.sha === r.source_commit, "tag source mismatch");
  const main = await api(token, `/repos/${REPO}/compare/${r.source_commit}...main`);
  requireOK(["ahead", "identical"].includes(main.status), "release source is not on main");
  const file = await api(token, `/repos/${REPO}/contents/version.json?ref=${r.source_commit}`);
  let version;
  try { version = JSON.parse(Buffer.from(file.content, "base64").toString("utf8")); } catch { throw new Error("tagged version unreadable"); }
  requireOK(version.version === r.version && version.version_scheme === r.version_scheme, "tagged version mismatch");
}

export async function probeLive(r, { fetcher = fetch, now = Date.now, wait = sleep, windowMS = 60_000 } = {}) {
  requireOK(windowMS >= 60_000, "probe window must be at least 60 seconds");
  const started = now(), deadline = started + 600_000;
  let polls = 0, probe5xx = 0, first5xx = null, last5xx = null;
  const evidence = () => ({ polls, started_at: new Date(started).toISOString(), ended_at: new Date(now()).toISOString(),
    probe_5xx: probe5xx, first_5xx_at: first5xx === null ? null : new Date(first5xx).toISOString(),
    last_5xx_at: last5xx === null ? null : new Date(last5xx).toISOString(), scope: "unauthenticated GET probes; host counters supplied separately" });
  async function get(path, type) {
    let response;
    try { response = await fetcher(`${BASE}${path}`, { redirect: "error", signal: AbortSignal.timeout(10_000), headers: { "Cache-Control": "no-cache" } }); }
    catch { throw new Error("live probe request failed"); }
    if (response.status >= 500) { probe5xx++; first5xx ??= now(); last5xx = now(); }
    requireOK(response.status === 200, "live probe status failed");
    requireOK((response.headers.get("content-type") ?? "").includes(type), "live probe content type mismatch");
    return response;
  }
  try {
    while (true) {
      polls++;
      const version = await (await get("/api/version", "application/json")).json();
      if (version.version === r.version) {
        requireOK(version.scheme === r.version_scheme, "live version scheme mismatch");
        break;
      }
      requireOK(validCalendarVersion(version.version) && version.version < r.version, "unexpected live version");
      requireOK(now() < deadline, "version poll timed out after ten minutes");
      await wait(Math.min(5000, deadline - now()));
    }
    const windowStart = now();
    do {
      const version = await (await get("/api/version", "application/json")).json();
      requireOK(version.version === r.version && version.scheme === r.version_scheme, "live version changed during observation");
      const health = await (await get("/api/health", "application/json")).json();
      requireOK(health.status === "ok" && health.db === "ok", "live health/database failed");
      const ready = await (await get("/api/ready", "application/json")).json();
      requireOK(ready.status === "ready", "live readiness failed");
      const html = await (await get("/", "text/html")).text();
      requireOK(html.includes('<div id="app"></div>') && html.includes(`src="${r.web.entrypoint}"`), "SPA bundle marker mismatch");
      const bundle = await get(r.web.entrypoint, "javascript");
      requireOK(sha256(Buffer.from(await bundle.arrayBuffer())) === r.web.sha256, "live bundle checksum mismatch");
      if (now() - windowStart >= windowMS) break;
      await wait(Math.min(5000, windowMS - (now() - windowStart)));
    } while (true);
    return evidence();
  } catch (error) {
    const failure = error instanceof VerificationError ? error : new VerificationError("live probes failed");
    failure.probes = evidence();
    throw failure;
  }
}

export function liveBody(body, r) {
  const line = `Live verification: ${r.version}; ${r.image_digest}; source ${r.source_commit}; pin ${r.pin.merge_commit_sha}; health/ready/SPA/bundle/read-only smoke passed; restarts=0; host 5xx=0 (${r.observation.started_at}..${r.observation.ended_at}).`;
  const previous = String(body ?? "").split(/\r?\n/).filter(l => l.startsWith("Live verification:"));
  requireOK(previous.length <= 1 && (!previous.length || previous[0] === line), "conflicting live-verification line");
  return previous.length ? body : `${String(body ?? "").trimEnd()}\n\n${line}\n`;
}

export async function finalizeRelease(token, r, release, apply = false, request = api) {
  validateRelease(release, r);
  const fresh = await request(token, `/repos/${REPO}/releases/${release.id}`);
  validateRelease(fresh, r);
  requireOK(fresh.body === release.body && JSON.stringify(fresh.assets) === JSON.stringify(release.assets) && fresh.draft === release.draft, "release changed during verification");
  const body = liveBody(fresh.body, r);
  if (!apply) return { state: "verified", would_publish: fresh.draft };
  if (fresh.body === body && !fresh.draft) return { state: "current" };
  const updated = await request(token, `/repos/${REPO}/releases/${fresh.id}`, "PATCH", { body, draft: false });
  validateRelease(updated, r);
  requireOK(updated.draft === false && updated.body === body, "release finalization mismatch");
  return { state: fresh.draft ? "published" : "finalized" };
}

function command(program, args, env) {
  const result = spawnSync(program, args, { env, encoding: "utf8", stdio: ["ignore", "pipe", "pipe"], timeout: 180_000 });
  requireOK(result.status === 0, "external verification command failed");
}

export async function run(r, { apply = false, env = process.env, dependencies = {} } = {}) {
  const d = { installationToken, verifySource, command, api, verifyArtifacts, probeLive, finalizeRelease,
    fetchPublishedChecksums, bumpHomebrewTap, mergeHomebrewTap, now: Date.now, wait: sleep, ...dependencies };
  validateRollout(r);
  requireOK(env.GITHUB_REPOSITORY === REPO && env.GITHUB_REF === "refs/heads/main" && env.GITHUB_EVENT_NAME === "workflow_dispatch", "trusted main workflow dispatch required");
  requireOK(/^\d+$/.test(env.RELEASE_APP_ID ?? "") && env.RELEASE_APP_KEY && /^\d+$/.test(env.HOMEBREW_TAP_APP_ID ?? "") && env.HOMEBREW_TAP_APP_KEY, "release and tap App identities required");
  const token = await d.installationToken(env.RELEASE_APP_ID, env.RELEASE_APP_KEY, REPO);
  await d.verifySource(token, r);
  d.command("gh", ["attestation", "verify", `oci://ghcr.io/inspr-at/aeon@${r.image_digest}`, "--repo", REPO,
    "--signer-workflow", `${REPO}/.github/workflows/release.yml`, "--source-ref", `refs/tags/v${r.version}`,
    "--source-digest", r.source_commit, "--deny-self-hosted-runners"], { PATH: env.PATH, HOME: env.HOME, GH_TOKEN: token });
  const release = await d.api(token, `/repos/${REPO}/releases/tags/v${r.version}`);
  const checksums = await d.verifyArtifacts(token, release, r);
  const probes = await d.probeLive(r);
  // Browser process has no App/PPM credentials or operator browser profile.
  d.command(process.execPath, ["scripts/verify-live-smoke.mjs"], {
    PATH: env.PATH, HOME: env.HOME, LIVE_VERSION: r.version, LIVE_SCHEME: r.version_scheme,
    LIVE_BUNDLE: r.web.entrypoint, LIVE_BUNDLE_SHA256: r.web.sha256,
  });
  validateRollout(r); // Evidence must still be fresh after slow checks.
  await d.verifySource(token, r);
  const final = await d.finalizeRelease(token, r, release, apply);
  let tap = { state: "pending-publication" };
  if (!release.draft || apply) {
    const publicChecksums = await d.fetchPublishedChecksums(r.version);
    requireOK(sha256(publicChecksums) === r.qualification.sha256sums && publicChecksums === checksums, "public SHA256SUMS mismatch");
    if (apply) await d.bumpHomebrewTap({ ...env, VERSION: r.version });
    const tapToken = await d.installationToken(env.HOMEBREW_TAP_APP_ID, env.HOMEBREW_TAP_APP_KEY, "inspr-at/homebrew-tap", true);
    const deadline = d.now() + 300_000;
    while (true) {
      try { tap = await d.mergeHomebrewTap(tapToken, r.version, publicChecksums, apply); break; }
      catch (error) {
        if (!(error instanceof TapPendingError)) throw error;
        requireOK(d.now() < deadline, "tap checks timed out after five minutes");
        await d.wait(Math.min(5000, deadline - d.now()));
      }
    }
  }
  return { schema: "aeon.live-verification.v1", version: r.version, image_digest: r.image_digest,
    source_commit: r.source_commit, checks: "passed", release: final, tap, probes, manual: MANUAL };
}

if (process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href) {
  try {
    const args = process.argv.slice(2);
    requireOK(args.length === 1 || (args.length === 2 && args[1] === "--apply"), "usage: verify-live.mjs ROLLOUT.json [--apply]");
    const report = await run(JSON.parse(readFileSync(args[0], "utf8")), { apply: args[1] === "--apply" });
    if (process.env.LIVE_REPORT) writeFileSync(process.env.LIVE_REPORT, JSON.stringify(report, null, 2) + "\n");
    console.log(JSON.stringify(report));
  } catch (error) {
    // Never echo remote errors, record payloads, credentials or signed asset URLs.
    const report = { schema: "aeon.live-verification.v1", checks: "failed", reason: error instanceof VerificationError ? error.message : "external verification failed",
      probes: error instanceof VerificationError ? error.probes : undefined,
      action: "Stop publication/tap mutation; alert the lead via the failed run. Preserve or roll back the server under coordinator control.", manual: MANUAL };
    if (process.env.LIVE_REPORT) writeFileSync(process.env.LIVE_REPORT, JSON.stringify(report, null, 2) + "\n");
    console.error(JSON.stringify(report));
    process.exitCode = 1;
  }
}
