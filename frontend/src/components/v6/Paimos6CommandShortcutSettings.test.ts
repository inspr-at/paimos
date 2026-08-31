import { createPinia, setActivePinia } from 'pinia'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { nextTick } from 'vue'

import { api } from '@/api/client'
import { mountComponent } from '@/components/ai/testMount'
import { useAuthStore, type User } from '@/stores/auth'
import Paimos6CommandShortcutSettings from './Paimos6CommandShortcutSettings.vue'

function wire(user: string | null) {
  return {
    schema_version: 1,
    default_shortcut: 'Mod+KeyK',
    instance_shortcut: null,
    user_shortcut: user,
    effective_shortcut: user ?? 'Mod+KeyK',
    source: user ? 'user' : 'default',
  }
}

describe('Paimos 6 command shortcut settings (PAI-866)', () => {
  afterEach(() => {
    vi.restoreAllMocks()
    document.body.innerHTML = ''
  })

  it('shows effective/reset truth and rejects reserved collisions before saving', async () => {
    setActivePinia(createPinia())
    const auth = useAuthStore()
    auth.user = { id: 1, username: 'operator', role: 'member', status: 'active', is_super_admin: false } as User
    const get = vi.spyOn(api, 'get').mockResolvedValue(wire(null) as never)
    const put = vi.spyOn(api, 'put').mockImplementation((_path, body) => {
      const shortcut = (body as { shortcut: string | null }).shortcut
      return Promise.resolve(wire(shortcut)) as never
    })
    const mounted = await mountComponent(Paimos6CommandShortcutSettings)
    await vi.waitFor(() => expect(get).toHaveBeenCalledWith('/command-palette/v1/settings', expect.any(Object)))
    expect(mounted.el.textContent).toContain('Effective shortcut:')
    expect(mounted.el.textContent).toContain('default')
    expect(mounted.el.textContent).toContain('never in this browser or URL')

    const input = mounted.el.querySelector<HTMLInputElement>('input')!
    input.value = 'Mod+KeyR'
    input.dispatchEvent(new Event('input', { bubbles: true }))
    mounted.el.querySelector<HTMLButtonElement>('.btn:not(.btn-ghost)')!.click()
    await nextTick()
    expect(mounted.el.textContent).toContain('Shortcut collides with a reserved browser or shell command.')
    expect(put).not.toHaveBeenCalled()

    input.value = 'Alt+KeyJ'
    input.dispatchEvent(new Event('input', { bubbles: true }))
    mounted.el.querySelector<HTMLButtonElement>('.btn:not(.btn-ghost)')!.click()
    await vi.waitFor(() => expect(put).toHaveBeenCalledWith('/command-palette/v1/settings', { shortcut: 'Alt+KeyJ' }))
    await vi.waitFor(() => expect(mounted.el.textContent).toContain('User command shortcut saved.'))

    mounted.el.querySelector<HTMLButtonElement>('.btn-ghost')!.click()
    await vi.waitFor(() => expect(put).toHaveBeenLastCalledWith('/command-palette/v1/settings', { shortcut: null }))
    await vi.waitFor(() => expect(mounted.el.textContent).toContain('User override reset to the inherited shortcut.'))
    await mounted.unmount()
  })
})
