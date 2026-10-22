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
      return { command, root: read('root'), bootstrap: read('bootstrap'), config: read('config'), sources: existsSync(snapshot)
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
      assert.equal(dependencies.length, 3, 'refresh alternate indexes before retrying the original install')
      assert.match(Object.values(dependencies[0].sources).join('\n'), /http:\/\/azure\.archive\.ubuntu\.com\/ubuntu\//)
      for (const attempt of dependencies) {
        assert.equal(attempt.root, '1', 'apt and Playwright dependencies must run under a root timeout')
        assert.match(attempt.config, /Acquire::Retries "0";/)
        assert.match(attempt.config, /Acquire::http::Timeout "15";/)
        assert.match(attempt.config, /Acquire::https::Timeout "15";/)
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
  for (const [format, mirrorList] of [['list', true], ['sources', true], ['sources', 'hosted']]) {
    for (const mode of ['fetch-fail', 'mirror-stall']) {
      for (const command of ['apt aeon-apt-fixture-missing-tool', 'playwright install-deps chromium', 'playwright install --with-deps --only-shell chromium']) {
        const browser = command.includes('--with-deps')
        const firstLimit = command.startsWith('apt ') ? 90 : 120
        // Use an injected clock to spend the entire first-attempt allowance.
        // A stub returning 124 immediately would not prove time remains.
        const elapsed = [...(browser ? [0] : []), mode === 'mirror-stall' ? firstLimit : 0, 0, 0]
        const result = await run(`bash "${script}" ${command}`, { mode, format, mirrorList, elapsed })
        assert.equal(result.status, 0, `${format} ${mirrorList} ${mode} ${command}: ${result.error}\n${result.stdout}\n${result.stderr}`)
        const dependencies = result.attempts.filter(attempt => attempt.config)
        assert.equal(dependencies.length, 3)
        assert.match(dependencies[1].command, /update -qq$/)
        assert.equal(dependencies[2].command, dependencies[0].command.includes('install-deps') ? dependencies[0].command : 'install -y -qq aeon-apt-fixture-missing-tool')
        const timeouts = result.timeouts.filter(timeout => timeout.root === '1')
        assert.equal(timeouts[0].seconds, firstLimit)
        assert.ok(timeouts[1].seconds > 0, 'reserve time to refresh alternate indexes')
        assert.ok(timeouts[2].seconds > 0, 'reserve time to retry the original install')
        const initial = dependencies[0].sources[format === 'list' ? 'sources.list' : 'ubuntu.sources']
        assert.match(initial, /mirror\+file:.*\/apt-mirrors\.txt/)
        const served = dependencies.at(-1).sources
        const ubuntu = served[format === 'list' ? 'sources.list' : 'ubuntu.sources']
        assert.doesNotMatch(ubuntu, /mirror\+file:|azure\.archive\.ubuntu\.com/)
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
          for (const block of blocks) assert.match(block, /Components: main restricted universe multiverse\n/)
        }
        assert.deepEqual(dependencies[1].sources, served, 'update and install must use the same alternate sources')
        for (const attempt of dependencies) {
          assert.equal(attempt.root, '1')
          assert.match(attempt.bootstrap, /^Dir::Etc::main ".*\/apt-final\.conf";\n$/)
          assert.match(attempt.config, /Acquire::Retries "0";\nAcquire::http::Timeout "15";\nAcquire::https::Timeout "15";/)
          assert.match(attempt.config, /APT::Update::Error-Mode "any";/)
          assert.doesNotMatch(attempt.config, /AllowUnauthenticated|AllowInsecure|Verify-Peer|trusted=/i)
          if (mirrorList === 'hosted') assert.ok(attempt.config.startsWith(`#include "${result.sourceRoot}/apt.conf";\n`), 'retain host main config before the final overrides')
          assert.equal(attempt.sources['vendor.list'], result.sources['sources.list.d/vendor.list'])
          assert.equal(attempt.sources['vendor.sources'], result.sources['sources.list.d/vendor.sources'])
        }
        for (const [file, original] of Object.entries(result.sources)) assert.equal(readFileSync(join(result.sourceRoot, file), 'utf8'), original)
        for (const config of result.configPaths) assert.equal(existsSync(config), false)
        assert.match(result.stdout, /bounded-apt: using .*mirror\+file:/)
        assert.match(result.stdout, /dependencies installed via archive\.ubuntu\.com \/ security\.ubuntu\.com/)
        if (browser) {
          const downloads = result.attempts.filter(attempt => !attempt.config)
          assert.equal(downloads.length, 1)
          assert.equal(downloads[0].root, '0')
        }
      }
    }
  }
})

test('a failed fallback fetch fails with its status after exactly two attempts', async () => {
  for (const mode of ['fetch-always-fail', 'install-fetch-always-fail']) {
    const result = await run(`bash "${script}" playwright install-deps chromium`, { mode })
    assert.equal(result.status, 100, result.stderr)
    assert.equal(result.calls.length, mode === 'fetch-always-fail' ? 2 : 3, 'stop after a failed alternate update or one failed retry install')
    assert.equal(result.calls.filter(call => call.includes('install-deps')).length, mode === 'fetch-always-fail' ? 1 : 2)
    assert.doesNotMatch(result.stdout, /dependencies installed/)
    for (const config of result.configPaths) assert.equal(existsSync(config), false)
  }
})

test('both apt mirrors share the overall deadline', async () => {
  const result = await run(`bash "${script}" apt aeon-apt-fixture-missing-tool`, { mode: 'fetch-fail', elapsed: [90, 120] })
  assert.equal(result.status, 124, `${result.error}\n${result.stdout}\n${result.stderr}`)
  assert.deepEqual(result.timeouts.map(({ root, seconds }) => ({ root, seconds })), [{ root: '1', seconds: 90 }, { root: '1', seconds: 120 }])
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
