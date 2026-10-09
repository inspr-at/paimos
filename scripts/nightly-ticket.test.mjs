// SPDX-License-Identifier: AGPL-3.0-only
import test from 'node:test'
import assert from 'node:assert/strict'
import { mkdtempSync, mkdirSync, writeFileSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { nightlyTicket, deliverTicket, failureEvidence, main } from './nightly-ticket.mjs'

const run = { id: 123, run_attempt: 2, repository: { full_name: 'inspr-at/paimos' }, path: '.github/workflows/nightly-full.yml',
  head_branch: 'main', event: 'schedule', head_sha: 'a'.repeat(40), status: 'completed', conclusion: 'failure', html_url: 'https://github.com/inspr-at/paimos/actions/runs/123' }
const jobs = [{ id: 1, run_id: 123, head_sha: run.head_sha, status: 'completed', conclusion: 'failure', name: 'nightly-web-setup', steps: [{ name: 'Typecheck', conclusion: 'failure' }] }]
const account = 'instance: ppm (https://aeon.barta.cm)\ntenant: INSPR (inspr)\n'

test('red nightly produces exact-SHA evidence; green, pending and foreign runs cannot create work', () => {
  const ticket = nightlyTicket(run, jobs, { cases: ['go-test-5: internal/db:TestRestore'], runners: ['web-unit'] })
  assert.match(ticket.description, /Commit: a{40}\nAttempt: 2/)
  assert.match(ticket.description, /nightly-web-setup \/ Typecheck/)
  assert.match(ticket.description, /internal\/db:TestRestore/)
  assert.equal(nightlyTicket({ ...run, conclusion: 'success' }, []), null)
  assert.equal(nightlyTicket({ ...run, conclusion: 'cancelled' }, []), null)
  for (const patch of [{ status: 'in_progress' }, { head_branch: 'work/foreign' }, { path: '.github/workflows/ci.yml' }, { html_url: 'https://example.test' }, { repository: { full_name: 'other/repo' } }])
    assert.throws(() => nightlyTicket({ ...run, ...patch }, jobs))
  assert.throws(() => nightlyTicket(run, []), /no failed job/)
  assert.throws(() => nightlyTicket(run, [{ ...jobs[0], head_sha: 'b'.repeat(40) }]), /mismatched/)
})

test('existing reporter authority is verified; duplicate and uncertain writes never report success', () => {
  const ticket = nightlyTicket(run, jobs), calls = []
  const invoke = (_bin, args, input) => {
    calls.push({ args, input })
    if (args[0] === 'whoami') return account
    if (args[1] === 'search') return JSON.stringify({ items: [] })
    return JSON.stringify({ issue_key: 'AEON-2000' })
  }
  assert.equal(deliverTicket(ticket, { paimos: '/approved/pm', write: true, invoke }).status, 'reported')
  assert.match(calls.at(-1).input, /Reporter key: nightly:inspr-at\/paimos:123/)
  assert.ok(calls.at(-1).args.includes('--bug'))
  assert.equal(deliverTicket(ticket, { paimos: '/approved/pm', write: true, invoke: (_bin,args) => args[0] === 'whoami' ? account : JSON.stringify({ items: [{ title: ticket.title, issue_key: 'AEON-2000' }] }) }).status, 'already-reported')
  assert.throws(() => deliverTicket(ticket, { paimos: '/approved/pm', write: true, invoke: () => 'instance: pma' }), /ppm INSPR/)
  assert.throws(() => deliverTicket(ticket, { paimos: '/approved/pm', write: true, invoke: (_bin,args) => args[0] === 'whoami' ? account : JSON.stringify({ items: [], next_cursor: 'next' }) }), /incomplete/)
  assert.throws(() => deliverTicket(ticket, { paimos: '/approved/pm', write: true, invoke: (_bin,args) => args[0] === 'whoami' ? account : args[1] === 'search' ? '[]' : '{}' }), /not acknowledged/)
  assert.equal(deliverTicket(ticket, { invoke: () => { assert.fail('dry-run writes') } }).status, 'dry-run')
})

test('artifact binding rejects another SHA or attempt and limits input before reading', t => {
  const dir = mkdtempSync(join(tmpdir(), 'nightly-evidence-')); t.after(() => rmSync(dir, { recursive: true, force: true }))
  const data = { version: 1, job: 'go-test-5', runId: '123', attempt: '2', sha: run.head_sha, cases: ['internal/db:TestRestore'], runnerFailure: false }
  const file = join(dir, 'go-test-5-failures.json')
  writeFileSync(file, JSON.stringify(data))
  assert.deepEqual(failureEvidence(dir, run), { cases: ['go-test-5: internal/db:TestRestore'], runners: [] })
  writeFileSync(file, JSON.stringify({ ...data, attempt: '1' }))
  assert.throws(() => failureEvidence(dir, run), /binding mismatch/)
  writeFileSync(file, ' '.repeat(2 * 1024 * 1024 + 1))
  assert.throws(() => failureEvidence(dir, run), /byte limit/)
})

test('reporter paginates all jobs and serializes writes; missing pages and lock collisions fail closed', t => {
  const state = mkdtempSync(join(tmpdir(), 'nightly-state-')); t.after(() => rmSync(state, { recursive: true, force: true }))
  const invoke = (_bin,args) => {
    if (args[0] === 'whoami') return account
    if (args[1] === 'search') return '[]'
    if (args[1] === 'create') return JSON.stringify({ issue_key: 'AEON-2000' })
    if (!args[1].includes('/jobs?')) return JSON.stringify(run)
    return JSON.stringify({ total_count: 1, jobs })
  }
  const flags = ['--run-id','123','--paimos','/approved/pm','--state-directory',state,'--write']
  assert.deepEqual(main(flags, { invoke }), { status: 'reported', key: 'AEON-2000' })
  mkdirSync(join(state, 'nightly-123.lock'))
  assert.throws(() => main(flags, { invoke }), /EEXIST/)
  assert.throws(() => main(['--run-id','123'], { invoke: (_bin,args) => args[1].includes('/jobs?') ? JSON.stringify({ total_count: 2, jobs: args[1].endsWith('page=1') ? jobs : [] }) : JSON.stringify(run) }), /Incomplete/)
})
