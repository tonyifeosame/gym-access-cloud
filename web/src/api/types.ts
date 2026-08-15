// Types mirroring the API contracts in API_SPEC.md.
//
// Hand-written rather than generated: the surface is small, and writing them by
// hand is what forced the semantics below to be read carefully rather than
// assumed.

/**
 * An application is a CAPABILITY the platform offers -- not an industry, a
 * customer, or the identity of the product. The same console serves a company
 * running door access, one recording attendance, and one doing neither.
 *
 * This union is a convenience for the ones we know about today. The API is the
 * authority: `available` in the applications response is the real catalog, and
 * an unknown code must still render rather than be dropped, so that a capability
 * added to the platform appears without a frontend release.
 */
export type KnownApplicationCode =
  | 'ACCESS_CONTROL'
  | 'ATTENDANCE'
  | 'REGISTRATION'
  | 'CHECK_IN'
  | 'VERIFICATION'
  | 'TIME_TRACKING'
  | 'VISITOR_MANAGEMENT'

/** Any code the API may send, known to this build or not. */
export type ApplicationCode = KnownApplicationCode | (string & {})

/**
 * MULTI_PURPOSE is a TERMINAL mode, never a company capability: a terminal that
 * serves whatever its company has enabled. It is deliberately not part of
 * ApplicationCode's known set.
 */
export const MULTI_PURPOSE = 'MULTI_PURPOSE' as const

export type Role = 'OWNER' | 'ADMIN' | 'MANAGER' | 'VIEWER'

export interface Operator {
  id: string
  email: string
  full_name: string
  role: Role
}

export interface Company {
  id: string
  name: string
  slug: string
}

export interface SiteGrant {
  site_id: string
  site_name: string
}

export interface EnabledApplication {
  code: ApplicationCode
  settings: Record<string, unknown>
}

/**
 * The body returned identically by POST /auth/login and GET /auth/me.
 *
 * Two fields carry semantics that are easy to get backwards:
 *
 *   all_sites   An empty `sites` array means the operator is NOT SCOPED to
 *               particular sites, which is EVERY site in the company -- not
 *               none. This flag is how the two are told apart.
 *
 *   applications  The capabilities this company has enabled. An EMPTY ARRAY IS
 *               LEGITIMATE and common; it must never be treated as an error or
 *               as a reason to fall back to a default set of screens.
 */
export interface Session {
  operator: Operator
  company: Company
  role: Role
  sites: SiteGrant[]
  all_sites: boolean
  applications: EnabledApplication[]
  /**
   * Set when the credential in use was chosen by somebody OTHER than its owner —
   * an invitation redeemed with a generated password, or an administrative
   * reset.
   *
   * REPORTED, NOT ENFORCED, and the console has to supply the enforcement. The
   * server deliberately does not refuse these sessions: an account refused every
   * request could not reach /auth/password either, which is the one thing it
   * needs to do. So the block lives here, in front of the console, and it must be
   * a block rather than a suggestion — the whole point is that somebody else
   * currently knows this password.
   */
  must_change_password?: boolean
  csrf_token: string
  session_expires_at: string
  session_expires_in_seconds: number
}

export interface CompanyDetail extends Company {
  contact_email?: string
  active: boolean
  created_at: string
}

export interface Site {
  id: string
  name: string
  address?: string
  /**
   * IANA zone. Describes where the HARDWARE stands — a different question from
   * the zone an operator reads timestamps in, which is their own browser's.
   */
  timezone: string
  active: boolean
  /** Live terminals at this site. */
  terminal_count: number
  created_at: string
  /**
   * The first 12 characters of the site's provisioning key. NOT SECRET — it
   * identifies which key a site is on without being reconstructible.
   *
   * OPTIONAL BECAUSE THE READ ENDPOINTS DO NOT YET RETURN IT. The column exists
   * and `database.SiteKeyPrefix` reads it, but `consoleSiteColumns` does not
   * select it, so today this is populated only from a create or rotate
   * response. Typed optional rather than assumed, so adding it to the projection
   * is a backend-only change with nothing to alter here.
   */
  api_key_prefix?: string
}

