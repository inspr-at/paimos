declare module '@/vendor/calendar-version-display/version.js' {
  export function parts(value: string, scheme: string): Record<string, string> | null
  export function renderVersion(
    element: HTMLElement,
    value: string,
    scheme: string,
    options: {
      config: unknown
      mode?: 'pretty' | 'reduced'
      brand?: string
      interactive?: boolean
    },
  ): void
  export function disposeVersion(element: HTMLElement): void
}
declare module '@/vendor/calendar-version-display/version-interaction.js' {
  export function attachVersionInteraction(
    element: HTMLElement,
    canonical: string,
  ): { dispose(): void }
}
