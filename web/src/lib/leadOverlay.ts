// SPDX-License-Identifier: AGPL-3.0-only
import { reactive } from 'vue'

// One lead panel and one lead sheet at a time, opened from the band, the line,
// a ticket (also a preview over any page) or the Agents page. The app shell mounts LeadOverlays once.
// Closing hands focus back to whatever opened that overlay.
export interface LeadOverlayState {
  panel: string | null
  start: { projects: string[]; showProject: boolean } | null
  pause: string | null
}
export const leadOverlay = reactive<LeadOverlayState>({ panel: null, start: null, pause: null })
let panelFrom: HTMLElement | null = null, sheetFrom: HTMLElement | null = null
const active = () => document.activeElement instanceof HTMLElement ? document.activeElement : null
export function openLeadPanel(project: string, from?: HTMLElement | null) { panelFrom = from ?? active(); leadOverlay.panel = project }
/** Several projects let the sheet offer a choice; one fixes the project. */
export function openStartLead(projects: string[], from?: HTMLElement | null, showProject = false) {
  if (!projects.length) return
  sheetFrom = from ?? active(); leadOverlay.pause = null; leadOverlay.start = { projects, showProject: showProject || projects.length > 1 }
}
export function openLeadPause(project: string, from?: HTMLElement | null) { sheetFrom = from ?? active(); leadOverlay.start = null; leadOverlay.pause = project }
export function closeLeadSheet(restore = true) {
  leadOverlay.start = null; leadOverlay.pause = null
  if (restore && sheetFrom?.isConnected) sheetFrom.focus({ preventScroll: true })
  sheetFrom = null
}
export function closeLeadPanel(restore = true) {
  leadOverlay.panel = null
  if (restore && panelFrom?.isConnected) panelFrom.focus({ preventScroll: true })
  panelFrom = null
}
export function resetLeadOverlays() { leadOverlay.panel = null; leadOverlay.start = null; leadOverlay.pause = null; panelFrom = null; sheetFrom = null }
