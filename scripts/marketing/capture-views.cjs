// PAI-746 marketing captures — seeded demo workspace only, never a live
// instance. Runbook #3594: 1600x1000 @2x, dev chrome hidden, provenance
// in every caption.
const { chromium } = require('playwright');
const fs = require('fs');

const API = 'http://localhost:8888';
const APP = 'http://localhost:5173';
const OUT = process.env.OUT_DIR || '/tmp/pai746';
const TOKEN = process.env.PAIMOS_DEV_LOGIN_TOKEN || '';

// Anything that screams "developer laptop" rather than "product".
const HIDE_CHROME = `
  .dev-login-banner, .totp-warning, .app-dev-banner,
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

  const page = await ctx.newPage();

  await shoot(page, 'ui-dashboard', { path: '/' });

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

  // The 5.x story the current site never shows.
  await shoot(page, 'ui-voice-intake', {
    path: '/intake',
    prepare: async (p) => {
      // Expand the right-hand cards so the seeded artifacts are visible.
      for (const name of ['Impact Analysis', 'Understanding Check', 'Ticket Preview']) {
        const h = p.locator(`text=${name}`).first();
        if (await h.count()) { await h.click().catch(() => {}); await p.waitForTimeout(250); }
      }
      await p.waitForTimeout(600);
    },
  });

  await browser.close();
  console.log(`\nwrote to ${OUT}`);
})().catch((e) => {
  console.error('capture failed:', e.message);
  process.exit(1);
});
