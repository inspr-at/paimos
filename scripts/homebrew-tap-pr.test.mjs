// SPDX-License-Identifier: AGPL-3.0-only
import assert from "node:assert/strict";
import { generateKeyPairSync, createVerify } from "node:crypto";
import { spawnSync } from "node:child_process";
import test from "node:test";
import { appJWT } from "./homebrew-tap-pr.mjs";

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
