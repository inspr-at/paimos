// SPDX-License-Identifier: AGPL-3.0-only
import assert from "node:assert/strict";
import test from "node:test";
import { createHash } from "node:crypto";
import { readFileSync } from "node:fs";
import { spawnSync } from "node:child_process";
import { validateRollout, validateRelease, verifyArtifacts, verifySource, probeLive, liveBody, finalizeRelease, run } from "./verify-live.mjs";
import { allowLiveRead } from "./verify-live-smoke.mjs";
import { mergeHomebrewTap, TapPendingError } from "./homebrew-tap-pr.mjs";
import { renderFormula } from "./homebrew-formula.mjs";

const hash = bytes => createHash("sha256").update(bytes).digest("hex");
const version = "261001130110.0.0";
function fixture(now = Date.now()) {
  const binaries = new Map(["aeon-cli", "paimos-agentd"].flatMap(name =>
    ["darwin-arm64", "darwin-amd64", "linux-arm64", "linux-amd64"].map(p => [`${name}-${p}`, Buffer.from(`${name}-${p} fixture bytes`)])));
  const sums = [...binaries].map(([name, bytes]) => `${hash(bytes)}  ${name}\n`).join("");
  const bytes = new Map([...binaries, ["SHA256SUMS", Buffer.from(sums)]]);
  const iso = offset => new Date(now + offset).toISOString();
  const r = {
    schema: "aeon.rollout.v1", direction: "forward", outcome: "success", version, version_scheme: "inspr-calver-3",
    image_digest: `sha256:${"a".repeat(64)}`, running_digest: `sha256:${"a".repeat(64)}`, source_commit: "b".repeat(40),
    pin: { repository: "markus-barta/nixcfg", number: 890, merge_commit_sha: "c".repeat(40), merged_at: iso(-180_000) },
    live_at: iso(-130_000), observation: { started_at: iso(-120_000), ended_at: iso(-10_000), restart_count: 0, requests_5xx: 0, requests_total: 100 },
    web: { entrypoint: "/assets/index-fixture.js", sha256: hash("bundle fixture") },
    qualification: { version, asset: "paimos-agentd-darwin-arm64", sha256: hash(binaries.get("paimos-agentd-darwin-arm64")), sha256sums: hash(sums),
      operator: "markus-barta", spctl: true, foreground_socket: true, acl_fixture: true, attach_preview: true, touch_id: true, evidence: "AEON-487/comment/native" },
  };
  const release = { id: 123, tag_name: `v${version}`, draft: true, prerelease: false,
    body: `Container: ghcr.io/inspr-at/aeon:${version}\nDigest: ${r.image_digest}\n`,
    assets: [...bytes].map(([name, b], i) => ({ id: i + 1, name, size: b.length, state: "uploaded", digest: `sha256:${hash(b)}` })) };
  return { r, release, bytes, sums };
}

test("fresh extended rollout record passes without inventing version or native evidence", () => {
  const { r } = fixture();
  assert.equal(validateRollout(r), r);
});

for (const [name, mutate] of [
  ["failed rollout", r => r.outcome = "failed"], ["rollback", r => r.direction = "rollback"],
  ["invalid calendar date", r => r.version = "260229130110.0.0"], ["wrong scheme", r => r.version_scheme = "legacy"],
  ["digest mismatch", r => r.running_digest = `sha256:${"d".repeat(64)}`], ["missing digest", r => delete r.image_digest],
  ["wrong pin owner", r => r.pin.repository = "augmentoring-team/agm-nixcfg"], ["missing pin merge", r => delete r.pin.merge_commit_sha],
  ["restart", r => r.observation.restart_count = 1], ["missing restart count", r => delete r.observation.restart_count],
  ["5xx", r => r.observation.requests_5xx = 1], ["unmeasured traffic", r => r.observation.requests_total = 0],
  ["short observation", r => r.observation.started_at = r.observation.ended_at],
  ["future observation", r => r.observation.ended_at = new Date(Date.now() + 10000).toISOString()],
  ["stale observation", r => r.observation.ended_at = new Date(Date.now() - 3600000).toISOString()],
  ["unqualified Touch ID", r => r.qualification.touch_id = false], ["different native bytes", r => r.qualification.version = "260929203122.0.0"],
  ["unsafe evidence text", r => r.qualification.evidence = "arbitrary signed URL?credential=private"],
  ["missing bundle hash", r => delete r.web.sha256], ["external bundle", r => r.web.entrypoint = "https://example.com/app.js"],
]) test(`${name} refuses before any network or mutation`, async () => {
  const { r } = fixture(); mutate(r);
  let calls = 0;
  await assert.rejects(run(r, { dependencies: { installationToken: async () => { calls++; } } }));
  assert.equal(calls, 0);
});

