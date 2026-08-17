import { HttpResponse, http } from 'msw'
import { setupServer } from 'msw/node'

import type {
  AuditRecord,
  ConfiguredApplication,
  FieldEvent,
  FirmwareVersion,
  Permission,
  Schedule,
  ScheduleWindow,
  OperatorAccount,
  Person,
  Session,
  Site,
  Terminal,
  TerminalDetail,
} from '../api/types'
import type { PlatformCompany, PlatformSession } from '../platform/types'
import {
  makeAuditRecord,
  makeCredentialToken,
  makeEvent,
  makeFirmwareVersion,
  makeOperatorAccount,
  makePermission,
  makePerson,
  makePlatformCompany,
  makePlatformSession,
  makeSchedule,
  makeSession,
  makeSite,
  makeTerminal,
} from './fixtures'

/**
 * A mock AccessLink API.
 *
 * MODELLED ON THE REAL CONTRACTS, not on what the client happens to expect: 401
 * for no session, the same body from login and /me, 204 from logout, `{count,
 * <name>: [...]}` envelopes, server-side search and paging for people. A mock
 * that agrees with the client rather than with the API passes while the real
 * thing fails, which is worse than having no mock at all.
 *
 * Two behaviours are reproduced deliberately because screens depend on them:
 *
 *   - PEOPLE ARE SEARCHED AND PAGED HERE, in the handler, so a test that
 *     filters client-side and calls it search fails against this mock exactly
 *     as it would against the API.
 *   - TERMINALS ARE NARROWED BY SITE GRANTS, so a test cannot accidentally
 *     assert that a scoped operator sees the whole fleet.
 */

interface ServerState {
  session: Session | null
  loginStatus: number
  loginRetryAfter?: number
  sites: Site[]
  terminals: Terminal[]
  people: Person[]
  operators: OperatorAccount[]
  applications: ConfiguredApplication[]
  available: string[]
  settings: Record<string, { settings: Record<string, unknown>; settings_version: number }>
  audit: AuditRecord[]
  firmware: FirmwareVersion[]
  permissions: Permission[]
  schedules: Schedule[]
  events: FieldEvent[]
  /**
   * Addresses that already belong to an account, for the signup conflict. Email
   * is unique GLOBALLY in the schema rather than per company, so a second
   * company cannot be registered against an address already in use.
   */
  registeredEmails: string[]
  /** The platform administrator's session — a DIFFERENT identity from `session`. */
  platformSession: PlatformSession | null
  platformLoginStatus: number
  companies: PlatformCompany[]
  /**
   * What each redemption token is worth. Absent means unknown, which is what an
   * invented token gets — so a test does not have to register a failure to
   * exercise the "that link is not valid" path.
   */
  redeemable: Record<string, 'ok' | 'expired' | 'used'>
  /** Forces the next matching request to fail, for error-path tests. */
  failNext: Record<string, number>
  requests: { method: string; url: string; headers: Headers }[]
}

function initialState(): ServerState {
  return {
    session: null,
    loginStatus: 200,
    loginRetryAfter: undefined,
    sites: [],
    terminals: [],
    people: [],
    operators: [],
    applications: [],
    available: [
      'ACCESS_CONTROL',
      'ATTENDANCE',
      'REGISTRATION',
      'CHECK_IN',
      'VERIFICATION',
      'TIME_TRACKING',
      'VISITOR_MANAGEMENT',
    ],
    settings: {},
    audit: [],
    firmware: [],
    permissions: [],
    schedules: [],
    events: [],
    registeredEmails: [],
    platformSession: null,
    platformLoginStatus: 200,
    companies: [],
    redeemable: {},
    failNext: {},
    requests: [],
  }
}

export const state: ServerState = initialState()

export function resetServerState(session: Session | null = null): void {
  Object.assign(state, initialState())
  state.session = session
  // Held outside `state` because no response carries it, but still cleared
  // between tests or a superseded-code count leaks from one test into the next.
  outstandingClaims.clear()
}

/** Seeds the tenant's data. Call after resetServerState. */
export function seed(data: Partial<Omit<ServerState, 'requests' | 'failNext'>>): void {
  Object.assign(state, data)
}

/** Makes the next request to `key` fail with `status`. */
export function failNext(key: string, status = 500): void {
  state.failNext[key] = status
}

function takeFailure(key: string): number | null {
  const status = state.failNext[key]
  if (status === undefined) return null
  delete state.failNext[key]
  return status
}

function record(request: Request): void {
  state.requests.push({
    method: request.method,
    url: request.url,
    headers: new Headers(request.headers),
  })
}

const REQUEST_ID = 'test-request-id'

function json(body: object, status = 200) {
  return HttpResponse.json(body, { status, headers: { 'X-Request-ID': REQUEST_ID } })
}

function noContent() {
  return new HttpResponse(null, { status: 204, headers: { 'X-Request-ID': REQUEST_ID } })
}

function unauthorized() {
  return json({ error: 'Authentication required' }, 401)
}

/**
 * The session guard AND the CSRF check.
 *
 * The real API refuses an unsafe request without X-CSRF-Token, and reproducing
 * that here is what keeps the client's header handling under test rather than
 * merely written.
 */
function guard(request: Request): Response | null {
  if (!state.session) return unauthorized()
  const method = request.method.toUpperCase()
  if (method !== 'GET' && method !== 'HEAD' && method !== 'OPTIONS') {
    if (!request.headers.get('X-CSRF-Token')) {
      return json({ error: 'CSRF token required' }, 403)
    }
  }
  return null
}

/** Mirrors the grant rule: OWNER/ADMIN and the ungranted are unscoped. */
function reachableSiteIds(): string[] | null {
  const session = state.session
  if (!session || session.all_sites) return null
  return session.sites.map((grant) => grant.site_id)
}

/**
 * The platform surface's own guard.
 *
 * SEPARATE FROM `guard`, and that separation is the thing being modelled: an
 * operator session must not authenticate a platform route and vice versa. Both
 * cookies are Path=/ in the real deployment, so the browser genuinely offers
 * each to the other's routes and only the middleware names keep them apart — a
 * mock that shared one session would hide exactly that.
 */
function guardPlatform(request: Request): Response | null {
  if (!state.platformSession) return unauthorized()
  const method = request.method.toUpperCase()
  if (method !== 'GET' && method !== 'HEAD' && method !== 'OPTIONS') {
    if (!request.headers.get('X-CSRF-Token')) {
      return json({ error: 'CSRF token required' }, 403)
    }
  }
  return null
}

/** Mirrors models.NormalizeSlug. */
function normalizeSlug(raw: string): string {
  return raw
    .toLowerCase()
    .trim()
    .replace(/[^a-z0-9]+/g, '-')
    .replace(/^-+|-+$/g, '')
}

/** The delivery notice every minted token is returned beside. */
const DELIVERY_NOTICE =
  'This link is shown once and is not stored. Send it to the operator over a ' +
  'channel you trust; the platform does not deliver it.'

/**
 * The full chain in front of a terminal route: session, CSRF, role, then the
 * grant on the site the terminal stands at.
 *
 * ALL FOUR, IN THAT ORDER, because that is what the server does and each one
 * answers differently. A terminal in another tenant is a 404 and one at an
 * ungranted site is a 403 — a mock that conflated them would let a console ship
 * that told an operator a terminal did not exist when in fact they were simply
 * not scoped to it.
 */
function guardTerminal(
  request: Request,
  serial: string,
  minimumRole: 'MANAGER' | 'ADMIN',
): Response | null {
  const refused = guard(request)
  if (refused) return refused

  const role = state.session?.role
  const rank = { VIEWER: 0, MANAGER: 1, ADMIN: 2, OWNER: 3 }
  if ((rank[role ?? 'VIEWER'] ?? 0) < rank[minimumRole]) {
    return json({ error: 'Insufficient permissions' }, 403)
  }

  const terminal = state.terminals.find((entry) => entry.serial_number === serial)
  if (!terminal) return json({ error: 'Terminal not found' }, 404)

  const scope = reachableSiteIds()
  if (scope && !scope.includes(terminal.site_public_id)) {
    return json({ error: 'Site access denied' }, 403)
  }
  return null
}

