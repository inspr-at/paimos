export interface VersionEntry {
  version: string
  date: string
  bodyMd: string
  bumpKind: 'major' | 'minor' | 'patch' | 'unknown'
}

const CALENDAR_VERSION = String.raw`\d{2}\.\d{2}\.\d{2}(?:\.\d{2}\.\d{2})?`
const SEMVER_VERSION = String.raw`(?!\d{2}\.)\d+\.\d+\.\d+`
const RELEASE_VERSION = `(?:${CALENDAR_VERSION}|${SEMVER_VERSION})`
const VERSION_HEADING_RE = new RegExp(
  String.raw`^## \[(${RELEASE_VERSION})\] — (\d{4}-\d{2}-\d{2})`,
  'm',
)
const VERSION_SECTION_RE = new RegExp(String.raw`(?=^## \[${RELEASE_VERSION}\])`, 'm')
const CALENDAR_VERSION_RE = new RegExp(`^${CALENDAR_VERSION}$`)

export function parseChangelog(raw: string): VersionEntry[] {
  const list: VersionEntry[] = raw
    .split(VERSION_SECTION_RE)
    .map(section => section.trim())
    .filter(section => VERSION_HEADING_RE.test(section))
    .map(section => {
      const match = section.match(VERSION_HEADING_RE)!
      return {
        version: match[1],
        date: match[2],
        bodyMd: section.replace(VERSION_HEADING_RE, '').trim(),
        bumpKind: 'unknown' as const,
      }
    })

  for (let index = 0; index < list.length - 1; index++) {
    const current = list[index]
    const previous = list[index + 1]
    if (CALENDAR_VERSION_RE.test(current.version) || CALENDAR_VERSION_RE.test(previous.version)) continue
    const cur = current.version.split('.').map(Number)
    const prev = previous.version.split('.').map(Number)
    if (cur[0] > prev[0]) current.bumpKind = 'major'
    else if (cur[1] > prev[1]) current.bumpKind = 'minor'
    else if (cur[2] > prev[2]) current.bumpKind = 'patch'
  }
  return list
}
