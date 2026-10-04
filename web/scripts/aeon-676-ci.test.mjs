// SPDX-License-Identifier: AGPL-3.0-only
import test from 'node:test'
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'

const requiredSpecs = ['clip-tip', 'aeon-632b-clip', 'key-trim', 'model-prefs']
  .map(name => `tests/${name}.spec.ts`)

test('AEON-676 regressions reach the required hosted web gate through the committed runner', () => {
  const workflow = readFileSync(new URL('../../.github/workflows/ci.yml', import.meta.url), 'utf8')
  const job = id => {
    const match = new RegExp(`^  ${id}:\\n([\\s\\S]*?)(?=^  [a-z][a-z-]*:|$(?![\\s\\S]))`, 'm').exec(workflow)
    assert.ok(match, `Missing CI job ${id}`)
    return match[1]
  }
  // Other dependencies may grow; these gates must remain required.
  const requireDependencies = (id, required) => {
    const needs = /^    needs: (?:\[([^\]\n]*)\]|([a-z][a-z-]*))$/m.exec(job(id))
    assert.ok(needs, `${id} must declare its dependencies`)
    const dependencies = (needs[1] ?? needs[2]).split(',').map(name => name.trim())
    for (const name of required) {
      assert.ok(dependencies.includes(name), `${id} must require ${name}`)
    }
  }
  assert.match(job('web'), /if: always\(\)/)
  requireDependencies('web', ['web-setup', 'web-shard'])
  assert.match(job('web'), /test "\$\{WEB_SETUP\}" = success/)
  assert.match(job('web'), /test "\$\{WEB_SHARD\}" = success/)
  assert.match(job('web-setup'), /npm run test:unit/)
  const shard = job('web-shard')
  requireDependencies('web-shard', ['web-setup'])
  assert.match(shard, /runs-on: ubuntu-latest/)
  assert.match(shard, /npm --prefix web run ci:web:shard -- \$\{\{ matrix.shard \}\}\/12/)
  assert.match(shard, /exit "\$code"/)
  assert.doesNotMatch(shard, /continue-on-error:\s*true/)
  const scripts = JSON.parse(readFileSync(new URL('../package.json', import.meta.url))).scripts
  assert.equal(scripts['ci:web:shard'], 'node scripts/ci-web-shard.mjs')
  assert.ok(scripts['test:unit'].startsWith('npm run ci:web:shard:test &&'))
  assert.ok(scripts['ci:web:shard:test'].includes('scripts/aeon-676-ci.test.mjs'))
  const quarantine = JSON.parse(readFileSync(new URL('../../scripts/ci-quarantine.json', import.meta.url)))
  assert.ok(!quarantine.entries.some(entry => entry.owner === 'AEON-676' ||
    requiredSpecs.some(file => entry.id.includes(file.split('/').at(-1)))), 'AEON-676 specs must block CI on failure')
})

test('default twelve-shard execution selects all four specs once and preserves each failure', async () => {
  const { loadManifest, main, webRoot } = await import('./ci-web-shard.mjs')
  const manifest = loadManifest()
  assert.equal(manifest.defaultShards, 12)
  const plans = []
  assert.equal(await main(['--list', '--strict', '--shards', '12'], {
    manifest, env: {}, out: value => plans.push(...JSON.parse(value)),
    run: async () => assert.fail('Listing launched a browser'),
  }), 0)
  assert.equal(plans.length, 12)
  for (const file of requiredSpecs) {
    const owners = plans.filter(plan => plan.commands.some(command => command.files.includes(file)))
    assert.equal(owners.length, 1, `${file} must gate exactly once without --all`)
    const selected = owners[0].commands.filter(command => command.files.includes(file))
    assert.equal(selected.length, 1)
    const command = selected[0]
    assert.equal(command.hostedOnly, true)
    assert.ok(command.args.includes('--workers=2'))
    assert.ok(command.args.includes('--retries=0'))
    assert.ok(command.args.some(arg => arg.startsWith('^') && new RegExp(arg).test(`${webRoot}${file}`)))
    assert.ok(!(manifest.exclusions ?? []).some(exclusion => exclusion.file === file))
    const calls = []
    const code = await main([`${owners[0].index}/12`, '--strict'], {
      manifest, env: { CI: '1', RUNNER_ENVIRONMENT: 'github-hosted', PW_RETRIES: '0' }, out: () => {},
      run: async args => {
        calls.push(args)
        return { code: args.some(arg => arg.startsWith('^') && new RegExp(arg).test(`${webRoot}${file}`)) ? 7 : 0 }
      },
    })
    assert.equal(code, 7, `${file} failure must fail the shard`)
    assert.equal(calls.length, owners[0].commands.length, 'later groups must still run')
  }
})
