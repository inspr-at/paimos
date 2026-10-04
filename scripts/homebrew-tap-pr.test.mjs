// SPDX-License-Identifier: AGPL-3.0-only
import assert from "node:assert/strict";
import { generateKeyPairSync, createVerify, createHash } from "node:crypto";
import { spawnSync } from "node:child_process";
import test from "node:test";
import { appJWT, bumpHomebrewTap, fetchPublishedChecksums, installationToken, downloadReleaseAsset, RELEASE_ASSETS } from "./homebrew-tap-pr.mjs";

test("app JWT is RS256 and names the app id without embedding the key", () => {
  const { privateKey, publicKey } = generateKeyPairSync("rsa", { modulusLength: 2048 });
  const pem = privateKey.export({ type: "pkcs8", format: "pem" });
  const now = 1_800_000_000;
  const jwt = appJWT("5123698", pem, now);
  assert.equal(jwt.includes("PRIVATE"), false);
  assert.equal(jwt.includes(pem), false);
  const [header, payload, signature] = jwt.split(".");
  assert.deepEqual(JSON.parse(Buffer.from(header, "base64url").toString()), { alg: "RS256", typ: "JWT" });
  assert.deepEqual(JSON.parse(Buffer.from(payload, "base64url").toString()), {
    iat: now - 60,
    exp: now + 540,
    iss: "5123698",
  });
  const verify = createVerify("RSA-SHA256");
  verify.update(`${header}.${payload}`);
  verify.end();
  assert.equal(verify.verify(publicKey, Buffer.from(signature, "base64url")), true);
});

test("missing app secrets skip the tap bump", () => {
  const run = spawnSync(process.execPath, ["scripts/homebrew-tap-pr.mjs"], {
    encoding: "utf8",
    env: { PATH: process.env.PATH, HOMEBREW_TAP_APP_ID: "", HOMEBREW_TAP_APP_KEY: "" },
  });
  assert.equal(run.status, 0);
  assert.equal(run.stdout, "homebrew tap bump skipped: app secrets absent\n");
  assert.equal(run.stderr, "");
});

test("App tokens stay repository-scoped; existing bump permissions stay compatible", async t => {
  const { privateKey } = generateKeyPairSync("rsa", { modulusLength: 2048 });
  const pem = privateKey.export({ type: "pkcs8", format: "pem" });
  const requests = []; let extra = false;
  t.mock.method(globalThis, "fetch", async (url, opts) => {
    if (opts.method === "GET") return Response.json({ id: 123 });
    if (opts.method === "DELETE") return new Response(null, { status: 204 });
    const body = JSON.parse(opts.body); requests.push(body);
    return Response.json({ token: "unused fixture token", permissions: extra ? { ...body.permissions, issues: "write" } : body.permissions }, { status: 201 });
  });
  await installationToken("123", pem);
  assert.deepEqual(requests[0], { repositories: ["homebrew-tap"], permissions: { contents: "write", pull_requests: "write", metadata: "read" } });
  await installationToken("123", pem, "inspr-at/homebrew-tap", "tap-read");
  assert.deepEqual(requests[1].permissions, { contents: "read", pull_requests: "read", checks: "read", statuses: "read", metadata: "read" });
  await installationToken("123", pem, "inspr-at/paimos", "attestation");
  assert.deepEqual(requests[2], { repositories: ["paimos"], permissions: { attestations: "read", metadata: "read" } });
  await installationToken("123", pem, "inspr-at/paimos", "release-write");
  assert.deepEqual(requests[3].permissions, { contents: "write", metadata: "read" });
  extra = true; await assert.rejects(installationToken("123", pem), /unexpected App token permissions/);
  await assert.rejects(installationToken("123", pem, "augmentoring-team/agm-nixcfg"), /unsupported/);
});

const version = "260929203122.0.0";
const hash = bytes => createHash("sha256").update(bytes).digest("hex");
const sums = RELEASE_ASSETS.map(name => `${hash(name)}  ${name}\n`).join("");
const published = {
  tag_name: `v${version}`,
  draft: false,
  prerelease: false,
  published_at: "2026-09-29T21:00:00Z",
  immutable: true,
  assets: [...RELEASE_ASSETS.map((name, i) => ({ id: i + 1, name, digest: `sha256:${hash(name)}`, state: "uploaded", size: name.length })),
    { id: 9, name: "SHA256SUMS", digest: `sha256:${hash(sums)}`, state: "uploaded", size: Buffer.byteLength(sums) }],
};
const releaseURL = `https://api.github.com/repos/inspr-at/paimos/releases/tags/v${version}`;

