// SPDX-License-Identifier: AGPL-3.0-only
import { expect, it, vi } from 'vitest'
vi.mock('../src/lib/api', () => ({ api: vi.fn(), APIError: class extends Error {} }))
import { decideDefinitionChange, definitionBlocks, definitionAdminNames, definitionChangeWords, definitionDate, definitionDecisionWords, definitionDeskWords, definitionDiff, definitionNotice, resetChanges, ruleIcon, ruleLabel, ruleWords, sameRule, saveWorkspaceDefinition, type DefinitionChange, type DefinitionChecks, type DefinitionContext } from '../src/lib/definitions'
import { api } from '../src/lib/api'
import { deskItemID, deskLinkOut } from '../src/lib/decisionDesk'
import { displayLanguage } from '../src/lib/displayLanguage'
import { DEFINITION_EXPLAINERS_DE } from '../src/lib/projectSettingsExplainers'

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
  // The approved Definitions catalog follows the profile through the shared
  // policy, without switching the surrounding incomplete app into German.
  for (const locale of ['de', 'de-AT', ' DE-de ']) {
    const language = displayLanguage(locale, 'definitions')
    expect(language).toBe('de')
    expect(definitionNotice('PRJ-26', 0, '', language)).toBe('Alle Änderungen entschieden.')
    expect(displayLanguage(locale)).toBe('en')
  }
  for (const locale of [undefined, null, '', 'en-GB', 'fr-FR']) expect(displayLanguage(locale, 'definitions')).toBe('en')
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
  expect(definitionDecisionWords(changes[1]!,changes[1]!.before!)).toEqual(['Use it here too','Keep all tickets'])
  expect(definitionNotice('PRJ-26',1,date)).toContain('1\u00a0change waits for a decision')
  expect(definitionNotice('PRJ-26',1,deDate,'de')).toBe('Der Workspace-Standard wurde am 9.\u00a0Okt. geändert, nachdem PRJ-26 ihn kopiert hat. 1\u00a0Änderung wartet unten auf eine Entscheidung; bis dahin behält PRJ-26 seine Kopie unverändert.')
  expect(definitionNotice('PRJ-26',3,date)).toContain('3\u00a0changes wait')
  expect(definitionNotice('PRJ-26',0,date)).toBe('All changes decided.')
  expect(definitionNotice('PRJ-26',0,deDate,'de')).toBe('Alle Änderungen entschieden.')
  expect(definitionDeskWords('PRJ-26',3,'de')).toBe('PRJ-26: 3\u00a0Änderungen am Workspace-Standard warten auf eine Entscheidung')
  expect(definitionDeskWords('PRJ-26',1)).toBe('PRJ-26: 1\u00a0change to the workspace default waits for a decision')
  expect(definitionDeskWords('PRJ-26',1,'de')).toBe('PRJ-26: 1\u00a0Änderung am Workspace-Standard wartet auf eine Entscheidung')
  expect(definitionAdminNames([{id:'ada',name:'Ada'}],2)).toBe('Ada and 2 more')
  expect(definitionAdminNames([{id:'ada',name:'Ada'}],2,'de')).toBe('Ada und 2 weitere')
  expect(definitionAdminNames()).toBe('')
  // The released built-in workspace policy also contains this check, although
  // it is not an additional choice in the approved Add menu.
  expect(ruleLabel({id:'lesson_recorded'})).toBe('Lesson recorded')
  expect(ruleLabel({id:'lesson_recorded'},'de')).toBe('Lehre festgehalten')
  expect(ruleIcon({id:'lesson_recorded'})).toBe('book')
  expect(definitionBlocks('de').map(block => [block.name,block.to])).toEqual([['Bereit','In Arbeit'],['Qualitätsprüfung','Qualitätsprüfung'],['Erledigt','Erledigt']])
  // Risk: translated navigation prose introduces a text icon and breaks the
  // global glyph guard. Name the settings location in plain German instead.
  expect(DEFINITION_EXPLAINERS_DE.definitions.who).toBe('Administratoren legen den Workspace-Standard in den Einstellungen unter Richtlinien fest. Projektadministratoren können einem Projekt eine eigene Definition geben.')
  expect(ruleWords({id:'manual:check',label:'Prüfung',kinds:['work'],labels:['ui'],fallback_owner_id:'ada'},[{id:'ada',name:'Ada'}],'de')).toBe('Alle Tickets · Arten: work · Labels: ui · Bestätigt von der ticketverantwortlichen Person, sonst Ada')
  expect(resetChanges({in_progress:[{id:'design_attached'},{id:'estimate_set',condition:'code'}]}, {in_progress:[{id:'estimate_set'},{id:'acceptance_present'}]}, 'de')).toEqual([
    'Entfällt: Freigegebenes Design angehängt.', 'Wird wieder aufgenommen: Abnahmekriterien beschrieben.', 'Verwendet die Workspace-Version: Schätzung gesetzt.',
  ])
  expect(resetChanges({}, {}, 'de')).toEqual(['Die Prüfungen bleiben gleich; künftige Workspace-Änderungen gelten hier sofort.'])
  expect(resetChanges({in_progress:[{id:'acceptance_present'},{id:'estimate_set'}]}, {in_progress:[{id:'estimate_set'},{id:'acceptance_present'}]}, 'de')).toEqual(['Bereit übernimmt wieder die Reihenfolge des Workspace-Standards.'])
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
  // These are url.PathEscape outputs, including characters Go leaves literal
  // while encodeURIComponent escapes them. Delimiters remain inside one key.
  for (const [escaped,key] of [['PRJ%20%C3%9C%2F26%3F@+&','PRJ Ü/26?@+&'],['A:B=C$D','A:B=C$D'],['A%23B%25C','A#B%C'],['%E6%97%A5%E6%9C%AC','日本']]) {
    expect(deskLinkOut({kind:'definition_change',href:`/p/${escaped}/settings/definitions?definition_changes=1#definition-changes`})).toBe(`/p/${encodeURIComponent(key!)}/settings/definitions?definition_changes=1#definition-changes`)
  }
  expect(deskItemID({kind:'definition_change',id:'copy-id'})).toBe('d:copy-id')
  for (const href of ['https://example.com', '//example.com','/p/PRJ-26/settings/access', '/p/PRJ-26/settings/definitions?definition_changes=1#other', '/p/A/B/settings/definitions?definition_changes=1#definition-changes', '/p/%ZZ/settings/definitions?definition_changes=1#definition-changes', '/p/%2e%2e/settings/definitions?definition_changes=1#definition-changes']) expect(deskLinkOut({kind:'definition_change',href})).toBe('')
})
