// SPDX-License-Identifier: AGPL-3.0-only
// AEON-933: an unprivileged timeout cannot reap root apt-get. The 5d155476
// retry then collides on the lock and the step fails. This fails on that
// commit because its workflow commands are the historical ones below.
import { afterEach, test } from 'node:test'
import assert from 'node:assert/strict'
import { readFileSync, writeFileSync, existsSync, readdirSync, rmSync, cpSync, mkdirSync, statSync, symlinkSync, chmodSync } from 'node:fs'
import { join } from 'node:path'
import { fileURLToPath } from 'node:url'
import { aptFixture } from './testdata/apt-fixture.mjs'
import { fixtureOwner } from './testdata/fixture-lifecycle.mjs'
import { auditWorkflows, auditWorkflowText, withoutOptionalAptDownload } from './check-apt-bounds.mjs'

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
  if (typeof command === 'function') command = command(state)
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
    const killGraces = existsSync(join(state, 'kill-graces')) ? readFileSync(join(state, 'kill-graces'), 'utf8').trim().split('\n').map(Number) : []
    const summary = existsSync(join(state, 'summary')) ? readFileSync(join(state, 'summary'), 'utf8') : ''
    return { status, stderr, stdout, error, calls, state, attempts, sources, sourceRoot, timeouts, configPaths, killGraces, summary }
  } finally { clearTimeout(deadline); fixtures.cleanup() }
}

