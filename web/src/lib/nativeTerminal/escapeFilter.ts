// SPDX-License-Identifier: AGPL-3.0-only
/** A bounded, streaming allowlist in front of the maintained VT parser.
 * All string controls (OSC/DCS/APC/PM/SOS), queries, window operations and
 * input reporting modes are discarded, including across chunk boundaries.
 * This is an export filter, not a terminal emulator or a secret redactor. */
export class TerminalEscapeFilter {
  private decoder = new TextDecoder('utf-8', { fatal: true })
  private state: 'text' | 'escape' | 'csi' | 'charset' | 'string' | 'stringEscape' = 'text'
  private sequence = ''
  private oversized = false
  omitted = false

  reset() {
    this.decoder = new TextDecoder('utf-8', { fatal: true })
    this.state = 'text'; this.sequence = ''; this.oversized = false; this.omitted = false
  }
  write(bytes: Uint8Array): string {
    const text = this.decoder.decode(bytes, { stream: true })
    const output: string[] = []
    for (const char of text) {
      if (this.state === 'string' || this.state === 'stringEscape') {
        if (char === '\u0007' || char === '\u009c' || (this.state === 'stringEscape' && char === '\\')) this.state = 'text'
        else this.state = char === '\u001b' ? 'stringEscape' : 'string'
        continue
      }
      if (char === '\u001b') { this.state = 'escape'; this.sequence = ''; this.oversized = false; continue }
      if (char === '\u009b') { this.state = 'csi'; this.sequence = ''; this.oversized = false; continue }
      if (['\u0090', '\u009d', '\u009e', '\u009f', '\u0098'].includes(char)) { this.string(); continue }
      if (char === '\u0018' || char === '\u001a') { this.state = 'text'; this.sequence = ''; continue }
      if (this.state === 'escape') {
        if (char === '[') this.state = 'csi'
        else if (']PX^_'.includes(char)) this.string()
        else if ('()'.includes(char)) { this.state = 'charset'; this.sequence = char }
        else { if ('78DEMH=>'.includes(char)) output.push(`\u001b${char}`); else this.omitted = true; this.state = 'text' }
      } else if (this.state === 'charset') {
        if (char === 'B' || char === '0') output.push(`\u001b${this.sequence}${char}`)
        else this.omitted = true
        this.sequence = ''; this.state = 'text'
      } else if (this.state === 'csi') {
        const code = char.charCodeAt(0)
        if (code >= 0x40 && code <= 0x7e) {
          if (!this.oversized && this.safeCSI(this.sequence, char)) output.push(`\u001b[${this.sequence}${char}`)
          else this.omitted = true
          this.sequence = ''; this.oversized = false; this.state = 'text'
        } else if (code >= 0x20 && code <= 0x3f) {
          if (this.sequence.length < 128 && !this.oversized) this.sequence += char
          else { this.oversized = true; this.omitted = true; this.sequence = '' }
        } else { this.omitted = true; this.sequence = ''; this.state = 'text' }
      } else {
        const code = char.codePointAt(0)!
        if (code >= 0x20 && !(code >= 0x7f && code <= 0x9f)) output.push(char)
        else if ('\r\n\t\b'.includes(char)) output.push(char)
        else this.omitted = true
      }
    }
    return output.join('')
  }
  private string() { this.state = 'string'; this.sequence = ''; this.omitted = true }
  private safeCSI(params: string, final: string): boolean {
    if (final === 'm') return /^[\d;:]*$/.test(params)
    if (final === 'h' || final === 'l') {
      // Render-only modes: insert, origin, wrapping, caret visibility and
      // alternate screen. Mouse/focus/paste/keyboard modes never reach xterm.
      return params === '4' || /^\?(?:6|7|25|47|1047|1048|1049)(?:;(?:6|7|25|47|1047|1048|1049))*$/.test(params)
    }
    return '@ABCDEFGHIJKLMPSTXZ`abdefgrsu'.includes(final) && /^[\d;]*$/.test(params) &&
      params.split(';').every(value => value.length <= 3 && Number(value) <= 320)
  }
}