/**
 * Create a site.
 *
 * No key field in either direction. The server generates it — a caller-chosen
 * provisioning secret is a caller-chosen weak provisioning secret — and returns
 * it exactly once, in the response.
 */
export interface CreateSiteRequest {
  name: string
  address?: string
  /** Empty defaults to UTC server-side. */
  timezone?: string
}

/**
 * Update a site's metadata. Every field optional; only what is sent is applied.
 *
 * `active: false` is DEACTIVATION, which is reversible and is not retirement.
 * It stops the site key and every terminal at the site authenticating, and
 * destroys nothing. DELETE is the one-way door.
 */
export interface UpdateSiteRequest {
  name?: string
  address?: string
  timezone?: string
  active?: boolean
}

/**
 * A freshly minted provisioning key.
 *
 * THIS IS THE ONLY SHAPE THAT EVER CARRIES ONE, and it arrives only from site
 * creation or key rotation. It must never be written to localStorage,
 * sessionStorage, a URL, a query key, or the React Query cache — it lives in
 * component state for the life of one panel and is dropped when that panel
 * closes. `shown_once` is the server saying so, so no client has to remember.
 */
export interface SiteCredential {
  api_key: string
  api_key_prefix: string
  shown_once: boolean
}

export interface CreateSiteResponse {
  site: Site
  credential: SiteCredential
}

/**
 * The result of rotating a site's key.
 *
 * `legacy_terminals` counts terminals at the site that have never been issued a
 * device credential of their own and therefore certainly still authenticate
 * with the SITE key — the ones this rotation just locked out. Terminals holding
 * their own device key are unaffected.
 */
export interface RotateSiteKeyResponse {
  credential: SiteCredential
  legacy_terminals: number
}

/**
 * The result of retiring a site.
 *
 * `terminals_retired` is not decoration: retiring a site soft-deletes its
 * terminals in the same transaction and every one of them stops opening a door
 * immediately. A screen that does not surface this number is not describing
 * what happened.
 */
export interface RetireSiteResponse {
  retired: boolean
  terminals_retired: number
}

export interface ConfiguredApplication {
  id: string
  code: ApplicationCode
  enabled: boolean
  settings: Record<string, unknown>
  created_at: string
  updated_at: string
}

export interface ApplicationsResponse {
  /** Every capability the company has configured, enabled or not. */
  configured: ConfiguredApplication[]
  /** The subset currently switched on. */
  enabled: ApplicationCode[]
  /** The platform's catalog. Read this rather than hard-coding the list. */
  available: ApplicationCode[]
}

/** Console lists are enveloped: `{count, <name>: [...]}`. */
export interface SitesResponse {
  count: number
  sites: Site[]
}

/**
 * A site's device configuration.
 *
 * `settings` is an OPEN JSON object, deliberately. The platform does not fix the
 * key set: firmware gains options over time and a console that only understood a
 * closed list would silently drop whatever it had not been taught. The console
 * therefore offers a guided editor over the keys it recognises AND a raw editor
 * over the whole object, and never discards a key it did not expect.
 *
 * `settings_version` increments on every write and is the value a terminal uses
 * to discard a stale push it receives after a newer one.
 */
export interface SiteSettings {
  settings: Record<string, unknown>
  settings_version: number
}

/**
 * PUT /console/sites/:id/settings replaces the object WHOLESALE. Keys omitted
 * are removed, not merged. Any editor must therefore send the complete object,
 * which is why the guided form is built on top of the fetched settings rather
 * than on its own idea of the shape.
 */
export type SiteSettingsRequest = Record<string, unknown>

// ---------------------------------------------------------------------------
// Terminals
// ---------------------------------------------------------------------------

/**
 * Reported terminal states. `status` is a string on the wire and an unknown
 * value must still render -- firmware may report a state this build predates.
 */
export type KnownTerminalStatus =
  | 'ONLINE'
  | 'OFFLINE'
  | 'UPDATING'
  | 'ERROR'
  | 'DISABLED'
  | 'PROVISIONING'

export type TerminalStatus = KnownTerminalStatus | (string & {})

