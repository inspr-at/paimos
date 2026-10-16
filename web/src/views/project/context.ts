// SPDX-License-Identifier: AGPL-3.0-only
import { inject, type Ref, type InjectionKey } from 'vue'
import type { useProjectPage } from './useProjectPage'

export const PROJECT_PAGE: InjectionKey<ReturnType<typeof useProjectPage>> = Symbol('project-page')
export function useProjectPageContext() {
  const page = inject(PROJECT_PAGE)
  if (!page) throw new Error('Project sections require their page owner')
  return page
}

// Ref ownership stays in the page; the section supplies the mounted element.
export function bindProjectRef<T>(target: Ref<T>) {
  return (value: unknown) => { target.value = value as T }
}