export const handlers = [
  // --- auth ---------------------------------------------------------------

  http.get('*/api/v1/auth/me', ({ request }) => {
    record(request)
    if (!state.session) return unauthorized()
    return json(state.session)
  }),

  http.post('*/api/v1/auth/login', async ({ request }) => {
    record(request)

    if (state.loginStatus === 429) {
      return HttpResponse.json(
        { error: 'Too many failed attempts, this account is temporarily locked' },
        {
          status: 429,
          headers: {
            'X-Request-ID': REQUEST_ID,
            'Retry-After': String(state.loginRetryAfter ?? 60),
          },
        },
      )
    }
    if (state.loginStatus !== 200) {
      return json({ error: 'Invalid email or password' }, state.loginStatus)
    }

    state.session = state.session ?? makeSession()
    return json(state.session)
  }),

  /**
   * Self-service signup. UNAUTHENTICATED, and it ENDS IN A SESSION.
   *
   * Modelled on what the server actually does rather than on what the form
   * sends: the four fields are validated, the slug is DERIVED from the company
   * name, an address already in use is a 409, and the success body is the same
   * session shape login returns. A mock that answered with anything else would
   * let a console ship that sent the new customer back to a login form after
   * they had already been signed in.
   *
   * NOTHING HERE CARRIES A SITE KEY, deliberately. The server creates the
   * company's first site in the same transaction and keeps only the hash of its
   * provisioning credential — so there is no plaintext for this response to
   * contain, and a mock that invented one would let a console ship that
   * displayed it.
   */
  http.post('*/api/v1/auth/register', async ({ request }) => {
    record(request)

    const failure = takeFailure('register')
    if (failure) return json({ error: 'Failed to create the account' }, failure)

    const body = (await request.json()) as {
      full_name?: string
      company_name?: string
      email?: string
      password?: string
    }

    const fullName = (body.full_name ?? '').trim()
    const companyName = (body.company_name ?? '').trim()
    const email = (body.email ?? '').trim().toLowerCase()

    if (!fullName) return json({ error: 'your name is required' }, 400)
    if (!companyName) return json({ error: 'a company name is required' }, 400)
    if (!email.includes('@')) return json({ error: 'email address is not valid' }, 400)
    if ((body.password ?? '').length < 12) {
      return json({ error: 'password must be at least 12 characters' }, 400)
    }
    if (state.registeredEmails.includes(email)) {
      return json({ error: 'That email address is already in use' }, 409)
    }

    state.registeredEmails = [...state.registeredEmails, email]

    const session = makeSession({
      operator: { id: 'operator-new', email, full_name: fullName, role: 'OWNER' },
      company: { id: 'company-new', name: companyName, slug: normalizeSlug(companyName) },
      role: 'OWNER',
      // Unscoped, as an OWNER always is, and with NO capability enabled — which
      // is the state every new company genuinely starts in.
      sites: [],
      all_sites: true,
      applications: [],
    })
    state.session = session
    return json(session, 201)
  }),

  http.post('*/api/v1/auth/logout', ({ request }) => {
    record(request)
    state.session = null
    return noContent()
  }),

  http.post('*/api/v1/auth/password', ({ request }) => {
    record(request)
    const refused = guard(request)
    if (refused) return refused

    // 401 here means the CURRENT password was wrong, not that the session
    // ended -- the real API answers the same way, and the console has to tell
    // those two apart.
    const failure = takeFailure('change-password')
    if (failure) {
      return json(
        {
          error:
            failure === 401
              ? 'Current password is incorrect'
              : 'Too many attempts, please wait',
        },
        failure,
      )
    }
    return noContent()
  }),

  /**
   * Self-service reset. ALWAYS 202 WITH THE SAME BODY, whatever the address.
   *
   * Reproduced exactly, because a console that branched on the response would
   * reintroduce in the browser the enumeration oracle the server refuses to be —
   * and a mock that answered 404 for an unknown address would let that ship.
   */
  http.post('*/api/v1/auth/forgot-password', async ({ request }) => {
    record(request)
    await request.json()
    return json(
      {
        status: 'accepted',
        message:
          'If that address belongs to an operator account, a password reset has ' +
          'been issued. Contact your administrator if you do not receive it.',
      },
      202,
    )
  }),

  /**
   * Redemption. The token IS the authorisation, and the three failures are
   * REPORTED DISTINCTLY — unknown, expired, already used. That is not an
   * enumeration risk: a caller holding a token already holds the secret, and
   * "expired" is the difference between asking for a new link and concluding the
   * platform is broken.
   */
  http.post('*/api/v1/auth/redeem', async ({ request }) => {
    record(request)
    const body = (await request.json()) as { token?: string; new_password?: string }

    if (!body.token) return json({ error: 'A token is required' }, 400)
    if ((body.new_password ?? '').length < 12) {
      return json({ error: 'password must be at least 12 characters' }, 400)
    }

    const verdict = state.redeemable[body.token] ?? 'unknown'
    if (verdict === 'expired') return json({ error: 'that link has expired' }, 410)
    if (verdict === 'used') return json({ error: 'that link has already been used' }, 409)
    if (verdict === 'unknown') return json({ error: 'that link is not valid' }, 404)

    // 204 and NOT a session: redeeming sets a password, it does not sign anybody
    // in. A mock that returned a session would let a console ship that skipped
    // the login the new credential has to be exercised at.
    return noContent()
  }),

  // --- platform administration ---------------------------------------------
  //
  // A SEPARATE IDENTITY WITH A SEPARATE SESSION, reproduced here as a separate
  // piece of state. A mock that let the operator session authenticate these
  // routes would let a console ship that assumed one login served both, and the
  // failure would only appear against the real API.

  http.post('*/api/v1/platform/login', async ({ request }) => {
    record(request)
    await request.json()

    if (state.platformLoginStatus !== 200) {
      return json({ error: 'Invalid email or password' }, state.platformLoginStatus)
    }

    state.platformSession = state.platformSession ?? makePlatformSession()
    return json(state.platformSession)
  }),

  http.get('*/api/v1/platform/me', ({ request }) => {
    record(request)
    if (!state.platformSession) return unauthorized()
    return json(state.platformSession)
  }),

  http.post('*/api/v1/platform/logout', ({ request }) => {
    record(request)
    state.platformSession = null
    return noContent()
  }),

  http.get('*/api/v1/platform/companies', ({ request }) => {
    record(request)
    const refused = guardPlatform(request)
    if (refused) return refused

    const failure = takeFailure('platform-companies')
    if (failure) return json({ error: 'Failed to retrieve companies' }, failure)

    return json({ count: state.companies.length, companies: state.companies })
  }),

  http.post('*/api/v1/platform/companies', async ({ request }) => {
    record(request)
    const refused = guardPlatform(request)
    if (refused) return refused

    const body = (await request.json()) as {
      name?: string
      slug?: string
      contact_email?: string
      timezone?: string
    }
    const name = (body.name ?? '').trim()
    if (!name) return json({ error: 'company name is required' }, 400)

    // Derived from the name when absent, exactly as the server does it — a
    // console that sent its own derivation would have a second implementation
    // of the rule to keep in step.
    const slug = (body.slug ?? '').trim() || normalizeSlug(name)
    if (state.companies.some((company) => company.slug === slug)) {
      return json({ error: 'a company with that slug already exists' }, 409)
    }

    const company = makePlatformCompany({
      id: `company-${state.companies.length + 1}`,
      name,
      slug,
      contact_email: body.contact_email,
      timezone: (body.timezone ?? '').trim() || 'UTC',
      // Created EMPTY and with no operator. The whole point of the console's
      // onboarding checklist is that this state exists and is visible.
      site_count: 0,
      terminal_count: 0,
      person_count: 0,
      operator_count: 0,
    })
    state.companies = [...state.companies, company]
    return json(company, 201)
  }),

  http.get('*/api/v1/platform/companies/:companyId', ({ request, params }) => {
    record(request)
    const refused = guardPlatform(request)
    if (refused) return refused

    const company = state.companies.find((entry) => entry.id === String(params.companyId))
    if (!company) return json({ error: 'Company not found' }, 404)
    return json(company)
  }),

  http.put('*/api/v1/platform/companies/:companyId', async ({ request, params }) => {
    record(request)
    const refused = guardPlatform(request)
    if (refused) return refused

    const companyId = String(params.companyId)
    const existing = state.companies.find((entry) => entry.id === companyId)
    if (!existing) return json({ error: 'Company not found' }, 404)

    const failure = takeFailure('platform-update-company')
    if (failure) return json({ error: 'Failed to update the company' }, failure)

    const body = (await request.json()) as Record<string, unknown>
    const updated: PlatformCompany = {
      ...existing,
      name: typeof body.name === 'string' ? body.name : existing.name,
      contact_email:
        typeof body.contact_email === 'string' ? body.contact_email : existing.contact_email,
      timezone: typeof body.timezone === 'string' && body.timezone ? body.timezone : existing.timezone,
      active: typeof body.active === 'boolean' ? body.active : existing.active,
      event_retention_days:
        typeof body.event_retention_days === 'number'
          ? body.event_retention_days
          : existing.event_retention_days,
      audit_retention_days:
        typeof body.audit_retention_days === 'number'
          ? body.audit_retention_days
          : existing.audit_retention_days,
    }
    state.companies = state.companies.map((entry) => (entry.id === companyId ? updated : entry))
    return json(updated)
  }),

  http.post('*/api/v1/platform/companies/:companyId/operators', async ({ request, params }) => {
    record(request)
    const refused = guardPlatform(request)
    if (refused) return refused

    const companyId = String(params.companyId)
    const company = state.companies.find((entry) => entry.id === companyId)
    if (!company) return json({ error: 'Company not found' }, 404)

    // ONLY INTO A COMPANY THAT HAS NO OPERATOR. The rule that stops this being a
    // standing back door into every customer, reproduced so a console cannot
    // ship offering the action for a running tenant.
    if (company.operator_count > 0) {
      return json(
        {
          error:
            'That company already has an operator. Further accounts are created ' +
            'from its own console.',
        },
        409,
      )
    }

    const body = (await request.json()) as { email?: string; full_name?: string; password?: string }
    if (!body.email) return json({ error: 'email is required' }, 400)

    state.companies = state.companies.map((entry) =>
      entry.id === companyId ? { ...entry, operator_count: 1 } : entry,
    )

    const response: Record<string, unknown> = {
      operator: {
        id: 'operator-owner-1',
        email: body.email,
        full_name: body.full_name ?? '',
        role: 'OWNER',
      },
      must_change_password: true,
    }
    if (!body.password) {
      response.invitation = makeCredentialToken({ purpose: 'INVITE' })
      response.delivery = DELIVERY_NOTICE
    }
    return json(response, 201)
  }),

  // --- company ------------------------------------------------------------

  http.get('*/api/v1/console/company', ({ request }) => {
    record(request)
    if (!state.session) return unauthorized()

    const failure = takeFailure('company')
    if (failure) return json({ error: 'Failed to retrieve company' }, failure)

    return json({
      ...state.session.company,
      active: true,
      created_at: '2026-01-01T00:00:00Z',
    })
  }),

  // --- sites --------------------------------------------------------------

  http.get('*/api/v1/console/sites', ({ request }) => {
    record(request)
    if (!state.session) return unauthorized()

    const failure = takeFailure('sites-list')
    if (failure) return json({ error: 'Failed to retrieve sites' }, failure)

    const scope = reachableSiteIds()
    const sites = scope ? state.sites.filter((site) => scope.includes(site.id)) : state.sites
    return json({ count: sites.length, sites })
  }),

  http.get('*/api/v1/console/sites/:siteId/settings', ({ request, params }) => {
    record(request)
    if (!state.session) return unauthorized()
    const siteId = String(params.siteId)
    const scope = reachableSiteIds()
    if (scope && !scope.includes(siteId)) return json({ error: 'Site access denied' }, 403)
    return json(state.settings[siteId] ?? { settings: {}, settings_version: 1 })
  }),

  http.put('*/api/v1/console/sites/:siteId/settings', async ({ request, params }) => {
    record(request)
    const refused = guard(request)
    if (refused) return refused

    const siteId = String(params.siteId)
    const scope = reachableSiteIds()
    if (scope && !scope.includes(siteId)) return json({ error: 'Site access denied' }, 403)

    const failure = takeFailure('site-settings')
    if (failure) return json({ error: 'Failed to update settings' }, failure)

    // A FULL REPLACEMENT, exactly as the API does it — keys omitted are gone.
    const body = (await request.json()) as Record<string, unknown>

    // RESERVED KEYS ARE REFUSED, as `models.RejectReservedSettingsKeys` does.
    // A copy of the offline policy inside this object would be ignored in
    // favour of the validated column, so the server rejects the write rather
    // than accepting a value that does nothing. Reproduced here because the
    // console has to strip a stale copy before saving, and a mock that accepted
    // it would let a console ship that 400s on every save at a site carrying an
    // old settings object.
    for (const reserved of ['offline_policy', 'offline_grace_minutes']) {
      if (reserved in body) {
        return json(
          {
            error: `settings must not contain a reserved key ("${reserved}" was supplied)`,
            code: 'RESERVED_SETTINGS_KEY',
          },
          400,
        )
      }
    }
    const previous = state.settings[siteId]?.settings_version ?? 1
    state.settings[siteId] = { settings: body, settings_version: previous + 1 }
    return json(state.settings[siteId])
  }),

  http.post('*/api/v1/console/sites', async ({ request }) => {
    record(request)
    const refused = guard(request)
    if (refused) return refused

    // ADMIN, as the server enforces.
    if (state.session?.role !== 'ADMIN' && state.session?.role !== 'OWNER') {
      return json({ error: 'Insufficient permissions' }, 403)
    }

    const failure = takeFailure('create-site')
    if (failure) return json({ error: 'Failed to create site' }, failure)

    const body = (await request.json()) as { name?: string; address?: string; timezone?: string }
    const name = (body.name ?? '').trim()
    if (!name) return json({ error: 'site name is required' }, 400)
    if (name.length > 100) return json({ error: 'site name must be 100 characters or fewer' }, 400)
    if (state.sites.some((site) => site.name === name)) {
      return json({ error: 'a site with that name already exists in this company' }, 409)
    }

    // A key shaped exactly as the server issues one: ats_ + 64 hex.
    const key = `ats_${'ab12cd34'.repeat(8)}`
    const site = makeSite({
      id: `site-${state.sites.length + 1}`,
      name,
      address: body.address,
      timezone: (body.timezone ?? '').trim() || 'UTC',
      terminal_count: 0,
      api_key_prefix: key.slice(0, 12),
    })
    state.sites = [...state.sites, site]

    return json(
      { site, credential: { api_key: key, api_key_prefix: key.slice(0, 12), shown_once: true } },
      201,
    )
  }),

  http.put('*/api/v1/console/sites/:siteId', async ({ request, params }) => {
    record(request)
    const refused = guard(request)
    if (refused) return refused
    if (state.session?.role !== 'ADMIN' && state.session?.role !== 'OWNER') {
      return json({ error: 'Insufficient permissions' }, 403)
    }

    const siteId = String(params.siteId)
    const existing = state.sites.find((site) => site.id === siteId)
    if (!existing) return json({ error: 'Site not found' }, 404)

    const failure = takeFailure('update-site')
    if (failure) return json({ error: 'Failed to update site' }, failure)

    const body = (await request.json()) as {
      name?: string
      address?: string
      timezone?: string
      active?: boolean
      offline_policy?: string
      offline_grace_minutes?: number
    }
    if (body.name && state.sites.some((s) => s.id !== siteId && s.name === body.name)) {
      return json({ error: 'a site with that name already exists in this company' }, 409)
    }

    // THE OFFLINE POLICY IS VALIDATED AND APPLIED FIRST, exactly as the server
    // does it: a caller sending both must not get the rename without the safety
    // control. The closed set and the 30-day bound are the platform's, and
    // reproducing them here is what keeps the console's own bounds honest — a
    // form that allowed 10,080 would pass against a mock that allowed anything.
    if (body.offline_policy !== undefined) {
      if (!['DENY_ALL', 'CACHED_GRACE', 'CACHED_INDEFINITE'].includes(body.offline_policy)) {
        return json(
          { error: 'offline_policy must be DENY_ALL, CACHED_GRACE or CACHED_INDEFINITE' },
          400,
        )
      }
    }
    if (body.offline_grace_minutes !== undefined) {
      if (
        !Number.isInteger(body.offline_grace_minutes) ||
        body.offline_grace_minutes < 0 ||
        body.offline_grace_minutes > 43_200
      ) {
        return json({ error: 'offline_grace_minutes must be between 0 and 43200' }, 400)
      }
    }
    if (body.offline_policy !== undefined || body.offline_grace_minutes !== undefined) {
      // Delivered to terminals, so the site's settings version moves — the
      // server bumps it and fans a SETTINGS job out to every terminal at the
      // site, which is what makes this a different class of change from a
      // rename.
      const current = state.settings[siteId]
      state.settings[siteId] = {
        settings: current?.settings ?? {},
        settings_version: (current?.settings_version ?? 1) + 1,
      }
    }

    const updated: Site = {
      ...existing,
      name: body.name ?? existing.name,
      address: body.address ?? existing.address,
      timezone: body.timezone ?? existing.timezone,
      active: body.active ?? existing.active,
      // Carried on the site itself, and returned by every projection.
      offline_policy: (body.offline_policy as Site['offline_policy']) ?? existing.offline_policy,
      offline_grace_minutes: body.offline_grace_minutes ?? existing.offline_grace_minutes,
    }
    state.sites = state.sites.map((site) => (site.id === siteId ? updated : site))
    return json(updated)
  }),

  /**
   * Claim codes. ADMIN, matching site key rotation.
   *
   * The code comes back ONCE and no read endpoint returns it, so there is
   * deliberately no GET here to pair with this POST — a mock that offered one
   * would let a console ship that fetched a credential it can only ever be
   * handed.
   */
  http.post('*/api/v1/console/sites/:siteId/claim-codes', async ({ request, params }) => {
    record(request)
    const refused = guard(request)
    if (refused) return refused
    if (state.session?.role !== 'ADMIN' && state.session?.role !== 'OWNER') {
      return json({ error: 'Insufficient permissions' }, 403)
    }

    const siteId = String(params.siteId)
    const site = state.sites.find((entry) => entry.id === siteId)
    if (!site) return json({ error: 'Site not found' }, 404)

    const scope = reachableSiteIds()
    if (scope && !scope.includes(siteId)) return json({ error: 'Site access denied' }, 403)

    const failure = takeFailure('claim-code')
    if (failure) return json({ error: 'Failed to issue claim code' }, failure)

    const body = (await request.json()) as {
      serial_number?: string
      expires_in_minutes?: number
    }
    const serial = (body.serial_number ?? '').trim()
    if (!serial) return json({ error: 'serial_number is required' }, 400)
    // The firmware holds a serial in char[16]; the server refuses anything
    // longer rather than letting it fail at a door.
    if (serial.length > 15) {
      return json({ error: 'serial number must be 15 characters or fewer' }, 400)
    }

    // CLAMPED, NOT REFUSED, exactly as the store does it — a caller asking for a
    // week gets a day and no error. Reproduced because the console's own bound
    // exists precisely so the expiry it displays is the expiry in force.
    const requested = body.expires_in_minutes && body.expires_in_minutes > 0
      ? body.expires_in_minutes
      : 120
    const minutes = Math.min(requested, 1440)

    // Issuing supersedes every outstanding code for the same serial, in the same
    // transaction. Two live codes for one serial would make "single use"
    // meaningless, and an installer holding the older printout has just been
    // stranded — which is why the count comes back.
    const key = `${siteId}::${serial}`
    const superseded = outstandingClaims.get(key) ?? 0
    outstandingClaims.set(key, 1)

    // Crockford's alphabet minus the characters misread off a screen, in two
    // groups of four, as generateClaimCode produces them.
    const code = `H7K2-${String(superseded)}M9P`.slice(0, 9)

    return json(
      {
        claim_code: code,
        code_prefix: code.slice(0, 4),
        serial_number: serial,
        site_name: site.name,
        expires_at: new Date(Date.UTC(2026, 7, 16, 12, 0, 0) + minutes * 60_000).toISOString(),
        shown_once: true,
        superseded_codes: superseded,
      },
      201,
    )
  }),

  http.delete('*/api/v1/console/sites/:siteId', ({ request, params }) => {
    record(request)
    const refused = guard(request)
    if (refused) return refused
    if (state.session?.role !== 'ADMIN' && state.session?.role !== 'OWNER') {
      return json({ error: 'Insufficient permissions' }, 403)
    }

    const siteId = String(params.siteId)
    const existing = state.sites.find((site) => site.id === siteId)
    if (!existing) return json({ error: 'Site not found' }, 404)

    const failure = takeFailure('retire-site')
    if (failure) return json({ error: 'Failed to retire site' }, failure)

    // Retirement CASCADES, exactly as the server does it.
    const retired = state.terminals.filter((t) => t.site_public_id === siteId).length
    state.terminals = state.terminals.filter((t) => t.site_public_id !== siteId)
    state.sites = state.sites.filter((site) => site.id !== siteId)

    return json({ retired: true, terminals_retired: retired })
  }),

  http.post('*/api/v1/console/sites/:siteId/api-key', ({ request, params }) => {
    record(request)
    const refused = guard(request)
    if (refused) return refused
    if (state.session?.role !== 'ADMIN' && state.session?.role !== 'OWNER') {
      return json({ error: 'Insufficient permissions' }, 403)
    }

    const siteId = String(params.siteId)
    const existing = state.sites.find((site) => site.id === siteId)
    if (!existing) return json({ error: 'Site not found' }, 404)

    const failure = takeFailure('rotate-key')
    if (failure) return json({ error: 'Failed to rotate the site key' }, failure)

    const key = `ats_${'ff99ee88'.repeat(8)}`
    state.sites = state.sites.map((site) =>
      site.id === siteId ? { ...site, api_key_prefix: key.slice(0, 12) } : site,
    )

    // Terminals with no device credential of their own still depend on the
    // site key; the server reports them and so does this.
    const legacy = state.terminals.filter(
      (t) => t.site_public_id === siteId && t.status === 'PROVISIONING',
    ).length

    return json({
      credential: { api_key: key, api_key_prefix: key.slice(0, 12), shown_once: true },
      legacy_terminals: legacy,
    })
  }),

  http.get('*/api/v1/console/sites/:siteId', ({ request, params }) => {
    record(request)
    if (!state.session) return unauthorized()
    const siteId = String(params.siteId)
    const scope = reachableSiteIds()
    const site = state.sites.find((entry) => entry.id === siteId)
    if (!site) return json({ error: 'Site not found' }, 404)
    if (scope && !scope.includes(siteId)) return json({ error: 'Site access denied' }, 403)
    return json(site)
  }),

  // --- terminals ----------------------------------------------------------

  http.get('*/api/v1/console/terminals/summary', ({ request }) => {
    record(request)
    if (!state.session) return unauthorized()

    const failure = takeFailure('terminals-summary')
    if (failure) return json({ error: 'Failed to retrieve summary' }, failure)

    const scope = reachableSiteIds()
    const visible = scope
      ? state.terminals.filter((terminal) => scope.includes(terminal.site_public_id))
      : state.terminals
    const count = (status: string) => visible.filter((t) => t.status === status).length
    return json({
      total: visible.length,
      online: count('ONLINE'),
      offline: count('OFFLINE'),
      updating: count('UPDATING'),
      error: count('ERROR'),
      disabled: count('DISABLED'),
      provisioning: count('PROVISIONING'),
      firmware_outdated: visible.filter((t) => t.firmware_outdated).length,
    })
  }),

  http.get('*/api/v1/console/terminals/:serial', ({ request, params }) => {
    record(request)
    if (!state.session) return unauthorized()
    const serial = String(params.serial)
    const terminal = state.terminals.find((entry) => entry.serial_number === serial)
    if (!terminal) return json({ error: 'Terminal not found' }, 404)

    const scope = reachableSiteIds()
    if (scope && !scope.includes(terminal.site_public_id)) {
      return json({ error: 'Site access denied' }, 403)
    }
    return json(terminalDetail(terminal))
  }),

  http.put('*/api/v1/console/terminals/:serial/application-mode', async ({ request, params }) => {
    record(request)
    const refused = guard(request)
    if (refused) return refused

    const serial = String(params.serial)
    const terminal = state.terminals.find((entry) => entry.serial_number === serial)
    if (!terminal) return json({ error: 'Terminal not found' }, 404)

    const scope = reachableSiteIds()
    if (scope && !scope.includes(terminal.site_public_id)) {
      return json({ error: 'Site access denied' }, 403)
    }

    const body = (await request.json()) as { application_mode: string }
    const enabled = state.session?.applications.map((application) => application.code) ?? []
    if (body.application_mode !== 'MULTI_PURPOSE' && !enabled.includes(body.application_mode)) {
      return json({ error: 'That application is not enabled for this company' }, 409)
    }

    modes.set(serial, body.application_mode)
    return json(terminalDetail(terminal))
  }),

  // --- terminal lifecycle ---------------------------------------------------
  //
  // The three destructive operations are modelled with the DIFFERENCES the
  // console has to make visible, not as one shared "deactivate":
  //
  //   state    flips status, leaves the credential alone — reversible
  //   revoke   clears the credential, cancels queued work — hardware must
  //            re-register
  //   retire   removes the row entirely — one-way
  //
  // A mock that collapsed them would let a console ship that described them
  // interchangeably, which is precisely the failure the audit recorded.

  http.put('*/api/v1/console/terminals/:serial/state', async ({ request, params }) => {
    record(request)
    const refused = guardTerminal(request, String(params.serial), 'ADMIN')
    if (refused) return refused

    const failure = takeFailure('terminal-state')
    if (failure) return json({ error: 'Failed to update terminal' }, failure)

    const body = (await request.json()) as { disabled?: boolean; reason?: string }
    if (body.disabled === undefined) return json({ error: 'disabled is required' }, 400)

    const serial = String(params.serial)
    const terminal = state.terminals.find((entry) => entry.serial_number === serial) as Terminal
    const updated: Terminal = {
      ...terminal,
      status: body.disabled ? 'DISABLED' : 'OFFLINE',
      active: !body.disabled,
    }
    state.terminals = state.terminals.map((entry) =>
      entry.serial_number === serial ? updated : entry,
    )

    return json({
      terminal: terminalDetail(updated),
      status: updated.status,
      active: updated.active,
      // The credential is DELIBERATELY untouched — that is what makes this
      // reversible without a site visit.
      credential_cleared: false,
    })
  }),

  http.post('*/api/v1/console/terminals/:serial/revoke', async ({ request, params }) => {
    record(request)
    const refused = guardTerminal(request, String(params.serial), 'ADMIN')
    if (refused) return refused

    const failure = takeFailure('terminal-revoke')
    if (failure) return json({ error: 'Failed to revoke the terminal credential' }, failure)

    const serial = String(params.serial)
    const terminal = state.terminals.find((entry) => entry.serial_number === serial) as Terminal
    const updated: Terminal = { ...terminal, status: 'DISABLED', active: false }
    state.terminals = state.terminals.map((entry) =>
      entry.serial_number === serial ? updated : entry,
    )
    revoked.add(serial)

    return json({
      terminal: terminalDetail(updated),
      status: updated.status,
      active: false,
      credential_cleared: true,
      pending_jobs_cancelled: 4,
      // VERBATIM FROM models.TerminalRecoveryInstruction. The console appends
      // this to what the operator sees without paraphrasing it, so a fixture
      // carrying the server's OLD sentence would test a string the API no
      // longer sends. It previously said "re-register with the site
      // provisioning key", which omitted the re-enable step that provisioning
      // refuses to proceed without and named the credential claim codes exist
      // to keep off installers' laptops.
      recovery:
        'Re-enable this terminal, then issue a single-use claim code for its ' +
        'serial and redeem it at the unit. Re-enabling first is required: ' +
        'provisioning refuses a disabled terminal. The site provisioning key ' +
        'is not needed and should not be used.',
    })
  }),

  http.put('*/api/v1/console/terminals/:serial/site', async ({ request, params }) => {
    record(request)
    const refused = guardTerminal(request, String(params.serial), 'ADMIN')
    if (refused) return refused

    const body = (await request.json()) as { site_id?: string }
    if (!body.site_id) return json({ error: 'site_id is required' }, 400)

    // A site in another tenant and one that does not exist are the SAME answer,
    // as the server does it — naming another company's site must not be
    // distinguishable from naming nothing.
    const destination = state.sites.find((site) => site.id === body.site_id)
    if (!destination) return json({ error: 'Site not found' }, 404)

    const serial = String(params.serial)
    const terminal = state.terminals.find((entry) => entry.serial_number === serial) as Terminal
    const updated: Terminal = {
      ...terminal,
      site_public_id: destination.id,
      site_name: destination.name,
    }
    state.terminals = state.terminals.map((entry) =>
      entry.serial_number === serial ? updated : entry,
    )

    return json({
      terminal: terminalDetail(updated),
      moved: true,
      pending_jobs_cancelled: 2,
    })
  }),

  http.post('*/api/v1/console/terminals/:serial/resync', ({ request, params }) => {
    record(request)
    // MANAGER, not ADMIN: forcing a resync queues a snapshot and changes no
    // state an operator has to reason about afterwards.
    const refused = guardTerminal(request, String(params.serial), 'MANAGER')
    if (refused) return refused

    const failure = takeFailure('terminal-resync')
    if (failure) return json({ error: 'Failed to queue a full sync' }, failure)

    return json({
      serial_number: String(params.serial),
      superseded_jobs: 3,
      pending_jobs: 12,
    })
  }),

  http.delete('*/api/v1/console/terminals/:serial', ({ request, params }) => {
    record(request)
    const refused = guardTerminal(request, String(params.serial), 'ADMIN')
    if (refused) return refused

    const failure = takeFailure('terminal-retire')
    if (failure) return json({ error: 'Failed to retire the terminal' }, failure)

    const serial = String(params.serial)
    state.terminals = state.terminals.filter((entry) => entry.serial_number !== serial)

    return json({
      serial_number: serial,
      retired: true,
      credential_cleared: true,
      pending_jobs_cancelled: 7,
    })
  }),

  http.get('*/api/v1/console/terminals', ({ request }) => {
    record(request)
    if (!state.session) return unauthorized()

    const failure = takeFailure('terminals-list')
    if (failure) return json({ error: 'Failed to retrieve terminals' }, failure)

    const url = new URL(request.url)
    const scope = reachableSiteIds()
    let terminals = scope
      ? state.terminals.filter((terminal) => scope.includes(terminal.site_public_id))
      : state.terminals
    if (url.searchParams.get('outdated') === 'true') {
      terminals = terminals.filter((terminal) => terminal.firmware_outdated)
    }
    return json({ count: terminals.length, terminals })
  }),

  // --- people -------------------------------------------------------------

  http.get('*/api/v1/console/people', ({ request }) => {
    record(request)
    if (!state.session) return unauthorized()

    const failure = takeFailure('people')
    if (failure) return json({ error: 'Failed to retrieve people' }, failure)

    const url = new URL(request.url)
    const search = (url.searchParams.get('q') ?? '').trim().toLowerCase()
    const limit = Number(url.searchParams.get('limit') ?? 50)
    const offset = Number(url.searchParams.get('offset') ?? 0)

    // Searched HERE, as the API does it, so a client-side filter cannot pass.
    const matched = search
      ? state.people.filter(
          (person) =>
            person.full_name.toLowerCase().includes(search) ||
            person.external_id.toLowerCase().includes(search),
        )
      : state.people

    const page = matched.slice(offset, offset + limit)
    return json({
      count: page.length,
      total: matched.length,
      limit,
      offset,
      has_more: offset + page.length < matched.length,
      people: page,
    })
  }),

  http.post('*/api/v1/console/people', async ({ request }) => {
    record(request)
    const refused = guard(request)
    if (refused) return refused

    const body = (await request.json()) as { external_id?: string; full_name: string; category?: string }
    if (!body.external_id) return json({ error: 'external_id is required' }, 400)
    if (state.people.some((person) => person.external_id === body.external_id)) {
      return json({ error: 'A person with that external_id already exists' }, 409)
    }

    const person = makePerson({
      external_id: body.external_id,
      full_name: body.full_name,
      category: body.category,
    })
    state.people = [person, ...state.people]
    return json(person, 201)
  }),

  http.get('*/api/v1/console/people/:externalId', ({ request, params }) => {
    record(request)
    if (!state.session) return unauthorized()
    const person = state.people.find((entry) => entry.external_id === String(params.externalId))
    if (!person) return json({ error: 'Person not found' }, 404)
    return json(person)
  }),

  http.put('*/api/v1/console/people/:externalId', async ({ request, params }) => {
    record(request)
    const refused = guard(request)
    if (refused) return refused

    const externalId = String(params.externalId)
    const index = state.people.findIndex((entry) => entry.external_id === externalId)
    if (index === -1) return json({ error: 'Person not found' }, 404)

    const body = (await request.json()) as { full_name: string; category?: string; active?: boolean }
    const existing = state.people[index] as Person
    const updated: Person = {
      ...existing,
      full_name: body.full_name,
      category: body.category ?? existing.category,
      active: body.active ?? existing.active,
      updated_at: new Date().toISOString(),
    }
    state.people = state.people.map((entry, position) => (position === index ? updated : entry))
    return json(updated)
  }),

  http.delete('*/api/v1/console/people/:externalId', ({ request, params }) => {
    record(request)
    const refused = guard(request)
    if (refused) return refused

    const failure = takeFailure('delete-person')
    if (failure) return json({ error: 'Failed to delete person' }, failure)

    state.people = state.people.filter(
      (entry) => entry.external_id !== String(params.externalId),
    )
    return noContent()
  }),

  // --- operators ----------------------------------------------------------

  http.get('*/api/v1/console/operators', ({ request }) => {
    record(request)
    if (!state.session) return unauthorized()

    const failure = takeFailure('operators-list')
    if (failure) return json({ error: 'Failed to retrieve operators' }, failure)

    return json({ count: state.operators.length, operators: state.operators })
  }),

  http.post('*/api/v1/console/operators', async ({ request }) => {
    record(request)
    const refused = guard(request)
    if (refused) return refused

    const body = (await request.json()) as {
      email: string
      full_name: string
      password?: string
      role: OperatorAccount['role']
      site_ids?: string[]
    }
    if (state.operators.some((operator) => operator.email === body.email)) {
      return json({ error: 'That email address is already in use' }, 409)
    }

    const operator = makeOperatorAccount({
      id: `operator-${state.operators.length + 10}`,
      email: body.email,
      full_name: body.full_name,
      role: body.role,
      // An invited account has never signed in. That is what makes it eligible
      // for a re-invitation rather than a reset, and the console branches on it.
      last_login_at: undefined,
      all_sites: !body.site_ids || body.site_ids.length === 0,
      sites: (body.site_ids ?? []).map((id) => ({
        site_id: id,
        site_name: state.sites.find((site) => site.id === id)?.name ?? id,
      })),
    })
    state.operators = [...state.operators, operator]

    // AN ABSENT PASSWORD MEANS AN INVITATION, exactly as the server does it, and
    // the response is a DIFFERENT SHAPE rather than the same one with extras.
    // Reproducing that here is what keeps the client's narrowing under test.
    if (!body.password) {
      return json(
        {
          operator,
          invitation: makeCredentialToken({ purpose: 'INVITE' }),
          delivery: DELIVERY_NOTICE,
        },
        201,
      )
    }
    return json(operator, 201)
  }),

  http.post('*/api/v1/console/operators/:operatorId/invite', ({ request, params }) => {
    record(request)
    const refused = guard(request)
    if (refused) return refused

    const operator = state.operators.find((entry) => entry.id === String(params.operatorId))
    if (!operator) return json({ error: 'Operator not found' }, 404)

    const failure = takeFailure('invite-operator')
    if (failure) return json({ error: 'Failed to issue the invitation' }, failure)

    // 409 for an account that has signed in, as the server does: re-inviting one
    // would be a reset wearing an invitation's name, and the two are audited
    // differently.
    if (operator.last_login_at) {
      return json(
        {
          error:
            'That operator has already signed in. Use the password reset route ' +
            'instead of an invitation.',
        },
        409,
      )
    }

    return json(
      { invitation: makeCredentialToken({ purpose: 'INVITE' }), delivery: DELIVERY_NOTICE },
      201,
    )
  }),

  http.post('*/api/v1/console/operators/:operatorId/reset', ({ request, params }) => {
    record(request)
    const refused = guard(request)
    if (refused) return refused

    const operator = state.operators.find((entry) => entry.id === String(params.operatorId))
    if (!operator) return json({ error: 'Operator not found' }, 404)

    const failure = takeFailure('reset-operator')
    if (failure) return json({ error: 'Failed to issue the reset' }, failure)

    return json(
      {
        reset: makeCredentialToken({ purpose: 'RESET', token: 'f9e8d7c6'.repeat(8) }),
        delivery: DELIVERY_NOTICE,
      },
      201,
    )
  }),

  http.get('*/api/v1/console/operators/:operatorId/sites', ({ request, params }) => {
    record(request)
    if (!state.session) return unauthorized()
    const operator = state.operators.find((entry) => entry.id === String(params.operatorId))
    if (!operator) return json({ error: 'Operator not found' }, 404)
    return json({
      count: operator.sites?.length ?? 0,
      sites: operator.sites ?? [],
      all_sites: operator.all_sites,
    })
  }),

  http.put('*/api/v1/console/operators/:operatorId/sites', async ({ request, params }) => {
    record(request)
    const refused = guard(request)
    if (refused) return refused

    const operatorId = String(params.operatorId)
    const operator = state.operators.find((entry) => entry.id === operatorId)
    if (!operator) return json({ error: 'Operator not found' }, 404)

    const body = (await request.json()) as { site_ids: string[] }
    const grants = body.site_ids.map((id) => ({
      site_id: id,
      site_name: state.sites.find((site) => site.id === id)?.name ?? id,
    }))
    // An EMPTY list means unscoped — every site — not none.
    const allSites = grants.length === 0 || operator.role === 'OWNER' || operator.role === 'ADMIN'
    const updated = { ...operator, sites: grants, all_sites: allSites }
    state.operators = state.operators.map((entry) => (entry.id === operatorId ? updated : entry))
    return json({ count: grants.length, sites: grants, all_sites: allSites })
  }),

  http.get('*/api/v1/console/operators/:operatorId', ({ request, params }) => {
    record(request)
    if (!state.session) return unauthorized()
    const operator = state.operators.find((entry) => entry.id === String(params.operatorId))
    if (!operator) return json({ error: 'Operator not found' }, 404)
    return json(operator)
  }),

  http.put('*/api/v1/console/operators/:operatorId', async ({ request, params }) => {
    record(request)
    const refused = guard(request)
    if (refused) return refused

    const operatorId = String(params.operatorId)
    const operator = state.operators.find((entry) => entry.id === operatorId)
    if (!operator) return json({ error: 'Operator not found' }, 404)

    const failure = takeFailure('update-operator')
    if (failure) return json({ error: 'Failed to update operator' }, failure)

    const body = (await request.json()) as {
      role?: OperatorAccount['role']
      active?: boolean
      password?: string
    }

    // Mirrors the server: nobody may change their own role or disable
    // themselves, and only an OWNER may touch an OWNER.
    if (state.session?.operator.id === operatorId && (body.role || body.active !== undefined)) {
      return json(
        { error: 'You cannot change your own role or disable your own account' },
        403,
      )
    }
    if (
      (operator.role === 'OWNER' || body.role === 'OWNER') &&
      state.session?.role !== 'OWNER'
    ) {
      return json({ error: 'You may not modify that operator' }, 403)
    }

    const updated = {
      ...operator,
      role: body.role ?? operator.role,
      active: body.active ?? operator.active,
    }
    state.operators = state.operators.map((entry) => (entry.id === operatorId ? updated : entry))
    return json(updated)
  }),

  http.delete('*/api/v1/console/operators/:operatorId', ({ request, params }) => {
    record(request)
    const refused = guard(request)
    if (refused) return refused

    const operatorId = String(params.operatorId)
    if (state.session?.operator.id === operatorId) {
      return json({ error: 'You cannot remove your own account' }, 403)
    }
    state.operators = state.operators.filter((entry) => entry.id !== operatorId)
    return noContent()
  }),

  // --- access control ------------------------------------------------------
  //
  // The engine's rule, reproduced so a console cannot ship assuming otherwise:
  // ABSENCE OF PERMISSION IS NOT PERMISSION. A person with no rules is returned
  // an empty list, and the console has to describe that as reaching nothing.

  http.get('*/api/v1/console/people/:externalId/permissions', ({ request, params }) => {
    record(request)
    if (!state.session) return unauthorized()

    const failure = takeFailure('permissions')
    if (failure) return json({ error: 'Failed to retrieve permissions' }, failure)

    const externalId = String(params.externalId)
    const permissions = state.permissions.filter((entry) => entry.person_id === externalId)
    return json({ count: permissions.length, permissions })
  }),

  http.post('*/api/v1/console/people/:externalId/permissions', async ({ request, params }) => {
    record(request)
    const refused = guard(request)
    if (refused) return refused
    if (state.session?.role === 'VIEWER') {
      return json({ error: 'Insufficient permissions' }, 403)
    }

    const body = (await request.json()) as {
      scope_type?: string
      site_id?: string
      device_serial?: string
      effect?: string
      schedule_id?: string
      application?: string
      starts_at?: string
      ends_at?: string
    }

    // The server validates a rule's internal consistency before it reaches the
    // database, so a caller gets a message naming what is wrong rather than a
    // constraint violation. Reproduced because the console relies on it.
    if (!body.scope_type || !['COMPANY', 'SITE', 'TERMINAL'].includes(body.scope_type)) {
      return json({ error: 'scope must be COMPANY, SITE or TERMINAL' }, 400)
    }
    if (body.scope_type === 'SITE' && !body.site_id) {
      return json({ error: 'a SITE or TERMINAL scope must name one' }, 400)
    }
    if (body.scope_type === 'TERMINAL' && !body.device_serial) {
      return json({ error: 'a SITE or TERMINAL scope must name one' }, 400)
    }
    if (body.scope_type === 'COMPANY' && (body.site_id || body.device_serial)) {
      return json({ error: 'a COMPANY scope must not name a site or terminal' }, 400)
    }

    const site = state.sites.find((entry) => entry.id === body.site_id)
    const terminal = state.terminals.find((entry) => entry.serial_number === body.device_serial)
    const schedule = state.schedules.find((entry) => entry.id === body.schedule_id)

    const permission: Permission = {
      id: `permission-${state.permissions.length + 1}`,
      person_id: String(params.externalId),
      scope_type: body.scope_type as Permission['scope_type'],
      site_id: body.site_id,
      site_name: site?.name,
      device_serial: body.device_serial,
      device_name: terminal?.device_name,
      application: body.application,
      effect: (body.effect ?? 'ALLOW') as Permission['effect'],
      schedule_id: body.schedule_id,
      schedule_name: schedule?.name,
      starts_at: body.starts_at,
      ends_at: body.ends_at,
      active: true,
      created_at: '2026-08-15T00:00:00Z',
      updated_at: '2026-08-15T00:00:00Z',
    }
    state.permissions = [...state.permissions, permission]

    // A schedule's permission_count is what the console shows before an edit.
    if (schedule) {
      state.schedules = state.schedules.map((entry) =>
        entry.id === schedule.id
          ? { ...entry, permission_count: entry.permission_count + 1 }
          : entry,
      )
    }

    return json(permission, 201)
  }),

  http.delete('*/api/v1/console/permissions/:permissionId', ({ request, params }) => {
    record(request)
    const refused = guard(request)
    if (refused) return refused

    const id = String(params.permissionId)
    const existing = state.permissions.find((entry) => entry.id === id)
    if (!existing) return json({ error: 'Permission not found' }, 404)

    state.permissions = state.permissions.filter((entry) => entry.id !== id)
    if (existing.schedule_id) {
      state.schedules = state.schedules.map((entry) =>
        entry.id === existing.schedule_id
          ? { ...entry, permission_count: Math.max(0, entry.permission_count - 1) }
          : entry,
      )
    }
    return noContent()
  }),

  // --- schedules -----------------------------------------------------------

  http.get('*/api/v1/console/schedules', ({ request }) => {
    record(request)
    if (!state.session) return unauthorized()

    const failure = takeFailure('schedules')
    if (failure) return json({ error: 'Failed to retrieve schedules' }, failure)

    return json({ count: state.schedules.length, schedules: state.schedules })
  }),

  http.post('*/api/v1/console/schedules', async ({ request }) => {
    record(request)
    const refused = guard(request)
    if (refused) return refused

    const body = (await request.json()) as { name?: string; windows?: ScheduleWindow[] }
    if (!body.name) return json({ error: 'a name is required' }, 400)
    if (!body.windows || body.windows.length === 0) {
      return json({ error: 'a schedule must have at least one window' }, 400)
    }
    if (state.schedules.some((entry) => entry.name === body.name)) {
      return json({ error: 'a schedule with that name already exists' }, 409)
    }

    const schedule = makeSchedule({
      id: `schedule-${state.schedules.length + 1}`,
      name: body.name,
      windows: body.windows,
      permission_count: 0,
    })
    state.schedules = [...state.schedules, schedule]
    return json(schedule, 201)
  }),

  http.put('*/api/v1/console/schedules/:scheduleId', async ({ request, params }) => {
    record(request)
    const refused = guard(request)
    if (refused) return refused

    const id = String(params.scheduleId)
    const existing = state.schedules.find((entry) => entry.id === id)
    if (!existing) return json({ error: 'Schedule not found' }, 404)

    const body = (await request.json()) as Partial<Schedule>
    const updated: Schedule = {
      ...existing,
      name: body.name ?? existing.name,
      description: body.description ?? existing.description,
      timezone: body.timezone ?? existing.timezone,
      windows: body.windows ?? existing.windows,
    }
    state.schedules = state.schedules.map((entry) => (entry.id === id ? updated : entry))
    return json(updated)
  }),

  http.delete('*/api/v1/console/schedules/:scheduleId', ({ request, params }) => {
    record(request)
    const refused = guard(request)
    if (refused) return refused

    const id = String(params.scheduleId)
    const existing = state.schedules.find((entry) => entry.id === id)
    if (!existing) return json({ error: 'Schedule not found' }, 404)

    // REFUSED WHILE RULES DEPEND ON IT. The foreign key is ON DELETE SET NULL,
    // which would silently WIDEN every rule that used it — a permission
    // restricted to office hours would become one with no time restriction at
    // all. 409 rather than 400: the request is well formed and would be valid
    // once those rules change.
    if (existing.permission_count > 0) {
      return json({ error: 'schedule is still used by permissions' }, 409)
    }

    state.schedules = state.schedules.filter((entry) => entry.id !== id)
    return json({ deleted: true, schedule_id: id })
  }),

  // --- the decision preview ------------------------------------------------

  http.post('*/api/v1/console/terminals/:serial/evaluate', async ({ request, params }) => {
    record(request)
    const refused = guardTerminal(request, String(params.serial), 'MANAGER')
    if (refused) return refused

    const body = (await request.json()) as { external_id?: string }
    if (!body.external_id) return json({ error: 'external_id is required' }, 400)

    const serial = String(params.serial)
    const terminal = state.terminals.find((entry) => entry.serial_number === serial) as Terminal
    const person = state.people.find((entry) => entry.external_id === body.external_id)

    // A tiny evaluator, deliberately reproducing the ENGINE'S ORDER rather than
    // a convenient one: deny beats allow at every scope, and no matching rule
    // means refused. A mock that granted by default would let a console ship
    // describing the opposite of what the platform does.
    if (!person) {
      return json({
        granted: false,
        reason: 'PERSON_UNKNOWN',
        external_id: body.external_id,
        decided_at: '2026-08-15T12:00:00Z',
      })
    }

    const rules = state.permissions.filter((entry) => entry.person_id === person.external_id)
    const applies = (rule: Permission) =>
      rule.active &&
      (rule.scope_type === 'COMPANY' ||
        (rule.scope_type === 'SITE' && rule.site_id === terminal.site_public_id) ||
        (rule.scope_type === 'TERMINAL' && rule.device_serial === serial))

    const deny = rules.find((rule) => rule.effect === 'DENY' && applies(rule))
    const allow = rules.find((rule) => rule.effect === 'ALLOW' && applies(rule))

    const decision =
      deny !== undefined
        ? { granted: false, reason: 'EXPLICIT_DENY', matched_permission: deny.id }
        : allow !== undefined
          ? { granted: true, reason: 'ALLOWED', matched_permission: allow.id }
          : { granted: false, reason: 'NO_PERMISSION' }

    return json({
      ...decision,
      person_id: person.id,
      person_name: person.full_name,
      external_id: person.external_id,
      decided_at: '2026-08-15T12:00:00Z',
    })
  }),

  // --- field events --------------------------------------------------------

  http.get('*/api/v1/console/events', ({ request }) => {
    record(request)
    if (!state.session) return unauthorized()

    const failure = takeFailure('events')
    if (failure) return json({ error: 'Failed to retrieve events' }, failure)

    const url = new URL(request.url)
    const decision = url.searchParams.get('decision') ?? ''
    const eventType = url.searchParams.get('event_type') ?? ''
    const serial = url.searchParams.get('serial') ?? ''
    const search = (url.searchParams.get('q') ?? '').toLowerCase()
    const from = url.searchParams.get('from')
    const to = url.searchParams.get('to')
    const limit = Number(url.searchParams.get('limit') ?? 50)
    const offset = Number(url.searchParams.get('offset') ?? 0)

    // FILTERED HERE, as the API does it, so a console that narrowed a fetched
    // page in the browser would fail against this mock exactly as it would
    // against the server.
    const matched = state.events.filter((event) => {
      if (decision && event.decision !== decision) return false
      if (eventType && event.event_type !== eventType) return false
      if (serial && event.device_serial !== serial) return false
      if (from && event.occurred_at < from) return false
      if (to && event.occurred_at > to) return false
      if (search) {
        const haystack = `${event.person_name ?? ''} ${event.subject_external_id ?? ''}`.toLowerCase()
        if (!haystack.includes(search)) return false
      }
      return true
    })

    const page = matched.slice(offset, offset + limit)
    return json({
      count: page.length,
      total: matched.length,
      limit,
      offset,
      has_more: offset + page.length < matched.length,
      events: page,
    })
  }),

  // --- audit ---------------------------------------------------------------

  /**
   * The audit trail. ADMIN, and FILTERED HERE rather than in the client.
   *
   * Same reasoning as people: a console that fetched a page and filtered it in
   * the browser would be filtering the page rather than the trail, which is
   * silently wrong the moment a company has more records than fit on one — and
   * an audit trail is the surface where "silently wrong" matters most.
   */
  http.get('*/api/v1/console/audit', ({ request }) => {
    record(request)
    if (!state.session) return unauthorized()
    if (state.session.role !== 'ADMIN' && state.session.role !== 'OWNER') {
      return json({ error: 'Insufficient permissions' }, 403)
    }

    const failure = takeFailure('audit')
    if (failure) return json({ error: 'Failed to retrieve the audit trail' }, failure)

    const url = new URL(request.url)
    const action = url.searchParams.get('action') ?? ''
    const targetType = url.searchParams.get('target_type') ?? ''
    const actor = (url.searchParams.get('actor') ?? '').toLowerCase()
    const since = url.searchParams.get('since')
    const until = url.searchParams.get('until')
    const limit = Number(url.searchParams.get('limit') ?? 50)
    const offset = Number(url.searchParams.get('offset') ?? 0)

    const matched = state.audit.filter((entry) => {
      if (action && entry.action !== action) return false
      if (targetType && entry.target_type !== targetType) return false
      if (actor && !(entry.actor_email ?? '').toLowerCase().includes(actor)) return false
      if (since && entry.occurred_at < since) return false
      if (until && entry.occurred_at > until) return false
      return true
    })

    const page = matched.slice(offset, offset + limit)
    return json({
      count: page.length,
      total: matched.length,
      limit,
      offset,
      has_more: offset + page.length < matched.length,
      entries: page,
    })
  }),

  // --- firmware ------------------------------------------------------------

  http.get('*/api/v1/console/firmware', ({ request }) => {
    record(request)
    if (!state.session) return unauthorized()
    if (state.session.role !== 'ADMIN' && state.session.role !== 'OWNER') {
      return json({ error: 'Insufficient permissions' }, 403)
    }

    const failure = takeFailure('firmware')
    if (failure) return json({ error: 'Failed to retrieve firmware versions' }, failure)

    return json({ count: state.firmware.length, firmware_versions: state.firmware })
  }),

  http.post('*/api/v1/console/firmware', async ({ request }) => {
    record(request)
    const refused = guard(request)
    if (refused) return refused
    if (state.session?.role !== 'ADMIN' && state.session?.role !== 'OWNER') {
      return json({ error: 'Insufficient permissions' }, 403)
    }

    const body = (await request.json()) as { version?: string; device_type?: string; release_channel?: string }
    if (!body.version) return json({ error: 'version is required' }, 400)

    const deviceType = body.device_type || 'TERMINAL'
    const channel = body.release_channel || 'STABLE'
    if (
      state.firmware.some(
        (entry) => entry.version === body.version && entry.device_type === deviceType,
      )
    ) {
      return json({ error: 'That version already exists for this device type' }, 409)
    }

    // PUBLISHED IS NOT CURRENT. Adding a build and deciding a fleet should run
    // it are different decisions, and the server keeps them as two calls.
    const created = makeFirmwareVersion({
      id: state.firmware.length + 1,
      public_id: `firmware-${state.firmware.length + 1}`,
      version: body.version,
      device_type: deviceType,
      release_channel: channel,
      is_current: false,
    })
    state.firmware = [...state.firmware, created]
    return json(created, 201)
  }),

  http.put('*/api/v1/console/firmware/:id/current', ({ request, params }) => {
    record(request)
    const refused = guard(request)
    if (refused) return refused
    if (state.session?.role !== 'ADMIN' && state.session?.role !== 'OWNER') {
      return json({ error: 'Insufficient permissions' }, 403)
    }

    const id = Number(params.id)
    const target = state.firmware.find((entry) => entry.id === id)
    if (!target) return json({ error: 'Firmware version not found' }, 404)

    // Current is per device type AND channel, so promoting one demotes only its
    // own peers rather than the whole catalogue.
    state.firmware = state.firmware.map((entry) =>
      entry.device_type === target.device_type && entry.release_channel === target.release_channel
        ? { ...entry, is_current: entry.id === id }
        : entry,
    )
    return json({ ...target, is_current: true })
  }),

  // --- applications -------------------------------------------------------

  http.get('*/api/v1/console/applications', ({ request }) => {
    record(request)
    if (!state.session) return unauthorized()

    const failure = takeFailure('applications')
    if (failure) return json({ error: 'Failed to retrieve applications' }, failure)

    return json({
      configured: state.applications,
      enabled: state.applications.filter((app) => app.enabled).map((app) => app.code),
      available: state.available,
    })
  }),

  http.put('*/api/v1/console/applications/:code', async ({ request, params }) => {
    record(request)
    const refused = guard(request)
    if (refused) return refused

    // OWNER only, as the server enforces.
    if (state.session?.role !== 'OWNER') {
      return json({ error: 'Insufficient permissions' }, 403)
    }

    const code = String(params.code)
    if (!state.available.includes(code)) {
      return json({ error: 'Unknown application', available: state.available }, 400)
    }

    const failure = takeFailure('set-application')
    if (failure) return json({ error: 'Failed to update application' }, failure)

    const body = (await request.json()) as { enabled?: boolean; settings?: Record<string, unknown> }
    const existing = state.applications.find((app) => app.code === code)
    const updated: ConfiguredApplication = {
      id: existing?.id ?? `app-${code}`,
      code,
      enabled: body.enabled ?? true,
      settings: body.settings ?? existing?.settings ?? {},
      created_at: existing?.created_at ?? '2026-01-01T00:00:00Z',
      updated_at: new Date().toISOString(),
    }
    state.applications = existing
      ? state.applications.map((app) => (app.code === code ? updated : app))
      : [...state.applications, updated]

    // Navigation is derived from the SESSION's applications, so the session
    // has to move too — reproducing that here is what keeps the client's
    // invalidation under test.
    if (state.session) {
      state.session = {
        ...state.session,
        applications: state.applications
          .filter((app) => app.enabled)
          .map((app) => ({ code: app.code, settings: app.settings })),
      }
    }
    return json(updated)
  }),
]

