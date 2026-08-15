import type { ApplicationCode, ApplicationsResponse, ConfiguredApplication } from '../api/types'

/**
 * How ready a capability actually is.
 *
 * FOUR STATES THAT ARE ROUTINELY COLLAPSED INTO ONE, and collapsing them is how
 * a configuration screen turns into a false claim about a product:
 *
 *   AVAILABLE     The platform offers the capability at all. The server's
 *                 `available` list — the catalogue, not a constant in this
 *                 build.
 *
 *   ENABLED       This company has switched it on. Decides what the console
 *                 offers and what a terminal may be assigned to.
 *
 *   CONFIGURED    A settings record exists for it. Distinct from enabled: a
 *                 company can configure something it has not switched on, and
 *                 can enable something it has never configured.
 *
 *   OPERATIONAL   THE PLATFORM ACTUALLY DOES THE WORK. Evaluates the decision,
 *                 records the attendance, computes the time. This is the only
 *                 one a customer cares about, and it is the only one that is
 *                 NOT derived from configuration.
 *
 * THE LAST DISTINCTION IS THE WHOLE POINT OF THIS MODULE. Every other state is
 * a fact about a row in a database, and a console can read all three and still
 * be lying: an owner who enables "Attendance", configures it, assigns a terminal
 * to it and sees three green ticks has been told the product records attendance.
 * It does not. Nothing in the platform yet evaluates any of these workflows.
 *
 * WHY IMPLEMENTATION STATUS IS A CONSTANT HERE AND NOT AN API FIELD. It is a
 * property of the BUILD — of what code exists — not of a company's
 * configuration, and there is no endpoint that reports it. Hard-coding it is
 * therefore correct rather than a shortcut, and it is recorded as a backend
 * requirement so that it can move to the catalogue when the server has something
 * to say. Until then, the honest place for "we have not built this yet" is the
 * build that has not built it.
 *
 * KEEP THIS TABLE IN STEP WITH docs/market-readiness.md. If they ever disagree,
 * the code is the one customers see.
 */

export type Implementation =
  /** Configuration exists. No behaviour. Nothing acts on it. */
  | 'NOT_IMPLEMENTED'
  /** Some of it works end to end; named gaps remain. */
  | 'PARTIAL'
  /** A person can be affected by it end to end, and an operator can see that. */
  | 'IMPLEMENTED'

interface ImplementationRecord {
  status: Implementation
  /** What is missing, in the operator's terms. Required for anything unfinished. */
  gap?: string
}

const IMPLEMENTATION: Record<string, ImplementationRecord> = {
  ACCESS_CONTROL: {
    status: 'PARTIAL',
    gap:
      'Terminals hold a roster and open for people on it, but the platform has no ' +
      'permission engine — there are no schedules, no per-door rules and no ' +
      'validity windows. Every person a terminal knows about is treated the same.',
  },
  REGISTRATION: {
    status: 'PARTIAL',
    gap:
      'People can be created and a credential can be enrolled at a terminal, but ' +
      'an enrolment is local to the terminal that took it: the same person is not ' +
      'recognised at any other door, and the console cannot start or manage an ' +
      'enrolment.',
  },
  ATTENDANCE: {
    status: 'NOT_IMPLEMENTED',
    gap: 'Nothing records attendance. No presence is derived, stored or reported.',
  },
  CHECK_IN: {
    status: 'NOT_IMPLEMENTED',
    gap: 'Nothing records a check-in. No arrival is stored or reported.',
  },
  VERIFICATION: {
    status: 'NOT_IMPLEMENTED',
    gap: 'Nothing performs or reports a verification.',
  },
  TIME_TRACKING: {
    status: 'NOT_IMPLEMENTED',
    gap: 'No worked time is accumulated from anything.',
  },
  VISITOR_MANAGEMENT: {
    status: 'NOT_IMPLEMENTED',
    gap: 'No visitor workflow exists. Visitors can only be added as ordinary people.',
  },
}

/**
 * What this build knows about a capability's implementation.
 *
 * An UNKNOWN code is reported as not implemented with the reason stated, rather
 * than as implemented or as unknown-and-therefore-fine. A capability newer than
 * this console is one this console cannot vouch for, and the safe direction to
 * be wrong in is the modest one.
 */
export function implementationOf(code: ApplicationCode): ImplementationRecord {
  return (
    IMPLEMENTATION[code] ?? {
      status: 'NOT_IMPLEMENTED',
      gap:
        'This capability is newer than this version of the console, which cannot ' +
        'say what the platform does with it. Treat it as unverified.',
    }
  )
}

export interface ApplicationReadiness {
  code: ApplicationCode
  available: boolean
  enabled: boolean
  configured: boolean
  operational: boolean
  implementation: Implementation
  gap?: string
  record?: ConfiguredApplication
}

/**
 * The four states for one capability.
 *
 * `operational` is deliberately a CONJUNCTION: enabled AND fully implemented. A
 * capability that works but is switched off is not operating, and one that is
 * switched on but unbuilt is not either. Reporting anything else as operational
 * would make the word useless, which is exactly the failure this module exists
 * to prevent.
 */
export function readinessOf(
  code: ApplicationCode,
  response: Pick<ApplicationsResponse, 'available' | 'enabled' | 'configured'>,
): ApplicationReadiness {
  const implementation = implementationOf(code)
  const record = response.configured.find((entry) => entry.code === code)
  const enabled = response.enabled.includes(code)

  return {
    code,
    available: response.available.includes(code),
    enabled,
    // A row exists. Not "has non-empty settings": a company that deliberately
    // configured a capability with no options has still configured it.
    configured: Boolean(record),
    operational: enabled && implementation.status === 'IMPLEMENTED',
    implementation: implementation.status,
    gap: implementation.gap,
    record,
  }
}

/**
 * A one-line summary of where a capability stands, for a list.
 *
 * ORDERED BY WHAT MISLEADS MOST. "Enabled, not yet operational" is the sentence
 * that has to appear on a capability somebody has just switched on and expects
 * to work — it is the whole reason this function is not simply enabled/disabled.
 */
export function summariseReadiness(readiness: ApplicationReadiness): string {
  if (!readiness.available) return 'Not offered by this platform'
  if (!readiness.enabled) {
    return readiness.implementation === 'IMPLEMENTED'
      ? 'Available, not enabled'
      : 'Available, not enabled — and not yet built'
  }
  if (readiness.operational) return 'Enabled and operational'
  return readiness.implementation === 'PARTIAL'
    ? 'Enabled — partly operational'
    : 'Enabled — not operational, nothing acts on it yet'
}

/** Whether anything a company has switched on actually does something. */
export function anyOperational(
  response: Pick<ApplicationsResponse, 'available' | 'enabled' | 'configured'>,
): boolean {
  return response.enabled.some((code) => readinessOf(code, response).operational)
}
