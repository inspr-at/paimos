// SPDX-License-Identifier: AGPL-3.0-only
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import test from "node:test";
import { parseChecksums, renderFormula } from "./homebrew-formula.mjs";

const version = "260929113854.0.0";
const arm = "a".repeat(64);
const intel = "b".repeat(64);

function sums(extra = "") {
  return `${arm}  paimos-agentd-darwin-arm64\n${intel}  paimos-agentd-darwin-amd64\n${extra}`;
}

test("maps darwin checksums and the release version into the formula", () => {
  const parsed = parseChecksums(sums(`${"c".repeat(64)}  paimos-agentd-linux-arm64\n${"d".repeat(64)} *aeon-cli-darwin-arm64\n`));
  assert.equal(parsed.get("paimos-agentd-darwin-arm64"), arm);
  assert.equal(parsed.get("paimos-agentd-darwin-amd64"), intel);
  assert.equal(parsed.get("aeon-cli-darwin-arm64"), "d".repeat(64));
  const formula = renderFormula(version, parsed);
  assert.match(formula, new RegExp(`version "${version}"`));
  assert.match(formula, new RegExp(`url "https://github.com/inspr-at/paimos/releases/download/v${version}/paimos-agentd-darwin-arm64"`));
  assert.match(formula, new RegExp(`url "https://github.com/inspr-at/paimos/releases/download/v${version}/paimos-agentd-darwin-amd64"`));
  assert.match(formula, new RegExp(`sha256 "${arm}"`));
  assert.match(formula, new RegExp(`sha256 "${intel}"`));
  assert.match(formula, /bin\.install "paimos-agentd-darwin-#\{arch\}" => "aeon-agentd"/);
  assert.match(formula, /assert_match "260929113854\.0\.0", shell_output\("#\{bin\}\/aeon-agentd --version"\)/);
  assert.equal(formula.includes("paimos-agentd-linux"), false);
  assert.equal(formula.includes("go build"), false);
  assert.equal(formula.includes('system "'), false);
});

test("accepts a single space and rejects malformed, duplicate, and incomplete manifests", () => {
  assert.equal(parseChecksums(`${arm} paimos-agentd-darwin-arm64\n`).get("paimos-agentd-darwin-arm64"), arm);
  assert.throws(() => parseChecksums(`${arm.toUpperCase()}  paimos-agentd-darwin-arm64\n`), /malformed/);
  assert.throws(() => parseChecksums(`${arm}  paimos-agentd-darwin-arm64 extra\n`), /malformed/);
  assert.throws(() => parseChecksums(`${arm}  paimos-agentd-darwin-arm64\n${"e".repeat(64)}  paimos-agentd-darwin-arm64\n`), /duplicate/);
  assert.throws(() => renderFormula("1.2.3", sums()), /invalid calendar version/);
  assert.throws(() => renderFormula(version, `${arm}  paimos-agentd-darwin-arm64\n`), /missing/);
});

test("stable 105 sample is the generator output for its own darwin checksums", () => {
  const sample = readFileSync(new URL("../docs/homebrew/aeon-agentd.rb", import.meta.url), "utf8");
  const armLine = /paimos-agentd-darwin-arm64"\n\s+sha256 "([0-9a-f]{64})"/.exec(sample);
  const intelLine = /paimos-agentd-darwin-amd64"\n\s+sha256 "([0-9a-f]{64})"/.exec(sample);
  assert.ok(armLine && intelLine);
  const rendered = renderFormula(version, `${armLine[1]}  paimos-agentd-darwin-arm64\n${intelLine[1]}  paimos-agentd-darwin-amd64\n`);
  assert.equal(rendered, sample);
});
