// SPDX-License-Identifier: AGPL-3.0-only
// AEON-933: an unprivileged timeout cannot reap root apt-get. The 5d155476
// retry then collides on the lock and the step fails. This fails on that
// commit because its workflow commands are the historical ones below.
import { afterEach, test } from 'node:test'
import assert from 'node:assert/strict'
import { readFileSync, existsSync, readdirSync, rmSync } from 'node:fs'
import { join } from 'node:path'
import { fileURLToPath } from 'node:url'
import { aptFixture } from './testdata/apt-fixture.mjs'
import { fixtureOwner } from './testdata/fixture-lifecycle.mjs'
import { auditWorkflows, auditWorkflowText } from './check-apt-bounds.mjs'

const root = fileURLToPath(new URL('..', import.meta.url))
const script = join(root, 'scripts/ci/bounded-apt.sh')
const historical = `for i in 1 2 3; do
  timeout 240 npx playwright install-deps chromium && break
done`

const fixtures = fixtureOwner()
const states = []
afterEach(() => {
  fixtures.cleanup()
  for (const state of states.splice(0)) rmSync(state, { recursive: true, force: true })
})

async function run(command, options) {
  const { state, env, sources, sourceRoot } = aptFixture(options)
  states.push(state)
  let stdout = '', stderr = '', error = ''
  const child = fixtures.spawn('bash', ['-eo', 'pipefail', '-c', command], { cwd: root, env, stdio: ['ignore', 'pipe', 'pipe'] })
  child.stdout.on('data', data => { stdout += data })
  child.stderr.on('data', data => { stderr += data })
  const deadline = setTimeout(() => { error = 'fixture timed out'; fixtures.cleanup() }, 8000)
  try {
    const status = await new Promise((resolve, reject) => { child.once('error', reject); child.once('close', resolve) })
    const calls = existsSync(join(state, 'calls')) ? readFileSync(join(state, 'calls'), 'utf8').split('\n').filter(Boolean) : []
    const attempts = calls.map((command, index) => {
      const snapshot = join(state, `attempt-${index + 1}`)
      const read = file => existsSync(join(snapshot, file)) ? readFileSync(join(snapshot, file), 'utf8') : ''
      return { command, root: read('root'), config: read('config'), sources: existsSync(snapshot)
        ? Object.fromEntries(readdirSync(snapshot).filter(file => /\.(list|sources)$/.test(file)).map(file => [file, read(file)])) : {} }
    })
    const timeouts = existsSync(join(state, 'timeouts')) ? readFileSync(join(state, 'timeouts'), 'utf8').trim().split('\n').map(line => {
      const [root, seconds, command] = line.split('|')
      return { root, seconds: Number(seconds), command }
    }) : []
    const configPaths = existsSync(join(state, 'config-paths')) ? readFileSync(join(state, 'config-paths'), 'utf8').trim().split('\n') : []
    return { status, stderr, stdout, error, calls, state, attempts, sources, sourceRoot, timeouts, configPaths }
  } finally { clearTimeout(deadline); fixtures.cleanup() }
}

test('workflow installs delegate to the root-timeout helper', () => {
  assert.deepEqual(auditWorkflows(), [])
  const old = `jobs:\n  demo:\n    steps:\n      - name: Install Chromium system dependencies\n        timeout-minutes: 13\n        run: |\n          for i in 1 2 3; do\n            timeout 240 npx playwright install-deps chromium && break\n          done\n`
  const failures = auditWorkflowText('old.yml', old)
  assert.ok(failures.some(line => line.includes('must go through')), failures.join('\n'))
})

test('a hung root apt is reaped before the retry, which the 5d155476 command cannot do', async () => {
  const current = await run(`bash "${script}" playwright install-deps chromium`, { mode: 'hung' })
  assert.equal(current.status, 0, `${current.error}\n${current.stdout}\n${current.stderr}`)
  assert.equal(current.calls.filter(call => call.includes('install-deps')).length, 2)
  const legacy = await run(historical, { mode: 'hung' })
  assert.notEqual(legacy.status, 0, 'unprivileged timeout must leave the apt lock held')
  assert.match(legacy.stderr, /Could not get lock/, `${legacy.error}\n${legacy.calls.join('\n')}\n${legacy.stderr}`)
})

test('browser downloads stay unprivileged while dependency installs run as root', async () => {
  const result = await run(`bash "${script}" playwright install --with-deps --only-shell chromium`, { mode: 'hung' })
  assert.equal(result.status, 0, result.stderr)
  assert.ok(result.calls.some(call => call.includes('--only-shell')))
  assert.ok(result.calls.some(call => call.includes('install-deps')))
  assert.equal(existsSync(join(result.state, 'errors')), false)
})