/** Terminal application modes, kept apart so a fixture stays a plain object. */
const modes = new Map<string, string>()

/** Serials whose device credential has been revoked. */
const revoked = new Set<string>()

/** Outstanding claim codes per site and serial, for the superseding count. */
const outstandingClaims = new Map<string, number>()

/**
 * What the platform holds for a site's outage behaviour.
 *
 * Read back off the site itself, because that is where the API keeps it: the
 * columns are on every site projection, so a test asserting on the WRITE and a
 * screen reading it back are looking at the same value. A separate store here
 * would let the two drift and hide exactly the bug it was meant to catch.
 */
export function offlinePolicyFor(
  siteId: string,
): { policy: string; grace: number } | undefined {
  const site = state.sites.find((entry) => entry.id === siteId)
  if (!site) return undefined
  return { policy: site.offline_policy, grace: site.offline_grace_minutes }
}

function terminalDetail(terminal: Terminal): TerminalDetail {
  const mode = modes.get(terminal.serial_number) ?? 'MULTI_PURPOSE'
  const enabled = state.session?.applications.map((application) => application.code) ?? []
  return {
    ...terminal,
    application_mode: mode,
    effective_applications: enabled.filter((code) => mode === 'MULTI_PURPOSE' || code === mode),
  }
}

export function resetTerminalModes(): void {
  modes.clear()
  revoked.clear()
  outstandingClaims.clear()
}

export const server = setupServer(...handlers)

export {
  makeAuditRecord,
  makeCredentialToken,
  makeEvent,
  makeFirmwareVersion,
  makeOperatorAccount,
  makePermission,
  makePerson,
  makePlatformCompany,
  makePlatformSession,
  makeSchedule,
  makeSite,
  makeTerminal,
}