/**
 * A terminal as the console lists it.
 *
 * NO CREDENTIAL MATERIAL. Not the device key, not its hash, not the site key --
 * the projection does not select them, so there is nothing here to leak.
 *
 * TWO SITE IDENTIFIERS. `site_id` is the internal row id and predates the
 * console; `site_public_id` is what `/console/sites` calls `id`, and is the only
 * one a browser can join on. Match on the public id, never on `site_name` --
 * names are editable and not unique.
 */
export interface Terminal {
  id: number
  public_id: string
  site_id: number
  site_public_id: string
  site_name: string
  serial_number: string
  device_name: string
  device_type: string
  status: TerminalStatus
  active: boolean
  release_channel: string
  firmware_version: string
  hardware_revision?: string
  build_number?: string
  boot_count?: number
  last_seen_at?: string
  last_sync_at?: string
  last_heartbeat_at?: string
  current_firmware_version: string
  firmware_outdated: boolean
}

/**
 * One terminal in full.
 *
 * `application_mode` is what the terminal is ASSIGNED to do;
 * `effective_applications` is what that RESOLVES TO right now. They diverge when
 * the company disables a capability a terminal is pointed at: the assignment is
 * retained rather than rewritten, and effective goes empty. Showing only one of
 * the two would be misleading in exactly that case.
 *
 * The mode may be MULTI_PURPOSE, which is a device mode and never a company
 * capability.
 */
export interface TerminalDetail extends Terminal {
  application_mode: ApplicationCode | typeof MULTI_PURPOSE
  effective_applications: ApplicationCode[]
}

export interface TerminalsResponse {
  count: number
  terminals: Terminal[]
}

/** Counts for the fleet, narrowed to the caller's granted sites. */
export interface FleetSummary {
  total: number
  online: number
  offline: number
  updating: number
  error: number
  disabled: number
  provisioning: number
  firmware_outdated: number
}

export interface TerminalModeRequest {
  application_mode: ApplicationCode | typeof MULTI_PURPOSE
}

// ---------------------------------------------------------------------------
// Terminal lifecycle
// ---------------------------------------------------------------------------

/**
 * THREE OPERATIONS THAT SOUND ALIKE AND ARE NOT. Every screen in front of them
 * has to make the difference unmistakable, because choosing the wrong one is
 * either an unnecessary site visit or a door that quietly keeps working.
 *
 *   DISABLE   Reversible. The terminal stops authenticating; its credential is
 *             untouched, so re-enabling brings it back with no site visit. This
 *             is what a faulty or temporarily unused unit wants.
 *
 *   REVOKE    Irreversible for that credential. The device key hash is cleared,
 *             so the credential stops resolving at all. The hardware still
 *             exists and can be re-registered with the site's provisioning key.
 *             This is what a STOLEN or missing unit wants.
 *
 *   RETIRE    One-way. The row is soft-deleted AND the credential revoked. The
 *             terminal leaves every console list. This is what a unit that has
 *             been decommissioned, destroyed or returned wants.
 *
 * All three are ADMIN server-side, matching site lifecycle rather than the
 * MANAGER gate on day-to-day writes.
 */

/** Disable or re-enable. `reason` lands in the audit trail and on the row. */
export interface TerminalStateRequest {
  disabled: boolean
  reason?: string
}

export interface TerminalRevokeRequest {
  reason?: string
}

export interface TerminalRetireRequest {
  reason?: string
}

/** `site_id` is a site's PUBLIC id, matching what /console/sites returns. */
export interface TerminalMoveRequest {
  site_id: string
}

/**
 * What a lifecycle mutation answers with.
 *
 * `terminal` is OPTIONAL, and that is the server's contract rather than
 * defensiveness: the mutation is applied first and the row read back
 * afterwards, and a failed read-back returns the operation's own facts without
 * it rather than a 500 that would invite a retry of a completed mutation. A
 * caller must therefore treat a missing `terminal` as "it worked, refetch"
 * rather than as failure.
 */
export interface TerminalLifecycleResponse {
  terminal?: TerminalDetail
  status?: TerminalStatus
  active?: boolean
  credential_cleared?: boolean
  /**
   * Queued sync work that will now never be delivered. Not decoration: an
   * operator who is not told believes those changes reached the hardware.
   */
  pending_jobs_cancelled?: number
  moved?: boolean
  /** The server's own words on how to bring a revoked terminal back. */
  recovery?: string
}