test('workflow installs delegate to the root-timeout helper', () => {
  assert.deepEqual(auditWorkflows(), [])
  const workflow = readFileSync(join(root, '.github/workflows/ci.yml'), 'utf8')
  const shard = workflow.slice(workflow.indexOf('\n  web-shard:'), workflow.indexOf('\n  web:'))
  assert.doesNotMatch(withoutOptionalAptDownload(shard), /continue-on-error/)
  assert.throws(() => withoutOptionalAptDownload(shard.replace('      - name: Download optional apt bundle', '      - name: Download optional apt bundle\n        run: npm test')), /cannot suppress test execution/)
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
        const firstLimit = command.startsWith('apt ') ? 60 : 120
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

// AEON-1652 fix4: trickling downloads never emit Err/Ign and can consume the
// whole first cap plus SIGKILL grace. Retry only before dpkg has started.
test('slow package downloads time out into fallback with a usable retry budget', async () => {
  for (const timeoutStatus of [124, 137]) {
    for (const command of ['apt aeon-apt-fixture-missing-tool', 'playwright install-deps chromium', 'playwright install --with-deps --only-shell chromium']) {
      const apt = command.startsWith('apt '), browser = command.includes('--with-deps')
      const firstLimit = apt ? 60 : 120, browserSeconds = browser ? 240 : 0
      const elapsed = [...(browser ? [browserSeconds] : []), ...(apt ? [0] : []), firstLimit + 20, 20, 0]
      const result = await run(`bash "${script}" ${command}`, { mode: 'slow-fetch', mirrorList: 'hosted', timeoutStatus, elapsed })
      assert.equal(result.status, 0, `${timeoutStatus} ${command}: ${result.error}\n${result.stdout}\n${result.stderr}`)
      assert.match(result.stdout, /^Get:2 .*fonts-ipafont-gothic/m)
      assert.doesNotMatch(result.stdout, /^(Ign:|Err:|E:)/m, 'a timeout alone must trigger fallback')
      assert.match(result.stdout, /retrying fetch once via archive\.ubuntu\.com/)
      assert.match(result.stdout, /dependencies installed via archive\.ubuntu\.com \/ security\.ubuntu\.com/)
      const dependencies = result.attempts.filter(attempt => attempt.config)
      assert.equal(dependencies.length, apt ? 4 : 3, 'refresh fallback indexes and retry the install once')
      assert.match(dependencies.at(-2).command, /update -qq$/)
      assert.match(dependencies.at(-1).command, apt ? /install -y -qq aeon-apt-fixture-missing-tool$/ : /playwright install-deps chromium$/)
      assert.doesNotMatch(dependencies.at(-1).sources['ubuntu.sources'], /mirror\+file:|azure\.archive\.ubuntu\.com/)
      assert.match(dependencies.at(-1).sources['ubuntu.sources'], /http:\/\/archive\.ubuntu\.com\/ubuntu\//)
      assert.ok(dependencies.every(attempt => attempt.root === '1'))
      const timeouts = result.timeouts.filter(timeout => timeout.root === '1')
      assert.deepEqual(result.killGraces, result.timeouts.map(() => 20), 'every timeout has the budgeted kill grace')
      assert.equal(timeouts[0].seconds, firstLimit)
      assert.equal(timeouts.at(-1).seconds, apt ? 90 : 240, 'retain a full install allowance after first-cap + kill grace + index refresh')
      assert.ok(browserSeconds + firstLimit + 20 + 20 + timeouts.at(-1).seconds + 20 <= (apt ? 210 : 720), 'final kill grace fits inside the shared budget')
      for (const [file, original] of Object.entries(result.sources)) assert.equal(readFileSync(join(result.sourceRoot, file), 'utf8'), original)
      for (const config of result.configPaths) assert.equal(existsSync(config), false)
      if (browser) assert.equal(result.attempts.filter(attempt => !attempt.config).length, 1)
    }
  }
})

test('timeouts after any dpkg activity fail without retrying', async () => {
  for (const timeoutStatus of [124, 137]) {
    for (const dpkgActivity of ['Preparing to unpack libtest.deb ...', 'Unpacking libtest (1.0) ...', 'Setting up libtest (1.0) ...', 'Selecting previously unselected package libtest.']) {
      for (const command of ['apt aeon-apt-fixture-missing-tool', 'playwright install-deps chromium']) {
        const apt = command.startsWith('apt ')
        const result = await run(`bash "${script}" ${command}`, { mode: 'dpkg-timeout', timeoutStatus, dpkgActivity: `Ign:1 http://azure.archive.ubuntu.com/ubuntu noble InRelease\n${dpkgActivity}`, elapsed: [...(apt ? [0] : []), (apt ? 60 : 120) + 20] })
        assert.equal(result.status, timeoutStatus, `${dpkgActivity}: ${result.error}\n${result.stdout}\n${result.stderr}`)
        assert.ok(result.stdout.includes(dpkgActivity))
        assert.equal(result.calls.length, apt ? 2 : 1)
        assert.doesNotMatch(result.stdout, /retrying fetch|dependencies installed/)
        for (const config of result.configPaths) assert.equal(existsSync(config), false)
      }
    }
  }
})

test('both apt mirrors share the overall deadline', async () => {
  const result = await run(`bash "${script}" apt aeon-apt-fixture-missing-tool`, { mode: 'fetch-fail', elapsed: [80, 130] })
  assert.equal(result.status, 124, `${result.error}\n${result.stdout}\n${result.stderr}`)
  assert.deepEqual(result.timeouts.map(({ root, seconds }) => ({ root, seconds })), [{ root: '1', seconds: 60 }, { root: '1', seconds: 110 }])
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

// AEON-1652 fix5 risk: fetching in every shard multiplies mirror outages;
// partial artifacts must fall back before dpkg, never after a failed write.
test('one complete image bundle installs offline and invalid bundles retain network fallback', async () => {
  const producer = await run(state => `bash "${script}" bundle "${join(state, 'bundle')}"`, { mode: 'bundle-fetch-retry', rootOnly: true, mirrorList: 'hosted', elapsed: [0, 0, 260, 0, 260, 0, 0, 0] })
  assert.equal(producer.status, 0, `${producer.error}\n${producer.stdout}\n${producer.stderr}`)
  const bundleDir = join(producer.state, 'bundle')
  const manifest = JSON.parse(readFileSync(join(bundleDir, 'manifest.json'), 'utf8'))
  assert.deepEqual(manifest.packages, ['fish', 'fonts-test', 'libtest', 'zsh'], 'retain installed libraries from the dry-run argument list')
  assert.deepEqual(manifest.image, ['ubuntu24', '20261021.1', 'X64'])
  assert.equal(producer.calls.filter(call => call.includes('--download-only')).length, 3, 'allow several bounded fetch attempts across mirrors')
  assert.deepEqual(producer.timeouts.filter(timeout => timeout.root === '1').map(timeout => timeout.seconds), [150, 240, 150, 240, 150, 240, 30, 30, 30], 'fetch attempts, ownership handoff and cleanup stay inside the shared 18-minute budget')
  // AEON-1652 fix7: apt's root-only output must reach an unprivileged
  // manifest and artifact upload without weakening checksum validation.
  assert.deepEqual(JSON.parse(readFileSync(join(producer.state, 'root-only-modes'), 'utf8')), {
    'lists/partial': 0o700, 'archives/partial': 0o700, 'lists/auxfiles': 0o700,
    'lists/noble_Packages': 0o600, 'archives/fish_1_amd64.deb': 0o600,
  })
  assert.equal(readFileSync(join(producer.state, 'ownership-handoff'), 'utf8'), `${process.getuid()}:${process.getgid()}\n${bundleDir}\n`)
  assert.equal(existsSync(join(producer.state, 'root-owned-bundle')), false)
  assert.equal(existsSync(join(bundleDir, 'lists/auxfiles')), false, 'exclude auxiliary metadata from the artifact')
  for (const [file, mode] of [['lists/noble_Packages', 0o644], ['archives/fish_1_amd64.deb', 0o644]]) {
    assert.equal(statSync(join(bundleDir, file)).mode & 0o777, mode, `${file} must be readable by the runner and artifact uploader`)
  }
  const manifestTimeout = producer.timeouts.find(timeout => timeout.command === `python3 - write ${bundleDir}`)
  assert.equal(manifestTimeout?.root, '0', 'manifest writing must remain unprivileged')
  // Negative control uses a private copy, never a throwaway branch commit.
  const missingHandoff = await run(state => {
    const copy = join(state, 'without-handoff.sh')
    writeFileSync(copy, readFileSync(script, 'utf8').replace(/^      run_root_timeout 30 chown .*\n/m, ''))
    return `bash "${copy}" bundle "${join(state, 'bundle')}"`
  }, { mode: 'bundle-ok', rootOnly: true })
  assert.equal(missingHandoff.status, 1, missingHandoff.stdout)
  assert.match(missingHandoff.stderr, /bundle still owned by root at manifest write/)
  assert.equal(existsSync(join(missingHandoff.state, 'bundle/manifest.json')), false)
  for (const file of ['archives/partial', 'archives/lock', 'lists/partial', 'lists/lock']) assert.equal(existsSync(join(bundleDir, file)), false, 'exclude private apt working files from the artifact')
  assert.match(producer.attempts.filter(attempt => attempt.command.includes('--download-only'))[1].sources['ubuntu.sources'], /http:\/\/archive\.ubuntu\.com/)
  assert.ok(producer.attempts.filter(attempt => attempt.command.includes('--download-only')).every(attempt => attempt.root === '1' && attempt.command.includes('--reinstall')))
  for (const [file, original] of Object.entries(producer.sources)) assert.equal(readFileSync(join(producer.sourceRoot, file), 'utf8'), original)

  for (const command of ['apt libtest', 'playwright install-deps chromium', 'playwright install --with-deps --only-shell chromium']) {
    // The network adapter would fail. Success therefore requires the bundle.
    const offline = await run(`bash "${script}" ${command}`, { mode: 'always-fail', bundleDir })
    assert.equal(offline.status, 0, `${offline.error}\n${offline.stdout}\n${offline.stderr}`)
    assert.match(offline.stdout, /dependencies installed offline/)
    assert.match(offline.summary, /path=exact bundle/)
    const dependencies = offline.calls.filter(call => !call.includes('playwright install --only-shell'))
    assert.equal(dependencies.length, 2, 'archive-only preflight followed by one bounded install')
    assert.ok(dependencies.every(call => call.includes('--no-download') && !call.includes('update') && !call.includes('npx')))
    assert.ok(dependencies[0].includes('--download-only'))
    assert.ok(!dependencies[1].includes('--download-only'))
    assert.ok(offline.attempts.filter(attempt => attempt.command.includes('--no-download')).every(attempt => attempt.root === '1' && attempt.config.includes(`${bundleDir}/lists`) && attempt.config.includes(`${bundleDir}/archives`)))
    assert.ok(offline.timeouts.filter(timeout => timeout.root === '1').every(timeout => timeout.seconds <= 150))
  }

  // Model download-artifact's pattern layout, including two image versions.
  const downloaded = join(producer.state, 'downloaded')
  mkdirSync(downloaded)
  const exactDir = join(downloaded, 'apt-bundle-ubuntu24-20261021.1-X64')
  const crossDir = join(downloaded, 'apt-bundle-ubuntu24-20261012.334.1-X64')
  cpSync(bundleDir, exactDir, { recursive: true })
  cpSync(bundleDir, crossDir, { recursive: true })
  writeFileSync(join(crossDir, 'manifest.json'), JSON.stringify({ ...manifest, image: ['ubuntu24', '20261012.334.1', 'X64'] }))
  const preferred = await run(`bash "${script}" playwright install-deps chromium`, { mode: 'always-fail', bundleDir: downloaded })
  assert.equal(preferred.status, 0, preferred.stderr)
  assert.match(preferred.summary, /path=exact bundle/)
  assert.equal(preferred.calls.length, 2, 'exact match wins even when another image sorts first')
  assert.ok(preferred.attempts.every(attempt => attempt.config.includes(`${exactDir}/archives`)))

  for (const command of ['apt libtest', 'playwright install-deps chromium', 'playwright install --with-deps --only-shell chromium']) {
    const cross = await run(`bash "${script}" ${command}`, { mode: 'always-fail', bundleDir: downloaded, imageVersion: '20261018.339.1', elapsed: [5, 10, 10, 0] })
    assert.equal(cross.status, 0, `${cross.error}\n${cross.stdout}\n${cross.stderr}`)
    assert.match(cross.summary, /path=cross-image bundle/)
    const dependencies = cross.calls.filter(call => !call.includes('playwright install --only-shell'))
    assert.equal(dependencies.length, 3)
    assert.match(dependencies[0], /--simulate/)
    assert.match(dependencies[1], /--download-only/)
    assert.doesNotMatch(dependencies[2], /--simulate|--download-only/)
    assert.ok(dependencies.every(call => call.includes('--no-download') && !call.includes('update') && !call.includes('npx')))
    assert.ok(cross.attempts.filter(attempt => attempt.command.includes('--no-download')).every(attempt => attempt.config.includes(`${crossDir}/lists`) && attempt.config.includes(`${crossDir}/archives`)))
    const rootTimeouts = cross.timeouts.filter(timeout => timeout.root === '1')
    assert.equal(rootTimeouts.length, 3)
    // Injected time spent validating and preflighting reduces the same cap;
    // add the final reaping grace to prove the complete <=60-second bound.
    assert.ok(rootTimeouts.at(-1).seconds + (command.includes('--only-shell') ? 20 : 25) + 20 <= 60)
  }

  for (const mode of ['offline-missing', 'offline-simulation-fail']) {
    const missing = await run(`bash "${script}" playwright install-deps chromium`, { mode, bundleDir: downloaded, imageVersion: '20261018.339.1' })
    assert.equal(missing.status, 0, missing.stderr)
    assert.match(missing.summary, /path=network/)
    assert.match(missing.calls[0], /--simulate/)
    if (mode === 'offline-missing') assert.match(missing.calls[1], /--download-only/)
    assert.match(missing.calls.at(-1), /playwright install-deps chromium$/)
    assert.equal(existsSync(join(missing.state, 'offline-dpkg-started')), false, 'reject incomplete or incompatible cross-image bundles before dpkg')
    assert.doesNotMatch(missing.attempts.at(-1).config, /Dir::State::lists|Dir::Cache::archives/, 'network fallback restores runner sources and caches')
  }

  const capped = await run(`bash "${script}" playwright install-deps chromium`, { mode: 'ok', bundleDir: downloaded, imageVersion: '20261018.339.1', elapsed: [5, 35] })
  assert.equal(capped.status, 0, capped.stderr)
  assert.equal(capped.calls.length, 2, 'exhausted preflight time must prevent the offline install')
  assert.match(capped.calls[0], /--simulate/)
  assert.match(capped.calls[1], /playwright install-deps chromium$/)
  assert.match(capped.summary, /path=network/)
  assert.equal(existsSync(join(capped.state, 'offline-dpkg-started')), false)

  const absent = await run(`bash "${script}" playwright install-deps chromium`, { mode: 'ok' })
  assert.equal(absent.status, 0, absent.stderr)
  assert.deepEqual(absent.calls, ['playwright install-deps chromium'])
  assert.match(absent.summary, /path=network/)

  for (const options of [{ imageOS: 'ubuntu26' }, { runnerArch: 'ARM64' }, { imageVersion: '' }, { mode: 'offline-missing' }]) {
    const fallback = await run(`bash "${script}" playwright install-deps chromium`, { mode: 'ok', bundleDir, ...options })
    assert.equal(fallback.status, 0, `${fallback.stdout}\n${fallback.stderr}`)
    assert.match(fallback.calls.at(-1), /playwright install-deps chromium$/)
    assert.doesNotMatch(fallback.stdout, /dependencies installed offline/)
    assert.match(fallback.summary, /path=network/)
    if (options.mode === 'offline-missing') assert.ok(fallback.calls[0].includes('--no-download'), 'exercise resolution preflight, not identity rejection')
  }

  for (const imageVersion of ['20261021.1', '20261018.339.1']) {
    const dpkg = await run(`bash "${script}" apt libtest`, { mode: 'offline-install-fail', bundleDir, imageVersion })
    assert.equal(dpkg.status, 100, dpkg.stdout)
    assert.match(dpkg.stderr, /dpkg: error/)
    assert.equal(dpkg.calls.length, imageVersion === '20261021.1' ? 2 : 3)
    assert.ok(dpkg.calls.every(call => call.includes('--no-download')), 'no network fallback after an offline mutation fails')
    assert.doesNotMatch(dpkg.stdout, /dependencies installed|retrying fetch/)
    assert.equal(existsSync(join(dpkg.state, 'offline-dpkg-started')), true)
  }

  for (const filename of ['archives/fish_1_amd64.deb', 'lists/noble_Packages']) {
    const file = join(bundleDir, filename), original = readFileSync(file)
    rmSync(file)
    const incomplete = await run(`bash "${script}" playwright install-deps chromium`, { mode: 'ok', bundleDir })
    assert.equal(incomplete.status, 0, incomplete.stderr)
    assert.deepEqual(incomplete.calls, ['playwright install-deps chromium'], 'reject missing bundle files before package mutation')
    assert.ok(incomplete.stderr.includes(`${join(bundleDir, filename)}: not a regular file (missing)`), incomplete.stderr)
    assert.match(incomplete.stderr, /st_mode=unavailable, owner=unavailable/)
    assert.doesNotMatch(incomplete.stdout, /dependencies installed offline/)
    writeFileSync(file, original)
  }
  writeFileSync(join(bundleDir, 'archives/fish_1_amd64.deb'), 'corrupt')
  const corrupt = await run(`bash "${script}" playwright install-deps chromium`, { mode: 'ok', bundleDir })
  assert.equal(corrupt.status, 0, corrupt.stderr)
  assert.deepEqual(corrupt.calls, ['playwright install-deps chromium'])
  assert.ok(corrupt.stderr.includes(`${join(bundleDir, 'archives/fish_1_amd64.deb')}: bundle checksum mismatch`), corrupt.stderr)
  const corruptInfo = statSync(join(bundleDir, 'archives/fish_1_amd64.deb'))
  assert.ok(corrupt.stderr.includes(`st_mode=0o${corruptInfo.mode.toString(8)}, owner=${corruptInfo.uid}:${corruptInfo.gid}`), corrupt.stderr)

  const exhausted = await run(state => `bash "${script}" bundle "${join(state, 'bundle')}"`, {
    mode: 'bundle-fetch-fail', elapsed: [30, 0, 260, 0, 260, 0, 260, 0, 260],
  })
  assert.equal(exhausted.status, 100, exhausted.stdout)
  assert.equal(exhausted.calls.filter(call => call.includes('--download-only')).length, 4)
  assert.equal(existsSync(join(exhausted.state, 'bundle/manifest.json')), false, 'never publish a completion marker for partial downloads')
  assert.match(exhausted.stderr, /failed bundle tree \(at most 200 entries, depth 4\)/)
  const listing = exhausted.stderr.split('\n').filter(line => /^[bcdlps-][rwxStTs-]{9} \d+:\d+ \d+ /.test(line))
  assert.ok(listing.length > 0 && listing.length <= 200, 'failure diagnostics must still list files after exhausting the fetch deadline')
  assert.equal(exhausted.timeouts.at(-1).seconds, 5)
  assert.equal(exhausted.killGraces.at(-1), 2)
  const spent = Number(readFileSync(join(exhausted.state, 'clock'), 'utf8')) - 1000
  assert.equal(spent, 1070, 'exhaust the fetch deadline before attempting diagnostics')
  assert.ok(spent + 5 + 2 <= 1080, 'the failure listing and its kill grace fit inside the original 18-minute budget')
})

// AEON-1652 fix8 risk: a timed-out mirror+file attempt leaves symlinks and
// auxiliary metadata that are legitimate apt cache entries, not install inputs.
test('an interrupted first bundle fetch and a fallback fetch export only offline inputs', async () => {
  for (const timeoutStatus of [124, 137]) {
    const options = { mode: 'bundle-interrupted', mirrorList: 'hosted', rootOnly: true, timeoutStatus }
    const producer = await run(state => `bash "${script}" bundle "${join(state, 'bundle')}"`, options)
    assert.equal(producer.status, 0, `${producer.error}\n${producer.stdout}\n${producer.stderr}`)
    assert.match(readFileSync(join(producer.state, 'interrupted-tree'), 'utf8'), /killed first fetch/)
    const bundleDir = join(producer.state, 'bundle')
    const manifest = JSON.parse(readFileSync(join(bundleDir, 'manifest.json'), 'utf8'))
    const fetches = producer.attempts.filter(attempt => attempt.command.includes('--download-only'))
    assert.equal(fetches.length, 2, 'the interrupted fetch must reach a successful second attempt')
    assert.match(fetches[0].sources['ubuntu.sources'], /mirror\+file:/)
    assert.match(fetches[1].sources['ubuntu.sources'], /http:\/\/archive\.ubuntu\.com/)
    assert.equal(readFileSync(join(bundleDir, 'etc/sources.list.d/ubuntu.sources'), 'utf8'), fetches[1].sources['ubuntu.sources'])
    assert.equal(manifest.version, 2)
    assert.equal(Object.keys(manifest.debs).length, 4)
    for (const [name, digest] of Object.entries(manifest.debs)) {
      assert.match(name, /^archives\/[^/]+\.deb$/)
      assert.match(digest, /^[a-f0-9]{64}$/)
    }
    assert.ok(manifest.lists.includes('lists/mirror+file_old_binary-amd64_Packages'), 'retain usable indexes from the killed attempt')
    assert.ok(manifest.lists.includes('lists/archive.ubuntu.com_ubuntu_dists_noble_main_binary-amd64_Packages.lz4'))
    assert.ok(manifest.lists.includes('lists/archive.ubuntu.com_ubuntu_dists_noble_InRelease'))
    for (const excluded of ['lists/auxfiles', 'lists/partial', 'archives/partial']) {
      assert.equal(existsSync(join(bundleDir, excluded)), false, `${excluded} must not reach the uploader`)
    }
    const offline = await run(`bash "${script}" apt libtest`, { mode: 'always-fail', bundleDir })
    assert.equal(offline.status, 0, offline.stderr)
    assert.match(offline.stdout, /dependencies installed offline/)

    // Negative control: the former all-files selection rejects apt's local
    // mirror-list link even after the ownership and permission handoff.
    const allFiles = await run(state => {
      const copy = join(state, 'all-list-files.sh')
      writeFileSync(copy, readFileSync(script, 'utf8')
        .replace("for p in entries(root / 'lists') if index_name(p.name)", "for p in (root / 'lists').rglob('*') if p.is_file() and p.name != 'lock' and 'partial' not in p.parts")
        .replace('if name in lists and', 'if False and'))
      return `bash "${copy}" bundle "${join(state, 'bundle')}"`
    }, options)
    assert.equal(allFiles.status, 1, allFiles.stdout)
    assert.match(allFiles.stderr, /lists\/auxfiles\/_etc_apt_apt-mirrors\.txt: symlink \(st_mode=0o120[0-7]+, owner=\d+:\d+\)/)
    assert.match(allFiles.stderr, /failed bundle tree/)
    assert.match(allFiles.stderr, /[-dlrwx]{10} \d+:\d+ \d+ .*archives\/fish_1_amd64\.deb/)
    assert.equal(existsSync(join(allFiles.state, 'bundle/manifest.json')), false)
  }
})

test('bundle diagnostics identify unsafe or unreadable install inputs before dpkg', async () => {
  const producer = await run(state => `bash "${script}" bundle "${join(state, 'bundle')}"`, { mode: 'bundle-ok' })
  assert.equal(producer.status, 0, producer.stderr)
  for (const fault of ['symlink', 'directory', 'unreadable', 'outside']) {
    const bundleDir = join(producer.state, fault)
    cpSync(join(producer.state, 'bundle'), bundleDir, { recursive: true })
    const file = join(bundleDir, 'archives/fish_1_amd64.deb')
    let expectedPath = file, reason = fault
    if (fault === 'symlink') {
      rmSync(file)
      symlinkSync(join(bundleDir, 'archives/zsh_1_amd64.deb'), file)
    } else if (fault === 'directory') {
      rmSync(file)
      mkdirSync(file)
      reason = 'not a regular file'
    } else if (fault === 'unreadable') {
      chmodSync(file, 0o000)
    } else {
      const manifestFile = join(bundleDir, 'manifest.json')
      const manifest = JSON.parse(readFileSync(manifestFile, 'utf8'))
      manifest.debs['../outside.deb'] = manifest.debs['archives/fish_1_amd64.deb']
      writeFileSync(manifestFile, JSON.stringify(manifest))
      expectedPath = `${bundleDir}/../outside.deb`
      reason = 'outside root'
    }
    const rejected = await run(`bash "${script}" apt libtest`, { mode: 'ok', bundleDir })
    assert.equal(rejected.status, 0, rejected.stderr)
    assert.ok(rejected.stderr.includes(`${expectedPath}: ${reason}`), rejected.stderr)
    assert.match(rejected.stderr, /st_mode=\S+, owner=\S+/)
    assert.deepEqual(rejected.calls, ['update -qq', 'install -y -qq libtest'])
    assert.equal(existsSync(join(rejected.state, 'offline-dpkg-started')), false)
    if (fault === 'unreadable') chmodSync(file, 0o644)
  }
})
