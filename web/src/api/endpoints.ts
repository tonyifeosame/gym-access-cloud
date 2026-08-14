import { ApiError, api } from './client'
import { setCsrfToken } from './csrf'
import type { ApplicationsResponse, CompanyDetail, Session, SitesResponse } from './types'

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

export function fetchSites(): Promise<SitesResponse> {
  return api.get<SitesResponse>('/api/v1/console/sites')
}

export function fetchApplications(): Promise<ApplicationsResponse> {
  return api.get<ApplicationsResponse>('/api/v1/console/applications')
}
