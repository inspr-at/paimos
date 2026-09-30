// SPDX-License-Identifier: AGPL-3.0-only
// English wording for the shared INSPR version renderer (its defaults are German).
// The renderer builds the accessible name itself: canonical version, UTC date-time
// and the copy action (INSPR-CalVer3, AEON-309).
export const VERSION_COPY_TEXT = {
  copy: 'Copy version',
  copied: 'Copied',
  failed: 'Copy unavailable. The version is selected: press Command+C or Ctrl+C.',
}

// Calendar coordinates render the same under inspr-calver-3 and its predecessor
// inspr-calendar-v2 (one grammar), so a display that only knows the coordinate
// uses the current scheme.
export const CALENDAR_DISPLAY_SCHEME = 'inspr-calver-3'

// Puts `text` on the clipboard; the Clipboard API first, then a hidden field
// copied with execCommand. Reports whether it worked.
export async function copyToClipboard(text: string, view: Window = window): Promise<boolean> {
  try { await view.navigator.clipboard.writeText(text); return true } catch { /* fall through */ }
  const doc = view.document
  const area = doc.createElement('textarea')
  try {
    Object.assign(area, { value: text, readOnly: true })
    area.setAttribute('aria-hidden', 'true')
    Object.assign(area.style, { position: 'fixed', top: '0', left: '0', width: '1px', height: '1px', opacity: '0', pointerEvents: 'none' })
    doc.body.append(area)
    area.select()
    return doc.execCommand('copy') === true
  } catch { return false } finally { area.remove() }
}
