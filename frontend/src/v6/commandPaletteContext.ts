import type { InjectionKey, Ref } from 'vue'

export interface Paimos6CommandContext {
  selectedSessionId: Readonly<Ref<string | null>>
  openTalk(): void
  clearSession(): void
}

export type RegisterPaimos6CommandContext = (context: Paimos6CommandContext | null) => void

export const PAIMOS6_COMMAND_CONTEXT_KEY: InjectionKey<RegisterPaimos6CommandContext> = Symbol('paimos6-command-context')
