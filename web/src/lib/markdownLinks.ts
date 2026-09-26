// SPDX-License-Identifier: AGPL-3.0-only
// Ticket keys and bare http(s) addresses in Markdown prose. markdown-it stays
// at linkify:false so this pass can skip code and links that are already there.
// A key becomes a placeholder; MarkdownBody mounts TicketLink into it, so a
// plain click opens the app peek and a modified click follows the real URL.
import type { MarkdownIt, StateCore, Token } from 'markdown-it'

// Same shape as a node key: prefix of 2–10 characters, then a number without a
// leading zero, at most 30 characters in all.
const KEY = /[A-Z][A-Z0-9]{1,9}-[1-9][0-9]*/g
const BARE_URL = /https?:\/\/[^\s<>"'`]+/gi
const TRAILING_PUNCT = /[.,;:!?]+$/

export interface PlainPart { kind: 'text' | 'ticket' | 'url'; text: string }

function count(value: string, char: string) {
  let n = 0
  for (const c of value) if (c === char) n++
  return n
}

// Trailing sentence punctuation stays outside the address. A closing
// parenthesis stays when it belongs to the path.
export function trimBareUrl(raw: string): string {
  let url = raw
  for (;;) {
    const cut = url.replace(TRAILING_PUNCT, '')
    if (cut !== url) { url = cut; continue }
    if (url.endsWith(')') && count(url, '(') < count(url, ')')) { url = url.slice(0, -1); continue }
    return url
  }
}

function safeHttpUrl(raw: string): string | null {
  const text = trimBareUrl(raw)
  if (!/^https?:\/\//i.test(text)) return null
  try {
    const url = new URL(text)
    if (url.protocol !== 'http:' && url.protocol !== 'https:') return null
    if (url.username || url.password || !url.hostname) return null
    return text
  } catch {
    return null
  }
}

function overlaps(start: number, end: number, ranges: { start: number; end: number }[]) {
  return ranges.some(range => start < range.end && end > range.start)
}

// Split one text run. Callers skip text that already sits in a link or in code.
export function splitPlainText(input: string): PlainPart[] {
  if (!input) return []
  interface Hit { start: number; end: number; kind: 'ticket' | 'url'; text: string }
  const hits: Hit[] = []
  const blocked: { start: number; end: number }[] = []
  for (const match of input.matchAll(new RegExp(BARE_URL.source, 'gi'))) {
    const start = match.index ?? 0
    const raw = match[0]
    blocked.push({ start, end: start + raw.length })
    const text = safeHttpUrl(raw)
    if (!text) continue
    hits.push({ start, end: start + text.length, kind: 'url', text })
  }
  for (const match of input.matchAll(new RegExp(KEY.source, 'g'))) {
    const text = match[0]
    if (text.length > 30) continue
    const start = match.index ?? 0
    const end = start + text.length
    const before = start > 0 ? input[start - 1] : ''
    const after = end < input.length ? input[end] : ''
    if ((before && /[A-Za-z0-9_]/.test(before)) || (after && /[A-Za-z0-9_]/.test(after))) continue
    if (overlaps(start, end, blocked)) continue
    hits.push({ start, end, kind: 'ticket', text })
  }
  hits.sort((a, b) => a.start - b.start || b.end - a.end)
  const parts: PlainPart[] = []
  let cursor = 0
  for (const hit of hits) {
    if (hit.start < cursor) continue
    if (hit.start > cursor) parts.push({ kind: 'text', text: input.slice(cursor, hit.start) })
    parts.push({ kind: hit.kind, text: hit.text })
    cursor = hit.end
  }
  if (cursor < input.length) parts.push({ kind: 'text', text: input.slice(cursor) })
  return parts
}

function linkChildren(children: Token[], TokenCtor: StateCore['Token'], markdown: MarkdownIt): Token[] {
  const out: Token[] = []
  let inLink = 0
  for (const child of children) {
    if (child.type === 'link_open') { inLink++; out.push(child); continue }
    if (child.type === 'link_close') { inLink = Math.max(0, inLink - 1); out.push(child); continue }
    if (inLink > 0 || child.type !== 'text' || !child.content) { out.push(child); continue }
    const parts = splitPlainText(child.content)
    if (parts.length === 1 && parts[0].kind === 'text') { out.push(child); continue }
    for (const part of parts) {
      if (part.kind === 'text') {
        const token = new TokenCtor('text', '', 0)
        token.content = part.text
        out.push(token)
      } else if (part.kind === 'ticket') {
        const token = new TokenCtor('ticket_ref', '', 0)
        token.content = part.text
        out.push(token)
      } else {
        const href = markdown.normalizeLink(part.text)
        if (!markdown.validateLink(href)) {
          const token = new TokenCtor('text', '', 0)
          token.content = part.text
          out.push(token)
          continue
        }
        const open = new TokenCtor('link_open', 'a', 1)
        open.attrSet('href', href)
        open.attrSet('rel', 'noopener noreferrer')
        const text = new TokenCtor('text', '', 0)
        text.content = part.text
        const close = new TokenCtor('link_close', 'a', -1)
        out.push(open, text, close)
      }
    }
  }
  return out
}

// `after` is the ruler name this follows. MarkdownBody runs task lists first,
// then this, so a checkbox marker is not mistaken for a key.
export function installMarkdownTicketLinks(markdown: MarkdownIt, after = 'task-lists') {
  markdown.core.ruler.after(after, 'ticket-and-urls', state => {
    for (const token of state.tokens) {
      if (token.type !== 'inline' || !token.children) continue
      token.children = linkChildren(token.children, state.Token, markdown)
    }
  })
  markdown.renderer.rules.ticket_ref = (tokens, idx) => {
    const key = markdown.utils.escapeHtml(tokens[idx].content)
    return `<span class="md-ticket" data-ticket-key="${key}">${key}</span>`
  }
}
