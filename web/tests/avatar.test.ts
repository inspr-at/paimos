// SPDX-License-Identifier: AGPL-3.0-only
import { test } from 'node:test'
import assert from 'node:assert/strict'
import { createHash } from 'node:crypto'
import { AVATAR_PALETTE, avatarColor, avatarSources, avatarUploadDimensions, centred, clampView, cropOf, deriveInitials, learnPictures, onPictures, pan, pictureKnown, prepareAvatarUpload, sha256FirstByte, shouldRequestAvatar, uploadError, uploadProblem, zoomAt } from '../src/lib/avatar.ts'

test('the initials colour follows the server: palette[sha256(id)[0] % 12]', () => {
  for (const id of ['11111111-1111-4111-8111-111111111111', '22222222-2222-4222-8222-222222222222', 'a', '', 'x'.repeat(200)]) {
    assert.equal(sha256FirstByte(id), createHash('sha256').update(id).digest()[0], id)
  }
  const id = '11111111-1111-4111-8111-111111111111'
  assert.equal(avatarColor(id), AVATAR_PALETTE[createHash('sha256').update(id).digest()[0] % 12])
})

test('initials derive like the server', () => {
  assert.equal(deriveInitials({ first_name: 'Markus', last_name: 'Barta' }), 'MB')
  assert.equal(deriveInitials({ first_name: '', preferred_name: 'mira', last_name: 'Holm' }), 'MH')
  assert.equal(deriveInitials({ short_name: 'mba' }), 'M')
  assert.equal(deriveInitials({ first_name: 'Élodie', last_name: '' }), 'É')
  assert.equal(deriveInitials({}), '?')
})

test('sources pick sizes sharp on 2x screens, versioned when the hash is known', () => {
  const s = avatarSources('p1', 40, { '64': 'aa', '128': 'bb' })
  assert.equal(s.src, '/api/people/p1/avatar/64?v=aa')
  assert.equal(s.srcset, '/api/people/p1/avatar/64?v=aa 1.6x, /api/people/p1/avatar/128?v=bb 3.2x, /api/people/p1/avatar/256 6.4x')
})

test('uploads are checked in plain words', () => {
  assert.match(uploadProblem({ type: 'image/heic', size: 10 }), /PNG, JPEG or WebP/)
  assert.match(uploadProblem({ type: 'image/png', size: 12 * 1024 * 1024 }), /12\.0 MB; a photo can be up to 8 MB/)
  assert.equal(uploadProblem({ type: 'image/jpeg', size: 1000 }), '')
  assert.match(uploadError(413, ''), /larger than 8 MB/)
  assert.match(uploadError(400, 'invalid image'), /could not be read/)
  assert.equal(uploadError(400, 'image dimensions exceed limit'), 'Use a photo with at most 4,194,304 pixels and 4096 pixels per side, and a square crop at most 2048 pixels per side.')
  assert.match(uploadError(400, 'crop outside oriented image'), /inside the photo, at most 2048 pixels per side/)
})

test('camera photos fit both upload limits without enlarging small images', () => {
  assert.deepEqual(avatarUploadDimensions(6000, 4000), { width: 2508, height: 1672 })
  assert.deepEqual(avatarUploadDimensions(4000, 6000), { width: 1672, height: 2508 })
  assert.deepEqual(avatarUploadDimensions(2048, 2048), { width: 2048, height: 2048 })
  assert.deepEqual(avatarUploadDimensions(1000, 500), { width: 1000, height: 500 })
  assert.deepEqual(avatarUploadDimensions(10000, 100), { width: 4096, height: 40 })
  for (const [width, height] of [[3000, 2000], [4097, 1], [1, 4097], [2049, 2049], [6001, 3999], [12000, 9000]]) {
    const resized = avatarUploadDimensions(width, height)
    assert.ok(resized.width * resized.height <= 4_194_304)
    assert.ok(Math.max(resized.width, resized.height) <= 4096)
    for (const zoom of [1, 2, 5]) {
      const original = pan(zoomAt(centred(320, width, height), zoom), -100, -50)
      const before = cropOf(original)
      const after = cropOf({ ...original, ...resized })
      assert.ok(after.size >= 1 && after.size <= 2048)
      assert.ok(after.x >= 0 && after.y >= 0 && after.x + after.size <= resized.width && after.y + after.size <= resized.height)
      // Rounding costs at most two output pixels; the selected region stays put.
      for (const [a, b, scale] of [[after.x, before.x, resized.width / width], [after.y, before.y, resized.height / height], [after.size, before.size, Math.min(resized.width / width, resized.height / height)]]) {
        assert.ok(Math.abs(a - b * scale) <= 2)
      }
    }
  }
  for (const width of [0, -1, NaN, Infinity]) assert.throws(() => avatarUploadDimensions(width, 100), /could not be read/)
})

