// SPDX-License-Identifier: AGPL-3.0-only
import { createHash } from 'node:crypto'
import { readFileSync, statSync } from 'node:fs'
import { resolve } from 'node:path'
import { fileURLToPath, pathToFileURL } from 'node:url'
import { assertRenderer, visualClock } from './visual-renderer.mjs'

const root = fileURLToPath(new URL('../../', import.meta.url))
const rank = { NIGHTLY: 0, 'GATED-FULL': 1, ESSENTIAL: 2 }
const sha256 = value => createHash('sha256').update(value).digest('hex')
const identity = row => row.kind === 'go' ? `${row.package}:${row.name}` :
  `${row.kind}:${row.file}:${row.name}${row.occurrence === undefined ? '' : `#${row.occurrence}`}`
const same = (a, b) => JSON.stringify(a) === JSON.stringify(b)
const need = (condition, message) => { if (!condition) throw new Error(message) }
const text = value => typeof value === 'string' && value.trim().length > 0
const digest = value => /^[a-f0-9]{64}$/.test(value ?? '')

export function visualReportOnly(visual) {
  need(['report-only', 'blocking'].includes(visual.comparison?.mode), 'Missing visual comparison mode')
  return visual.comparison.mode === 'report-only'
}

// Both the browser runner and comparator use this policy. A report retains the
// actual failure; switching to blocking rethrows that same failure unchanged.
export async function visualCheckResult(visual, run) {
  const reportOnly = visualReportOnly(visual)
  try { return { status: 'passed', value: await run() } }
  catch (error) {
    if (!reportOnly) throw error
    return { status: 'failed', error: String(error) }
  }
}

function indexed(rows, name) {
  need(Array.isArray(rows) && rows.length <= 20_000, `Invalid ${name} rows`)
  const result = new Map()
  for (const row of rows) {
    need(text(row.id) && !result.has(row.id), `Invalid/duplicate ${name} identity: ${row.id}`)
    result.set(row.id, row)
  }
  return result
}

// Derive obligations independently of the editable mapping. Deleting a ledger
// row, including an already unresolved historical title, cannot erase its risk.
export function requiredRisks(baseline) {
  return [
    ...baseline.driftObligations.cases.map(source => ({ id: `drift:${identity(source)}`, source })),
    ...baseline.driftObligations.goKeys.map(key => {
      const split = key.indexOf(':')
      return { id: `go-drift:${key}`, source: { kind: 'go', package: key.slice(0, split), name: key.slice(split + 1) } }
    }),
    ...baseline.deleteCandidateGroups.map(group => ({ id: `candidate-group:${group.file}`, source: { kind: 'candidate-group', file: group.file } })),
    // The prune plan's candidate population is browser-only. Runtime drift
    // obligations remain protected independently by driftObligations above.
    ...baseline.catalogue.filter(row => row.kind === 'browser' && row.tags?.includes('delete-candidate')).map(row => ({
      id: `candidate-case:${identity(row)}`,
      source: { kind: row.kind, file: row.file, name: row.name, ...(row.occurrence === undefined ? {} : { occurrence: row.occurrence }) },
    })),
  ]
}

function currentAnchors(source, baseline) {
  if (source.kind === 'go') return [identity(source)]
  const candidates = baseline.catalogue.filter(row => row.file === source.file &&
    (source.kind === 'candidate-group' || row.kind === source.kind))
  if (source.kind === 'candidate-group') return candidates.map(identity)
  const matches = candidates.filter(row => row.name === source.name ||
    row.name.endsWith(` › ${source.name}`) || source.name.endsWith(` › ${row.name}`))
  if (source.occurrence !== undefined) return matches.filter(row => row.occurrence === source.occurrence).map(identity)
  // This mirrors the existing selector's fail-wide fallback for historical
  // titles that no longer resolve. Keep all current native occurrences.
  return (matches.length ? matches : candidates).map(identity)
}

