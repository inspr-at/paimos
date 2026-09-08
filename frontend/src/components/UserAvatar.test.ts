import { afterEach, describe, expect, it } from 'vitest'
import { createApp, h } from 'vue'

import UserAvatar from './UserAvatar.vue'

describe('UserAvatar asset binding', () => {
  afterEach(() => {
    document.body.innerHTML = ''
    window.__PAIMOS_PUBLIC_BASE_PATH__ = ''
  })

  it('prefixes a server-returned root-relative avatar path', () => {
    window.__PAIMOS_PUBLIC_BASE_PATH__ = '/paimos'
    document.body.innerHTML = '<div id="root"></div>'
    const app = createApp({
      render: () => h(UserAvatar, {
        user: { username: 'operator', avatar_path: '/api/avatars/9001.jpg' },
      }),
    })
    app.mount('#root')

    expect(document.querySelector<HTMLImageElement>('.ua-img')!.getAttribute('src'))
      .toBe('/paimos/api/avatars/9001.jpg')
    app.unmount()
  })

  it('preserves root-mode and external avatar URLs', () => {
    document.body.innerHTML = '<div id="root"></div>'
    const rootApp = createApp({
      render: () => h(UserAvatar, {
        user: { username: 'operator', avatar_path: '/api/avatars/9001.jpg' },
      }),
    })
    rootApp.mount('#root')
    expect(document.querySelector<HTMLImageElement>('.ua-img')!.getAttribute('src'))
      .toBe('/api/avatars/9001.jpg')
    rootApp.unmount()

    document.body.innerHTML = '<div id="root"></div>'
    window.__PAIMOS_PUBLIC_BASE_PATH__ = '/paimos'
    const externalApp = createApp({
      render: () => h(UserAvatar, {
        user: { username: 'operator', avatar_path: 'https://cdn.example/avatar.jpg' },
      }),
    })
    externalApp.mount('#root')
    expect(document.querySelector<HTMLImageElement>('.ua-img')!.getAttribute('src'))
      .toBe('https://cdn.example/avatar.jpg')
    externalApp.unmount()
  })
})
