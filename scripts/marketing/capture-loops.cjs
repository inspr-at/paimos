// PAI-694 — short seeded product flows for paimos.inspr.at.
//
// Playwright records WebM because that is Chromium's native output. The
// orchestrating shell script trims the measured setup pre-roll and transcodes
// each recording to a fast-start H.264 MP4 for Safari and the public site.
// This script only ever talks to the local dev stack and synthetic fixture DB.
const { chromium } = require('playwright');
const fs = require('fs');

const API = 'http://localhost:8888';
const APP = 'http://localhost:5173';
const OUT = process.env.OUT_DIR || '/tmp/paimos-loops';
const TOKEN = process.env.PAIMOS_DEV_LOGIN_TOKEN || '';

if (!TOKEN) throw new Error('PAIMOS_DEV_LOGIN_TOKEN is required');

const VIEWPORT = { width: 1280, height: 800 };
const HIDE_CHROME = `
  .dev-login-banner, .totp-warning, .app-dev-banner,
  [class*="dev-login"], [class*="staging-banner"] { display: none !important; }
  *, *::before, *::after {
    animation-duration: 0s !important; animation-delay: 0s !important;
    transition-duration: 0s !important; transition-delay: 0s !important;
  }
  * { caret-color: transparent !important; }
`;

async function authenticatedContext(browser, videoDir) {
  const context = await browser.newContext({
    viewport: VIEWPORT,
    colorScheme: 'light',
    reducedMotion: 'reduce',
    recordVideo: { dir: videoDir, size: VIEWPORT },
  });
  const response = await context.request.post(`${API}/api/auth/dev-login`, {
    data: { username: 'dev_admin', token: TOKEN },
  });
  if (!response.ok()) throw new Error(`dev-login failed: ${response.status()}`);
  return context;
}

async function record(browser, name, prepare, perform) {
  const context = await authenticatedContext(browser, OUT);
  const startedAt = Date.now();
  const page = await context.newPage();
  await prepare(page);
  await page.addStyleTag({ content: HIDE_CHROME });
  await page.waitForTimeout(350);
  const trimStartSeconds = Number(((Date.now() - startedAt) / 1000).toFixed(3));
  await perform(page);
  const video = page.video();
  if (!video) throw new Error(`${name}: Playwright video recorder is unavailable`);
  await page.close();
  await video.saveAs(`${OUT}/${name}.webm`);
  await context.close();
  const bytes = fs.statSync(`${OUT}/${name}.webm`).size;
  console.log(`  ✓ ${name}.webm  ${(bytes / 1024).toFixed(0)} KB`);
  return { trimStartSeconds };
}

(async () => {
  fs.mkdirSync(OUT, { recursive: true });
  const browser = await chromium.launch();
  const recordings = {};

  recordings['loop-issue-workbench'] = await record(
    browser,
    'loop-issue-workbench',
    async (page) => {
      await page.goto(`${APP}/issues/PAI-28`, { waitUntil: 'domcontentloaded' });
      await page.waitForLoadState('networkidle').catch(() => {});
      await page.locator('#ai-workbench').waitFor({ state: 'attached' });
      await page.evaluate(() => {
        const scroller = document.querySelector('.main-content');
        if (scroller) scroller.scrollTop = 0;
      });
    },
    async (page) => {
      await page.waitForTimeout(900);
      await page.locator('#ai-workbench').evaluate((element) => {
        const scroller = element.closest('.main-content');
        if (!scroller) throw new Error('main content scroller is missing');
        const scrollerTop = scroller.getBoundingClientRect().top;
        const target = scroller.scrollTop + element.getBoundingClientRect().top - scrollerTop - 12;
        scroller.scrollTo({ top: target, behavior: 'smooth' });
      });
      await page.waitForTimeout(2600);
      await page.getByRole('button', { name: /^Implement (this|\+ deploy)$/ }).first().focus();
      await page.waitForTimeout(1900);
    },
  );

  recordings['loop-search-navigate'] = await record(
    browser,
    'loop-search-navigate',
    async (page) => {
      await page.goto(`${APP}/issues`, { waitUntil: 'domcontentloaded' });
      await page.waitForLoadState('networkidle').catch(() => {});
      await page.locator('.ah-search-input').waitFor({ state: 'visible' });
    },
    async (page) => {
      const input = page.locator('.ah-search-input');
      await input.click();
      await input.type('PAI-28', { delay: 150 });
      await page.locator('.search-palette .sp-item').first().waitFor({ state: 'visible' });
      await page.waitForTimeout(1100);
      await page.keyboard.press('Enter');
      await page.waitForURL((url) => url.pathname !== '/issues');
      await page.waitForLoadState('networkidle').catch(() => {});
      await page.waitForTimeout(3500);
    },
  );

  await browser.close();
  fs.writeFileSync(
    `${OUT}/capture-loops.json`,
    `${JSON.stringify({ schemaVersion: 1, viewport: VIEWPORT, recordings }, null, 2)}\n`,
  );
  console.log(`\nwrote video sources to ${OUT}`);
})().catch((error) => {
  console.error('loop capture failed:', error.message);
  process.exit(1);
});
