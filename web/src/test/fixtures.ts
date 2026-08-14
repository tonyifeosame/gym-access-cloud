import type {
  ConfiguredApplication,
  OperatorAccount,
  Person,
  Session,
  Site,
  Terminal,
} from '../api/types'

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

/**
 * Resource fixtures.
 *
 * Deliberately DOMAIN-NEUTRAL: depots, gates, contractors and visitors. Naming a
 * fixture "member" or "trainer" would quietly reintroduce the single-purpose
 * product this platform is not, and fixtures are where that kind of assumption
 * usually creeps back in first.
 */

export function makeSite(overrides: Partial<Site> = {}): Site {
  return {
    id: 'site-a',
    name: 'Lagos Depot',
    address: '14 Marina Road',
    timezone: 'Africa/Lagos',
    active: true,
    terminal_count: 2,
    created_at: '2026-01-01T00:00:00Z',
    ...overrides,
  }
}

export function makeTerminal(overrides: Partial<Terminal> = {}): Terminal {
  return {
    id: 1,
    public_id: 'terminal-public-1',
    site_id: 1,
    // The id a browser can actually join on; site_id is internal.
    site_public_id: 'site-a',
    site_name: 'Lagos Depot',
    serial_number: 'AT-0001',
    device_name: 'North Gate',
    device_type: 'TERMINAL',
    status: 'ONLINE',
    active: true,
    release_channel: 'STABLE',
    firmware_version: '1.2.0',
    hardware_revision: 'rev-c',
    build_number: '456',
    boot_count: 12,
    last_seen_at: '2026-08-14T17:00:00Z',
    last_sync_at: '2026-08-14T17:00:00Z',
    last_heartbeat_at: '2026-08-14T17:00:00Z',
    current_firmware_version: '1.2.0',
    firmware_outdated: false,
    ...overrides,
  }
}

export function makePerson(overrides: Partial<Person> = {}): Person {
  return {
    id: 'person-public-1',
    external_id: 'P-0001',
    full_name: 'Ada Okonkwo',
    category: 'STANDARD',
    active: true,
    // A BOOLEAN is the entire biometric surface. No template, no locator, no
    // sensor detail — there is deliberately nothing else to put here.
    biometric_enrolled: false,
    created_at: '2026-01-02T09:00:00Z',
    updated_at: '2026-01-02T09:00:00Z',
    ...overrides,
  }
}

export function makeOperatorAccount(
  overrides: Partial<OperatorAccount> = {},
): OperatorAccount {
  return {
    id: 'operator-2',
    email: 'viewer@example.com',
    full_name: 'Site Viewer',
    role: 'VIEWER',
    active: true,
    last_login_at: '2026-08-13T08:30:00Z',
    sites: [],
    all_sites: true,
    created_at: '2026-01-01T00:00:00Z',
    ...overrides,
  }
}

export function makeApplication(
  overrides: Partial<ConfiguredApplication> = {},
): ConfiguredApplication {
  return {
    id: 'app-1',
    code: 'ACCESS_CONTROL',
    enabled: true,
    settings: {},
    created_at: '2026-01-01T00:00:00Z',
    updated_at: '2026-01-01T00:00:00Z',
    ...overrides,
  }
}
