// SPDX-License-Identifier: AGPL-3.0-only
/** Pixel tolerance for caret positions that sit on one wrapped line. */
export const VISUAL_LINE_EPSILON_PX = 2

/** Bounds of the logical line that contains `offset`, split only on newline characters. */
export function logicalLineBounds(text: string, offset: number): { start: number; end: number } {
  const length = text.length
  const clamped = Math.max(0, Math.min(offset, length))
  // Walk from the caret. lastIndexOf(fromIndex 0) would treat a leading newline as the previous line.
  let start = clamped
  while (start > 0 && text.charAt(start - 1) !== '\n') start -= 1
  let end = clamped
  while (end < length && text.charAt(end) !== '\n') end += 1
  return { start, end }
}

export type CaretBox = {
  top: number
  bottom: number
  left: number
  right: number
  width: number
  height: number
}

/**
 * Visual line containing the caret, using caret tops when layout can measure them.
 * `caretTopAt` receives a caret offset. A null top falls back to the logical line.
 * The result stays inside the current newline segment and the current text block.
 */
export type LineEdge = { offset: number; start: number; end: number }

/** Fallback Home/End target. A remembered edge keeps a second press on that wrapped line. */
export function lineBoundaryTarget(
  key: 'Home' | 'End',
  offset: number,
  text: string,
  remembered: LineEdge | null,
  caretTopAt?: (offset: number) => number | null,
): { target: number; edge: LineEdge } {
  const clamped = Math.max(0, Math.min(offset, text.length))
  const line =
    remembered && remembered.offset === clamped
      ? remembered
      : visualLineOf(text, clamped, caretTopAt)
  const target = key === 'Home' ? line.start : line.end
  return { target, edge: { offset: target, start: line.start, end: line.end } }
}

export function collectTextNodes(root: Node): Text[] {
  if (root.nodeType === Node.TEXT_NODE) return [root as Text]
  const nodes: Text[] = []
  const walker = document.createTreeWalker(root, NodeFilter.SHOW_TEXT)
  let current = walker.nextNode()
  while (current) {
    nodes.push(current as Text)
    current = walker.nextNode()
  }
  return nodes
}

export function linearText(nodes: readonly Text[]): string {
  return nodes.map((node) => node.data).join('')
}

/** Caret offset counted through existing text nodes. Does not rewrite the DOM. */
export function offsetInTextNodes(
  nodes: readonly Text[],
  focus: Node,
  focusOffset: number,
): number | null {
  let cursor = 0
  for (const node of nodes) {
    if (node === focus) return cursor + Math.max(0, Math.min(focusOffset, node.data.length))
    cursor += node.data.length
  }
  return null
}

export function offsetInRoot(root: HTMLElement, focus: Node, focusOffset: number): number {
  const nodes = collectTextNodes(root)
  const direct = offsetInTextNodes(nodes, focus, focusOffset)
  if (direct != null) return direct
  if (focus !== root) return 0
  let count = 0
  for (let index = 0; index < focusOffset && index < root.childNodes.length; index += 1) {
    count += linearText(collectTextNodes(root.childNodes[index]!)).length
  }
  return count
}

/** Map a linear offset onto the text node that already holds that character. */
export function pointInTextNodes(
  nodes: readonly Text[],
  offset: number,
): { node: Text; offset: number } | null {
  if (!nodes.length) return null
  let cursor = 0
  for (const node of nodes) {
    const next = cursor + node.data.length
    if (offset <= next) return { node, offset: offset - cursor }
    cursor = next
  }
  const last = nodes[nodes.length - 1]!
  return { node: last, offset: last.data.length }
}

export function caretTopAtLinear(nodes: readonly Text[], offset: number): number | null {
  const point = pointInTextNodes(nodes, offset)
  if (!point) return null
  const range = document.createRange()
  range.setStart(point.node, point.offset)
  range.collapse(true)
  const rect = range.getBoundingClientRect()
  return rect.height > 0 ? rect.top : null
}

export function visualLineOf(
  text: string,
  offset: number,
  caretTopAt?: (offset: number) => number | null,
): { start: number; end: number } {
  const segment = logicalLineBounds(text, offset)
  if (!caretTopAt || segment.end <= segment.start) return segment
  const origin = caretTopAt(offset)
  if (origin == null) return segment
  const same = (caretOffset: number) => {
    const top = caretTopAt(caretOffset)
    if (top == null) return null
    return Math.abs(top - origin) <= VISUAL_LINE_EPSILON_PX
  }
  let start = offset
  for (let index = offset; index >= segment.start; index -= 1) {
    const hit = same(index)
    if (hit == null) return segment
    if (!hit) break
    start = index
  }
  let end = offset
  for (let index = offset; index <= segment.end; index += 1) {
    const hit = same(index)
    if (hit == null) return segment
    if (!hit) break
    end = index
  }
  return { start, end }
}
