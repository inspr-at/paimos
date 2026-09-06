// Keep marketing's empty Home capture on the actual Habitat contract.
// This module has no browser dependency and never launches a browser itself.
const HOME_SELECTORS = Object.freeze({
  home: '.habitat-home',
  summary: '.habitat-home [aria-label="Workspace summary"]',
  projects: '.habitat-home [aria-label="Your projects"]',
  setup: '.habitat-home .habitat-welcome .habitat-primary',
});

async function prepareHabitatHome(page) {
  for (const selector of Object.values(HOME_SELECTORS)) {
    await page.locator(selector).waitFor({ state: 'visible' });
  }
  const setup = page.locator(HOME_SELECTORS.setup);
  if (!(await setup.isEnabled())) throw new Error('Habitat Home setup action is not available');
  await page.evaluate(() => window.scrollTo(0, 0));
}

module.exports = { HOME_SELECTORS, prepareHabitatHome };
