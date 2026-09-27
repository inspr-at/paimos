// SPDX-License-Identifier: AGPL-3.0-only
import 'vue-router'
declare module 'vue-router' {
  // fill: the view owns the full height of the page between header and footer.
  // bare: no header and no footer (sign-in brings its own).
  // foldHeader: the page may fold the app header away (lib/chrome.ts).
  interface RouteMeta { title?: string; fill?: boolean; bare?: boolean; foldHeader?: boolean; keepsFocus?: boolean; public?: boolean }
}
export {}
