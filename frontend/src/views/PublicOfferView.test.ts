import { afterEach, describe, expect, it, vi } from 'vitest'
import { createApp, nextTick, type App } from 'vue'
import PublicOfferView from './PublicOfferView.vue'
vi.mock('vue-router', () => ({ useRoute: () => ({ params: { token: 'test-capability' } }) }))
vi.mock('@/components/offers/OfferDocument.vue', () => ({
  default: { template: '<div>Vollständiges Angebot</div>' },
}))
const payload = (status = 'sent') => ({
  offer_no: 'A260910-01',
  status,
  revision: 2,
  document: {
    title: 'IT-Consulting',
    valid_until: '2026-10-10',
    sender: { company: 'Consulting GmbH', email: 'office@example.test' },
    customer: { name: 'Testkunde GmbH', contact: 'Eva Test' },
  },
  accepted_name: 'Eva Test',
  accepted_company: 'Testkunde GmbH',
  accepted_at: '2026-09-10T16:00:00Z',
})
let app: App | undefined
let el: HTMLDivElement
async function flush() {
  await new Promise((resolve) => setTimeout(resolve, 0))
  await nextTick()
}
async function mount(status = 'sent') {
  const fetcher = vi.fn().mockResolvedValue({ ok: true, json: async () => payload(status) })
  vi.stubGlobal('fetch', fetcher)
  el = document.createElement('div')
  document.body.appendChild(el)
  app = createApp(PublicOfferView)
  app.mount(el)
  await flush()
  return fetcher
}
afterEach(() => {
  app?.unmount()
  el?.remove()
  vi.unstubAllGlobals()
})
describe('public offer acceptance', () => {
  it('requires explicit confirmation and submits the displayed revision without session credentials', async () => {
    const fetcher = await mount()
    const submit = el.querySelector<HTMLButtonElement>('button[type=submit]')!
    expect(submit.disabled).toBe(true)
    const checkbox = el.querySelector<HTMLInputElement>('input[type=checkbox]')!
    expect(checkbox.checked).toBe(false)
    checkbox.checked = true
    checkbox.dispatchEvent(new Event('change', { bubbles: true }))
    await nextTick()
    expect(submit.disabled).toBe(false)
    fetcher.mockResolvedValueOnce({ ok: true, json: async () => payload('accepted') })
    el.querySelector('form')!.dispatchEvent(
      new Event('submit', { bubbles: true, cancelable: true }),
    )
    await flush()
    const [, options] = fetcher.mock.calls[1]!
    expect(options.credentials).toBe('omit')
    expect(options.headers['X-Offer-Acceptance']).toBe('1')
    expect(JSON.parse(options.body)).toMatchObject({
      name: 'Eva Test',
      company: 'Testkunde GmbH',
      revision: 2,
      confirmed: true,
    })
    expect(el.querySelector('form')).toBeNull()
    expect(el.textContent).toContain('Vielen Dank. Das Angebot wurde angenommen.')
  })
  it.each(['expired', 'accepted'])('does not offer acceptance in state %s', async (status) => {
    await mount(status)
    expect(el.querySelector('form')).toBeNull()
    expect(el.textContent).toContain(
      status === 'expired' ? 'Bindefrist ist abgelaufen' : 'wurde angenommen',
    )
  })
  it('keeps failures visible without claiming acceptance', async () => {
    const fetcher = await mount()
    fetcher.mockResolvedValueOnce({ ok: false, status: 409 })
    const checkbox = el.querySelector<HTMLInputElement>('input[type=checkbox]')!
    checkbox.checked = true
    checkbox.dispatchEvent(new Event('change', { bubbles: true }))
    await nextTick()
    el.querySelector('form')!.dispatchEvent(
      new Event('submit', { bubbles: true, cancelable: true }),
    )
    await flush()
    expect(el.querySelector('[role=alert]')?.textContent).toContain('nicht mehr angenommen')
    expect(el.textContent).not.toContain('Vielen Dank.')
  })
})