export interface TerminalRetiredResponse {
  serial_number: string
  retired: boolean
  credential_cleared: boolean
  pending_jobs_cancelled: number
}

/**
 * The result of forcing a resync.
 *
 * `superseded_jobs` is queued work the snapshot replaced; `pending_jobs` is what
 * the terminal will now collect. Both are worth showing — a resync that
 * supersedes a large backlog is a different event from one that queues three
 * rows.
 */
export interface TerminalResyncResponse {
  serial_number: string
  superseded_jobs: number
  pending_jobs: number
}

// ---------------------------------------------------------------------------
// People
// ---------------------------------------------------------------------------

/**
 * Someone the platform knows about.
 *
 * `biometric_enrolled` IS THE ENTIRE BIOMETRIC SURFACE -- a boolean. The
 * credential itself is an abstraction the backend owns: no template, no locator,
 * no sensor or vendor detail crosses this boundary, and none may be introduced
 * here. That is what lets the storage change without the console noticing, and
 * it is enforced by a lint rule as well as by this type.
 *
 * `category` is free text and optional. It maps to a legacy column and carries
 * no meaning the platform assigns -- a company doing visitor management has no
 * "category" worth requiring, and demanding one would be the product assuming a
 * workflow it has no business assuming.
 */
export interface Person {
  id: string
  external_id: string
  full_name: string
  category?: string
  active: boolean
  biometric_enrolled: boolean
  created_at: string
  updated_at: string
}

/**
 * A page of people.
 *
 * `count` is the size of THIS page; `total` is the size of the whole match.
 * Both are needed: "showing 50 of 1,284" needs the pair, and `has_more` is what
 * says whether to offer a next page.
 */
export interface PeoplePage {
  count: number
  total: number
  limit: number
  offset: number
  has_more: boolean
  people: Person[]
}

export interface PeopleQuery {
  /** Matches external id or full name, anywhere, case-insensitively. */
  search?: string
  limit?: number
  offset?: number
}

/**
 * Create or update a person.
 *
 * `active` is optional so that "not supplied" differs from `false`: on update an
 * omitted field leaves the value alone, and sending a plain `false` by accident
 * would deactivate someone whose name was merely being corrected.
 *
 * There is deliberately no credential field in either direction. Enrolment
 * happens at a terminal; the console does not write credentials.
 */
export interface PersonRequest {
  external_id?: string
  full_name: string
  category?: string
  active?: boolean
}

// ---------------------------------------------------------------------------
// Operators
// ---------------------------------------------------------------------------

/**
 * An operator account.
 *
 * `all_sites` again distinguishes "not scoped" from "no access": an empty
 * `sites` array with this set means EVERY site in the company. OWNER and ADMIN
 * always have it, and so does anyone holding no grants.
 */
export interface OperatorAccount {
  id: string
  email: string
  full_name: string
  role: Role
  active: boolean
  last_login_at?: string
  sites?: SiteGrant[]
  all_sites: boolean
  created_at: string
}

export interface OperatorsResponse {
  count: number
  operators: OperatorAccount[]
}

export interface OperatorSitesResponse {
  count: number
  sites: SiteGrant[]
  all_sites: boolean
}

/**
 * Create an operator.
 *
 * `password` IS OPTIONAL, AND OMITTING IT IS THE PREFERRED PATH. An absent
 * password creates the account with a credential nobody holds and returns a
 * single-use invitation instead, so the administrator creating the account never
 * learns how to sign in as it. Supplying one is kept for a deployment that
 * cannot deliver a link at all, and the console says as much where it is offered.
 */
export interface CreateOperatorRequest {
  email: string
  full_name: string
  password?: string
  role: Role
  site_ids?: string[]
}

/**
 * What creating an operator answers with.
 *
 * TWO SHAPES FROM ONE ENDPOINT: a bare account when the caller supplied a
 * password, and `{operator, invitation, delivery}` when it did not. Modelled as
 * a union rather than as an account with optional extras so that reading the
 * invitation requires narrowing, and a caller cannot forget that it may be
 * absent.
 */
export type CreateOperatorResponse =
  | OperatorAccount
  | { operator: OperatorAccount; invitation: CredentialToken; delivery: string }

