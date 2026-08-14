import { HttpResponse, http } from 'msw'
import { setupServer } from 'msw/node'

import type {
  ConfiguredApplication,
  OperatorAccount,
  Person,
  Session,
  Site,
  Terminal,
  TerminalDetail,
} from '../api/types'
import { makeOperatorAccount, makePerson, makeSession, makeSite, makeTerminal } from './fixtures'

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
    failNext: {},
    requests: [],
  }
}

export const state: ServerState = initialState()

export function resetServerState(session: Session | null = null): void {
  Object.assign(state, initialState())
  state.session = session
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

  http.post('*/api/v1/auth/logout', ({ request }) => {
    record(request)
    state.session = null
    return noContent()
  }),

  http.post('*/api/v1/auth/password', ({ request }) => {
    record(request)
    const refused = guard(request)
    if (refused) return refused
    return noContent()
  }),

  // --- company ------------------------------------------------------------

  http.get('*/api/v1/console/company', ({ request }) => {
    record(request)
    if (!state.session) return unauthorized()
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
    }
    if (body.name && state.sites.some((s) => s.id !== siteId && s.name === body.name)) {
      return json({ error: 'a site with that name already exists in this company' }, 409)
    }

    const updated = {
      ...existing,
      name: body.name ?? existing.name,
      address: body.address ?? existing.address,
      timezone: body.timezone ?? existing.timezone,
      active: body.active ?? existing.active,
    }
    state.sites = state.sites.map((site) => (site.id === siteId ? updated : site))
    return json(updated)
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
      role: OperatorAccount['role']
      site_ids?: string[]
    }
    if (state.operators.some((operator) => operator.email === body.email)) {
      return json({ error: 'That email address is already in use' }, 409)
    }

    const operator = makeOperatorAccount({
      email: body.email,
      full_name: body.full_name,
      role: body.role,
      all_sites: !body.site_ids || body.site_ids.length === 0,
      sites: (body.site_ids ?? []).map((id) => ({
        site_id: id,
        site_name: state.sites.find((site) => site.id === id)?.name ?? id,
      })),
    })
    state.operators = [...state.operators, operator]
    return json(operator, 201)
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

  // --- applications -------------------------------------------------------

  http.get('*/api/v1/console/applications', ({ request }) => {
    record(request)
    if (!state.session) return unauthorized()
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
}

export const server = setupServer(...handlers)

export { makeOperatorAccount, makePerson, makeSite, makeTerminal }