test("published exact-tag checksums are read without draft-capable credentials", async (t) => {
  const calls = [];
  t.mock.method(globalThis, "fetch", async (url, options) => {
    calls.push(url);
    assert.equal(new Headers(options.headers).has("authorization"), false);
    assert.equal(options.method ?? "GET", "GET");
    if (url === releaseURL) return Response.json(published);
    if (url.includes("/git/ref/")) return Response.json({ object: { type: "tag", sha: "a".repeat(40) } });
    if (url.includes("/git/tags/")) return Response.json({ tag: `v${version}`, object: { type: "commit", sha: "b".repeat(40) } });
    if (url.includes("/compare/")) return Response.json({ status: "ahead" });
    assert.equal(url, "https://api.github.com/repos/inspr-at/paimos/releases/assets/9");
    return new Response(sums);
  });
  assert.equal(await fetchPublishedChecksums(version), sums);
  assert.equal(calls.length, 5);
});

for (const [name, metadata] of [
  ["draft", { ...published, draft: true }],
  ["prerelease", { ...published, prerelease: true }],
  ["different tag", { ...published, tag_name: "v260929200000.0.0" }],
  ["unpublished", { ...published, published_at: null }],
  ["invalid publication time", { ...published, published_at: "invalid" }],
  ["missing draft flag", { ...published, draft: undefined }],
  ["missing prerelease flag", { ...published, prerelease: undefined }],
  ["missing metadata", null],
]) {
  test(`${name} cannot download checksums or authenticate/mutate the tap`, async (t) => {
    const calls = [];
    t.mock.method(globalThis, "fetch", async (url) => {
      calls.push(url);
      assert.equal(url, releaseURL);
      return Response.json(metadata);
    });
    await assert.rejects(bumpHomebrewTap({
      VERSION: version,
      HOMEBREW_TAP_APP_ID: "123",
      // Never parsed: the publication gate must precede token generation.
      HOMEBREW_TAP_APP_KEY: "unused test fixture",
    }), /exact published, non-draft, non-prerelease release/);
    assert.deepEqual(calls, [releaseURL]);
  });
}

for (const status of [404, 403, 429, 500]) {
  test(`release lookup HTTP ${status} fails closed before checksum download`, async (t) => {
    const calls = [];
    t.mock.method(globalThis, "fetch", async (url) => {
      calls.push(url);
      return new Response(null, { status });
    });
    await assert.rejects(fetchPublishedChecksums(version), new RegExp(`HTTP ${status}`));
    assert.deepEqual(calls, [releaseURL]);
  });
}

test("asset redirects allow only GitHub storage and never forward authorization", async t => {
  const body = "fixture bytes", asset = { id: 9, size: body.length, digest: `sha256:${hash(body)}` };
  let location = "https://release-assets.githubusercontent.com/fixture?signature=not-a-credential";
  let followed = 0;
  t.mock.method(globalThis, "fetch", async (raw, options) => {
    if (String(raw).startsWith("https://api.github.com/")) {
      assert.equal(options.redirect, "manual");
      return new Response(null, { status: 302, headers: { location } });
    }
    followed++; assert.equal(new Headers(options.headers).has("authorization"), false); assert.equal(options.redirect, "error");
    return new Response(body);
  });
  assert.equal((await downloadReleaseAsset("unused fixture", asset)).toString(), body);
  for (const unsafe of ["https://attacker.example/asset", "http://release-assets.githubusercontent.com/asset",
    "https://user:fixture@release-assets.githubusercontent.com/asset", "https://release-assets.githubusercontent.com.evil/asset"]) {
    location = unsafe;
    await assert.rejects(downloadReleaseAsset("unused fixture", asset), /redirect refused/);
  }
  assert.equal(followed, 1);
});

test("digest, size and redirects fail before any tap credential or branch write", async t => {
  let mode = "digest", calls = [];
  t.mock.method(globalThis, "fetch", async (url, options) => {
    calls.push({ url: String(url), method: options.method ?? "GET" });
    assert.equal(options.method ?? "GET", "GET");
    if (url === releaseURL) return Response.json(mode === "mutable" ? { ...published, immutable: false } : published);
    if (url.includes("/git/ref/")) return Response.json({ object: { type: mode === "lightweight" ? "commit" : "tag", sha: "a".repeat(40) } });
    if (url.includes("/git/tags/")) return Response.json({ tag: `v${version}`, object: { type: "commit", sha: "b".repeat(40) } });
    if (url.includes("/compare/")) return Response.json({ status: mode === "off main" ? "diverged" : "identical" });
    if (mode === "redirect") return new Response(null, { status: 302, headers: { location: "https://attacker.example/asset" } });
    return new Response(mode === "size" ? "oversized".repeat(1000) : sums.replace(/^[a-f0-9]/, "f"));
  });
  for (const failure of ["digest", "size", "redirect", "mutable", "lightweight", "off main"]) {
    mode = failure; calls = [];
    await assert.rejects(bumpHomebrewTap({ VERSION: version, HOMEBREW_TAP_APP_ID: "123", HOMEBREW_TAP_APP_KEY: "unused fixture" }));
    assert.ok(calls.every(c => !c.url.includes("homebrew-tap") && !c.url.includes("access_tokens")));
  }
});

