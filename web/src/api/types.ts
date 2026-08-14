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

export interface CreateOperatorRequest {
  email: string
  full_name: string
  password: string
  role: Role
  site_ids?: string[]
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
// Applications
// ---------------------------------------------------------------------------

export interface ApplicationRequest {
  enabled?: boolean
  settings?: Record<string, unknown>
}
