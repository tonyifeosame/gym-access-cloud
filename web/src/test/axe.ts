import axe, { type AxeResults, type RunOptions, type Result } from 'axe-core'

/**
 * Automated accessibility checking, run inside the existing jsdom tests.
 *
 * FE-01 recorded that no accessibility verification of any kind had been done.
 * This closes the automated half of it and is honest about which half that is.
 *
 * WHAT AXE IN JSDOM CATCHES, and it is the bulk of what actually goes wrong in
 * an admin console: a control with no accessible name, a form field with no
 * label, a table with no header association, an ARIA attribute pointing at
 * nothing, a heading level skipped, a dialog with no name, a landmark
 * duplicated, a list containing something that is not a list item.
 *
 * WHAT IT CANNOT CATCH, and must not be claimed:
 *
 *   - COLOUR CONTRAST. jsdom implements no layout and no cascade, so axe
 *     disables the rule. Contrast is checked separately and directly against the
 *     token palette in contrast.test.ts, which is the one thing that CAN be
 *     verified without a browser.
 *   - Anything about focus ORDER as rendered, off-screen content, or overlap.
 *   - Whether a screen reader actually announces something sensibly. Automated
 *     tools catch missing names, not bad ones.
 *
 * So this is a floor, not a certificate. A manual screen-reader pass and a real
 * browser pass remain open and are named as such in the audit register rather
 * than quietly covered by these tests.
 */

/**
 * Rules disabled with a reason, never for convenience.
 *
 * `color-contrast` is the only one, and it is disabled because it CANNOT
 * produce a result in jsdom — axe needs computed styles and a layout box. It is
 * not skipped; it is checked elsewhere, by a test that computes the ratios from
 * the palette itself.
 */
const JSDOM_UNAVAILABLE = ['color-contrast'] as const

export interface AxeCheckOptions {
  /** Extra rules to disable, each of which must be justified at the call site. */
  disable?: string[]
}

/**
 * Runs axe over a container and returns the violations.
 *
 * Scoped to WCAG 2.1 A and AA plus the best-practice pack. AAA is deliberately
 * out: it includes rules (a 7:1 contrast floor, no abbreviations) that a data-
 * dense operator console cannot meet without becoming harder to use, and
 * claiming a level nobody is holding to is worse than naming the one that is.
 */
export async function findViolations(
  container: HTMLElement = document.body,
  options: AxeCheckOptions = {},
): Promise<Result[]> {
  const rules: RunOptions['rules'] = {}
  for (const rule of [...JSDOM_UNAVAILABLE, ...(options.disable ?? [])]) {
    rules[rule] = { enabled: false }
  }

  const results: AxeResults = await axe.run(container, {
    runOnly: { type: 'tag', values: ['wcag2a', 'wcag2aa', 'wcag21a', 'wcag21aa', 'best-practice'] },
    rules,
    // Every violation, not a sample: a second instance of the same fault is
    // usually a different component with the same mistake.
    resultTypes: ['violations'],
  })

  return results.violations
}

/**
 * A violation as a sentence somebody can act on.
 *
 * axe's raw output is a large nested object, and a test that prints it produces
 * a failure nobody reads. This gives the rule, the impact, the guidance and the
 * offending markup — which is enough to fix it without opening the tool.
 */
export function describeViolations(violations: Result[]): string {
  if (violations.length === 0) return 'no violations'

  return violations
    .map((violation) => {
      const nodes = violation.nodes
        .map((node) => `      ${node.html}\n      ${node.failureSummary ?? ''}`)
        .join('\n')
      return `  [${violation.impact ?? 'unknown'}] ${violation.id}: ${violation.help}\n${nodes}`
    })
    .join('\n\n')
}

/**
 * Asserts a container has no accessibility violations.
 *
 * Throws with the readable report rather than returning a boolean, so the test
 * that fails says what is wrong at the point of failure.
 */
export async function expectNoViolations(
  container: HTMLElement = document.body,
  options: AxeCheckOptions = {},
): Promise<void> {
  const violations = await findViolations(container, options)
  if (violations.length > 0) {
    throw new Error(
      `${violations.length} accessibility violation(s):\n\n${describeViolations(violations)}`,
    )
  }
}
