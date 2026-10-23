// SPDX-License-Identifier: AGPL-3.0-only
import { test } from 'node:test'
import assert from 'node:assert/strict'
import { rememberCodename, updateToast } from '../src/lib/codenames.ts'
import { noteKindCounts, noteKindLine, withPublicNoteItems, type Release, type ReleaseChange, type ReleaseNoteItem } from '../src/lib/releases.ts'
import { dismiss, toast, toasts } from '../src/lib/toast.ts'
import { describeControl } from '../src/lib/version-reveal.ts'
import { copyToClipboard } from '../src/lib/version-copy.ts'

// AEON-430 fix round 1: the update toast, the tooltip's association and the copy control.
const VERSION = '260930115354.0.0'

test('the update toast names the release by the codename /api/version gave when the release detail fetch failed', () => {
  rememberCodename(VERSION, 'Hinged Hangar')
  const { message, release } = updateToast('PAIMOS AEON', VERSION, undefined)
  assert.equal(message, 'PAIMOS AEON was updated to Hinged Hangar')
  assert.deepEqual(release, { version: VERSION, name: 'Hinged Hangar', before: 'PAIMOS AEON was updated to ', after: '' })
  assert.ok(!message.includes(VERSION))
})

// AEON-1655: the sentence ends at the name; what the release brings follows as counts by kind.
test('the release detail’s own name wins and ends the sentence, with the counts beside it', () => {
  rememberCodename(VERSION, 'Hinged Hangar')
  const line = noteKindLine([{ kind: 'features', count: 30 }, { kind: 'fixes', count: 18 }], 'en')
  const { message, release } = updateToast('PAIMOS AEON', VERSION, 'Crisp Chart', line)
  assert.equal(message, 'PAIMOS AEON was updated to Crisp Chart')
  assert.equal(release.after, '')
  assert.deepEqual(release.counts?.items.map(item => `${item.count} ${item.label}`), ['30 new', '18 fixes'])
  assert.equal(release.counts?.spoken, '30 new features, 18 fixes')
  assert.equal(updateToast('PAIMOS AEON', VERSION, 'Crisp Chart', noteKindLine([])).release.counts, undefined)
})

const AT = '2026-10-22T08:28:52Z'
const released = (extra: Partial<Release> = {}): Release => ({
  version: VERSION, tag: `v${VERSION}`, release_channel: 'stable', release_sequence: 138, state: 'published', reserved_at: AT, tagged_at: AT, published_at: AT,
  headline: 'release', tickets: [], changes: [], changes_omitted: 0,
  evidence: { source_commit: '', source_url: '', image: null, ci: null, release_run: null, release_url: '', unavailable: [] }, ...extra,
})
const note = (key: string, group?: ReleaseNoteItem['group']) => ({ key, ...(group ? { group } : {}), pill_en: `Pill ${key}`, pill_de: '', benefit_en: '', benefit_de: '' })
const publicNotes = (items: Omit<ReleaseNoteItem, 'id'>[], source = 'embedded-product-notes') => ({ source, snapshot_sha256: 'a'.repeat(64), captured_at: AT, release_revision: 1, items: [], public_items: items, gaps: [], hidden: 0 })
const commit = (sha: string, key: string, group: ReleaseChange['group']): ReleaseChange => ({ commit: sha, type: 'other', subject: `${key}: change`, scope: '', tickets: [key], at: AT, group })

test('public notes count by kind: a bug ticket is a fix, commits are never counted, and kinds with none are left out', () => {
  const release = withPublicNoteItems(released({
    notes: publicNotes([note('AEON-1', 'features'), note('AEON-2', 'features'), note('AEON-3', 'fixes'), note('AEON-4')]),
    // AEON-4's note carries no group: its own commit is the bug-tagged ticket the server grouped as fixes.
    // AEON-9 has no note, so its commit stays under Other in the sheet and is not counted here.
    changes: [commit('a', 'AEON-1', 'features'), commit('b', 'AEON-3', 'fixes'), commit('c', 'AEON-4', 'fixes'), commit('d', 'AEON-9', 'other')],
  }))
  const counts = noteKindCounts(release)
  assert.deepEqual(counts, [{ kind: 'features', count: 2 }, { kind: 'fixes', count: 2 }])
  assert.deepEqual(noteKindLine(counts, 'en').items.map(item => item.label), ['new', 'fixes'])
})

test('a note the server put under other counts as other, in singular words and in German', () => {
  const counts = noteKindCounts(withPublicNoteItems(released({ notes: publicNotes([note('AEON-5', 'fixes'), note('AEON-8', 'other')]) })))
  assert.deepEqual(counts, [{ kind: 'fixes', count: 1 }, { kind: 'other', count: 1 }])
  assert.deepEqual(noteKindLine(counts, 'en'), { items: [{ kind: 'fixes', count: 1, label: 'fix' }, { kind: 'other', count: 1, label: 'other' }], spoken: '1 fix, 1 other change' })
  assert.equal(noteKindLine([{ kind: 'features', count: 10 }, { kind: 'fixes', count: 5 }], 'de').spoken, '10 neue Funktionen, 5 Korrekturen')
})

test('a release without public notes counts nothing, even when its commits name tickets', () => {
  const linked = { ...commit('a', 'AEON-1', 'features'), linked_tickets: [{ key: 'AEON-1', pill_en: 'Pill', pill_de: '', benefit_en: '', benefit_de: '', group: 'features' as const }] }
  assert.deepEqual(noteKindCounts(released({ changes: [linked] })), [])
  assert.deepEqual(noteKindCounts(withPublicNoteItems(released({ notes: publicNotes([note('AEON-1', 'features')], 'unavailable'), changes: [linked] }))), [])
  assert.deepEqual(noteKindCounts(withPublicNoteItems(released({ notes: publicNotes([]), changes: [linked] }))), [])
})

test('a release nobody has named falls back to its version, still as structured metadata', () => {
  const { message, release } = updateToast('PAIMOS AEON', '991231235959.0.0', undefined)
  assert.equal(message, 'PAIMOS AEON was updated to 991231235959.0.0')
  assert.equal(release.name, '')
  assert.equal(release.version, '991231235959.0.0')
})

test('a toast keeps the structured release beside its plain sentence', () => {
  const { message, release } = updateToast('PAIMOS AEON', VERSION, 'Hinged Hangar')
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