/** Narrows the union above. */
export function operatorOf(response: CreateOperatorResponse): OperatorAccount {
  return 'operator' in response ? response.operator : response
}

export function invitationOf(response: CreateOperatorResponse): CredentialToken | null {
  return 'invitation' in response ? response.invitation : null
}

/** Every field optional; only what is supplied is applied. */
export interface UpdateOperatorRequest {
  role?: Role
  active?: boolean
  password?: string
}

/** Replaces grants wholesale. An empty list means "not scoped" — every site. */
export interface SiteGrantsRequest {
  site_ids: string[]
}

// ---------------------------------------------------------------------------
// Operator credential handover
// ---------------------------------------------------------------------------

/**
 * A minted invitation or password reset.
 *
 * `token` IS A CREDENTIAL and is returned exactly once, by the call that created
 * it. The server stores only its SHA-256, so it cannot be read back. Everything
 * that applies to a site provisioning key applies here: it must not be written
 * to storage of any kind, put in a query key, logged, or left in the React Query
 * cache after the panel showing it closes.
 *
 * `shown_once` is the server saying so in the payload rather than in
 * documentation, because a client that stores it is the failure mode.
 */
export interface CredentialToken {
  token: string
  purpose: 'INVITE' | 'RESET'
  expires_at: string
  shown_once: boolean
}

/**
 * What an invitation call answers with.
 *
 * `delivery` is the server stating plainly that IT DOES NOT SEND THE LINK. There
 * is no transactional email on this platform, so an administrator who closes the
 * panel without copying the link has to issue another one. Any UI must say that
 * before the operator can navigate away.
 */
export interface InvitationResponse {
  invitation: CredentialToken
  delivery: string
}

export interface ResetResponse {
  reset: CredentialToken
  delivery: string
}

/** Sets a password from an invitation or reset link. The token IS the authorisation. */
export interface RedeemRequest {
  token: string
  new_password: string
}

/**
 * The answer to a self-service reset request.
 *
 * ALWAYS 202 AND ALWAYS IDENTICAL, whether or not the address belongs to an
 * account. The endpoint is unauthenticated and permanently exposed, so an
 * honest "no such account" would be an enumeration oracle. A console that
 * branched on this would reintroduce the oracle in the browser.
 */
export interface PasswordResetAcceptedResponse {
  status: string
  message: string
}

// ---------------------------------------------------------------------------
// Access control
// ---------------------------------------------------------------------------

/**
 * THE RULE THE WHOLE MODEL RESTS ON, restated here because every screen in front
 * of it has to convey it:
 *
 *   Absence of permission is not permission.
 *
 * A person with no rules reaches nothing. That is the opposite of what the
 * platform did before the engine existed — nothing read the permissions table,
 * so every active person with a credential opened every terminal in their
 * company — and it is the single most important thing an operator must not get
 * backwards, because the mistake admits people rather than locking them out and
 * so nobody reports it.
 */

/** A rule names exactly one scope. */
export type PermissionScope = 'COMPANY' | 'SITE' | 'TERMINAL'

/**
 * `DENY` beats any `ALLOW` at any scope.
 *
 * Exclusion cannot be expressed with allow-only rules without enumerating
 * everybody who is not excluded and re-enumerating them whenever anyone joins.
 */
export type PermissionEffect = 'ALLOW' | 'DENY'

export interface Permission {
  id: string
  person_id: string
  scope_type: PermissionScope
  /** Exactly one pair is set, matching `scope_type`. Public ids. */
  site_id?: string
  site_name?: string
  device_serial?: string
  device_name?: string
  /** Empty means EVERY capability, not none. */
  application?: string
  effect: PermissionEffect
  schedule_id?: string
  schedule_name?: string
  starts_at?: string
  ends_at?: string
  active: boolean
  created_at: string
  updated_at: string
}

export interface PermissionsResponse {
  count: number
  permissions: Permission[]
}

export interface PermissionRequest {
  scope_type: PermissionScope
  site_id?: string
  device_serial?: string
  application?: string
  effect?: PermissionEffect
  schedule_id?: string
  starts_at?: string
  ends_at?: string
  active?: boolean
}