test("all nine immutable assets and exact binary checksums are required", async () => {
  const { r, release, bytes, sums } = fixture();
  assert.equal(await verifyArtifacts("fixture", release, r, async (_, a) => bytes.get(a.name)), sums);
  await assert.rejects(verifyArtifacts("fixture", release, r, async (_, a) => a.name === r.qualification.asset ? Buffer.from("tampered") : bytes.get(a.name)), /asset checksum mismatch/);
  const missing = structuredClone(release); missing.assets.pop();
  assert.throws(() => validateRelease(missing, r), /nine-asset/);
  const wrong = structuredClone(release); wrong.body = "Digest: sha256:" + "f".repeat(64);
  assert.throws(() => validateRelease(wrong, r), /image digest mismatch/);
  const partial = structuredClone(release); delete partial.assets[0].digest;
  assert.throws(() => validateRelease(partial, r), /metadata incomplete/);
  const duplicate = structuredClone(release); duplicate.assets[1] = duplicate.assets[0];
  assert.throws(() => validateRelease(duplicate, r), /nine-asset/);
});

test("live version polls old coordinate then observes health, ready, SPA and bundle for a full minute", async () => {
  const { r } = fixture(); let clock = 0, versionCalls = 0;
  const paths = [];
  const fetcher = async (raw, options) => {
    const path = new URL(raw).pathname; paths.push(path);
    assert.equal(options.redirect, "error"); assert.equal(options.method ?? "GET", "GET");
    if (path === "/api/version") return Response.json({ version: ++versionCalls === 1 ? "260929203122.0.0" : version, scheme: r.version_scheme });
    if (path === "/api/health") return Response.json({ status: "ok", db: "ok" });
    if (path === "/api/ready") return Response.json({ status: "ready" });
    if (path === "/") return new Response(`<div id="app"></div><script src="${r.web.entrypoint}"></script>`, { headers: { "Content-Type": "text/html" } });
    return new Response("bundle fixture", { headers: { "Content-Type": "text/javascript" } });
  };
  const report = await probeLive(r, { fetcher, now: () => clock, wait: async ms => { clock += ms; } });
  assert.equal(report.polls, 2); assert.equal(report.probe_5xx, 0); assert.equal(clock, 65000);
  assert.ok(paths.includes("/api/ready")); assert.ok(paths.includes(r.web.entrypoint));
});

for (const [name, path, response] of [
  ["health DB down", "/api/health", () => Response.json({ status: "ok", db: "down" })],
  ["unready", "/api/ready", () => Response.json({ status: "unavailable" })],
  ["5xx", "/api/health", () => new Response("private response text", { status: 503 })],
  ["stale SPA", "/", () => new Response('<div id="app"></div>', { headers: { "Content-Type": "text/html" } })],
  ["bundle checksum", "/assets/index-fixture.js", () => new Response("wrong", { headers: { "Content-Type": "text/javascript" } })],
  ["wrong scheme", "/api/version", () => Response.json({ version, scheme: "legacy" })],
  ["unexpected newer version", "/api/version", () => Response.json({ version: "261001230110.0.0", scheme: "inspr-calver-3" })],
]) test(`${name} fails the live gate`, async () => {
  const { r } = fixture();
  const fetcher = async url => {
    const p = new URL(url).pathname;
    if (p === path) return response();
    if (p === "/api/version") return Response.json({ version, scheme: r.version_scheme });
    if (p === "/api/health") return Response.json({ status: "ok", db: "ok" });
    if (p === "/api/ready") return Response.json({ status: "ready" });
    if (p === "/") return new Response(`<div id="app"></div><script src="${r.web.entrypoint}"></script>`, { headers: { "Content-Type": "text/html" } });
    return new Response("bundle fixture", { headers: { "Content-Type": "text/javascript" } });
  };
  await assert.rejects(probeLive(r, { fetcher, now: () => 0, wait: async () => {} }));
});

