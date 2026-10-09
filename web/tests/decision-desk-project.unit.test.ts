// SPDX-License-Identifier: AGPL-3.0-only
// AEON-1057 risk: a project link that points to the wrong place, a colour that
// changes between screens, or a "Now in …" notice for an item in the same project.
import { describe, expect, it } from 'vitest'
import { deskProject, projectColor, projectHue, projectSwitch } from '../src/lib/decisionDesk'

describe('Decision Desk project identity', () => {
  it('links a project by key, keeps one hue per key and leaves workspace items neutral', () => {
    const orbit = deskProject({ projectId: 'p-orbit', projectName: 'Orbit', projectKey: 'ORB 1' })
    expect(orbit).toMatchObject({ id: 'p-orbit', key: 'ORB 1', name: 'Orbit', href: '/p/ORB%201' })
    expect(orbit.hue).toBe(projectHue('ORB 1'))
    expect(deskProject({ projectId: 'p-orbit', projectName: 'Orbit (renamed)', projectKey: 'ORB 1' }).hue).toBe(orbit.hue)
    expect([190, 75, 290, 12, 150, 240, 45, 335]).toContain(orbit.hue)
    expect(projectColor(orbit)).toBe(`oklch(var(--project-l) var(--project-c) ${orbit.hue})`)
    const workspace = deskProject({ projectId: '', projectName: 'Workspace', projectKey: 'IGNORED' })
    expect(workspace).toMatchObject({ key: '', href: '', hue: null })
    expect(projectColor(workspace)).toBe('var(--ink-3)')
  })

  it('names the previous project only when the project changed', () => {
    const orbit = deskProject({ projectId: 'p-orbit', projectName: 'Orbit', projectKey: 'ORB' })
    const harbor = deskProject({ projectId: 'p-harbor', projectName: 'Harbor', projectKey: 'HBR' })
    expect(projectSwitch(undefined, orbit)).toBeUndefined()
    expect(projectSwitch(orbit, { ...orbit, name: 'Orbit' })).toBeUndefined()
    expect(projectSwitch(orbit, harbor)).toBe(orbit)
  })
})