test('browser resizing draws the oriented preview, encodes and releases the canvas', async t => {
  const photo = { naturalWidth: 4000, naturalHeight: 6000 } as HTMLImageElement
  const file = new File(['original'], 'camera.jpg', { type: 'image/jpeg' })
  const encoded = new Blob(['resized'], { type: 'image/jpeg' })
  const calls: unknown[][] = []
  const canvas = {
    width: 0, height: 0,
    getContext: () => ({ drawImage: (...args: unknown[]) => calls.push(args) }),
    toBlob: (done: (blob: Blob | null) => void, type: string, quality: number) => { calls.push([type, quality]); done(encoded) },
  }
  const previous = Object.getOwnPropertyDescriptor(globalThis, 'document')
  Object.defineProperty(globalThis, 'document', { configurable: true, value: { createElement: () => canvas } })
  t.after(() => { if (previous) Object.defineProperty(globalThis, 'document', previous); else Reflect.deleteProperty(globalThis, 'document') })
  assert.deepEqual(await prepareAvatarUpload(file, photo), { file: encoded, width: 1672, height: 2508 })
  assert.deepEqual(calls, [[photo, 0, 0, 1672, 2508], ['image/jpeg', 0.92]])
  assert.equal(canvas.width, 0); assert.equal(canvas.height, 0)
  calls.length = 0
  assert.equal((await prepareAvatarUpload(file, { naturalWidth: 1000, naturalHeight: 500 } as HTMLImageElement)).file, file)
  assert.deepEqual(calls, [])
  canvas.toBlob = done => done(null)
  await assert.rejects(prepareAvatarUpload(file, photo), /could not be resized/)
  assert.equal(canvas.width, 0); assert.equal(canvas.height, 0)
})

test('the crop always covers the viewport and maps to image pixels', () => {
  // A landscape 2000x1000 photo in a 300px viewport: covered at height.
  let v = centred(300, 2000, 1000)
  assert.deepEqual(cropOf(v), { x: 500, y: 0, size: 1000 })
  v = pan(v, 10_000, 10_000) // pinned to the top-left
  assert.deepEqual(cropOf(v), { x: 0, y: 0, size: 1000 })
  v = zoomAt(v, 2) // zooming about the centre keeps the centre
  assert.deepEqual(cropOf(v), { x: 250, y: 250, size: 500 })
  v = zoomAt(v, 9) // clamped to the maximum
  assert.equal(v.zoom, 5)
  v = clampView({ ...v, zoom: 0.2 })
  assert.equal(v.zoom, 1)
  const c = cropOf(pan(v, -10_000, -10_000))
  assert.deepEqual(c, { x: 1000, y: 0, size: 1000 })
})

test('zones and languages read well', async () => {
  const { matchZone, zoneParts, zoneOffset, datePreview, localeName } = await import('../src/lib/zones.ts')
  assert.deepEqual(zoneParts('America/Argentina/Salta'), { city: 'Salta', region: 'America / Argentina' })
  assert.deepEqual(zoneParts('UTC'), { city: 'UTC', region: '' })
  assert.ok(matchZone('America/New_York', 'new york'))
  assert.ok(!matchZone('Europe/Vienna', 'tokyo'))
  assert.equal(zoneOffset('Europe/Vienna', new Date('2026-09-24T12:00:00Z')), 'GMT+2')
  assert.equal(zoneOffset('UTC', new Date('2026-09-24T12:00:00Z')), 'GMT')
  assert.match(datePreview('de-AT', 'Europe/Vienna', new Date('2026-09-24T12:00:00Z')), /Donnerstag, 24\. September 2026 · 24\.09\.26, 14:00/)
  assert.equal(localeName('de-AT').english, 'Austrian German')
})

test('a picture is requested only when there is one (U27)', () => {
  const person = { kind: 'person' as const, id: 'p1', mine: false, myPicture: false, known: undefined as boolean | undefined, missing: false }
  // Nobody said: initials, no request.
  assert.equal(shouldRequestAvatar(person), false)
  assert.equal(shouldRequestAvatar({ ...person, known: false }), false)
  assert.equal(shouldRequestAvatar({ ...person, known: true }), true)
  // Found missing this session, an agent, or no id: never.
  assert.equal(shouldRequestAvatar({ ...person, known: true, missing: true }), false)
  assert.equal(shouldRequestAvatar({ ...person, known: true, kind: 'agent' }), false)
  assert.equal(shouldRequestAvatar({ ...person, known: true, id: null }), false)
  // My own follows my profile, whatever a payload said.
  assert.equal(shouldRequestAvatar({ ...person, mine: true, myPicture: true, known: false }), true)
  assert.equal(shouldRequestAvatar({ ...person, mine: true, myPicture: false, known: true }), false)
})

test('people payloads teach who has a picture, and listeners hear of changes only', () => {
  let heard = 0
  const stop = onPictures(() => { heard++ })
  learnPictures([{ id: 'with', has_avatar: true }, { id: 'without', has_avatar: false }, { id: 'silent' }, null, { name: 'no id', has_avatar: true } as never])
  assert.equal(pictureKnown('with'), true)
  assert.equal(pictureKnown('without'), false)
  assert.equal(pictureKnown('silent'), undefined)
  assert.equal(heard, 1)
  learnPictures([{ id: 'with', has_avatar: true }])
  assert.equal(heard, 1)
  learnPictures([{ id: 'without', has_avatar: true }])
  assert.equal(pictureKnown('without'), true)
  assert.equal(heard, 2)
  stop()
  learnPictures([{ id: 'with', has_avatar: false }])
  assert.equal(heard, 2)
})
