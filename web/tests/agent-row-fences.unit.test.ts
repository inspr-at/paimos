// SPDX-License-Identifier: AGPL-3.0-only
import { expect, it } from 'vitest'
import { ESLint } from 'eslint'
import * as rows from '../src/lib/agentRows'

const eslint = new ESLint()
const rejected = async (code: string, path = 'src/components/agents/FenceFixture.ts') => {
  const [result] = await eslint.lintText(code, { filePath: path })
  expect(result!.errorCount, code).toBeGreaterThan(0)
}

it('does not export a path builder for fetching raw session/run rows', () => {
  expect(rows).not.toHaveProperty('sessionResource')
  expect(rows).not.toHaveProperty('sessionResourceById')
})

it('rejects scalar, array, generic, imported-alias and local-alias casts to row brands', async () => {
  for (const code of [
    'const row = value as Admitted<RawRow>',
    'const rows = value as HarnessSession[]',
    'const rows = <AgentRun[]>value',
    'const rows = value as Array<Wire<RawRow>>',
    "import type { HarnessSession as Session } from '../../lib/agents'; const rows = value as Session[]",
    "import type { Wire as W } from '../../lib/wire'; const row = <W<RawRow>>value",
    'type Rows = AgentRun[]; type Alias = Rows; const rows = value as Alias',
  ]) await rejected(code)
})

it('rejects direct JSON.parse into a typed row and Object.assign copies that carry its brand', async () => {
  await rejected('const row: HarnessSession = JSON.parse(text)')
  await rejected('const rows: AgentRun[] = JSON.parse(text)')
  await rejected('const row = Object.assign({}, admitted, { phase: "stopped" })')
})

it('rejects endpoint literals and opening/wrapping wire rows outside their owner', async () => {
  await rejected("fetch('/runs/' + id)")
  await rejected("fetch(`/projects/${project}/harness-sessions/${id}`)")
  await rejected("import { openRow } from '../../lib/wire'")
  await rejected("import { wrapRow } from '../../lib/wire'")
})

it('accepts normal typed reads, store admission and fixed sub-resource operations', async () => {
  const [result] = await eslint.lintText(`
    import type { HarnessSession } from '../../lib/agents'
    import { getSession, readSessionProvenance } from '../../lib/agentRows'
    const session: HarnessSession = agents.admitSessions([await getSession(project, id)])[0]
    const provenance = await readSessionProvenance(project, id)
  `, { filePath: 'src/components/agents/FenceFixture.ts' })
  expect(result!.messages).toEqual([])
})
