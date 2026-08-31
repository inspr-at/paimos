import { describe, expect, it } from 'vitest'
import { parseChangelog } from './changelog'

describe('parseChangelog', () => {
  it('parses calendar releases with preserved leading zeroes and ignores Unreleased', () => {
    const entries = parseChangelog(`# Changelog

## [Unreleased]

- Pending.

## [26.08.31.14.05] — 2026-08-31

- Calendar recut.

## [26.08.31] — 2026-08-31

- Calendar cut.

## [5.21.0] — 2026-08-30

- Legacy release.
`)

    expect(entries.map(entry => entry.version)).toEqual(['26.08.31.14.05', '26.08.31', '5.21.0'])
    expect(entries[0].bodyMd).toBe('- Calendar recut.')
    expect(entries[0].bumpKind).toBe('unknown')
    expect(entries[1].bumpKind).toBe('unknown')
  })

  it('retains legacy SemVer bump classification', () => {
    const entries = parseChangelog(`## [5.22.0] — 2026-08-30\n\n- Minor.\n\n## [5.21.1] — 2026-08-29\n\n- Patch.\n\n## [5.21.0] — 2026-08-28\n\n- Base.`)
    expect(entries.map(entry => entry.bumpKind)).toEqual(['minor', 'patch', 'unknown'])
  })

  it('drops malformed calendar headings', () => {
    const entries = parseChangelog(`## [26.8.31] — 2026-08-31\n\n- Bad.\n\n## [26.08.31] — 2026-08-31\n\n- Good.`)
    expect(entries.map(entry => entry.version)).toEqual(['26.08.31'])
  })
})
