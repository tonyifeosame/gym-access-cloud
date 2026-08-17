import { readFileSync } from 'node:fs'
import { join } from 'node:path'
import { describe, expect, it } from 'vitest'

/**
 * Colour contrast, checked against the palette itself.
 *
 * THE ONE ACCESSIBILITY PROPERTY THAT CANNOT BE TESTED IN JSDOM. axe disables
 * its contrast rule there, because it needs computed styles and a layout box and
 * jsdom has neither — so the automated sweep in accessibility.test.tsx is
 * silent about the failure that most often makes a console unusable in daylight
 * or on a cheap monitor.
 *
 * It does not need a browser, though. Contrast is a pure function of two colour
 * values, and both are in tokens.css. Reading them out and computing the ratio
 * checks the real palette — the one that ships — rather than a copy of it, and
 * it catches the case that matters: somebody adjusting a token to look better on
 * their own screen and taking a text pairing below the threshold.
 *
 * WHAT IS CHECKED AND WHY THOSE PAIRINGS. Every combination the console actually
 * renders as text, in BOTH themes. Not every possible pairing — a foreground
 * that is never drawn on a given background is not a defect — and not decorative
 * borders, which WCAG does not hold to a text ratio.
 *
 * THE THRESHOLDS ARE WCAG 2.1 AA: 4.5:1 for body text, 3:1 for large text and
 * for the non-text parts of a control. AAA is not claimed, here or anywhere: its
 * 7:1 floor pushes a data-dense operator console into a palette that is harder
 * to read, not easier, and claiming a level nobody is holding to is worse than
 * naming the one that is.
 */

const TOKENS = join(process.cwd(), 'src', 'styles', 'tokens.css')

/**
 * Reads the palette out of the stylesheet.
 *
 * The FILE, not a duplicated table: a copy would keep passing after somebody
 * changed the real one, which is exactly the change this exists to catch.
 *
 * Light lives on bare `:root`; dark overrides a subset inside the media query.
 * The dark palette is therefore light-with-overrides, which is how the
 * stylesheet is written and how a browser resolves it.
 */