/**
 * Day-of-week bits. Monday is 1 through Sunday 64; 127 is every day.
 *
 * A BITMASK, not a list of days, because that is what the server stores and what
 * a terminal evaluates. The console converts at the edge so no screen has to
 * think in powers of two.
 */
export const DAY_BITS = {
  MONDAY: 1,
  TUESDAY: 2,
  WEDNESDAY: 4,
  THURSDAY: 8,
  FRIDAY: 16,
  SATURDAY: 32,
  SUNDAY: 64,
} as const

export const EVERY_DAY = 127

/**
 * One span within a schedule.
 *
 * A window whose end is NOT AFTER its start CROSSES MIDNIGHT: a 22:00–06:00
 * night shift is one window, and `days_of_week` names the day it STARTS on.
 * Splitting it in two would make Sunday night's shift look like Monday's, which
 * is wrong by a whole day for exactly the people most likely to be affected.
 */
export interface ScheduleWindow {
  days_of_week: number
  /** "HH:MM" or "HH:MM:SS", wall clock in the schedule's zone. */
  start_time: string
  end_time: string
}

export interface Schedule {
  id: string
  name: string
  description?: string
  /** Empty means "the timezone of the site the terminal is at", the common case. */
  timezone?: string
  windows: ScheduleWindow[]
  /** How many rules depend on it. Deleting one that any rule uses is refused. */
  permission_count: number
  active: boolean
  created_at: string
  updated_at: string
}

export interface SchedulesResponse {
  count: number
  schedules: Schedule[]
}

export interface ScheduleRequest {
  name: string
  description?: string
  timezone?: string
  active?: boolean
  windows: ScheduleWindow[]
}

/**
 * Why a decision went the way it did.
 *
 * An OPEN set — an application may define its own — but each of these is a
 * distinct thing an operator would do something different about, which is the
 * whole reason the field exists instead of a bare boolean.
 */
export type KnownDecisionReason =
  | 'ALLOWED'
  | 'NO_PERMISSION'
  | 'EXPLICIT_DENY'
  | 'OUTSIDE_SCHEDULE'
  | 'PERMISSION_EXPIRED'
  | 'PERMISSION_NOT_YET_VALID'
  | 'PERSON_INACTIVE'
  | 'PERSON_UNKNOWN'
  | 'CREDENTIAL_UNKNOWN'
  | 'CREDENTIAL_REVOKED'
  | 'CREDENTIAL_SUSPENDED'
  | 'CREDENTIAL_EXPIRED'
  | 'CREDENTIAL_NOT_YET_VALID'
  | 'APPLICATION_NOT_ENABLED'
  | 'TERMINAL_DISABLED'
  | 'SITE_INACTIVE'
  | 'COMPANY_INACTIVE'
  | 'OFFLINE_POLICY'

export type DecisionReason = KnownDecisionReason | (string & {})

/**
 * The outcome of evaluating one presentation.
 *
 * `reason` is present on a GRANT as well as a refusal. A trail that records why
 * somebody was turned away but not why somebody was admitted answers half the
 * question a security review asks.
 */
export interface AccessDecision {
  granted: boolean
  reason: DecisionReason
  person_id?: string
  person_name?: string
  external_id?: string
  credential_id?: string
  application?: string
  /** The rule that decided it. Empty on a deny-by-default, which IS the answer. */
  matched_permission?: string
  decided_at: string
}

/**
 * Ask the engine what it WOULD decide.
 *
 * The terminal is the path parameter, not a body field — an operator must not be
 * able to preview a decision at a terminal they are not scoped to. `at` makes a
 * schedule testable without waiting until Tuesday.
 *
 * THIS WRITES NO EVENT, by design. A preview that recorded a door event would
 * put a presentation that never happened into the trail an attendance report is
 * built from.
 */
export interface AccessEvaluationRequest {
  external_id: string
  credential_id?: string
  application?: string
  at?: string
}

// ---------------------------------------------------------------------------
// Field events
// ---------------------------------------------------------------------------

export type EventDecision = 'GRANTED' | 'DENIED' | 'RECORDED' | 'ERROR'

