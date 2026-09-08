/// <reference types="vite/client" />

declare const __APP_VERSION__: string
declare const __GIT_HASH__: string

declare module '@inspr/flow-shell'
declare module '@inspr/flow-shell/identity' {
  export function identityBinding(identity: unknown): Record<string, unknown>
  export function normalizeIdentityContext(input: unknown): { status: string }
}
declare module '@inspr/flow-shell/state' {
  export function normalizeShellState(input?: unknown): Record<string, unknown>
}
declare module '@inspr/flow-shell/assets/inspr-logo.svg' {
  const src: string
  export default src
}

declare module "*.vue" {
  import type { DefineComponent } from "vue";
  const component: DefineComponent<Record<string, unknown>, Record<string, unknown>, any>;
  export default component;
}
