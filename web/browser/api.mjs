/**
 * The mock API for the browser pass, served by intercepting the network.
 *
 * SHAPED ON THE REAL CONTRACTS, exactly as the MSW mock in src/test/server.ts
 * is, and for the same reason: a mock that agrees with the client rather than
 * with the API passes while the real thing fails. The two are separate because
 * they run in different places — MSW inside the test process, this in the
 * browser's network layer — and duplicating a handful of payload shapes is
 * cheaper than making one work in both.
 *
 * The DATA is deliberately fuller than the unit fixtures: enough people to page
 * through, terminals in several states, long names that will actually wrap. The
 * point of a browser pass is layout and contrast, and a table with two short
 * rows exercises neither.
 */

const SESSION = {
  operator: {
    id: 'operator-1',
    email: 'oluwaseun.adebayo@northwind.example',
    full_name: 'Oluwaseun Adebayo',
    role: 'OWNER',
  },
  company: { id: 'company-1', name: 'Northwind Logistics International', slug: 'northwind' },
  role: 'OWNER',
  sites: [],
  all_sites: true,
  applications: [
    { code: 'ACCESS_CONTROL', settings: {} },
    { code: 'ATTENDANCE', settings: {} },
  ],
  must_change_password: false,
  csrf_token: 'browser-csrf-token',
  session_expires_at: '2030-01-01T00:00:00Z',
  session_expires_in_seconds: 604800,
}

const SITES = [
  {
    id: 'site-a',
    name: 'Lagos Distribution Centre',
    address: '14 Marina Road, Lagos Island',
    timezone: 'Africa/Lagos',
    active: true,
    terminal_count: 3,
    created_at: '2026-01-01T00:00:00Z',
  },
  {
    id: 'site-b',
    name: 'Abuja Depot',
    address: '3 Aminu Kano Crescent, Wuse II',
    timezone: 'Africa/Lagos',
    active: false,
    terminal_count: 1,
    created_at: '2026-02-01T00:00:00Z',
  },
]

function terminal(index, overrides = {}) {
  return {
    id: index,
    public_id: `terminal-public-${index}`,
    site_id: 1,
    site_public_id: 'site-a',
    site_name: 'Lagos Distribution Centre',
    serial_number: `AT-${String(index).padStart(4, '0')}`,
    device_name: `Gate ${index}`,
    device_type: 'TERMINAL',
    status: 'ONLINE',
    active: true,
    release_channel: 'STABLE',
    firmware_version: '1.2.0',
    hardware_revision: 'rev-c',
    build_number: '456',
    boot_count: 12,
    last_seen_at: '2026-08-15T09:00:00Z',
    last_sync_at: '2026-08-15T09:00:00Z',
    last_heartbeat_at: '2026-08-15T09:00:00Z',
    current_firmware_version: '1.2.0',
    firmware_outdated: false,
    ...overrides,
  }
}

// Every status the badge knows how to draw, so the contrast check sees them all.
const TERMINALS = [
  terminal(1, { device_name: 'North Gate (staff and contractor entrance)' }),
  terminal(2, { status: 'OFFLINE', device_name: 'Loading Bay' }),
  terminal(3, { status: 'ERROR', device_name: 'Reception', firmware_outdated: true, firmware_version: '1.1.0' }),
  terminal(4, {
    status: 'DISABLED',
    active: false,
    site_public_id: 'site-b',
    site_name: 'Abuja Depot',
    device_name: 'Side Door',
  }),
]

const PEOPLE = Array.from({ length: 64 }, (_, index) => ({
  id: `person-${index}`,
  external_id: `P-${String(index + 1).padStart(4, '0')}`,
  full_name:
    index % 7 === 0
      ? 'Chukwuemeka Nwachukwu-Oluwaseun'
      : ['Ada Okonkwo', 'Bem Tor', 'Ngozi Eze', 'Yusuf Bello', 'Amara Obi'][index % 5],
  category: ['STAFF', 'CONTRACTOR', 'VISITOR', ''][index % 4],
  active: index % 9 !== 0,
  biometric_enrolled: index % 3 === 0,
  created_at: '2026-01-02T09:00:00Z',
  updated_at: '2026-06-02T09:00:00Z',
}))

const OPERATORS = [
  {
    id: 'operator-1',
    email: 'oluwaseun.adebayo@northwind.example',
    full_name: 'Oluwaseun Adebayo',
    role: 'OWNER',
    active: true,
    last_login_at: '2026-08-15T08:00:00Z',
    sites: [],
    all_sites: true,
    created_at: '2026-01-01T00:00:00Z',
  },
  {
    id: 'operator-2',
    email: 'site.viewer@northwind.example',
    full_name: 'Site Viewer',
    role: 'VIEWER',
    active: true,
    sites: [{ site_id: 'site-a', site_name: 'Lagos Distribution Centre' }],
    all_sites: false,
    created_at: '2026-03-01T00:00:00Z',
  },
  {
    id: 'operator-3',
    email: 'never.signed.in@northwind.example',
    full_name: 'Never Signed In',
    role: 'MANAGER',
    active: true,
    sites: [],
    all_sites: true,
    created_at: '2026-08-01T00:00:00Z',
  },
]

