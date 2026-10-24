// SPDX-License-Identifier: AGPL-3.0-only
import test from 'node:test'
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { createHash } from 'node:crypto'
import { checkRepository, requiredRisks, validateLedger, validateVisual } from '../../scripts/test-prune/check.mjs'

const read = (path: string) => readFileSync(new URL(`../../${path}`, import.meta.url))
const json = (path: string) => JSON.parse(read(path).toString())
const key = (row: { kind: string; package?: string; file?: string; name: string; occurrence?: number }) =>
  row.kind === 'go' ? `${row.package}:${row.name}` :
    `${row.kind}:${row.file}:${row.name}${row.occurrence === undefined ? '' : `#${row.occurrence}`}`

// Risk: consolidating a protected historical identity or mixed delete candidate
// could silently discard its safety assertion, even by deleting its ledger row.
test('test-prune ledger rejects removed risks without lineage and keeps P01 execution frozen', () => {
  const baseline = json('scripts/test-prune/baseline.json')
  // Generic delete-candidate tags also exist on units outside the browser
  // prune plan. They must not inflate its lineage or hide explicit unit drift.
  for (const kind of ['node', 'vitest']) {
    assert.ok(baseline.catalogue.some(
      (row: { kind: string; tags?: string[] }) => row.kind === kind && row.tags?.includes('delete-candidate')),
    `The regression fixture must retain tagged ${kind} units`)
  }
  const required = requiredRisks(baseline)
  const candidates = required.filter((row: { id: string }) => row.id.startsWith('candidate-case:'))
  assert.equal(candidates.length, 155, 'The audited browser candidate population must remain exact')
  assert.ok(candidates.every((row: { source: { kind: string } }) => row.source.kind === 'browser'),
    'Generic unit candidate tags are outside the browser prune plan')
  const runtimeDrift = baseline.driftObligations.cases.filter((row: { kind: string }) => row.kind !== 'browser')
  assert.equal(runtimeDrift.length, 4, 'The regression fixture must retain all explicit runtime drift obligations')
  for (const source of runtimeDrift) {
    assert.deepEqual(required.find((row: { id: string }) => row.id === `drift:${key(source)}`)?.source, source,
      'Explicit runtime drift remains protected alongside browser candidates')
  }
  const summary = checkRepository()
  assert.deepEqual(summary, { browserCases: 5318, specs: 378, core: 29, protectedRisks: 647,
    driftCases: 455, candidateGroups: 31, candidateCases: 155, visualCases: 80,
    visualApproval: 'approved', phase: json('scripts/test-prune/risk-ledger.json').phase, cuts: false })
  const original = {
    baseline,
    ledger: json('scripts/test-prune/risk-ledger.json'),
    visual: json('scripts/test-prune/visual-manifest.json'),
    currentWeb: json('scripts/ci/web-test-tiers.json'), currentGo: json('scripts/ci/go-test-tiers.json'),
  }
  const staged = json('scripts/test-prune/p02-execution.json')
  assert.equal(staged.cuts, false)
  const p02Owners = new Set(original.ledger.specs.filter((row: { package: string }) => row.package === 'P02').map((row: { file: string }) => row.file))
  const p02Originals = original.baseline.catalogue.filter((row: { kind: string; file: string }) => row.kind === 'browser' && p02Owners.has(row.file))
  assert.deepEqual(staged.originalCaseDestinations.flatMap((row: { oldNativeKeys: string[] }) => row.oldNativeKeys).sort(),
    p02Originals.map((row: { key: string }) => row.key).sort(), 'Staged P02 mapping cannot drop an original identity')
  const currentKeys = new Set(original.currentWeb.tests.map(key))
  assert.ok(staged.candidateNativeKeys.every((id: string) => currentKeys.has(id)), 'Every staged destination has a real native registration')
  for (const core of original.ledger.core.filter((row: { package: string }) => row.package === 'P02')) {
    assert.ok(currentKeys.has(core.nativeBinding.key), `P02 core registration missing: ${core.id}`)
  }
  const sourceHash = (path: string) => createHash('sha256').update(read(path)).digest('hex')
  const frozenHash = (path: string) => baseline.sourcePins.find((pin: { path: string }) => pin.path === path).sha256
  const p01Snapshot = () => ({
    ...structuredClone(original),
    ledger: { ...structuredClone(original.ledger), phase: 'P01' },
    currentWeb: { ...structuredClone(baseline.cataloguePolicy), tests: baseline.catalogue.map((row: { key: string }) => {
      const { key: ignored, ...entry } = row
      return entry
    }) },
    sourceHash: frozenHash,
  })
  // Risk: pruning the old tours before Tick's replacement fixtures are encoded
  // would lose the central workspace/start surfaces and Goals profile coverage.
  for (const [id, fixture] of [
    ['ticket-workspace', 'Long title and key; properties panel; attachment strip with one preview and its focus ring; read-only checklist; chat dock open'],
    ['start-agent', 'Run once mode; task filled; "Create a ticket in a project"; read-only; folded "Runs on …" row with a capacity warning'],
  ]) {
    const state = original.visual.states.find((row: { id: string }) => row.id === id)
    assert.ok(state, `Tick's replacement state must be present: ${id}`)
    assert.equal(state.fixture, fixture)
    assert.equal(state.owner, `tests/visual/${id}.spec.ts`)
    assert.deepEqual(state.checkpoints.map((row: { id: string }) => row.id),
      [390, 1440].flatMap(width => ['light', 'dark'].map(theme => `V-${id}-${width}-${theme}`)))
    const destination = original.ledger.destinations.find((row: { id: string }) => row.id === `visual:${id}`)
    assert.equal(destination?.state, id)
    assert.equal(destination?.owner, state.owner)
    assert.equal(destination?.risk, fixture)
  }
  const goals = original.visual.states.find((row: { id: string }) => row.id === 'goals')
  assert.equal(goals.referenceStatus, 'deferred')
  assert.deepEqual(goals.deferredUntil, ['AEON-1621', 'AEON-1625'])
  assert.match(goals.deferredReason, /v6 canvas/)
  assert.deepEqual(goals.fixturesByProfile, {
    phone: 'Simple; 56 px rows; candy bar frozen by reduced motion',
    desktop: 'Expert; one goal expanded; pace badges; a late Lands with "at this pace"; numbered steps with stage tracks; waits marker',
  })
  const genericGoals = structuredClone(original.visual)
  delete genericGoals.states.find((row: { id: string }) => row.id === 'goals').fixturesByProfile
  assert.throws(() => validateVisual(genericGoals), /Missing Goals fixtures by profile/)
  for (const retired of ['theme-editor', 'releases']) {
    assert.ok(!original.visual.states.some((row: { id: string }) => row.id === retired))
    assert.ok(!original.ledger.destinations.some((row: { id: string }) => row.id === `visual:${retired}`))
  }

  // Risk: an unrelated unit rename/removal must not turn the standing lineage
  // check into a visual-approval gate for the entire frozen catalogue.
  const protectedKeys = new Set(original.ledger.risks.flatMap((row: { currentKeys: string[] }) => row.currentKeys))
  const unrelated = original.currentWeb.tests.find((row: Parameters<typeof key>[0]) =>
    row.kind === 'vitest' && row.file === 'tests/access-keys.unit.test.ts' && !protectedKeys.has(key(row)))
  assert.ok(unrelated, 'The counterexample must use an unprotected unit registration')
  for (const operation of ['rename', 'remove']) {
    const edited = structuredClone(original)
    const index = edited.currentWeb.tests.findIndex((row: Parameters<typeof key>[0]) => key(row) === key(unrelated))
    assert.notEqual(index, -1)
    if (operation === 'rename') edited.currentWeb.tests[index].name += ' (renamed without changing its risk)'
    else edited.currentWeb.tests.splice(index, 1)
    assert.equal(validateLedger({ ...edited, sourceHash }).cuts, false,
      `An unprotected unit ${operation} must pass without Tick references`)
    // Start from a valid P01 snapshot: an advanced phase or an earlier cut
    // must not satisfy this negative control for the wrong reason.
    const snapshot = p01Snapshot()
    assert.equal(validateLedger({ ...snapshot, p01: true }).cuts, false)
    const frozenIndex = snapshot.currentWeb.tests.findIndex((row: Parameters<typeof key>[0]) => key(row) === key(unrelated))
    assert.notEqual(frozenIndex, -1)
    if (operation === 'rename') snapshot.currentWeb.tests[frozenIndex].name += ' (renamed without changing its risk)'
    else snapshot.currentWeb.tests.splice(frozenIndex, 1)
    assert.throws(() => validateLedger({ ...snapshot, p01: true }), {
      message: 'P01 cannot remove or demote any existing test execution',
    })
  }
  const renamedProtected = structuredClone(original)
  const protectedRisk = renamedProtected.ledger.risks[0]
  const anchor = renamedProtected.currentWeb.tests.find((row: Parameters<typeof key>[0]) => key(row) === protectedRisk.currentKeys[0])
  assert.ok(anchor, 'The protected rename must start from a current risk anchor')
  anchor.name += ' (renamed protected anchor)'
  assert.throws(() => validateLedger({ ...renamedProtected, sourceHash }), {
    message: `Removed risk has no verified destination: ${protectedRisk.id} -> ${protectedRisk.destinations[0]}`,
  })
  const fixture = json('scripts/test-prune/testdata/missing-destination.json')
  const missing = structuredClone(original)
  const risk = missing.ledger.risks.find((row: { id: string }) => row.id === fixture.riskId)
  assert.ok(risk, 'The negative control must retain the exact protected person-switch fixture')
  risk.destinations = fixture.destinations
  if (fixture.removeCurrentAnchors) missing.currentWeb.tests = missing.currentWeb.tests.filter(
    (row: Parameters<typeof key>[0]) => !risk.currentKeys.includes(key(row)))
  assert.throws(() => validateLedger({ ...missing, sourceHash }), {
    message: fixture.expectedErrorPrefix + fixture.riskId,
  })
  for (const prefix of ['drift:', 'go-drift:', 'candidate-group:', 'candidate-case:']) {
    const omitted = structuredClone(original)
    const index = omitted.ledger.risks.findIndex((row: { id: string }) => row.id.startsWith(prefix))
    assert.notEqual(index, -1)
    omitted.ledger.risks.splice(index, 1)
    assert.throws(() => validateLedger({ ...omitted, sourceHash }), /Missing\/extra protected risk lineage rows/)
  }
  for (const source of runtimeDrift) {
    const omitted = structuredClone(original)
    const index = omitted.ledger.risks.findIndex((row: { id: string }) => row.id === `drift:${key(source)}`)
    assert.notEqual(index, -1, 'The ledger must retain the exact runtime drift obligation')
    omitted.ledger.risks.splice(index, 1)
    assert.throws(() => validateLedger({ ...omitted, sourceHash }), /Missing\/extra protected risk lineage rows/)
  }
  const unknown = structuredClone(original)
  unknown.ledger.risks[0].destinations = ['runtime:nonexistent-replacement']
  assert.throws(() => validateLedger({ ...unknown, sourceHash }), /Unknown destination runtime:nonexistent-replacement/)
  const cut = structuredClone(original)
  const first = cut.ledger.risks[0].currentKeys[0]
  cut.currentWeb.tests = cut.currentWeb.tests.filter((row: Parameters<typeof key>[0]) => key(row) !== first)
  assert.throws(() => validateLedger({ ...cut, sourceHash }), /Removed risk has no verified destination/)
  const demoted = structuredClone(original)
  const protectedKey = demoted.ledger.risks.flatMap((row: { currentKeys: string[] }) => row.currentKeys).find(
    (id: string) => demoted.baseline.catalogue.some((row: { key: string; tier: string }) => row.key === id && row.tier !== 'NIGHTLY'))
  assert.ok(protectedKey)
  const protectedRow = demoted.currentWeb.tests.find((row: Parameters<typeof key>[0]) => key(row) === protectedKey)
  assert.notEqual(protectedRow.tier, 'NIGHTLY', 'The demotion counterexample must start in the full gate')
  protectedRow.tier = 'NIGHTLY'
  assert.throws(() => validateLedger({ ...demoted, sourceHash }), /Removed risk has no verified destination/)

  // A real protected Agents identity isolates per-area authority. Its reference
  // set must be complete, reviewed and available; unrelated pending Projects
  // and deferred Goals/quality fixes must never block that cut.
  const areaCut = structuredClone(original)
  areaCut.visual.comparison.mode = 'blocking'
  for (const state of areaCut.visual.states) if (state.id !== 'agents')
    for (const checkpoint of state.checkpoints) checkpoint.reference = { attachmentMember: null, sha256: null }
  const areaRisk = areaCut.ledger.risks.find((row: { source: { file: string } }) => row.source.file === 'tests/agents-phone-rows.spec.ts')
  assert.deepEqual(areaRisk.destinations, ['visual:agents'])
  const removedKey = areaRisk.currentKeys[0]
  areaCut.currentWeb.tests = areaCut.currentWeb.tests.filter((row: Parameters<typeof key>[0]) => key(row) !== removedKey)
  const impactedDestinations = new Set(areaCut.ledger.risks.filter((row: { currentKeys: string[] }) => row.currentKeys.includes(removedKey))
    .flatMap((row: { destinations: string[] }) => row.destinations))
  assert.deepEqual([...impactedDestinations], ['visual:agents'])
  for (const destination of areaCut.ledger.destinations) if (impactedDestinations.has(destination.id))
    destination.verification = { status: 'verified', evidence: 'Synthetic authority counterexample, never a product approval' }
  areaCut.visual.approval = { reviewer: 'Tick', status: 'approved', approvedAt: 'synthetic-proof', evidence: 'Synthetic proof only' }
  areaCut.visual.baselineAuthority.status = 'approved'
  areaCut.visual.renderer.runner.imageVersion = 'synthetic-image'
  Object.assign(areaCut.visual.baselineAuthority.source, { attachmentId: 'synthetic-only', sha256: 'a'.repeat(64) })
  areaCut.visual.baselineAuthority.retention.verifiedAt = 'synthetic-proof'
  const area = areaCut.visual.states.find((row: { id: string }) => row.id === 'agents')
  for (const checkpoint of area.checkpoints) {
    delete checkpoint.referenceStatus; delete checkpoint.deferredUntil; delete checkpoint.deferredReason
    checkpoint.reference = { attachmentMember: `${checkpoint.id}.png`, sha256: 'b'.repeat(64) }
  }
  assert.equal(validateLedger({ ...areaCut, sourceHash }).cuts, true,
    'A pinned Agents cut passes while unrelated Projects is unpinned and Goals is deferred')
  const p01AreaCut = { ...areaCut, ledger: { ...areaCut.ledger, phase: 'P01' } }
  assert.throws(() => validateLedger({ ...p01AreaCut, sourceHash, p01: true }), /P01 cannot remove or demote/)
  const unpinnedArea = structuredClone(areaCut)
  unpinnedArea.visual.states.find((row: { id: string }) => row.id === 'agents').checkpoints[1].reference.sha256 = null
  assert.throws(() => validateLedger({ ...unpinnedArea, sourceHash }), { message: 'Unpinned visual reference: V-agents-390-dark' })
  const deferredArea = structuredClone(areaCut)
  Object.assign(deferredArea.visual.states.find((row: { id: string }) => row.id === 'agents'), {
    referenceStatus: 'deferred', deferredUntil: ['AEON-1687'], deferredReason: 'Synthetic required-area refusal',
  })
  assert.throws(() => validateLedger({ ...deferredArea, sourceHash }), { message: 'Deferred visual reference: V-agents-390-light (AEON-1687)' })
  const unapprovedArea = structuredClone(areaCut)
  unapprovedArea.visual.approval.status = 'pending'
  assert.throws(() => validateLedger({ ...unapprovedArea, sourceHash }), /Cuts require Tick approval/)
  const unavailableArea = structuredClone(areaCut)
  unavailableArea.visual.baselineAuthority.retention.verifiedAt = null
  assert.throws(() => validateLedger({ ...unavailableArea, sourceHash }), /approved, pinned and available/)
  assert.throws(() => validateLedger({ ...original, sourceHash: () => '0'.repeat(64) }), /Frozen historical-evidence changed/)
  // Snapshot enforcement is explicit: future assertion additions and refreshed
  // timings must not be blocked by P01's one-time no-execution-change check.
  const frozen = p01Snapshot()
  assert.equal(validateLedger({ ...frozen, sourceHash: frozenHash, p01: true }).cuts, false)
  assert.throws(() => validateLedger({ ...frozen, sourceHash: () => '0'.repeat(64), p01: true }), /Frozen launch-policy changed/)
  frozen.currentWeb.defaultNewTier = 'ESSENTIAL'
  assert.throws(() => validateLedger({ ...frozen, sourceHash: frozenHash, p01: true }), /P01 changed catalogue selection\/tagging\/timing policy/)
  const reportOnly = structuredClone(original.visual)
  reportOnly.comparison.mode = 'report-only'
  for (const state of reportOnly.states) for (const checkpoint of state.checkpoints)
    checkpoint.reference = { attachmentMember: null, sha256: null }
  assert.throws(() => validateVisual(reportOnly, { cuts: true, stateIds: ['goals'] }), /Protected visual cuts stay closed/)
  assert.throws(() => validateVisual(reportOnly, { cuts: true, checkpointIds: ['V-projects-390-light'] }), /Protected visual cuts stay closed/)
  assert.throws(() => validateVisual(original.visual, { cuts: true, stateIds: ['goals'] }), /Protected visual cuts stay closed/)
  assert.throws(() => validateVisual(original.visual, { cuts: true, checkpointIds: ['V-projects-390-light'] }), /Protected visual cuts stay closed/)
  // Preserve the per-checkpoint approval and deferral proof independently of
  // the live report-only switch while cross-machine renderer proof is pending.
  const approvedBlocking = structuredClone(original.visual)
  approvedBlocking.comparison.mode = 'blocking'
  assert.throws(() => validateVisual(approvedBlocking, { cuts: true, stateIds: ['goals'] }),
    { message: 'Deferred visual reference: V-goals-390-light (AEON-1621, AEON-1625)' })
  assert.equal(validateVisual(approvedBlocking, { cuts: true, checkpointIds: ['V-projects-390-light'] }), 80)
  const missingCheckpoint = structuredClone(original.visual)
  missingCheckpoint.states[0].checkpoints.pop()
  assert.throws(() => validateVisual(missingCheckpoint), /Missing\/duplicate visual checkpoint/)
})

