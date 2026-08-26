import type { ApplicationCode, Role } from '../api/types'

/**
 * The application registry.
 *
 * AccessLink is a general-purpose biometric terminal platform. An application is
 * a CAPABILITY a company may enable -- not an industry, not a customer, and not
 * the identity of this product. Two companies running the same build may share
 * no enabled capabilities at all, and both are using it correctly.
 *
 * Consequences this file exists to enforce:
 *
 *   * Navigation is DERIVED from the session's `applications`, never hard-coded.
 *     A company with none enabled gets no module entries, and that is a
 *     legitimate, fully-working state rather than something to paper over.
 *
 *   * An UNKNOWN code still renders. The API's `available` list is the real
 *     catalog, so a capability added to the platform appears in Applications
 *     without a frontend release, humanised from its code.
 *
 *   * MULTI_PURPOSE never appears. It is a terminal mode describing a device
 *     that serves whatever its company has enabled, not a capability a company
 *     can turn on, and the API rejects it as one.
 */

export interface ApplicationDefinition {
  code: ApplicationCode
  /** URL segment under /applications. */
  slug: string
  label: string
  /** One line, shown wherever the capability is listed. */
  description: string
  /** Lowest role that may open the module. */
  minimumRole: Role
  /**
   * The console screen this capability owns, if it has one.
   *
   * ABSENT MEANS NO NAVIGATION ENTRY, and every definition below is currently
   * absent. Capabilities used to share one parameterised route that rendered a
   * page explaining the screens had not been built; a menu item leading there
   * costs an operator a click to be told about the state of our development, so
   * a capability now earns its entry by having somewhere to go.
   *
   * Setting this is the whole of what it takes to put one back in the
   * navigation -- `moduleNav` reads it directly.
   */
  route?: string
}

const DEFINITIONS: ApplicationDefinition[] = [
  {
    code: 'ACCESS_CONTROL',
    slug: 'access-control',
    label: 'Access Control',
    description: 'Decide whether to release a door, barrier or lock.',
    minimumRole: 'VIEWER',
  },
  {
    code: 'ATTENDANCE',
    slug: 'attendance',
    label: 'Attendance',
    description: 'Record presence against a schedule.',
    minimumRole: 'VIEWER',
  },
  {
    code: 'REGISTRATION',
    slug: 'registration',
    label: 'Registration',
    description: 'Enrol people and their credentials.',
    minimumRole: 'MANAGER',
  },
  {
    code: 'CHECK_IN',
    slug: 'check-in',
    label: 'Check-in',
    description: 'Record arrival at an event or appointment.',
    minimumRole: 'VIEWER',
  },
  {
    code: 'VERIFICATION',
    slug: 'verification',
    label: 'Verification',
    description: 'Confirm a person is who they claim, and report it.',
    minimumRole: 'VIEWER',
  },
  {
    code: 'TIME_TRACKING',
    slug: 'time-tracking',
    label: 'Time Tracking',
    description: 'Accumulate worked time from arrivals and departures.',
    minimumRole: 'VIEWER',
  },
  {
    code: 'VISITOR_MANAGEMENT',
    slug: 'visitor-management',
    label: 'Visitor Management',
    description: 'Admit and record people who are not on the roster.',
    minimumRole: 'VIEWER',
  },
]

const BY_CODE = new Map(DEFINITIONS.map((definition) => [definition.code, definition]))
const BY_SLUG = new Map(DEFINITIONS.map((definition) => [definition.slug, definition]))

/**
 * The description given to a code this build has never heard of.
 *
 * SINGLE-SOURCED HERE, because two places have to agree on it and they are
 * compared by identity: `describeApplication` writes it, and the detail page
 * asks "is this description the invented one" to decide whether to warn that
 * the console has no copy for the feature. When the same sentence was written
 * out in both files, editing one of them made that comparison permanently
 * false -- every unknown feature silently lost its warning, and nothing failed
 * except a test nobody had run yet. Import it; do not retype it.
 */
export const UNKNOWN_DESCRIPTION = 'Recently added to the platform.'

/** Turns AN_UNKNOWN_CODE into "An Unknown Code". */
function humanise(code: string): string {
  return code
    .toLowerCase()
    .split('_')
    .filter(Boolean)
    .map((word) => word.charAt(0).toUpperCase() + word.slice(1))
    .join(' ')
}

function slugify(code: string): string {
  return code.toLowerCase().replace(/_/g, '-')
}

/**
 * Resolves a code to a definition, inventing a reasonable one when this build
 * has never heard of it. Never returns undefined: a capability the API reports
 * as enabled must be visible to the operator even if we cannot describe it, or
 * the console would silently hide part of what the company is paying for.
 */
export function describeApplication(code: ApplicationCode): ApplicationDefinition {
  const known = BY_CODE.get(code)
  if (known) return known

  return {
    code,
    slug: slugify(code),
    label: humanise(code),
    description: UNKNOWN_DESCRIPTION,
    minimumRole: 'VIEWER',
  }
}

export function findApplicationBySlug(slug: string): ApplicationDefinition | undefined {
  return BY_SLUG.get(slug)
}

/** The codes this build can describe. The API's `available` is the real list. */
export function knownApplications(): ApplicationDefinition[] {
  return [...DEFINITIONS]
}
