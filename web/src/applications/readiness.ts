import type { ApplicationCode, ApplicationsResponse, ConfiguredApplication } from '../api/types'

/**
 * Where a capability stands for one company.
 *
 * THREE STATES THAT ARE ROUTINELY COLLAPSED INTO ONE, and collapsing them is how
 * a configuration screen starts describing something other than what it does:
 *
 *   AVAILABLE     The platform offers the capability at all. The server's
 *                 `available` list -- the catalogue, not a constant in this
 *                 build, so a capability added to the platform appears without a
 *                 frontend release.
 *
 *   ENABLED       This company has switched it on. Decides what the console
 *                 offers and what a terminal may be assigned to.
 *
 *   CONFIGURED    A settings record exists for it. Distinct from enabled: a
 *                 company can configure something it has not switched on, and
 *                 can enable something it has never configured.
 *
 * ALL THREE ARE FACTS ABOUT THIS COMPANY'S CONFIGURATION, which is what makes
 * them the console's business. This module used to carry a fourth, hard-coded
 * per-capability table describing how much of each workflow had been built, and
 * rendered it to operators as "Not built yet", "Partly built" and a paragraph of
 * gaps. That is a property of the BUILD rather than of the customer's
 * configuration, it does not belong on a screen a customer administers their
 * company from, and it is tracked in docs/market-readiness.md instead.
 */

export interface ApplicationReadiness {
  code: ApplicationCode
  available: boolean
  enabled: boolean
  configured: boolean
  record?: ConfiguredApplication
}

/** What this company's configuration says about one capability. */
export function readinessOf(
  code: ApplicationCode,
  response: Pick<ApplicationsResponse, 'available' | 'enabled' | 'configured'>,
): ApplicationReadiness {
  const record = response.configured.find((entry) => entry.code === code)

  return {
    code,
    available: response.available.includes(code),
    enabled: response.enabled.includes(code),
    // A row exists. Not "has non-empty settings": a company that deliberately
    // configured a capability with no options has still configured it.
    configured: Boolean(record),
    record,
  }
}

/**
 * A one-line summary of where a capability stands, for a list.
 *
 * ORDERED FROM THE STATE THAT EXPLAINS THE MOST. "Not offered by this platform"
 * answers a question the other two cannot, so it is tested first.
 */
export function summariseReadiness(readiness: ApplicationReadiness): string {
  if (!readiness.available) return 'Not offered by this platform'
  if (!readiness.enabled) return 'Available, not enabled'
  return readiness.configured ? 'Enabled and configured' : 'Enabled'
}
