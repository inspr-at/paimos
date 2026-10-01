// SPDX-License-Identifier: AGPL-3.0-only
// Static brand silhouettes for session rows. Geometry is copied from the
// public files named below and is not fetched when the app runs. Fills are
// currentColor so the row stays muted and theme-aware. These marks name the
// harness or model provider; they are not an endorsement.

export interface BrandMark {
  viewBox: string
  paths: readonly string[]
  /** Width in px when drawn at 14px tall in the execution column. */
  width: number
}

// OpenAI blossom, the February 2025 symbol. Codex uses this same mark.
// Fetched 2026-09-27 from
// https://commons.wikimedia.org/wiki/Special:FilePath/OpenAI_logo_2025_(symbol).svg
// File page: https://commons.wikimedia.org/wiki/File:OpenAI_logo_2025_(symbol).svg
// License: PD-textlogo (simple geometric mark, public domain). The blossom is
// an OpenAI trademark.
const openai: BrandMark = {
  viewBox: '1.68 1.75 16.65 16.5',
  width: 14,
  paths: [
    'M11.248 18.25q-.825 0-1.568-.314a4.3 4.3 0 0 1-1.32-.874 4 4 0 0 1-1.304.214 4 4 0 0 1-2.046-.544 4.27 4.27 0 0 1-1.518-1.485 4 4 0 0 1-.56-2.095q0-.48.131-1.04A4.4 4.4 0 0 1 2.04 10.71a4.07 4.07 0 0 1 .017-3.4 4.2 4.2 0 0 1 1.056-1.418 3.8 3.8 0 0 1 1.6-.842 3.9 3.9 0 0 1 .76-1.683q.593-.759 1.451-1.188a4.04 4.04 0 0 1 1.832-.429q.825 0 1.567.313.742.314 1.32.875a4 4 0 0 1 1.304-.215q1.106 0 2.046.545a4.14 4.14 0 0 1 1.501 1.485q.578.941.578 2.095 0 .48-.132 1.04.66.61 1.023 1.419.363.792.363 1.666 0 .892-.38 1.717a4.3 4.3 0 0 1-1.072 1.435 3.8 3.8 0 0 1-1.584.825 3.8 3.8 0 0 1-.775 1.683 4.06 4.06 0 0 1-1.436 1.188 4.04 4.04 0 0 1-1.832.429m-4.076-2.062q.825 0 1.435-.347l3.103-1.782a.36.36 0 0 0 .164-.313v-1.42L7.881 14.62a.67.67 0 0 1-.726 0l-3.118-1.798a.5.5 0 0 1-.017.115v.198q0 .841.396 1.551.413.693 1.139 1.089a3.2 3.2 0 0 0 1.617.412m.165-2.69a.4.4 0 0 0 .181.05q.083 0 .165-.05l1.238-.71-3.977-2.31a.7.7 0 0 1-.363-.643v-3.58q-.825.362-1.32 1.122a2.9 2.9 0 0 0-.495 1.65q0 .809.413 1.55.412.743 1.072 1.123zm3.91 3.663q.875 0 1.585-.396a2.96 2.96 0 0 0 1.534-2.64v-3.564a.32.32 0 0 0-.165-.297l-1.254-.726v4.604a.7.7 0 0 1-.363.643l-3.119 1.799a3 3 0 0 0 1.783.577m.627-6.039V8.878L10.01 7.822 8.129 8.878v2.244l1.881 1.056zM7.057 5.859a.7.7 0 0 1 .363-.644l3.119-1.798a3 3 0 0 0-1.782-.578q-.874 0-1.584.396A2.96 2.96 0 0 0 6.05 4.324a3.07 3.07 0 0 0-.396 1.551v3.547q0 .199.165.314l1.237.726zm8.383 7.887q.825-.364 1.303-1.123.495-.758.495-1.65a3.15 3.15 0 0 0-.412-1.55q-.413-.743-1.073-1.123l-3.086-1.782q-.099-.065-.181-.049a.3.3 0 0 0-.165.05l-1.238.692 3.993 2.327a.6.6 0 0 1 .264.264.64.64 0 0 1 .1.363zm-3.317-8.382a.63.63 0 0 1 .726 0l3.135 1.831v-.297q0-.792-.396-1.501a2.86 2.86 0 0 0-1.105-1.155q-.71-.43-1.65-.43-.825 0-1.436.347L8.294 5.941a.36.36 0 0 0-.165.314v1.418z',
  ],
}

