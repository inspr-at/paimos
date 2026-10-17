// SPDX-License-Identifier: AGPL-3.0-only
// AEON-1473 (Tick's decisions 1–5): the Decision Desk count in the browser tab.
// Every title leads with "(N) " and the favicon carries an amber count dot; both
// show the header badge's count (useDecisionDesk), never a second source. Motion
// is one-shot: when the count rises the dot pops once, or, in a background tab,
// the title reads "New decision" for two seconds. Reduced motion keeps only the
// number. Nothing here takes focus or plays a sound. The wiring is useDeskTab.ts.

export const TAB_MAX = 9
/** The count as the tab shows it: nothing at 0 or while unknown, 1 to 9, then "9+". */
export function tabCount(count: number | null): string {
  if (count === null || !Number.isFinite(count) || count < 1) return ''
  return count > TAB_MAX ? `${TAB_MAX}+` : String(Math.floor(count))
}
/** "(3) " before the page title; parentheses, the browser convention. */
export function titlePrefix(count: number | null): string {
  const label = tabCount(count)
  return label ? `(${label}) ` : ''
}

// Decision 3 named a leading bullet. A tab title cannot hold an SVG, and the
// glyph guard rejects that bullet, so the notice is the words alone.
export const NEW_DECISION = 'New decision'
export const NOTICE_MS = 2000
/** One pop: scale 1 → 1.2 → 1 in six frames of 100 ms. The last frame is the resting dot. */
export const POP_SCALES: readonly number[] = [1.07, 1.14, 1.2, 1.13, 1.07, 1]
export const POP_FRAME_MS = 100
/** Below this many device pixels a digit is unreadable: the dot stays plain. */
export const DIGIT_MIN_PX = 24

/** The dot in device pixels, anchored to the icon's bottom-right corner so a pop grows into the icon. */
export interface DotLayout { x: number; y: number; width: number; height: number; gap: number; label: string; font: number }
export function dotLayout(size: number, count: number | null, scale = 1, measure: (text: string, font: number) => number = (text, font) => text.length * font * 0.62): DotLayout | null {
  const label = tabCount(count)
  if (!label) return null
  const digits = size >= DIGIT_MIN_PX
  const height = size * (digits ? 0.6 : 0.56) * scale
  const font = Math.round(size * 0.44 * scale)
  const shown = digits ? label : ''
  // A digit sits in a round dot; "9+" widens it into a short capsule.
  const width = shown ? Math.max(height, measure(shown, font) + height * 0.45) : height
  return { x: size - width, y: size - height, width, height, gap: Math.max(1, Math.round(size / 16)), label: shown, font }
}

export interface DotStyle { fill: string; ink: string; family: string }
export const DEFAULT_DOT_STYLE: DotStyle = { fill: '#e8c07a', ink: '#102327', family: 'ui-monospace, SFMono-Regular, Menlo, monospace' }

function capsule(ctx: CanvasRenderingContext2D, x: number, y: number, width: number, height: number) {
  ctx.beginPath()
  ctx.roundRect(x, y, width, height, height / 2)
}

/** Draws the icon with its count dot: a gap cut out of the icon, the amber dot, a hairline edge and the digit. */
export function drawTabIcon(ctx: CanvasRenderingContext2D, size: number, icon: CanvasImageSource | null, count: number | null, scale: number, style: DotStyle) {
  ctx.clearRect(0, 0, size, size)
  if (icon) ctx.drawImage(icon, 0, 0, size, size)
  const fontOf = (font: number) => `700 ${font}px ${style.family}`
  const dot = dotLayout(size, count, scale, (text, font) => { ctx.font = fontOf(font); return ctx.measureText(text).width })
  if (!dot) return
  ctx.save()
  ctx.globalCompositeOperation = 'destination-out'
  capsule(ctx, dot.x - dot.gap, dot.y - dot.gap, dot.width + dot.gap * 2, dot.height + dot.gap * 2)
  ctx.fill()
  ctx.restore()
  capsule(ctx, dot.x, dot.y, dot.width, dot.height)
  ctx.fillStyle = style.fill
  ctx.fill()
  // The hairline keeps the pale amber visible on a light tab strip; on a dark one the amber carries it.
  const line = Math.max(1, size / 32)
  ctx.save()
  ctx.globalAlpha = 0.45
  ctx.lineWidth = line
  ctx.strokeStyle = style.ink
  capsule(ctx, dot.x + line / 2, dot.y + line / 2, dot.width - line, dot.height - line)
  ctx.stroke()
  ctx.restore()
  if (!dot.label) return
  ctx.font = fontOf(dot.font)
  ctx.fillStyle = style.ink
  ctx.textAlign = 'center'
  ctx.textBaseline = 'alphabetic'
  const metrics = ctx.measureText(dot.label)
  const ascent = metrics.actualBoundingBoxAscent || dot.font * 0.72, descent = metrics.actualBoundingBoxDescent || 0
  ctx.fillText(dot.label, dot.x + dot.width / 2, dot.y + dot.height / 2 + (ascent - descent) / 2)
}

