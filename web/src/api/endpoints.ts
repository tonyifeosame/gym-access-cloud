import { ApiError, api } from './client'
import { setCsrfToken } from './csrf'
import type {
  ApplicationCode,
  ApplicationRequest,
  ApplicationsResponse,
  CompanyDetail,
  ConfiguredApplication,
  CreateOperatorRequest,
  FleetSummary,
  OperatorAccount,
  OperatorSitesResponse,
  OperatorsResponse,
  PeoplePage,
  PeopleQuery,
  Person,
  PersonRequest,
  Session,
  Site,
  SiteGrantsRequest,
  SiteSettings,
  SiteSettingsRequest,
  SitesResponse,
  TerminalDetail,
  TerminalModeRequest,
  TerminalsResponse,
  UpdateOperatorRequest,
} from './types'

/**
 * One function per API contract. Nothing else in the app builds a URL.
 *
 * Every path here is under /api/v1/auth or /api/v1/console -- the two trees that
 * authenticate with an operator session. The site-key and device trees are not
 * reachable from a browser and are deliberately absent.
 */

/** Captures the CSRF token that arrives with every session body. */
function adoptSession(session: Session): Session {
  setCsrfToken(session.csrf_token)
  return session
}

export async function login(email: string, password: string): Promise<Session> {
  const session = await api.post<Session>('/api/v1/auth/login', { email, password })
  return adoptSession(session)
}

/**
 * Restores a session at boot, or reports that there is none.
 *
 * Returns null rather than throwing on 401: "nobody is signed in" is the normal
 * state of a first visit, not a failure. Any other error is a real one and
 * propagates, so an API outage shows as an error rather than silently looking
 * like a logged-out user and sending someone to re-enter a working password.
 */
export async function fetchSession(signal?: AbortSignal): Promise<Session | null> {
  try {
    const session = await api.get<Session>('/api/v1/auth/me', {
      signal,
      expectUnauthenticated: true,
    })
    return adoptSession(session)
  } catch (error) {
    if (error instanceof ApiError && error.isUnauthenticated) {
      setCsrfToken(null)
      return null
    }
    throw error
  }
}

export async function logout(): Promise<void> {
  try {
    await api.post<void>('/api/v1/auth/logout')
  } finally {
    // Whatever the server said, this browser is done with the session.
    setCsrfToken(null)
  }
}

export function changePassword(currentPassword: string, newPassword: string): Promise<void> {
  return api.post<void>('/api/v1/auth/password', {
    current_password: currentPassword,
    new_password: newPassword,
  })
}

export function fetchCompany(): Promise<CompanyDetail> {
  return api.get<CompanyDetail>('/api/v1/console/company')
}

// ---------------------------------------------------------------------------
// Sites
// ---------------------------------------------------------------------------

export function fetchSites(): Promise<SitesResponse> {
  return api.get<SitesResponse>('/api/v1/console/sites')
}

export function fetchSite(siteId: string): Promise<Site> {
  return api.get<Site>(`/api/v1/console/sites/${encodeURIComponent(siteId)}`)
}

export function fetchSiteSettings(siteId: string): Promise<SiteSettings> {
  return api.get<SiteSettings>(`/api/v1/console/sites/${encodeURIComponent(siteId)}/settings`)
}

/**
 * Replaces a site's settings WHOLESALE — keys omitted are removed, not merged.
 *
 * Side effect on the server: a SETTINGS sync job is queued for every terminal at
 * the site. Callers should tell the operator that, because it is the difference
 * between editing a record and reconfiguring hardware.
 */
export function updateSiteSettings(
  siteId: string,
  settings: SiteSettingsRequest,
): Promise<SiteSettings> {
  return api.put<SiteSettings>(
    `/api/v1/console/sites/${encodeURIComponent(siteId)}/settings`,
    settings,
  )
}

// ---------------------------------------------------------------------------
// Terminals
// ---------------------------------------------------------------------------

export function fetchTerminals(options: { outdated?: boolean } = {}): Promise<TerminalsResponse> {
  const query = options.outdated ? '?outdated=true' : ''
  return api.get<TerminalsResponse>(`/api/v1/console/terminals${query}`)
}

export function fetchTerminalSummary(): Promise<FleetSummary> {
  return api.get<FleetSummary>('/api/v1/console/terminals/summary')
}

export function fetchTerminal(serial: string): Promise<TerminalDetail> {
  return api.get<TerminalDetail>(`/api/v1/console/terminals/${encodeURIComponent(serial)}`)
}