test('P11 pruning rejects missing original-case mappings and dangling current destinations', () => {
  const baseline = json('scripts/test-prune/baseline.json')
  const ledger = json('scripts/test-prune/risk-ledger.json')
  const execution = json('scripts/test-prune/p11-execution.json')
  const owners = new Set<string>(ledger.specs.filter((row: { package: string }) => row.package === 'P11').map((row: { file: string }) => row.file))
  const originals = baseline.catalogue.filter((row: { kind: string; file: string }) => row.kind === 'browser' && owners.has(row.file))
  const current = new Map<string, Parameters<typeof key>[0]>(json('scripts/ci/web-test-tiers.json').tests.map((row: Parameters<typeof key>[0]) => [key(row), row]))
  const protectedKeys = new Set(ledger.risks.flatMap((row: { currentKeys: string[] }) => row.currentKeys))
  type Mapping = { source: string; disposition: string; destinations: string[]; approval?: string }
  type Execution = {
    baselineSha256: string; actualAfter: number; unchangedSpecs: string[]; upstreamAdditionalCases: string[]
    postPruneAdditionalCases: string[]
    specs: { file: string; before: number; actualAfter: number; replacement: string; mappings: Mapping[] }[]
    cutA: { beforeBrowserCases: number; afterBrowserCases: number; removedBrowserCases: number; mappings: (Mapping & { file: string })[]; groups: { file: string; before: number; after: number; removed: number }[] }
  }
  const validate = (proof: Execution) => {
    assert.equal(proof.baselineSha256, ledger.baselineSha256)
    const files = [...proof.unchangedSpecs, ...proof.specs.map(row => row.file)]
    assert.equal(new Set(files).size, files.length, 'Duplicate P11 source owner')
    assert.deepEqual([...new Set(files)].sort(), [...owners].sort(), 'Missing P11 source owner')
    for (const group of proof.specs) {
      const source = originals.filter((row: { file: string }) => row.file === group.file)
      assert.equal(group.before, source.length)
      assert.equal(new Set(group.mappings.map(row => row.source)).size, group.mappings.length, 'Duplicate P11 original mapping')
      assert.deepEqual(group.mappings.map(row => row.source).sort(), source.map(key).sort(), 'Missing P11 original mapping')
      assert.ok(current.has(group.replacement), 'Missing P11 replacement identity')
      for (const mapping of group.mappings) {
        assert.ok(mapping.destinations.length > 0 && mapping.destinations.every(id => current.has(id)), 'Missing P11 current destination')
        if (mapping.disposition === 'retained') assert.deepEqual(mapping.destinations, [mapping.source])
        else {
          assert.ok(!protectedKeys.has(mapping.source), 'P11 protected cuts remain closed')
          assert.ok(!current.has(mapping.source), 'A consolidated original must leave the catalogue')
          if (mapping.disposition === 'approved-retirement') assert.equal(mapping.approval, ledger.approval.id)
          else assert.equal(mapping.disposition, 'consolidated')
        }
      }
      assert.equal(group.actualAfter, [...current.values()].filter((row: Parameters<typeof key>[0]) => row.kind === 'browser' && row.file === group.file).length)
    }
    for (const row of originals.filter((row: { file: string }) => proof.unchangedSpecs.includes(row.file))) {
      const { key: ignored, ...entry } = row
      assert.deepEqual(current.get(key(row)), entry, 'An unmapped P11 original changed')
    }
    assert.equal(proof.actualAfter, [...current.values()].filter(row => row.kind === 'browser' && row.file !== undefined && owners.has(row.file)).length)
    // Main added cases after P01. Derive this round's complete removal set
    // from those inputs as well, so a missing new-main mapping cannot hide
    // behind the frozen catalogue's original-only coverage.
    const knownSources = [...originals.map(key), ...proof.upstreamAdditionalCases]
    // Later regressions are live inventory, not part of cutA's historical
    // before/after measurement. Retain exact identities so an addition cannot
    // conceal a removed original or inflate the recorded pruning saving.
    const additions = new Set(proof.postPruneAdditionalCases)
    assert.equal(additions.size, proof.postPruneAdditionalCases.length, 'Duplicate post-prune case')
    for (const id of additions) {
      const row = current.get(id)
      assert.ok(row?.kind === 'browser' && row.file !== undefined && owners.has(row.file), 'Missing post-prune current destination')
      assert.ok(!knownSources.includes(id), 'A post-prune case must not replace a known source')
    }
    const historicalCurrent = [...current.values()].filter(row => !additions.has(key(row)))
    const removed = knownSources.filter(id => !current.has(id) && proof.cutA.groups.some(group => id.startsWith(`browser:${group.file}:`)))
    assert.deepEqual(proof.cutA.mappings.map(row => row.source).sort(), removed.sort(), 'Missing cutA original mapping')
    assert.equal(new Set(proof.cutA.groups.map(row => row.file)).size, proof.cutA.groups.length, 'Duplicate cutA owner')
    for (const mapping of proof.cutA.mappings) {
      assert.ok(mapping.source.startsWith(`browser:${mapping.file}:`), 'Incorrect cutA source owner')
      assert.ok(!protectedKeys.has(mapping.source), 'P11 protected cuts remain closed')
      assert.ok(mapping.destinations.length > 0 && mapping.destinations.every(id => current.has(id)), 'Missing P11 current destination')
    }
    for (const group of proof.cutA.groups) {
      assert.equal(group.before, knownSources.filter(id => id.startsWith(`browser:${group.file}:`)).length)
      assert.equal(group.after, historicalCurrent.filter(row => row.kind === 'browser' && row.file === group.file).length,
        `Unrecorded post-prune cases: ${group.file}`)
      assert.equal(group.removed, group.before - group.after)
    }
    assert.equal(proof.cutA.removedBrowserCases, removed.length)
    assert.equal(proof.cutA.beforeBrowserCases - proof.cutA.afterBrowserCases, removed.length)
    assert.equal(proof.cutA.afterBrowserCases + additions.size, proof.actualAfter)
  }
  validate(execution)
  const missing = structuredClone(execution)
  missing.specs[0].mappings.pop()
  assert.throws(() => validate(missing), /Missing P11 original mapping/)
  const dangling = structuredClone(execution)
  dangling.specs[0].mappings[0].destinations = ['browser:tests/status-icons.spec.ts:missing P11 replacement']
  assert.throws(() => validate(dangling), /Missing P11 current destination/)
  const missingNewMain = structuredClone(execution)
  const extra = missingNewMain.cutA.mappings.findIndex((row: Mapping) => missingNewMain.upstreamAdditionalCases.includes(row.source))
  assert.notEqual(extra, -1, 'The negative control must retain a new-main cut')
  missingNewMain.cutA.mappings.splice(extra, 1)
  assert.throws(() => validate(missingNewMain), /Missing cutA original mapping/)
  const additionKey = 'browser:tests/delivery-numbers.spec.ts:AEON-1710: history failure pills stay on one line and expose the full reason'
  const missingAddition = structuredClone(execution)
  assert.ok(missingAddition.postPruneAdditionalCases.includes(additionKey), 'Retain the AEON-1710 post-prune regression fixture')
  missingAddition.postPruneAdditionalCases = missingAddition.postPruneAdditionalCases.filter((id: string) => id !== additionKey)
  assert.throws(() => validate(missingAddition), /Unrecorded post-prune cases: tests\/delivery-numbers.spec.ts/)
  const danglingAddition = structuredClone(execution)
  danglingAddition.postPruneAdditionalCases[0] = 'browser:tests/delivery-numbers.spec.ts:missing AEON-1710 regression'
  assert.throws(() => validate(danglingAddition), /Missing post-prune current destination/)
  const duplicateAddition = structuredClone(execution)
  duplicateAddition.postPruneAdditionalCases.push(additionKey)
  assert.throws(() => validate(duplicateAddition), /Duplicate post-prune case/)
  const disguisedOriginal = structuredClone(execution)
  disguisedOriginal.postPruneAdditionalCases = [originals.find((row: Parameters<typeof key>[0]) => current.has(key(row))).key]
  assert.throws(() => validate(disguisedOriginal), /A post-prune case must not replace a known source/)
})