export function validateVisual(visual, { cuts = false, stateIds = visual.states?.map(state => state.id), checkpointIds } = {}) {
  need(visual.version === 1, 'Invalid visual manifest version')
  const reportOnly = visualReportOnly(visual)
  const expected = ['projects', 'tickets', 'goals', 'agents', 'session-chat', 'decision-desk',
    'account-use-matrix', 'models', 'access', 'knowledge', 'delivery-numbers', 'hours',
    'quote-editor', 'public-quote', 'start-agent', 'agent-rules', 'capacity', 'ticket-workspace']
  const tablet = new Set(['agents', 'decision-desk', 'account-use-matrix', 'quote-editor'])
  need(same(visual.states?.map(row => row.id), expected), 'Visual state list must contain the exact 18 Tick-adjusted section-8 states')
  need(same(visual.profiles, { phone: { width: 390, height: 844 }, tablet: { width: 1024, height: 768 }, desktop: { width: 1440, height: 900 } }), 'Visual viewport matrix changed')
  need(same(visual.themes, ['light', 'dark']), 'Both visual themes are required')
  need(Array.isArray(stateIds) && new Set(stateIds).size === stateIds.length &&
    stateIds.every(id => expected.includes(id)), 'Unknown/duplicate required visual state')
  const required = new Set(cuts ? stateIds : [])
  if (checkpointIds !== undefined) need(Array.isArray(checkpointIds) && new Set(checkpointIds).size === checkpointIds.length &&
    checkpointIds.every(id => visual.states.some(state => state.checkpoints?.some(checkpoint => checkpoint.id === id))),
  'Unknown/duplicate required visual checkpoint')
  const requiredCheckpoints = new Set(cuts ? checkpointIds : [])
  if (required.size || requiredCheckpoints.size) need(!reportOnly,
    'Protected visual cuts stay closed until reference set 3 is pinned and comparisons are blocking')
  const checkpoints = new Set()
  for (const state of visual.states) {
    visualClock(visual, state.id)
    const profiles = tablet.has(state.id) ? ['phone', 'tablet', 'desktop'] : ['phone', 'desktop']
    need(state.owner === `tests/visual/${state.id}.spec.ts` && text(state.fixture), `Missing independently owned visual fixture: ${state.id}`)
    need(same(state.profiles, profiles) && same(state.themes, visual.themes), `Unexpected visual matrix: ${state.id}`)
    if (state.id === 'goals') need(same(Object.keys(state.fixturesByProfile ?? {}).sort(), [...profiles].sort()) &&
      profiles.every(profile => text(state.fixturesByProfile[profile])), 'Missing Goals fixtures by profile')
    if (state.referenceStatus !== undefined) need(state.referenceStatus === 'deferred' &&
      state.deferredUntil?.length > 0 && state.deferredUntil.every(ticket => /^AEON-\d+$/.test(ticket)) &&
      text(state.deferredReason), `Missing visual deferral authority: ${state.id}`)
    const expectedCases = profiles.flatMap(profile => visual.themes.map(theme => `V-${state.id}-${visual.profiles[profile].width}-${theme}`))
    need(same(state.checkpoints?.map(row => row.id), expectedCases), `Missing/duplicate visual checkpoint: ${state.id}`)
    for (const checkpoint of state.checkpoints) {
      need(!checkpoints.has(checkpoint.id) && checkpoint.id === `V-${state.id}-${visual.profiles[checkpoint.profile]?.width}-${checkpoint.theme}`, `Invalid visual checkpoint: ${checkpoint.id}`)
      checkpoints.add(checkpoint.id)
      if (checkpoint.referenceStatus !== undefined) need(['deferred', 'needs-re-capture'].includes(checkpoint.referenceStatus) &&
        checkpoint.deferredUntil?.length > 0 && checkpoint.deferredUntil.every(ticket => /^AEON-\d+$/.test(ticket)) &&
        text(checkpoint.deferredReason) && !checkpoint.reference?.attachmentMember && !checkpoint.reference?.sha256,
      `Missing checkpoint deferral authority or deferred reference is pinned: ${checkpoint.id}`)
      if (checkpoint.referenceStatus === 'needs-re-capture') need(same(checkpoint.deferredUntil, ['AEON-1692']) &&
        !visual.approval.states?.includes(state.id) && !visual.approval.checkpoints?.includes(checkpoint.id) && state.approval?.decision !== 'ok-to-pin',
      `Stale visual reference still has comparison authority: ${checkpoint.id}`)
      if (checkpointIds === undefined ? required.has(state.id) : requiredCheckpoints.has(checkpoint.id)) {
        const deferred = checkpoint.referenceStatus === 'deferred' ? checkpoint : state
        need(deferred.referenceStatus !== 'deferred', `Deferred visual reference: ${checkpoint.id} (${deferred.deferredUntil?.join(', ')})`)
        need(checkpoint.reference?.attachmentMember === `${checkpoint.id}.png` && digest(checkpoint.reference.sha256), `Unpinned visual reference: ${checkpoint.id}`)
      }
    }
  }
  need(checkpoints.size === 80, 'Visual budget must be exactly 80 checkpoints')
  const requirements = visual.requirements
  for (const field of ['actualImageComparison', 'axe', 'rejectClipping', 'rejectOverlap', 'rejectConsoleFailures',
    'waitForFonts', 'explicitApplicationReadiness', 'frozenFixtureClockAndData', 'reducedMotion', 'separateInteractionProof'])
    need(requirements?.[field] === true, `Missing visual requirement: ${field}`)
  need(requirements.maskAssessedContent === false && requirements.fullPageTour === false &&
    requirements.controlStabilityTolerancePx === 0.5 && requirements.captures === 'testInfo.outputPath()', 'Visual capture/stability ownership changed')
  const renderer = visual.renderer
  need(renderer?.runner?.label === 'ubuntu-24.04' && renderer.runner.imageOS === 'ubuntu24' &&
    renderer.runner.platform === 'linux' && renderer.runner.arch === 'x64' && text(renderer.playwright) &&
    renderer.browser?.name === 'chromium-headless-shell' && text(renderer.browser.revision) && text(renderer.browser.version) &&
    renderer.browser.headless === true && same(renderer.browser.args, ['--disable-gpu']) &&
    renderer.deviceScaleFactor === 1 && same(renderer.colourScheme, ['light', 'dark']) && renderer.reducedMotion === 'reduce' &&
    renderer.timezone === 'Europe/Vienna' && renderer.locale === 'en-GB' && renderer.fonts?.length === 10 &&
    renderer.fonts[0].family === 'Inter Variable' && renderer.fonts[0].asset === 'web/tests/visual/fonts/inter-v4.1.ttf' &&
    renderer.fonts.every(font => digest(font.sha256) && text(font.asset) && text(font.license)) &&
    same(renderer.fontconfig?.aliases, { '-apple-system': 'Inter Variable', 'system-ui': 'Inter Variable', 'sans-serif': 'Inter Variable', monospace: 'JetBrains Mono' }), 'Missing deterministic hosted visual renderer policy')
  need(visual.comparison?.threshold === 0 && visual.comparison.maxDiffPixels === 0 && text(visual.comparison.justification),
    'Visual comparison requires exact pixels with a documented tolerance policy')
  need(visual.capture?.workflow === '.github/workflows/ci.yml' && visual.capture.event === 'workflow_dispatch' &&
    visual.capture.input === 'visual_capture' && visual.capture.job === 'visual-reference-capture' && visual.capture.retentionDays === 7 &&
    same(visual.capture.states, ['projects', 'access', 'knowledge', 'public-quote', 'agent-rules', 'start-agent',
      'quote-editor', 'session-chat', 'hours', 'capacity', 'tickets', 'agents', 'decision-desk',
      'account-use-matrix', 'delivery-numbers', 'models']), 'Manual visual capture must retain the exact sixteen-state review scope')
  const authority = visual.baselineAuthority
  need(authority?.onMissing === 'fail' && authority.onExpired === 'fail' && authority.onDigestMismatch === 'fail' &&
    authority.automaticRewrite === false && authority.currentRunMayBeReference === false, 'Visual baseline failures must fail closed')
  need(authority.source?.kind === 'existing-ppm-ticket-attachment' && authority.source.ticket === 'AEON-1670' &&
    text(authority.retention?.policy) && text(authority.retention.availability), 'Missing baseline retention/availability authority')
  if (checkpointIds === undefined ? required.size > 0 : requiredCheckpoints.size > 0) {
    need(visual.approval?.reviewer === 'Tick' && visual.approval.status === 'approved' &&
      text(visual.approval.evidence) && text(visual.approval.approvedAt), 'Cuts require Tick approval of visual fixtures and references')
    need(authority.status === 'approved' && text(authority.source.attachmentId) && digest(authority.source.sha256) &&
      text(authority.retention.verifiedAt), 'Cuts require an approved, pinned and available immutable baseline source')
    assertRenderer(renderer, renderer)
  }
  return checkpoints.size
}

