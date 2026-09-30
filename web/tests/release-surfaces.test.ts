// SPDX-License-Identifier: AGPL-3.0-only
import { test } from 'node:test'
import assert from 'node:assert/strict'
import { rememberCodename, updateToast } from '../src/lib/codenames.ts'
import { dismiss, toast, toasts } from '../src/lib/toast.ts'
import { describeControl } from '../src/lib/version-reveal.ts'
import { copyToClipboard } from '../src/lib/version-copy.ts'

// AEON-430 fix round 1: the update toast, the tooltip's association and the copy control.
const VERSION = '260930115354.0.0'

test('the update toast names the release by the codename /api/version gave when the release detail fetch failed', () => {
  rememberCodename(VERSION, 'Hinged Hangar')
  const { message, release } = updateToast('PAIMOS AEON', VERSION, undefined, '')
  assert.equal(message, 'PAIMOS AEON was updated to Hinged Hangar')
  assert.deepEqual(release, { version: VERSION, name: 'Hinged Hangar', before: 'PAIMOS AEON was updated to ', after: '' })
  assert.ok(!message.includes(VERSION))
})

test('the release detail’s own name wins, and its headline follows the name', () => {
  rememberCodename(VERSION, 'Hinged Hangar')
  const { message, release } = updateToast('PAIMOS AEON', VERSION, 'Hinged Hangar', ': Releases with a name')
  assert.equal(message, 'PAIMOS AEON was updated to Hinged Hangar: Releases with a name')
  assert.equal(release.after, ': Releases with a name')
})

test('a release nobody has named falls back to its version, still as structured metadata', () => {
  const { message, release } = updateToast('PAIMOS AEON', '991231235959.0.0', undefined, '')
  assert.equal(message, 'PAIMOS AEON was updated to 991231235959.0.0')
  assert.equal(release.name, '')
  assert.equal(release.version, '991231235959.0.0')
})

test('a toast keeps the structured release beside its plain sentence', () => {
  const { message, release } = updateToast('PAIMOS AEON', VERSION, 'Hinged Hangar', '')
  const id = toast(message, { sticky: true, key: 'update', release })
  const item = toasts.find(t => t.id === id)!
  assert.equal(item.message, message)
  assert.equal(item.release?.name, 'Hinged Hangar')
  assert.equal(`${item.release!.before}${item.release!.name}${item.release!.after}`, item.message)
  dismiss(id)
})

// A control that remembers its attributes and a MutationObserver that tells when they change.
class FakeControl {
  attrs = new Map<string, string>()
  getAttribute(name: string) { return this.attrs.get(name) ?? null }
  setAttribute(name: string, value: string) { this.attrs.set(name, value); watchers.forEach(w => w.fire(this)) }
  removeAttribute(name: string) { this.attrs.delete(name); watchers.forEach(w => w.fire(this)) }
}
const watchers = new Set<{ fire(target: unknown): void }>()
class FakeObserver {
  target?: unknown
  run: () => void
  constructor(run: () => void) { this.run = run }
  observe(target: unknown) { this.target = target; watchers.add(this) }
  disconnect() { watchers.delete(this) }
  fire(target: unknown) { if (target === this.target) this.run() }
}
;(globalThis as unknown as { MutationObserver: unknown }).MutationObserver = FakeObserver

test('the control that takes focus carries the stamp as its description', () => {
  const control = new FakeControl()
  const off = describeControl(control as unknown as HTMLElement, 'stamp-1')
  assert.equal(control.getAttribute('aria-describedby'), 'stamp-1')
  off()
  assert.equal(control.getAttribute('aria-describedby'), null)
})

test('an existing description stays, and only the stamp is taken back out', () => {
  const control = new FakeControl()
  control.setAttribute('aria-describedby', 'hint')
  const off = describeControl(control as unknown as HTMLElement, 'stamp-1')
  assert.equal(control.getAttribute('aria-describedby'), 'hint stamp-1')
  off()
  assert.equal(control.getAttribute('aria-describedby'), 'hint')
})

test('a control whose owner rewrites its description gets the stamp back', () => {
  const control = new FakeControl()
  const off = describeControl(control as unknown as HTMLElement, 'stamp-1')
  control.setAttribute('aria-describedby', 'owner-hint')
  assert.equal(control.getAttribute('aria-describedby'), 'owner-hint stamp-1')
  off()
  assert.equal(control.getAttribute('aria-describedby'), 'owner-hint')
})

test('copying uses the clipboard, then the hidden field, and says when neither worked', async () => {
  const written: string[] = []
  const field = { value: '', style: {} as Record<string, string>, setAttribute() {}, select() {}, remove() {} }
  const view = (clipboard: boolean, fallback: boolean) => ({
    navigator: { clipboard: { writeText: async (text: string) => { if (!clipboard) throw new Error('denied'); written.push(text) } } },
    document: { createElement: () => field, body: { append() {} }, execCommand: () => fallback },
  }) as unknown as Window
  assert.equal(await copyToClipboard(VERSION, view(true, false)), true)
  assert.deepEqual(written, [VERSION])
  assert.equal(await copyToClipboard(VERSION, view(false, true)), true)
  assert.equal(field.value, VERSION)
  assert.equal(await copyToClipboard(VERSION, view(false, false)), false)
})