// Claude spark, Anthropic's product symbol, from the public marketing asset
// https://claude.com/static/product-overview/marks/claude.svg
// fetched 2026-09-27. Path geometry is unchanged. The source fill is
// #D97757; the row uses currentColor. Anthropic trademark.
const anthropic: BrandMark = {
  viewBox: '0 0 39.6037 39.6037',
  width: 14,
  paths: [
    'M18.7721 39.6037L17.782 38.8512L17.2276 37.6235L17.782 35.1681L18.4157 31.9998L18.9306 29.4651L19.4058 26.3364L19.683 25.3067L19.6434 25.2275L19.4454 25.2671L17.0692 28.5146L13.4652 33.3859L10.6138 36.3958L9.94052 36.673L8.75241 36.0789L8.87122 34.97L9.54449 34.0196L13.4652 28.9899L15.8415 25.8612L17.386 24.079L17.3464 23.8414H17.2672L6.81183 30.6532L4.95046 30.8909L4.11878 30.1384L4.23759 28.9107L4.63363 28.5146L7.76232 26.3364L15.5642 21.98L15.6831 21.584L15.5642 21.386H15.1682L13.8613 21.3068L9.42567 21.188L5.58412 21.0296L1.82177 20.8315L0.871281 20.6335L0 19.4454L0.0792073 18.8513L0.871281 18.3365L2.01979 18.4157L4.51482 18.6137L8.27717 18.8513L11.0098 19.0098L15.0494 19.4454H15.6831L15.7623 19.1682L15.5642 19.0098L15.4058 18.8513L11.4851 16.2375L7.28708 13.4652L5.06927 11.8415L3.88116 11.0098L3.2871 10.2574L3.04948 8.594L4.11878 7.40589L5.58412 7.5247L5.94055 7.60391L7.40589 8.75241L10.5346 11.1682L14.6534 14.2177L15.2474 14.693L15.5246 14.5345V14.4157L15.2474 13.9801L13.0296 9.94052L10.6534 5.82174L9.58409 4.11878L9.30686 3.08909C9.20125 2.73265 9.14845 2.33662 9.14845 1.90098L10.3762 0.237624L11.0494 0L12.7128 0.237624L13.386 0.831679L14.4157 3.16829L16.0395 6.85144L18.6137 11.8415L19.3662 13.3464L19.7622 14.693L19.9206 15.1286H20.1979V14.891L20.3959 12.0395L20.7919 8.594L21.188 4.15839L21.3068 2.89107L21.9404 1.38613L23.1681 0.594057L24.1186 1.0297L24.9107 2.1782L24.7919 2.89107L24.3563 5.94055L23.4058 10.7326L22.8117 13.9801H23.1681L23.5642 13.5445L25.1879 11.4059L27.9206 7.99994L29.1087 6.65342L30.5344 5.14848L31.4453 4.43561H33.1483L34.376 6.29698L33.8215 8.23756L32.079 10.4554L30.6136 12.3167L28.5146 15.1286L27.2473 17.386L27.3661 17.5444H27.6434L32.3562 16.5147L34.9304 16.0791L37.9403 15.5642L39.3264 16.1979L39.4849 16.8316L38.9304 18.1781L35.6829 18.9702L31.881 19.7226L26.2176 21.0692L26.1384 21.1088L26.2176 21.2276L28.7523 21.4652L29.8612 21.5444H32.5542L37.5443 21.9008L38.8512 22.7721L39.6037 23.8018L39.4849 24.6335L37.4651 25.6236L34.772 24.9899L28.4354 23.485L26.2968 22.9701H25.98V23.1285L27.8018 24.9107L31.0889 27.881L35.2473 31.7225L35.4453 32.673L34.9304 33.4651L34.376 33.3859L30.7325 30.6136L29.3067 29.3859L26.1384 26.7325H25.9404V27.0097L26.6533 28.079L30.5344 33.9007L30.7325 35.6829L30.4552 36.2374L29.4255 36.5938L28.3562 36.3958L26.0592 33.2275L23.7226 29.6235L21.8216 26.4157L21.6236 26.5741L20.4751 38.5344L19.9603 39.1284L18.7721 39.6037Z',
  ],
}

