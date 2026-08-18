import { ApiError, api } from './client'
import { setCsrfToken } from './csrf'
import type {
  AccessDecision,
  AccessEvaluationRequest,
  AdoptTerminalRequest,
  ApplicationCode,
  ApplicationRequest,
  ApplicationsResponse,
  ApproveTerminalRequest,
  AuditPage,
  AuditQuery,
  ClaimCodeRequest,
  ClaimCodeResponse,
  CompanyDetail,
  ConfiguredApplication,
  CreateFirmwareRequest,
  CreateSiteRequest,
  CreateSiteResponse,
  CreateOperatorRequest,
  CreateOperatorResponse,
  EventPage,
  EventQuery,
  FirmwareResponse,
  FirmwareVersion,
  FleetSummary,
  InvitationResponse,
  OperatorAccount,
  OperatorSitesResponse,
  OperatorsResponse,
  PasswordResetAcceptedResponse,
  PendingTerminal,
  PendingTerminalsResponse,
  PeoplePage,
  PeopleQuery,
  Permission,
  PermissionRequest,
  PermissionsResponse,
  Person,
  PersonRequest,
  RejectTerminalRequest,
  ResetResponse,
  RetireSiteResponse,
  Schedule,
  ScheduleRequest,
  SchedulesResponse,
  RotateSiteKeyResponse,
  Session,
  Site,
  SiteGrantsRequest,
  SiteSettings,
  SiteSettingsRequest,
  SitesResponse,
  SignupRequest,
  TerminalDetail,
  TerminalLifecycleResponse,
  TerminalModeRequest,
  TerminalMoveRequest,
  TerminalResyncResponse,
  TerminalRetireRequest,
  TerminalRetiredResponse,
  TerminalRevokeRequest,
  TerminalStateRequest,
  TerminalsResponse,
  UpdateOperatorRequest,
  UpdateSiteRequest,
  WifiRecoveryStatus,
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
 * Creates a company and its first OWNER, and signs them in. UNAUTHENTICATED.
 *
 * RETURNS THE SAME BODY LOGIN DOES, and for the same reason /me does: the
 * account exists and the session cookie is already set by the time this
 * resolves, so a console that had to send the new customer back to a login form
 * would be asking them to prove something the server has just established.
 *
 * WHAT IT DOES NOT RETURN, and must never: the site provisioning key. The server
 * creates the company's first site in the same transaction and keeps only the
 * hash of its credential — there is no plaintext to leak here, which is what
 * makes this endpoint safe to expose to an anonymous browser at all.
 */
export async function register(body: SignupRequest): Promise<Session> {
  const session = await api.post<Session>('/api/v1/auth/register', body)
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

/**
 * Creates a site and returns it WITH its one-time provisioning key.
 *
 * The only endpoint besides rotation whose response carries a site key. The
 * caller owns what happens to it next: it must not be cached, stored, logged or
 * put in a URL, and there is no way to ask for it again.
 */
export function createSite(body: CreateSiteRequest): Promise<CreateSiteResponse> {
  return api.post<CreateSiteResponse>('/api/v1/console/sites', body)
}

export function updateSite(siteId: string, body: UpdateSiteRequest): Promise<Site> {
  return api.put<Site>(`/api/v1/console/sites/${encodeURIComponent(siteId)}`, body)
}

/**
 * Retires a site AND every terminal at it, reporting how many.
 *
 * Not reversible through the API. `updateSite({active: false})` is the
 * reversible alternative and is what "closed for a month" needs.
 */
export function retireSite(siteId: string): Promise<RetireSiteResponse> {
  return api.delete<RetireSiteResponse>(`/api/v1/console/sites/${encodeURIComponent(siteId)}`)
}

/**
 * Issues a replacement provisioning key, invalidating the old one immediately.
 *
 * A POST to a sub-resource rather than a PUT on the site: it does not set a
 * value the caller supplied, it mints one. Returns the new key once, plus the
 * count of terminals it just locked out.
 */
export function rotateSiteKey(siteId: string): Promise<RotateSiteKeyResponse> {
  return api.post<RotateSiteKeyResponse>(
    `/api/v1/console/sites/${encodeURIComponent(siteId)}/api-key`,
  )
}

/**
 * Mints a one-time claim code for ONE terminal serial at this site. ADMIN.
 *
 * THE ALTERNATIVE TO HANDING OUT THE SITE KEY. The provisioning key registers
 * any terminal at the site, for ever; this registers one serial, once, for
 * minutes, and cannot be read back afterwards. Anywhere the console offers to
 * provision hardware it offers this, and never the key as a shortcut.
 *
 * Same caching contract as `createSite` and `rotateSiteKey`: the code lives in
 * the mutation's own `data` for the life of one panel, is never written to the
 * query cache, and the caller resets the mutation when that panel closes.
 */
export function issueClaimCode(
  siteId: string,
  body: ClaimCodeRequest,
): Promise<ClaimCodeResponse> {
  return api.post<ClaimCodeResponse>(
    `/api/v1/console/sites/${encodeURIComponent(siteId)}/claim-codes`,
    body,
  )
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
// Adding a terminal: announce and approve
// ---------------------------------------------------------------------------

/**
 * Claims a terminal that is displaying a pairing code. ADMIN.
 *
 * THE ONE FIELD A CUSTOMER FILLS IN. Until this succeeds the terminal belongs to
 * nobody and is listed by no endpoint — there is no browsing of unclaimed
 * hardware, which is what keeps one customer's units invisible to every other.
 *
 * Three refusals worth handling distinctly, and the server distinguishes them by
 * `code`: `PAIRING_CODE_REFUSED` (404, deliberately uniform across unknown,
 * expired and spent), `TERMINAL_OWNED_ELSEWHERE` (409) and `TERMINAL_DISABLED`
 * (409).
 *
 * NOTHING IS MINTED HERE. Adoption records that a company has taken
 * responsibility for a unit; the credential comes later, and only after approval.
 */
export function adoptTerminal(body: AdoptTerminalRequest): Promise<PendingTerminal> {
  return api.post<PendingTerminal>('/api/v1/console/terminal-announcements/adopt', body)
}

/** Terminals waiting to be set up. MANAGER — seeing one is operational. */
export function fetchPendingTerminals(): Promise<PendingTerminalsResponse> {
  return api.get<PendingTerminalsResponse>('/api/v1/console/terminal-announcements')
}

export function fetchPendingTerminal(id: string): Promise<PendingTerminal> {
  return api.get<PendingTerminal>(
    `/api/v1/console/terminal-announcements/${encodeURIComponent(id)}`,
  )
}

/**
 * Places an adopted terminal at a site and authorises it. ADMIN.
 *
 * AUTHORISES; MINTS NOTHING. The terminal collects its own credential
 * afterwards, which is why the response carries no key and why there is nothing
 * here for a caller to be careful with.
 */
export function approveTerminal(
  id: string,
  body: ApproveTerminalRequest,
): Promise<PendingTerminal> {
  return api.post<PendingTerminal>(
    `/api/v1/console/terminal-announcements/${encodeURIComponent(id)}/approve`,
    body,
  )
}

/**
 * Refuses a terminal, and the UNDO for an approval.
 *
 * The second is the operationally useful one: it frees the serial, so a unit
 * that was approved and then reset can be added again.
 */
export function rejectTerminal(
  id: string,
  body: RejectTerminalRequest = {},
): Promise<PendingTerminal> {
  return api.post<PendingTerminal>(
    `/api/v1/console/terminal-announcements/${encodeURIComponent(id)}/reject`,
    body,
  )
}

// ---------------------------------------------------------------------------
// Terminal lifecycle
// ---------------------------------------------------------------------------

/**
 * Disables or re-enables a terminal. REVERSIBLE, and does not touch the
 * credential — a repaired unit comes back without a site visit.
 */
export function setTerminalState(
  serial: string,
  body: TerminalStateRequest,
): Promise<TerminalLifecycleResponse> {
  return api.put<TerminalLifecycleResponse>(
    `/api/v1/console/terminals/${encodeURIComponent(serial)}/state`,
    body,
  )
}

/**
 * Invalidates a terminal's device credential. THE ANSWER TO A STOLEN TERMINAL.
 *
 * Irreversible for that credential: the hardware must re-register with the
 * site's provisioning key before it can authenticate again. Queued work for it
 * is cancelled, and the response says how much.
 */
export function revokeTerminalCredential(
  serial: string,
  body: TerminalRevokeRequest = {},
): Promise<TerminalLifecycleResponse> {
  return api.post<TerminalLifecycleResponse>(
    `/api/v1/console/terminals/${encodeURIComponent(serial)}/revoke`,
    body,
  )
}

/**
 * Retires a terminal: soft-deleted AND its credential revoked.
 *
 * One-way through the API. The body carries the reason, which is why this sends
 * one on a DELETE — the server reads it when there is one and the reason is what
 * the next operator needs.
 */
export function retireTerminal(
  serial: string,
  body: TerminalRetireRequest = {},
): Promise<TerminalRetiredResponse> {
  return api.delete<TerminalRetiredResponse>(
    `/api/v1/console/terminals/${encodeURIComponent(serial)}`,
    { body },
  )
}

/**
 * Reassigns a terminal to another site in the same company.
 *
 * THE ROSTER IS REBUILT RATHER THAN CARRIED: queued work for the old site is
 * cancelled and the terminal re-converges against the new one. A caller has to
 * say so — a move is not a metadata edit.
 */
export function moveTerminal(
  serial: string,
  body: TerminalMoveRequest,
): Promise<TerminalLifecycleResponse> {
  return api.put<TerminalLifecycleResponse>(
    `/api/v1/console/terminals/${encodeURIComponent(serial)}/site`,
    body,
  )
}

/** Replaces a terminal's queue with a snapshot of current state. MANAGER. */
export function resyncTerminal(serial: string): Promise<TerminalResyncResponse> {
  return api.post<TerminalResyncResponse>(
    `/api/v1/console/terminals/${encodeURIComponent(serial)}/resync`,
  )
}

/**
 * Asks one terminal to return to Wi-Fi setup mode. ADMIN.
 *
 * NO BODY, AND THERE IS NOWHERE TO PUT ONE. The platform never learns the
 * customer's Wi-Fi password: the command hands the terminal back to the same
 * setup portal a new unit uses, and somebody standing next to it connects a
 * phone and types the new password into the hardware. If a network name or a
 * passphrase ever appears in this call, the feature has been misunderstood.
 *
 * 202, not 200: the command has been accepted for delivery and nothing has
 * happened at the terminal yet. Poll `fetchWifiRecoveryStatus` for what did.
 *
 * SAFELY REPEATABLE. Sending it again while one is outstanding returns the same
 * command with `already_queued`, rather than queueing a second — which the
 * terminal would apply twice, the second time after the customer had already
 * re-provisioned it.
 */
export function requestWifiRecovery(serial: string): Promise<WifiRecoveryStatus> {
  return api.post<WifiRecoveryStatus>(
    `/api/v1/console/terminals/${encodeURIComponent(serial)}/wifi-recovery`,
  )
}

/** What became of this terminal's most recent Change Wi-Fi command. ADMIN. */
export function fetchWifiRecoveryStatus(serial: string): Promise<WifiRecoveryStatus> {
  return api.get<WifiRecoveryStatus>(
    `/api/v1/console/terminals/${encodeURIComponent(serial)}/wifi-recovery`,
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

/**
 * Creates an operator.
 *
 * Omitting `password` is the preferred path: the account is created with a
 * credential nobody holds and the response carries a single-use invitation
 * instead. The return type is a union for that reason — see CreateOperatorResponse.
 */
export function createOperator(body: CreateOperatorRequest): Promise<CreateOperatorResponse> {
  return api.post<CreateOperatorResponse>('/api/v1/console/operators', body)
}

/**
 * Issues a fresh invitation for an account that has NEVER SIGNED IN.
 *
 * Refused with 409 for an account that has, because re-inviting one would be a
 * reset wearing an invitation's name and the two are audited differently. Issuing
 * supersedes any outstanding link, so re-sending cannot leave two live tokens.
 */
export function inviteOperator(operatorId: string): Promise<InvitationResponse> {
  return api.post<InvitationResponse>(
    `/api/v1/console/operators/${encodeURIComponent(operatorId)}/invite`,
  )
}

/**
 * Mints a single-use reset link, WITHOUT the administrator choosing a password.
 *
 * The alternative — `updateOperator({password})` — still exists for a deployment
 * that cannot deliver a link, and leaves the administrator knowing the
 * credential. This one does not.
 */
export function resetOperatorPassword(operatorId: string): Promise<ResetResponse> {
  return api.post<ResetResponse>(
    `/api/v1/console/operators/${encodeURIComponent(operatorId)}/reset`,
  )
}

// ---------------------------------------------------------------------------
// Unauthenticated credential handover
// ---------------------------------------------------------------------------

/**
 * Asks for a password reset. UNAUTHENTICATED — somebody who has forgotten their
 * password cannot sign in to ask.
 *
 * ALWAYS 202 WITH THE SAME BODY whether or not the address exists. The console
 * must not branch on the response either, or it would reintroduce in the browser
 * the enumeration oracle the server refuses to be.
 */
export function requestPasswordReset(email: string): Promise<PasswordResetAcceptedResponse> {
  return api.post<PasswordResetAcceptedResponse>('/api/v1/auth/forgot-password', { email })
}

/**
 * Sets a password from an invitation or reset link. UNAUTHENTICATED — the token
 * IS the authorisation.
 *
 * Answers 204 and NOT a session: redeeming sets a password, it does not sign
 * anybody in. Every existing session for the account is revoked server-side,
 * because somebody redeeming a reset usually believes the old password was known
 * to someone else.
 *
 * The distinct failures matter and are branched on by STATUS, never by message:
 * 404 unknown, 410 expired, 409 already used.
 */
export function redeemCredential(token: string, newPassword: string): Promise<void> {
  return api.post<void>('/api/v1/auth/redeem', { token, new_password: newPassword })
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

// ---------------------------------------------------------------------------
// Access control
// ---------------------------------------------------------------------------

/**
 * Every rule about one person. VIEWER server-side.
 *
 * Readable by anyone, deliberately: "why was she refused" is a question somebody
 * at a front desk has to be able to answer, and the rules are not secret from
 * the people administering the deployment.
 */
export function fetchPersonPermissions(externalId: string): Promise<PermissionsResponse> {
  return api.get<PermissionsResponse>(
    `/api/v1/console/people/${encodeURIComponent(externalId)}/permissions`,
  )
}

/** Grants (or denies) at one scope. MANAGER — this is day-to-day work. */
export function grantPermission(
  externalId: string,
  body: PermissionRequest,
): Promise<Permission> {
  return api.post<Permission>(
    `/api/v1/console/people/${encodeURIComponent(externalId)}/permissions`,
    body,
  )
}

/** Addressed by the permission's own id, not by the person's. */
export function revokePermission(permissionId: string): Promise<void> {
  return api.delete<void>(`/api/v1/console/permissions/${encodeURIComponent(permissionId)}`)
}

export function fetchSchedules(): Promise<SchedulesResponse> {
  return api.get<SchedulesResponse>('/api/v1/console/schedules')
}

export function createSchedule(body: ScheduleRequest): Promise<Schedule> {
  return api.post<Schedule>('/api/v1/console/schedules', body)
}

export function updateSchedule(scheduleId: string, body: ScheduleRequest): Promise<Schedule> {
  return api.put<Schedule>(
    `/api/v1/console/schedules/${encodeURIComponent(scheduleId)}`,
    body,
  )
}

/**
 * Deletes a schedule.
 *
 * REFUSED WITH 409 while any permission still references it. Cascading would
 * silently WIDEN every rule that used it — a permission restricted to office
 * hours would become one with no time restriction at all — so the server refuses
 * and the console has to say which rules are in the way.
 */
export function deleteSchedule(scheduleId: string): Promise<{ deleted: boolean }> {
  return api.delete<{ deleted: boolean }>(
    `/api/v1/console/schedules/${encodeURIComponent(scheduleId)}`,
  )
}

/**
 * "Would this person get in at this terminal, right now, and why."
 *
 * WRITES NO EVENT. A preview that recorded a door event would put a
 * presentation that never happened into the trail an attendance report is built
 * from — so this is a POST because it carries a body, not because it changes
 * anything.
 */
export function evaluateAccess(
  serial: string,
  body: AccessEvaluationRequest,
): Promise<AccessDecision> {
  return api.post<AccessDecision>(
    `/api/v1/console/terminals/${encodeURIComponent(serial)}/evaluate`,
    body,
  )
}

// ---------------------------------------------------------------------------
// Field events
// ---------------------------------------------------------------------------

/**
 * The door log. VIEWER, and narrowed by site grant inside the handler.
 *
 * VIEWER unlike the audit trail, which is ADMIN, and the distinction is the
 * point: an event trail says what happened in the field, while an audit trail
 * names which operators changed what — administrative information about
 * colleagues rather than the product working.
 */
export function fetchEvents(query: EventQuery = {}): Promise<EventPage> {
  const params = new URLSearchParams()
  for (const [key, value] of Object.entries(query)) {
    if (value !== undefined && value !== '') params.set(key, String(value))
  }
  const suffix = params.size > 0 ? `?${params}` : ''
  return api.get<EventPage>(`/api/v1/console/events${suffix}`)
}

// ---------------------------------------------------------------------------
// Audit trail
// ---------------------------------------------------------------------------

/**
 * One page of recorded operator actions. ADMIN server-side.
 *
 * Filters are narrowing conveniences: the server IGNORES an unparseable date
 * rather than rejecting the request, so this validates instants before sending
 * and simply omits an empty filter rather than sending a blank one.
 */
export function fetchAuditEvents(query: AuditQuery = {}): Promise<AuditPage> {
  const params = new URLSearchParams()
  if (query.action) params.set('action', query.action)
  if (query.target_type) params.set('target_type', query.target_type)
  if (query.actor) params.set('actor', query.actor)
  if (query.since) params.set('since', query.since)
  if (query.until) params.set('until', query.until)
  if (query.limit !== undefined) params.set('limit', String(query.limit))
  if (query.offset) params.set('offset', String(query.offset))
  const suffix = params.size > 0 ? `?${params}` : ''
  return api.get<AuditPage>(`/api/v1/console/audit${suffix}`)
}

// ---------------------------------------------------------------------------
// Firmware catalogue
// ---------------------------------------------------------------------------

export function fetchFirmware(): Promise<FirmwareResponse> {
  return api.get<FirmwareResponse>('/api/v1/console/firmware')
}

/**
 * Adds a build to the catalogue. It does NOT become the deployment target —
 * that is a separate, deliberate call.
 */
export function createFirmware(body: CreateFirmwareRequest): Promise<FirmwareVersion> {
  return api.post<FirmwareVersion>('/api/v1/console/firmware', body)
}

/**
 * Moves the deployment target for a device type and release channel.
 *
 * THIS STARTS A ROLLOUT. Terminals of that type on that channel are offered the
 * build on their next heartbeat, and a terminal that accepts one downloads it,
 * verifies the digest, flashes it and reboots. Any UI in front of this must
 * confirm before calling it and must say what it can set in motion.
 *
 * The response is the promoted build and says nothing about how many terminals
 * were affected — the count has to be derived from the fleet the caller already
 * holds, using the same device-type, channel and version rule the server's offer
 * query applies.
 *
 * Addressed by the numeric `id`, not `public_id` — that is what the route takes.
 */
export function setCurrentFirmware(id: number): Promise<FirmwareVersion> {
  return api.put<FirmwareVersion>(`/api/v1/console/firmware/${encodeURIComponent(id)}/current`)
}
