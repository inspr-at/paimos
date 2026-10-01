// SPDX-License-Identifier: AGPL-3.0-only
import { expect, it, vi } from 'vitest'
import { exitGraphFullscreen, requestGraphFullscreen } from '../src/lib/graphFocus'

function browser() {
  const document = { fullscreenEnabled: true, fullscreenElement: null as HTMLElement | null, exitFullscreen: vi.fn(async () => { document.fullscreenElement = null }) }
  const element = { ownerDocument: document, requestFullscreen: vi.fn(async () => { document.fullscreenElement = element as unknown as HTMLElement }) } as unknown as HTMLElement
  return { element, document, request: vi.mocked(element.requestFullscreen) }
}

it('prefers native fullscreen when the browser accepts it', async () => {
  const { element, request, document } = browser()
  expect(await requestGraphFullscreen(element, () => true)).toBe(true)
  expect(request).toHaveBeenCalledOnce()
  expect(document.fullscreenElement).toBe(element)
  await exitGraphFullscreen(element)
  expect(document.exitFullscreen).toHaveBeenCalledOnce()
  expect(document.fullscreenElement).toBeNull()
})

it('keeps in-app focus when the Fullscreen API is missing', async () => {
  const { element } = browser()
  Object.defineProperty(element, 'requestFullscreen', { value: undefined })
  expect(await requestGraphFullscreen(element, () => true)).toBe(false)
})

it('does not request fullscreen when permissions disable it', async () => {
  const { element, document, request } = browser()
  document.fullscreenEnabled = false
  expect(await requestGraphFullscreen(element, () => true)).toBe(false)
  expect(request).not.toHaveBeenCalled()
})

it.each(['rejected', 'thrown', 'ignored'])('keeps in-app focus when fullscreen is %s', async failure => {
  const { element, request, document } = browser()
  if (failure === 'rejected') request.mockRejectedValue(new Error('Denied'))
  else if (failure === 'thrown') request.mockImplementation(() => { throw new Error('Denied') })
  else request.mockResolvedValue(undefined)
  expect(await requestGraphFullscreen(element, () => true)).toBe(false)
  expect(document.exitFullscreen).not.toHaveBeenCalled()
})

it('never takes over or exits another element’s fullscreen', async () => {
  const { element, document, request } = browser()
  document.fullscreenElement = {} as HTMLElement
  expect(await requestGraphFullscreen(element, () => true)).toBe(false)
  await exitGraphFullscreen(element)
  expect(request).not.toHaveBeenCalled()
  expect(document.exitFullscreen).not.toHaveBeenCalled()
})

it('does not request fullscreen after focus has already ended', async () => {
  const { element, request } = browser()
  expect(await requestGraphFullscreen(element, () => false)).toBe(false)
  expect(request).not.toHaveBeenCalled()
})

it('exits a late fullscreen grant after Escape, navigation or unmount', async () => {
  const { element, document, request } = browser()
  let focused = true, finish!: () => void
  request.mockImplementation(() => new Promise(resolve => { finish = () => { document.fullscreenElement = element; resolve() } }))
  const pending = requestGraphFullscreen(element, () => focused)
  focused = false
  finish()
  expect(await pending).toBe(false)
  expect(document.exitFullscreen).toHaveBeenCalledOnce()
  expect(document.fullscreenElement).toBeNull()
})

it('contains a rejected exit and tolerates a missing exit API', async () => {
  const { element, document } = browser()
  document.fullscreenElement = element
  document.exitFullscreen.mockRejectedValue(new Error('Denied'))
  await expect(exitGraphFullscreen(element)).resolves.toBeUndefined()
  Object.defineProperty(document, 'exitFullscreen', { value: undefined })
  await expect(exitGraphFullscreen(element)).resolves.toBeUndefined()
})
