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
  /*
    GRANTS AS WELL AS `all_sites`, SO THE SITE SCOPE IS ACTUALLY MEASURED.

    This was an empty array, which is the ordinary owner shape -- an owner
    reaches every site by role and holds no explicit grants. It is also the
    shape for which the scope control renders NOTHING: its options came to one,
    "All sites", and a selection with one value is not a selection. So the
    control was in the product and no viewport ever drew it.

    Two grants alongside `all_sites` gives it three options and puts it on the
    overview at every viewport, including 360px, where its label, its select and
    its touch target are measured like everything else. Nothing else reads
    `sites`, and the default selection is still ALL_SITES, so every other screen
    in this pass is unchanged.
  */
  sites: [
    { site_id: 'site-a', site_name: 'Lagos Distribution Centre' },
    { site_id: 'site-b', site_name: 'Abuja Depot' },
  ],
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
    offline_policy: 'CACHED_GRACE',
    offline_grace_minutes: 720,
  },
  {
    id: 'site-b',
    name: 'Abuja Depot',
    address: '3 Aminu Kano Crescent, Wuse II',
    timezone: 'Africa/Lagos',
    active: false,
    terminal_count: 1,
    created_at: '2026-02-01T00:00:00Z',
    // A different policy from its neighbour, so the sites table renders both
    // badge tones and the contrast check sees each.
    offline_policy: 'DENY_ALL',
    offline_grace_minutes: 0,
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
  /*
    ONE UNIT ON THE BETA CHANNEL, whose target cannot be installed.

    Without it the BETA group has no hardware on it, so the firmware screen
    correctly says nothing about it -- and "Cannot be installed", the one fault
    only this console can report, was never drawn at any viewport. A terminal
    pointed at a version the platform will never send is exactly the situation
    the notice exists for: nothing is being offered to it and nobody would know.

    NEVER REPORTED A VERSION EITHER, so the third segment of the fleet meter is
    non-zero too. A brand-new unit that has not checked in is counted as
    outdated by the API and must not be counted as "update available" here.
  */
  terminal(5, {
    release_channel: 'BETA',
    firmware_version: '',
    current_firmware_version: '1.3.0-rc2',
    firmware_outdated: true,
    device_name: 'Test Bench',
    status: 'PROVISIONING',
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

/*
  THREE ROWS, SO THE FIRMWARE SCREEN HAS SOMETHING TO SAY.

  This was two, both of them `is_current`, so no group had a newer version and
  the screen's whole reason for existing -- the "Update available" card, with its
  count, its consequences and its primary button -- was never drawn at any
  viewport. A sweep over a page whose main content never renders reports no
  violations and has measured nothing.

  1.4.0 is newer than the STABLE target and fully deliverable, so it produces a
  real update card. The BETA row is deliberately left undeliverable -- no digest,
  no size, no address -- because "Cannot be installed" is the one fault only this
  console can report, and it must be drawn and contrast-checked too.
*/
const FIRMWARE = [
  {
    id: 1,
    public_id: 'firmware-1',
    version: '1.2.0',
    device_type: 'TERMINAL',
    release_channel: 'STABLE',
    checksum_sha256: 'a'.repeat(64),
    download_url: 'https://builds.northwind.example/accesslink/terminal/1.2.0.bin',
    release_notes: 'Sensor timeout raised, and the sync backoff no longer resets on a 429.',
    size_bytes: 1_842_000,
    is_mandatory: false,
    is_current: true,
    created_at: '2026-06-01T00:00:00Z',
    published_at: '2026-06-01T00:00:00Z',
  },
  {
    id: 3,
    public_id: 'firmware-3',
    version: '1.4.0',
    device_type: 'TERMINAL',
    release_channel: 'STABLE',
    checksum_sha256: 'b'.repeat(64),
    download_url: 'https://builds.northwind.example/accesslink/terminal/1.4.0.bin',
    release_notes:
      'Wi-Fi recovery from the button on the unit, and a faster first sync after a restart.',
    size_bytes: 1_901_000,
    is_mandatory: false,
    is_current: false,
    created_at: '2026-08-01T00:00:00Z',
    published_at: '2026-08-01T00:00:00Z',
  },
  {
    id: 2,
    public_id: 'firmware-2',
    version: '1.3.0-rc2',
    device_type: 'TERMINAL',
    release_channel: 'BETA',
    is_mandatory: true,
    is_current: true,
    created_at: '2026-08-10T00:00:00Z',
    published_at: '2026-08-10T00:00:00Z',
  },
]

const SCHEDULES = [
  {
    id: 'schedule-1',
    name: 'Office hours',
    description: 'The shift most staff work, across every site.',
    windows: [
      { days_of_week: 31, start_time: '08:00', end_time: '18:00' },
      { days_of_week: 32, start_time: '09:00', end_time: '13:00' },
    ],
    permission_count: 14,
    active: true,
    created_at: '2026-01-01T00:00:00Z',
    updated_at: '2026-01-01T00:00:00Z',
  },
  {
    id: 'schedule-2',
    name: 'Night shift',
    // Crosses midnight, which is the window shape most likely to be rendered
    // badly.
    windows: [{ days_of_week: 127, start_time: '22:00', end_time: '06:00' }],
    permission_count: 0,
    active: true,
    timezone: 'Africa/Lagos',
    created_at: '2026-01-01T00:00:00Z',
    updated_at: '2026-01-01T00:00:00Z',
  },
]

/**
 * The door log.
 *
 * STAMPED RELATIVE TO THE RUN, not to a fixed date. The overview asks for
 * everything since the READER'S midnight, so a hard-coded August afternoon puts
 * every event outside the window for good and the day's figures render as zero
 * on a screen whose whole job is to show them. Two thirds land today and the
 * rest yesterday, so the boundary is real and the tile is not a zero.
 */
const RUN_AT = new Date()
const HOUR = 60 * 60 * 1000

/** An instant `hours` before the run started. Never in the future. */
function hoursAgo(hours) {
  return new Date(RUN_AT.getTime() - hours * HOUR).toISOString()
}

const EVENTS = Array.from({ length: 40 }, (_, index) => {
  /*
    UNKNOWN IMPLIES DENIED, WHICH IS NOT A STYLISTIC CHOICE.

    These two were independent -- `index % 3` and `index % 11` -- and they
    collided at 0, so the first row of every sweep read "Granted" beside "Nobody
    matched": the terminal admitted somebody it could not identify. That cannot
    happen, and a fixture that renders an impossible row teaches whoever reads
    the screenshot that it can.
  */
  const unknown = index % 11 === 0
  const denied = unknown || index % 3 !== 0
  // Newest first, an hour apart, walking back from the moment the sweep
  // started. Forty of them reach back past midnight whatever time it runs, so
  // the day boundary the overview asks about is always a real one -- and no
  // event is ever stamped in the future, which a fixed hour-of-day schedule
  // could not promise.
  const occurred = hoursAgo(index)
  return {
    id: `event-${index}`,
    event_type: denied ? 'ACCESS_DENIED' : 'ACCESS_GRANTED',
    decision: denied ? 'DENIED' : 'GRANTED',
    reason: unknown
      ? 'PERSON_UNKNOWN'
      : denied
        ? ['NO_PERMISSION', 'OUTSIDE_SCHEDULE', 'EXPLICIT_DENY'][index % 3]
        : 'ALLOWED',
    application: 'ACCESS_CONTROL',
    site_name: index % 4 === 3 ? 'Abuja Depot' : 'Lagos Distribution Centre',
    device_serial: `AT-000${(index % 4) + 1}`,
    device_name: 'North Gate (staff and contractor entrance)',
    person_id: unknown ? undefined : `person-${index}`,
    person_name: unknown ? undefined : 'Chukwuemeka Nwachukwu-Oluwaseun',
    subject_external_id: unknown ? 'UNKNOWN-CARD-4471' : `P-${String(index).padStart(4, '0')}`,
    occurred_at: occurred,
    // Divergent, so the "reported later" line is exercised.
    // A buffered upload: it happened then and was reported two hours later,
    // unless that would be in the future.
    recorded_at: index % 5 === 0 ? hoursAgo(Math.max(0, index - 2)) : occurred,
    occurred_at_trusted: index % 7 !== 0,
  }
})

/*
  THE PLATFORM'S OWN ERROR, which nothing in the fixtures produced.

  `ROSTER_CAPACITY_EXCEEDED` is the only event type carrying `decision: ERROR`,
  it is written by the platform rather than by a door, and it sets NO reason --
  which is exactly why the events page could not explain it. Without a fixture
  the sweep rendered the Error badge nowhere and the gap stayed invisible.

  It names no subject and no person on purpose: nobody was refused. The payload
  mirrors what `database/capacity.go` records.
*/
EVENTS.unshift({
  id: 'event-roster-overflow',
  event_type: 'ROSTER_CAPACITY_EXCEEDED',
  decision: 'ERROR',
  application: 'ACCESS_CONTROL',
  site_name: 'Lagos Distribution Centre',
  device_serial: 'AT-0002',
  device_name: 'North Gate (staff and contractor entrance)',
  payload: { roster_size: 5200, capacity: 5000, shortfall: 200 },
  occurred_at: hoursAgo(1),
  recorded_at: hoursAgo(1),
  occurred_at_trusted: true,
})

/**
 * Terminals showing a pairing code and waiting for somebody to approve them.
 *
 * PRESENT BECAUSE THE OVERVIEW ASKS FOR THEM. Without a route here the sweep
 * would meet the mock's own 501 on every load, which reads as a broken tile
 * rather than as the missing handler it is.
 */
const PENDING = [
  {
    id: 'announcement-1',
    serial_number: 'AT-B7K2M9',
    state: 'ANNOUNCED',
    verdict: 'NEW',
    firmware_version: '1.4.0',
    hardware_revision: 'rev-C',
    capabilities: ['wifi_provisioning', 'wifi_recovery', 'terminal_announce'],
    first_seen_ip: '81.2.0.5',
    last_seen_ip: '81.2.0.5',
    last_seen_at: hoursAgo(2),
    announced_at: hoursAgo(2),
    expires_at: hoursAgo(-1),
  },
  {
    id: 'announcement-2',
    serial_number: 'AT-Q4X8T1',
    state: 'ANNOUNCED',
    verdict: 'NEW',
    firmware_version: '1.4.0',
    capabilities: [],
    first_seen_ip: '81.2.0.6',
    last_seen_ip: '81.2.0.6',
    last_seen_at: hoursAgo(1),
    announced_at: hoursAgo(1),
    expires_at: hoursAgo(-1),
  },
]

/** Routes matched in order; the first hit wins, so specific paths come first. */
const ROUTES = [

  /*
    The setup facts the overview cannot derive for itself.

    ANCHORED AND LISTED EARLY, like every other exact path here. The overview
    reads this to decide whether to raise "grant people access"; an unmatched
    call would be answered with the mock's own 501 and the page would render its
    error state instead of the screen this pass is meant to measure.

    A NON-ZERO FIGURE, deliberately: the item it feeds is one of the widest
    blocks of prose the overview draws, it carries a primary button, and it is
    exactly the kind of thing that overflows a 390px viewport. Sweeping the
    screen with it absent would measure a layout the customer this fix is for
    never sees.
  */
  [/\/api\/v1\/console\/onboarding$/, () => ({ people_without_access: 2 })],

  /*
    The self-service reset request.

    ALWAYS THE SAME 202, whatever address was typed, which is the server's own
    contract and the reason the screen it produces is generic. The sweep below
    submits this form to reach the "how to get back in" panel — the widest block
    of prose on any unauthenticated screen, and the one a locked-out sole owner
    reads on a phone.
  */
  [/\/api\/v1\/auth\/forgot-password$/, () => ({
    status: 'accepted',
    message:
      'If that address belongs to an operator account, a password reset has been issued.',
  })],

  /*
    The two remaining ways somebody arrives with no session.

    NEITHER IS SUBMITTED BY THE SWEEP. The screens are measured at rest and,
    for the redeem page, with a token in the query string -- which is the state
    a real recipient lands in and the one that carries the most on screen. The
    routes exist so that a stray request cannot fall through to the mock's own
    501 and turn a layout measurement into an error-state measurement.
  */
  [/\/api\/v1\/auth\/register$/, () => SESSION],
  [/\/api\/v1\/auth\/redeem$/, () => ({})],
  [/\/api\/v1\/auth\/login$/, () => SESSION],
  [/\/api\/v1\/auth\/password$/, () => ({})],
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
  [/\/api\/v1\/console\/sites\/[^/]+\/settings$/, () => ({
    // Two keys the console deliberately no longer edits, so the browser pass
    // renders the "settings this console no longer edits" notice and its list.
    settings: { unlock_duration_seconds: 5, tamper_alarm: true, offline_grace_minutes: 720 },
    settings_version: 4,
  })],
  // Before the bare `/sites/{id}` pattern, which would otherwise swallow it.
  [/\/api\/v1\/console\/sites\/[^/]+\/claim-codes$/, () => ({
    claim_code: 'H7K2-M9PX',
    code_prefix: 'H7K2',
    serial_number: 'AT-0042',
    site_name: SITES[0].name,
    expires_at: '2026-08-15T13:00:00Z',
    shown_once: true,
    // Non-zero, so the "an earlier code stopped working" warning is rendered and
    // its contrast measured.
    superseded_codes: 1,
  })],
  [/\/api\/v1\/console\/sites\/[^/]+$/, () => SITES[0]],
  // Searched, as the API searches it -- and as the unit mock does. A fixture
  // that ignored `q` would answer every request identically, so the browser
  // sweep would exercise a search box that could not be shown to do anything.
  [/\/api\/v1\/console\/sites$/, (url) => {
    const term = (url.searchParams.get('q') ?? '').trim().toLowerCase()
    const sites = term
      ? SITES.filter(
          (site) =>
            site.name.toLowerCase().includes(term) ||
            (site.address ?? '').toLowerCase().includes(term),
        )
      : SITES
    return { count: sites.length, sites }
  }],
  /*
    A PERSON'S PERMISSIONS, AND WHY THIS ROUTE HAS TO EXIST.

    The person detail page fetches them. Without an entry here the request fell
    through to the unanchored `/console/people` pattern below, which answered
    with the LIST envelope -- a 200 carrying `people` where the page expected
    `permissions`, so it died on `permissions.length` and rendered the error
    boundary. A 501 would have been loud; a wrong-shaped 200 was not.

    It must stay ABOVE both people patterns: these are matched in order.

    EMPTY, deliberately. "This person cannot get in anywhere" is the state worth
    sweeping -- it is the one with a real empty state, an explanation and a
    call to action, and it is what a newly created person actually looks like.
  */
  [/\/api\/v1\/console\/people\/[^/]+\/permissions$/, () => ({ count: 0, permissions: [] })],
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
  /*
    FILTERED HERE, AS THE SERVER FILTERS IT.

    This route honoured `limit` and `offset` and ignored the other five, so the
    sweep could not exercise the activity filters at all -- and, worse, could not
    reach the filtered empty state, because narrowing to something that matches
    nothing still returned the whole trail. A mock that answers every query
    identically makes a filter look like it works no matter what it does.

    The predicates mirror `database.ListAuditEvents`: exact action, exact target
    type, case-insensitive substring on the actor's email, and an inclusive
    instant range.
  */
  [/\/api\/v1\/console\/audit/, (url) => {
    const limit = Number(url.searchParams.get('limit') ?? 50)
    const offset = Number(url.searchParams.get('offset') ?? 0)
    const action = url.searchParams.get('action') ?? ''
    const targetType = url.searchParams.get('target_type') ?? ''
    const actor = (url.searchParams.get('actor') ?? '').toLowerCase()
    const since = url.searchParams.get('since')
    const until = url.searchParams.get('until')

    const matched = AUDIT.filter((entry) => {
      if (action && entry.action !== action) return false
      if (targetType && entry.target_type !== targetType) return false
      if (actor && !(entry.actor_email ?? '').toLowerCase().includes(actor)) return false
      if (since && entry.occurred_at < since) return false
      if (until && entry.occurred_at > until) return false
      return true
    })

    const page = matched.slice(offset, offset + limit)
    return {
      count: page.length,
      total: matched.length,
      limit,
      offset,
      has_more: offset + page.length < matched.length,
      entries: page,
    }
  }],
  [/\/api\/v1\/console\/applications$/, () => ({
    configured: APPLICATIONS,
    enabled: APPLICATIONS.filter((app) => app.enabled).map((app) => app.code),
    available: AVAILABLE,
  })],

  /*
    TURNING A FEATURE ON AND OFF, which this mock could not do at all.

    There was no route for `PUT /console/applications/{code}`, so every toggle in
    a browser sweep failed with this file's own 501 -- which meant the
    destructive path could not be exercised, and the missing confirmation on the
    Features LIST went unseen through every sweep that had ever run.

    The state is mutated so the sweep can assert the OUTCOME rather than just
    that a request was sent: turn a feature off, confirm, and the next read says
    it is off. A handler that echoed success without changing anything would let
    a broken toggle pass.

    A code the platform does not offer is refused, as the server refuses it.
  */
  [/\/api\/v1\/console\/applications\/[^/]+$/, (url, request) => {
    const code = decodeURIComponent(url.pathname.split('/').pop() ?? '')
    if (!AVAILABLE.includes(code)) {
      return { error: `unknown application ${code}` }
    }

    if (request && request.method() === 'PUT') {
      let body = {}
      try {
        body = JSON.parse(request.postData() ?? '{}')
      } catch {
        body = {}
      }

      const existing = APPLICATIONS.find((app) => app.code === code)
      if (existing) {
        if (body.enabled !== undefined) existing.enabled = Boolean(body.enabled)
        if (body.settings !== undefined) existing.settings = body.settings
        existing.updated_at = '2026-08-25T00:00:00Z'
        return existing
      }

      const created = {
        id: `app-${APPLICATIONS.length + 1}`,
        code,
        enabled: body.enabled !== undefined ? Boolean(body.enabled) : true,
        settings: body.settings ?? {},
        created_at: '2026-08-25T00:00:00Z',
        updated_at: '2026-08-25T00:00:00Z',
      }
      APPLICATIONS.push(created)
      return created
    }

    return (
      APPLICATIONS.find((app) => app.code === code) ?? {
        id: 'app-unconfigured', code, enabled: false, settings: {},
        created_at: '2026-01-01T00:00:00Z', updated_at: '2026-01-01T00:00:00Z',
      }
    )
  }],
  [/\/api\/v1\/console\/firmware$/, () => ({ count: FIRMWARE.length, firmware_versions: FIRMWARE })],

  // Access control and the door log. The event set deliberately mixes outcomes,
  // an unrecognised presentation and a buffered upload, because the layout
  // questions a browser answers -- does a long reason wrap, does the denial
  // summary push the table off screen -- need the awkward rows present.
  [/\/api\/v1\/console\/schedules$/, () => ({ count: SCHEDULES.length, schedules: SCHEDULES })],
  [/\/api\/v1\/console\/permissions/, () => ({ count: 0, permissions: [] })],
  [/\/api\/v1\/console\/terminal-announcements$/, () => ({
    count: PENDING.length,
    pending: PENDING,
  })],

  /*
    FILTERED HERE, as the API filters it.

    The overview asks this endpoint four times on load -- once for the day's
    total, once each for granted and denied, and once for the most recent rows --
    and reads `total` off three of those envelopes. A mock that ignored
    `decision` and `from` would answer all four identically, so every band of the
    day's meter would be the same number and the screen would look right while
    measuring nothing.
  */
  [/\/api\/v1\/console\/events/, (url) => {
    const limit = Number(url.searchParams.get('limit') ?? 50)
    const offset = Number(url.searchParams.get('offset') ?? 0)
    const decision = url.searchParams.get('decision') ?? ''
    const from = url.searchParams.get('from')
    const to = url.searchParams.get('to')
    const siteId = url.searchParams.get('site_id') ?? ''
    // An event carries a site NAME and no id, so narrowing by site means
    // resolving the id first -- exactly what the store does with a subquery.
    const siteName = siteId ? (SITES.find((site) => site.id === siteId)?.name ?? ' none') : ''

    const matched = EVENTS.filter((event) => {
      if (decision && event.decision !== decision) return false
      if (siteName && event.site_name !== siteName) return false
      if (from && event.occurred_at < from) return false
      if (to && event.occurred_at >= to) return false
      return true
    })

    const page = matched.slice(offset, offset + limit)
    return {
      count: page.length,
      total: matched.length,
      limit,
      offset,
      has_more: offset + page.length < matched.length,
      events: page,
    }
  }],
]

/**
 * Installs the interception on a Playwright page.
 *
 * `options.session` chooses who this PAGE is signed in as:
 *
 *   omitted   the owner in SESSION, which is what every authenticated sweep
 *             has always used.
 *   null      nobody. /auth/me answers 401, so the guard sends the browser to
 *             the login form and the unauthenticated screens render ALONE --
 *             without the shell that supplies their landmarks, heading level
 *             and type scale on every other screen. That difference is exactly
 *             what axe catches and a sighted read does not.
 *   object    a session with something changed, for a state that is only
 *             reachable through the session itself. `must_change_password` is
 *             the one that matters: the interstitial it produces is rendered
 *             INSIDE RequireAuth, in place of the whole console, and no URL
 *             reaches it.
 *
 * HELD IN A CLOSURE, NOT IN MODULE STATE. The pass keeps several pages open in
 * one viewport -- an authenticated one, an anonymous one, one that must change
 * its password -- and a shared slot would mean whichever was configured last
 * decided who ALL of them were. A page's identity has to belong to the page.
 *
 * WHICH IS WHY /auth/me IS ANSWERED HERE rather than from ROUTES: ROUTES is a
 * module constant whose handlers are shared by every page, so it is the one
 * place a per-page answer cannot live.
 */
export async function mockApi(page, options = {}) {
  const session = 'session' in options ? options.session : SESSION

  await page.route('**/api/v1/**', async (route) => {
    const url = new URL(route.request().url())

    /*
      ANONYMOUS IS A 401, NOT AN EMPTY BODY.

      `fetchSession` treats 401 as "nobody is signed in" and anything else as a
      failure the operator cannot fix -- and RequireAuth renders those two
      differently on purpose: one redirects to the login form, the other says
      the platform cannot be reached. Answering an anonymous visitor with a 200
      and a null body would put the sweep on the error screen and measure that
      while believing it was measuring the login form.
    */
    if (/\/api\/v1\/auth\/me$/.test(url.pathname)) {
      return route.fulfill({
        status: session === null ? 401 : 200,
        contentType: 'application/json',
        headers: { 'X-Request-ID': 'browser-pass' },
        body: JSON.stringify(session ?? { error: 'not authenticated' }),
      })
    }

    /*
      THE REQUEST IS PASSED THROUGH NOW, not just the URL.

      Routes were matched on pathname alone and handlers received only the URL,
      so a mutation and a read on the same path were indistinguishable -- which
      is why there was no way to add a toggle handler without this. Every
      existing handler ignores the second argument and is unaffected.
    */
    for (const [pattern, handler] of ROUTES) {
      if (pattern.test(url.pathname)) {
        return route.fulfill({
          status: 200,
          contentType: 'application/json',
          headers: { 'X-Request-ID': 'browser-pass' },
          body: JSON.stringify(handler(url, route.request())),
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
