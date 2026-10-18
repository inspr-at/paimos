// SPDX-License-Identifier: AGPL-3.0-only
// Both adopted Aeon calendar schemes use this canonical coordinate grammar.
export function validCalendarVersion(v) {
  const m = /^([1-9][0-9])(0[1-9]|1[0-2])(0[1-9]|[12][0-9]|3[01])([01][0-9]|2[0-3])([0-5][0-9])([0-5][0-9])\.0\.0$/.exec(v || "");
  if (!m) return false;
  const d = new Date(Date.UTC(2000 + +m[1], +m[2] - 1, +m[3]));
  return d.getUTCMonth() + 1 === +m[2] && d.getUTCDate() === +m[3];
}
