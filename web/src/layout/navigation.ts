import type { Role, Session } from '../api/types'
import { roleAtLeast } from '../auth/roles'
import { describeApplication } from '../applications/registry'

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
  /** Which of the three headings a platform entry sits under. */
  group?: NavGroupId
  /** True for capability-derived entries. */
  module?: boolean
  description?: string
}

/**
 * The three headings the platform entries sit under.
 *
 * ELEVEN LINKS OF EQUAL WEIGHT WAS THE PROBLEM. A front-desk manager uses four
 * of them every day and the other seven looked just as important and just as
 * alarming -- "Firmware" and "API access" sat one line below "People". The
 * grouping separates "my day" from "setting things up" from "the account", and
 * that is all it does: the links, their labels and their role gates are exactly
 * what they were.
 */
export type NavGroupId = 'everyday' | 'manage' | 'settings'

export const NAV_GROUPS: { id: NavGroupId; title: string }[] = [
  { id: 'everyday', title: 'Everyday' },
  { id: 'manage', title: 'Manage' },
  { id: 'settings', title: 'Settings' },
]

export interface NavGroup {
  id: NavGroupId
  title: string
  items: NavItem[]
}

/** Present for every company, whatever it uses the platform for. */
export const PLATFORM_NAV: NavItem[] = [
  // --- Everyday: what a front desk opens every day ---------------------------
  { id: 'dashboard', label: 'Overview', path: '/', minimumRole: 'VIEWER', group: 'everyday' },
  { id: 'people', label: 'People', path: '/people', minimumRole: 'VIEWER', group: 'everyday' },
  {
    id: 'terminals',
    label: 'Terminals',
    path: '/terminals',
    minimumRole: 'VIEWER',
    group: 'everyday',
  },
  {
    id: 'events',
    label: 'Events',
    path: '/events',
    // VIEWER: "why was she refused" is a question somebody at a front desk has
    // to be able to answer without an administrator.
    minimumRole: 'VIEWER',
    group: 'everyday',
  },

  // --- Manage: setting up and looking after the deployment -------------------
  {
    id: 'schedules',
    label: 'Schedules',
    path: '/access/schedules',
    // Readable by anyone; the write controls carry their own MANAGER gate.
    minimumRole: 'VIEWER',
    group: 'manage',
  },
  { id: 'sites', label: 'Sites', path: '/sites', minimumRole: 'VIEWER', group: 'manage' },
  {
    id: 'operators',
    label: 'Operators',
    path: '/operators',
    minimumRole: 'ADMIN',
    group: 'manage',
  },
  {
    id: 'activity',
    label: 'Activity',
    path: '/activity',
    // ADMIN, matching the server: the trail names which operators did what.
    minimumRole: 'ADMIN',
    group: 'manage',
  },

  // --- Settings: the account and the things an administrator configures once -
  {
    id: 'settings',
    label: 'Settings',
    path: '/settings',
    // VIEWER: it holds YOUR OWN account and password. Every operator needs it,
    // and nothing on it is privileged -- the company section is read-only and
    // the rest are links to pages with their own gates.
    minimumRole: 'VIEWER',
    group: 'settings',
  },
  {
    id: 'applications',
    // "Features" is the customer-facing word for a capability throughout the
    // console. The id and the path stay as they are: one is what tests and code
    // address this entry by, the other is a URL people may have bookmarked.
    label: 'Features',
    path: '/settings/applications',
    // ADMIN sees what the company is configured for; only OWNER may change it,
    // and that gate is on the controls rather than the route.
    minimumRole: 'ADMIN',
    group: 'settings',
  },
  {
    id: 'firmware',
    label: 'Firmware',
    path: '/settings/firmware',
    // ADMIN, matching the server. The catalogue decides what the fleet is
    // measured against, which is why these routes left the site-key tree.
    minimumRole: 'ADMIN',
    group: 'settings',
  },
  {
    id: 'api-credentials',
    label: 'API access',
    path: '/settings/api-credentials',
    // ADMIN, matching the server's route group: issuing a key is "let this
    // company's data leave it", the same decision as rotating a site key, and
    // the server mounts both on the same ADMIN group. OWNER qualifies through
    // the hierarchy; MANAGER and below never see the entry and get 403 from
    // the API if they type the URL.
    minimumRole: 'ADMIN',
    group: 'settings',
  },
]

export function platformNav(role: string): NavItem[] {
  return PLATFORM_NAV.filter((item) => roleAtLeast(role, item.minimumRole))
}

/**
 * The platform entries this role may see, under their three headings.
 *
 * A HEADING WITH NOTHING UNDER IT IS DROPPED rather than rendered empty, and
 * order within a group is the order of PLATFORM_NAV, so nothing reshuffles by
 * role -- a VIEWER's "Manage" is simply Schedules and Sites.
 */
export function groupedPlatformNav(role: string): NavGroup[] {
  const visible = platformNav(role)
  return NAV_GROUPS.map((group) => ({
    ...group,
    items: visible.filter((item) => item.group === group.id),
  })).filter((group) => group.items.length > 0)
}

/**
 * Navigation for the capabilities this company has enabled, this operator may
 * open, AND that have a screen to open.
 *
 * THE LAST CONDITION IS THE ONE THAT MATTERS TODAY, and it is why this currently
 * returns nothing. Every enabled capability used to produce an entry pointing at
 * a shared page whose entire content was that the screens had not been built. An
 * operator following one spent a click to be told about the state of our
 * development, and a company that had enabled six capabilities got six of them.
 *
 * The mechanism is intact rather than deleted: this is still derived from the
 * session and nothing is hard-coded, so the day a capability gains a screen it
 * gains its entry by declaring `route` in the registry -- no change here.
 *
 * Order follows the API's, which is stable, so the menu does not reshuffle
 * between requests.
 */
export function moduleNav(session: Session): NavItem[] {
  return session.applications
    .map((application) => describeApplication(application.code))
    .filter((definition) => roleAtLeast(session.role, definition.minimumRole))
    .flatMap((definition) =>
      definition.route
        ? [
            {
              id: `application:${definition.code}`,
              label: definition.label,
              path: definition.route,
              minimumRole: definition.minimumRole,
              module: true,
              description: definition.description,
            },
          ]
        : [],
    )
}

export function navigationFor(session: Session): {
  platform: NavItem[]
  groups: NavGroup[]
  modules: NavItem[]
} {
  return {
    platform: platformNav(session.role),
    groups: groupedPlatformNav(session.role),
    modules: moduleNav(session),
  }
}