// Monochrome Google G from Simple Icons (CC0-1.0).
// https://github.com/simple-icons/simple-icons/blob/521c96fd04b0ea93034db8715eda5a4de27a58bb/icons/google.svg
// blob 2eaf9155437a3eceb2d445416bb7654a4b1ccaaf, fetched from develop on 2026-09-27.
// The official Super G is a full-color gradient. This row does not recolor that
// asset; it uses this published single-path silhouette so the mark can be muted.
const google: BrandMark = {
  viewBox: '0 0 24 24',
  width: 14,
  paths: [
    'M12.48 10.92v3.28h7.84c-.24 1.84-.853 3.187-1.787 4.133-1.147 1.147-2.933 2.4-6.053 2.4-4.827 0-8.6-3.893-8.6-8.72s3.773-8.72 8.6-8.72c2.6 0 4.507 1.027 5.907 2.347l2.307-2.307C18.747 1.44 16.133 0 12.48 0 5.867 0 .307 5.387.307 12s5.56 12 12.173 12c3.573 0 6.267-1.173 8.373-3.36 2.16-2.16 2.84-5.213 2.84-7.667 0-.76-.053-1.467-.173-2.053H12.48z',
  ],
}

// Cursor cube from Simple Icons (CC0-1.0), added from Cursor's official asset
// in https://github.com/simple-icons/simple-icons/commit/be23679deda9e227ded614e94a1dc262ff930cf1
// blob 61303fbcc34fe825b8c33685980dfd68984934c9, fetched from develop on 2026-09-27.
// cursor.com/brand publishes lockups; this is the square cube silhouette.
const cursor: BrandMark = {
  viewBox: '0 0 24 24',
  width: 14,
  paths: [
    'M11.503.131 1.891 5.678a.84.84 0 0 0-.42.726v11.188c0 .3.162.575.42.724l9.609 5.55a1 1 0 0 0 .998 0l9.61-5.55a.84.84 0 0 0 .42-.724V6.404a.84.84 0 0 0-.42-.726L12.497.131a1.01 1.01 0 0 0-.996 0M2.657 6.338h18.55c.263 0 .43.287.297.515L12.23 22.918c-.062.107-.229.064-.229-.06V12.335a.59.59 0 0 0-.295-.51l-9.11-5.257c-.109-.063-.064-.23.061-.23',
  ],
}

// Grok logomark from the SpaceXAI brand kit linked on
// https://x.ai/legal/brand-guidelines
// file Grok_Logomark_Dark.svg inside
// https://data.x.ai/logos/SpaceXAI_Grok_Assets.zip
// fetched 2026-09-27. Path geometry is unchanged; fill is currentColor.
// SpaceXAI / Grok trademark.
const grok: BrandMark = {
  viewBox: '0 0 1024 1024',
  width: 14,
  paths: [
    'M395.479 633.828L735.91 381.105C752.599 368.715 776.454 373.548 784.406 392.792C826.26 494.285 807.561 616.253 724.288 699.996C641.016 783.739 525.151 802.104 419.247 760.277L303.556 814.143C469.49 928.202 670.987 899.995 796.901 773.282C896.776 672.843 927.708 535.937 898.785 412.476L899.047 412.739C857.105 231.37 909.358 158.874 1016.4 10.6326C1018.93 7.11771 1021.47 3.60279 1024 0L883.144 141.651V141.212L395.392 633.916',
    'M325.226 695.251C206.128 580.84 226.662 403.776 328.285 301.668C403.431 226.097 526.549 195.254 634.026 240.596L749.454 186.994C728.657 171.88 702.007 155.623 671.424 144.2C533.19 86.9942 367.693 115.465 255.323 228.382C147.234 337.081 113.244 504.215 171.613 646.833C215.216 753.423 143.739 828.818 71.7385 904.916C46.2237 931.893 20.6216 958.87 0 987.429L325.139 695.339',
  ],
}