/**
 * One thing that happened in the field.
 *
 * TWO TIMES, AND THE DIFFERENCE IS NOT PEDANTRY. `occurred_at` is when somebody
 * stood at the terminal; `recorded_at` is when the platform heard about it. A
 * terminal buffers while offline, so an event that appears now may have happened
 * hours ago — and a console showing only one of them cannot explain that to the
 * person reading it.
 *
 * `occurred_at_trusted` says whether the terminal's clock was believable when it
 * stamped the first one. A terminal that has never reached NTP sends nothing and
 * the server stamps its own arrival; presenting that as a door time would be a
 * quiet lie, and the flag is what lets the console avoid telling it.
 *
 * `person_id` is EMPTY when the presentation matched nobody, which is a real
 * outcome and the more interesting half of a security trail —
 * `subject_external_id` keeps what the terminal actually read, so the attempt is
 * still traceable.
 */
export interface FieldEvent {
  id: string
  event_type: string
  application?: string
  decision: EventDecision
  reason?: string
  direction?: string
  site_name?: string
  device_serial?: string
  device_name?: string
  person_id?: string
  person_name?: string
  subject_external_id?: string
  credential_id?: string
  credential_type?: string
  payload?: unknown
  occurred_at: string
  recorded_at: string
  occurred_at_trusted: boolean
}

export interface EventPage {
  count: number
  total: number
  limit: number
  offset: number
  has_more: boolean
  events: FieldEvent[]
}

export interface EventQuery {
  event_type?: string
  decision?: EventDecision
  application?: string
  site_id?: string
  serial?: string
  external_id?: string
  person_id?: string
  direction?: string
  q?: string
  from?: string
  to?: string
  limit?: number
  offset?: number
}

// ---------------------------------------------------------------------------
// Audit trail
// ---------------------------------------------------------------------------

/**
 * One recorded operator action.
 *
 * The actor is a DENORMALISED email and role, not a reference: an operator
 * account can be deleted, and the record of what they did has to stay readable
 * afterwards. `actor_role` may be `PLATFORM`, which is not one of the operator
 * roles — it marks a change made by the vendor's platform administration surface
 * rather than by somebody inside the company, and a reader must be able to tell.
 *
 * `changes` is free-form and already redacted server-side. It is rendered as
 * data, never parsed for meaning.
 */
export interface AuditRecord {
  id: string
  action: string
  actor_email?: string
  actor_role?: string
  target_type?: string
  target_id?: string
  target_label?: string
  ip_address?: string
  changes?: unknown
  occurred_at: string
}

export interface AuditPage {
  count: number
  total: number
  limit: number
  offset: number
  has_more: boolean
  entries: AuditRecord[]
}

/**
 * Audit filters.
 *
 * `since`/`until` are RFC3339 instants. The server IGNORES an unparseable one
 * rather than rejecting the request, so a client must not rely on a 400 to tell
 * it a date was malformed — it validates before sending instead.
 */
export interface AuditQuery {
  action?: string
  target_type?: string
  actor?: string
  since?: string
  until?: string
  limit?: number
  offset?: number
}

// ---------------------------------------------------------------------------
// Firmware catalogue
// ---------------------------------------------------------------------------

/**
 * A build in the company's catalogue.
 *
 * `is_current` IS THE DEPLOYMENT TARGET, and it is the value every "is this
 * terminal outdated" report is measured against. The platform does not push
 * firmware — there is no OTA — so marking a build current changes what the fleet
 * is COMPARED TO and nothing else. A console that implied otherwise would have
 * operators believing a fleet had been updated.
 */
export interface FirmwareVersion {
  id: number
  public_id: string
  version: string
  device_type: string
  release_channel: string
  download_url?: string
  checksum_sha256?: string
  size_bytes?: number
  release_notes?: string
  is_mandatory: boolean
  is_current: boolean
  published_at?: string
  created_at: string
}

export interface FirmwareResponse {
  count: number
  firmware_versions: FirmwareVersion[]
}

export interface CreateFirmwareRequest {
  version: string
  device_type?: string
  release_channel?: string
  download_url?: string
  checksum_sha256?: string
  release_notes?: string
  is_mandatory?: boolean
}

// ---------------------------------------------------------------------------
// Applications
// ---------------------------------------------------------------------------

export interface ApplicationRequest {
  enabled?: boolean
  settings?: Record<string, unknown>
}
