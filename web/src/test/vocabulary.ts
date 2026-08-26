import { expect } from 'vitest'

/**
 * The word the product does not use for the place a terminal stands at.
 *
 * ---------------------------------------------------------------------------
 * WHY THIS IS ONE CONSTANT RATHER THAN A REGEX PER TEST
 * ---------------------------------------------------------------------------
 *
 * AccessLink is not a door product. A terminal is bolted to a door in an
 * office, to a turnstile in a warehouse, to a barrier at a yard gate, to a
 * locker in a school and to a lift in a residential block, and copy that names
 * one of them describes the others wrongly. "Access point" is the neutral name
 * for the physical endpoint; "terminal" names the hardware; "site" names the
 * location.
 *
 * The screens this guards -- Overview, Activity, Events and Terminals -- each
 * used to carry the word in a heading, an empty state or a note, so the rule is
 * asserted from ONE definition. Four copies of the same regex is how a rule
 * that holds on three screens and not the fourth comes about.
 *
 * NOTHING HERE CONSTRAINS THE BACKEND. Field names, event codes, firmware
 * semantics and internal identifiers are untouched by this: it reads rendered
 * text, which is the only place the customer meets our vocabulary.
 */
export const DOOR_WORDING = /\bdoors?\b/i

/**
 * Fail with the offending sentence rather than with "expected false to be true".
 *
 * A bare `not.toMatch` says a screen contains a banned word and leaves whoever
 * broke it to find which of a page of copy did it. The surrounding words are
 * what makes the failure actionable, so they are quoted.
 */
export function expectNoDoorWording(surface: string, text: string): void {
  const match = DOOR_WORDING.exec(text)
  const context = match
    ? text.slice(Math.max(0, match.index - 60), match.index + match[0].length + 60).trim()
    : ''

  expect(
    match,
    `${surface} calls an access point a "${match?.[0]}": …${context}…`,
  ).toBeNull()
}