function palettes(): { light: Record<string, string>; dark: Record<string, string> } {
  const css = readFileSync(TOKENS, 'utf8')

  const darkStart = css.indexOf('@media (prefers-color-scheme: dark)')
  const lightSection = css.slice(0, darkStart)
  const darkSection = css.slice(darkStart)

  const read = (section: string): Record<string, string> => {
    const found: Record<string, string> = {}
    for (const match of section.matchAll(/(--[a-z-]+):\s*(#[0-9a-fA-F]{3,8})\s*;/g)) {
      found[match[1] as string] = match[2] as string
    }
    return found
  }

  const light = read(lightSection)
  return { light, dark: { ...light, ...read(darkSection) } }
}

/** sRGB channel to linear, per WCAG's relative-luminance definition. */
function channel(value: number): number {
  const c = value / 255
  return c <= 0.04045 ? c / 12.92 : ((c + 0.055) / 1.055) ** 2.4
}

function luminance(hex: string): number {
  const value = hex.replace('#', '')
  const full =
    value.length === 3
      ? value
          .split('')
          .map((c) => c + c)
          .join('')
      : value

  const r = Number.parseInt(full.slice(0, 2), 16)
  const g = Number.parseInt(full.slice(2, 4), 16)
  const b = Number.parseInt(full.slice(4, 6), 16)

  return 0.2126 * channel(r) + 0.7152 * channel(g) + 0.0722 * channel(b)
}

export function contrastRatio(foreground: string, background: string): number {
  const a = luminance(foreground)
  const b = luminance(background)
  const [lighter, darker] = a > b ? [a, b] : [b, a]
  return ((lighter as number) + 0.05) / ((darker as number) + 0.05)
}

/**
 * The pairings the console actually renders as text.
 *
 * `minimum` is 4.5 for body text and 3 where the text is large or the value is
 * used as a control's edge rather than as prose.
 */
const PAIRINGS: { foreground: string; background: string; minimum: number; where: string }[] = [
  { foreground: '--text', background: '--surface', minimum: 4.5, where: 'body text on a panel' },
  {
    foreground: '--text',
    background: '--surface-raised',
    minimum: 4.5,
    where: 'body text on a raised surface',
  },
  {
    foreground: '--text',
    background: '--surface-sunken',
    minimum: 4.5,
    where: 'body text on a sunken surface (code blocks, table stripes)',
  },
  {
    foreground: '--text-muted',
    background: '--surface',
    minimum: 4.5,
    where: 'hints, timestamps and secondary cells — small text, so no exemption',
  },
  {
    foreground: '--text-muted',
    background: '--surface-raised',
    minimum: 4.5,
    where: 'muted text on a raised surface',
  },
  {
    foreground: '--accent',
    background: '--surface',
    minimum: 4.5,
    where: 'links, which are body-sized',
  },
  {
    foreground: '--on-accent',
    background: '--accent',
    minimum: 4.5,
    where: 'the primary button label',
  },
  {
    foreground: '--on-accent',
    background: '--accent-hover',
    minimum: 4.5,
    where: 'the primary button label while hovered — the fill changes, the text does not',
  },
  {
    foreground: '--on-danger',
    background: '--danger',
    minimum: 4.5,
    where: 'the danger button label — revoke, retire, remove',
  },
  {
    foreground: '--accent',
    background: '--accent-soft',
    minimum: 4.5,
    where: 'the active navigation link and the info badge',
  },
  {
    foreground: '--text',
    background: '--surface-sunken',
    minimum: 4.5,
    where: 'the page background behind everything',
  },
  {
    foreground: '--danger',
    background: '--surface',
    minimum: 4.5,
    where: 'error text and the danger badge',
  },
  {
    foreground: '--danger',
    background: '--danger-soft',
    minimum: 4.5,
    where: 'error text inside its own soft panel',
  },
  {
    foreground: '--warning',
    background: '--surface',
    minimum: 4.5,
    where: 'warning badges and the platform-administration chip',
  },
  {
    foreground: '--success',
    background: '--surface',
    minimum: 4.5,
    where: 'the positive badge — "Online", "Enabled", "Done"',
  },
]

describe.each(['light', 'dark'] as const)('%s theme contrast', (theme) => {
  const palette = palettes()[theme]

  it('defines every token the console pairs', () => {
    for (const pairing of PAIRINGS) {
      expect(palette[pairing.foreground], `${pairing.foreground} in ${theme}`).toBeTruthy()
      expect(palette[pairing.background], `${pairing.background} in ${theme}`).toBeTruthy()
    }
  })

  for (const { foreground, background, minimum, where } of PAIRINGS) {
    it(`${where} meets ${minimum}:1`, () => {
      const ratio = contrastRatio(palette[foreground] as string, palette[background] as string)
      expect(
        Number(ratio.toFixed(2)),
        `${foreground} on ${background} in the ${theme} theme is ${ratio.toFixed(2)}:1`,
      ).toBeGreaterThanOrEqual(minimum)
    })
  }
})

describe('the ratio calculation itself', () => {
  it('agrees with the two values everybody knows', () => {
    // Black on white is 21:1 and a colour on itself is 1:1. If the arithmetic
    // above ever drifts, every threshold assertion becomes meaningless — so the
    // measure is checked before it is trusted.
    expect(Math.round(contrastRatio('#000000', '#ffffff'))).toBe(21)
    expect(contrastRatio('#3f7fbf', '#3f7fbf')).toBeCloseTo(1, 5)
  })

  it('is symmetric, because contrast has no direction', () => {
    expect(contrastRatio('#14181f', '#ffffff')).toBeCloseTo(
      contrastRatio('#ffffff', '#14181f'),
      10,
    )
  })

  it('handles three-digit hex', () => {
    expect(contrastRatio('#000', '#fff')).toBeCloseTo(contrastRatio('#000000', '#ffffff'), 10)
  })
})
