import type { AuditQuery, EventQuery, PeopleQuery } from '../api/types'

/**
 * Normalises an event filter into a stable cache key.
 *
 * Every field, sorted, with absent and empty collapsed — so `{}` and
 * `{q: ''}` are one entry rather than two, and adding a filter to the type
 * cannot silently split the cache because somebody forgot to list it here.
 */
function normaliseEventQuery(query: EventQuery): Record<string, string | number> {
  const entries = Object.entries(query)
    .filter(([, value]) => value !== undefined && value !== '')
    .map(([key, value]) => [key, value as string | number] as const)
    .sort(([a], [b]) => a.localeCompare(b))
  return Object.fromEntries(entries)
}

/**
 * Query keys, in one place.
 *
 * WHY A FACTORY RATHER THAN INLINE ARRAYS. A key is written in two places that
 * must agree — where data is read and where it is invalidated after a write —
 * and when they drift the symptom is a screen showing stale data after a
 * successful save. That bug is invisible in review and annoying to reproduce, so
 * neither spelling is written by hand.
 *
 * The keys are HIERARCHICAL, and the hierarchy is what makes invalidation
 * precise. React Query matches a prefix, so `keys.people.all` invalidates every
 * page and every search of people without touching terminals, and
 * `keys.terminals.detail(serial)` refreshes exactly one row.
 *
 * Everything hangs off a single `console` root so that a session ending can drop
 * all tenant data in one call while leaving the session query itself alone.
 */

const ROOT = 'console' as const

export const keys = {
  /** Everything fetched on behalf of the signed-in company. */
  all: [ROOT] as const,

  company: {
    all: [ROOT, 'company'] as const,
    detail: () => [ROOT, 'company', 'detail'] as const,
  },

  sites: {
    all: [ROOT, 'sites'] as const,
    list: () => [ROOT, 'sites', 'list'] as const,
    detail: (siteId: string) => [ROOT, 'sites', 'detail', siteId] as const,
    settings: (siteId: string) => [ROOT, 'sites', 'settings', siteId] as const,
  },

  terminals: {
    all: [ROOT, 'terminals'] as const,
    list: (options: { outdated?: boolean } = {}) =>
      [ROOT, 'terminals', 'list', { outdated: options.outdated ?? false }] as const,
    summary: () => [ROOT, 'terminals', 'summary'] as const,
    detail: (serial: string) => [ROOT, 'terminals', 'detail', serial] as const,

    /**
     * The Change Wi-Fi command's progress, for one terminal.
     *
     * UNDER `terminals` rather than beside it, because it genuinely belongs to
     * one terminal and every fleet-wide invalidation should refresh it — a
     * terminal that has just been disabled or retired must not keep polling a
     * command that can no longer be delivered.
     */
    wifiRecovery: (serial: string) => [ROOT, 'terminals', 'wifi-recovery', serial] as const,
  },

  /**
   * Terminals waiting to be set up.
   *
   * A SIBLING OF `terminals` RATHER THAN A CHILD, deliberately. A pending
   * terminal is not a terminal yet — it has no credential, no site until it is
   * approved, and no row in the fleet — and nesting it under `terminals.all`
   * would make every fleet invalidation refetch it and vice versa.
   *
   * The two do have to move together at one moment, and exactly one: approval
   * turns a pending row into a terminal shortly afterwards. The mutation hooks
   * invalidate both by hand there, which is one deliberate line rather than a
   * hierarchy that couples them everywhere.
   */
  pendingTerminals: {
    all: [ROOT, 'pending-terminals'] as const,
    list: () => [ROOT, 'pending-terminals', 'list'] as const,
    detail: (id: string) => [ROOT, 'pending-terminals', 'detail', id] as const,
  },

  people: {
    all: [ROOT, 'people'] as const,
    // The query is part of the key, so a search and a page are separate cache
    // entries rather than one that overwrites the other. Normalised so that
    // `{}` and `{search: ''}` do not become two keys for the same request.
    list: (query: PeopleQuery = {}) =>
      [
        ROOT,
        'people',
        'list',
        {
          search: query.search?.trim() ?? '',
          limit: query.limit ?? null,
          offset: query.offset ?? 0,
        },
      ] as const,
    detail: (externalId: string) => [ROOT, 'people', 'detail', externalId] as const,
  },

  operators: {
    all: [ROOT, 'operators'] as const,
    list: () => [ROOT, 'operators', 'list'] as const,
    detail: (operatorId: string) => [ROOT, 'operators', 'detail', operatorId] as const,
    sites: (operatorId: string) => [ROOT, 'operators', 'sites', operatorId] as const,
  },

  applications: {
    all: [ROOT, 'applications'] as const,
    list: () => [ROOT, 'applications', 'list'] as const,
  },

  /**
   * Access-control rules, keyed by the PERSON they belong to.
   *
   * There is no company-wide permission list endpoint, so this hierarchy mirrors
   * the API rather than inventing a shape the server cannot serve. Granting or
   * revoking invalidates one person's rules and nothing else.
   */
  permissions: {
    all: [ROOT, 'permissions'] as const,
    forPerson: (externalId: string) => [ROOT, 'permissions', 'person', externalId] as const,
  },

  schedules: {
    all: [ROOT, 'schedules'] as const,
    list: () => [ROOT, 'schedules', 'list'] as const,
  },

  /**
   * Field events — the door log.
   *
   * Separate from `audit`: different endpoint, different role, different
   * meaning. Collapsing them would let one invalidation refresh the other and
   * make "what happened at the door" and "what an operator changed" share a
   * cache entry.
   */
  events: {
    all: [ROOT, 'events'] as const,
    list: (query: EventQuery = {}) => [ROOT, 'events', 'list', normaliseEventQuery(query)] as const,
  },

  /**
   * The audit trail.
   *
   * The whole filter is part of the key, normalised so that an absent filter and
   * an empty one are the same cache entry rather than two. Every mutation hook
   * that writes an audit record invalidates `all`, which is why the filters hang
   * below it: a new record may belong to any filtered view.
   */
  audit: {
    all: [ROOT, 'audit'] as const,
    list: (query: AuditQuery = {}) =>
      [
        ROOT,
        'audit',
        'list',
        {
          action: query.action ?? '',
          target_type: query.target_type ?? '',
          actor: query.actor?.trim() ?? '',
          since: query.since ?? '',
          until: query.until ?? '',
          limit: query.limit ?? null,
          offset: query.offset ?? 0,
        },
      ] as const,
  },

  firmware: {
    all: [ROOT, 'firmware'] as const,
    list: () => [ROOT, 'firmware', 'list'] as const,
  },
} as const
