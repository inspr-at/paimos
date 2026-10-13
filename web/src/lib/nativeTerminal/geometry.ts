// SPDX-License-Identifier: AGPL-3.0-only
export const terminalGeometry = { baseFont: 13, baseLine: 17, minimumFont: 10, maximumFont: 16, padX: 20, padY: 16 } as const
export interface GridGeometry { font: number; line: number; cell: number; width: number; height: number; top: boolean; right: boolean }

export function viewerGeometry(width: number, height: number, cols: number, rows: number, cell13: number, fit: boolean, holder = false): GridGeometry {
  const g = terminalGeometry
  // Actual size never scales up. Very narrow boxes shrink to 10 px then scroll
  // internally. A holder uses the measured 13/11 px cell rather than Fit.
  let font = Math.max(g.minimumFont, Math.floor(Math.min(fit ? g.maximumFont : g.baseFont,
    g.baseFont * Math.max(0, width - g.padX) / (cols * cell13)) * 8) / 8)
  if (holder) font = (width - g.padX) / cell13 < 40 || (height - g.padY) / g.baseLine < 12 ? 11 : 13
  const line = holder && font === 11 ? 15 : Math.round(g.baseLine * font / g.baseFont * 100) / 100
  const cell = cell13 * font / g.baseFont, w = cols * cell + g.padX, h = rows * line + g.padY
  return { font, line, cell, width: w, height: h, top: h < height - .5, right: w < width - .5 }
}

export function holderGrid(width: number, height: number, cell13: number) {
  const low = (width - 20) / cell13 < 40 || (height - 16) / 17 < 12
  const cell = cell13 * (low ? 11 / 13 : 1), line = low ? 15 : 17
  return { cols: Math.min(320, Math.max(40, Math.floor((width - 20) / cell + 1e-6))),
    rows: Math.min(100, Math.max(12, Math.floor((height - 16) / line + 1e-6))) }
}
