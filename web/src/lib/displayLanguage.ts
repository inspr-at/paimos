// SPDX-License-Identifier: AGPL-3.0-only
export type DisplayLanguage = 'en' | 'de'

// One app language (AEON-1609). English today; a later catalog (AEON-1610)
// can switch every surface together. Region, profile and browser locale never
// switch a surface on their own, so a German reader does not get German
// panels inside an English shell. German catalogs stay in the source as input
// for that catalog. Release notes keep their own explicit DE/EN control
// (releaseLang / noteLocale, AEON-323); that choice is not this function.
export function displayLanguage(): DisplayLanguage {
  return 'en'
}
