// SPDX-License-Identifier: AGPL-3.0-only
import { nativeSessionBounds as bounds } from '../nativeSessionContract'
import { gridSize } from './frames'
import { TerminalEscapeFilter } from './escapeFilter'
import { browserClock, type TerminalClock } from './ports'

/** Public structural subset of @xterm/headless, supplied by I2. No DOM open,
 * addon, event subscription, link provider, data/reply or native I/O exists. */
export interface HeadlessCell {
  getChars(): string
  getWidth(): number
  isBold(): number
  isInverse(): number
  isFgDefault(): boolean
  isFgPalette(): boolean
  isFgRGB(): boolean
  getFgColor(): number
  isBgDefault(): boolean
  isBgPalette(): boolean
  isBgRGB(): boolean
  getBgColor(): number
}
export interface HeadlessTerminal {
  buffer: { active: { baseY: number; getLine(index: number): { getCell(index: number): HeadlessCell | undefined } | undefined } }
  write(data: string, callback: () => void): void
  dispose(): void
}
export interface HeadlessOptions {
  cols: number; rows: number; scrollback: 0; allowProposedApi: true; disableStdin: true
  cursorBlink: false; logLevel: 'off'; windowOptions: Record<string, never>
}
export type HeadlessConstructor = new (options: HeadlessOptions) => HeadlessTerminal
export interface TerminalRenderer {
  append(bytes: Uint8Array): boolean
  dispose(): void
}
export type TerminalRendererFactory = (element: HTMLElement, cols: number, rows: number, incomplete: (reason: string, closed?: boolean) => void, clock?: TerminalClock) => TerminalRenderer

function color(cell: HeadlessCell, background: boolean): string {
  const value = background ? cell.getBgColor() : cell.getFgColor()
  const defaults = background ? cell.isBgDefault() : cell.isFgDefault()
  const palette = background ? cell.isBgPalette() : cell.isFgPalette()
  const rgb = background ? cell.isBgRGB() : cell.isFgRGB()
  if (defaults) return background ? 'var(--nt-bg)' : 'var(--nt-fg)'
  if (!Number.isInteger(value) || value < 0) return background ? 'var(--nt-bg)' : 'var(--nt-fg)'
  if (palette && value < 16) return `var(--nt-ansi-${value})`
  if (palette && value < 232) {
    const n = value - 16, channel = (c: number) => c ? 55 + 40 * c : 0
    return `rgb(${channel(Math.floor(n / 36))},${channel(Math.floor(n / 6) % 6)},${channel(n % 6)})`
  }
  if (palette && value < 256) { const gray = 8 + (value - 232) * 10; return `rgb(${gray},${gray},${gray})` }
  if (rgb && value <= 0xffffff) return `#${value.toString(16).padStart(6, '0')}`
  return background ? 'var(--nt-bg)' : 'var(--nt-fg)'
}

/** xterm parses and maintains the grid; painting reads cells into textContent.
 * No raw VT bytes, markup, URLs, caret or vendor error can reach the DOM. */
