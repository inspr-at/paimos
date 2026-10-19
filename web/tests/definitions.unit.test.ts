// SPDX-License-Identifier: AGPL-3.0-only
import { expect, it, vi } from 'vitest'
vi.mock('../src/lib/api', () => ({ api: vi.fn(), APIError: class extends Error {} }))
import { decideDefinitionChange, definitionChangeWords, definitionDate, definitionDecisionWords, definitionDeskWords, definitionDiff, definitionNotice, resetChanges, ruleWords, sameRule, saveWorkspaceDefinition, type DefinitionChange, type DefinitionChecks, type DefinitionContext } from '../src/lib/definitions'
import { api } from '../src/lib/api'
import { deskItemID, deskLinkOut } from '../src/lib/decisionDesk'

// Risk: a full copy is misrepresented as a partial merge, later changes look
// deliberate, or a reset confirmation omits a condition, owner or order change.
it('AEON-1316 describes complete copies, conditions, owners and their retained base', () => {
  const base: DefinitionChecks = { in_progress: [{ id: 'estimate_set' }, { id: 'acceptance_present' }], done: [{ id: 'manual:review', label: 'Reviewed', owner_id: 'ada' }] }
  const workspace: DefinitionChecks = { in_progress: [{ id: 'estimate_set', labels: ['ui'] }, { id: 'acceptance_present' }, { id: 'no_active_build' }] }
  const own: DefinitionChecks = { in_progress: [{ id: 'estimate_set' }, { id: 'design_attached' }], done: [{ id: 'manual:review', label: 'Reviewed', owner_id: 'ada' }] }
  expect(definitionDiff(own, workspace, base).map(r => [r.item.id,r.kind])).toEqual([
    ['estimate_set','waiting_changed'], ['design_attached','added'], ['acceptance_present','left'], ['no_active_build','waiting_new'], ['manual:review','waiting_removed'],
  ])
  expect(definitionDiff({}, workspace).every(r => r.kind === 'left')).toBe(true)
  expect(sameRule({ id: 'design_attached' }, { id: 'design_attached', condition: 'ui', labels: [] })).toBe(true)
  expect(sameRule({ id: 'estimate_set', labels: ['UI','code'] }, { id: 'estimate_set', labels: ['code','ui'] })).toBe(true)
  expect(sameRule({ id: 'estimate_set', kinds: ['Work'] }, { id: 'estimate_set', kinds: ['work'] })).toBe(false)
  expect(sameRule({ id: 'manual:review', label: 'Reviewed', owner_id: 'ada' }, { id: 'manual:review', label: 'Reviewed', owner_id: 'grace' })).toBe(false)
  expect(ruleWords({ id: 'estimate_set', kinds: ['work'], labels: ['ui','security'] })).toBe('All tickets · Kinds: work · Labels: ui, security')
  expect(resetChanges(own, workspace)).toEqual([
    'Dropped: Approved design attached, Reviewed.', 'Comes back: Acceptance criteria written, No other build in flight.', 'Uses the workspace version: Estimate set.',
  ])
  expect(resetChanges({ in_progress: [{id:'acceptance_present'},{id:'estimate_set'}] }, base)).toContain('Ready returns to the workspace check order.')
})

// Risk: a decision drops its displayed revision, accepts arbitrary link-out
// destinations, or loses the exact German copy and nonbreaking date/count spaces.
it('AEON-1317 binds decisions to both revisions and renders the complete bilingual decision copy', async () => {
  const date = definitionDate('2026-10-09T08:00:00Z'), deDate = definitionDate('2026-10-09T08:00:00Z','de')
  expect(date).toBe('9\u00a0Oct'); expect(deDate).toBe('9.\u00a0Okt.')
  const changes: DefinitionChange[] = [
    {check_id:'no_active_build',block:'in_progress',kind:'new',before:null,current:{id:'no_active_build'}},
    {check_id:'estimate_set',block:'in_progress',kind:'changed',before:{id:'estimate_set'},current:{id:'estimate_set',condition:'code'}},
    {check_id:'acceptance_present',block:'in_progress',kind:'removed',before:{id:'acceptance_present'},current:null},
  ]
  expect(changes.map(change => definitionChangeWords(change,deDate,'de'))).toEqual([
    'Neu im Workspace-Standard seit 9.\u00a0Okt.',
    'Der Workspace-Standard sagt jetzt Code-Tickets (seit 9.\u00a0Okt.)',
    'Seit 9.\u00a0Okt. nicht mehr im Workspace-Standard',
  ])
  expect(changes.map(change => definitionDecisionWords(change,change.before ?? undefined,'de'))).toEqual([['Hier übernehmen','Weglassen'],['Auch hier übernehmen','Alle Tickets behalten'],['Auch hier entfernen','Behalten']])
  expect(definitionNotice('PRJ-26',1,deDate,'de')).toBe('Der Workspace-Standard wurde am 9.\u00a0Okt. geändert, nachdem PRJ-26 ihn kopiert hat. 1\u00a0Änderung wartet unten auf eine Entscheidung; bis dahin behält PRJ-26 seine Kopie unverändert.')
  expect(definitionNotice('PRJ-26',3,date)).toContain('3\u00a0changes wait')
  expect(definitionNotice('PRJ-26',0,date)).toBe('All changes decided.')
  expect(definitionNotice('PRJ-26',0,deDate,'de')).toBe('Alle Änderungen entschieden.')
  expect(definitionDeskWords('PRJ-26',3,'de')).toBe('PRJ-26: 3\u00a0Änderungen am Workspace-Standard warten auf eine Entscheidung')
  expect(definitionChangeWords(changes[1]!,date,'en','Code tickets · Kinds: work · Labels: security')).toBe('The workspace default now says Code tickets · Kinds: work · Labels: security (since 9\u00a0Oct)')
  const context: DefinitionContext = {project_id:'project-a',definition:{id:'copy',revision:'copy-revision',source:'project',copied_from_revision:'original-copy-revision',checks:{}},workspace_default:{id:'workspace',revision:'workspace-revision',source:'workspace',copied_from_revision:null,checks:{}},copied_from_checks:{},can_manage:true}
  vi.mocked(api).mockResolvedValue({ok:true,json:async () => context} as Response)
  await decideDefinitionChange('project-a',changes[0]!,'keep',context)
  const [path,request] = vi.mocked(api).mock.calls.at(-1)!
  expect(path).toBe('/projects/project-a/definitions/decisions')
  expect(JSON.parse(request!.body as string)).toEqual({expected_revision:'copy-revision',expected_workspace_revision:'workspace-revision',check_id:'no_active_build',decision:'keep'})
  await saveWorkspaceDefinition({}, {...context.workspace_default!,id:'00000000-0000-0000-0000-000000000000'})
  expect(JSON.parse(vi.mocked(api).mock.calls.at(-1)![1]!.body as string).expected_revision).toBeNull()
  const target='/p/PRJ-26/settings/definitions?definition_changes=1#definition-changes'
  expect(deskLinkOut({kind:'definition_change',href:target})).toBe(target)
  expect(deskItemID({kind:'definition_change',id:'copy-id'})).toBe('d:copy-id')
  for (const href of ['https://example.com', '//example.com','/p/PRJ-26/settings/access', '/p/PRJ-26/settings/definitions?definition_changes=1#other']) expect(deskLinkOut({kind:'definition_change',href})).toBe('')
})
