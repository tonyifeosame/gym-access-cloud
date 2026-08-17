import type { ApplicationCode, EnabledApplication, Role } from '../api/types'

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
 *     catalog, so a capability added to the platform appears here without a
 *     frontend release -- humanised label, generic route, placeholder page.
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
  /** One line, shown on the placeholder and in the module list. */
  description: string
  /** Lowest role that may open the module. */
  minimumRole: Role
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
    description: 'This capability is newer than this version of the console.',
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

export function applicationPath(definition: ApplicationDefinition): string {
  return `/applications/${definition.slug}`
}

/**
 * The modules an operator should see: enabled for the company AND permitted by
 * their role. Order follows the API's, which is stable, so navigation does not
 * reshuffle between requests.
 */
export function visibleApplications(
  enabled: EnabledApplication[],
  role: string,
  roleAtLeast: (role: string | undefined, minimum: Role) => boolean,
): ApplicationDefinition[] {
  return enabled
    .map((application) => describeApplication(application.code))
    .filter((definition) => roleAtLeast(role, definition.minimumRole))
}