export function createXtermRenderer(Terminal: HeadlessConstructor): TerminalRendererFactory {
  return (element, cols, rows, incomplete, clock = browserClock) => {
    if (!gridSize(cols, rows)) throw new Error('invalid terminal grid')
    const make = () => new Terminal({ cols, rows, scrollback: 0, allowProposedApi: true, disableStdin: true,
      cursorBlink: false, logLevel: 'off', windowOptions: {} })
    let terminal = make(), disposed = false, busy = false, writing = 0, queued = 0, consumed = 0
    let timer: unknown, timerActive = false
    const filter = new TerminalEscapeFilter(), queue: Uint8Array[] = []
    function paint() {
      if (disposed || !element.isConnected) return
      const fragment = document.createDocumentFragment()
      for (let y = 0; y < rows; y++) {
        const line = terminal.buffer.active.getLine(terminal.buffer.active.baseY + y), row = document.createElement('div')
        row.className = 'nt-row'
        let run: HTMLSpanElement | null = null, runStyle = '', runWidth = 0, runText = ''
        function flush() {
          if (!run) return
          run.textContent = runText; run.style.width = `calc(var(--nt-cell) * ${runWidth})`; row.append(run)
          run = null; runText = ''; runWidth = 0
        }
        for (let x = 0; x < cols; x++) {
          const cell = line?.getCell(x), width = cell?.getWidth() ?? 1
          if (width === 0) continue
          const chars = cell?.getChars() || ' ', text = chars.slice(0, 64)
          if (chars.length > 64) incomplete('A terminal glyph exceeded the display limit. This view is incomplete.')
          const fg = cell ? color(cell, false) : 'var(--nt-fg)', bg = cell ? color(cell, true) : 'var(--nt-bg)'
          const inverse = !!cell?.isInverse(), bold = !!cell?.isBold(), style = `${fg}/${bg}/${inverse}/${bold}`
          // Ordinary monospace runs share one node. Wide/combined glyphs get
          // their own cell so fallback glyph metrics cannot disturb the grid.
          const ordinary = width === 1 && /^[\x20-\x7e]$/.test(text)
          if (!ordinary || runStyle !== style) flush()
          if (!run) {
            run = document.createElement('span'); runStyle = style
            run.style.color = inverse ? bg : fg; run.style.backgroundColor = inverse ? fg : bg
            if (bold) run.style.fontWeight = '600'
          }
          runText += text; runWidth += width === 2 ? 2 : 1
          if (!ordinary) flush()
        }
        flush()
        fragment.append(row)
      }
      element.replaceChildren(fragment)
    }
    function dispose() {
      if (disposed) return
      disposed = true; writing++
      if (timerActive) clock.clearTimeout(timer)
      timerActive = false; queue.length = 0; queued = 0; consumed = 0; filter.reset()
      terminal.dispose(); element.replaceChildren()
    }
    function fail(reason: string) { dispose(); incomplete(reason, true) }
    function drain() {
      if (busy || disposed) return
      const bytes = queue.shift()
      if (!bytes) return
      queued -= bytes.byteLength
      if (consumed + bytes.byteLength > bounds.browser_tail_bytes) {
        terminal.dispose(); terminal = make(); consumed = 0; element.replaceChildren()
        incomplete('Earlier terminal output was discarded at the live buffer limit. This view is incomplete.')
      }
      consumed += bytes.byteLength
      let safe: string
      try { safe = filter.write(bytes) } catch { fail('A terminal frame had invalid UTF-8. Reconnect for a fresh live view.'); return }
      if (filter.omitted) incomplete('Unsafe terminal controls were omitted. This live view is incomplete.')
      if (!safe) { drain(); return }
      busy = true
      const turn = ++writing
      timerActive = true
      timer = clock.setTimeout(() => { if (!disposed && turn === writing) fail('Terminal rendering stopped. Reconnect for a fresh live view.'); }, bounds.body_work_deadline_seconds * 1000)
      try {
        terminal.write(safe, () => {
          if (disposed || turn !== writing) return
          if (timerActive) clock.clearTimeout(timer)
          timerActive = false; busy = false
          try { paint(); drain() } catch { fail('Terminal rendering stopped. Reconnect for a fresh live view.') }
        })
      } catch { fail('Terminal rendering stopped. Reconnect for a fresh live view.') }
    }
    return {
      append(bytes) {
        if (disposed || bytes.byteLength > bounds.pty_chunk_bytes || queued + bytes.byteLength > bounds.browser_tail_bytes || queue.length >= bounds.live_replay_frames) {
          if (!disposed) fail('The terminal renderer fell behind. Output was discarded; reconnect for a fresh live view.')
          return false
        }
        queue.push(bytes); queued += bytes.byteLength; drain(); return !disposed
      },
      dispose,
    }
  }
}
