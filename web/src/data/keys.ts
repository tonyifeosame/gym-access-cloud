import type { AuditQuery, PeopleQuery } from '../api/types'

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