export interface TabClock { setTimeout(run: () => void, ms: number): unknown; clearTimeout(id: unknown): void }
export interface TabOutput {
  /** The title's lead, "(3) " or "". */
  prefix(text: string): void
  /** A whole-title notice, or "" to settle back. */
  notice(text: string): void
  /** The favicon with this count's dot at this scale; a count without a label restores the plain icon. */
  paint(count: number | null, scale: number): void
  hidden(): boolean
  reducedMotion(): boolean
  clock: TabClock
}
/** What the Desk store says now. owner is empty when nobody, or no person, is signed in. */
export interface DeskReading { owner: string; count: number | null; error: boolean }

// A read in flight (the store is cleared while it reads again) keeps what the tab
// shows; a failed read shows nothing rather than an old count. Only a rise over the
// last count read for the same person moves anything, and only once.
export function createDeskTab(out: TabOutput) {
  let owner = '', known: number | null = null, shown: number | null = null
  let popTimer: unknown, noticeTimer: unknown
  function stopPop() {
    if (popTimer === undefined) return
    out.clock.clearTimeout(popTimer); popTimer = undefined
  }
  function settleNotice() {
    if (noticeTimer === undefined) return
    out.clock.clearTimeout(noticeTimer); noticeTimer = undefined
    out.notice('')
  }
  function show(count: number | null) {
    shown = count
    out.prefix(titlePrefix(count))
  }
  function rest(count: number | null) {
    stopPop()
    out.paint(count, 1)
  }
  function pop(count: number) {
    stopPop()
    let frame = 0
    const step = () => {
      popTimer = undefined
      out.paint(count, POP_SCALES[frame++])
      if (frame < POP_SCALES.length) popTimer = out.clock.setTimeout(step, POP_FRAME_MS)
    }
    step()
  }
  function flash() {
    settleNotice()
    out.notice(NEW_DECISION)
    noticeTimer = out.clock.setTimeout(() => { noticeTimer = undefined; out.notice('') }, NOTICE_MS)
  }
  function update(reading: DeskReading) {
    if (reading.owner !== owner) {
      owner = reading.owner; known = null
      settleNotice()
      if (shown !== null) { show(null); rest(null) }
    }
    if (!owner) return
    if (reading.count === null) {
      if (reading.error && shown !== null) { settleNotice(); show(null); rest(null) }
      return
    }
    const count = reading.count
    const rose = known !== null && count > known
    known = count
    if (!rose && count === shown) return
    show(count)
    if (!rose || out.reducedMotion() || !tabCount(count)) { rest(count); return }
    // A visible tab sees the dot pop; a background tab's timers are throttled, so its title speaks instead.
    if (out.hidden()) { rest(count); flash() } else pop(count)
  }
  /** A tab sent to the background finishes its pop at rest; one brought forward drops its notice. */
  function visibility(hidden: boolean) {
    if (hidden && popTimer !== undefined) rest(shown)
    if (!hidden) settleNotice()
  }
  function dispose() {
    settleNotice(); stopPop()
    owner = ''; known = null; shown = null
    out.prefix(''); out.paint(null, 1)
  }
  return { update, visibility, dispose }
}
