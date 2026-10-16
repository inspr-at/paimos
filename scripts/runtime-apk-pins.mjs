// SPDX-License-Identifier: AGPL-3.0-only
// One source for the runtime APK pins: the quoted chromium= and tini= arguments
// in scripts/Dockerfile.runtime, newest chromium first. tini stays one exact pin.
// A chromium bump edits those arguments (and any NOTICE source URL that names a dropped version).

export const apkVersion = /^[0-9]+(?:\.[0-9]+){2,3}-r[0-9]+$/;
const TINI_PIN = '0.19.0-r3';

function versionKey(version) {
  const [main, rev] = version.split('-r');
  return [...main.split('.').map(Number), Number(rev)];
}

// Positive when a is newer than b. Equal keys compare equal.
export function compareApkVersions(a, b) {
  const ka = versionKey(a), kb = versionKey(b);
  const width = Math.max(ka.length, kb.length);
  for (let i = 0; i < width; i++) {
    const delta = (ka[i] || 0) - (kb[i] || 0);
    if (delta) return delta;
  }
  return 0;
}

export function parseRuntimeApkPins(dockerfile) {
  if (typeof dockerfile !== 'string') throw new Error('runtime APK pins block missing');
  const pins = [...dockerfile.matchAll(/"([a-z0-9]+)=([^"]*)"/g)];
  if (pins.length === 0) throw new Error('runtime APK pins block missing');
  const chromium = [];
  const tinis = [];
  for (const [, name, version] of pins) {
    if (!apkVersion.test(version)) throw new Error(`runtime APK pin is not an exact version: ${name} ${version}`);
    if (name === 'chromium') chromium.push(version);
    else if (name === 'tini') tinis.push(version);
    else throw new Error(`runtime APK pin unknown package: ${name}`);
  }
  if (chromium.length === 0) throw new Error('runtime APK chromium allow-list is empty');
  if (new Set(chromium).size !== chromium.length) throw new Error('runtime APK chromium allow-list has a duplicate');
  for (let i = 1; i < chromium.length; i++) {
    if (compareApkVersions(chromium[i - 1], chromium[i]) <= 0) throw new Error('runtime APK chromium allow-list is not newest first');
  }
  if (tinis.length === 0 || tinis.some(version => version !== TINI_PIN)) throw new Error('runtime APK tini pin changed');
  return { chromium, tini: TINI_PIN };
}
