// SPDX-License-Identifier: AGPL-3.0-only
// P09: person-only proposal reads, honest empty/failure and stale-owner results.
import { afterEach, expect, it, vi } from 'vitest'
import { nextTick, reactive } from 'vue'
import * as doctrine from '../src/lib/doctrine'
import type { DoctrineFinding } from '../src/lib/doctrine'
import { clientComponent, mountRuntime, runtimeNodes, runtimeText } from './helpers/runtime-component'

const before = { name: 'gate:validation', rules_version: '26.9.28.1', samples: 12, value: .75, from: '2026-09-15T12:00:00Z', until: '2026-09-29T12:00:00Z' }
const findings: DoctrineFinding[] = [
  { id: 'draft', pattern: 'gate:validation', title: 'Recurring validation findings', count: 9, rules_version: '26.9.28.1', harness: 'codex', ticket_kind: 'ticket', status: 'draft', proposal_id: 'draft', pr_url: 'https://github.com/inspr-at/inspr-modules/pull/42', rule_label: 'Reproduce the failure before reporting the fix as complete.', created_at: before.until, before, metrics: [{ ...before, name: 'review_rounds', value: 2.5, samples: 9 }, { ...before, name: 'time_to_done', value: 3600, samples: 7 }],
    evidence: ['AEON-281', 'AEON-293', 'AEON-305'].map((key, i) => ({ id: `outcome-${i}`, ticket_id: `ticket-${i}`, ticket_key: key, href: `/p/AEON/${key}`, kind: 'review_verdict' })) },
  { id: 'measured', pattern: 'fix_rounds', title: 'Repeated fix rounds', count: 6, rules_version: '26.9.25.1', harness: 'claude', ticket_kind: 'ticket', status: 'observed', rule_label: 'Check the whole fix before requesting another review.', created_at: before.until,
    before: { ...before, name: 'fix_rounds', value: 3.5, samples: 6 }, after: { ...before, name: 'fix_rounds', rules_version: '26.9.29.1', value: 1.5, samples: 8 }, delta: -2, evidence: [] },
  { id: 'private', pattern: 'learning:security', title: 'Repeated security pitfalls', count: 3, rules_version: '26.9.28.1', harness: 'codex', ticket_kind: 'ticket', status: 'internal_note', reason: 'Kept internal to protect private instruction text.', created_at: before.until, before: { ...before, name: 'learning:security', value: .5, samples: 6 }, evidence: [] },
]


const session = reactive({ identity: { principal: { id: 'A', kind: 'person' } } })
const proposals = vi.fn(), analysis = vi.fn()
let access!: { reset: () => void; refresh: () => void }
const component = clientComponent(new URL('../src/components/rules/DoctrineProposals.vue', import.meta.url), {
  '../../lib/authz': { can: () => true, onAccessRevalidate: (callbacks: typeof access) => { access = callbacks; return () => {} } },
  '../../stores/session': { useSession: () => session },
  '../../lib/doctrine': { ...doctrine, getDoctrineProposals: proposals, getDoctrineFindings: analysis },
  '../business/BizIcon.vue': { default: { render: () => null } },
})
const mounts: ReturnType<typeof mountRuntime>[] = []
afterEach(() => { for (const app of mounts.splice(0)) app.unmount(); vi.resetAllMocks() })
function mount(kind = 'person') {
  session.identity.principal = { id: 'A', kind }
  const app = mountRuntime(component); mounts.push(app); return app.root
}
async function settled() { await Promise.resolve(); await Promise.resolve(); await nextTick() }

it('renders analysis evidence and measured outcomes without inventing an approval action for a finding', async () => {
  proposals.mockResolvedValue([])
  analysis.mockResolvedValue(findings)
  const root = mount(); await settled()
  expect(runtimeText(root)).toContain('Repeated fix rounds')
  expect(runtimeText(root)).toContain('75%')
  expect(runtimeText(root)).toContain('Kept internal to protect private instruction text.')
  expect(runtimeText(root)).toContain('The comparison will appear')
  expect(runtimeNodes(root, row => row.tag === 'a').map(row => row.props.href)).toContain(findings[0]!.pr_url)
  expect(runtimeText(root)).toContain('−2 rounds')
  expect(runtimeText(root)).toContain('does not establish cause')
  expect(runtimeNodes(root, row => row.tag === 'a').map(row => row.props.href)).toContain('/p/AEON/AEON-281')
  expect(runtimeNodes(root, row => row.tag === 'button')).toEqual([])
})

it('does not request or render proposals for an agent principal', async () => {
  const root = mount('agent'); await settled()
  expect(proposals).not.toHaveBeenCalled(); expect(analysis).not.toHaveBeenCalled()
  expect(runtimeText(root)).toBe('')
})

it('reports empty only after successful reads and preserves an honest failure', async () => {
  proposals.mockResolvedValue([]); analysis.mockResolvedValue([])
  const root = mount(); await settled()
  expect(runtimeText(root)).toContain('No proposals yet.')
  access.reset(); analysis.mockRejectedValue(new Error('Analysis unavailable')); access.refresh(); await settled()
  expect(runtimeText(root)).not.toContain('No proposals yet.')
  expect(runtimeNodes(root, row => row.props.role === 'alert')).toHaveLength(1)
})

it('discards a held analysis after the person changes and cannot reveal the previous owner’s finding', async () => {
  let release!: (value: unknown[]) => void, entered!: () => void
  const held = new Promise<unknown[]>(resolve => { release = resolve }), asked = new Promise<void>(resolve => { entered = resolve })
  proposals.mockResolvedValue([]); analysis.mockImplementation(() => { entered(); return held })
  const root = mount(); await asked
  session.identity.principal = { id: 'B', kind: 'person' }; access.reset()
  release([{ id: 'private', title: 'A private finding', count: 1, status: 'observed', pattern: 'fix_rounds', rules_version: '26.9.25.1', harness: 'codex', ticket_kind: 'ticket',
    before: { name: 'fix_rounds', value: 1, samples: 1 }, evidence: [] }]); await settled()
  expect(runtimeText(root)).not.toContain('A private finding')
  expect(runtimeText(root)).not.toContain('No proposals yet.')
  analysis.mockResolvedValue([]); access.refresh(); await settled()
  expect(runtimeText(root)).toContain('No proposals yet.')
})
