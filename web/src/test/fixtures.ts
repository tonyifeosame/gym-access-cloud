import type {
  AuditRecord,
  FieldEvent,
  Permission,
  Schedule,
  ConfiguredApplication,
  CredentialToken,
  FirmwareVersion,
  OperatorAccount,
  PendingTerminal,
  Person,
  Session,
  Site,
  Terminal,
} from '../api/types'
import type { PlatformCompany, PlatformSession } from '../platform/types'

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

/**
 * A site.
 *
 * The offline policy defaults to the platform's own column default —
 * `CACHED_GRACE` at 720 minutes — rather than to whatever is convenient, because
 * every screen that renders it is asserting something about what a door does and
 * a fixture is where a wrong default would go unnoticed.
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
    offline_policy: 'CACHED_GRACE',
    offline_grace_minutes: 720,
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

    // CAPABLE BY DEFAULT, because the default fixture is a terminal on current
    // firmware and that is the case most tests are about. The interesting rows
    // are the overrides: `capabilities: []` is a unit that reports and cannot,
    // and OMITTING the field entirely is the whole fleet in the field today --
    // one that has never said. Both are refused, and a test that wants either
    // has to ask for it.
    capabilities: ['wifi_provisioning', 'wifi_recovery', 'terminal_announce'],
    ...overrides,
  }
}

/**
 * A terminal that has announced itself and been adopted.
 *
 * DEFAULTS TO THE ORDINARY CASE: new hardware, nobody owns it, waiting for
 * somebody to choose a site. The interesting rows are the overrides — a
 * RE_PROVISION carries `existing_terminal`, and the two refusals carry a
 * verdict the console must not offer an Approve button for.
 */