export function validateLedger({ baseline, ledger, visual, currentWeb, currentGo, sourceHash, p01 = false }) {
  need(baseline.version === 1 && ledger.version === 1 && /^P(?:0[1-9]|1[0-2])$/.test(ledger.phase), 'Invalid prune ledger version/phase')
  const approval = ledger.approval
  need(approval?.id === 'AEON-1670-2026-10-22' && approval.ticket === 'AEON-1670' && approval.person === 'Markus' &&
    approval.approvedAt === '2026-10-22T22:15:00+02:00' && text(approval.scope), 'Missing person-approved retirement/demotion note')
  const destinations = indexed(ledger.destinations, 'destination')
  const risks = indexed(ledger.risks, 'risk')
  const current = new Map([...currentWeb.tests, ...currentGo.tests].map(row => [identity(row), row]))
  const original = new Map(baseline.catalogue.map(row => [identity(row), row]))
  const changed = key => !current.has(key) || (original.has(key) && rank[current.get(key).tier] < rank[original.get(key).tier])
  const references = (ids, label) => {
    need(Array.isArray(ids) && ids.length > 0, `Missing destination: ${label}`)
    need(new Set(ids).size === ids.length, `Duplicate destination: ${label}`)
    for (const id of ids) need(destinations.has(id), `Unknown destination ${id}: ${label}`)
  }
  for (const destination of destinations.values()) {
    need(['retained', 'runtime', 'visual', 'approved-retirement'].includes(destination.kind) &&
      text(destination.owner) && text(destination.risk) && /^P\d{2}$/.test(destination.package), `Invalid destination: ${destination.id}`)
    need(['planned', 'verified'].includes(destination.verification?.status), `Missing verification status: ${destination.id}`)
    if (destination.kind === 'approved-retirement') {
      need(destination.approval === approval.id, `Unapproved retirement: ${destination.id}`)
      references(destination.survivingDestinations, destination.id)
      need(destination.survivingDestinations.every(id => destinations.get(id).kind !== 'approved-retirement'), `Retirement cannot replace retirement: ${destination.id}`)
    }
    if (destination.kind === 'visual') need(visual.states.some(state => state.id === destination.state && state.owner === destination.owner), `Unknown visual destination: ${destination.id}`)
  }
  const specs = new Map(ledger.specs.map(row => [row.file, row]))
  const browser = baseline.catalogue.filter(row => row.kind === 'browser')
  const owners = new Set(browser.map(row => row.file))
  need(specs.size === owners.size && specs.size === ledger.specs.length, 'Missing/duplicate spec budget owner')
  for (const file of owners) {
    const spec = specs.get(file)
    need(spec && spec.before === browser.filter(row => row.file === file).length &&
      Number.isSafeInteger(spec.after) && spec.after >= 0 && text(spec.risk), `Missing/invalid spec budget: ${file}`)
    references(spec.destinations, file)
  }
  need(ledger.specs.reduce((n, row) => n + row.after, 0) === 338 &&
    ledger.budget?.proposedBrowserCap === 338 && ledger.budget.functional === 256 && ledger.budget.requestBounds === 2 &&
    ledger.budget.visual === 80 && ledger.budget.core === 29, 'Prune budget must remain 256 functional + 2 request-bound + 80 visual')
  need(ledger.core?.length === 29 && same(ledger.core.map(row => row.id), Array.from({ length: 29 }, (_, i) => `C${String(i + 1).padStart(2, '0')}`)), 'Core must contain exactly C01–C29')
  const coreOwners = ['shell', 'shell', 'shell', 'projects', 'project-list', 'ticket-panel', 'ticket-panel', 'record-ownership',
    'bulk', 'work-queue', 'agents', 'agent-pairing', 'session-chat', 'managed-controls', 'decision-desk', 'decision-desk',
    'access', 'guest', 'html-sandbox', 'quote-public', 'quotes/parity-flow', 'releases', 'theme-apply', 'role-picker-layout',
    'settings-shared', 'no-shift', 'no-shift', 'performance', 'performance']
  for (const [index, core] of ledger.core.entries()) {
    need(core.owner === `web/tests/${coreOwners[index]}.spec.ts` && text(core.risk) &&
      destinations.get(core.destination)?.owner === core.owner, `Invalid core owner/destination: ${core.id}`)
    need(ledger.core.filter(row => row.owner === core.owner).length <= specs.get(core.owner.slice(4)).after, `Core exceeds owner's budget: ${core.id}`)
  }
  const required = requiredRisks(baseline)
  const requiredVisualStates = new Set()
  const requireVisualDestination = destination => {
    if (destination.kind === 'visual') requiredVisualStates.add(destination.state)
    if (destination.kind === 'approved-retirement') for (const id of destination.survivingDestinations)
      requireVisualDestination(destinations.get(id))
  }
  need(risks.size === required.length, 'Missing/extra protected risk lineage rows')
  for (const obligation of required) {
    const risk = risks.get(obligation.id)
    need(risk, `Missing lineage: ${obligation.id}`)
    need(same(risk.source, obligation.source) && text(risk.risk), `Changed protected source: ${obligation.id}`)
    const anchors = currentAnchors(obligation.source, baseline)
    need(anchors.length > 0 && same(risk.currentKeys, anchors), `Missing/changed historical fallback: ${risk.id}`)
    references(risk.destinations, risk.id)
    if (anchors.some(changed)) for (const id of risk.destinations) {
      const replacement = destinations.get(id)
      need(replacement.verification.status === 'verified' && text(replacement.verification.evidence), `Removed risk has no verified destination: ${risk.id} -> ${id}`)
      requireVisualDestination(replacement)
      if (replacement.kind !== 'approved-retirement' && replacement.kind !== 'visual') need(
        replacement.verification.nativeKeys?.length > 0 && replacement.verification.nativeKeys.every(key => current.has(key)),
        `Replacement native identity missing: ${risk.id} -> ${id}`)
    }
  }
  // Standing enforcement follows protected risks. The whole catalogue is a
  // one-time P01 snapshot, not a visual-approval gate for unrelated unit edits.
  const cuts = required.some(row => currentAnchors(row.source, baseline).some(changed))
  if (p01) {
    need(ledger.phase === 'P01', 'The P01 snapshot check only applies to P01')
    need(!cuts && ![...original.keys()].some(changed), 'P01 cannot remove or demote any existing test execution')
    need(same(Object.fromEntries(Object.entries(currentWeb).filter(([key]) => key !== 'tests')), baseline.cataloguePolicy),
      'P01 changed catalogue selection/tagging/timing policy')
    for (const [key, before] of original) {
      const { key: ignored, ...row } = before
      need(same(current.get(key), row), `P01 changed an existing catalogue row: ${key}`)
    }
  }
  // Only the visual destinations protecting changed risks need reviewed
  // references. A deferred or unpinned area cannot block an unrelated cut.
  const visualCases = validateVisual(visual, { cuts, stateIds: [...requiredVisualStates] })
  for (const pin of baseline.sourcePins) if (pin.category === 'historical-evidence' ||
    p01 && ['launch-policy', 'launch-record', 'selector-policy', 'browser-source', 'current-obligations'].includes(pin.category))
    need(sourceHash(pin.path) === pin.sha256, `Frozen ${pin.category} changed: ${pin.path}`)
  return { browserCases: browser.length, specs: specs.size, core: ledger.core.length, protectedRisks: risks.size,
    driftCases: baseline.driftObligations.cases.length, candidateGroups: baseline.deleteCandidateGroups.length,
    candidateCases: browser.filter(row => row.tags?.includes('delete-candidate')).length, visualCases,
    visualApproval: visual.approval.status, phase: ledger.phase, cuts }
}

