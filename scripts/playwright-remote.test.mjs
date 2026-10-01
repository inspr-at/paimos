// SPDX-License-Identifier: AGPL-3.0-only
import assert from 'node:assert/strict'
import { spawnSync } from 'node:child_process'
import { existsSync, mkdirSync, mkdtempSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { test } from 'node:test'
import { quote, remoteProbe, remoteScript, runRemote } from './playwright-remote.mjs'

const idle = { consoleUser: 'mba', idleSeconds: '600', load: '5', active: false, builderOn: false }
function probe(overrides = {}) {
  const state = { ...idle, ...overrides }
  const directory = mkdtempSync(join(tmpdir(), 'aeon-pw-remote-probe-'))
  if (state.builderOn) writeFileSync(join(directory, '.aeon-builder-on'), '')
  if (state.active) {
    mkdirSync(join(directory, '.aeon-remote-test'))
    writeFileSync(join(directory, '.aeon-remote-test/other.pid'), '1')
  }
  // Run the shipped shell guards against value-only probes, without SSH or
  // touching another user's home. Shell variables remain task-specific.
  const prelude = `lane_probe_home=${quote(directory)}\n` +
    `stat() { printf '%s\\n' ${quote(state.consoleUser)}; }\n` +
    `ioreg() { printf '"HIDIdleTime" = %s\\n' ${quote(state.idleSeconds ? String(Number(state.idleSeconds) * 1e9) : '')}; }\n` +
    `sysctl() { printf '{ %s 2 2 }\\n' ${quote(state.load)}; }\n`
  // Replace only the script's HOME references to isolate its fixture paths.
  return spawnSync('bash', ['-s'], { input: prelude + remoteProbe.replaceAll('$HOME', '$lane_probe_home'), encoding: 'utf8' })
}
test('remote presence, load, builder and capacity guards fail closed', () => {
  assert.equal(probe().status, 0)
  assert.equal(probe({ consoleUser: 'ci', idleSeconds: '0' }).status, 0)
  for (const override of [
    { consoleUser: 'mailina' }, { consoleUser: '' }, { idleSeconds: '599' }, { idleSeconds: '' },
    { load: '18.01' }, { load: 'NaN' }, { active: true }, { builderOn: true },
  ]) assert.equal(probe(override).status, 3, JSON.stringify(override))
})
test('remote args are shell quoted without substitution or word splitting', () => {
  for (const argument of ["spec's.ts", '$(exit 19)', '`exit 19`', 'a b', 'a\nb']) {
    const result = spawnSync('bash', ['-c', `printf '%s' ${quote(argument)}`], { encoding: 'utf8' })
    assert.equal(result.status, 0)
    assert.equal(result.stdout, argument)
  }
})
test('remote script checks presence again under the reservation and installs no browsers', () => {
  const script = remoteScript('abcdefabcdef-12345678-1234-1234-1234-123456789abc', ['tests/a.spec.ts'])
  assert.equal(spawnSync('bash', ['-n'], { input: script }).status, 0)
  assert.ok(script.indexOf('mkdir "$lock"') < script.lastIndexOf('console_user='))
  assert.ok(script.lastIndexOf('console_user=') < script.indexOf('npm ci'))
  assert.match(script, /OPS-247 owns installation/)
  assert.doesNotMatch(script, /playwright install|git push|rm -/)
  assert.match(script, /CI= PW_WORKERS=1/)
  assert.throws(() => remoteScript('../foreign', []), /identity/)
})

test('OPS hold and capture controls refuse before any SSH or heavy setup', async () => {
  const previous = process.env.AEON_REMOTE_CONTROL_DIR
  const directory = mkdtempSync(join(tmpdir(), 'aeon-pw-remote-control-'))
  try {
    delete process.env.AEON_REMOTE_CONTROL_DIR
    await assert.rejects(runRemote([]), /Set AEON_REMOTE_CONTROL_DIR/)
    process.env.AEON_REMOTE_CONTROL_DIR = directory
    await assert.rejects(runRemote([]), /OPS-247 bootstrap and approved launcher are pending/)
    const cli = spawnSync(process.execPath, [new URL('./playwright-remote.mjs', import.meta.url).pathname], {
      env: { ...process.env, AEON_HEAVY_JOB_LANE: '/unavailable-ops-launcher', AEON_UI_LANE_ACTIVE: '1' }, encoding: 'utf8',
    })
    assert.equal(cli.status, 3)
    assert.match(cli.stderr, /OPS-247 bootstrap and approved launcher are pending/)
    writeFileSync(join(directory, '.capture-open'), '')
    await assert.rejects(runRemote([]), /capture window/)
    writeFileSync(join(directory, '.hold-mbp2606'), '')
    await assert.rejects(runRemote([]), /held by OPS/)
  } finally { if (previous === undefined) delete process.env.AEON_REMOTE_CONTROL_DIR; else process.env.AEON_REMOTE_CONTROL_DIR = previous }
})

test('missing approved browser exits 3 and releases only this run reservation', () => {
  const directory = mkdtempSync(join(tmpdir(), 'aeon-pw-remote-missing-'))
  const run = 'abcdefabcdef-12345678-1234-1234-1234-123456789abc'
  const prelude = `lane_probe_home=${quote(directory)}\n` +
    'stat() { echo mba; }\nioreg() { echo \'"HIDIdleTime" = 600000000000\'; }\nsysctl() { echo "{ 5 2 2 }"; }\n' +
    'tar() { mkdir -p "$4/web"; }\nnpm() { :; }\nnode() { return 3; }\n'
  const result = spawnSync('bash', ['-s'], { input: prelude + remoteScript(run, []).replaceAll('$HOME', '$lane_probe_home'), encoding: 'utf8' })
  assert.equal(result.status, 3, result.stderr)
  assert.equal(existsSync(join(directory, '.aeon-ui-remote.lock')), false)
  assert.equal(existsSync(join(directory, `.aeon-remote-test/ui-${run}.pid`)), false)
  // Run files remain for inspection; only our reservation is released.
  assert.equal(existsSync(join(directory, `aeon-ui-runs/${run}/web`)), true)
})