export function makePendingTerminal(
  overrides: Partial<PendingTerminal> = {},
): PendingTerminal {
  return {
    id: 'announcement-1',
    serial_number: 'AT-A1B2C3',
    state: 'ADOPTED',
    verdict: 'NEW',
    firmware_version: '1.4.0',
    hardware_revision: 'rev-C',

    // REPORTED BY DEFAULT, because the default fixture is current firmware.
    // The interesting rows are the overrides: `capabilities: []` is a unit that
    // reports and cannot, and OMITTING the field is one that never said —
    // which the approval screen has to render as unknown rather than as none.
    capabilities: ['wifi_provisioning', 'wifi_recovery', 'terminal_announce'],
    first_seen_ip: '81.2.0.5',
    last_seen_ip: '81.2.0.5',
    last_seen_at: '2026-08-17T12:00:00Z',
    announced_at: '2026-08-17T11:58:00Z',
    adopted_by: 'owner@example.com',
    adopted_at: '2026-08-17T11:59:00Z',
    expires_at: '2026-08-17T12:14:00Z',
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

/**
 * A minted invitation or reset.
 *
 * The token is shaped like the server's — 64 hex characters — so that a test
 * asserting a link is not asserting on something implausibly short. It is still
 * a fixture and not a secret; what matters is that the console never stores it.
 */
export function makeCredentialToken(
  overrides: Partial<CredentialToken> = {},
): CredentialToken {
  return {
    token: 'a1b2c3d4'.repeat(8),
    purpose: 'INVITE',
    expires_at: '2030-01-01T00:00:00Z',
    shown_once: true,
    ...overrides,
  }
}

/**
 * One audit record.
 *
 * Defaults to an action with a named target, because the interesting rendering
 * question is how a record reads when it has one — an action with no target is
 * the degenerate case, not the representative one.
 */
export function makeAuditRecord(overrides: Partial<AuditRecord> = {}): AuditRecord {
  return {
    id: 'audit-1',
    action: 'TERMINAL_DISABLED',
    actor_email: 'ops@example.com',
    actor_role: 'ADMIN',
    target_type: 'TERMINAL',
    target_label: 'AT-0001',
    ip_address: '203.0.113.10',
    occurred_at: '2026-08-14T17:05:00Z',
    ...overrides,
  }
}

export function makeFirmwareVersion(
  overrides: Partial<FirmwareVersion> = {},
): FirmwareVersion {
  return {
    id: 1,
    public_id: 'firmware-1',
    version: '1.2.0',
    device_type: 'TERMINAL',
    release_channel: 'STABLE',
    is_mandatory: false,
    is_current: true,
    created_at: '2026-01-01T00:00:00Z',
    ...overrides,
  }
}

/**
 * The platform administrator's session.
 *
 * A DIFFERENT SHAPE from an operator session, deliberately: no company, no role,
 * no site grants and no applications. There is nothing about a tenant on it,
 * because this identity reaches `companies` and nothing inside them.
 */
export function makePlatformSession(
  overrides: Partial<PlatformSession> = {},
): PlatformSession {
  return {
    admin: {
      id: 'platform-admin-1',
      email: 'vendor@accesslink.example',
      full_name: 'Vendor Support',
      active: true,
    },
    csrf_token: 'platform-csrf-token',
    session_expires_at: '2030-01-01T00:00:00Z',
    session_expires_in_seconds: 604800,
    ...overrides,
  }
}

/**
 * A tenant as the platform sees it.
 *
 * Retention defaults to NULL, which means "keep for ever" — the state a company
 * is actually created in. Defaulting it to a number would let a console ship
 * that never rendered the indefinite case.
 */
export function makePlatformCompany(
  overrides: Partial<PlatformCompany> = {},
): PlatformCompany {
  return {
    id: 'company-1',
    name: 'Northwind Logistics',
    slug: 'northwind',
    contact_email: 'ops@northwind.example',
    timezone: 'Africa/Lagos',
    active: true,
    event_retention_days: null,
    audit_retention_days: null,
    site_count: 2,
    terminal_count: 5,
    person_count: 120,
    operator_count: 3,
    created_at: '2026-01-01T00:00:00Z',
    ...overrides,
  }
}

/**
 * One access rule.
 *
 * Defaults to a SITE-scoped ALLOW with no schedule and no validity window,
 * which is the plainest rule the model can express — every awkward case
 * (a deny, an expiry, a schedule) is then a visible override in the test that
 * needs it rather than something buried in the fixture.
 */
export function makePermission(overrides: Partial<Permission> = {}): Permission {
  return {
    id: 'permission-1',
    person_id: 'P-0001',
    scope_type: 'SITE',
    site_id: 'site-a',
    site_name: 'Lagos Depot',
    effect: 'ALLOW',
    active: true,
    created_at: '2026-01-01T00:00:00Z',
    updated_at: '2026-01-01T00:00:00Z',
    ...overrides,
  }
}

/**
 * A schedule.
 *
 * Defaults to weekdays 09:00-17:00 — five bits, not 127 — because a mask that
 * happens to be "every day" would let a broken day-mask conversion pass.
 */
export function makeSchedule(overrides: Partial<Schedule> = {}): Schedule {
  return {
    id: 'schedule-1',
    name: 'Office hours',
    windows: [{ days_of_week: 31, start_time: '09:00', end_time: '17:00' }],
    permission_count: 0,
    active: true,
    created_at: '2026-01-01T00:00:00Z',
    updated_at: '2026-01-01T00:00:00Z',
    ...overrides,
  }
}

/**
 * One thing that happened at a terminal.
 *
 * Defaults to a DENIAL, deliberately: a grant is a door opening and a denial is
 * somebody standing outside who expected not to be, which is the case the
 * screens exist to explain.
 */
export function makeEvent(overrides: Partial<FieldEvent> = {}): FieldEvent {
  return {
    id: 'event-1',
    event_type: 'ACCESS_DENIED',
    decision: 'DENIED',
    reason: 'NO_PERMISSION',
    application: 'ACCESS_CONTROL',
    site_name: 'Lagos Depot',
    device_serial: 'AT-0001',
    device_name: 'North Gate',
    person_id: 'person-public-1',
    person_name: 'Ada Okonkwo',
    subject_external_id: 'P-0001',
    occurred_at: '2026-08-15T08:00:00Z',
    recorded_at: '2026-08-15T08:00:00Z',
    occurred_at_trusted: true,
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
