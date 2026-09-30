// SPDX-License-Identifier: AGPL-3.0-only
import assert from "node:assert/strict";
import { generateKeyPairSync, createVerify } from "node:crypto";
import { spawnSync } from "node:child_process";
import test from "node:test";
import { appJWT, bumpHomebrewTap, fetchPublishedChecksums } from "./homebrew-tap-pr.mjs";

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

const version = "260929203122.0.0";
const published = {
  tag_name: `v${version}`,
  draft: false,
  prerelease: false,
  published_at: "2026-09-29T21:00:00Z",
};
const releaseURL = `https://api.github.com/repos/inspr-at/paimos/releases/tags/v${version}`;

test("published exact-tag checksums are read without draft-capable credentials", async (t) => {
  const calls = [];
  t.mock.method(globalThis, "fetch", async (url, options) => {
    calls.push(url);
    assert.equal(new Headers(options.headers).has("authorization"), false);
    assert.equal(options.method ?? "GET", "GET");
    if (url === releaseURL) return Response.json(published);
    assert.equal(url, `https://github.com/inspr-at/paimos/releases/download/v${version}/SHA256SUMS`);
    return new Response("checksums fixture\n");
  });
  assert.equal(await fetchPublishedChecksums(version), "checksums fixture\n");
  assert.deepEqual(calls, [releaseURL, `https://github.com/inspr-at/paimos/releases/download/v${version}/SHA256SUMS`]);
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
