// SPDX-License-Identifier: AGPL-3.0-only
import { mkdirSync, writeFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { expect, test } from '@playwright/test'

// Frozen LA1/LA2 fixtures come directly from main before IV1 (provenance in
// each fixture). They work in shallow checkouts, without Git history or an API.
// Compare the real old components, not a handwritten copy of the new artwork.
const shots = resolve('../.agent-shots')
test.beforeAll(() => mkdirSync(shots, { recursive: true }))

for (const theme of ['light', 'dark']) {
  for (const motion of ['reduce', 'no-preference'] as const) {
    for (const hovering of [false, true]) {
      test(`LA1 and LA2 pixel identity: ${theme}, ${motion}, hovering ${hovering}`, async ({ page }) => {
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
          createApp({ render: () => [h('h1', 'Original main / restored robot'), h('div', { class: 'pairs' }, [26, 64].flatMap(size => [true, false].flatMap(lead => ['', '44444444-4444-4444-8444-444444444444'].flatMap(id => [1, 5].map(robot => {
            const props = { state: 'working', size, lead, id, index: lead ? 0 : 1, indicatorStyle: 'calm', hovering }
            return h('section', { class: 'pair', 'data-case': `${robot}-${size}-${lead}-${id || 'default'}` }, [
              h('h2', `Robot ${robot} · ${size}px · ${lead ? 'lead' : 'follower'} · ${id ? 'agent hue' : 'default hue'}`),
              h('span', { class: 'column-label' }, 'Original'), h('span', { class: 'column-label' }, 'Restored'),
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
            // Render each existing artwork tree at the same initial coordinates.
            // Moving an already composited SVG can reuse fractional raster tiles;
            // that tests the browser's layer cache rather than the artwork.
            const capture = async (side: string) => {
              await pair.locator(side).evaluate((art, at) => {
                document.querySelector('#pixel-probe')?.remove()
                const probe = document.createElement('div')
                probe.id = 'pixel-probe'
                probe.style.cssText = 'position:fixed;left:16px;top:16px;z-index:9999;width:208px;height:144px;display:grid;place-items:center;background:var(--surface-raised)'
                probe.append(art.firstElementChild!.cloneNode(true))
                document.body.append(probe)
                for (const animation of probe.getAnimations({ subtree: true })) { animation.pause(); animation.currentTime = at }
              }, time)
              return page.locator('#pixel-probe').screenshot({ animations: 'allow' })
            }
            const original = await capture('.original')
            const restored = await capture('.restored')
            await page.evaluate(() => document.querySelector('#pixel-probe')?.remove())
            if (!original.equals(restored)) {
              writeFileSync(resolve(shots, `mismatch-${theme}-${motion}-${hovering}-${label}-${time}-original.png`), original)
              writeFileSync(resolve(shots, `mismatch-${theme}-${motion}-${hovering}-${label}-${time}-restored.png`), restored)
              writeFileSync(resolve(shots, `mismatch-${theme}-${motion}-${hovering}-${label}-${time}.json`), JSON.stringify(await pair.evaluate(el => [...el.querySelectorAll('.art')].map(art => [...art.querySelectorAll('*')].map(node => {
                const style = getComputedStyle(node)
                const rect = node.getBoundingClientRect(), bounds = art.getBoundingClientRect()
                return { tag: node.tagName, class: node.getAttribute('class'), x: rect.x - bounds.x, y: rect.y - bounds.y, width: rect.width, height: rect.height, fill: style.fill, stroke: style.stroke, opacity: style.opacity, transform: style.transform, animation: style.animationName, delay: style.animationDelay }
              }))), null, 2))
            }
            expect(restored.equals(original), `${label}: exact raster match at ${time}ms`).toBe(true)
          }
          await page.locator('#fidelity').screenshot({ path: resolve(shots, `iv1-fidelity-${theme}-${motion}-${hovering}-${time}.png`), animations: 'allow' })
        }
      })
    }
  }
}
