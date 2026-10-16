// SPDX-License-Identifier: AGPL-3.0-only

// "1h30", "1h 30m", "90m", "1.5" (hours), "1,5h", "1:30". Whole minutes only;
// returns seconds, or null when the text is not a duration.
export function parseDurationInput(text: string): number | null {
  const value = text.trim().toLowerCase().replace(',', '.').replace(/\s+/g, '')
  if (!value) return null
  let minutes: number | null = null
  let match: RegExpExecArray | null
  if ((match = /^(\d{1,2}):([0-5]\d)$/.exec(value))) minutes = Number(match[1]) * 60 + Number(match[2])
  else if ((match = /^(\d{1,2})h(?:(\d{1,3})m?)?$/.exec(value))) minutes = Number(match[1]) * 60 + Number(match[2] ?? 0)
  else if ((match = /^(\d{1,4})m(?:in)?$/.exec(value))) minutes = Number(match[1])
  else if ((match = /^(\d{1,2})(?:\.(\d{1,2}))?h?$/.exec(value))) {
    const hundredths = Number(match[1]) * 100 + Number((match[2] ?? '').padEnd(2, '0'))
    if ((hundredths * 60) % 100 !== 0) return null
    minutes = (hundredths * 60) / 100
  }
  if (minutes === null || minutes <= 0 || minutes > 24 * 60) return null
  return minutes * 60
}

// Compact clock for grids: 1:30, 0:45, 12:00.
export function formatClock(seconds: number): string {
  const minutes = Math.round(seconds / 60)
  return `${Math.floor(minutes / 60)}:${String(minutes % 60).padStart(2, '0')}`
}

// Prose: 1h 30m, 45m, 8h.
export function formatSpan(seconds: number): string {
  const minutes = Math.round(seconds / 60)
  const hours = Math.floor(minutes / 60)
  const rest = minutes % 60
  if (!hours) return `${rest}m`
  return rest ? `${hours}h ${rest}m` : `${hours}h`
}