// SpaceXAI symbol (the xAI mark) from the same brand kit, file
// "spacexai - symbol - black - transparent.svg". Wide viewBox kept so the
// swoosh is not crushed into a square. Path geometry is unchanged.
const xai: BrandMark = {
  viewBox: '0 0 834 318',
  width: 37,
  paths: [
    'M832.565 0.590983C736.633 8.41256 359.563 55.1598 98.1057 317.999H1.08203L11.9228 307.195C66.6371 254.336 308.741 30.3127 832.565 0.299805V0.590983Z',
    'M504.192 317.999H428.146L278.315 208.933C292.122 200.25 306.018 191.98 319.957 184.104L504.192 317.999Z',
    'M382.659 318H306.648L283.589 301.228H154.852C163.915 293.18 173.091 285.368 182.362 277.784H251.322L215.636 251.826C228.392 242.335 241.288 233.244 254.287 224.535L382.659 318Z',
    'M105.997 116.89L164.612 159.488C150.024 168.078 136.37 176.58 123.626 184.897L30.0522 116.854L105.997 116.89Z',
  ],
}

// Pi coding-agent mark from https://pi.dev/logo.svg fetched 2026-09-27.
// The three source paths are the coral, blue and gold regions. They are drawn
// as one currentColor silhouette. viewBox is cropped to the ink (165.29–634.72)
// so the mark fills a 10px badge; the path data is unchanged.
const pi: BrandMark = {
  viewBox: '165.29 165.29 469.43 469.43',
  width: 14,
  paths: [
    'M165.29 165.29H517.36V400H400V282.65H165.29Z',
    'M165.29 282.65H282.65V400H400V517.36H282.65V634.72H165.29Z',
    'M517.36 400H634.72V634.72H517.36Z',
  ],
}

// OpenCode brand-kit square mark, v1.14.48. Outer path geometry is unchanged;
// the source background/shadow is omitted for a currentColor outline.
// https://github.com/anomalyco/opencode/blob/v1.14.48/packages/console/app/src/asset/brand/opencode-logo-dark-square.svg
// OpenCode trademark. Cropped to the ink, before the source's 30px translation.
const opencode: BrandMark = {
  viewBox: '0 0 240 300', width: 11.2,
  paths: ['M180 60H60V240H180V60ZM240 300H0V0H240V300Z'],
}

export const BRAND_MARKS = { openai, anthropic, google, cursor, grok, xai, pi, opencode } as const

export type BrandId = keyof typeof BRAND_MARKS

const HARNESS_BRAND: Record<string, BrandId> = {
  codex: 'openai',
  claude: 'anthropic',
  pi: 'pi',
  cursor: 'cursor',
  grok: 'grok',
  gemini: 'google',
  opencode: 'opencode',
}

export function harnessBrand(harness: string): BrandId | null {
  return HARNESS_BRAND[harness] ?? null
}

const PROVIDER_BRAND: Record<string, BrandId> = {
  openai: 'openai',
  anthropic: 'anthropic',
  google: 'google',
  xai: 'xai',
  cursor: 'cursor',
}

export function providerBrand(provider: string): BrandId | null {
  return PROVIDER_BRAND[provider] ?? null
}
