import type { Role, Session } from '../api/types'
import { roleAtLeast } from '../auth/roles'
import { applicationPath, describeApplication } from '../applications/registry'

/**
 * How the console decides what to show.
 *
 * TWO KINDS OF ENTRY, AND THE DISTINCTION IS THE PRODUCT:
 *
 *   Platform  Company, sites, people, terminals, operators. These exist for
 *             every AccessLink deployment regardless of what it is used for.
 *             They are the platform's own nouns.
 *
 *   Modules   Derived entirely from the capabilities the company has enabled.
 *             Nothing here is assumed, and a company with none enabled simply
 *             has no module entries -- a legitimate, fully-working state.
 *
 * Collapsing those two would be how a general-purpose platform turns into a
 * single-purpose product: the moment a workflow becomes permanent navigation, it
 * has stopped being configuration.
 */

export interface NavItem {
  id: string
  label: string
  path: string
  /** Lowest role that may see it. */
  minimumRole: Role
  /** True for capability-derived entries. */
  module?: boolean
  description?: string
}

/** Present for every company, whatever it uses the platform for. */
export const PLATFORM_NAV: NavItem[] = [
  { id: 'dashboard', label: 'Overview', path: '/', minimumRole: 'VIEWER' },
  { id: 'people', label: 'People', path: '/people', minimumRole: 'VIEWER' },
  { id: 'terminals', label: 'Terminals', path: '/terminals', minimumRole: 'VIEWER' },
  {
    id: 'events',
    label: 'Events',
    path: '/events',
    // VIEWER: "why was she refused" is a question somebody at a front desk has
    // to be able to answer without an administrator.
    minimumRole: 'VIEWER',
  },
  {
    id: 'schedules',
    label: 'Schedules',
    path: '/access/schedules',
    // Readable by anyone; the write controls carry their own MANAGER gate.
    minimumRole: 'VIEWER',
  },
  { id: 'sites', label: 'Sites', path: '/sites', minimumRole: 'VIEWER' },
  { id: 'operators', label: 'Operators', path: '/operators', minimumRole: 'ADMIN' },
  {
    id: 'activity',
    label: 'Activity',
    path: '/activity',
    // ADMIN, matching the server: the trail names which operators did what.
    minimumRole: 'ADMIN',
  },
  {
    id: 'applications',
    label: 'Applications',
    path: '/settings/applications',
    // ADMIN sees what the company is configured for; only OWNER may change it,
    // and that gate is on the controls rather than the route.
    minimumRole: 'ADMIN',
  },
  {
    id: 'settings',
    label: 'Settings',
    path: '/settings',
    // VIEWER: it holds YOUR OWN account and password. Every operator needs it,
    // and nothing on it is privileged -- the company section is read-only and
    // the rest are links to pages with their own gates.
    minimumRole: 'VIEWER',
  },
]

export function platformNav(role: string): NavItem[] {
  return PLATFORM_NAV.filter((item) => roleAtLeast(role, item.minimumRole))
}

/**
 * Navigation for the capabilities this company has enabled and this operator may
 * open. Order follows the API's, which is stable, so the menu does not reshuffle
 * between requests.
 */
export function moduleNav(session: Session): NavItem[] {
  return session.applications
    .map((application) => describeApplication(application.code))
    .filter((definition) => roleAtLeast(session.role, definition.minimumRole))
    .map((definition) => ({
      id: `application:${definition.code}`,
      label: definition.label,
      path: applicationPath(definition),
      minimumRole: definition.minimumRole,
      module: true,
      description: definition.description,
    }))
}

export function navigationFor(session: Session): { platform: NavItem[]; modules: NavItem[] } {
  return {
    platform: platformNav(session.role),
    modules: moduleNav(session),
  }
}
