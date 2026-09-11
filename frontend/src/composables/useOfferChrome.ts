import type { InjectionKey, Ref } from 'vue'

export const OFFER_CHROME_KEY: InjectionKey<Ref<boolean>> = Symbol('offer-chrome-collapsed')
