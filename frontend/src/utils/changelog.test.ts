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

  it('parses INSPR calendar v2 coordinates ahead of the SemVer-shaped legacy line (PAI-979)', () => {
    const entries = parseChangelog(`## [Unreleased]

- Pending.

## [260910081500.0.0] — 2026-09-10

- First v2 cut.

## [26.09.09.13.13] — 2026-09-09

- Last v1 recut.

## [5.21.0] — 2026-08-30

- Legacy release.
`)
    expect(entries.map(entry => entry.version)).toEqual(['260910081500.0.0', '26.09.09.13.13', '5.21.0'])
    expect(entries[0].bodyMd).toBe('- First v2 cut.')
    // A twelve-digit MAJOR is a timestamp, never a "major bump".
    expect(entries.map(entry => entry.bumpKind)).toEqual(['unknown', 'unknown', 'unknown'])
  })

  it('drops malformed v2 headings (wrong width, suffix, non-zero minor/patch)', () => {
    const entries = parseChangelog(
      `## [2609100815.0.0] — 2026-09-10\n\n- Ten digits.\n\n## [260910081500.0.1] — 2026-09-10\n\n- Patch.\n\n## [260910081500.0.0-rc1] — 2026-09-10\n\n- Suffix.\n\n## [260910081500.0.0] — 2026-09-10\n\n- Good.`,
    )
    expect(entries.map(entry => entry.version)).toEqual(['260910081500.0.0'])
  })

  it('drops malformed calendar headings', () => {
    const entries = parseChangelog(`## [26.8.31] — 2026-08-31\n\n- Bad.\n\n## [26.08.31] — 2026-08-31\n\n- Good.`)
    expect(entries.map(entry => entry.version)).toEqual(['26.08.31'])
  })
})
