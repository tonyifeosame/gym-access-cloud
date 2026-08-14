import type { Session } from '../api/types'

/**
 * Session fixtures.
 *
 * Note what the DEFAULT is: a company with NO applications enabled. That is the
 * state every company starts in, and making it the default keeps the tests
 * honest about the console working without any capability configured.
 */
export function makeSession(overrides: Partial<Session> = {}): Session {
  return {
    operator: {
      id: 'operator-1',
      email: 'ops@example.com',
      full_name: 'Ops Person',
      role: 'OWNER',
    },
    company: { id: 'company-1', name: 'Northwind Logistics', slug: 'northwind' },
    role: 'OWNER',
    sites: [],
    all_sites: true,
    applications: [],
    csrf_token: 'csrf-token-value',
    session_expires_at: '2030-01-01T00:00:00Z',
    session_expires_in_seconds: 604800,
    ...overrides,
  }
}

export const SITE_A = { site_id: 'site-a', site_name: 'Lagos Depot' }
export const SITE_B = { site_id: 'site-b', site_name: 'Abuja Depot' }