const AUDIT = Array.from({ length: 40 }, (_, index) => ({
  id: `audit-${index}`,
  action: [
    'TERMINAL_CREDENTIAL_REVOKED',
    'PERSON_CREATED',
    'SITE_KEY_ROTATED',
    'OPERATOR_ROLE_CHANGED',
    'VISITOR_BADGE_PRINTED',
  ][index % 5],
  actor_email: 'oluwaseun.adebayo@northwind.example',
  actor_role: index % 5 === 2 ? 'PLATFORM' : 'ADMIN',
  target_type: 'TERMINAL',
  target_label: `AT-${String((index % 4) + 1).padStart(4, '0')}`,
  ip_address: '203.0.113.10',
  changes: { reason: 'reported stolen from the east entrance', pending_jobs_cancelled: 4 },
  occurred_at: '2026-08-14T17:05:00Z',
}))

const APPLICATIONS = [
  {
    id: 'app-1',
    code: 'ACCESS_CONTROL',
    enabled: true,
    settings: {},
    created_at: '2026-01-01T00:00:00Z',
    updated_at: '2026-01-01T00:00:00Z',
  },
  {
    id: 'app-2',
    code: 'ATTENDANCE',
    enabled: true,
    settings: { rounding_minutes: 15 },
    created_at: '2026-01-01T00:00:00Z',
    updated_at: '2026-01-01T00:00:00Z',
  },
]

const AVAILABLE = [
  'ACCESS_CONTROL',
  'ATTENDANCE',
  'REGISTRATION',
  'CHECK_IN',
  'VERIFICATION',
  'TIME_TRACKING',
  'VISITOR_MANAGEMENT',
]

/** Routes matched in order; the first hit wins, so specific paths come first. */
const ROUTES = [
  [/\/api\/v1\/auth\/me$/, () => SESSION],
  [/\/api\/v1\/console\/company$/, () => ({
    ...SESSION.company,
    contact_email: 'ops@northwind.example',
    active: true,
    created_at: '2026-01-01T00:00:00Z',
  })],
  [/\/api\/v1\/console\/terminals\/summary$/, () => ({
    total: TERMINALS.length,
    online: 1,
    offline: 1,
    updating: 0,
    error: 1,
    disabled: 1,
    provisioning: 0,
    firmware_outdated: 1,
  })],
  [/\/api\/v1\/console\/terminals\/[^/]+$/, (url) => {
    const serial = url.pathname.split('/').pop()
    const found = TERMINALS.find((entry) => entry.serial_number === serial) ?? TERMINALS[0]
    return { ...found, application_mode: 'MULTI_PURPOSE', effective_applications: ['ACCESS_CONTROL', 'ATTENDANCE'] }
  }],
  [/\/api\/v1\/console\/terminals$/, () => ({ count: TERMINALS.length, terminals: TERMINALS })],
  [/\/api\/v1\/console\/sites\/[^/]+\/settings$/, () => ({ settings: {}, settings_version: 1 })],
  [/\/api\/v1\/console\/sites\/[^/]+$/, () => SITES[0]],
  [/\/api\/v1\/console\/sites$/, () => ({ count: SITES.length, sites: SITES })],
  [/\/api\/v1\/console\/people\/[^/]+$/, () => PEOPLE[0]],
  [/\/api\/v1\/console\/people/, (url) => {
    const limit = Number(url.searchParams.get('limit') ?? 50)
    const offset = Number(url.searchParams.get('offset') ?? 0)
    const search = (url.searchParams.get('q') ?? '').toLowerCase()
    const matched = search
      ? PEOPLE.filter(
          (person) =>
            person.full_name.toLowerCase().includes(search) ||
            person.external_id.toLowerCase().includes(search),
        )
      : PEOPLE
    const page = matched.slice(offset, offset + limit)
    return {
      count: page.length,
      total: matched.length,
      limit,
      offset,
      has_more: offset + page.length < matched.length,
      people: page,
    }
  }],
  [/\/api\/v1\/console\/operators\/[^/]+\/sites$/, () => ({ count: 0, sites: [], all_sites: true })],
  [/\/api\/v1\/console\/operators\/[^/]+$/, (url) => {
    const id = url.pathname.split('/').pop()
    return OPERATORS.find((entry) => entry.id === id) ?? OPERATORS[0]
  }],
  [/\/api\/v1\/console\/operators$/, () => ({ count: OPERATORS.length, operators: OPERATORS })],
  [/\/api\/v1\/console\/audit/, (url) => {
    const limit = Number(url.searchParams.get('limit') ?? 50)
    const offset = Number(url.searchParams.get('offset') ?? 0)
    const page = AUDIT.slice(offset, offset + limit)
    return {
      count: page.length,
      total: AUDIT.length,
      limit,
      offset,
      has_more: offset + page.length < AUDIT.length,
      entries: page,
    }
  }],
  [/\/api\/v1\/console\/applications$/, () => ({
    configured: APPLICATIONS,
    enabled: APPLICATIONS.filter((app) => app.enabled).map((app) => app.code),
    available: AVAILABLE,
  })],
  [/\/api\/v1\/console\/firmware$/, () => ({ count: 0, firmware_versions: [] })],
]

/** Installs the interception on a Playwright page. */
export async function mockApi(page) {
  await page.route('**/api/v1/**', async (route) => {
    const url = new URL(route.request().url())

    for (const [pattern, handler] of ROUTES) {
      if (pattern.test(url.pathname)) {
        return route.fulfill({
          status: 200,
          contentType: 'application/json',
          headers: { 'X-Request-ID': 'browser-pass' },
          body: JSON.stringify(handler(url)),
        })
      }
    }

    // An unmatched call is a bug in this mock, not something to paper over with
    // a 200: a screen quietly rendering an empty state would pass a sweep while
    // showing nothing.
    return route.fulfill({
      status: 501,
      contentType: 'application/json',
      body: JSON.stringify({ error: `browser mock has no route for ${url.pathname}` }),
    })
  })
}

export { SESSION }