/**
 * Points a terminal at a capability, or at MULTI_PURPOSE.
 *
 * Returns the same shape as the detail read, so a caller can replace what it
 * holds with the response rather than refetching.
 */
export function updateTerminalMode(
  serial: string,
  body: TerminalModeRequest,
): Promise<TerminalDetail> {
  return api.put<TerminalDetail>(
    `/api/v1/console/terminals/${encodeURIComponent(serial)}/application-mode`,
    body,
  )
}

// ---------------------------------------------------------------------------
// People
// ---------------------------------------------------------------------------

/**
 * One page of people, searched in SQL rather than in the browser.
 *
 * Filtering a fetched page client-side would search the page, not the roster,
 * which is silently wrong the moment a company has more people than fit on one.
 */
export function fetchPeople(query: PeopleQuery = {}): Promise<PeoplePage> {
  const params = new URLSearchParams()
  if (query.search) params.set('q', query.search)
  if (query.limit !== undefined) params.set('limit', String(query.limit))
  if (query.offset) params.set('offset', String(query.offset))
  const suffix = params.size > 0 ? `?${params}` : ''
  return api.get<PeoplePage>(`/api/v1/console/people${suffix}`)
}

export function fetchPerson(externalId: string): Promise<Person> {
  return api.get<Person>(`/api/v1/console/people/${encodeURIComponent(externalId)}`)
}

export function createPerson(body: PersonRequest): Promise<Person> {
  return api.post<Person>('/api/v1/console/people', body)
}

export function updatePerson(externalId: string, body: PersonRequest): Promise<Person> {
  return api.put<Person>(`/api/v1/console/people/${encodeURIComponent(externalId)}`, body)
}

/**
 * Soft-deletes a person.
 *
 * NOT a cosmetic removal. The server enqueues a DELETE sync job to every
 * terminal in the company, which is the only mechanism by which an offline
 * terminal ever learns to forget a credential. Any caller must say so before
 * asking for confirmation.
 */
export function deletePerson(externalId: string): Promise<void> {
  return api.delete<void>(`/api/v1/console/people/${encodeURIComponent(externalId)}`)
}

// ---------------------------------------------------------------------------
// Operators
// ---------------------------------------------------------------------------

export function fetchOperators(): Promise<OperatorsResponse> {
  return api.get<OperatorsResponse>('/api/v1/console/operators')
}

export function fetchOperator(operatorId: string): Promise<OperatorAccount> {
  return api.get<OperatorAccount>(`/api/v1/console/operators/${encodeURIComponent(operatorId)}`)
}

export function fetchOperatorSites(operatorId: string): Promise<OperatorSitesResponse> {
  return api.get<OperatorSitesResponse>(
    `/api/v1/console/operators/${encodeURIComponent(operatorId)}/sites`,
  )
}

export function createOperator(body: CreateOperatorRequest): Promise<OperatorAccount> {
  return api.post<OperatorAccount>('/api/v1/console/operators', body)
}

export function updateOperator(
  operatorId: string,
  body: UpdateOperatorRequest,
): Promise<OperatorAccount> {
  return api.put<OperatorAccount>(
    `/api/v1/console/operators/${encodeURIComponent(operatorId)}`,
    body,
  )
}

/** Replaces grants wholesale. An empty list means "not scoped" — every site. */
export function setOperatorSites(
  operatorId: string,
  body: SiteGrantsRequest,
): Promise<OperatorSitesResponse> {
  return api.put<OperatorSitesResponse>(
    `/api/v1/console/operators/${encodeURIComponent(operatorId)}/sites`,
    body,
  )
}

export function deleteOperator(operatorId: string): Promise<void> {
  return api.delete<void>(`/api/v1/console/operators/${encodeURIComponent(operatorId)}`)
}

// ---------------------------------------------------------------------------
// Applications
// ---------------------------------------------------------------------------

export function fetchApplications(): Promise<ApplicationsResponse> {
  return api.get<ApplicationsResponse>('/api/v1/console/applications')
}

/**
 * Enables, disables or configures one capability. OWNER only, server-side.
 *
 * Settings are left alone when the body omits them, so toggling `enabled` does
 * not discard a configuration the caller never mentioned.
 */
export function updateApplication(
  code: ApplicationCode,
  body: ApplicationRequest,
): Promise<ConfiguredApplication> {
  return api.put<ConfiguredApplication>(
    `/api/v1/console/applications/${encodeURIComponent(code)}`,
    body,
  )
}