test("old version polling stops at ten minutes", async () => {
  const { r } = fixture(); let clock = 0;
  await assert.rejects(probeLive(r, { now: () => clock, wait: async ms => { clock += ms; },
    fetcher: async () => Response.json({ version: "260929203122.0.0" }) }), /ten minutes/);
  assert.equal(clock, 600000);
});

test("failed 5xx probe retains measured window without response payload", async () => {
  const { r } = fixture();
  const start = Date.parse("2026-10-01T13:00:00Z");
  await assert.rejects(probeLive(r, { now: () => start, fetcher: async () => new Response("private response body", { status: 502 }) }), error => {
    assert.equal(error.probes.probe_5xx, 1);
    assert.equal(error.probes.first_5xx_at, "2026-10-01T13:00:00.000Z");
    assert.equal(JSON.stringify(error).includes("private response body"), false);
    return true;
  });
});

test("publication is read-only by default, idempotent after finalization, and rejects concurrent changes", async () => {
  const { r, release } = fixture(); let current = structuredClone(release); const writes = [];
  const request = async (_, path, method = "GET", body) => {
    if (method === "PATCH") { writes.push({ path, body }); current = { ...current, ...body }; }
    return structuredClone(current);
  };
  assert.equal((await finalizeRelease("fixture", r, release, false, request)).state, "verified");
  assert.equal(writes.length, 0);
  assert.equal((await finalizeRelease("fixture", r, release, true, request)).state, "published");
  assert.equal((await finalizeRelease("fixture", r, current, true, request)).state, "current");
  assert.equal(writes.length, 1);
  assert.equal(current.body.split("Live verification:").length, 2);
  assert.throws(() => liveBody(current.body.replace("restarts=0", "restarts=1"), r), /conflicting/);
  current.body += "\nconcurrent coordinator edit";
  await assert.rejects(finalizeRelease("fixture", r, release, true, request), /changed during verification/);
  assert.equal(writes.length, 1);
});

const workflowEnv = { GITHUB_REPOSITORY: "inspr-at/paimos", GITHUB_REF: "refs/heads/main", GITHUB_EVENT_NAME: "workflow_dispatch",
  RELEASE_APP_ID: "123", RELEASE_APP_KEY: "unused fixture", HOMEBREW_TAP_APP_ID: "456", HOMEBREW_TAP_APP_KEY: "unused fixture" };
for (const failure of ["verifySource", "attestation", "verifyArtifacts", "probeLive", "smoke"]) {
  test(`failed ${failure} never publishes or touches the tap`, async () => {
    const { r, release, sums } = fixture(); const writes = [];
    const dependencies = {
      installationToken: async () => "fixture", verifySource: async () => { if (failure === "verifySource") throw new Error("fixture"); },
      command: program => { if ((failure === "attestation" && program === "gh") || (failure === "smoke" && program !== "gh")) throw new Error("fixture"); },
      api: async () => release, verifyArtifacts: async () => { if (failure === "verifyArtifacts") throw new Error("fixture"); return sums; },
      probeLive: async () => { if (failure === "probeLive") throw new Error("fixture"); return {}; },
      finalizeRelease: async () => { writes.push("publish"); }, bumpHomebrewTap: async () => { writes.push("tap"); },
    };
    await assert.rejects(run(r, { env: workflowEnv, apply: true, dependencies })); assert.deepEqual(writes, []);
  });
}

test("successful run gates writes and keeps browser credentials isolated; default is read-only", async () => {
  const { r, release, sums } = fixture(); const actions = [];
  const dependencies = {
    installationToken: async () => "fixture", verifySource: async () => actions.push("source"), api: async () => release,
    verifyArtifacts: async () => { actions.push("artifacts"); return sums; }, probeLive: async () => { actions.push("live"); return {}; },
    command: (program, args, env) => { actions.push(program === "gh" ? "attestation" : "smoke");
      if (program !== "gh") { assert.equal(env.RELEASE_APP_KEY, undefined); assert.equal(env.GH_TOKEN, undefined); assert.equal(env.HOMEBREW_TAP_APP_KEY, undefined); } },
    finalizeRelease: async (_, __, ___, apply) => { actions.push(`release:${apply}`); return {}; },
    fetchPublishedChecksums: async () => sums, bumpHomebrewTap: async () => actions.push("tap-pr"),
    mergeHomebrewTap: async (_, __, ___, apply) => { actions.push(`tap:${apply}`); return {}; },
  };
  await run(r, { env: workflowEnv, dependencies });
  assert.deepEqual(actions, ["source", "attestation", "artifacts", "live", "smoke", "source", "release:false"]);
  actions.length = 0;
  await run(r, { env: workflowEnv, apply: true, dependencies });
  assert.deepEqual(actions, ["source", "attestation", "artifacts", "live", "smoke", "source", "release:true", "tap-pr", "tap:true"]);
  await assert.rejects(run(r, { env: { ...workflowEnv, GITHUB_REF: "refs/heads/work/aeon-414-live-verify" }, dependencies }), /trusted main/);
});

