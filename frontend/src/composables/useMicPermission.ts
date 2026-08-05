/*
 * PAIMOS — Your Professional & Personal AI Project OS
 * Copyright (C) 2026 Markus Barta <markus@barta.com>
 *
 * This program is free software: you can redistribute it and/or modify
 * it under the terms of the GNU Affero General Public License as
 * published by the Free Software Foundation, version 3.
 *
 * This program is distributed in the hope that it will be useful,
 * but WITHOUT ANY WARRANTY; without even the implied warranty of
 * MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
 * GNU Affero General Public License for more details.
 *
 * You should have received a copy of the GNU Affero General Public
 * License along with this program. If not, see <https://www.gnu.org/licenses/>.
 */

// PAI-715: continuous microphone-permission tracking for the Voice Intake
// workbench. The Permissions API gives the live state AND an onchange
// event, so the status chip updates the moment the user flips the
// browser's site setting — no polling. Where the API is unsupported (or
// rejects the "microphone" name, as some Safari versions do), the state
// is "unknown" and the first getUserMedia resolves it in practice.
//
// Browser rules worth knowing (encoded in the chip's affordances):
//   prompt  → getUserMedia re-invokes the permission dialog. We request
//             and immediately stop the tracks — this is only about the
//             permission, never about recording.
//   denied  → the browser will NOT re-prompt; the user must flip the
//             site setting. The chip explains that and offers a
//             re-check for browsers without onchange support.

import { ref } from "vue";

export type MicPermission = "granted" | "denied" | "prompt" | "unknown";

const permission = ref<MicPermission>("unknown");
let status: PermissionStatus | null = null;
let initialized = false;

async function refresh(): Promise<void> {
  if (!navigator.permissions?.query) {
    permission.value = "unknown";
    return;
  }
  try {
    const s = await navigator.permissions.query({ name: "microphone" as PermissionName });
    status = s;
    permission.value = s.state as MicPermission;
    s.onchange = () => {
      permission.value = s.state as MicPermission;
    };
  } catch {
    permission.value = "unknown";
  }
}

/** Start tracking (idempotent). */
async function init(): Promise<void> {
  if (initialized) return;
  initialized = true;
  await refresh();
}

/**
 * (Re-)invoke the browser's permission dialog. Tracks are stopped
 * immediately — this never records. Returns the resulting state.
 */
async function requestAccess(): Promise<MicPermission> {
  try {
    const stream = await navigator.mediaDevices.getUserMedia({ audio: true });
    stream.getTracks().forEach((t) => t.stop());
    if (!status) permission.value = "granted"; // no Permissions API — infer
  } catch {
    if (!status) permission.value = "denied";
  }
  await refresh();
  return permission.value;
}

/** Manual re-check for browsers whose PermissionStatus lacks onchange. */
async function recheck(): Promise<MicPermission> {
  await refresh();
  return permission.value;
}

export function useMicPermission() {
  return { permission, init, requestAccess, recheck };
}
