import { ApiError, api } from '../api/client'
import { setCsrfToken } from '../api/csrf'
import type {
  CreateCompanyRequest,
  FirstOperatorRequest,
  FirstOperatorResponse,
  PlatformCompaniesResponse,
  PlatformCompany,
  PlatformSession,
  UpdateCompanyRequest,
} from './types'

/**
 * Every call to /api/v1/platform/*.
 *
 * SEPARATE FROM src/api/endpoints.ts ON PURPOSE. These authenticate a different
 * table with a different cookie, and mixing them into the tenant console's
 * endpoint module would make it possible to call one from the other by
 * autocomplete. The `scope: 'platform'` on every request below is what selects
 * the right CSRF token and keeps a 401 here from ending an operator's session.
 *
 * The whole file is deliberately short. There is no route that reads inside a
 * tenant, and if one ever appears here it should be obvious that it does not
 * belong.
 */

const SCOPE = { scope: 'platform' } as const

function adopt(session: PlatformSession): PlatformSession {
  setCsrfToken(session.csrf_token, 'platform')
  return session
}

export async function platformLogin(email: string, password: string): Promise<PlatformSession> {
  const session = await api.post<PlatformSession>(
    '/api/v1/platform/login',
    { email, password },
    SCOPE,
  )
  return adopt(session)
}

/**
 * Restores a platform session at boot, or reports that there is none.
 *
 * Returns null on 401 rather than throwing: "nobody is signed in here" is the
 * normal state, and it is especially normal on this surface — most visitors to
 * this installation are tenant operators who have never had a platform session
 * and never will.
 */
export async function fetchPlatformSession(signal?: AbortSignal): Promise<PlatformSession | null> {
  try {
    const session = await api.get<PlatformSession>('/api/v1/platform/me', {
      ...SCOPE,
      signal,
      expectUnauthenticated: true,
    })
    return adopt(session)
  } catch (error) {
    if (error instanceof ApiError && error.isUnauthenticated) {
      setCsrfToken(null, 'platform')
      return null
    }
    throw error
  }
}

export async function platformLogout(): Promise<void> {
  try {
    await api.post<void>('/api/v1/platform/logout', undefined, SCOPE)
  } finally {
    setCsrfToken(null, 'platform')
  }
}

export function fetchCompanies(): Promise<PlatformCompaniesResponse> {
  return api.get<PlatformCompaniesResponse>('/api/v1/platform/companies', SCOPE)
}

export function fetchCompany(companyId: string): Promise<PlatformCompany> {
  return api.get<PlatformCompany>(
    `/api/v1/platform/companies/${encodeURIComponent(companyId)}`,
    SCOPE,
  )
}

/**
 * Creates a tenant, EMPTY and with no operator.
 *
 * Issuing the first operator is a separate call, deliberately: an onboarding
 * that fails halfway then leaves a company somebody can see and finish, rather
 * than a half-built one inside a single request.
 */
export function createCompany(body: CreateCompanyRequest): Promise<PlatformCompany> {
  return api.post<PlatformCompany>('/api/v1/platform/companies', body, SCOPE)
}

export function updateCompany(
  companyId: string,
  body: UpdateCompanyRequest,
): Promise<PlatformCompany> {
  return api.put<PlatformCompany>(
    `/api/v1/platform/companies/${encodeURIComponent(companyId)}`,
    body,
    SCOPE,
  )
}

/**
 * Mints a company's first OWNER. ONLY INTO A COMPANY THAT HAS NONE.
 *
 * Refused with 409 otherwise, by a query predicate rather than a check. That is
 * the rule that keeps this from being a standing back door into every customer:
 * once a tenant has an owner, every further account is created by them through
 * their own console.
 *
 * Omitting the password issues an invitation instead, which is the path the
 * console offers — it is what stops the vendor ever knowing a customer's owner
 * password.
 */
export function createFirstOperator(
  companyId: string,
  body: FirstOperatorRequest,
): Promise<FirstOperatorResponse> {
  return api.post<FirstOperatorResponse>(
    `/api/v1/platform/companies/${encodeURIComponent(companyId)}/operators`,
    body,
    SCOPE,
  )
}
