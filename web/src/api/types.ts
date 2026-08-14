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
  timezone: string
  active: boolean
  /** Live terminals at this site. */
  terminal_count: number
  created_at: string
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