test("tap polling retries pending checks, but a failed checksum stops immediately", async () => {
  const { r, release, sums } = fixture(); let clock = 0, calls = 0;
  const dependencies = { installationToken: async () => "fixture", verifySource: async () => {}, command: () => {},
    api: async () => release, verifyArtifacts: async () => sums, probeLive: async () => ({}), finalizeRelease: async () => ({}),
    fetchPublishedChecksums: async () => sums, bumpHomebrewTap: async () => {}, now: () => clock, wait: async ms => { clock += ms; },
    mergeHomebrewTap: async () => { if (++calls < 3) throw new TapPendingError("pending fixture"); return { state: "merged" }; } };
  assert.equal((await run(r, { env: workflowEnv, apply: true, dependencies })).tap.state, "merged");
  assert.equal(clock, 10000); assert.equal(calls, 3);
  dependencies.mergeHomebrewTap = async () => { calls++; throw new Error("checksum mismatch"); };
  await assert.rejects(run(r, { env: workflowEnv, apply: true, dependencies }), /checksum mismatch/);
  assert.equal(calls, 4);
});

test("browser permits only fixed-origin safe reads, and refuses login even via GET", () => {
  assert.equal(allowLiveRead("GET", "https://aeon.barta.cm/signin"), true);
  assert.equal(allowLiveRead("GET", "https://aeon.barta.cm/assets/index-abc.js"), true);
  for (const [method, url] of [["POST", "https://aeon.barta.cm/api/nodes"], ["GET", "https://aeon.barta.cm/api/auth/login"],
    ["GET", "https://other.example/assets/main.js"], ["GET", "https://aeon.barta.cm/api/events"], ["GET", "https://aeon.barta.cm/api/auth/logout"]]) {
    assert.equal(allowLiveRead(method, url), false);
  }
});

test("invalid CLI input does not echo record payload or secrets", () => {
  const result = spawnSync(process.execPath, ["scripts/verify-live.mjs", "missing-record-fixture.json"], { encoding: "utf8" });
  assert.equal(result.status, 1); assert.match(result.stderr, /external verification failed/); assert.doesNotMatch(result.stderr, /missing-record-fixture/);
});

test("workflow is dispatch-only, main-only, hosted, protected and read-only by default", () => {
  const yaml = readFileSync(".github/workflows/verify-live.yml", "utf8");
  assert.match(yaml, /workflow_dispatch:/); assert.match(yaml, /default: false/); assert.match(yaml, /github.ref == 'refs\/heads\/main'/);
  assert.match(yaml, /environment: live-verification/); assert.match(yaml, /runs-on: ubuntu-latest/);
  assert.match(yaml, /persist-credentials: false/); assert.match(yaml, /contents: read/); assert.match(yaml, /if: always\(\)/);
  assert.doesNotMatch(yaml, /pull_request_target|repository_dispatch|self-hosted|--admin|PPMAPIKEY|PAIMOS_API_KEY|ssh /);
});

