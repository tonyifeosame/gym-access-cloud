import { readdirSync, readFileSync } from 'node:fs'
import { join } from 'node:path'

import { describe, expect, it } from 'vitest'

/**
 * Every class a component renders has a rule somewhere.
 *
 * ---------------------------------------------------------------------------
 * WHY THIS EXISTS
 * ---------------------------------------------------------------------------
 *
 * The waiting-to-be-set-up panel shipped with markup and no stylesheet at all:
 * `pending-terminals__list`, `__item`, `__identity` and `__actions` were in the
 * JSX and in no CSS file. It rendered as a default bulleted list with its
 * approve and remove buttons stacked under each row -- on the one screen where
 * somebody is standing next to the hardware, waiting.
 *
 * NOTHING CAUGHT IT. jsdom does not lay out, so the unit suite could not; axe
 * and the contrast pass only judge what IS rendered, and an unstyled list has no
 * contrast problem; the browser sweep checks overflow and target size, which a
 * plain `<ul>` happens to satisfy. The gap was between the tools, so the check
 * has to be a string one: a class that appears in a `className` and in no rule
 * is either dead markup or missing styling, and both are worth a failure.
 *
 * IT IS DELIBERATELY CRUDE. It does not parse CSS or JSX -- it looks for the
 * class name as a selector anywhere in the stylesheets. That is enough to catch
 * "no rule at all", which is the failure that actually happened, without
 * pretending to understand cascade or specificity.
 */

const SRC = join(process.cwd(), 'src')
const STYLES = join(SRC, 'styles')

/**
 * Classes that carry no rule ON PURPOSE, each with the reason it is here.
 *
 * A waiver is a claim that the class does a job styling is not part of, and it
 * has to survive being read out loud. "It looked fine" is not one of them --
 * `field--checkbox` was on this list in an earlier draft and turned out to be a
 * real missing rule, so it was styled instead of waived.
 */
const DELIBERATELY_UNSTYLED: Record<string, string> = {
  // The browser sweep waits on `table, .state--empty` to know a list screen has
  // finished resolving. It is a hook the harness selects on; giving it a rule
  // would not make it more real, and deleting it would blind the sweep.
  'state--empty': 'the browser harness waits on this selector',
  'state--loading': 'the pair of state--empty; both name the state for tests',
  // Three spans in a row where only the value is emphasised: the label takes
  // .legend__item's muted colour by inheritance, and a rule restating that
  // would be noise that later drifts from the parent.
  legend__label: 'inherits .legend__item deliberately, in contrast with __value',
  // A BEM root. `.panel` supplies every visual the section has; this name exists
  // so the __list/__item/__identity/__actions rules have something to be
  // elements of.
  'pending-terminals': 'names the block its elements belong to; .panel styles it',
}

/** Rules live across several files; a class defined in any of them counts. */
function stylesheets(): string {
  return readdirSync(STYLES)
    .filter((name) => name.endsWith('.css'))
    .map((name) => readFileSync(join(STYLES, name), 'utf8'))
    .join('\n')
}

function sourceFiles(directory: string = SRC): string[] {
  return readdirSync(directory, { withFileTypes: true }).flatMap((entry) => {
    const path = join(directory, entry.name)
    if (entry.isDirectory()) return sourceFiles(path)
    return /\.tsx$/.test(entry.name) && !/\.test\.tsx$/.test(entry.name) ? [path] : []
  })
}

/**
 * Class names as they are written in JSX.
 *
 * TEMPLATE LITERALS AND TERNARIES ARE SKIPPED, not half-parsed: a
 * `className={open ? 'a' : 'b'}` is a runtime decision and reading one branch
 * out of it would report a class the component may never render. Plain string
 * attributes are the overwhelming majority and are where the gap appeared.
 */
function renderedClasses(): Map<string, string> {
  const found = new Map<string, string>()
  for (const path of sourceFiles()) {
    const source = readFileSync(path, 'utf8')
    for (const match of source.matchAll(/className="([^"{]+)"/g)) {
      for (const name of (match[1] ?? '').split(/\s+/).filter(Boolean)) {
        if (!found.has(name)) found.set(name, path.replace(process.cwd(), ''))
      }
    }
  }
  return found
}

describe('every rendered class is styled', () => {
  it('finds a rule for each one', () => {
    const css = stylesheets()
    const unstyled: string[] = []

    for (const [name, path] of renderedClasses()) {
      // A selector, not a substring: `.panel` must not satisfy `.panel__title`.
      if (name in DELIBERATELY_UNSTYLED) continue
      const selector = new RegExp(`\\.${name.replace(/[-_]/g, '[-_]')}(?![\\w-])`)
      if (!selector.test(css)) unstyled.push(`${name} (${path})`)
    }

    expect(unstyled, 'classes rendered with no rule anywhere').toEqual([])
  })

  /*
    THE ONE THE CHECK ABOVE WAS WRITTEN FOR, asserted by name so that deleting
    the rules and the generic check together still fails.
  */
  it('lays the waiting-to-be-set-up panel out rather than leaving it a bare list', () => {
    const css = stylesheets()

    expect(css).toMatch(/\.pending-terminals__list\s*\{/)
    // `list-style: none` is the difference between a designed list and a
    // bulleted one, and it is what was missing.
    const list = css.slice(css.indexOf('.pending-terminals__list'))
    expect(list.slice(0, list.indexOf('}'))).toMatch(/list-style:\s*none/)

    for (const part of ['__item', '__identity', '__actions']) {
      expect(css, `pending-terminals${part} has no rule`).toContain(`.pending-terminals${part}`)
    }
  })

  /*
    AND NOTHING IN THE OTHER DIRECTION. A rule for a class no component renders
    is dead weight that outlives whatever removed its markup --
    `.detail-list--compact` was exactly that, and is gone.
  */
  it('keeps no rule for a class nothing renders', () => {
    expect(stylesheets()).not.toContain('.detail-list--compact')
  })

  /*
    A WAIVER THAT STOPS BEING TRUE IS A WAIVER THAT SHOULD BE DELETED. If
    somebody later styles one of these, the entry above is stale and the reason
    it gives is wrong, so say so rather than letting the list rot.
  */
  it('keeps no waiver for a class that is styled after all', () => {
    const css = stylesheets()
    const stale = Object.keys(DELIBERATELY_UNSTYLED).filter((name) =>
      new RegExp(`\\.${name.replace(/[-_]/g, '[-_]')}(?![\\w-])`).test(css),
    )

    expect(stale, 'waived as unstyled, but a rule exists').toEqual([])
  })

  /*
    THE ONE REAL FINDING THIS CHECK TURNED UP BESIDES THE PANEL: a checkbox
    field's hint sat under the box rather than under the label it explains.
  */
  it('lines a checkbox hint up with its label, not with the box', () => {
    const css = stylesheets()
    const rule = css.slice(css.indexOf('.field--checkbox .field__hint'))

    expect(rule.slice(0, rule.indexOf('}'))).toMatch(/padding-inline-start:\s*calc\(24px/)
  })

})
