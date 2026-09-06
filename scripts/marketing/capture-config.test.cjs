const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');
const { spawnSync } = require('node:child_process');
const { captureConfig } = require('./capture-config.cjs');
const { HOME_SELECTORS, prepareHabitatHome } = require('./capture-home.cjs');
const ROOT = path.resolve(__dirname, '../..');

test('defaults preserve the existing local demo stack', () => {
  const config = captureConfig({});
  assert.equal(config.apiUrl, 'http://localhost:8888');
  assert.equal(config.appUrl, 'http://localhost:5173');
  assert.equal(config.database, path.join(ROOT, 'data/paimos.db'));
  assert.equal(config.siteRoot, path.resolve(ROOT, '../inspr-at'));
});

test('isolated ports, data and site remain one explicit configuration', () => {
  const config = captureConfig({
    PAIMOS_CAPTURE_API_URL: 'http://127.0.0.1:59441/',
    PAIMOS_CAPTURE_APP_URL: 'http://127.0.0.1:59442',
    PAIMOS_CAPTURE_DATA_DIR: '/tmp/paimos capture/data',
    PAIMOS_CAPTURE_SITE_DIR: '/tmp/reviewed-site',
  });
  assert.deepEqual(config, {
    apiUrl: 'http://127.0.0.1:59441', appUrl: 'http://127.0.0.1:59442',
    dataDir: '/tmp/paimos capture/data', database: '/tmp/paimos capture/data/paimos.db',
    siteRoot: '/tmp/reviewed-site',
  });
  assert.equal(captureConfig({ PAIMOS_CAPTURE_SITE_DIR: '/tmp/env-site' }, '/tmp/explicit-site').siteRoot, '/tmp/explicit-site');
});

test('remote, credential-bearing and non-origin URLs fail before capture', () => {
  for (const value of ['https://pm.barta.cm', 'http://localhost.example:8888', 'http://user:password@localhost:8888', 'file:///tmp/app', 'http://localhost:8888/api', 'http://localhost:8888/?token=x', 'http://localhost:8888/#x']) {
    assert.throws(() => captureConfig({ PAIMOS_CAPTURE_API_URL: value }), /loopback HTTP origin/);
  }
  assert.throws(() => captureConfig({ PAIMOS_CAPTURE_APP_URL: 'http://127.0.0.1:5173' }), /same loopback host/);
  assert.throws(() => captureConfig({ PAIMOS_CAPTURE_DATA_DIR: 'relative/data' }), /absolute path/);
  assert.throws(() => captureConfig({ PAIMOS_CAPTURE_DATA_DIR: '/tmp/data\nother' }), /absolute path/);
});

test('wrapper dry run never reads tokens, boots browsers, polishes data or writes site files', () => {
  const temp = fs.mkdtempSync(path.join(os.tmpdir(), 'paimos-capture-config-'));
  try {
    const site = path.join(temp, 'absent-site');
    const data = path.join(temp, 'absent-data');
    const result = spawnSync('bash', [path.join(__dirname, 'refresh-captures.sh'), '--dry-run', site], {
      cwd: ROOT, encoding: 'utf8', env: {
        ...process.env, PAIMOS_DEV_LOGIN_TOKEN: '', PAIMOS_DEV_LOGIN_TOKEN_FILE: path.join(temp, 'absent-secret'),
        PAIMOS_CAPTURE_API_URL: 'http://127.0.0.1:59441', PAIMOS_CAPTURE_APP_URL: 'http://127.0.0.1:59442',
        PAIMOS_CAPTURE_DATA_DIR: data,
      },
    });
    assert.equal(result.status, 0, result.stderr);
    assert.equal(JSON.parse(result.stdout).database, path.join(data, 'paimos.db'));
    assert.equal(JSON.parse(result.stdout).siteRoot, site);
    assert.deepEqual(fs.readdirSync(temp), []);
  } finally { fs.rmdirSync(temp); }
});

test('wrapper refuses off-origin configuration even in dry run', () => {
  const result = spawnSync('bash', [path.join(__dirname, 'refresh-captures.sh'), '--dry-run'], {
    encoding: 'utf8', env: { ...process.env, PAIMOS_CAPTURE_API_URL: 'http://example.com' },
  });
  assert.notEqual(result.status, 0);
  assert.match(result.stderr, /loopback HTTP origin/);
});

test('Home capture contract points at present Habitat markup, not retired session-home classes', () => {
  const source = fs.readFileSync(path.join(ROOT, 'frontend/src/components/habitat/HabitatHome.vue'), 'utf8');
  assert.match(source, /class="habitat-home"/);
  assert.match(source, /aria-label="Workspace summary"/);
  assert.match(source, /aria-label="Your projects"/);
  assert.match(source, /class="habitat-welcome"/);
  assert.match(source, /<button class="habitat-primary" type="button" @click="emit\('assign'\)"/);
  assert.ok(Object.values(HOME_SELECTORS).every(selector => !selector.includes('.p6-')));
});

test('Home preparation requires visible functional setup and restores scroll position', async () => {
  const visited = [];
  let scrolled = false;
  const page = {
    locator(selector) { return {
      async waitFor(options) { assert.equal(options.state, 'visible'); visited.push(selector); },
      async isEnabled() { return true; },
    }; },
    async evaluate() { scrolled = true; },
  };
  await prepareHabitatHome(page);
  assert.equal(visited.length, 4);
  assert.equal(scrolled, true);
  page.locator = () => ({ async waitFor() {}, async isEnabled() { return false; } });
  await assert.rejects(prepareHabitatHome(page), /setup action is not available/);
});