test("source gate binds actual pin merge, annotated tag and main ancestry", async t => {
  const { r } = fixture(); let mode = "valid";
  t.mock.method(globalThis, "fetch", async url => {
    const path = new URL(url).pathname;
    if (path.endsWith(`/pulls/${r.pin.number}`)) return Response.json({ merged: mode !== "unmerged", merge_commit_sha: mode === "wrong pin" ? "0".repeat(40) : r.pin.merge_commit_sha, merged_at: r.pin.merged_at, base: { ref: "main", repo: { full_name: r.pin.repository } } });
    if (path.endsWith("/files")) return Response.json([{ filename: "hosts/csb1/docker/compose-spec.nix", status: "modified", patch: `@@ -1 +1 @@\n-image = "ghcr.io/inspr-at/aeon:260929203122.0.0@sha256:${"f".repeat(64)}";\n+image = "ghcr.io/inspr-at/aeon:${version}@${r.image_digest}";` }]);
    if (path.includes("/git/ref/")) return Response.json({ object: { type: mode === "lightweight" ? "commit" : "tag", sha: "d".repeat(40) } });
    if (path.includes("/git/tags/")) return Response.json({ tag: `v${version}`, object: { type: "commit", sha: r.source_commit } });
    if (path.includes("/compare/")) return Response.json({ status: mode === "off main" ? "diverged" : "ahead" });
    return Response.json({ content: Buffer.from(JSON.stringify({ version: mode === "wrong version" ? "260929203122.0.0" : version, version_scheme: r.version_scheme })).toString("base64") });
  });
  await verifySource("fixture", r);
  for (const failure of ["lightweight", "unmerged", "wrong pin", "off main", "wrong version"]) {
    mode = failure; await assert.rejects(verifySource("fixture", r));
  }
});

test("tap checksum gate merges only the verified head and is idempotent on exact main formula", async t => {
  const { sums } = fixture(); const formula = renderFormula(version, sums); const sha = "e".repeat(40);
  let merged = false, tampered = false, pending = false, extraFile = false, changedHead = false, failedCheck = false, truncated = false, reads = 0;
  const mutations = [];
  const pr = { number: 7, state: "open", draft: false, head: { sha, ref: `aeon-agentd-v${version}`, repo: { full_name: "inspr-at/homebrew-tap" } },
    base: { sha: "f".repeat(40), ref: "main", repo: { full_name: "inspr-at/homebrew-tap" } } };
  t.mock.method(globalThis, "fetch", async (raw, opts) => {
    const url = new URL(raw), path = url.pathname;
    if (opts.method !== "GET") { mutations.push(path); assert.equal(JSON.parse(opts.body).sha, sha); merged = true; return Response.json({ merged: true }); }
    if (path === "/repos/inspr-at/homebrew-tap") return Response.json({ default_branch: "main" });
    if (path.includes("/contents/")) return Response.json({ sha: "a".repeat(40), content: Buffer.from(url.searchParams.get("ref") === "main" && !merged ? '  version "260929203122.0.0"' : tampered ? "wrong checksum" : formula).toString("base64") });
    if (path.endsWith("/pulls")) return Response.json([pr]);
    if (path.endsWith("/files")) return Response.json([{ filename: "Formula/aeon-agentd.rb", status: "modified" }, ...(extraFile ? [{ filename: "extra.rb", status: "added" }] : [])]);
    if (path.endsWith("/check-runs")) return Response.json({ total_count: truncated ? 100 : 1, check_runs: [{ head_sha: sha, status: pending ? "in_progress" : "completed", conclusion: pending ? null : failedCheck ? "failure" : "success" }] });
    if (path.endsWith("/status")) return Response.json({ total_count: 0, statuses: [] });
    reads++;
    return Response.json(changedHead && reads % 2 === 0 ? { ...pr, head: { ...pr.head, sha: "0".repeat(40) } } : pr);
  });
  assert.equal((await mergeHomebrewTap("fixture", version, sums)).state, "verified"); assert.equal(mutations.length, 0);
  pending = true; await assert.rejects(mergeHomebrewTap("fixture", version, sums, true), /checks/); assert.equal(mutations.length, 0);
  pending = false; tampered = true; await assert.rejects(mergeHomebrewTap("fixture", version, sums, true), /checksum/); assert.equal(mutations.length, 0);
  tampered = false; extraFile = true; await assert.rejects(mergeHomebrewTap("fixture", version, sums, true), /only the formula/);
  extraFile = false; failedCheck = true; await assert.rejects(mergeHomebrewTap("fixture", version, sums, true), /checks/);
  failedCheck = false; truncated = true; await assert.rejects(mergeHomebrewTap("fixture", version, sums, true), /checks/);
  truncated = false; changedHead = true; reads = 0; await assert.rejects(mergeHomebrewTap("fixture", version, sums, true), /changed/);
  assert.equal(mutations.length, 0);
  changedHead = false; assert.equal((await mergeHomebrewTap("fixture", version, sums, true)).state, "merged");
  assert.equal((await mergeHomebrewTap("fixture", version, sums, true)).state, "current"); assert.equal(mutations.length, 1);
});
