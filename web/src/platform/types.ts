import type { CredentialToken, Role } from '../api/types'

/**
 * The platform-administration surface.
 *
 * A SEPARATE CREDENTIAL CLASS, NOT A ROLE ABOVE OWNER, and these types live
 * apart from the tenant console's for the same reason the server keeps the
 * identities apart. Every console query is scoped to exactly one company, and
 * that single-tenant-per-call contract is what makes the tenancy boundary
 * checkable at all. An operator who could reach several companies would dissolve
 * it everywhere; a different identity that reaches `companies` and nothing
 * inside them does not touch it.
 *
 * WHAT THIS SURFACE CANNOT SEE, and what the console must therefore never try to
 * show: a tenant's people, credentials, events, terminals or site keys. There is
 * no route and no query that could serve one. The counts below stop at
 * cardinality — how many sites, terminals, people, operators — which is what
 * "is this tenant healthy and how big is it" needs and the whole of what
 * onboarding and support have a claim to.
 *
 * A support credential able to read every customer's biometric roster would be
 * the most valuable secret on the installation. The safest way not to leak it is
 * to be unable to load it, and this file is where a well-meaning addition would
 * first break that.
 */

export interface PlatformAdmin {
  id: string
  email: string
  full_name: string
  active: boolean
  last_login_at?: string
  created_at?: string
}

export interface PlatformSession {
  admin: PlatformAdmin
  csrf_token: string
  session_expires_at: string
  session_expires_in_seconds: number
}

/**
 * A tenant as the platform sees it.
 *
 * `event_retention_days` and `audit_retention_days` are NULLABLE and null means
 * "keep for ever", which is the default. Rendered as "indefinite" rather than as
 * a number nobody chose — showing 0 or a blank would read as a policy that had
 * been set.
 */
export interface PlatformCompany {
  id: string
  name: string
  slug: string
  contact_email?: string
  timezone: string
  active: boolean
  event_retention_days: number | null
  audit_retention_days: number | null
  site_count: number
  terminal_count: number
  person_count: number
  operator_count: number
  created_at: string
}

export interface PlatformCompaniesResponse {
  count: number
  companies: PlatformCompany[]
}

/**
 * Create a tenant.
 *
 * The slug is DERIVED FROM THE NAME when absent, server-side. Refusing
 * "Acme Logistics (UK)" to enforce a format the platform invented is less useful
 * than turning it into "acme-logistics-uk", so the console offers the derivation
 * as a preview rather than as a required field.
 */
export interface CreateCompanyRequest {
  name: string
  slug?: string
  contact_email?: string
  timezone?: string
}

/**
 * Update a tenant. Every field optional; only what is sent is applied.
 *
 * THERE IS NO SLUG FIELD, deliberately: it appears in the bootstrap environment
 * and in operator-facing URLs, so renaming it would silently break whatever
 * refers to the company by it. The display name is what a rebrand needs.
 *
 * `active: false` SUSPENDS THE TENANT. Every operator session in it stops
 * resolving on the next request. Nothing is deleted, and it is audited as its
 * own action rather than as a field change.
 */
export interface UpdateCompanyRequest {
  name?: string
  contact_email?: string
  timezone?: string
  active?: boolean
  deactivated_reason?: string
  event_retention_days?: number
  audit_retention_days?: number
}

/** Mint a company's first OWNER. Omitting the password issues an invitation. */
export interface FirstOperatorRequest {
  email: string
  full_name?: string
  password?: string
}

/**
 * What creating the first operator answers with.
 *
 * `invitation` is present only when no password was supplied — which is the
 * preferred path, and the one that means the VENDOR NEVER KNOWS THE CUSTOMER'S
 * OWNER PASSWORD.
 */
export interface FirstOperatorResponse {
  operator: {
    id: string
    email: string
    full_name: string
    role: Role
  }
  must_change_password: boolean
  invitation?: CredentialToken
  delivery?: string
}
