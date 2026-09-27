// SPDX-License-Identifier: AGPL-3.0-only
import { mkdirSync } from 'node:fs'
import { resolve } from 'node:path'
import { expect, test } from '@playwright/test'

// Frozen LA1/LA2 fixtures come directly from main before IV1 (provenance in
// each fixture). They work in shallow checkouts, without Git history or an API.
// SC1 intentionally replaces identity colours and adds state marks. Preserve
// the original drawing geometry and working motion against the real fixtures.
const shots = resolve('../.agent-shots')
test.beforeAll(() => mkdirSync(shots, { recursive: true }))

for (const theme of ['light', 'dark']) {
  for (const motion of ['reduce', 'no-preference'] as const) {
    for (const hovering of [false, true]) {
      test(`LA1 and LA2 geometry and motion: ${theme}, ${motion}, hovering ${hovering}`, async ({ page }) => {
        await page.emulateMedia({ reducedMotion: motion })
        await page.setViewportSize({ width: 1000, height: 1800 })
        await page.route('**/api/preferences/agent-indicator', route => route.fulfill({ json: { value: { style: 'robot-1', hovering } } }))
        await page.goto(`/tests/indicator-harness.html?theme=${theme}`)
        await page.evaluate(async hovering => {
          const vuePath = '/node_modules/.vite/deps/vue.js'
          const { createApp, h } = await import(vuePath)
          const paths = ['/src/components/projects/LiveBot.vue', '/tests/fixtures/LA2Calm.vue', '/tests/fixtures/LA1Playful.vue']
          const [LiveBot, Calm, Playful] = await Promise.all(paths.map(path => import(path).then(module => module.default)))
          const host = document.createElement('main')
          host.id = 'fidelity'
          document.querySelector('#app')!.replaceWith(host)
          const css = document.createElement('style')
          css.textContent = 'body::before{display:none}#fidelity{position:relative;z-index:1}#fidelity h1{line-height:28px}.pairs{display:grid;grid-template-columns:repeat(2,416px);gap:12px}.pair{display:grid;grid-template-columns:repeat(2,208px);background:var(--surface-raised)}.pair h2{grid-column:1/-1;font:500 12px/16px system-ui;padding:8px;margin:0}.art{display:grid;place-items:center;width:208px;height:144px}.column-label{font-size:11px;line-height:16px;text-align:center;color:var(--ink-2)}'
          document.head.append(css)
          createApp({ render: () => [h('h1', 'Original artwork / shared state colours'), h('div', { class: 'pairs' }, [26, 64].flatMap(size => [true, false].flatMap(lead => ['', '44444444-4444-4444-8444-444444444444'].flatMap(id => [1, 5].map(robot => {
            const props = { state: 'working', size, lead, id, index: lead ? 0 : 1, indicatorStyle: 'calm', hovering }
            return h('section', { class: 'pair', 'data-case': `${robot}-${size}-${lead}-${id || 'default'}` }, [
              h('h2', `Robot ${robot} · ${size}px · ${lead ? 'lead' : 'follower'} · ${id ? 'agent hue' : 'default hue'}`),
              h('span', { class: 'column-label' }, 'Original'), h('span', { class: 'column-label' }, 'Shared state'),
              h('div', { class: 'art original' }, [h(robot === 1 ? Calm : Playful, props)]),
              h('div', { class: 'art restored' }, [h(LiveBot, { ...props, indicatorStyle: `robot-${robot}` })]),
            ])
          })))))] }).mount(host)
        }, hovering)
        await expect(page.locator('.pair')).toHaveCount(16)
        await expect(page.locator('.restored .indicator')).toHaveCount(16)
        await page.mouse.move(0, 0)
        for (const time of motion === 'reduce' ? [0] : [0, 1000, 4300]) {
          await page.evaluate(at => { for (const animation of document.getAnimations()) { animation.pause(); animation.currentTime = at } }, time)
          for (const pair of await page.locator('.pair').all()) {
            const label = await pair.getAttribute('data-case')
            const geometry = await pair.evaluate(el => [...el.querySelectorAll('.art')].map(art => {
              const robot = art.querySelector('.robot5 .bot, .playful-bot .bot, svg')!
              const bounds = art.getBoundingClientRect()
              return [...robot.querySelectorAll('*')].filter(node => !node.closest('.agent-state-mark')).map(node => {
                const rect = node.getBoundingClientRect()
                const style = getComputedStyle(node)
                return {
                  tag: node.tagName,
                  shape: ['d', 'cx', 'cy', 'r', 'x', 'y', 'width', 'height', 'rx', 'pathLength'].map(name => node.getAttribute(name)),
                  // Browser positions round to CSS pixel fractions; compare motion at
                  // the same time, independent of the two columns' page position.
                  box: [rect.x - bounds.x, rect.y - bounds.y, rect.width, rect.height].map(n => Math.round(n * 100) / 100),
                  transform: style.transform, duration: style.animationDuration, delay: style.animationDelay,
                }
              })
            }))
            expect(geometry[1], `${label}: artwork and motion at ${time}ms`).toEqual(geometry[0])
          }
          await page.locator('#fidelity').screenshot({ path: resolve(shots, `iv1-fidelity-${theme}-${motion}-${hovering}-${time}.png`), animations: 'allow' })
        }
      })
    }
  }
}
