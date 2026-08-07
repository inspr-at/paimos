// PAI-746 — the Voice Intake hero shot.
//
// The workbench resumes a session only from in-memory store state, so a
// cold page load always renders the empty state. To show the product
// actually working we: create a session through the real UI, seed that
// session's artifacts into the local demo DB, then SPA-navigate away and
// back so onMounted re-hydrates from the DB and the cards fill in.
//
// Everything here runs against the gitignored local dev DB and the
// seeded demo workspace. Nothing is copied from a live instance.
const { chromium } = require('playwright');
const { execFileSync } = require('child_process');
const fs = require('fs');

const API = 'http://localhost:8888';
const APP = 'http://localhost:5173';
const DB = '/Users/markus/Code/paimos/data/paimos.db';
const OUT = process.env.OUT_DIR || '/tmp/pai746';
const TOKEN = process.env.PAIMOS_DEV_LOGIN_TOKEN || '';

const HIDE_CHROME = `
  .dev-login-banner, .totp-warning, [class*="dev-login"] { display: none !important; }
  *, *::before, *::after { animation-duration: 0s !important; transition-duration: 0s !important; }
  * { caret-color: transparent !important; }
`;

const sql = (q) => execFileSync('sqlite3', [DB, q], { encoding: 'utf8' }).trim();

(async () => {
  fs.mkdirSync(OUT, { recursive: true });
  const browser = await chromium.launch();
  const ctx = await browser.newContext({
    viewport: { width: 1600, height: 1000 },
    deviceScaleFactor: 2,
    colorScheme: 'light',
    reducedMotion: 'reduce',
  });
  const res = await ctx.request.post(`${API}/api/auth/dev-login`, {
    data: { username: 'dev_admin', token: TOKEN },
  });
  if (!res.ok()) throw new Error(`dev-login failed: ${res.status()}`);

  const page = await ctx.newPage();
  await page.goto(`${APP}/intake`, { waitUntil: 'domcontentloaded' });
  await page.waitForLoadState('networkidle').catch(() => {});
  await page.addStyleTag({ content: HIDE_CHROME });

  // 1. Create a real session through the UI.
  await page.getByRole('button', { name: /Start Talking/ }).click();
  await page.waitForTimeout(1500);

  const sid = sql("SELECT id FROM intake_sessions WHERE user_id=9001 ORDER BY id DESC LIMIT 1;");
  if (!sid) throw new Error('no session was created');
  console.log(`  session created: ${sid}`);

  // 2. Move the seeded artifacts onto that session id.
  // COPY from the template session (id 1), never move — moving empties
  // the template and every later run captures an empty workbench.
  sql(`DELETE FROM intake_events WHERE session_id=${sid};`);
  sql(`INSERT INTO intake_events (session_id,seq,kind,source,label,payload_json,created_at)
       SELECT ${sid},seq,kind,source,label,payload_json,created_at
         FROM intake_events WHERE session_id=1;`);
  const transcript = sql("SELECT transcript FROM intake_sessions WHERE id=1;");
  execFileSync('sqlite3', [DB,
    `UPDATE intake_sessions SET transcript=(SELECT transcript FROM intake_sessions WHERE id=1),
       transcript_bytes=(SELECT transcript_bytes FROM intake_sessions WHERE id=1),
       rev=9, detected_project_id=2, detected_score=94,
       session_prompt_tokens=4120, session_completion_tokens=1880
     WHERE id=${sid};`]);
  console.log(`  artifacts attached (${transcript.length} chars of transcript)`);

  // 3. SPA-navigate away and back: onMounted re-hydrates from the DB.
  await page.getByRole('link', { name: /^Issues$/ }).click();
  await page.waitForTimeout(900);
  await page.getByRole('link', { name: /Voice Intake/ }).click();
  await page.waitForTimeout(2500);
  await page.addStyleTag({ content: HIDE_CHROME });

  // Expand the artifact cards.
  for (const name of ['Impact Analysis', 'Understanding Check', 'Ticket Preview']) {
    const h = page.locator(`text=${name}`).first();
    if (await h.count()) { await h.click().catch(() => {}); await page.waitForTimeout(300); }
  }
  await page.waitForTimeout(800);

  await page.screenshot({ path: `${OUT}/ui-voice-intake.png`, animations: 'disabled', caret: 'hide' });
  console.log(`  ✓ ui-voice-intake.png`);

  await browser.close();
})().catch((e) => { console.error('failed:', e.message); process.exit(1); });