test("verified checksum mismatch is refused before re-download or App access", async t => {
  t.mock.method(globalThis, "fetch", async () => { assert.fail("mismatched bytes must not reach network"); });
  await assert.rejects(bumpHomebrewTap({ VERSION: version, HOMEBREW_TAP_APP_ID: "123", HOMEBREW_TAP_APP_KEY: "unused fixture" },
    { text: sums + "changed", sha256: hash(sums) }), /verified checksum bytes mismatch/);
});

test("verified bytes are reused once, retry writes only the exact base tip and PR; concurrent advance refuses", async t => {
  const { privateKey } = generateKeyPairSync("rsa", { modulusLength: 2048 });
  const pem = privateKey.export({ type: "pkcs8", format: "pem" });
  const base = "a".repeat(40), commit = "c".repeat(40), tree = "d".repeat(40);
  let mode = "retry", writes = [], revocations = 0;
  t.mock.method(globalThis, "fetch", async (raw, options) => {
    const path = new URL(raw).pathname, method = options.method ?? "GET", body = options.body ? JSON.parse(options.body) : null;
    assert.ok(!path.includes("/paimos/"), "qualified bytes must never be downloaded a second time");
    if (path.endsWith("/installation")) return Response.json({ id: 123 });
    if (path.endsWith("/access_tokens")) return Response.json({ token: "unused fixture", permissions: body.permissions }, { status: 201 });
    if (method === "DELETE") { revocations++; return new Response(null, { status: 204 }); }
    if (method !== "GET") writes.push({ path, method, body });
    if (path === "/repos/inspr-at/homebrew-tap") return Response.json({ default_branch: mode === "default collision" ? `aeon-agentd-v${version}` : "main" });
    if (path.includes("/git/ref/heads/")) return Response.json({ object: { sha: mode === "foreign branch" && !path.endsWith("/main") ? "b".repeat(40) : base } });
    if (path.includes("/contents/")) return Response.json({ sha: "b".repeat(40), content: Buffer.from('  version "260929200000.0.0"').toString("base64") });
    if (path.endsWith(`/git/commits/${base}`)) return Response.json({ tree: { sha: tree } });
    if (path.endsWith("/git/trees")) return Response.json({ sha: tree }, { status: 201 });
    if (path.endsWith("/git/commits")) { assert.deepEqual(body.parents, [base]); return Response.json({ sha: commit }, { status: 201 }); }
    if (path.includes("/git/refs/heads/")) {
      assert.equal(body.force, false); assert.ok(!path.endsWith("/main"));
      return mode === "concurrent advance" ? Response.json({}, { status: 422 }) : Response.json({ object: { sha: commit } });
    }
    if (path.endsWith("/pulls")) return method === "GET" ? Response.json([]) : Response.json({ html_url: "https://github.com/inspr-at/homebrew-tap/pull/7" }, { status: 201 });
    assert.fail(`unexpected fixture request ${method} ${path}`);
  });
  const env = { VERSION: version, HOMEBREW_TAP_APP_ID: "123", HOMEBREW_TAP_APP_KEY: pem };
  const verified = { text: sums, sha256: hash(sums) };
  await bumpHomebrewTap(env, verified);
  assert.equal(writes.at(-1).path, "/repos/inspr-at/homebrew-tap/pulls"); assert.equal(revocations, 1);
  mode = "foreign branch"; writes = [];
  await assert.rejects(bumpHomebrewTap(env, verified), /existing tap branch checksum mismatch/);
  assert.deepEqual(writes, []); assert.equal(revocations, 2);
  mode = "concurrent advance"; writes = [];
  await assert.rejects(bumpHomebrewTap(env, verified), /branch changed during formula update/);
  assert.ok(writes.every(c => !c.path.endsWith("/pulls"))); assert.equal(revocations, 3);
  mode = "default collision"; writes = [];
  await assert.rejects(bumpHomebrewTap(env, verified), /must differ from the default branch/);
  assert.deepEqual(writes, []); assert.equal(revocations, 4);
});