function boundedRead(base, path) {
  need(text(path) && path.length <= 4096 && !path.includes('\\') && !path.startsWith('/') &&
    path.split('/').every(part => part !== '..' && part !== '.' && part !== ''), 'Invalid ledger input path')
  const full = resolve(base, path)
  need(statSync(full).isFile() && statSync(full).size <= 16 * 1024 * 1024, `Ledger input exceeds 16 MiB: ${path}`)
  return readFileSync(full)
}

export function checkRepository(base = root, { p01 = false } = {}) {
  const read = path => boundedRead(base, path)
  const parse = path => JSON.parse(read(path))
  const baselineBytes = read('scripts/test-prune/baseline.json')
  const ledger = parse('scripts/test-prune/risk-ledger.json')
  need(sha256(baselineBytes) === ledger.baselineSha256, 'Frozen baseline SHA-256 mismatch')
  return validateLedger({ baseline: JSON.parse(baselineBytes), ledger, visual: parse('scripts/test-prune/visual-manifest.json'),
    currentWeb: parse('scripts/ci/web-test-tiers.json'), currentGo: parse('scripts/ci/go-test-tiers.json'),
    sourceHash: path => sha256(read(path)), p01 })
}

export function main(args = process.argv.slice(2)) {
  need(args.length === 0 || same(args, ['--p01']), 'Usage: node scripts/test-prune/check.mjs [--p01]')
  console.log(JSON.stringify(checkRepository(root, { p01: args.length === 1 })))
}

if (process.argv[1] && import.meta.url === pathToFileURL(resolve(process.argv[1])).href) {
  try { main() } catch (error) { console.error(`Test-prune ledger: ${error.message}`); process.exitCode = 1 }
}
