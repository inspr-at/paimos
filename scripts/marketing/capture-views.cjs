// PAI-746 marketing captures — seeded demo workspace only, never a live
// instance. Runbook #3594: 1600x1000 @2x, dev chrome hidden, provenance
// in every caption.
const { chromium } = require('playwright');
const fs = require('fs');
const path = require('path');

const API = 'http://localhost:8888';
const APP = 'http://localhost:5173';
const OUT = process.env.OUT_DIR || '/tmp/pai746';
const TOKEN = process.env.PAIMOS_DEV_LOGIN_TOKEN || '';
const AGENT_MODE_FIXTURE = JSON.parse(fs.readFileSync(
  path.join(__dirname, '../../backend/contracts/fixtures/agent-mode/snapshot-v1-10.json'),
  'utf8',
));

if (!TOKEN) throw new Error('PAIMOS_DEV_LOGIN_TOKEN is required');

// Anything that screams "developer laptop" rather than "product".
const HIDE_CHROME = `
  .dev-login-banner, .totp-warning, .aml-totp-warning, .app-dev-banner,
  [class*="dev-login"], [class*="staging-banner"] { display: none !important; }
  /* Freeze anything that could blur a 2x capture mid-animation. */
  *, *::before, *::after {
    animation-duration: 0s !important; animation-delay: 0s !important;
    transition-duration: 0s !important; transition-delay: 0s !important;
  }
  /* Hide carets so re-shoots are byte-stable. */
  * { caret-color: transparent !important; }
`;

async function shoot(page, name, { path: route, prepare, clip, full = false } = {}) {
  if (route) {
    await page.goto(`${APP}${route}`, { waitUntil: 'domcontentloaded' });
    await page.waitForLoadState('networkidle').catch(() => {});
  }
  await page.addStyleTag({ content: HIDE_CHROME });
  await page.waitForTimeout(600);
  if (prepare) await prepare(page);
  await page.waitForTimeout(500);
  const file = `${OUT}/${name}.png`;
  await page.screenshot({ path: file, fullPage: full, clip, animations: 'disabled', caret: 'hide' });
  const { size } = fs.statSync(file);
  console.log(`  ✓ ${name}.png  ${(size / 1024).toFixed(0)} KB`);
}

function normalizedBox(box) {
  if (!box) throw new Error('required capture landmark is not visible');
  return {
    x: Number((box.x / 1600).toFixed(4)),
    y: Number((box.y / 1000).toFixed(4)),
    width: Number((box.width / 1600).toFixed(4)),
    height: Number((box.height / 1000).toFixed(4)),
  };
}

function currentAgentModeFixture() {
  return JSON.parse(JSON.stringify(AGENT_MODE_FIXTURE));
}

(async () => {
  fs.mkdirSync(OUT, { recursive: true });
  const browser = await chromium.launch();
  const ctx = await browser.newContext({
    viewport: { width: 1600, height: 1000 },
    deviceScaleFactor: 2,
    colorScheme: 'light',
    reducedMotion: 'reduce',
  });

  // Authenticate through the dev-login route (seeded fixture user).
  const res = await ctx.request.post(`${API}/api/auth/dev-login`, {
    data: { username: 'dev_admin', token: TOKEN },
  });
  if (!res.ok()) throw new Error(`dev-login failed: ${res.status()}`);
  const me = await ctx.request.get(`${API}/api/auth/me`);
  const permissionsEpoch = me.headers()['x-permissions-epoch'];
  if (!me.ok() || !permissionsEpoch) throw new Error('dev-login authority proof is missing');

  const page = await ctx.newPage();
  await page.addInitScript(() => {
    class CaptureEventSource extends EventTarget {
      static CONNECTING = 0;
      static OPEN = 1;
      static CLOSED = 2;
      constructor(url) {
        super();
        this.url = String(url);
        this.withCredentials = false;
        this.readyState = CaptureEventSource.OPEN;
        this.onopen = null;
        this.onmessage = null;
        this.onerror = null;
        queueMicrotask(() => this.dispatchEvent(new Event('open')));
      }
      close() { this.readyState = CaptureEventSource.CLOSED; }
    }
    Object.defineProperty(window, 'EventSource', { configurable: true, value: CaptureEventSource });
  });
  await page.route('**/api/agent-mode/deliveries**', async (route) => {
    if (new URL(route.request().url()).pathname !== '/api/agent-mode/deliveries') {
      await route.continue();
      return;
    }
    await route.fulfill({
      status: 200,
      json: currentAgentModeFixture(),
      headers: { 'X-Permissions-Epoch': permissionsEpoch },
    });
  });

  await shoot(page, 'product-surface', {
    path: '/issues/PAI-1',
    prepare: async (p) => {
      await p.locator('#ai-workbench').waitFor({ state: 'visible' });
      await p.locator('.am-section').waitFor({ state: 'visible' });
      const tasks = p.getByText(/^Tasks$/i).first();
      if (!(await tasks.count())) throw new Error('TASKS framing anchor is missing');
      await tasks.evaluate((element) => {
        const scroller = element.closest('.main-content');
        if (!scroller) throw new Error('main content scroller is missing');
        const targetTop = scroller.getBoundingClientRect().top + 6;
        scroller.scrollTop += element.getBoundingClientRect().top - targetTop;
      });
      await p.waitForTimeout(400);

      const implement = p.getByRole('button', { name: /^Implement (this|\+ deploy)$/ }).first();
      const taskBox = await tasks.boundingBox();
      const metadata = {
        schemaVersion: 1,
        viewport: { width: 1600, height: 1000, deviceScaleFactor: 2 },
        framing: {
          anchor: 'TASKS',
          tasksTop: Number((((taskBox && taskBox.y) || 0) / 1000).toFixed(4)),
        },
        landmarks: {
          issueContext: normalizedBox(await p.locator('.iw-context').boundingBox()),
          executionControl: normalizedBox(await implement.boundingBox()),
          applicableMemories: normalizedBox(await p.locator('.am-section').boundingBox()),
        },
      };
      fs.writeFileSync(`${OUT}/capture-surface.json`, `${JSON.stringify(metadata, null, 2)}\n`);
    },
  });

  await shoot(page, 'ui-agent-mode', {
    path: '/agent-mode?detail=10',
    prepare: async (p) => {
      await p.locator('.am-selection-anchor').waitFor({ state: 'visible' });
      await p.locator('.am-project-picker__trigger').waitFor({ state: 'visible' });
      await p.locator('.am-hints-current').waitFor({ state: 'visible' });
    },
  });

  await shoot(page, 'ui-issues', { path: '/issues' });

  await shoot(page, 'ui-board', {
    path: '/sprint-board',
    prepare: async (p) => {
      // The board defaults to a sprint with nothing in it; pick the one
      // with a real spread across columns.
      const sel = p.locator('select').first();
      if (await sel.count()) {
        await sel.selectOption({ label: /26S09/ }).catch(async () => {
          const opts = await sel.locator('option').allTextContents();
          const i = opts.findIndex((o) => o.includes('26S09'));
          if (i >= 0) await sel.selectOption({ index: i });
        });
        await p.waitForTimeout(1200);
      }
    },
  });

  await shoot(page, 'ui-search', {
    path: '/issues',
    prepare: async (p) => {
      const input = p.locator('.ah-search-input');
      await input.click();
      await input.type('voice intake', { delay: 45 });
      await p.waitForTimeout(1200);
    },
  });

  await browser.close();
  console.log(`\nwrote to ${OUT}`);
})().catch((e) => {
  console.error('capture failed:', e.message);
  process.exit(1);
});
