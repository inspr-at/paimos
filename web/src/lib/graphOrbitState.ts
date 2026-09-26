// SPDX-License-Identifier: AGPL-3.0-only
// Live orbit period for every graph renderer. The account preference publishes
// here; the shared canvas reads the same number onto data-orbit-seconds.
// OrbitControls autoRotateSpeed 1 is one revolution per 60 seconds, so the
// calm default (120 seconds) is speed 0.5.

let seconds = 120

export function graphOrbitSeconds() { return seconds }

export function publishGraphOrbitSeconds(value: number) {
  seconds = Number.isFinite(value) && value > 0 ? value : 0
}

/** OrbitControls autoRotateSpeed. 1 revolves once per 60 seconds; 0 holds still. */
export function autoRotateSpeedFor(periodSeconds: number) {
  return periodSeconds > 0 ? 60 / periodSeconds : 0
}

export function graphOrbitPace() {
  return autoRotateSpeedFor(seconds)
}