test('non-fetch failures fail once and installed shells skip apt', async () => {
  const exhausted = await run(`bash "${script}" playwright install-deps chromium`, { mode: 'always-fail' })
  assert.equal(exhausted.status, 100, exhausted.stderr)
  assert.equal(exhausted.calls.length, 1)
  const skipped = await run(`bash "${script}" apt fish zsh`, { mode: 'always-fail', shells: true })
  assert.equal(skipped.status, 0, skipped.stderr)
  assert.deepEqual(skipped.calls, [])
  const recovered = await run(`bash "${script}" apt aeon-apt-fixture-missing-tool`, { mode: 'hung' })
  assert.equal(recovered.status, 0, recovered.stderr)
})

// AEON-1652 risk: a stalled Azure mirror wastes the whole browser shard;
// retry only fetch failures, without changing host sources or apt security.
test('fetch failures retry once through temporary Ubuntu mirrors for apt and Playwright', async () => {
  for (const format of ['list', 'sources']) {
    for (const command of ['apt aeon-apt-fixture-missing-tool', 'playwright install-deps chromium', 'playwright install --with-deps --only-shell chromium']) {
      const result = await run(`bash "${script}" ${command}`, { mode: 'fetch-fail', format })
      assert.equal(result.status, 0, `${command}: ${result.error}\n${result.stdout}\n${result.stderr}`)
      const dependencies = result.attempts.filter(attempt => attempt.config)
      assert.equal(dependencies.length, command.startsWith('apt ') ? 3 : 2)
      assert.match(Object.values(dependencies[0].sources).join('\n'), /http:\/\/azure\.archive\.ubuntu\.com\/ubuntu\//)
      for (const attempt of dependencies) {
        assert.equal(attempt.root, '1', 'apt and Playwright dependencies must run under a root timeout')
        assert.match(attempt.config, /Acquire::Retries "3";/)
        assert.match(attempt.config, /Acquire::http::Timeout "20";/)
        assert.match(attempt.config, /Acquire::https::Timeout "20";/)
        assert.match(attempt.config, /APT::Update::Error-Mode "any";/)
        assert.equal(attempt.sources['vendor.list'], result.sources['sources.list.d/vendor.list'])
        assert.equal(attempt.sources['vendor.sources'], result.sources['sources.list.d/vendor.sources'])
      }
      const served = dependencies.at(-1).sources
      const ubuntu = served[format === 'list' ? 'sources.list' : 'ubuntu.sources']
      assert.doesNotMatch(ubuntu, /azure\.archive\.ubuntu\.com/)
      assert.match(ubuntu, /http:\/\/archive\.ubuntu\.com\/ubuntu\//)
      assert.match(ubuntu, /http:\/\/security\.ubuntu\.com\/ubuntu\//)
      assert.match(ubuntu, /signed-by=\/usr\/share\/keyrings\/ubuntu-archive-keyring\.gpg|Signed-By: \/usr\/share\/keyrings\/ubuntu-archive-keyring\.gpg/)
      if (format === 'sources') {
        const blocks = ubuntu.trim().split(/\n\s*\n/)
        assert.equal(blocks.length, 2)
        assert.match(blocks[0], /Suites: noble noble-updates noble-backports\n/)
        assert.match(blocks[0], /URIs: http:\/\/archive\.ubuntu\.com\/ubuntu\//)
        assert.match(blocks[1], /Suites: noble-security\n/)
        assert.match(blocks[1], /URIs: http:\/\/security\.ubuntu\.com\/ubuntu\//)
        for (const block of blocks) {
          assert.match(block, /Types: deb deb-src\n/)
          assert.match(block, /Components: main restricted universe multiverse\n/)
          assert.match(block, /Architectures: amd64\n/)
        }
      } else {
        assert.match(ubuntu, /deb-src http:\/\/security\.ubuntu\.com\/ubuntu\/ noble-security main restricted/)
        assert.match(ubuntu, /deb \[arch=amd64 signed-by=[^\]]+\] http:\/\/archive\.ubuntu\.com\/ubuntu\/ noble main restricted/)
        assert.match(ubuntu, /# Keep comments and source options\./)
      }
      for (const [file, original] of Object.entries(result.sources)) assert.equal(readFileSync(join(result.sourceRoot, file), 'utf8'), original)
      for (const config of result.configPaths) assert.equal(existsSync(config), false, 'temporary configuration must be cleaned up')
      assert.match(result.stdout, /bounded-apt: using azure\.archive\.ubuntu\.com/)
      assert.match(result.stdout, /retrying fetch once via archive\.ubuntu\.com/)
      assert.match(result.stdout, /dependencies installed via archive\.ubuntu\.com \/ security\.ubuntu\.com/)
      if (command.includes('--with-deps')) {
        const browsers = result.attempts.filter(attempt => attempt.command.includes('--only-shell'))
        assert.equal(browsers.length, 1, 'retry dependencies without downloading the browser again')
        assert.equal(browsers[0].root, '0')
      }
    }
  }
})

test('dependency errors never retry even after an ignored fetch', async () => {
  for (const command of ['apt aeon-apt-fixture-missing-tool', 'playwright install-deps chromium']) {
    const result = await run(`bash "${script}" ${command}`, { mode: 'dependency-fail' })
    assert.equal(result.status, 100, result.stderr)
    assert.match(result.stdout, /held broken packages/)
    assert.equal(result.calls.length, 1)
    assert.doesNotMatch(result.stdout, /retrying fetch|dependencies installed/)
    for (const config of result.configPaths) assert.equal(existsSync(config), false)
  }
})

// AEON-1652 fix2: hosted runners hide Azure behind mirror+file URIs. A
// timeout with only Ign lines must still reach a different Ubuntu mirror.
test('hosted Ubuntu mirror lists recover after fetch errors and stalls without changing vendor lists', async () => {
  for (const format of ['list', 'sources']) {
    for (const mode of ['fetch-fail', 'mirror-stall']) {
      for (const command of ['apt aeon-apt-fixture-missing-tool', 'playwright install-deps chromium']) {
        const result = await run(`bash "${script}" ${command}`, { mode, format, mirrorList: true })
        assert.equal(result.status, 0, `${format} ${mode} ${command}: ${result.error}\n${result.stdout}\n${result.stderr}`)
        assert.equal(result.calls.length, command.startsWith('apt ') ? 3 : 2)
        const initial = result.attempts[0].sources[format === 'list' ? 'sources.list' : 'ubuntu.sources']
        assert.match(initial, /mirror\+file:.*\/apt-mirrors\.txt/)
        const served = result.attempts.at(-1).sources
        const ubuntu = served[format === 'list' ? 'sources.list' : 'ubuntu.sources']
        assert.doesNotMatch(ubuntu, /mirror\+file:|azure\.archive\.ubuntu\.com/)
        assert.match(ubuntu, /https:\/\/archive\.ubuntu\.com\/ubuntu\//)
        assert.match(ubuntu, /https:\/\/security\.ubuntu\.com\/ubuntu\//)
        assert.match(ubuntu, /signed-by=\/usr\/share\/keyrings\/ubuntu-archive-keyring\.gpg|Signed-By: \/usr\/share\/keyrings\/ubuntu-archive-keyring\.gpg/)
        for (const attempt of result.attempts) {
          assert.equal(attempt.root, '1')
          assert.equal(attempt.sources['vendor.list'], result.sources['sources.list.d/vendor.list'])
          assert.equal(attempt.sources['vendor.sources'], result.sources['sources.list.d/vendor.sources'])
        }
        for (const [file, original] of Object.entries(result.sources)) assert.equal(readFileSync(join(result.sourceRoot, file), 'utf8'), original)
        for (const config of result.configPaths) assert.equal(existsSync(config), false)
        assert.match(result.stdout, /bounded-apt: using .*mirror\+file:/)
        assert.match(result.stdout, /dependencies installed via archive\.ubuntu\.com \/ security\.ubuntu\.com/)
      }
    }
  }
})

test('a failed fallback fetch fails with its status after exactly two attempts', async () => {
  const result = await run(`bash "${script}" playwright install-deps chromium`, { mode: 'fetch-always-fail' })
  assert.equal(result.status, 100, result.stderr)
  assert.equal(result.calls.length, 2)
  assert.doesNotMatch(result.stdout, /dependencies installed/)
})

test('both apt mirrors share the overall deadline', async () => {
  const result = await run(`bash "${script}" apt aeon-apt-fixture-missing-tool`, { mode: 'fetch-fail', elapsed: [150, 60] })
  assert.equal(result.status, 124, `${result.error}\n${result.stdout}\n${result.stderr}`)
  assert.deepEqual(result.timeouts.map(({ root, seconds }) => ({ root, seconds })), [{ root: '1', seconds: 150 }, { root: '1', seconds: 60 }])
  assert.equal(result.calls.length, 2, 'the exhausted budget must prevent apt install')
  assert.ok(result.calls.every(call => call.includes('update -qq')))
  assert.doesNotMatch(result.stdout, /dependencies installed/)
})

test('browser download errors do not trigger an apt mirror retry', async () => {
  const result = await run(`bash "${script}" playwright install --with-deps chromium`, { mode: 'browser-fail' })
  assert.equal(result.status, 1, result.stderr)
  assert.deepEqual(result.calls, ['playwright install chromium'])
  assert.equal(result.attempts[0].root, '0')
  assert.doesNotMatch(result.stdout, /retrying fetch|dependencies installed/)
})
