// SPDX-License-Identifier: AGPL-3.0-only
// AEON-752: dependency-free component colour guard, owned by ci-static only.
import { readFileSync, readdirSync } from 'node:fs'
import { resolve, relative } from 'node:path'
import { fileURLToPath } from 'node:url'

// These surfaces describe a document, supplied brand artwork, or a fixed-mode
// specimen. Their palette must not inherit the surrounding app's chosen theme.
export const fixedSurfaces = {
  'components/quotes/editor/QuoteDocument.vue': 'Frozen quote/document palette and QR ink',
  'components/quotes/editor/QuotePositions.vue': 'Document rules',
  'components/quotes/editor/QuoteProse.vue': 'Document editor focus on paper',
  'components/settings/QuoteSettingsCard.vue': 'Document specimen',
  'views/business/QuoteDocumentView.vue': 'Document and print output',
  'views/settings/DocumentProfilesView.vue': 'Document paper specimens',
  'components/settings/profiles/ProfilePreview.vue': 'Document layout guides',
  'components/settings/BrandCard.vue': 'Supplied brand artwork in light/dark specimens',
}
const specimenRules = /\.(?:colour-preview(?:\.dark)?|colour-preview header|preview-row|preview-progress)[^{]*\{[^}]*\}/g
// Narrow exceptions rather than exempting the containing app component.
const exceptions = {
  'components/settings/ThemeColoursCard.vue': [/<script\b[^>]*>[\s\S]*?<\/script>/g, specimenRules],
  'components/settings/AvatarCropDialog.vue': [/\.mask\s*\{[^}]*\}/g],
  'components/indicators/Robot5.vue': [/--blush:\s*oklch\([^)]*\)/g],
  'components/Avatar.vue': [/oklch\([^)]*\)/g],
  'components/projects/GroupMarker.vue': [/oklch\([^)]*\)/g],
  'components/CalendarVersion.vue': [/brand:\s*'#D69B31'/g],
  'components/VersionDisplay.vue': [/brand:\s*'#D69B31'/g],
  'components/journey/ReleaseVersion.vue': [/brand:\s*'#D69B31'/g],
  'components/quotes/details/QuoteLinkCard.vue': [/(?:fill="#fff"|fill="#000")/g],
  'components/settings/profiles/ProfileColors.vue': [/Use a colour like #2a7f78\./g],
  'components/work/AttachmentLightbox.vue': [/\.html-preview\s*\{[^}]*\}/g], // Document paper inside the viewer.
}
const blank = text => text.replace(/[^\n]/g, ' ')
const namedColours = new Set(('aliceblue antiquewhite aqua aquamarine azure beige bisque black blanchedalmond blue blueviolet brown burlywood cadetblue chartreuse chocolate coral cornflowerblue cornsilk crimson cyan darkblue darkcyan darkgoldenrod darkgray darkgreen darkgrey darkkhaki darkmagenta darkolivegreen darkorange darkorchid darkred darksalmon darkseagreen darkslateblue darkslategray darkslategrey darkturquoise darkviolet deeppink deepskyblue dimgray dimgrey dodgerblue firebrick floralwhite forestgreen fuchsia gainsboro ghostwhite gold goldenrod gray green greenyellow grey honeydew hotpink indianred indigo ivory khaki lavender lavenderblush lawngreen lemonchiffon lightblue lightcoral lightcyan lightgoldenrodyellow lightgray lightgreen lightgrey lightpink lightsalmon lightseagreen lightskyblue lightslategray lightslategrey lightsteelblue lightyellow lime limegreen linen magenta maroon mediumaquamarine mediumblue mediumorchid mediumpurple mediumseagreen mediumslateblue mediumspringgreen mediumturquoise mediumvioletred midnightblue mintcream mistyrose moccasin navajowhite navy oldlace olive olivedrab orange orangered orchid palegoldenrod palegreen paleturquoise palevioletred papayawhip peachpuff peru pink plum powderblue purple rebeccapurple red rosybrown royalblue saddlebrown salmon sandybrown seagreen seashell sienna silver skyblue slateblue slategray slategrey snow springgreen steelblue tan teal thistle tomato turquoise violet wheat white whitesmoke yellow yellowgreen').split(' '))
export function colourLiterals(source, path) {
  if (fixedSurfaces[path]) return []
  let scan = source.replace(/<!--[\s\S]*?-->|\/\*[\s\S]*?\*\/|\/\/[^\n]*/g, blank)
  // Black/white in a CSS alpha mask are geometry, not visible colour. This
  // exception applies only to the mask declaration, never a neighbour's fill.
  scan = scan.replace(/(?:-webkit-)?mask-image\s*:[^;{}]+/g, blank)
  for (const pattern of exceptions[path] ?? []) scan = scan.replace(pattern, blank)
  const literals = /#[\da-f]{8}(?![\w-])|#[\da-f]{6}(?![\w-])|#[\da-f]{4}(?![\w-])|#[\da-f]{3}(?![\w-])|\b(?:rgba?|hsla?|oklch|oklab|lab|lch|color)\(\s*(?:[\d.+-]|(?:srgb|display-p3)\s+[\d.+-])[^)]*\)|(?<![\w-])(?:color|background(?:-color)?|fill|stroke|stop-color|border(?:-color)?)\s*[:=]\s*["']?(?:white|black|red|blue|green|yellow|orange|purple|pink|teal|cyan|gold|rebeccapurple)\b/gi
  const matches = [...scan.matchAll(literals)].map(m => ({ index: m.index, value: m[0] }))
  // Named colours can hide inside a gradient, shadow or border shorthand.
  for (const declaration of scan.matchAll(/(?:^|[;{])\s*((?:--)?[\w-]+)\s*:\s*([^;{}]+)/g)) {
    if (!/^(?:--|color$|background|border|outline|box-shadow$|text-shadow$|fill$|stroke$|stop-color$|filter$)/.test(declaration[1])) continue
    for (const word of declaration[2].matchAll(/(?<![\w-])[a-z]+(?![\w-])/gi)) {
      if (!namedColours.has(word[0].toLowerCase())) continue
      const index = declaration.index + declaration[0].indexOf(declaration[2]) + word.index
      if (!matches.some(m => index >= m.index && index < m.index + m.value.length)) matches.push({ index, value: word[0] })
    }
  }
  return matches.sort((a, b) => a.index - b.index).map(m => ({ line: scan.slice(0, m.index).split('\n').length, value: m.value }))
}
export function checkThemeColours(root) {
  const files = []
  function walk(dir) {
    for (const entry of readdirSync(dir, { withFileTypes: true })) {
      const path = resolve(dir, entry.name)
      if (entry.isDirectory()) walk(path)
      else if (entry.name.endsWith('.vue')) files.push(path)
    }
  }
  const source = resolve(root, 'web/src')
  walk(source)
  const failures = files.sort().flatMap(path => colourLiterals(readFileSync(path, 'utf8'), relative(source, path).split('\\').join('/'))
    .map(item => `${relative(root, path)}:${item.line}: colour literal ${item.value}; use a semantic theme token`))
  return { files: files.length, failures }
}
if (process.argv[1] && resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  const report = checkThemeColours(resolve(fileURLToPath(new URL('..', import.meta.url))))
  if (report.failures.length) { console.error(report.failures.join('\n')); process.exitCode = 1 }
  else console.log(`Theme colour literals: ${report.files} components checked`)
}
