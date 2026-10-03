// SPDX-License-Identifier: AGPL-3.0-only
import { readFileSync } from 'node:fs'
import vm from 'node:vm'
import ts from 'typescript'
import { describe, expect, it, vi } from 'vitest'

// Execute the component's actual handler with its reactive dependencies stubbed.
// Browser specs separately prove native scrolling and control geometry.
function handler(file: string, name: string, dependencies: Record<string, unknown>) {
  const source = readFileSync(new URL(`../src/components/${file}`, import.meta.url), 'utf8')
  const script = source.split('<script setup lang="ts">')[1]!.split('</script>')[0]!
  const ast = ts.createSourceFile(file, script, ts.ScriptTarget.Latest, true, ts.ScriptKind.TS)
  const declaration = ast.statements.find(node => ts.isFunctionDeclaration(node) && node.name?.text === name)
  if (!declaration) throw new Error(`Missing ${name} handler in ${file}`)
  const js = ts.transpile(declaration.getText(ast), { target: ts.ScriptTarget.ES2022 })
  return vm.runInNewContext(`${js}\n${name}`, dependencies) as (event: ReturnType<typeof keyEvent>) => void
}
class Target {
  constructor(private readonly region: string) {}
  tagName = 'DIV'
  closest(selector: string) { return selector === this.region ? this : null }
}
function keyEvent(key: string, region: string, shiftKey = false) {
  return { key, target: new Target(region), shiftKey, metaKey: false, ctrlKey: false, altKey: false,
    defaultPrevented: false, repeat: false, isComposing: false, preventDefault: vi.fn(), stopPropagation: vi.fn() }
}
const scrolling = ['ArrowDown', 'ArrowUp', 'ArrowLeft', 'ArrowRight', ' ', 'PageDown', 'PageUp', 'Home', 'End']
function desk() {
  const choose = vi.fn(), navigate = vi.fn(), close = vi.fn(), submit = vi.fn()
  const keys = handler('agents/DecisionDeskMemo.vue', 'keys', {
    Element: Target, viewing: { value: false }, fieldTarget: () => false, submitModifier: () => false,
    mac: false, pager: { value: false }, outcomeKeys: ['O', 'A', 'R', 'D'],
    item: { value: { choices: [{ id: 'first' }, { id: 'second' }] } }, draft: { value: { optionId: 'first' } },
    choose, navigate, close, submit,
  })
  return { keys, choose, navigate, close, submit }
}
function walker() {
  const toggle = vi.fn(), stepScreen = vi.fn(), step = vi.fn(), feature = vi.fn(), close = vi.fn(), dismissHint = vi.fn()
  const keys = handler('journey/ReleaseWalker.vue', 'keydown', {
    Element: Target, sheet: { value: false }, searching: { value: false }, compare: { value: false }, zoom: { value: false },
    dialog: { value: {} }, toggle, stepScreen, step, feature, close, dismissHint,
  })
  return { keys, toggle, stepScreen, step, feature, close }
}

describe('Decision Desk selected-answer scrolling', () => {
  for (const key of scrolling) for (const shift of [false, true]) it(`keeps ${shift ? 'Shift+' : ''}${key} native without changing the answer or memo`, () => {
    const { keys, choose, navigate, submit } = desk(), event = keyEvent(key, '.answer-slot', shift)
    keys(event)
    expect(event.preventDefault).not.toHaveBeenCalled()
    expect(choose).not.toHaveBeenCalled(); expect(navigate).not.toHaveBeenCalled(); expect(submit).not.toHaveBeenCalled()
  })
  it('retains choice navigation outside the summary and Escape inside it', () => {
    const { keys, choose, close } = desk()
    const down = keyEvent('ArrowDown', ''); keys(down)
    expect(choose).toHaveBeenCalledWith('second'); expect(down.preventDefault).toHaveBeenCalledOnce()
    keys(keyEvent('Escape', '.answer-slot')); expect(close).toHaveBeenCalledOnce()
  })
})
describe('Release Walker description scrolling', () => {
  for (const key of scrolling) for (const shift of [false, true]) it(`keeps ${shift ? 'Shift+' : ''}${key} native without changing membership, screens or tickets`, () => {
    const { keys, toggle, stepScreen, step, feature } = walker(), event = keyEvent(key, '.description.revealed', shift)
    keys(event)
    expect(event.preventDefault).not.toHaveBeenCalled()
    expect(toggle).not.toHaveBeenCalled(); expect(stepScreen).not.toHaveBeenCalled(); expect(step).not.toHaveBeenCalled(); expect(feature).not.toHaveBeenCalled()
  })
  it('retains membership and screen shortcuts outside the description and Escape inside it', () => {
    const { keys, toggle, stepScreen, close } = walker()
    keys(keyEvent(' ', '')); expect(toggle).toHaveBeenCalledOnce()
    keys(keyEvent('ArrowDown', '')); expect(stepScreen).toHaveBeenCalledWith(1)
    keys(keyEvent('Escape', '.description.revealed')); expect(close).toHaveBeenCalledOnce()
  })
})
