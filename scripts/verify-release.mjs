// Release checks for PAIMOS AEON (AEON-6).
// 1. The vendored INSPR presentation bundle matches the checked-in pin literals. Expected values live in
//    calendar-version-bundle-pin.json and are never derived from the candidate bytes; bundled JS is never executed here.
// 2. version.json is the one authoritative version source and holds a valid calendar coordinate. New
//    reservations declare inspr-calver-3 (INSPR-CalVer3); inspr-calendar-v2 (INSPR-CalVer2) stays valid only
//    for versions reserved up to LAST_CALVER2, which are history and never rewritten (AEON-309).
import { createHash } from "node:crypto";
import { lstatSync, readFileSync, readdirSync } from "node:fs";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { checkQuoteEvidence } from "./check-quote-evidence.mjs";
import { readPolicy, assertDeployable } from "./release-withdrawals.mjs";
import { validCalendarVersion } from "./release-calendar.mjs";
export { validCalendarVersion } from "./release-calendar.mjs";

const root = resolve(dirname(fileURLToPath(import.meta.url)), "..");
const sha = (bytes) => createHash("sha256").update(bytes).digest("hex");
const FILES = ["display.json", "manifest.json", "package.json", "presentation.js", "schemes.json", "version-interaction.js", "version.js"];

// The current scheme, and the last version Aeon reserved under its predecessor. Both schemes share one
// coordinate (YYMMDDhhmmss.0.0), so versions keep sorting as time across the switch.
export const SCHEME = "inspr-calver-3";
export const CALVER2 = "inspr-calendar-v2";
export const LAST_CALVER2 = "260929113854.0.0";

// schemeError says why a version.json declaration is not allowed, or returns "".
export function schemeError(scheme, version) {
  if (scheme === SCHEME) return version > LAST_CALVER2 ? "" : `${SCHEME} versions must be later than the last ${CALVER2} version ${LAST_CALVER2}`;
  if (scheme === CALVER2) return version <= LAST_CALVER2 ? "" : `new reservations declare ${SCHEME}; ${CALVER2} is history only (last ${LAST_CALVER2})`;
  return `unknown version scheme ${scheme}`;
}

export function verifyRelease() {
  const fail = (why) => { throw new Error(`release check: ${why}`); };
  checkQuoteEvidence();
  const pin = JSON.parse(readFileSync(join(root, "scripts/calendar-version-bundle-pin.json"), "utf8"));
  if (pin.repository !== "inspr-at/inspr" || !/^[a-f0-9]{40}$/.test(pin.revision) || !/^[a-f0-9]{64}$/.test(pin.configSha256) || !/^[a-f0-9]{64}$/.test(pin.manifestSha256)) fail("invalid pin");
  const dir = join(root, "web/src/vendor/calendar-version-display");
  const have = readdirSync(dir).sort();
  if (JSON.stringify(have) !== JSON.stringify(FILES)) fail(`bundle file set differs: ${have.join(", ")}`);
  for (const f of FILES) if (!lstatSync(join(dir, f)).isFile()) fail(`${f} is not a regular file`);
  const manifestBytes = readFileSync(join(dir, "manifest.json"));
  if (sha(manifestBytes) !== pin.manifestSha256) fail("manifest digest differs from the pin");
  const manifest = JSON.parse(manifestBytes);
  if (manifest.repository !== pin.repository || manifest.revision !== pin.revision || manifest.expectedConfigSha256 !== pin.configSha256) fail("manifest does not name the pinned source");
  const listed = manifest.files.map((x) => x.outputPath).sort();
  if (JSON.stringify(listed) !== JSON.stringify(FILES.filter((f) => f !== "manifest.json"))) fail("manifest file list differs");
  for (const x of manifest.files) { const b = readFileSync(join(dir, x.outputPath)); if (b.length !== x.size || sha(b) !== x.sha256) fail(`${x.outputPath} differs from the manifest`); }
  if (sha(readFileSync(join(dir, "display.json"))) !== pin.configSha256) fail("display config differs from the pin");
  const verPath = join(root, "version.json");
  let exists = true; try { lstatSync(verPath); } catch { exists = false; }
  if (!exists) { if (process.argv.includes("--release")) fail("version.json is required for a release"); return { version: "dev", scheme: SCHEME, bundle: `${pin.repository}@${pin.revision.slice(0, 7)}` }; }
  const ver = JSON.parse(readFileSync(verPath, "utf8"));
  if (!validCalendarVersion(ver.version)) fail(`invalid calendar version ${ver.version}`);
  const schemeWhy = schemeError(ver.version_scheme, ver.version);
  if (schemeWhy) fail(schemeWhy);
  if (!Number.isInteger(ver.release_sequence) || ver.release_sequence < 1 || !ver.release_channel) fail("release channel and sequence required");
  const policy = readPolicy(JSON.stringify(ver));
  if (process.argv.includes('--release')) assertDeployable(ver.version, undefined, policy);
  return { version: ver.version, scheme: ver.version_scheme, sequence: ver.release_sequence, bundle: `${pin.repository}@${pin.revision.slice(0, 7)}` };
}

if (process.argv[1] === fileURLToPath(import.meta.url)) {
  try { console.log(JSON.stringify(verifyRelease())); }
  catch (e) { console.error(e.message); process.exitCode = 1; }
}
